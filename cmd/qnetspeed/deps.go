package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/config"
	"github.com/m82pl/qnap-netspeed/internal/history"
	"github.com/m82pl/qnap-netspeed/internal/lan"
	"github.com/m82pl/qnap-netspeed/internal/link"
	"github.com/m82pl/qnap-netspeed/internal/lock"
	"github.com/m82pl/qnap-netspeed/internal/netroute"
	"github.com/m82pl/qnap-netspeed/internal/runner"
)

// system groups what the measurement commands take from the OS, so tests can
// replace it. A nil field means the real thing (see withDefaults).
type system struct {
	run     runner.Runner
	net     netroute.System
	sysfs   fs.FS // /sys/class/net
	now     func() time.Time
	euid    func() int
	procTCP string
}

func (a *app) sys() system {
	s := a.system
	if s.run == nil {
		s.run = runner.Exec{}
	}
	if s.net == nil {
		s.net = netroute.Linux{}
	}
	if s.sysfs == nil {
		s.sysfs = os.DirFS("/sys/class/net")
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.euid == nil {
		s.euid = os.Geteuid
	}
	if s.procTCP == "" {
		s.procTCP = "/proc/net/tcp"
	}
	return s
}

// tr picks the message for LANG (spec pkt 6: human text in Polish or English,
// JSON and keys always English).
func tr(c *config.Config, en, pl string) string {
	if c != nil && c.Get("LANG") == "pl" {
		return pl
	}
	return en
}

// parseFlags parses args allowing options after positional arguments
// (`lan-test HOST --time 5`). It returns the positional arguments.
func parseFlags(fset *flag.FlagSet, args []string) ([]string, error) {
	fset.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fset.Parse(args); err != nil {
			return nil, err
		}
		args = fset.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// listFlag collects a repeatable option.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// needRoot refuses commands that measure or change things when not root (spec pkt 6).
func (a *app) needRoot(c *config.Config) bool {
	if a.sys().euid() == 0 {
		return true
	}
	fmt.Fprintln(a.stderr, tr(c, "qnetspeed: this command must run as root (use sudo or log in as admin)",
		"qnetspeed: to polecenie wymaga uprawnień root (użyj sudo albo zaloguj się jako admin)"))
	return false
}

// acquire takes the single active-test lock (spec pkt 3.2). On failure it
// prints the reason and returns the exit code.
func (a *app) acquire(c *config.Config, typ string, waitSec int) (*lock.Lock, int) {
	if err := os.MkdirAll(a.layout.DataDir(), 0o755); err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed: %v\n", err)
		return nil, exitTestError
	}
	l, err := lock.Acquire(a.layout.LockFile(), typ, time.Duration(waitSec)*time.Second)
	var busy *lock.BusyError
	switch {
	case errors.As(err, &busy):
		if busy.Holder.Type == "" {
			fmt.Fprintln(a.stderr, tr(c, "qnetspeed: another test is running", "qnetspeed: trwa inny test"))
		} else {
			fmt.Fprintf(a.stderr, tr(c, "qnetspeed: another test is running: %s since %s\n", "qnetspeed: trwa inny test: %s od %s\n"),
				busy.Holder.Type, busy.Holder.Since.Local().Format("15:04:05"))
		}
		return nil, exitBusy
	case err != nil:
		fmt.Fprintf(a.stderr, "qnetspeed: %v\n", err)
		return nil, exitTestError
	}
	return l, exitOK
}

// interruptible returns a context cancelled by SIGINT/SIGTERM, so Ctrl+C
// stops librespeed-cli / iperf3 and releases the lock.
func interruptible() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// store appends a record to a history file and rotates it (spec pkt 13.4).
func (a *app) store(path string, c *config.Config, rec any) {
	s := history.Store{Path: path}
	if err := s.Append(rec); err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed: cannot write history: %v\n", err)
		return
	}
	if err := s.Rotate(c.Int("HISTORY_MAX_LINES")); err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed: history rotation: %v\n", err)
	}
}

// inbound reports an incoming iperf3 test on port, from /proc/net/tcp.
func (a *app) inbound(port int) bool {
	f, err := os.Open(a.sys().procTCP)
	if err != nil {
		return false
	}
	defer f.Close()
	return lan.InboundFromProcTCP(f, port)
}

// sysAdapter lets package lan resolve physical interfaces through the same fs.FS.
type sysAdapter struct{ fsys fs.FS }

func (s sysAdapter) Resolve(name string) []link.Physical {
	p, _ := link.Resolve(s.fsys, name)
	return p
}
func (s sysAdapter) MTU(name string) int {
	i, err := link.ReadOne(s.fsys, name)
	if err != nil {
		return 0
	}
	return i.MTU
}
