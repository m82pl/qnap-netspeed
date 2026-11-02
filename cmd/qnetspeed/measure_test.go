package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/lock"
	"github.com/m82pl/qnap-netspeed/internal/netroute"
	"github.com/m82pl/qnap-netspeed/internal/runner"
)

var fixedNow = time.Date(2026, 11, 2, 18, 50, 0, 0, time.UTC)

type fakeRun struct {
	byTool map[string][]runner.Result // keyed by base name of the program
	err    error
	calls  []string
}

func (f *fakeRun) Run(_ context.Context, _ time.Duration, name string, args ...string) (runner.Result, error) {
	f.calls = append(f.calls, filepath.Base(name)+" "+strings.Join(args, " "))
	if f.err != nil {
		return runner.Result{ExitCode: -1}, f.err
	}
	q := f.byTool[filepath.Base(name)]
	r := q[0]
	if len(q) > 1 {
		f.byTool[filepath.Base(name)] = q[1:]
	}
	return r, nil
}

type fakeNet struct{ addrs map[string]string }

func (f fakeNet) IPv4Addrs(i string) ([]net.IP, error) {
	a, ok := f.addrs[i]
	if !ok {
		return nil, errors.New("no such network interface")
	}
	if a == "" {
		return nil, nil
	}
	return []net.IP{net.ParseIP(a)}, nil
}
func (f fakeNet) RouteGet(src, _ net.IP) (netroute.Route, error) {
	if src == nil {
		return netroute.Route{EgressIface: "eth0", Table: "main"}, nil
	}
	for n, a := range f.addrs {
		if a == src.String() && n == "eth1" { // eth1 traffic leaves through eth0: no source routing
			return netroute.Route{EgressIface: "eth0", Gateway: "192.168.1.1", Table: "main"}, nil
		}
	}
	return netroute.Route{EgressIface: "eth0", Gateway: "192.168.1.1", Table: "main"}, nil
}
func (f fakeNet) HasSourceRule(net.IP) (bool, error) { return false, nil }

func lsOK(t *testing.T) runner.Result {
	b, err := os.ReadFile("../../testdata/librespeed/v1.0.14/synthetic-success.json")
	if err != nil {
		t.Fatal(err)
	}
	return runner.Result{Stdout: b}
}

func measureApp(t *testing.T, fr *fakeRun) (*app, *strings.Builder, *strings.Builder) {
	a, _, _ := testApp(t)
	var out, errOut strings.Builder
	a.stdout, a.stderr = &out, &errOut
	a.system = system{
		run: fr, net: fakeNet{map[string]string{"eth0": "192.168.1.10", "eth1": "10.0.0.5", "eth2": ""}},
		sysfs: fstest.MapFS{
			"eth0/operstate": {Data: []byte("up\n")}, "eth0/speed": {Data: []byte("1000\n")}, "eth0/duplex": {Data: []byte("full\n")},
			"eth0/mtu": {Data: []byte("1500\n")}, "eth0/device/x": {},
			"eth1/operstate": {Data: []byte("up\n")}, "eth1/speed": {Data: []byte("-1\n")}, "eth1/mtu": {Data: []byte("1500\n")}, "eth1/device/x": {},
			"lo/operstate": {Data: []byte("unknown\n")},
		},
		now: func() time.Time { return fixedNow }, euid: func() int { return 0 }, procTCP: "/nonexistent",
	}
	return a, &out, &errOut
}

func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func TestWanDefaultRoute(t *testing.T) {
	fr := &fakeRun{byTool: map[string][]runner.Result{"librespeed-cli": {lsOK(t)}}}
	a, out, errOut := measureApp(t, fr)
	if code := a.run([]string{"wan"}); code != exitOK {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out.String(), "↓ 850.40 Mb/s") || !strings.Contains(out.String(), "default") {
		t.Errorf("%q", out)
	}
	recs := readLines(t, a.layout.WanHistory())
	if len(recs) != 1 || recs[0]["status"] != "ok" || recs[0]["trigger"] != "cli" || recs[0]["interface"] != "default" {
		t.Fatalf("%v", recs)
	}
	if !strings.Contains(fr.calls[0], "librespeed-cli --json --no-icmp --duration 10 --timeout 15") || strings.Contains(fr.calls[0], "--source") {
		t.Errorf("%s", fr.calls[0])
	}
	if _, err := os.Stat(a.layout.LockFile()); err != nil {
		t.Error("lock file missing")
	}
	if h, _ := lock.Current(a.layout.LockFile()); h != nil {
		t.Error("lock must be released")
	}
}

