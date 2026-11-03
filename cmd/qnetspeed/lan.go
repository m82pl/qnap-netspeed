package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/m82pl/qnap-netspeed/internal/config"
	"github.com/m82pl/qnap-netspeed/internal/lan"
)

const lanUsage = "Usage: qnetspeed lan-test HOST [--time N] [--parallel N] [--iface IF] [--udp [--bitrate B]] [--bidir] [--json] [--quiet] [--wait N]\n"

// lanTest runs iperf3 from the NAS against another host (spec pkt 5.2.2).
func (a *app) lanTest(args []string) int {
	fset := flag.NewFlagSet("lan-test", flag.ContinueOnError)
	var dur, par, waitSec int
	var iface, bitrate string
	var udp, bidir, asJSON, quiet bool
	fset.IntVar(&dur, "time", 0, "")
	fset.IntVar(&par, "parallel", 0, "")
	fset.StringVar(&iface, "iface", "", "")
	fset.BoolVar(&udp, "udp", false, "")
	fset.StringVar(&bitrate, "bitrate", "1G", "")
	fset.BoolVar(&bidir, "bidir", false, "")
	fset.BoolVar(&asJSON, "json", false, "")
	fset.BoolVar(&quiet, "quiet", false, "")
	fset.IntVar(&waitSec, "wait", 0, "")
	pos, err := parseFlags(fset, args)
	if err != nil || len(pos) != 1 || waitSec < 0 || dur < 0 || par < 0 {
		fmt.Fprint(a.stderr, lanUsage)
		return exitUsage
	}
	c, issues, code := a.loadConfig(false)
	if code != exitOK {
		a.printIssues(issues)
		return code
	}
	if err := config.ValidHost(pos[0]); err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed lan-test: %v\n", err)
		return exitUsage
	}
	if iface != "" {
		if err := config.ValidIface(iface); err != nil {
			fmt.Fprintf(a.stderr, "qnetspeed lan-test: %v\n", err)
			return exitUsage
		}
	}
	if config.ValidBitrate(bitrate) != nil {
		fmt.Fprintf(a.stderr, "qnetspeed lan-test: --bitrate %q: expected e.g. 100M or 1G\n", bitrate)
		return exitUsage
	}
	if dur == 0 {
		dur = c.Int("LAN_TEST_DURATION")
	}
	if par == 0 {
		par = c.Int("LAN_PARALLEL_STREAMS")
	}
	if dur > 300 || par > 32 {
		fmt.Fprintln(a.stderr, "qnetspeed lan-test: --time is at most 300 and --parallel at most 32")
		return exitUsage
	}
	if !a.needRoot(c) {
		return exitTestError
	}
	l, code := a.acquire(c, "lan-test", waitSec)
	if l == nil {
		return code
	}
	defer l.Release()

	sys := a.sys()
	eng := &lan.Engine{Runner: sys.run, Net: sys.net, Sys: sysAdapter{sys.sysfs},
		Bin: filepath.Join(a.binDir, "iperf3"), Now: sys.now, CPUs: runtime.NumCPU(), Inbound: a.inbound}
	ctx, stop := interruptible()
	defer stop()
	if !quiet && !asJSON {
		fmt.Fprintf(a.stdout, tr(c, "Testing %s with iperf3 (about %d s)...\n", "Test z %s przez iperf3 (ok. %d s)...\n"),
			pos[0], dur*2+5)
	}
	rec := eng.Run(ctx, lan.Params{Host: pos[0], Port: c.Int("IPERF_PORT"), Duration: dur, Parallel: par,
		Iface: iface, UDP: udp, Bitrate: bitrate, Bidir: bidir}, "cli")
	a.store(a.layout.LanHistory(), c, rec)
	switch {
	case asJSON:
		b, _ := json.Marshal(rec)
		fmt.Fprintf(a.stdout, "%s\n", b)
	case !quiet:
		a.printLan(c, rec)
	}
	if rec.Status != "ok" {
		if ctx.Err() == nil && rec.Error != nil && strings.Contains(*rec.Error, "did not finish within") {
			return exitTimeout
		}
		return exitTestError
	}
	return exitOK
}

func contains(s, sub string) bool {
	return len(sub) > 0 && regexp.MustCompile(regexp.QuoteMeta(sub)).MatchString(s)
}

func (a *app) printLan(c *config.Config, r lan.Record) {
	if r.Status != "ok" {
		fmt.Fprintf(a.stdout, "%s: %s\n", tr(c, "ERROR", "BŁĄD"), deref(r.Error))
		return
	}
	res := r.Results
	fmt.Fprintf(a.stdout, "%s → %s  ↑ %.2f Mb/s  ↓ %.2f Mb/s", r.Interface, r.Target, res.UploadMbps, res.DownloadMbps)
	if res.RetransmitsUpload != nil {
		fmt.Fprintf(a.stdout, "  %s %d", tr(c, "retransmits", "retransmisje"), *res.RetransmitsUpload)
	}
	if res.JitterMs != nil && res.LostPercent != nil {
		fmt.Fprintf(a.stdout, "  UDP jitter %.3f ms, %s %.3f%%", *res.JitterMs, tr(c, "loss", "utrata"), *res.LostPercent)
	}
	fmt.Fprintln(a.stdout)
	for _, p := range r.Physical {
		sp := "?"
		if p.SpeedMbps != nil {
			sp = strconv.Itoa(*p.SpeedMbps)
		}
		fmt.Fprintf(a.stdout, "  %s: %s Mb/s, %s, MTU %d\n", p.Name, sp, p.Duplex, p.MTU)
	}
	for _, w := range r.Warnings {
		switch w {
		case lan.WarnCPULimited:
			fmt.Fprintln(a.stderr, tr(c, "warning: NAS CPU above 90% — the result is limited by the processor",
				"ostrzeżenie: CPU NAS-a powyżej 90% — wynik ograniczony procesorem"))
		case lan.WarnConcurrent:
			fmt.Fprintln(a.stderr, tr(c, "warning: an incoming iperf3 test was running; the result may be distorted",
				"ostrzeżenie: w trakcie pomiaru trwał test przychodzący iperf3; wynik mógł zostać zaburzony"))
		}
	}
}
