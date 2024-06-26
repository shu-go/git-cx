A conventional commits tool

[![Go Report Card](https://goreportcard.com/badge/github.com/shu-go/git-cx)](https://goreportcard.com/report/github.com/shu-go/git-cx)
![MIT License](https://img.shields.io/badge/License-MIT-blue)

# Install
## GitHub Releases

You can download binaries from [GitHub Releases Page](https://github.com/shu-go/git-cx/releases).

## go install

```
go install github.com/shu-go/git-cx@latest
```

## Put git-cx to PATH

# Usage

## Basic

```
git cx
```

## Customize commit types and rules

First, generate a rule file.

```
git cx gen

# The default name is .cx.yaml.

# You can give it a name
git cx gen myrule.yaml
```

Then, edit the file.

```yaml
headerformat: '{{.type}}{{.scope_with_parens}}{{.bang}}: {{.emoji_unicode}}{{.description}}'
headerformathint: .type, .scope, .scope_with_parens, .bang(if BREAKING CHANGE), .emoji, .emoji_unicode, .description
types:
    '# comment1':
        desc: 'comment starts with #'
        emoji: ""
    feat:
        desc: A new feature
        emoji: ':sparkles:'
    fix:
        desc: A bug fix
        emoji: ':bug:'
    :
  },
denyemptytype: false
denyadlibtype: false
usebreakingchange: false
```

## Record and complete scope history

Edit your gitconfig (I recommend to use [shu-go/git-konfig](https://github.com/shu-go/git-konfig))

```
[cx]
  scopes = myscopes.yaml
```

## An example

```
git cx --debug

Type: feat
Scope: hoge
Description: new feature hoge!
Body: (Enter 2 empty lines to finish)
dummy text

----------
feat(hoge): ✨new feature hoge!

dummy text
```

# The search order

1. gitconfig ([cx] rule={PATH})
2. current worktree root
3. config directory
   - {CONFIG_DIR}/git-cx/.cx.yaml
   - Windows: %appdata%\git-cx\.cx.yaml
   - (see https://cs.opensource.google/go/go/+/go1.17.3:src/os/file.go;l=457)
4. exe dir
   - .cx.yaml
   - Place the yaml in the same location as the executable.

<!-- vim: set et ft=markdown sts=4 sw=4 ts=4 tw=0 : -->
