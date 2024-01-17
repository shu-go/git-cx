package main

import (
	"time"

	"github.com/shu-go/orderedmap"
)

type CommitType struct {
	Desc  string `json:"description,omitempty"`
	Emoji string `json:"emoji,omitempty"`
}

type Rule struct {
	Header           string `json:"headerFormat"`
	HeaderFormatHint string `json:"headerFormatHint"`

	Types *orderedmap.OrderedMap[string, CommitType] `json:"types"` //map[string]CommitType

	DenyEmptyType bool `json:"denyEmptyType"`
	DenyAdlibType bool `json:"denyAdlibType"`

	UseBreakingChange bool `json:"useBreakingChange"`
}

type Scopes map[string]time.Time
