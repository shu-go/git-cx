package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	prompt "github.com/elk-language/go-prompt"
	pstrings "github.com/elk-language/go-prompt/strings"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/plumbing/format/config"

	"github.com/kyokomi/emoji/v2"
	"github.com/shu-go/findcfg"
	"github.com/shu-go/gli"
	"github.com/shu-go/orderedmap"
	"gopkg.in/yaml.v3"
)

const (
	userConfigFolder = "git-cx"

	defaultRuleFileName   = ".cx"
	defaultScopesFileName = ".scope-history"

	configSection      = "cx"
	configRule         = "rule"
	configScopeHistory = "scopes"
)

type globalCmd struct {
	repository *git.Repository

	rule *Rule

	scopesFileName string
	scopes         Scopes

	All bool `cli:"all,a" help:"commit all changed files"`

	Debug bool `cli:"debug" default:"false" help:"do not commit, do output to stdout"`

	Gen genCmd `cli:"generate,gen" help:"generate rule file"`
}

func (c globalCmd) Run() error {
	repos, err := git.PlainOpenWithOptions(".", &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return err
	}
	c.repository = repos

	wt, err := repos.Worktree()
	if err != nil {
		return err
	}

	wd, err := os.Getwd()
	if err != nil {
		return err
	}

	if c.Debug {
		fmt.Fprintln(os.Stderr, wd)
	}

	if !c.Debug && c.All {
		st, err := wt.Status()
		if err != nil {
			return err
		}
		for f, s := range st {
			//println(f, s.Worktree, s.Staging)
			switch s.Worktree {
			case git.Modified, git.Added, git.Deleted, git.Renamed, git.Copied, git.UpdatedButUnmerged:
				if _, err := wt.Add(f); err != nil {
					return fmt.Errorf("try git gc: adding %s: %w", s.Worktree, f, err)
				}
			default:
				//nop
			}
		}
	}

	st, err := wt.Status()
	if err != nil {
		return err
	}
	staged := false
	for _, s := range st {
		staged = staged || (s.Staging != git.Unmodified && s.Staging != git.Untracked)
	}
	if !staged {
		fmt.Fprintln(os.Stderr, "no changes")

		if !c.Debug {
			return nil
		}
	}

	if err := c.prepare(repos); err != nil {
		return err
	}

	msg := c.buildupCommitMessage()

	if c.Debug {
		fmt.Println("----------")
		fmt.Println(msg)
		return nil
	}

	f, err := os.CreateTemp("", "")
	if err != nil {
		return err
	}
	_, err = f.WriteString(msg)
	if err != nil {
		f.Close()
		return err
	}
	f.Close()

	cmd := exec.Command("git", "commit", "-F", f.Name())
	err = cmd.Run()
	os.Remove(f.Name())
	if err != nil {
		return err
	}

	return nil
}

func (c *globalCmd) prepare(repos *git.Repository) error {
	c.rule, _ = readRuleFile(repos)

	// scope history

	c.scopes, c.scopesFileName = readScopesFile(repos)
	if c.scopes == nil {
		c.scopes = make(Scopes)
	}

	return nil
}

func defaultRule(emoji bool) Rule {
	return Rule{
		Types:             defaultCommitTypes(emoji),
		DenyEmptyType:     false,
		DenyAdlibType:     false,
		UseBreakingChange: false,
		HeaderFormat:      "{{.type}}{{.scope_with_parens}}{{.bang}}: {{.emoji_unicode}}{{.description}}",
		HeaderFormatHint:  ".type, .scope, .scope_with_parens, .bang(if BREAKING CHANGE), .emoji, .emoji_unicode, .description",
	}
}

func defaultCommitTypes(emoji bool) *orderedmap.OrderedMap[string, CommitType] {
	iif := func(cond bool, t, f string) string {
		if cond {
			return t
		}
		return f
	}

	ct := orderedmap.New[string, CommitType]()
	ct.Set("# comment1", commitTypeAsOM(
		"comment starts with #",
		"",
	))
	ct.Set("# comment2", commitTypeAsOM(
		"This default definition is from https://github.com/angular/angular/blob/main/CONTRIBUTING.md#-commit-message-guidelines",
		"",
	))

	ct.Set("feat", commitTypeAsOM(
		"A new feature",
		iif(emoji, ":sparkles:", ""),
	))
	ct.Set("fix", commitTypeAsOM(
		"A bug fix",
		iif(emoji, ":bug:", ""),
	))
	ct.Set("docs", commitTypeAsOM(
		"Documentation only changes",
		iif(emoji, ":memo:", ""),
	))
	ct.Set("refactor", commitTypeAsOM(
		"A code change that neither fixes a bug nor adds a feature",
		iif(emoji, ":recycle:", ""),
	))
	ct.Set("perf", commitTypeAsOM(
		"A code change that improves performance",
		iif(emoji, ":zap:", ""),
	))
	ct.Set("test", commitTypeAsOM(
		"Adding missing tests or correcting existing tests",
		iif(emoji, ":test_tube:", ""),
	))
	ct.Set("build", commitTypeAsOM(
		"Changes that affect the build system or external dependencies",
		iif(emoji, ":package:", ""),
	))
	ct.Set("ci", commitTypeAsOM(
		"Changes to our CI configuration files and scripts",
		iif(emoji, ":hammer:", ""),
	))
	ct.Set("revert", commitTypeAsOM(
		"Reverts a previous commit",
		iif(emoji, ":rewind:", ""),
	))
	return ct
}

