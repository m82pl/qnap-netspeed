// Package paths resolves the package layout on the NAS (spec pkt 4.2).
package paths

import (
	"os"
	"path/filepath"
)

// EnvRoot overrides the package root (tests, development).
const EnvRoot = "QNETSPEED_ROOT"

// Layout is the set of files the program uses under $QPKG_ROOT.
type Layout struct {
	Root string
}

// Detect returns the layout: $QNETSPEED_ROOT if set, otherwise the parent of
// the directory holding the running binary ($QPKG_ROOT/bin/qnetspeed), with
// symlinks such as /usr/bin/qnetspeed resolved.
func Detect() Layout {
	if r := os.Getenv(EnvRoot); r != "" {
		return Layout{Root: r}
	}
	return Layout{Root: filepath.Dir(BinDir())}
}

// BinDir is the directory of the running binary (symlinks resolved).
func BinDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return filepath.Dir(exe)
}

func (l Layout) ConfigFile() string { return filepath.Join(l.Root, "etc", "qnetspeed.conf") }
func (l Layout) DefaultConfig() string {
	return filepath.Join(l.Root, "etc", "qnetspeed.conf.default")
}
func (l Layout) DataDir() string   { return filepath.Join(l.Root, "data") }
func (l Layout) LockFile() string  { return filepath.Join(l.DataDir(), ".test.lock") }
func (l Layout) StateFile() string { return filepath.Join(l.DataDir(), "state.json") }
func (l Layout) LogFile() string   { return filepath.Join(l.DataDir(), "qnetspeed.log") }
func (l Layout) TokenFile() string { return filepath.Join(l.DataDir(), "web.token") }

func (l Layout) WanHistory() string  { return filepath.Join(l.DataDir(), "wan.jsonl") }
func (l Layout) LanHistory() string  { return filepath.Join(l.DataDir(), "lan.jsonl") }
func (l Layout) LinkHistory() string { return filepath.Join(l.DataDir(), "link.jsonl") }
