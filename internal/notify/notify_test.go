package notify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/lan"
	"github.com/m82pl/qnap-netspeed/internal/link"
	"github.com/m82pl/qnap-netspeed/internal/netroute"
	"github.com/m82pl/qnap-netspeed/internal/runner"
	"github.com/m82pl/qnap-netspeed/internal/wan"
)

type entry struct {
	level int
	msg   string
}

type recorder struct {
	got  []entry
	fail bool
}

func (r *recorder) Log(level int, msg string) error {
	if r.fail {
		return errors.New("log_tool failed")
	}
	r.got = append(r.got, entry{level, msg})
	return nil
}

var t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func newState() *State { return &State{Schema: 1, Notify: map[string]*Entry{}} }

func problem(msg string) []Finding {
	return []Finding{{Rule: "link_degraded", Subject: "eth0", Problem: true, Level: LevelWarning, Message: msg,
		Recovery: "Link eth0: back to normal"}}
}
func fine() []Finding {
	return []Finding{{Rule: "link_degraded", Subject: "eth0", Level: LevelWarning, Recovery: "Link eth0: back to normal (2500 Mb/s)"}}
}

// A10: the warning is written once, not at every cron run, repeated after
// NOTIFY_REPEAT_HOURS, and closed by one info entry.
func TestDedupLifecycle(t *testing.T) {
	st, log := newState(), &recorder{}
	opt := func(d time.Duration) Options { return Options{Now: t0.Add(d), Repeat: 24 * time.Hour} }

	steps := []struct {
		name     string
		at       time.Duration
		findings []Finding
		want     []entry
	}{
		{"problem appears", 0, problem("Link eth0: 1000 Mb/s, expected 2500 Mb/s"), []entry{{1, "Link eth0: 1000 Mb/s, expected 2500 Mb/s"}}},
		{"6 h later, still bad", 6 * time.Hour, problem("Link eth0: 1000 Mb/s, expected 2500 Mb/s"), nil},
		{"18 h later, still bad", 18 * time.Hour, problem("x"), nil},
		{"24 h after the first entry", 24 * time.Hour, problem("Link eth0: 1000 Mb/s, expected 2500 Mb/s"), []entry{{1, "Link eth0: 1000 Mb/s, expected 2500 Mb/s"}}},
		{"30 h", 30 * time.Hour, problem("x"), nil},
		{"back to normal", 31 * time.Hour, fine(), []entry{{0, "Link eth0: back to normal (2500 Mb/s)"}}},
		{"still fine", 37 * time.Hour, fine(), nil},
		{"new problem after recovery is new again", 43 * time.Hour, problem("again"), []entry{{1, "again"}}},
	}
	for _, s := range steps {
		log.got = nil
		sent, errs := Apply(st, s.findings, opt(s.at), log)
		if len(errs) != 0 {
			t.Fatalf("%s: %v", s.name, errs)
		}
		if len(log.got) != len(s.want) || len(sent) != len(s.want) {
			t.Fatalf("%s: logged %v, want %v", s.name, log.got, s.want)
		}
		for i, w := range s.want {
			if log.got[i] != w {
				t.Errorf("%s: logged %v, want %v", s.name, log.got[i], w)
			}
		}
	}
}

func TestRecoveryUsesStoredTextWhenFindingHasNone(t *testing.T) {
	st, log := newState(), &recorder{}
	Apply(st, problem("bad"), Options{Now: t0, Repeat: time.Hour}, log)
	log.got = nil
	Apply(st, []Finding{{Rule: "link_degraded", Subject: "eth0"}}, Options{Now: t0.Add(time.Minute), Repeat: time.Hour}, log)
	if len(log.got) != 1 || log.got[0] != (entry{0, "Link eth0: back to normal"}) {
		t.Errorf("%v", log.got)
	}
	if len(st.Notify) != 0 {
		t.Errorf("state not cleared: %v", st.Notify)
	}
}

func TestFailedLogKeepsStateSoNextRunRetries(t *testing.T) {
	st, log := newState(), &recorder{fail: true}
	_, errs := Apply(st, problem("bad"), Options{Now: t0, Repeat: time.Hour}, log)
	if len(errs) != 1 || len(st.Notify) != 0 {
		t.Fatalf("errs %v state %v", errs, st.Notify)
	}
	log.fail = false
	Apply(st, problem("bad"), Options{Now: t0.Add(time.Minute), Repeat: time.Hour}, log)
	if len(log.got) != 1 {
		t.Errorf("not retried: %v", log.got)
	}
	// A failing recovery entry keeps the problem open.
	log.fail = true
	log.got = nil
	_, errs = Apply(st, fine(), Options{Now: t0.Add(2 * time.Minute), Repeat: time.Hour}, log)
	if len(errs) != 1 || len(st.Notify) != 1 {
		t.Errorf("errs %v state %v", errs, st.Notify)
	}
}

