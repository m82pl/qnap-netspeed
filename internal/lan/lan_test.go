package lan

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

	"github.com/m82pl/qnap-netspeed/internal/link"
	"github.com/m82pl/qnap-netspeed/internal/netroute"
	"github.com/m82pl/qnap-netspeed/internal/runner"
)

func td(t *testing.T, name string) runner.Result {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../../testdata/iperf3/3.22", name))
	if err != nil {
		t.Fatal(err)
	}
	return runner.Result{Stdout: b}
}

func TestParseOutputTCP(t *testing.T) {
	o, err := ParseOutput(td(t, "synthetic-tcp-upload.json"))
	if err != nil || o.ReceivedBps != 2315200000 || *o.Retransmits != 3 || *o.CPUHost != 41.0 || o.Version != "3.22" {
		t.Fatalf("%+v %v", o, err)
	}
	if o.JitterMs != nil || o.LostPercent != nil {
		t.Error("TCP has no jitter / loss")
	}
}

func TestParseOutputUDP(t *testing.T) {
	o, err := ParseOutput(td(t, "synthetic-udp.json"))
	if err != nil || o.ReceivedBps != 100000000 || *o.JitterMs != 0.031 || *o.LostPercent != 0.0046 {
		t.Fatalf("%+v %v", o, err)
	}
}

