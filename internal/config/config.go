// Package config reads, validates and writes qnetspeed.conf (spec pkt 10).
//
// The file is KEY=value, compatible with POSIX sh, but it is parsed here and
// never executed. Values may be bare words or wrapped in "…" or '…'; a '#'
// after whitespace starts a comment. Characters that would change meaning in
// a shell (quotes, $, backtick, backslash) are rejected in every value.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Severity of a configuration issue.
type Severity int

const (
	Warning Severity = iota
	Error
)

func (s Severity) String() string {
	if s == Error {
		return "error"
	}
	return "warning"
}

// Issue is one problem found while parsing or validating.
type Issue struct {
	Severity Severity
	Line     int // 0 when not tied to a line
	Key      string
	Msg      string
}

func (i Issue) String() string {
	var b strings.Builder
	b.WriteString(i.Severity.String())
	if i.Line > 0 {
		fmt.Fprintf(&b, ": line %d", i.Line)
	}
	if i.Key != "" {
		fmt.Fprintf(&b, ": %s", i.Key)
	}
	fmt.Fprintf(&b, ": %s", i.Msg)
	return b.String()
}

// HasErrors reports whether any issue is an Error.
func HasErrors(issues []Issue) bool {
	for _, i := range issues {
		if i.Severity == Error {
			return true
		}
	}
	return false
}

type line struct {
	raw string
	key string // empty for comments, blank lines and unparsable lines
}

// Config is a parsed configuration file plus defaults for missing keys.
type Config struct {
	lines  []line
	values map[string]string // keys present in the file (last one wins)
}

// Defaults returns a configuration with no file content.
func Defaults() *Config { return &Config{values: map[string]string{}} }

var keyRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// Parse parses file content. It never fails: problems are returned as issues
// and the offending lines are ignored (defaults apply).
func Parse(data []byte) (*Config, []Issue) {
	c := Defaults()
	var issues []Issue
	seen := map[string]int{}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return c, nil
	}
	for i, raw := range strings.Split(text, "\n") {
		n := i + 1
		l := line{raw: raw}
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") {
			c.lines = append(c.lines, l)
			continue
		}
		k, rest, ok := strings.Cut(t, "=")
		if !ok || !keyRe.MatchString(k) {
			issues = append(issues, Issue{Error, n, "", "expected KEY=value"})
			c.lines = append(c.lines, l)
			continue
		}
		v, err := parseValue(rest)
		if err != nil {
			issues = append(issues, Issue{Error, n, k, err.Error()})
			c.lines = append(c.lines, l)
			continue
		}
		if !Known(k) {
			issues = append(issues, Issue{Warning, n, k, "unknown key (typo?)"})
		}
		if prev, dup := seen[k]; dup {
			issues = append(issues, Issue{Warning, n, k, fmt.Sprintf("duplicate key, overrides line %d", prev)})
		}
		seen[k] = n
		l.key = k
		c.lines = append(c.lines, l)
		c.values[k] = v
	}
	return c, issues
}

const forbidden = "\"'`$\\"

var bareRe = regexp.MustCompile(`^[A-Za-z0-9_./:,@+%=-]*$`)

func parseValue(s string) (string, error) {
	var v, rest string
	switch {
	case strings.HasPrefix(s, `"`) || strings.HasPrefix(s, `'`):
		q := s[:1]
		end := strings.Index(s[1:], q)
		if end < 0 {
			return "", errors.New("missing closing quote")
		}
		v, rest = s[1:1+end], s[2+end:]
	default:
		end := strings.IndexAny(s, " \t")
		if end < 0 {
			end = len(s)
		}
		v, rest = s[:end], s[end:]
		if !bareRe.MatchString(v) {
			return "", errors.New("value with special characters must be in double quotes")
		}
	}
	if rest != "" {
		r := strings.TrimLeft(rest, " \t")
		if r != "" && (len(r) == len(rest) || !strings.HasPrefix(r, "#")) {
			return "", errors.New("unexpected text after value")
		}
	}
	if err := checkChars(v); err != nil {
		return "", err
	}
	return v, nil
}

func checkChars(v string) error {
	if strings.ContainsAny(v, forbidden) {
		return fmt.Errorf("characters %s are not allowed", forbidden)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return errors.New("control characters are not allowed")
		}
	}
	return nil
}

// Load reads and parses path. A missing file returns defaults and an error
// wrapping os.ErrNotExist.
func Load(path string) (*Config, []Issue, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Defaults(), nil, err
	}
	c, issues := Parse(data)
	return c, issues, nil
}