func readRuleFile(repos *git.Repository) (*Rule, string) {
	var rootDir string
	if wt, err := repos.Worktree(); err == nil {
		rootDir = wt.Filesystem.Root()
	}

	var exactPath string
	if rootDir != "" {
		// config
		if cfg := getGitConfig(repos, configRule); cfg != nil {
			exactPath = filepath.Join(rootDir, *cfg)
		}
	}

	finder := findcfg.New(
		findcfg.Name(defaultRuleFileName),
		findcfg.ExactPath(exactPath),
		findcfg.YAML(),
		findcfg.JSON(),
		findcfg.Dir(rootDir),
		findcfg.UserConfigDir(userConfigFolder),
		findcfg.ExecutableDir(),
	)
	found := finder.Find()
	if found != nil {
		if r, err := tryReadRuleFile(found.Path); err == nil {
			return r, found.Path
		}
	}

	r := defaultRule(false)
	return &r, finder.FallbackPath()
}

func commitTypeAsOM(desc string, emoji string) CommitType {
	return CommitType{
		Desc:  desc,
		Emoji: emoji,
	}
}

func tryReadRuleFile(filename string) (*Rule, error) {
	if s, err := os.Stat(filename); err != nil || s.IsDir() {
		return nil, err
	}

	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	r := Rule{
		Types: orderedmap.New[string, CommitType](),
	}

	if in(filepath.Ext(filename), ".yaml", ".yml") {
		if err := yaml.Unmarshal(content, &r); err != nil {
			return nil, err
		}
		return &r, nil
	}
	if in(filepath.Ext(filename), ".json") {
		if err := json.Unmarshal(content, &r); err != nil {
			return nil, err
		}
		return &r, nil
	}
	if err := yaml.Unmarshal(content, &r); err != nil {
		if err := json.Unmarshal(content, &r); err != nil {
			return nil, err
		}
		return &r, nil
	}
	return &r, nil
}

func readScopesFile(repos *git.Repository) (scopes Scopes, fileName string) {
	var rootDir string
	if wt, err := repos.Worktree(); err == nil {
		rootDir = wt.Filesystem.Root()
	}

	var exactPath string
	if rootDir != "" {
		// config
		if cfg := getGitConfig(repos, configScopeHistory); cfg != nil {
			exactPath = filepath.Join(rootDir, *cfg)
		}
	}

	finder := findcfg.New(
		findcfg.Name(defaultScopesFileName),
		findcfg.ExactPath(exactPath),
		findcfg.YAML(),
		findcfg.JSON(),
		findcfg.Dir(rootDir),
		findcfg.UserConfigDir(userConfigFolder),
		findcfg.ExecutableDir(),
	)
	found := finder.Find()
	if found != nil {
		if sc, err := tryReadScopesFile(found.Path); err == nil {
			return sc, exactPath
		}
		return nil, finder.FallbackPath()
	}

	return nil, finder.FallbackPath()
}

func tryReadScopesFile(filename string) (Scopes, error) {
	if s, err := os.Stat(filename); err != nil || s.IsDir() {
		return nil, err
	}

	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	sc := make(Scopes)

	if in(filepath.Ext(filename), ".yaml", ".yml") {
		if err = yaml.Unmarshal(content, &sc); err != nil {
			return nil, err
		}
		return sc, nil
	}
	if in(filepath.Ext(filename), ".json") {
		if err = json.Unmarshal(content, &sc); err != nil {
			return nil, err
		}
		return sc, nil
	}
	if err = yaml.Unmarshal(content, &sc); err != nil {
		if err = json.Unmarshal(content, &sc); err != nil {
			return nil, err
		}
		return sc, nil
	}
	return sc, nil
}

func getGitConfig(repos *git.Repository, key string) *string {
	config, err := repos.Config()
	if err != nil {
		return nil
	}

	var ss *gitconfig.Section
	var found bool
	for _, s := range config.Raw.Sections {
		if s.Name == configSection {
			found = true
			ss = s
		}
	}
	if !found {
		return nil
	}

	if ctp := ss.Options.Get(key); ctp != "" {
		return &ctp
	}
	return nil
}