func TestParseOutputErrors(t *testing.T) {
	_, err := ParseOutput(td(t, "synthetic-error-refused.json"))
	if err == nil || err.Error() != "unable to connect to server: Connection refused" {
		t.Errorf("%v", err)
	}
	for name, res := range map[string]runner.Result{
		"empty":        {ExitCode: 1},
		"stderr":       {ExitCode: 1, Stderr: []byte("iperf3: command not found\n")},
		"garbage":      {Stdout: []byte("iperf3: error - ...")},
		"empty result": {Stdout: []byte(`{"start":{},"end":{}}`)},
	} {
		if _, err := ParseOutput(res); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestPasses(t *testing.T) {
	p := Params{Host: "192.168.1.105", Port: 5288, Duration: 10, Parallel: 4}
	got := Passes(p, "")
	if len(got) != 2 || got[0].Name != "upload" || got[1].Name != "download" {
		t.Fatalf("%+v", got)
	}
	if want := "-c 192.168.1.105 -p 5288 -J -t 10 -P 4"; strings.Join(got[0].Args, " ") != want {
		t.Errorf("upload: %v", got[0].Args)
	}
	if want := "-c 192.168.1.105 -p 5288 -J -t 10 -P 4 -R"; strings.Join(got[1].Args, " ") != want {
		t.Errorf("download: %v", got[1].Args)
	}
	p.UDP, p.Bitrate = true, "1G"
	got = Passes(p, "192.168.1.10")
	if len(got) != 3 || strings.Join(got[2].Args, " ") != "-c 192.168.1.105 -p 5288 -J -t 10 -B 192.168.1.10 -u -b 1G" {
		t.Errorf("%+v", got)
	}
	if got[0].Args[len(got[0].Args)-2] != "-B" && !contains(got[0].Args, "-B") {
		t.Error("-B missing")
	}
	p.Bidir = true
	if got = Passes(p, ""); got[0].Name != "bidir" || !contains(got[0].Args, "--bidir") || len(got) != 2 {
		t.Errorf("%+v", got)
	}
}

func contains(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}

type seqRunner struct {
	res   []runner.Result
	err   error
	calls [][]string
}

func (s *seqRunner) Run(_ context.Context, _ time.Duration, _ string, args ...string) (runner.Result, error) {
	s.calls = append(s.calls, args)
	if s.err != nil {
		return runner.Result{}, s.err
	}
	r := s.res[0]
	if len(s.res) > 1 {
		s.res = s.res[1:]
	}
	return r, nil
}

type fakeNet struct{ addrs []net.IP }

func (f fakeNet) IPv4Addrs(string) ([]net.IP, error) { return f.addrs, nil }
func (f fakeNet) RouteGet(src, dst net.IP) (netroute.Route, error) {
	return netroute.Route{EgressIface: "qvs0"}, nil
}
func (f fakeNet) HasSourceRule(net.IP) (bool, error) { return false, nil }

type fakeSys struct{}

func (fakeSys) Resolve(string) []link.Physical {
	s := 2500
	return []link.Physical{{Name: "eth0", SpeedMbps: &s, Duplex: "full", MTU: 1500}}
}
func (fakeSys) MTU(string) int { return 1500 }

func eng(r runner.Runner) *Engine {
	return &Engine{Runner: r, Net: fakeNet{[]net.IP{net.ParseIP("192.168.1.10")}}, Sys: fakeSys{}, Bin: "iperf3",
		Now: time.Now}
}

var params = Params{Host: "192.168.1.105", Port: 5288, Duration: 10, Parallel: 4}

func TestRunTCP(t *testing.T) {
	r := &seqRunner{res: []runner.Result{td(t, "synthetic-tcp-upload.json"), td(t, "synthetic-tcp-download.json")}}
	rec := eng(r).Run(context.Background(), params, "cli")
	if rec.Status != "ok" || rec.Error != nil || rec.Type != "LAN" || rec.Target != "192.168.1.105" {
		t.Fatalf("%+v", rec)
	}
	res := rec.Results
	if res.UploadMbps != 2315.2 || res.DownloadMbps != 2340.5 || *res.RetransmitsUpload != 3 {
		t.Errorf("%+v", res)
	}
	if res.RetransmitsDownload != nil { // spec: never a false zero
		t.Error("retransmits_download must be null")
	}
	if res.JitterMs != nil || res.LostPercent != nil || *res.CPULocalPercent != 41.0 {
		t.Errorf("%+v", res)
	}
	if rec.Interface != "qvs0" || rec.MTU != 1500 || len(rec.Physical) != 1 || rec.Physical[0].Name != "eth0" {
		t.Errorf("interface resolution: %+v", rec)
	}
	if rec.Params.Protocol != "tcp" || rec.Engine.Version != "3.22" || len(rec.Warnings) != 0 || len(r.calls) != 2 {
		t.Errorf("%+v calls=%d", rec, len(r.calls))
	}
}

func TestRunUDP(t *testing.T) {
	r := &seqRunner{res: []runner.Result{td(t, "synthetic-tcp-upload.json"), td(t, "synthetic-tcp-download.json"), td(t, "synthetic-udp.json")}}
	p := params
	p.UDP, p.Bitrate = true, "100M"
	rec := eng(r).Run(context.Background(), p, "cli")
	if rec.Status != "ok" || *rec.Results.JitterMs != 0.031 || *rec.Results.LostPercent != 0.0046 || rec.Params.Protocol != "tcp+udp" {
		t.Fatalf("%+v %+v", rec, rec.Results)
	}
	if rec.Results.UploadMbps != 2315.2 { // the UDP pass must not overwrite TCP results
		t.Errorf("%+v", rec.Results)
	}
}

func TestRunBidir(t *testing.T) {
	p := params
	p.Bidir = true
	rec := eng(&seqRunner{res: []runner.Result{td(t, "synthetic-bidir.json")}}).Run(context.Background(), p, "cli")
	if rec.Status != "ok" || rec.Results.UploadMbps != 938 || rec.Results.DownloadMbps != 939 {
		t.Fatalf("%+v %+v", rec, rec.Results)
	}
}

func TestRunWithIfaceBindsAddress(t *testing.T) {
	r := &seqRunner{res: []runner.Result{td(t, "synthetic-tcp-upload.json")}}
	p := params
	p.Iface = "eth0"
	rec := eng(r).Run(context.Background(), p, "cli")
	if rec.Interface != "eth0" || !contains(r.calls[0], "192.168.1.10") || !contains(r.calls[0], "-B") {
		t.Fatalf("%+v %v", rec.Interface, r.calls[0])
	}
}

func TestRunIfaceWithoutIPv4(t *testing.T) {
	e := eng(&seqRunner{})
	e.Net = fakeNet{}
	p := params
	p.Iface = "eth0"
	rec := e.Run(context.Background(), p, "cli")
	if rec.Status != "error" || !strings.Contains(*rec.Error, "no usable IPv4") {
		t.Fatalf("%+v", rec)
	}
}

func TestRunErrors(t *testing.T) {
	rec := eng(&seqRunner{res: []runner.Result{td(t, "synthetic-error-refused.json")}}).Run(context.Background(), params, "cli")
	if rec.Status != "error" || !strings.Contains(*rec.Error, "upload pass: unable to connect") || rec.Results != nil {
		t.Fatalf("%+v", rec)
	}
	rec = eng(&seqRunner{err: runner.ErrTimeout}).Run(context.Background(), params, "cli")
	if !strings.Contains(*rec.Error, "did not finish within 40s") {
		t.Fatalf("%s", *rec.Error)
	}
	rec = eng(&seqRunner{err: errors.New("no such file")}).Run(context.Background(), params, "cron")
	if rec.Status != "error" || rec.Trigger != "cron" {
		t.Fatalf("%+v", rec)
	}
}

func TestRunWarnings(t *testing.T) {
	busy := td(t, "synthetic-tcp-upload.json")
	busy.Stdout = []byte(strings.Replace(string(busy.Stdout), `"host_total":41.0`, `"host_total":97.5`, 1))
	e := eng(&seqRunner{res: []runner.Result{busy}})
	e.Inbound = func(port int) bool { return port == 5288 }
	rec := e.Run(context.Background(), params, "cli")
	if !reflect.DeepEqual(rec.Warnings, []string{WarnCPULimited, WarnConcurrent}) {
		t.Fatalf("%v", rec.Warnings)
	}
}

func TestInboundFromProcTCP(t *testing.T) {
	const tbl = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:14A8 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0000
   1: 0A01A8C0:14A8 6901A8C0:D4F2 01 00000000:00000000 00:00000000 00000000     0        0 2 1 0000
   2: 0A01A8C0:22B8 6901A8C0:D4F3 01 00000000:00000000 00:00000000 00000000     0        0 3 1 0000
`
	if !InboundFromProcTCP(strings.NewReader(tbl), 5288) { // 0x14A8
		t.Error("established connection on 5288 not found")
	}
	if InboundFromProcTCP(strings.NewReader(tbl), 5201) {
		t.Error("false positive")
	}
	// A listening socket alone (state 0A) is not a test in progress.
	if InboundFromProcTCP(strings.NewReader(strings.Join(strings.Split(tbl, "\n")[:2], "\n")), 5288) {
		t.Error("LISTEN counted as a test")
	}
}

// Real iperf3 3.16+ reports the CPU time of all threads relative to one core:
// a 4-stream test on a 4-core NAS can show 318% without being CPU-limited.
func TestCPULimitScalesWithCores(t *testing.T) {
	res := func(total string) runner.Result {
		r := td(t, "synthetic-tcp-upload.json")
		r.Stdout = []byte(strings.Replace(string(r.Stdout), `"host_total":41.0`, `"host_total":`+total, 1))
		return r
	}
	run := func(cores, parallel int, total string) []string {
		e := eng(&seqRunner{res: []runner.Result{res(total)}})
		e.CPUs = cores
		p := params
		p.Parallel = parallel
		return e.Run(context.Background(), p, "cli").Warnings
	}
	if w := run(4, 4, "318.3"); len(w) != 0 {
		t.Errorf("318%% of 4 cores is not a limit: %v", w)
	}
	if w := run(4, 4, "370.0"); len(w) != 1 || w[0] != WarnCPULimited {
		t.Errorf("370%% of 4 cores is: %v", w)
	}
	if w := run(4, 1, "95.0"); len(w) != 1 {
		t.Errorf("one stream can use one core only: %v", w)
	}
	if w := run(2, 4, "150.0"); len(w) != 0 {
		t.Errorf("2 cores: 150%% is below 180%%: %v", w)
	}
}

func TestParseOutputUDPReceiverView(t *testing.T) {
	// iperf3 3.16+: jitter and loss are in end.sum_received (and end.sum).
	body := `{"start":{"version":"iperf 3.16"},"end":{"sum":{"bits_per_second":99989138,"jitter_ms":0.02,"lost_percent":0,"sender":true},` +
		`"sum_sent":{"bits_per_second":99989138,"jitter_ms":0,"lost_percent":0,"sender":true},` +
		`"sum_received":{"bits_per_second":99978942,"jitter_ms":0.0207,"lost_packets":3,"packets":763,"lost_percent":0.39,"sender":false}}}`
	o, err := ParseOutput(runner.Result{Stdout: []byte(body)})
	if err != nil || o.JitterMs == nil || *o.JitterMs != 0.0207 || o.LostPercent == nil || *o.LostPercent != 0.39 || o.ReceivedBps != 99978942 {
		t.Errorf("%+v %v", o, err)
	}
}

// The files captured-ci-* are real iperf3 3.22 output (testdata/iperf3/3.22/README.md).
func TestParseRealCaptures(t *testing.T) {
	up, err := ParseOutput(td(t, "captured-ci-tcp-upload.json"))
	if err != nil || mbps(up.ReceivedBps) != 165673.29 || *up.Retransmits != 4 || int(*up.CPUHost) != 251 || up.Version != "3.22" || up.JitterMs != nil {
		t.Errorf("upload: %+v %v", up, err)
	}
	down, err := ParseOutput(td(t, "captured-ci-tcp-download.json"))
	if err != nil || mbps(down.ReceivedBps) != 162679.31 {
		t.Errorf("download: %+v %v", down, err)
	}
	bi, err := ParseOutput(td(t, "captured-ci-tcp-bidir.json"))
	if err != nil || mbps(bi.ReceivedBps) != 73558.56 || bi.ReverseBps == nil || mbps(*bi.ReverseBps) != 73392.43 || *bi.Retransmits != 13 {
		t.Errorf("bidir: %+v %v", bi, err)
	}
	udp, err := ParseOutput(td(t, "captured-ci-udp.json"))
	if err != nil || mbps(udp.ReceivedBps) != 100.07 || udp.JitterMs == nil || *udp.JitterMs != 0.017142320191236689 || *udp.LostPercent != 0 {
		t.Errorf("udp: %+v %v", udp, err)
	}
	_, err = ParseOutput(td(t, "captured-ci-error-refused.json"))
	if err == nil || !strings.HasPrefix(err.Error(), "unable to connect to server") || !strings.HasSuffix(err.Error(), "Connection refused") {
		t.Errorf("refused: %v", err)
	}
}

func TestRunRealCapturesBidirAndUDP(t *testing.T) {
	p := params
	p.Bidir, p.UDP, p.Bitrate = true, true, "100M"
	e := eng(&seqRunner{res: []runner.Result{td(t, "captured-ci-tcp-bidir.json"), td(t, "captured-ci-udp.json")}})
	e.CPUs = 4
	rec := e.Run(context.Background(), p, "cli")
	r := rec.Results
	if rec.Status != "ok" || r.UploadMbps != 73558.56 || r.DownloadMbps != 73392.43 || r.JitterMs == nil || *r.RetransmitsUpload != 13 {
		t.Fatalf("%+v %+v", rec, r)
	}
	// 202% CPU with 8 streams on 4 cores is far from saturated.
	if len(rec.Warnings) != 0 {
		t.Errorf("warnings %v", rec.Warnings)
	}
}
