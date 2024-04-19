package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type genCmd struct {
}

func (c genCmd) Run(g globalCmd, args []string) error {
	filename := defaultRuleFileName + ".json"
	if len(args) > 0 {
		filename = args[0]
	}

	filename, err := filepath.Abs(filename)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "output: %v\n", filename)

	rule := defaultRule()

	content, err := json.MarshalIndent(rule, "", "  ")
	if err != nil {
		return err
	}

	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(string(content))
	if err != nil {
		return err
	}

	return nil
}
