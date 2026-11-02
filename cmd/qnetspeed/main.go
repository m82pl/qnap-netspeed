// Command qnetspeed is the main program of the qnap-netspeed QPKG:
// CLI, web server and scheduler in one static binary (spec D5).
//
// Phase F0 provides only the entry point, exit codes and `version`;
// the remaining commands arrive in later phases (see the build plan).
package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
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
  version    Show versions and architecture
  help       Show this help
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "version", "--version", "-v":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "qnetspeed version: unexpected argument %q\n", args[1])
			return exitUsage
		}
		fmt.Fprintf(stdout, "qnetspeed %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return exitOK
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "qnetspeed: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}