func TestWanMultiWANWarnsAndSkips(t *testing.T) {
	fr := &fakeRun{byTool: map[string][]runner.Result{"librespeed-cli": {lsOK(t)}}}
	a, out, errOut := measureApp(t, fr)
	writeFile(t, a.layout.ConfigFile(), "WAN_IFACES=\"eth0 eth1 eth2\"\nLANG=pl\n")
	code := a.run([]string{"wan"})
	if code != exitTestError { // eth2 was skipped
		t.Fatalf("code %d", code)
	}
	recs := readLines(t, a.layout.WanHistory())
	if len(recs) != 3 || recs[2]["status"] != "skipped" || recs[2]["reason"] != "no_ipv4" {
		t.Fatalf("%v", recs)
	}
	if w, _ := recs[1]["warnings"].([]any); len(w) < 1 || w[0] != "route_mismatch" {
		t.Errorf("eth1 warnings: %v", recs[1]["warnings"])
	}
	if !strings.Contains(errOut.String(), "ruch z eth1 wychodzi przez eth0") || !strings.Contains(errOut.String(), "ten sam publiczny adres IP") {
		t.Errorf("stderr %q", errOut)
	}
	if !strings.Contains(out.String(), "pominięto") {
		t.Errorf("stdout %q", out)
	}
	if len(fr.calls) != 2 || !strings.Contains(fr.calls[1], "--source 10.0.0.5") {
		t.Errorf("%v", fr.calls)
	}
}

func TestWanIfaceFlagAndJSONAndQuiet(t *testing.T) {
	fr := &fakeRun{byTool: map[string][]runner.Result{"librespeed-cli": {lsOK(t)}}}
	a, out, _ := measureApp(t, fr)
	if code := a.run([]string{"wan", "--iface", "eth0", "--server", "50", "--json"}); code != exitOK {
		t.Fatalf("code %d", code)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(out.String()), &rec); err != nil {
		t.Fatalf("%q: %v", out, err)
	}
	if rec["interface"] != "eth0" || rec["server"].(map[string]any)["id"] != float64(50) {
		t.Errorf("%v", rec)
	}
	if !strings.Contains(fr.calls[0], "--server 50") {
		t.Errorf("%s", fr.calls[0])
	}
	// --json prints what was stored.
	stored := readLines(t, a.layout.WanHistory())[0]
	if stored["id"] != rec["id"] {
		t.Error("--json must print the history record")
	}

	a, out, _ = measureApp(t, &fakeRun{byTool: map[string][]runner.Result{"librespeed-cli": {lsOK(t)}}})
	if code := a.run([]string{"wan", "--quiet"}); code != exitOK || out.Len() != 0 {
		t.Errorf("code %d out %q", code, out)
	}
}

func TestWanErrorsExitCodes(t *testing.T) {
	a, _, errOut := measureApp(t, &fakeRun{byTool: map[string][]runner.Result{"librespeed-cli": {{Stdout: []byte("null\n")}}}})
	if code := a.run([]string{"wan"}); code != exitTestError {
		t.Errorf("null: code %d", code)
	}
	recs := readLines(t, a.layout.WanHistory())
	if recs[0]["status"] != "error" || !strings.Contains(recs[0]["error"].(string), "did not respond") {
		t.Errorf("%v (stderr %q)", recs[0], errOut)
	}
	a, _, _ = measureApp(t, &fakeRun{err: runner.ErrTimeout})
	if code := a.run([]string{"wan"}); code != exitTimeout {
		t.Errorf("timeout: code %d", code)
	}
}

func TestWanUsageAndPreconditions(t *testing.T) {
	a, _, errOut := measureApp(t, &fakeRun{})
	for _, args := range [][]string{{"wan", "extra"}, {"wan", "--bogus"}, {"wan", "--wait", "-1"}, {"wan", "--iface", "../x"}, {"wan", "--server", "abc"}} {
		if code := a.run(args); code != exitUsage {
			t.Errorf("%v: code %d", args, code)
		}
	}
	a, _, errOut = measureApp(t, &fakeRun{})
	a.system.euid = func() int { return 1000 }
	if code := a.run([]string{"wan"}); code != exitTestError || !strings.Contains(errOut.String(), "must run as root") {
		t.Errorf("non-root: code %d %q", code, errOut)
	}
	a, _, _ = measureApp(t, &fakeRun{})
	writeFile(t, a.layout.ConfigFile(), "WAN_DURATION=0\n")
	if code := a.run([]string{"wan"}); code != exitConfig {
		t.Errorf("bad config: code %d", code)
	}
}

