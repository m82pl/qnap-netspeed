package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/lock"
)

// cron is started by crond (spec pkt 9). In F1 it only takes the test lock
// and records the run in data/qnetspeed.log, which proves the crontab entry
// works on the NAS. The scheduled tests arrive in F3.
func (a *app) cron() int {
	if err := os.MkdirAll(a.layout.DataDir(), 0o750); err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed cron: %v\n", err)
		return exitTestError
	}
	l, err := lock.Acquire(a.layout.LockFile(), "cron", a.cronWait)
	if errors.Is(err, lock.ErrBusy) {
		a.logLine("cron: skipped, " + err.Error())
		return exitBusy
	}
	if err != nil {
		a.logLine("cron: " + err.Error())
		return exitTestError
	}
	defer l.Release()
	a.logLine("cron: run (F1 skeleton: no scheduled tests yet)")
	return exitOK
}

// logLine appends one timestamped line to data/qnetspeed.log.
func (a *app) logLine(msg string) {
	f, err := os.OpenFile(a.layout.LogFile(), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o640)
	if err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed: %v\n", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s qnetspeed %s\n", time.Now().Format("2006/01/02 15:04:05"), msg)
}
