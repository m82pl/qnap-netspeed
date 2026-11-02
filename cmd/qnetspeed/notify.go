package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/m82pl/qnap-netspeed/internal/notify"
)

const notifyUsage = "Usage: qnetspeed notify test [--level 0|1|2]\n       qnetspeed notify status [--json]\n"

// notifyCmd: `test` writes one entry to System Logs (checks that log_tool works
// and where the entries show up); `status` lists the problems that have been
// reported and not closed yet (data/state.json, readable without root).
func (a *app) notifyCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.stderr, notifyUsage)
		return exitUsage
	}
	switch args[0] {
	case "test":
		return a.notifyTest(args[1:])
	case "status":
		return a.notifyStatus(args[1:])
	}
	fmt.Fprint(a.stderr, notifyUsage)
	return exitUsage
}

func (a *app) notifyTest(args []string) int {
	fset := flag.NewFlagSet("notify test", flag.ContinueOnError)
	level := fset.Int("level", notify.LevelInfo, "")
	pos, err := parseFlags(fset, args)
	if err != nil || len(pos) > 0 || *level < 0 || *level > 2 {
		fmt.Fprint(a.stderr, notifyUsage)
		return exitUsage
	}
	c, issues, code := a.loadConfig(false)
	if code != exitOK {
		a.printIssues(issues)
		return code
	}
	if !a.needRoot(c) {
		return exitTestError
	}
	msg := "test message from qnetspeed notify test"
	if err := (notify.LogTool{Run: a.sys().run}).Log(*level, msg); err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed notify: %v\n", err)
		return exitTestError
	}
	fmt.Fprintf(a.stdout, tr(c, "Written to System Logs (level %d): %s\n", "Zapisano w Dziennikach systemowych (poziom %d): %s\n"),
		*level, notify.Format(msg))
	return exitOK
}

func (a *app) notifyStatus(args []string) int {
	fset := flag.NewFlagSet("notify status", flag.ContinueOnError)
	asJSON := fset.Bool("json", false, "")
	pos, err := parseFlags(fset, args)
	if err != nil || len(pos) > 0 {
		fmt.Fprint(a.stderr, notifyUsage)
		return exitUsage
	}
	c, issues, code := a.loadConfig(false)
	if code != exitOK {
		a.printIssues(issues)
		return code
	}
	st, err := notify.LoadState(a.layout.StateFile())
	if err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed notify: %v\n", err)
		if st == nil {
			return exitTestError
		}
	}
	if *asJSON {
		b, _ := json.Marshal(st)
		fmt.Fprintf(a.stdout, "%s\n", b)
		return exitOK
	}
	keys := st.Active()
	if len(keys) == 0 {
		fmt.Fprintln(a.stdout, tr(c, "No open problems.", "Brak otwartych problemów."))
		return exitOK
	}
	for _, k := range keys {
		e := st.Notify[k]
		fmt.Fprintf(a.stdout, "%-24s since %s, last written %s: %s\n", k,
			e.Since.Local().Format("2006-01-02 15:04"), e.LastNotified.Local().Format("2006-01-02 15:04"),
			strings.TrimPrefix(notify.Format(e.Message), notify.Prefix))
	}
	return exitOK
}
