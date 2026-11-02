package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/m82pl/qnap-netspeed/internal/config"
	"github.com/m82pl/qnap-netspeed/internal/wan"
)

const wanUsage = "Usage: qnetspeed wan [--iface IF]... [--all] [--server ID]... [--json] [--quiet] [--wait N]\n"

// wan measures the internet connection of one or more interfaces (spec pkt 5.1).
func (a *app) wan(args []string) int {
	fset := flag.NewFlagSet("wan", flag.ContinueOnError)
	var ifaces, servers listFlag
	var all, asJSON, quiet bool
	var waitSec int
	fset.Var(&ifaces, "iface", "")
	fset.Var(&servers, "server", "")
	fset.BoolVar(&all, "all", false, "")
	fset.BoolVar(&asJSON, "json", false, "")
	fset.BoolVar(&quiet, "quiet", false, "")
	fset.IntVar(&waitSec, "wait", 0, "")
	pos, err := parseFlags(fset, args)
	if err != nil || len(pos) > 0 || waitSec < 0 {
		fmt.Fprint(a.stderr, wanUsage)
		return exitUsage
	}
	c, issues, code := a.loadConfig(false)
	if code != exitOK {
		a.printIssues(issues)
		return code
	}
	for _, i := range ifaces {
		if err := config.ValidIface(i); err != nil {
			fmt.Fprintf(a.stderr, "qnetspeed wan: %v\n", err)
			return exitUsage
		}
	}
	s, bad := a.wanSettings(c, servers)
	if bad != nil {
		fmt.Fprintf(a.stderr, "qnetspeed wan: %v\n", bad)
		return exitUsage
	}
	list := []string(ifaces)
	if len(list) == 0 { // --all and the default both mean WAN_IFACES; empty means the default route
		list = strings.Fields(c.Get("WAN_IFACES"))
	}
	if !a.needRoot(c) {
		return exitTestError
	}
	l, code := a.acquire(c, "wan", waitSec)
	if l == nil {
		return code
	}
	defer l.Release()

	sys := a.sys()
	eng := &wan.Engine{Runner: sys.run, Net: sys.net, Bin: wan.DefaultBin(a.binDir),
		Version: wanEngineVersion, Now: sys.now, EchoIP: wan.EchoHTTP, Inbound: a.inbound}
	ctx, stop := interruptible()
	defer stop()

	if !quiet && !asJSON {
		fmt.Fprintln(a.stdout, tr(c, "Testing the internet connection (about "+strconv.Itoa(s.Duration+15)+" s per link)...",
			"Test połączenia z internetem (ok. "+strconv.Itoa(s.Duration+15)+" s na łącze)..."))
	}
	recs := eng.RunAll(ctx, s, list, "cli", func(r wan.Record) {
		a.store(a.layout.WanHistory(), c, r)
		switch {
		case asJSON:
			b, _ := json.Marshal(r)
			fmt.Fprintf(a.stdout, "%s\n", b)
		case !quiet:
			a.printWan(c, r)
		}
	})
	return wanExit(recs, ctx.Err() != nil)
}

// wanEngineVersion is the pinned librespeed-cli (third_party/versions.env); it
// is recorded in history so a change of engine can be told from a change of link.
const wanEngineVersion = "1.0.14"

func wanExit(recs []wan.Record, interrupted bool) int {
	if interrupted {
		return exitTestError
	}
	code := exitOK
	for _, r := range recs {
		switch {
		case r.TimedOut:
			return exitTimeout
		case r.Status != wan.StatusOK:
			code = exitTestError
		}
	}
	return code
}

// wanSettings reads the WAN_* keys; --server overrides WAN_SERVER_IDS.
func (a *app) wanSettings(c *config.Config, servers []string) (wan.Settings, error) {
	s := wan.Settings{
		Duration:   c.Int("WAN_DURATION"),
		BindMode:   c.Get("WAN_BIND_MODE"),
		ServerList: c.Get("WAN_SERVER_LIST"),
		Secure:     c.Get("WAN_SECURE") == "1",
		ProbeIP:    net.ParseIP(c.Get("WAN_PROBE_IP")),
		IPEchoURL:  c.Get("WAN_IP_ECHO_URL"),
		IperfPort:  c.Int("IPERF_PORT"),
	}
	ids := servers
	if len(ids) == 0 {
		ids = strings.Fields(strings.ReplaceAll(c.Get("WAN_SERVER_IDS"), ",", " "))
	}
	for _, v := range ids {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return s, fmt.Errorf("--server %q: expected a server number", v)
		}
		s.ServerIDs = append(s.ServerIDs, n)
	}
	return s, nil
}

func (a *app) printWan(c *config.Config, r wan.Record) {
	w := a.stdout
	switch r.Status {
	case wan.StatusOK:
		res := r.Results
		fmt.Fprintf(w, "%-8s %s  ↓ %.2f Mb/s  ↑ %.2f Mb/s  ping %.1f ms  jitter %.1f ms  (%s)\n",
			r.Interface, tr(c, "OK", "OK"), res.DownloadMbps, res.UploadMbps, res.PingMs, res.JitterMs, r.Server.Name)
	case wan.StatusSkipped:
		fmt.Fprintf(w, "%-8s %s: %s\n", r.Interface, tr(c, "skipped", "pominięto"), deref(r.Error))
	default:
		fmt.Fprintf(w, "%-8s %s: %s\n", r.Interface, tr(c, "ERROR", "BŁĄD"), deref(r.Error))
	}
	for _, code := range r.Warnings {
		fmt.Fprintf(a.stderr, "%s\n", warningText(c, code, r))
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func warningText(c *config.Config, code string, r wan.Record) string {
	switch code {
	case wan.WarnRouteMismatch:
		egress := ""
		if r.Route != nil {
			egress = r.Route.EgressIface
		}
		return fmt.Sprintf(tr(c,
			"warning: traffic from %s leaves through %s — set up source-based routing in Network & Virtual Switch",
			"ostrzeżenie: ruch z %s wychodzi przez %s — skonfiguruj routing zależny od źródła w Network & Virtual Switch"),
			r.Interface, egress)
	case wan.WarnSamePublicIP:
		return tr(c, "warning: this link shows the same public IP as another tested link ("+r.Interface+")",
			"ostrzeżenie: to łącze ma ten sam publiczny adres IP co inne testowane łącze ("+r.Interface+")")
	case wan.WarnConcurrent:
		return tr(c, "warning: an incoming iperf3 test was running; the result may be distorted",
			"ostrzeżenie: w trakcie pomiaru trwał test przychodzący iperf3; wynik mógł zostać zaburzony")
	}
	return "warning: " + code
}
