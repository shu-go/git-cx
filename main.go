package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	prompt "github.com/c-bata/go-prompt"
	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/plumbing/format/config"
	"github.com/kyokomi/emoji/v2"
	"github.com/shu-go/gli"
	"github.com/shu-go/orderedmap"
)

const (
	userConfigFolder = "git-cx"

	defaultRuleFileName   = ".cx.json"
	defaultScopesFileName = ".scope-history.json"

	configSection      = "cx"
	configRule         = "rule"
	configScopeHistory = "scopes"
)

type globalCmd struct {
	repository *git.Repository

	rule *Rule

	scopesFileName        string
	scopes                Scopes
	writeBackScopeHisotry bool

	All bool `cli:"all,a" help:"commit all changed files"`

	Debug bool `cli:"debug" default:"false" help:"do not commit, do output to stdout"`

	Gen genCmd `cli:"generate,gen" help:"generate rule json file"`
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
	fmt.Fprintln(os.Stderr, wd)

	if !c.Debug && c.All {
		st, err := wt.Status()
		if err != nil {
			return err
		}
		for f, s := range st {
			//println(f, s.Worktree, s.Staging)
			switch s.Worktree {
			case git.Modified, git.Added, git.Deleted, git.Renamed, git.Copied, git.UpdatedButUnmerged:
				wt.Add(f)
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

	if err := c.prepare(wt.Filesystem.Root()); err != nil {
		return err
	}

	msg := c.buildupCommitMessage()

	if c.Debug {
		fmt.Println("----------")
		fmt.Println(msg)
		return nil
	}

	_, err = wt.Commit(msg, &git.CommitOptions{})
	if err != nil {
		return err
	}

	return nil
}

func (c *globalCmd) prepare(rootDir string) error {
	c.rule = c.readRuleFile(rootDir)

	// scope history

	c.scopes, c.scopesFileName, c.writeBackScopeHisotry = c.readScopesFile(rootDir)
	if c.scopes == nil {
		c.scopes = make(Scopes)
	}

	return nil
}

func (c globalCmd) defaultRule() Rule {
	return Rule{
		Types:             c.defaultCommitTypes(),
		DenyEmptyType:     false,
		DenyAdlibType:     false,
		UseBreakingChange: false,
		HeaderFormat:      "{{.type}}{{.scope_with_parens}}{{.bang}}: {{.emoji_unicode}}{{.description}}",
		HeaderFormatHint:  ".type, .scope, .scope_with_parens, .bang(if BREAKING CHANGE), .emoji, .emoji_unicode, .description",
	}
}

func (c globalCmd) defaultCommitTypes() *orderedmap.OrderedMap[string, CommitType] {
	ct := orderedmap.New[string, CommitType]()
	ct.Set("# comment1", commitTypeAsOM(
		"comment starts with #",
		"",
	))
	ct.Set("# comment2", commitTypeAsOM(
		"This default definition is from https://github.com/commitizen/conventional-commit-types/blob/master/index.json",
		"",
	))

	ct.Set("feat", commitTypeAsOM(
		"A new feature",
		":sparkles:",
	))
	ct.Set("fix", commitTypeAsOM(
		"A bug fix",
		":bug:",
	))
	ct.Set("docs", commitTypeAsOM(
		"Documentation only changes",
		":memo:",
	))
	ct.Set("style", commitTypeAsOM(
		"Changes that do not affect the meaning of the code",
		":gem:",
	))
	ct.Set("refactor", commitTypeAsOM(
		"A code change that neither fixes a bug nor adds a feature",
		":recycle:",
	))
	ct.Set("perf", commitTypeAsOM(
		"A code change that improves performance",
		":zap:",
	))
	ct.Set("test", commitTypeAsOM(
		"Adding missing tests or correcting existing tests",
		":test_tube:",
	))
	ct.Set("build", commitTypeAsOM(
		"Changes that affect the build system or external dependencies",
		":package:",
	))
	ct.Set("ci", commitTypeAsOM(
		"Changes to our CI configuration files and scripts",
		":hammer:",
	))
	ct.Set("chore", commitTypeAsOM(
		"Other changes that don't modify src or test files",
		"",
	))
	ct.Set("revert", commitTypeAsOM(
		"Reverts a previous commit",
		":rewind:",
	))
	return ct
}

func (c globalCmd) readRuleFile(rootDir string) *Rule {
	// config
	if cfg := c.getConfig(configRule); cfg != nil {
		if r, err := tryReadRuleFile(filepath.Join(rootDir, *cfg)); err == nil {
			return r
		}
	}

	// rootDir
	if r, err := tryReadRuleFile(filepath.Join(rootDir, defaultRuleFileName)); err == nil {
		return r
	}

	// user config dir
	if cp, err := os.UserConfigDir(); err == nil {
		if r, err := tryReadRuleFile(filepath.Join(cp, userConfigFolder, defaultRuleFileName)); err == nil {
			return r
		}
	}

	// executable dir
	if ep, err := os.Executable(); err != nil {
		ed, _ := filepath.Split(ep)
		if r, err := tryReadRuleFile(filepath.Join(ed, defaultRuleFileName)); err == nil {
			return r
		}
	}

	r := c.defaultRule()
	return &r
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
	if err := json.Unmarshal(content, &r); err != nil {
		return nil, err
	}

	return &r, nil
}

func (c globalCmd) readScopesFile(rootDir string) (Scopes, string, bool) {
	// config
	if cfg := c.getConfig(configScopeHistory); cfg != nil {
		filename := filepath.Join(rootDir, *cfg)
		if sc, err := tryReadScopesFile(filename); err == nil {
			return sc, filename, true
		}
	}

	// rootDir
	filename := filepath.Join(rootDir, defaultScopesFileName)
	if sc, err := tryReadScopesFile(filename); err == nil {
		return sc, filename, true
	}

	// user config dir
	if cp, err := os.UserConfigDir(); err == nil {
		filename := filepath.Join(cp, userConfigFolder, defaultScopesFileName)
		if sc, err := tryReadScopesFile(filename); err == nil {
			return sc, filename, false
		}
	}

	// executable dir
	if ep, err := os.Executable(); err == nil {
		ed, _ := filepath.Split(ep)
		filename := filepath.Join(ed, defaultScopesFileName)
		if sc, err := tryReadScopesFile(filename); err == nil {
			return sc, filename, false
		}
	}

	return nil, "", false
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
	if err = json.Unmarshal(content, &sc); err != nil {
		return nil, err
	}

	return sc, nil
}

func (c globalCmd) getConfig(key string) *string {
	config, err := c.repository.Config()
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

	if scope != "" && c.writeBackScopeHisotry {
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

		content, _ := json.MarshalIndent(outscope, "", "  ")

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
		templ.Execute(&buf, map[string]string{
			"type":              typ,
			"scope":             scope,
			"scope_with_parens": scopeWithParens,
			"bang":              bang,
			"emoji":             emoji,
			"emoji_unicode":     emojiUnicode,
			"description":       desc,
		})
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
			Description: c.emojiOf(k, true) + typ.Desc,
		}
		items = append(items, item)
	}

	typeCompleter := func(d prompt.Document) []prompt.Suggest {
		//return prompt.FilterHasPrefix(items, d.GetWordBeforeCursor(), true)
		return filterSuggestions(items, d.GetWordBeforeCursor(), true, fuzzyMatch)
	}

	for typ == "" {
		typ = prompt.Input("Type: ", typeCompleter, prompt.OptionShowCompletionAtStart())
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
	scopeCompleter := func(d prompt.Document) []prompt.Suggest {
		return prompt.FilterHasPrefix(items, d.GetWordBeforeCursor(), true)
	}
	scope = prompt.Input(
		"Scope: ",
		scopeCompleter,
		prompt.OptionShowCompletionAtStart(),
	)

	return scope
}

func (c globalCmd) promptDesc() string {
	var desc string

	descCompleter := func(d prompt.Document) []prompt.Suggest {
		return prompt.FilterHasPrefix(nil, d.GetWordBeforeCursor(), true)
	}

	desc = prompt.Input("Description: ", descCompleter)
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
		bcCompleter := func(d prompt.Document) []prompt.Suggest {
			return prompt.FilterHasPrefix(nil, d.GetWordBeforeCursor(), true)
		}
		breakingChange = prompt.Input("BREAKING CHANGE: ", bcCompleter)
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
(edit .cx.json)
git cx

# record and complete scope history
(gitconfig: [cx] scopes=.scopes.json)`
	app.Copyright = "(C) 2024 Shuhei Kubota"
	app.SuppressErrorOutput = true
	if err := app.Run(os.Args); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
