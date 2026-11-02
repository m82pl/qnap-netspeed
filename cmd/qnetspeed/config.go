package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/m82pl/qnap-netspeed/internal/config"
)

const configUsage = "Usage: qnetspeed config show | check | get KEY | set KEY VALUE\n"

func (a *app) config(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.stderr, configUsage)
		return exitUsage
	}
	want := map[string]int{"show": 1, "check": 1, "get": 2, "set": 3}
	n, ok := want[args[0]]
	if !ok || len(args) != n {
		fmt.Fprint(a.stderr, configUsage)
		return exitUsage
	}
	switch args[0] {
	case "show":
		c, issues, code := a.loadConfig(false)
		a.printIssues(issues)
		a.stdout.Write(c.Show())
		return code
	case "check":
		_, issues, code := a.loadConfig(true)
		a.printIssues(issues)
		if code == exitOK {
			fmt.Fprintf(a.stdout, "config OK: %s\n", a.layout.ConfigFile())
		}
		return code
	case "get":
		if !config.Known(args[1]) {
			fmt.Fprintf(a.stderr, "qnetspeed config: unknown key %s\n", args[1])
			return exitUsage
		}
		c, issues, code := a.loadConfig(false)
		if code != exitOK {
			a.printIssues(issues)
			return code
		}
		fmt.Fprintln(a.stdout, c.Get(args[1]))
		return exitOK
	default:
		return a.configSet(args[1], args[2])
	}
}

// loadConfig reads the configuration; a missing file means defaults.
// Parse and validation issues are returned; code is exitConfig on any error.
func (a *app) loadConfig(noteMissing bool) (*config.Config, []config.Issue, int) {
	path := a.layout.ConfigFile()
	c, issues, err := config.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		if noteMissing {
			fmt.Fprintf(a.stderr, "note: %s not found, using defaults\n", path)
		}
	} else if err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed: %v\n", err)
		return c, nil, exitConfig
	}
	issues = append(issues, c.Validate()...)
	if config.HasErrors(issues) {
		return c, issues, exitConfig
	}
	return c, issues, exitOK
}

func (a *app) printIssues(issues []config.Issue) {
	for _, i := range issues {
		fmt.Fprintf(a.stderr, "%s: %s\n", filepath.Base(a.layout.ConfigFile()), i)
	}
}

func (a *app) configSet(key, value string) int {
	if !config.Known(key) {
		fmt.Fprintf(a.stderr, "qnetspeed config: unknown key %s\n", key)
		return exitUsage
	}
	path := a.layout.ConfigFile()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		data, err = os.ReadFile(a.layout.DefaultConfig())
		if errors.Is(err, fs.ErrNotExist) {
			data, err = nil, nil
		}
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed: %v\n", err)
		return exitConfig
	}
	c, issues := config.Parse(data)
	if err := c.Set(key, value); err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed config: %v\n", err)
		return exitConfig
	}
	issues = append(issues, c.Validate()...)
	if config.HasErrors(issues) {
		a.printIssues(issues)
		fmt.Fprintln(a.stderr, "qnetspeed config: not saved")
		return exitConfig
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed: %v\n", err)
		return exitConfig
	}
	if err := config.WriteFileAtomic(path, c.Bytes(), 0o644); err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed: %v\n", err)
		return exitConfig
	}
	a.printIssues(issues)
	fmt.Fprintf(a.stdout, "%s=%s saved; restart the service to apply\n", key, config.FormatValue(value))
	return exitOK
}
