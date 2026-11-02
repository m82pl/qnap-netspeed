// Command qnetspeed is the main program of the qnap-netspeed QPKG:
// CLI, web server and scheduler in one static binary (spec D5).
//
// Phase F1 (walking skeleton) provides version, config, daemon (status page
// on WEB_PORT) and a cron entry point that only records that it ran.
package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/paths"
)

// version is set at build time: -ldflags "-X main.version=<ver>".
var version = "dev"

// Exit codes (spec pkt 6).
const (
	exitOK        = 0
	exitTestError = 1
	exitUsage     = 2
	exitConfig    = 3
	exitBusy      = 75
	exitTimeout   = 124
)

const usage = `Usage: qnetspeed <command> [options]

Commands:
  version                 Show versions and architecture
  config show             Show the effective configuration
  config check            Validate the configuration file
  config get KEY          Print one value
  config set KEY VALUE    Validate and save one value
  wan [--iface IF]... [--all] [--server ID]... [--json] [--quiet] [--wait N]
                          Internet speed test (librespeed-cli), result to wan.jsonl
  lan-test HOST [--time N] [--parallel N] [--iface IF] [--udp [--bitrate B]] [--bidir] [--json] [--quiet] [--wait N]
                          LAN speed test against a host running iperf3 -s
  link [--iface IF] [--json] [--record]
                          State of network interfaces (speed, duplex, MTU, bridge/bond)
  routes [--json]         Multi-WAN diagnostics: which interface the traffic leaves through
  history [--type wan|lan|link] [--last N] [--since 7d] [--iface IF] [--json|--csv]
                          Recent results
  daemon                  Run the web server (started by the service script)
  cron                    Scheduled run (started by crond)
  help                    Show this help
`

// app carries everything a command needs, so tests can replace it.
type app struct {
	layout   paths.Layout
	binDir   string
	stdout   io.Writer
	stderr   io.Writer
	cronWait time.Duration
	system   system // measurement dependencies; zero value = the real OS
}

func main() {
	a := &app{
		layout:   paths.Detect(),
		binDir:   paths.BinDir(),
		stdout:   os.Stdout,
		stderr:   os.Stderr,
		cronWait: 15 * time.Minute,
	}
	os.Exit(a.run(os.Args[1:]))
}

func (a *app) run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "version", "--version", "-v":
		if len(args) > 1 {
			fmt.Fprintf(a.stderr, "qnetspeed version: unexpected argument %q\n", args[1])
			return exitUsage
		}
		return a.version()
	case "config":
		return a.config(args[1:])
	case "wan":
		return a.wan(args[1:])
	case "lan-test":
		return a.lanTest(args[1:])
	case "link":
		return a.link(args[1:])
	case "routes":
		return a.routes(args[1:])
	case "history":
		return a.history(args[1:])
	case "daemon":
		if len(args) > 1 {
			fmt.Fprintf(a.stderr, "qnetspeed daemon: unexpected argument %q\n", args[1])
			return exitUsage
		}
		return a.daemon()
	case "cron":
		if len(args) > 1 {
			fmt.Fprintf(a.stderr, "qnetspeed cron: unexpected argument %q\n", args[1])
			return exitUsage
		}
		return a.cron()
	case "help", "--help", "-h":
		fmt.Fprint(a.stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(a.stderr, "qnetspeed: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}
