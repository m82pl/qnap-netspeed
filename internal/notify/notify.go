// Package notify decides which events reach the QTS System Logs and writes
// them with /sbin/log_tool (spec pkt 7). The decision is a pure function of
// the findings of one run and the state kept in data/state.json, so a cron
// run every 6 hours does not flood the log: a problem is written when it
// appears, repeated at most once per NOTIFY_REPEAT_HOURS while it lasts, and
// a single "back to normal" line closes it.
package notify

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/m82pl/qnap-netspeed/internal/runner"
)

// Log levels of /sbin/log_tool -t.
const (
	LevelInfo    = 0
	LevelWarning = 1
	LevelError   = 2
)

const (
	// Prefix starts every message; the QTS log is shared with other applications.
	Prefix = "[qnap-netspeed] "
	// MaxMessage is the longest message written, prefix included (spec pkt 7).
	MaxMessage = 200
	// LogToolPath is the QTS command that appends to System Logs.
	LogToolPath = "/sbin/log_tool"
	// forgetAfter drops state of rules that were not evaluated for this long
	// (a removed interface or LAN target must not stay "active" forever).
	forgetAfter = 30 * 24 * time.Hour
)

// Logger writes one System Logs entry.
type Logger interface {
	Log(level int, msg string) error
}

// LogTool is the real Logger: it runs /sbin/log_tool.
type LogTool struct {
	Run runner.Runner
	Bin string // empty = LogToolPath
}

// Log implements Logger. The message is sanitized and cut to MaxMessage; it
// is passed as one argument, never through a shell.
func (l LogTool) Log(level int, msg string) error {
	bin := l.Bin
	if bin == "" {
		bin = LogToolPath
	}
	res, err := l.Run.Run(context.Background(), 10*time.Second, bin,
		"-t", strconv.Itoa(level), "-a", Format(msg))
	if err != nil {
		return fmt.Errorf("%s: %w", bin, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s exited with code %d", bin, res.ExitCode)
	}
	return nil
}

// Format returns the text written to System Logs: Prefix, msg with control
// characters turned into spaces, at most MaxMessage characters.
func Format(msg string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, msg)
	full := Prefix + strings.Join(strings.Fields(clean), " ")
	if utf8.RuneCountInString(full) <= MaxMessage {
		return full
	}
	r := []rune(full)
	return string(r[:MaxMessage-1]) + "…"
}

// Finding is the verdict of one rule for one subject (interface, target) in
// the current run.
type Finding struct {
	Rule     string // e.g. "wan_dl"
	Subject  string // e.g. "eth0"
	Problem  bool
	Level    int    // for a problem: LevelWarning or LevelError
	Message  string // written when the problem appears or is repeated
	Recovery string // written (level info) when the problem is over
}

// Key is the state key: rule and subject (spec pkt 7).
func (f Finding) Key() string { return f.Rule + ":" + f.Subject }

// Options of Apply.
type Options struct {
	Now    time.Time
	Repeat time.Duration // NOTIFY_REPEAT_HOURS
}

// Kind of a sent entry.
type Kind string

const (
	KindNew       Kind = "new"
	KindRepeat    Kind = "repeat"
	KindRecovered Kind = "recovered"
)

// Sent is one entry written to System Logs.
type Sent struct {
	Kind    Kind
	Level   int
	Message string
}

// Apply compares findings with the state, writes what is due through log and
// updates the state. An entry that log refused is left as it was, so the next
// run tries again. It returns what was written and the errors met.
func Apply(st *State, findings []Finding, o Options, log Logger) ([]Sent, []error) {
	sorted := append([]Finding(nil), findings...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Key() < sorted[j].Key() })

	var sent []Sent
	var errs []error
	write := func(k Kind, level int, msg string) bool {
		if err := log.Log(level, msg); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", msg, err))
			return false
		}
		sent = append(sent, Sent{k, level, Format(msg)})
		return true
	}

	seen := map[string]bool{}
	for _, f := range sorted {
		key := f.Key()
		if seen[key] { // the first finding of a key wins
			continue
		}
		seen[key] = true
		e := st.Notify[key]
		switch {
		case f.Problem && e == nil:
			if write(KindNew, f.Level, f.Message) {
				st.Notify[key] = &Entry{Since: o.Now, LastNotified: o.Now, LastSeen: o.Now, Level: f.Level,
					Message: f.Message, Recovery: f.Recovery}
			}
		case f.Problem:
			e.LastSeen, e.Message, e.Level = o.Now, f.Message, f.Level
			if f.Recovery != "" {
				e.Recovery = f.Recovery
			}
			if o.Now.Sub(e.LastNotified) >= o.Repeat && write(KindRepeat, f.Level, f.Message) {
				e.LastNotified = o.Now
			}
		case e != nil:
			msg := f.Recovery
			if msg == "" {
				msg = e.Recovery
			}
			if msg == "" {
				msg = fmt.Sprintf("%s %s: back to normal", f.Rule, f.Subject)
			}
			if write(KindRecovered, LevelInfo, msg) {
				delete(st.Notify, key)
			} else {
				e.LastSeen = o.Now
			}
		}
	}
	for key, e := range st.Notify {
		if !seen[key] && o.Now.Sub(e.LastSeen) > forgetAfter {
			delete(st.Notify, key)
		}
	}
	return sent, errs
}