func TestWanBusyLock(t *testing.T) {
	fr := &fakeRun{byTool: map[string][]runner.Result{"librespeed-cli": {lsOK(t)}}}
	a, _, errOut := measureApp(t, fr)
	l, err := lock.Acquire(a.layout.LockFile(), "lan-test", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if code := a.run([]string{"wan"}); code != exitBusy || !strings.Contains(errOut.String(), "another test is running: lan-test since") {
		t.Fatalf("code %d %q", code, errOut)
	}
	if len(fr.calls) != 0 {
		t.Error("test must not start while the lock is held")
	}
}

func TestLanTest(t *testing.T) {
	read := func(n string) runner.Result {
		b, err := os.ReadFile("../../testdata/iperf3/3.22/" + n)
		if err != nil {
			t.Fatal(err)
		}
		return runner.Result{Stdout: b}
	}
	fr := &fakeRun{byTool: map[string][]runner.Result{"iperf3": {read("synthetic-tcp-upload.json"), read("synthetic-tcp-download.json")}}}
	a, out, errOut := measureApp(t, fr)
	if code := a.run([]string{"lan-test", "192.168.1.105", "--time", "5", "--parallel", "2"}); code != exitOK {
		t.Fatalf("code %d %q", code, errOut)
	}
	if len(fr.calls) != 2 || fr.calls[0] != "iperf3 -c 192.168.1.105 -p 5288 -J -t 5 -P 2" || !strings.HasSuffix(fr.calls[1], "-R") {
		t.Errorf("%v", fr.calls)
	}
	if !strings.Contains(out.String(), "↑ 2315.20 Mb/s  ↓ 2340.50 Mb/s") {
		t.Errorf("%q", out)
	}
	recs := readLines(t, a.layout.LanHistory())
	if recs[0]["type"] != "LAN" || recs[0]["target"] != "192.168.1.105" || recs[0]["interface"] != "eth0" {
		t.Errorf("%v", recs[0])
	}
}

func TestLanTestValidation(t *testing.T) {
	a, _, _ := measureApp(t, &fakeRun{})
	for _, args := range [][]string{
		{"lan-test"}, {"lan-test", "a", "b"}, {"lan-test", "-evil"}, {"lan-test", "host;rm"}, {"lan-test", "h", "--bitrate", "1G;x"},
		{"lan-test", "h", "--time", "301"}, {"lan-test", "h", "--parallel", "33"}, {"lan-test", "h", "--iface", "e th"},
	} {
		if code := a.run(args); code != exitUsage {
			t.Errorf("%v: code %d", args, code)
		}
	}
}

func TestHistory(t *testing.T) {
	fr := &fakeRun{byTool: map[string][]runner.Result{"librespeed-cli": {lsOK(t)}}}
	a, out, errOut := measureApp(t, fr)
	if code := a.run([]string{"history"}); code != exitOK || !strings.Contains(out.String(), "no results yet") {
		t.Fatalf("empty: %d %q", code, out)
	}
	for i := 0; i < 3; i++ {
		a.run([]string{"wan", "--quiet"})
	}
	// A torn line must be skipped and reported.
	f, _ := os.OpenFile(a.layout.WanHistory(), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("{\"timestamp\":\"2026-1\n")
	f.Close()
	out.Reset()
	errOut.Reset()
	if code := a.run([]string{"history", "--last", "2"}); code != exitOK {
		t.Fatalf("code %d", code)
	}
	if n := strings.Count(out.String(), "850.40"); n != 2 {
		t.Errorf("want 2 rows, got %d: %q", n, out)
	}
	if !strings.Contains(errOut.String(), "1 damaged line(s) skipped") {
		t.Errorf("stderr %q", errOut)
	}
	out.Reset()
	a.run([]string{"history", "--json"})
	var j struct {
		Records      []map[string]any `json:"records"`
		SkippedLines int              `json:"skipped_lines"`
	}
	if err := json.Unmarshal([]byte(out.String()), &j); err != nil || len(j.Records) != 3 || j.SkippedLines != 1 {
		t.Fatalf("%v %+v", err, j)
	}
	out.Reset()
	a.run([]string{"history", "--csv", "--last", "1"})
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "timestamp,interface,trigger,status,download_mbps") ||
		!strings.Contains(lines[1], ",850.4,295.1,8.2,1.1,") {
		t.Errorf("%q", out)
	}
	// Filters.
	out.Reset()
	a.run([]string{"history", "--iface", "eth7"})
	if !strings.Contains(out.String(), "no results yet") {
		t.Errorf("%q", out)
	}
	out.Reset()
	if code := a.run([]string{"history", "--since", "1d"}); code != exitOK || strings.Contains(out.String(), "no results") {
		t.Errorf("since 1d: %d %q", code, out)
	}
	for _, args := range [][]string{{"history", "--type", "x"}, {"history", "--since", "5y"}, {"history", "--json", "--csv"}, {"history", "--last", "0"}} {
		if code := a.run(args); code != exitUsage {
			t.Errorf("%v: %d", args, code)
		}
	}
}

func TestHistoryWorksForNonRoot(t *testing.T) {
	a, _, _ := measureApp(t, &fakeRun{})
	a.system.euid = func() int { return 1000 }
	if code := a.run([]string{"history"}); code != exitOK {
		t.Errorf("code %d", code)
	}
	if code := a.run([]string{"link"}); code != exitOK {
		t.Errorf("link code %d", code)
	}
	if code := a.run([]string{"routes"}); code != exitOK {
		t.Errorf("routes code %d", code)
	}
}

func TestLinkCommand(t *testing.T) {
	a, out, errOut := measureApp(t, &fakeRun{})
	writeFile(t, a.layout.ConfigFile(), "LINK_EXPECT=\"eth0:2500 eth1:1000\"\n")
	if code := a.run([]string{"link"}); code != exitOK {
		t.Fatalf("%d %q", code, errOut)
	}
	s := out.String()
	if !strings.Contains(s, "1000 (expected 2500)") || !strings.Contains(s, "n/a") {
		t.Errorf("%q", s)
	}
	if !strings.Contains(errOut.String(), "eth0 negotiated less than expected") || strings.Contains(errOut.String(), "eth1") {
		t.Errorf("%q", errOut) // eth1 has speed -1: the driver does not report, so no false degradation
	}
	if _, err := os.Stat(a.layout.LinkHistory()); err == nil {
		t.Error("link.jsonl must be written only with --record")
	}
	out.Reset()
	if code := a.run([]string{"link", "--iface", "eth0", "--json", "--record"}); code != exitOK {
		t.Fatalf("code %d", code)
	}
	var j map[string]any
	if err := json.Unmarshal([]byte(out.String()), &j); err != nil || len(j["interfaces"].([]any)) != 1 || j["type"] != "LINK" {
		t.Fatalf("%v %q", err, out)
	}
	if len(readLines(t, a.layout.LinkHistory())) != 1 {
		t.Error("--record did not write")
	}
	if code := a.run([]string{"link", "--iface", "nope"}); code != exitTestError {
		t.Errorf("unknown iface: %d", code)
	}
}

func TestRoutesCommand(t *testing.T) {
	a, out, errOut := measureApp(t, &fakeRun{})
	writeFile(t, a.layout.ConfigFile(), "WAN_IFACES=\"eth0 eth1 eth2 eth9\"\n")
	if code := a.run([]string{"routes"}); code != exitOK {
		t.Fatalf("%d", code)
	}
	s := out.String()
	for _, want := range []string{"eth0", "192.168.1.10", "mismatch", "no IP"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %q", want, s)
		}
	}
	if !strings.Contains(errOut.String(), "traffic from eth1 leaves through eth0") {
		t.Errorf("%q", errOut)
	}
	out.Reset()
	a.run([]string{"routes", "--json"})
	var rows []routeRow
	if err := json.Unmarshal([]byte(out.String()), &rows); err != nil || len(rows) != 4 || rows[1].State != "mismatch" || rows[0].State != "ok" || rows[2].State != "no_ip" {
		t.Fatalf("%v %+v", err, rows)
	}
}

func TestParseSince(t *testing.T) {
	for in, want := range map[string]time.Duration{"90m": 90 * time.Minute, "12h": 12 * time.Hour, "7d": 168 * time.Hour, "2w": 336 * time.Hour} {
		if got, err := parseSince(in); err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	for _, in := range []string{"", "d", "0d", "-1d", "5y", "xd"} {
		if _, err := parseSince(in); err == nil {
			t.Errorf("%q must fail", in)
		}
	}
}
