package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/config"
	"github.com/m82pl/qnap-netspeed/internal/history"
	"github.com/m82pl/qnap-netspeed/internal/lan"
	"github.com/m82pl/qnap-netspeed/internal/link"
	"github.com/m82pl/qnap-netspeed/internal/lock"
	"github.com/m82pl/qnap-netspeed/internal/notify"
	"github.com/m82pl/qnap-netspeed/internal/wan"
)

// cron is started by crond (spec pkt 9) and does, under one test lock:
// link record and check, WAN tests, scheduled LAN tests, notifications.
// History is rotated after every write (store), which covers the last step.
// Every step is written to data/qnetspeed.log (and to stdout, so a manual
// run shows what crond would do). Exit code: 0 all steps worked, 1 a test or
// a step failed, 75 another test kept the lock for the whole wait, 3 bad config.
func (a *app) cron() int {
	c, issues, code := a.loadConfig(false)
	if code != exitOK {
		a.printIssues(issues)
		a.logLine("cron: invalid configuration, nothing done")
		return code
	}
	if !a.needRoot(c) {
		return exitTestError
	}
	if err := os.MkdirAll(a.layout.DataDir(), 0o755); err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed cron: %v\n", err)
		return exitTestError
	}
	// Waits for a running test (GUI, CLI) instead of testing at the same time (A14).
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

	sys := a.sys()
	start := sys.now()
	ctx, stop := interruptible()
	defer stop()
	failed := false
	var findings []notify.Finding
	th := thresholds(c)

	// 2. Link state: record and check against LINK_EXPECT.
	if all, err := link.Read(sys.sysfs); err != nil {
		a.cronLog("cron: link: %v", err)
		failed = true
	} else {
		warnings := link.Evaluate(all, link.ParseExpect(c.Get("LINK_EXPECT")))
		a.store(a.layout.LinkHistory(), c, link.Record{Schema: 1, Timestamp: sys.now().UTC().Truncate(time.Second),
			Type: "LINK", Interfaces: all, Warnings: warnings})
		a.cronLog("cron: link: %d interfaces, %d warnings", len(all), len(warnings))
		findings = append(findings, notify.LinkFindings(all, warnings)...)
	}

	// 3. WAN tests, one link after another.
	var wanRecs []wan.Record
	if c.Get("WAN_SCHEDULE_ENABLE") == "1" && ctx.Err() == nil {
		s, bad := a.wanSettings(c, nil)
		if bad != nil {
			a.cronLog("cron: wan: %v", bad)
			failed = true
		} else {
			eng := &wan.Engine{Runner: sys.run, Net: sys.net, Bin: wan.DefaultBin(a.binDir),
				Version: wanEngineVersion, Now: sys.now, EchoIP: wan.EchoHTTP, Inbound: a.inbound}
			wanRecs = eng.RunAll(ctx, s, strings.Fields(c.Get("WAN_IFACES")), "cron", func(r wan.Record) {
				a.store(a.layout.WanHistory(), c, r)
			})
			for _, r := range wanRecs {
				a.cronLog("cron: wan %s", wanSummary(r))
				failed = failed || r.Status != wan.StatusOK
			}
		}
	} else if c.Get("WAN_SCHEDULE_ENABLE") != "1" {
		a.cronLog("cron: wan: scheduled tests are off (WAN_SCHEDULE_ENABLE=0)")
	}

	// 4. LAN tests against LAN_SCHEDULE_TARGETS (each needs a running iperf3 -s).
	var lanRecs []lan.Record
	if targets := strings.Fields(c.Get("LAN_SCHEDULE_TARGETS")); len(targets) > 0 {
		eng := &lan.Engine{Runner: sys.run, Net: sys.net, Sys: sysAdapter{sys.sysfs},
			Bin: filepath.Join(a.binDir, "iperf3"), Now: sys.now, CPUs: runtime.NumCPU(), Inbound: a.inbound}
		for _, host := range targets {
			if ctx.Err() != nil {
				break
			}
			if err := config.ValidHost(host); err != nil {
				a.cronLog("cron: lan: %v", err)
				failed = true
				continue
			}
			rec := eng.Run(ctx, lan.Params{Host: host, Port: c.Int("IPERF_PORT"), Duration: c.Int("LAN_TEST_DURATION"),
				Parallel: c.Int("LAN_PARALLEL_STREAMS"), Bitrate: "1G"}, "cron")
			a.store(a.layout.LanHistory(), c, rec)
			lanRecs = append(lanRecs, rec)
			a.cronLog("cron: lan %s", lanSummary(rec))
			failed = failed || rec.Status != "ok"
		}
	}
	findings = append(findings, notify.WANFindings(th, wanRecs, a.wanFailStreak)...)
	findings = append(findings, notify.LANFindings(th, lanRecs)...)

	// 5. Notifications.
	switch {
	case ctx.Err() != nil:
		a.cronLog("cron: interrupted, notifications skipped")
		failed = true
	case c.Get("NOTIFY_ENABLE") != "1":
		a.cronLog("cron: notifications are off (NOTIFY_ENABLE=0)")
	default:
		failed = a.cronNotify(c, findings) || failed
	}

	a.cronLog("cron: done in %d s", int(sys.now().Sub(start).Seconds()+0.5))
	if failed {
		return exitTestError
	}
	return exitOK
}