func (c globalCmd) buildupCommitMessage() string {
	typ := c.promptType()
	scope := c.promptScope()
	desc := c.promptDesc()
	body := c.promptBody()
	breakingChange := c.promptBreakingChange()

	// write back scope history

	if scope != "" && c.scopesFileName != "" {
		c.scopes[scope] = time.Now()

		type tmpscope struct {
			scope string
			ts    time.Time
		}
		sclist := []tmpscope{}
		for k, v := range c.scopes {
			sclist = append(sclist, tmpscope{
				scope: k,
				ts:    v,
			})
		}
		sort.Slice(sclist, func(i, j int) bool {
			return sclist[i].ts.After(sclist[j].ts)
		})

		outscope := orderedmap.New[string, time.Time]()
		for _, s := range sclist {
			outscope.Set(s.scope, s.ts)
		}

		var content []byte
		if in(filepath.Ext(c.scopesFileName), ".json") {
			content, _ = json.MarshalIndent(outscope, "", "  ")
		} else {
			content, _ = yaml.Marshal(outscope)
		}

		if file, err := os.Create(c.scopesFileName); err == nil {
			_, err = file.WriteString(string(content))
			if err != nil {
				fmt.Fprintf(os.Stderr, "WARNING: write scopes: %v\n", err)
			}
			file.Close()
		}
	}

	//---

	var header string
	{
		emoji := c.emojiOf(typ, false)
		emojiUnicode := c.emojiOf(typ, true)

		var scopeWithParens string
		if scope != "" {
			scopeWithParens = "(" + scope + ")"
		}

		var bang string
		if breakingChange != "" {
			bang = "!"
		}

		templ := template.Must(template.New("").Parse(c.rule.HeaderFormat))
		buf := bytes.Buffer{}
		err := templ.Execute(&buf, map[string]string{
			"type":              typ,
			"scope":             scope,
			"scope_with_parens": scopeWithParens,
			"bang":              bang,
			"emoji":             emoji,
			"emoji_unicode":     emojiUnicode,
			"description":       desc,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v: %q\n", err, c.rule.HeaderFormat)
			buf.WriteString(typ)
			buf.WriteString(scopeWithParens)
			buf.WriteString(bang)
			buf.WriteString(": ")
			buf.WriteString(desc)
		}
		header = buf.String()
	}
	msg := header

	if body != "" {
		msg += "\n\n" + body
	}
	if breakingChange != "" {
		msg += "\nBREAKING CHANGE: " + breakingChange
	}

	return msg
}

func (c globalCmd) promptType() string {
	var typ string

	items := make([]prompt.Suggest, 0, len(c.rule.Types.Keys()))

	for _, k := range c.rule.Types.Keys() {
		if strings.HasPrefix(k, "#") {
			continue
		}

		typ, ok := c.rule.Types.Get(k)
		if !ok || typ.Desc == "" {
			continue
		}

		item := prompt.Suggest{
			Text:        k,
			Description: c.emojiOf(k, true) + " " + typ.Desc,
		}
		items = append(items, item)
	}

	typeCompleter := func(in prompt.Document) ([]prompt.Suggest, pstrings.RuneNumber, pstrings.RuneNumber) {
		endIndex := in.CurrentRuneIndex()
		w := in.GetWordBeforeCursor()
		startIndex := endIndex - pstrings.RuneCountInString(w)

		return prompt.FilterHasPrefix(items, w, true), startIndex, endIndex
	}

	for typ == "" {
		typ = prompt.Input(prompt.WithPrefix("Type: "), prompt.WithCompleter(typeCompleter), prompt.WithShowCompletionAtStart())
		if typ == "" && c.rule.DenyEmptyType {
			fmt.Fprintln(os.Stderr, "type is required")
		}
		if typ != "" && c.rule.DenyAdlibType {
			_, found := c.rule.Types.Get(typ)
			if !found {
				fmt.Fprintln(os.Stderr, "ad-lib type is not allowed")
				typ = ""
			}
		}
	}

	return typ
}

func (c globalCmd) promptScope() string {
	var scope string

	items := make([]prompt.Suggest, 0, 8)

	for s, t := range c.scopes {
		item := prompt.Suggest{
			Text:        s,
			Description: t.Local().Format(time.RFC3339),
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Description > items[j].Description
	})
	// timestamps are not shown
	for i := range items {
		items[i].Description = ""
	}
	scopeCompleter := func(in prompt.Document) ([]prompt.Suggest, pstrings.RuneNumber, pstrings.RuneNumber) {
		endIndex := in.CurrentRuneIndex()
		w := in.GetWordBeforeCursor()
		startIndex := endIndex - pstrings.RuneCountInString(w)

		return prompt.FilterHasPrefix(items, w, true), startIndex, endIndex
	}
	scope = prompt.Input(
		prompt.WithPrefix("Scope: "),
		prompt.WithCompleter(scopeCompleter),
		prompt.WithShowCompletionAtStart(),
	)

	return scope
}

func (c globalCmd) promptDesc() string {
	var desc string

	descCompleter := func(in prompt.Document) ([]prompt.Suggest, pstrings.RuneNumber, pstrings.RuneNumber) {
		endIndex := in.CurrentRuneIndex()
		w := in.GetWordBeforeCursor()
		startIndex := endIndex - pstrings.RuneCountInString(w)

		return prompt.FilterHasPrefix(nil, w, true), startIndex, endIndex
	}

	desc = prompt.Input(prompt.WithPrefix("Description: "), prompt.WithCompleter(descCompleter))
	desc = strings.TrimSpace(desc)
	if desc == "" {
		fmt.Fprintln(os.Stderr, "description required")
	}

	return desc
}

func (c globalCmd) promptBody() string {
	var body string

	fmt.Println("Body: (Enter 2 empty lines to finish)")

	prevEmpty := false
	buf := bufio.NewReader(os.Stdin)
	for {
		linebyte, _, err := buf.ReadLine()
		if err != nil {
			break
		}

		line := strings.TrimSpace(string(linebyte))

		if line == "" {
			if prevEmpty {
				break
			}
			prevEmpty = true
			//continue
		} else {
			prevEmpty = false
		}

		if body != "" {
			body += "\n"
		}
		body += line
	}

	return body
}

// copied from github.com/c-bata/go-prompt/filter.go
func fuzzyMatch(s, sub string) bool {
	sChars := []rune(s)
	sIdx := 0

	// https://staticcheck.io/docs/checks#S1029
	for _, c := range sub {
		found := false
		for ; sIdx < len(sChars); sIdx++ {
			if sChars[sIdx] == c {
				found = true
				sIdx++
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (c globalCmd) promptBreakingChange() string {
	var breakingChange string

	if c.rule.UseBreakingChange {
		bcCompleter := func(in prompt.Document) ([]prompt.Suggest, pstrings.RuneNumber, pstrings.RuneNumber) {
			endIndex := in.CurrentRuneIndex()
			w := in.GetWordBeforeCursor()
			startIndex := endIndex - pstrings.RuneCountInString(w)

			return prompt.FilterHasPrefix(nil, w, true), startIndex, endIndex
		}
		breakingChange = prompt.Input(prompt.WithPrefix("BREAKING CHANGE: "), prompt.WithCompleter(bcCompleter))
		breakingChange = strings.TrimSpace(breakingChange)
	}

	return breakingChange
}

func (c globalCmd) emojiOf(typ string, emojize bool) string {
	if ct, found := c.rule.Types.Get(typ); found {
		e := ct.Emoji
		if emojize {
			e = strings.TrimSpace(emoji.Emojize(e))
		}
		return e
	}

	return ""
}

func filterSuggestions(suggestions []prompt.Suggest, sub string, ignoreCase bool, function func(string, string) bool) []prompt.Suggest {
	if sub == "" {
		return suggestions
	}
	if ignoreCase {
		sub = strings.ToUpper(sub)
	}

	ret := make([]prompt.Suggest, 0, len(suggestions))
	for i := range suggestions {
		c := suggestions[i].Text
		d := suggestions[i].Description
		if ignoreCase {
			c = strings.ToUpper(c)
			d = strings.ToUpper(d)
		}
		if function(c, sub) || function(d, sub) {
			ret = append(ret, suggestions[i])
		}
	}
	return ret
}

// Version is app version
var Version string

func main() {
	rule, scope := getPathToHelp()
	if rule != "" {
		rule = "\nrule: " + rule + "\n"
	}
	if scope != "" {
		scope = "scope: " + scope + "\n"
	}

	app := gli.NewWith(&globalCmd{})
	app.Name = "git-cx"
	app.Desc = "A conventional commits tool"
	app.Version = Version
	app.Usage = `
# prepare
# Put git-cx to PATH.

# basic usage
git cx

# customize
git cx gen
(edit .cx.yaml)
git cx
` + rule + scope + `

# record and complete scope history
(gitconfig: [cx] scopes=.scopes.yaml)`
	app.Copyright = "(C) 2024 Shuhei Kubota"
	app.SuppressErrorOutput = true
	if err := app.Run(os.Args); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func getPathToHelp() (rule string, scope string) {
	repos, err := git.PlainOpenWithOptions(".", &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return "", ""
	}

	_, rule = readRuleFile(repos)
	_, scope = readScopesFile(repos)

	return rule, scope
}

func in(s string, choices ...string) bool {
	if len(choices) == 0 {
		return false
	}

	for i := 0; i < len(choices); i++ {
		if strings.EqualFold(s, choices[i]) {
			return true
		}
	}

	return false
}