func TestDifferentSubjectsAreIndependent(t *testing.T) {
	st, log := newState(), &recorder{}
	fs := []Finding{
		{Rule: "wan_dl", Subject: "eth0", Problem: true, Level: 1, Message: "a"},
		{Rule: "wan_dl", Subject: "eth1", Problem: true, Level: 1, Message: "b"},
		{Rule: "wan_ul", Subject: "eth0", Problem: true, Level: 1, Message: "c"},
	}
	Apply(st, fs, Options{Now: t0, Repeat: time.Hour}, log)
	if len(log.got) != 3 || len(st.Notify) != 3 {
		t.Errorf("%v", log.got)
	}
	log.got = nil
	Apply(st, fs[:1], Options{Now: t0.Add(time.Minute), Repeat: time.Hour}, log)
	if len(log.got) != 0 {
		t.Errorf("unrelated problems must stay open: %v", log.got)
	}
}

func TestForgetsRulesNotEvaluatedForAMonth(t *testing.T) {
	st, log := newState(), &recorder{}
	Apply(st, problem("bad"), Options{Now: t0, Repeat: time.Hour}, log)
	Apply(st, nil, Options{Now: t0.Add(20 * 24 * time.Hour), Repeat: time.Hour}, log)
	if len(st.Notify) != 1 {
		t.Fatal("forgotten too early")
	}
	Apply(st, nil, Options{Now: t0.Add(31 * 24 * time.Hour), Repeat: time.Hour}, log)
	if len(st.Notify) != 0 || len(log.got) != 1 {
		t.Errorf("state %v log %v", st.Notify, log.got)
	}
}

func TestFormat(t *testing.T) {
	if got := Format("WAN eth0: ok"); got != "[qnap-netspeed] WAN eth0: ok" {
		t.Errorf("%q", got)
	}
	if got := Format("a\nb\t c\x00d"); got != "[qnap-netspeed] a b c d" {
		t.Errorf("control characters: %q", got)
	}
	long := Format(strings.Repeat("ż", 500))
	if n := len([]rune(long)); n != MaxMessage || !strings.HasSuffix(long, "…") {
		t.Errorf("%d runes: %q", n, long)
	}
}

type argRunner struct {
	args []string
	res  runner.Result
	err  error
}

func (a *argRunner) Run(_ context.Context, _ time.Duration, name string, args ...string) (runner.Result, error) {
	a.args = append([]string{name}, args...)
	return a.res, a.err
}

func TestLogToolCommandLine(t *testing.T) {
	r := &argRunner{}
	if err := (LogTool{Run: r}).Log(2, "WAN eth1: test failed 2x in a row: $(reboot); \"x\""); err != nil {
		t.Fatal(err)
	}
	want := []string{"/sbin/log_tool", "-t", "2", "-a", `[qnap-netspeed] WAN eth1: test failed 2x in a row: $(reboot); "x"`}
	if strings.Join(r.args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("%q", r.args)
	}
	r.res.ExitCode = 1
	if err := (LogTool{Run: r}).Log(0, "x"); err == nil {
		t.Error("exit code 1 must be an error")
	}
	r.res.ExitCode, r.err = -1, errors.New("no such file")
	if err := (LogTool{Run: r, Bin: "/x/log_tool"}).Log(0, "x"); err == nil || r.args[0] != "/x/log_tool" {
		t.Errorf("%v %q", err, r.args)
	}
}

func TestStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := LoadState(path)
	if err != nil || len(st.Notify) != 0 {
		t.Fatalf("missing file: %v %v", st, err)
	}
	st.Notify["wan_dl:eth0"] = &Entry{Since: t0, LastNotified: t0, LastSeen: t0, Level: 1, Message: "m"}
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", fi.Mode())
	}
	back, err := LoadState(path)
	if err != nil || back.Notify["wan_dl:eth0"] == nil || !back.Notify["wan_dl:eth0"].Since.Equal(t0) {
		t.Fatalf("%+v %v", back, err)
	}
	if err := os.WriteFile(path, []byte(`{"notify": [`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = LoadState(path)
	if !errors.Is(err, ErrCorrupt) || st == nil || st.Notify == nil || len(st.Notify) != 0 {
		t.Errorf("damaged file: %+v %v", st, err)
	}
	if err := os.WriteFile(path, []byte(`{"notify": {"a": null}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, err = LoadState(path); err != nil || len(st.Notify) != 0 {
		t.Errorf("null entry: %+v %v", st, err)
	}
}

// --- rules ---

func okWAN(iface string, dl, ul, ping float64) wan.Record {
	return wan.Record{Interface: iface, Status: wan.StatusOK, Warnings: []string{},
		Server:  &wan.Server{Name: "Poznan (INEA)"},
		Results: &wan.Results{DownloadMbps: dl, UploadMbps: ul, PingMs: ping}}
}

func byKey(fs []Finding) map[string]Finding {
	m := map[string]Finding{}
	for _, f := range fs {
		m[f.Key()] = f
	}
	return m
}

var defaults = Thresholds{MinWANDownload: 100, MinWANUpload: 30, MaxWANPing: 50, MinLAN: 0, FailThreshold: 2}

func TestWANThresholds(t *testing.T) {
	f := byKey(WANFindings(defaults, []wan.Record{okWAN("eth0", 42.14, 250, 80)}, nil))
	if d := f["wan_dl:eth0"]; !d.Problem || d.Level != 1 || d.Message != "WAN eth0: download 42.1 Mbps < 100 Mbps (server: Poznan (INEA))" {
		t.Errorf("%+v", d)
	}
	if p := f["wan_ping:eth0"]; !p.Problem || p.Message != "WAN eth0: ping 80.0 ms > 50 ms (server: Poznan (INEA))" {
		t.Errorf("%+v", p)
	}
	if u := f["wan_ul:eth0"]; u.Problem {
		t.Errorf("upload is fine: %+v", u)
	}
	if fl := f["wan_fail:eth0"]; fl.Problem || fl.Recovery == "" {
		t.Errorf("an OK test closes the failure rule: %+v", fl)
	}

	f = byKey(WANFindings(defaults, []wan.Record{okWAN("eth0", 850.4, 250, 10)}, nil))
	if d := f["wan_dl:eth0"]; d.Problem || d.Recovery != "WAN eth0: back to normal (download 850.4 Mbps)" {
		t.Errorf("%+v", d)
	}

	off := Thresholds{FailThreshold: 2}
	for k, v := range byKey(WANFindings(off, []wan.Record{okWAN("eth0", 1, 1, 9999)}, nil)) {
		if v.Problem {
			t.Errorf("threshold 0 switches the rule off, got problem %s", k)
		}
	}
}

func TestWANFailureNeedsAStreak(t *testing.T) {
	msg := "network is unreachable"
	bad := wan.Record{Interface: "eth1", Status: wan.StatusError, Error: &msg}
	streak := 1
	fs := WANFindings(defaults, []wan.Record{bad}, func(string) int { return streak })
	if len(fs) != 0 {
		t.Errorf("one failure is below the threshold: %+v", fs)
	}
	streak = 2
	f := byKey(WANFindings(defaults, []wan.Record{bad}, func(string) int { return streak }))["wan_fail:eth1"]
	if !f.Problem || f.Level != LevelError || f.Message != "WAN eth1: test failed 2x in a row: network is unreachable" {
		t.Errorf("%+v", f)
	}
	// A failed test says nothing about speed: those rules keep their state.
	if len(WANFindings(defaults, []wan.Record{bad}, func(string) int { return 2 })) != 1 {
		t.Error("only wan_fail expected for a failed test")
	}
}

func TestWANRouteAndSameIP(t *testing.T) {
	r := okWAN("eth1", 500, 100, 10)
	r.Route = &netroute.Route{EgressIface: "eth0", Mismatch: true}
	r.Warnings = []string{wan.WarnRouteMismatch, wan.WarnSamePublicIP}
	f := byKey(WANFindings(defaults, []wan.Record{r}, nil))
	if g := f["wan_route:eth1"]; !g.Problem || g.Message != "WAN eth1: traffic leaves via eth0, multi-WAN test not representative" {
		t.Errorf("%+v", g)
	}
	if g := f["wan_same_ip:eth1"]; !g.Problem || !strings.Contains(g.Message, "same public IP") {
		t.Errorf("%+v", g)
	}
	// No route information (lookup failed, default route): no verdict.
	if _, ok := byKey(WANFindings(defaults, []wan.Record{okWAN("default", 500, 100, 10)}, nil))["wan_route:default"]; ok {
		t.Error("route rule without a route")
	}
}

func TestLANMin(t *testing.T) {
	rec := func(up, down float64) lan.Record {
		return lan.Record{Target: "192.168.1.50", Status: "ok", Results: &lan.Results{UploadMbps: up, DownloadMbps: down}}
	}
	th := Thresholds{MinLAN: 900}
	got := LANFindings(th, []lan.Record{rec(940, 410)})
	if len(got) != 1 || !got[0].Problem || got[0].Message != "LAN 192.168.1.50: upload 940.0 / download 410.0 Mbps < 900 Mbps" {
		t.Errorf("%+v", got)
	}
	if got := LANFindings(th, []lan.Record{rec(940, 930)}); got[0].Problem {
		t.Errorf("%+v", got)
	}
	if got := LANFindings(Thresholds{}, []lan.Record{rec(1, 1)}); got[0].Problem {
		t.Error("MIN_LAN_MBPS=0 is off")
	}
	msg := "refused"
	if got := LANFindings(th, []lan.Record{{Target: "x", Status: "error", Error: &msg}}); len(got) != 0 {
		t.Errorf("a failed LAN test gives no verdict: %+v", got)
	}
}

func ip(v int) *int { return &v }

func TestLinkFindings(t *testing.T) {
	ifaces := []link.Interface{
		{Name: "eth0", Kind: link.KindPhysical, Operstate: "up", SpeedMbps: ip(1000), ExpectedMbps: ip(2500), Duplex: "full"},
		{Name: "eth1", Kind: link.KindPhysical, Operstate: "up", SpeedMbps: ip(1000), Duplex: "full"},
		{Name: "eth2", Kind: link.KindPhysical, Operstate: "down", ExpectedMbps: ip(1000)},
		{Name: "docker0", Kind: link.KindVirtual, Operstate: "up"},
	}
	f := byKey(LinkFindings(ifaces, []string{"link_degraded:eth0", "link_down:eth2"}))
	if g := f["link_degraded:eth0"]; !g.Problem || g.Message != "Link eth0: 1000 Mb/s, expected 2500 Mb/s" {
		t.Errorf("%+v", g)
	}
	if g := f["link_down:eth2"]; !g.Problem {
		t.Errorf("%+v", g)
	}
	if g, ok := f["link_degraded:eth1"]; !ok || g.Problem {
		t.Errorf("without an expectation the rule is still evaluated (so removing LINK_EXPECT closes an open problem): %+v", g)
	}
	if _, ok := f["link_degraded:eth2"]; ok {
		t.Error("unknown speed gives no verdict (no false recovery)")
	}
	if g := f["duplex_half:eth1"]; g.Problem {
		t.Errorf("%+v", g)
	}
	if _, ok := f["duplex_half:docker0"]; ok {
		t.Error("virtual interfaces have no duplex rule")
	}

	// After the cable is back at 2500: same rules, no problem.
	ifaces[0].SpeedMbps = ip(2500)
	f = byKey(LinkFindings(ifaces, nil))
	if g := f["link_degraded:eth0"]; g.Problem || g.Recovery != "Link eth0: back to normal (2500 Mb/s)" {
		t.Errorf("%+v", g)
	}
}

// LINK_EXPECT removed while link_degraded is open: the next run closes it.
func TestRemovingLinkExpectClosesOpenProblem(t *testing.T) {
	st, log := newState(), &recorder{}
	ifaces := []link.Interface{{Name: "eth0", Kind: link.KindPhysical, Operstate: "up", SpeedMbps: ip(1000), ExpectedMbps: ip(2500), Duplex: "full"}}
	Apply(st, LinkFindings(ifaces, []string{"link_degraded:eth0"}), Options{Now: t0, Repeat: time.Hour}, log)
	ifaces[0].ExpectedMbps = nil
	log.got = nil
	Apply(st, LinkFindings(ifaces, nil), Options{Now: t0.Add(time.Hour), Repeat: time.Hour}, log)
	if len(log.got) != 1 || log.got[0] != (entry{0, "Link eth0: back to normal (1000 Mb/s)"}) || len(st.Notify) != 0 {
		t.Errorf("%v %v", log.got, st.Notify)
	}
}