// thresholds reads the MIN_*/MAX_* keys.
func thresholds(c *config.Config) notify.Thresholds {
	return notify.Thresholds{
		MinWANDownload: c.Float("MIN_WAN_DL_MBPS"),
		MinWANUpload:   c.Float("MIN_WAN_UL_MBPS"),
		MaxWANPing:     c.Float("MAX_WAN_PING_MS"),
		MinLAN:         c.Float("MIN_LAN_MBPS"),
		FailThreshold:  c.Int("WAN_FAIL_THRESHOLD"),
	}
}

// cronNotify applies the findings to data/state.json and System Logs; it
// reports whether something went wrong.
func (a *app) cronNotify(c *config.Config, findings []notify.Finding) bool {
	sys := a.sys()
	path := a.layout.StateFile()
	failed := false
	st, err := notify.LoadState(path)
	if err != nil {
		a.cronLog("cron: notify: %v (starting with an empty state)", err)
		if !errors.Is(err, notify.ErrCorrupt) {
			return true
		}
	}
	sent, errs := notify.Apply(st, findings, notify.Options{
		Now:    sys.now(),
		Repeat: time.Duration(c.Int("NOTIFY_REPEAT_HOURS")) * time.Hour,
	}, notify.LogTool{Run: sys.run})
	for _, s := range sent {
		a.cronLog("cron: notify (%s, level %d): %s", s.Kind, s.Level, s.Message)
	}
	for _, e := range errs {
		a.cronLog("cron: notify: cannot write System Logs entry: %v", e)
		failed = true
	}
	if err := st.Save(path); err != nil {
		a.cronLog("cron: notify: cannot save state: %v", err)
		failed = true
	}
	if len(sent) == 0 && len(errs) == 0 {
		a.cronLog("cron: notify: nothing new (%d active)", len(st.Notify))
	}
	return failed
}

// wanFailStreak counts the newest WAN tests of iface that did not succeed,
// in a row, from wan.jsonl (manual tests count too).
func (a *app) wanFailStreak(iface string) int {
	recs, _, err := history.Store{Path: a.layout.WanHistory()}.Read(history.Filter{Interface: iface, Last: 100})
	if err != nil {
		return 0
	}
	n := 0
	for i := len(recs) - 1; i >= 0; i-- {
		var h struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(recs[i].Raw, &h) != nil || h.Status == wan.StatusOK {
			break
		}
		n++
	}
	return n
}

func wanSummary(r wan.Record) string {
	switch r.Status {
	case wan.StatusOK:
		return fmt.Sprintf("%s: ok, down %.1f / up %.1f Mb/s, ping %.1f ms", r.Interface,
			r.Results.DownloadMbps, r.Results.UploadMbps, r.Results.PingMs)
	case wan.StatusSkipped:
		return fmt.Sprintf("%s: skipped: %s", r.Interface, deref(r.Error))
	}
	return fmt.Sprintf("%s: error: %s", r.Interface, deref(r.Error))
}

func lanSummary(r lan.Record) string {
	if r.Status != "ok" || r.Results == nil {
		return fmt.Sprintf("%s: error: %s", r.Target, deref(r.Error))
	}
	return fmt.Sprintf("%s: ok, up %.1f / down %.1f Mb/s", r.Target, r.Results.UploadMbps, r.Results.DownloadMbps)
}

// cronLog writes one line to data/qnetspeed.log and to stdout.
func (a *app) cronLog(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	a.logLine(msg)
	fmt.Fprintln(a.stdout, msg)
}

// logLine appends one timestamped line to data/qnetspeed.log.
func (a *app) logLine(msg string) {
	f, err := os.OpenFile(a.layout.LogFile(), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o640)
	if err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed: %v\n", err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s qnetspeed %s\n", time.Now().Format("2006/01/02 15:04:05"), strings.ReplaceAll(msg, "\n", " "))
}
