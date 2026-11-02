package wan

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/netroute"
	"github.com/m82pl/qnap-netspeed/internal/runner"
)

const td = "../../testdata/librespeed/v1.0.14/"

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(td, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseOutputCapturedSuccess(t *testing.T) {
	r, err := ParseOutput(runner.Result{Stdout: read(t, "captured-ci-success.json")})
	if err != nil {
		t.Fatal(err)
	}
	want := Result{ServerName: "Los Angeles, USA (Sharktech)", ServerURL: "https://laxspeed.sharktech.net",
		Download: 6444.74, Upload: 1992.96, Ping: 8.26, Jitter: 0.05}
	if r != want { // Mb/s as printed: no division by 1e6
		t.Errorf("got %+v", r)
	}
}

func TestParseOutputClientIP(t *testing.T) {
	r, err := ParseOutput(runner.Result{Stdout: read(t, "synthetic-success.json")})
	if err != nil || r.ClientIP != "203.0.113.25" || r.Download != 850.4 || r.Upload != 295.1 {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = ParseOutput(runner.Result{Stdout: read(t, "synthetic-success-no-client-ip.json")})
	if err != nil || r.ClientIP != "" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestParseOutputFailures(t *testing.T) {
	tests := []struct {
		name string
		res  runner.Result
		want string
	}{
		{"server not responding: exit 0, null", runner.Result{Stdout: read(t, "captured-ci-server-not-responding.stdout")}, "did not respond"},
		{"empty array", runner.Result{Stdout: []byte("[]\n")}, "did not respond"},
		{"no server", runner.Result{ExitCode: 1, Stderr: read(t, "captured-ci-no-server.stderr")}, "No server is currently available"},
		{"unknown id", runner.Result{ExitCode: 1, Stderr: read(t, "captured-ci-unknown-server-id.stderr")}, "unexpected end of JSON input"},
		{"exit 1 without stderr", runner.Result{ExitCode: 1}, "exited with code 1"},
		{"empty stdout", runner.Result{}, "printed nothing"},
		{"garbage", runner.Result{Stdout: []byte("<html>")}, "cannot parse"},
		{"object, not array", runner.Result{Stdout: []byte(`{"download":1}`)}, "cannot parse"},
		{"missing fields", runner.Result{Stdout: []byte(`[{"download":1,"upload":2}]`)}, "lacks"},
	}
	for _, tc := range tests {
		_, err := ParseOutput(tc.res)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	// "Terminated due to error: " prefix is stripped.
	_, err := ParseOutput(runner.Result{ExitCode: 1, Stderr: read(t, "captured-ci-unknown-server-id.stderr")})
	if strings.HasPrefix(err.Error(), "Terminated") {
		t.Errorf("prefix kept: %v", err)
	}
}

func TestParseOutputZeroSpeedIsValid(t *testing.T) {
	r, err := ParseOutput(runner.Result{Stdout: []byte(`[{"server":{"name":"x","url":"u"},"ping":0,"jitter":0,"upload":0,"download":0}]`)})
	if err != nil || r.Download != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestArgs(t *testing.T) {
	addr := net.ParseIP("192.168.1.10")
	base := Settings{Duration: 10, BindMode: "source"}
	tests := []struct {
		name  string
		s     Settings
		iface string
		want  []string
	}{
		{"default route", base, "", []string{"--json", "--no-icmp", "--duration", "10", "--timeout", "15"}},
		{"source", base, "eth0", []string{"--json", "--no-icmp", "--duration", "10", "--timeout", "15", "--source", "192.168.1.10"}},
		{"interface", Settings{Duration: 5, BindMode: "interface"}, "eth1", []string{"--json", "--no-icmp", "--duration", "5", "--timeout", "15", "--interface", "eth1"}},
		{"servers, secure, list", Settings{Duration: 10, BindMode: "source", ServerIDs: []int{3, 7}, Secure: true, ServerList: "/x/s.json"}, "eth0",
			[]string{"--json", "--no-icmp", "--duration", "10", "--timeout", "15", "--server", "3", "--server", "7", "--source", "192.168.1.10", "--secure", "--local-json", "/x/s.json"}},
	}
	for _, tc := range tests {
		if got := Args(tc.s, tc.iface, addr); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %v\nwant %v", tc.name, got, tc.want)
		}
	}
	for _, a := range Args(base, "eth0", addr) {
		if a == "--share" || strings.HasPrefix(a, "--telemetry") {
			t.Errorf("forbidden flag %s", a)
		}
	}
}

func TestTimeout(t *testing.T) {
	if Timeout(10) != 80*time.Second {
		t.Error(Timeout(10))
	}
}

// --- engine ---

type fakeRunner struct {
	res   map[string]runner.Result // keyed by the value after --source/--interface ("" = none)
	err   error
	calls [][]string
}

func (f *fakeRunner) Run(_ context.Context, _ time.Duration, name string, args ...string) (runner.Result, error) {
	f.calls = append(f.calls, args)
	if f.err != nil {
		return runner.Result{ExitCode: -1}, f.err
	}
	key := ""
	for i, a := range args {
		if a == "--source" || a == "--interface" {
			key = args[i+1]
		}
	}
	return f.res[key], nil
}

type fakeNet struct {
	addrs  map[string]string
	egress map[string]string
}

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
	return netroute.Route{EgressIface: f.egress[src.String()], Gateway: "192.168.1.1", Table: "main"}, nil
}
func (f fakeNet) HasSourceRule(net.IP) (bool, error) { return false, nil }

func engine(r runner.Runner, n netroute.System) *Engine {
	now := time.Date(2026, 11, 2, 18, 50, 0, 0, time.UTC)
	return &Engine{Runner: r, Net: n, Bin: "/bin/librespeed-cli", Version: "1.0.14",
		Now: func() time.Time { now = now.Add(12 * time.Second); return now }}
}

var settings = Settings{Duration: 10, BindMode: "source", ProbeIP: net.ParseIP("1.1.1.1")}

func ok(t *testing.T) runner.Result {
	return runner.Result{Stdout: read(t, "synthetic-success.json")}
}

func TestRunInterfaceOK(t *testing.T) {
	n := fakeNet{addrs: map[string]string{"eth0": "192.168.1.10"}, egress: map[string]string{"192.168.1.10": "eth0"}}
	s := settings
	s.ServerIDs = []int{50}
	rec := engine(&fakeRunner{res: map[string]runner.Result{"192.168.1.10": ok(t)}}, n).RunInterface(context.Background(), s, "eth0", "cli")
	if rec.Status != StatusOK || rec.Interface != "eth0" || rec.Trigger != "cli" || rec.Type != "WAN" || rec.Schema != 1 {
		t.Fatalf("%+v", rec)
	}
	if rec.Results.DownloadMbps != 850.4 || rec.Results.JitterMs != 1.1 || *rec.PublicIP != "203.0.113.25" {
		t.Errorf("%+v", rec.Results)
	}
	if rec.Server.ID == nil || *rec.Server.ID != 50 {
		t.Errorf("single --server id must be recorded: %+v", rec.Server)
	}
	if rec.Bind.Address != "192.168.1.10" || rec.Route.Mismatch || len(rec.Warnings) != 0 || rec.Error != nil {
		t.Errorf("%+v", rec)
	}
	if rec.DurationS != 12 || len(rec.ID) != 26 || rec.Engine.Version != "1.0.14" {
		t.Errorf("%+v", rec)
	}
}

func TestRunInterfaceDefaultRouteHasNoBinding(t *testing.T) {
	fr := &fakeRunner{res: map[string]runner.Result{"": ok(t)}}
	rec := engine(fr, fakeNet{}).RunInterface(context.Background(), settings, "", "cron")
	if rec.Status != StatusOK || rec.Interface != "default" || rec.Bind != nil || rec.Route != nil || rec.Server.ID != nil {
		t.Fatalf("%+v", rec)
	}
}

func TestRunInterfaceMismatchKeepsResult(t *testing.T) {
	n := fakeNet{addrs: map[string]string{"eth1": "10.0.0.5"}, egress: map[string]string{"10.0.0.5": "eth0"}}
	rec := engine(&fakeRunner{res: map[string]runner.Result{"10.0.0.5": ok(t)}}, n).RunInterface(context.Background(), settings, "eth1", "cli")
	if rec.Status != StatusOK || !reflect.DeepEqual(rec.Warnings, []string{WarnRouteMismatch}) || !rec.Route.Mismatch || rec.Route.EgressIface != "eth0" {
		t.Fatalf("%+v", rec)
	}
}

func TestRunInterfaceSkipped(t *testing.T) {
	n := fakeNet{addrs: map[string]string{"eth1": ""}}
	fr := &fakeRunner{}
	e := engine(fr, n)
	rec := e.RunInterface(context.Background(), settings, "eth1", "cli")
	if rec.Status != StatusSkipped || rec.Reason != "no_ipv4" || rec.Error == nil || len(fr.calls) != 0 {
		t.Fatalf("%+v calls=%v", rec, fr.calls)
	}
	rec = e.RunInterface(context.Background(), settings, "eth9", "cli")
	if rec.Status != StatusSkipped || rec.Reason != "no_iface" {
		t.Fatalf("%+v", rec)
	}
}

func TestRunInterfaceErrors(t *testing.T) {
	n := fakeNet{addrs: map[string]string{"eth0": "192.168.1.10"}, egress: map[string]string{"192.168.1.10": "eth0"}}
	tests := []struct {
		name string
		fr   *fakeRunner
		want string
	}{
		{"null", &fakeRunner{res: map[string]runner.Result{"192.168.1.10": {Stdout: []byte("null\n")}}}, "did not respond"},
		{"exit 1", &fakeRunner{res: map[string]runner.Result{"192.168.1.10": {ExitCode: 1, Stderr: []byte("boom\n")}}}, "boom"},
		{"timeout", &fakeRunner{err: runner.ErrTimeout}, "did not finish within 1m20s"},
		{"cannot start", &fakeRunner{err: errors.New("fork/exec: no such file")}, "no such file"},
	}
	for _, tc := range tests {
		rec := engine(tc.fr, n).RunInterface(context.Background(), settings, "eth0", "cli")
		if rec.Status != StatusError || rec.Error == nil || !strings.Contains(*rec.Error, tc.want) || rec.Results != nil {
			t.Errorf("%s: %+v", tc.name, rec)
		}
	}
}

func TestRunAllSamePublicIP(t *testing.T) {
	n := fakeNet{addrs: map[string]string{"eth0": "192.168.1.10", "eth1": "10.0.0.5"},
		egress: map[string]string{"192.168.1.10": "eth0", "10.0.0.5": "eth1"}}
	fr := &fakeRunner{res: map[string]runner.Result{"192.168.1.10": ok(t), "10.0.0.5": ok(t)}}
	var seen []string
	recs := engine(fr, n).RunAll(context.Background(), settings, []string{"eth0", "eth1"}, "cron", func(r Record) { seen = append(seen, r.Interface) })
	if len(recs) != 2 || !reflect.DeepEqual(seen, []string{"eth0", "eth1"}) {
		t.Fatalf("%d %v", len(recs), seen)
	}
	if len(recs[0].Warnings) != 0 || !reflect.DeepEqual(recs[1].Warnings, []string{WarnSamePublicIP}) {
		t.Errorf("%v / %v", recs[0].Warnings, recs[1].Warnings)
	}
}

func TestRunAllNoIfacesMeansDefaultRoute(t *testing.T) {
	fr := &fakeRunner{res: map[string]runner.Result{"": ok(t)}}
	recs := engine(fr, fakeNet{}).RunAll(context.Background(), settings, nil, "cli", nil)
	if len(recs) != 1 || recs[0].Interface != "default" {
		t.Fatalf("%+v", recs)
	}
}

func TestRunAllStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if recs := engine(&fakeRunner{}, fakeNet{}).RunAll(ctx, settings, []string{"eth0"}, "cli", nil); len(recs) != 0 {
		t.Fatalf("%d", len(recs))
	}
}

func TestEchoIPUsedWhenClientIPMissing(t *testing.T) {
	n := fakeNet{addrs: map[string]string{"eth0": "192.168.1.10"}, egress: map[string]string{"192.168.1.10": "eth0"}}
	fr := &fakeRunner{res: map[string]runner.Result{"192.168.1.10": {Stdout: read(t, "synthetic-success-no-client-ip.json")}}}
	e := engine(fr, n)
	var gotSrc net.IP
	e.EchoIP = func(_ context.Context, url string, src net.IP) (string, error) {
		gotSrc = src
		return "198.51.100.7", nil
	}

	rec := e.RunInterface(context.Background(), settings, "eth0", "cli") // no WAN_IP_ECHO_URL: no request
	if rec.PublicIP != nil || gotSrc != nil {
		t.Fatalf("echo must be off by default: %+v", rec.PublicIP)
	}
	s := settings
	s.IPEchoURL = "https://echo.example/ip"
	rec = e.RunInterface(context.Background(), s, "eth0", "cli")
	if rec.PublicIP == nil || *rec.PublicIP != "198.51.100.7" || gotSrc.String() != "192.168.1.10" {
		t.Fatalf("%v src=%v", rec.PublicIP, gotSrc)
	}
	e.EchoIP = func(context.Context, string, net.IP) (string, error) { return "", errors.New("down") }
	if rec = e.RunInterface(context.Background(), s, "eth0", "cli"); rec.Status != StatusOK || rec.PublicIP != nil {
		t.Errorf("echo failure must not fail the test: %+v", rec)
	}
}