// Get returns the effective value of a known key (file value or default).
// It panics on an unknown key: that is a programming error.
func (c *Config) Get(name string) string {
	k, ok := keyIndex[name]
	if !ok {
		panic("config: unknown key " + name)
	}
	if v, ok := c.values[name]; ok {
		return v
	}
	return k.Default
}

// Int returns a key as int; call only after Validate reported no errors.
func (c *Config) Int(name string) int {
	n, _ := strconv.Atoi(c.Get(name))
	return n
}

// Float returns a key as float64; call only after Validate reported no errors.
func (c *Config) Float(name string) float64 {
	f, _ := strconv.ParseFloat(c.Get(name), 64)
	return f
}

// Validate checks every effective value and cross-key rules.
func (c *Config) Validate() []Issue {
	var issues []Issue
	for _, k := range Keys {
		if err := k.Validate(c.Get(k.Name)); err != nil {
			issues = append(issues, Issue{Error, c.lineOf(k.Name), k.Name, err.Error()})
		}
	}
	if c.Get("WEB_PORT") == c.Get("IPERF_PORT") {
		issues = append(issues, Issue{Error, c.lineOf("IPERF_PORT"), "IPERF_PORT", "must differ from WEB_PORT"})
	}
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Line < issues[j].Line })
	return issues
}

func (c *Config) lineOf(name string) int {
	n := 0
	for i, l := range c.lines {
		if l.key == name {
			n = i + 1
		}
	}
	return n
}

// Set validates a value and stores it, replacing the key's last line in the
// file (or appending one). Other lines, comments included, stay untouched.
func (c *Config) Set(name, value string) error {
	k, ok := keyIndex[name]
	if !ok {
		return fmt.Errorf("unknown key %s", name)
	}
	if err := checkChars(value); err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	if err := k.Validate(value); err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	newLine := line{raw: name + "=" + FormatValue(value), key: name}
	idx := -1
	for i, l := range c.lines {
		if l.key == name {
			idx = i
		}
	}
	if idx >= 0 {
		c.lines[idx] = newLine
	} else {
		c.lines = append(c.lines, newLine)
	}
	c.values[name] = value
	return nil
}

// FormatValue renders a value for the file: bare when safe, else in double quotes.
func FormatValue(v string) string {
	if v != "" && bareRe.MatchString(v) {
		return v
	}
	return `"` + v + `"`
}

// Bytes renders the file content.
func (c *Config) Bytes() []byte {
	var b bytes.Buffer
	for _, l := range c.lines {
		b.WriteString(l.raw)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// Show renders all known keys with their effective values, in spec order.
func (c *Config) Show() []byte {
	var b bytes.Buffer
	for _, k := range Keys {
		fmt.Fprintf(&b, "%s=%s\n", k.Name, FormatValue(c.Get(k.Name)))
	}
	return b.Bytes()
}

// WriteFileAtomic writes data to a temporary file in the same directory,
// syncs it and renames it over path. An existing file's mode is kept.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// LoadForEdit reads path for modification; a missing file starts from the
// default file, and a missing default from an empty configuration.
func LoadForEdit(path, defaultPath string) (*Config, []Issue, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		data, err = os.ReadFile(defaultPath)
		if errors.Is(err, fs.ErrNotExist) {
			data, err = nil, nil
		}
	}
	if err != nil {
		return nil, nil, err
	}
	c, issues := Parse(data)
	return c, issues, nil
}

// ValidateValue checks one value for a known key, with the same rules as Set
// but without the key name in the message (the API reports it per field).
func ValidateValue(name, value string) error {
	k, ok := keyIndex[name]
	if !ok {
		return fmt.Errorf("unknown key")
	}
	if err := checkChars(value); err != nil {
		return err
	}
	return k.Validate(value)
}

// restartKeys are read once, when the service starts; a change needs a restart.
var restartKeys = map[string]bool{
	"WEB_PORT": true, "WEB_BIND": true, "IPERF_PORT": true, "LAN_BIND": true,
	"LAN_SERVER_ENABLE": true, "CRON_SCHEDULE": true,
}

// RestartRequired reports whether a change of key applies only after the service restarts.
func RestartRequired(name string) bool { return restartKeys[name] }

// KeyInfo describes one key for the API.
type KeyInfo struct {
	Name    string
	Default string
}

// KeyList returns every known key with its default, in spec order.
func KeyList() []KeyInfo {
	out := make([]KeyInfo, len(Keys))
	for i, k := range Keys {
		out[i] = KeyInfo{k.Name, k.Default}
	}
	return out
}
