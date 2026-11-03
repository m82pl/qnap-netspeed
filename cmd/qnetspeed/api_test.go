package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/iperfsrv"
	"github.com/m82pl/qnap-netspeed/internal/runner"
	"github.com/m82pl/qnap-netspeed/internal/web"
)

const apiToken = "api-test-token-0123456789abcdef0123456789abcdef"

type stubLAN struct{ st iperfsrv.Status }

func (s *stubLAN) Status() iperfsrv.Status { return s.st }
func (s *stubLAN) Start()                  { s.st.State = iperfsrv.StateRunning; s.st.PID = 4242 }
func (s *stubLAN) Stop()                   { s.st.State, s.st.PID = iperfsrv.StateStopped, 0 }

// apiServer serves the real API over a measureApp (fake iperf3/librespeed, fake /sys).
// The config points WEB_BIND/WEB_PORT at the test server, as the lan-server command expects.
func apiServer(t *testing.T, fr *fakeRun) (*app, *httptest.Server, *stubLAN) {
	t.Helper()
	a, _, _ := measureApp(t, fr)
	stub := &stubLAN{st: iperfsrv.Status{State: iperfsrv.StateStopped, Port: 5288}}
	srv := web.New(web.Options{
		Info:    web.Info{Version: "t", Started: time.Now(), LockFile: a.layout.LockFile()},
		Backend: &backend{a: a}, Token: apiToken, LANServer: stub,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Close() })
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(ts.URL, "http://"))
	writeFile(t, a.layout.ConfigFile(), "WEB_BIND="+host+"\nWEB_PORT="+port+"\n")
	writeFile(t, a.layout.TokenFile(), apiToken+"\n")
	return a, ts, stub
}

func apiDo(t *testing.T, ts *httptest.Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func waitTest(t *testing.T, ts *httptest.Server, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, j := apiDo(t, ts, "GET", "/api/v1/tests/"+id, nil)
		if j["state"] != "running" {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("test did not finish")
	return nil
}

func iperfResults(t *testing.T) *fakeRun {
	read := func(n string) runner.Result {
		b, err := os.ReadFile("../../testdata/iperf3/3.22/" + n)
		if err != nil {
			t.Fatal(err)
		}
		return runner.Result{Stdout: b}
	}
	return &fakeRun{byTool: map[string][]runner.Result{"iperf3": {read("synthetic-tcp-upload.json"), read("synthetic-tcp-download.json")}}}
}

func TestAPILanTestWritesHistoryWithTriggerWeb(t *testing.T) {
	fr := iperfResults(t)
	a, ts, _ := apiServer(t, fr)
	code, acc := apiDo(t, ts, "POST", "/api/v1/tests/lan", map[string]any{"target": "192.168.1.105", "time": 5, "parallel": 2})
	if code != 202 {
		t.Fatalf("%d %v", code, acc)
	}
	j := waitTest(t, ts, acc["test_id"].(string))
	if j["state"] != "done" {
		t.Fatalf("job %v", j)
	}
	res := j["results"].([]any)[0].(map[string]any)
	if res["status"] != "ok" || res["trigger"] != "web" || res["target"] != "192.168.1.105" {
		t.Errorf("result %v", res)
	}
	if len(fr.calls) != 2 || fr.calls[0] != "iperf3 -c 192.168.1.105 -p 5288 -J -t 5 -P 2" {
		t.Errorf("calls %v", fr.calls)
	}
	recs := readLines(t, a.layout.LanHistory())
	if len(recs) != 1 || recs[0]["trigger"] != "web" {
		t.Errorf("history %v", recs)
	}
	// Defaults come from the configuration when time/parallel are left out.
	fr2 := iperfResults(t)
	*fr = *fr2
	fr.calls = nil
	_, acc = apiDo(t, ts, "POST", "/api/v1/tests/lan", map[string]any{"target": "nas.local"})
	waitTest(t, ts, acc["test_id"].(string))
	if len(fr.calls) < 1 || !strings.Contains(fr.calls[0], "-t 10 -P 4") {
		t.Errorf("defaults: %v", fr.calls)
	}
}

func TestAPIWanTest(t *testing.T) {
	fr := &fakeRun{byTool: map[string][]runner.Result{}}
	fr.byTool["librespeed-cli"] = []runner.Result{lsOK(t)}
	a, ts, _ := apiServer(t, fr)
	code, acc := apiDo(t, ts, "POST", "/api/v1/tests/wan", map[string]any{"ifaces": []string{"eth0"}, "server_ids": []int{12}})
	if code != 202 {
		t.Fatalf("%d %v", code, acc)
	}
	j := waitTest(t, ts, acc["test_id"].(string))
	if j["state"] != "done" {
		t.Fatalf("job %v", j)
	}
	rec := j["results"].([]any)[0].(map[string]any)
	if rec["status"] != "ok" || rec["trigger"] != "web" || rec["interface"] != "eth0" {
		t.Errorf("record %v", rec)
	}
	if len(fr.calls) != 1 || !strings.Contains(fr.calls[0], "--server 12") {
		t.Errorf("calls %v", fr.calls)
	}
	if recs := readLines(t, a.layout.WanHistory()); len(recs) != 1 {
		t.Errorf("history %v", recs)
	}
}

func TestAPIReadEndpoints(t *testing.T) {
	a, ts, _ := apiServer(t, &fakeRun{})
	writeFile(t, a.layout.WanHistory(), `{"timestamp":"2026-11-02T10:00:00Z","type":"WAN","interface":"eth0","status":"ok","results":{"download_mbps":90}}`+"\nbroken line\n")
	resp, err := http.Get(ts.URL + "/api/v1/history?type=wan")
	if err != nil {
		t.Fatal(err)
	}
	var h struct {
		Records      []map[string]any `json:"records"`
		SkippedLines int              `json:"skipped_lines"`
	}
	json.NewDecoder(resp.Body).Decode(&h)
	resp.Body.Close()
	if len(h.Records) != 1 || h.SkippedLines != 1 {
		t.Errorf("history %+v", h)
	}
	resp, _ = http.Get(ts.URL + "/api/v1/link")
	var l map[string]any
	json.NewDecoder(resp.Body).Decode(&l)
	resp.Body.Close()
	if l["type"] != "LINK" || len(l["interfaces"].([]any)) < 2 {
		t.Errorf("link %v", l)
	}
	resp, _ = http.Get(ts.URL + "/api/v1/routes")
	var rows []map[string]any
	json.NewDecoder(resp.Body).Decode(&rows)
	resp.Body.Close()
	if len(rows) == 0 || rows[0]["interface"] == "" {
		t.Errorf("routes %v", rows)
	}
}

func TestAPIStatusShowsWarnings(t *testing.T) {
	a, ts, _ := apiServer(t, &fakeRun{})
	writeFile(t, a.layout.StateFile(), `{"schema":1,"notify":{"link_degraded:eth0":{"since":"2026-11-02T10:00:00Z","last_sent":"2026-11-02T10:00:00Z","level":1}}}`)
	_, st := apiDo(t, ts, "GET", "/api/v1/status", nil)
	w, _ := st["warnings"].([]any)
	if len(w) != 1 || w[0] != "link_degraded:eth0" {
		t.Errorf("warnings %v (state file must parse: %v)", st["warnings"], st)
	}
}

func TestAPIConfig(t *testing.T) {
	a, ts, _ := apiServer(t, &fakeRun{})
	before, _ := os.ReadFile(a.layout.ConfigFile())

	code, out := apiDo(t, ts, "PUT", "/api/v1/config", map[string]any{"values": map[string]string{"MIN_WAN_DL_MBPS": "250", "LAN_BIND": "192.168.1.6"}})
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if rr := out["restart_required"].([]any); len(rr) != 1 || rr[0] != "LAN_BIND" {
		t.Errorf("restart_required %v", rr)
	}
	got, _ := os.ReadFile(a.layout.ConfigFile())
	if !strings.HasPrefix(string(got), string(before)) || !strings.Contains(string(got), "MIN_WAN_DL_MBPS=250\n") {
		t.Errorf("file:\n%s", got)
	}
	// The new value is visible without a restart.
	_, cfg := apiDo(t, ts, "GET", "/api/v1/config", nil)
	found := false
	for _, k := range cfg["keys"].([]any) {
		if m := k.(map[string]any); m["name"] == "MIN_WAN_DL_MBPS" && m["value"] == "250" {
			found = true
		}
	}
	if !found {
		t.Errorf("GET config does not show the saved value: %v", cfg["keys"])
	}

	// Invalid values and unknown keys: 422, nothing saved, errors per field.
	snapshot, _ := os.ReadFile(a.layout.ConfigFile())
	code, out = apiDo(t, ts, "PUT", "/api/v1/config", map[string]any{"values": map[string]string{"MIN_WAN_DL_MBPS": "300", "WEB_PORT": "8080", "NOPE": "1", "CRON_SCHEDULE": "* * * * * reboot"}})
	if code != 422 {
		t.Fatalf("%d %v", code, out)
	}
	f := out["fields"].(map[string]any)
	if len(f) != 3 || f["NOPE"] != "unknown key" || !strings.Contains(f["WEB_PORT"].(string), "QTS") || f["CRON_SCHEDULE"] == nil {
		t.Errorf("fields %v", f)
	}
	if now, _ := os.ReadFile(a.layout.ConfigFile()); !bytes.Equal(now, snapshot) {
		t.Errorf("file changed by a rejected request:\n%s", now)
	}
	// A cross-key rule (IPERF_PORT == WEB_PORT) is reported on the field.
	port := strings.TrimPrefix(ts.URL[strings.LastIndex(ts.URL, ":"):], ":")
	code, out = apiDo(t, ts, "PUT", "/api/v1/config", map[string]any{"values": map[string]string{"IPERF_PORT": port}})
	if code != 422 || out["fields"].(map[string]any)["IPERF_PORT"] == nil {
		t.Errorf("same ports: %d %v", code, out)
	}
	// Characters that would change meaning in a shell never reach the file.
	code, _ = apiDo(t, ts, "PUT", "/api/v1/config", map[string]any{"values": map[string]string{"WAN_IFACES": "eth0; reboot"}})
	if code != 422 {
		t.Errorf("shell characters: %d", code)
	}
}

func TestBackendConfigFollowsTheFile(t *testing.T) {
	a, _, _ := testApp(t)
	b := &backend{a: a}
	c, err := b.Config()
	if err != nil || c.Get("MIN_WAN_DL_MBPS") != "100" {
		t.Fatalf("defaults: %v %v", c, err)
	}
	writeFile(t, a.layout.ConfigFile(), "MIN_WAN_DL_MBPS=7\n")
	c, err = b.Config()
	if err != nil || c.Get("MIN_WAN_DL_MBPS") != "7" {
		t.Fatalf("after an edit: %v %v", c, err)
	}
	writeFile(t, a.layout.ConfigFile(), "WEB_PORT=22\n")
	if _, err = b.Config(); err == nil || !strings.Contains(err.Error(), "WEB_PORT") {
		t.Errorf("invalid config: %v", err)
	}
	writeFile(t, a.layout.ConfigFile(), "MIN_WAN_DL_MBPS=8\n")
	if c, err = b.Config(); err != nil || c.Get("MIN_WAN_DL_MBPS") != "8" {
		t.Errorf("recovered config: %v %v", c, err)
	}
}

func TestLanServerCommand(t *testing.T) {
	a, _, stub := apiServer(t, &fakeRun{})
	var out, errOut strings.Builder
	a.stdout, a.stderr = &out, &errOut
	if code := a.run([]string{"lan-server", "status"}); code != exitTestError || !strings.Contains(out.String(), "stopped") {
		t.Errorf("status of a stopped server: %d %q %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := a.run([]string{"lan-server", "start"}); code != exitOK || !strings.Contains(out.String(), "running (pid 4242, port 5288)") {
		t.Errorf("start: %d %q %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := a.run([]string{"lan-server", "status", "--json"}); code != exitOK || !strings.Contains(out.String(), `"state":"running"`) {
		t.Errorf("status --json: %d %q", code, out.String())
	}
	out.Reset()
	if code := a.run([]string{"lan-server", "stop"}); code != exitOK || stub.st.State != iperfsrv.StateStopped || !strings.Contains(out.String(), "stopped") {
		t.Errorf("stop: %d %q", code, out.String())
	}
	// Starting needs root (the token is root's), and a switched-off server is explained.
	a.system.euid = func() int { return 1000 }
	errOut.Reset()
	if code := a.run([]string{"lan-server", "start"}); code != exitTestError || !strings.Contains(errOut.String(), "root") {
		t.Errorf("start as a user: %d %q", code, errOut.String())
	}
	a.system.euid = func() int { return 0 }
	errOut.Reset()
	writeFile(t, a.layout.ConfigFile(), mustRead(t, a.layout.ConfigFile())+"LAN_SERVER_ENABLE=0\n")
	if code := a.run([]string{"lan-server", "start"}); code != exitTestError || !strings.Contains(errOut.String(), "LAN_SERVER_ENABLE=0") {
		t.Errorf("start while disabled: %d %q", code, errOut.String())
	}
	for _, args := range [][]string{{"lan-server"}, {"lan-server", "restart"}, {"lan-server", "status", "x"}} {
		if code := a.run(args); code != exitUsage {
			t.Errorf("%v: %d", args, code)
		}
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLanServerWrongToken(t *testing.T) {
	a, _, _ := apiServer(t, &fakeRun{})
	writeFile(t, a.layout.TokenFile(), "not-the-token\n")
	var errOut strings.Builder
	a.stderr = &errOut
	if code := a.run([]string{"lan-server", "start"}); code != exitTestError || !strings.Contains(errOut.String(), "HTTP 401") {
		t.Errorf("%d %q", code, errOut.String())
	}
}

func TestLanServerDaemonDown(t *testing.T) {
	a, ts, _ := apiServer(t, &fakeRun{})
	ts.Close()
	var errOut strings.Builder
	a.stderr = &errOut
	if code := a.run([]string{"lan-server", "status"}); code != exitTestError || !strings.Contains(errOut.String(), "does not answer") {
		t.Errorf("%d %q", code, errOut.String())
	}
}

func TestWebTokenCommand(t *testing.T) {
	a, out, errOut := testApp(t)
	if code := a.run([]string{"web-token"}); code != exitTestError || !strings.Contains(errOut.String(), "no token yet") {
		t.Errorf("missing: %d %q", code, errOut)
	}
	writeFile(t, a.layout.TokenFile(), apiToken+"\n")
	errOut.Reset()
	if code := a.run([]string{"web-token"}); code != exitOK || out.String() != apiToken+"\n" {
		t.Errorf("show: %d %q %q", code, out, errOut)
	}
	if code := a.run([]string{"web-token", "x"}); code != exitUsage {
		t.Errorf("extra arg: %d", code)
	}
}

// The compiled daemon: real process, real HTTP. Skipped without a C-free build step; it
// builds the binary with `go build` and runs it with a fake iperf3 script.
func TestDaemonProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the daemon")
	}
	a, _, _ := testApp(t)
	bin := a.binDir + "/qnetspeed"
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	os.WriteFile(a.binDir+"/iperf3", []byte("#!/bin/sh\necho \"fake iperf3 $*\"\nexec sleep 300\n"), 0o755)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	iport := ln2.Addr().(*net.TCPAddr).Port
	ln2.Close()
	writeFile(t, a.layout.ConfigFile(), "WEB_BIND=127.0.0.1\nWEB_PORT="+itoa(port)+"\nIPERF_PORT="+itoa(iport)+"\n")

	cmd := exec.Command(bin, "daemon")
	cmd.Env = append(os.Environ(), "QNETSPEED_ROOT="+a.layout.Root)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t.Cleanup(func() { cmd.Process.Kill() })

	url := "http://127.0.0.1:" + itoa(port)
	var st web.Status
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url + "/api/v1/status")
		if err == nil {
			json.NewDecoder(resp.Body).Decode(&st)
			resp.Body.Close()
			if st.LANServer.State == iperfsrv.StateRunning {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.LANServer.State != iperfsrv.StateRunning || st.LANServer.PID == 0 {
		t.Fatalf("iperf3 server not running: %+v\n%s", st, logs.String())
	}
	if fi, err := os.Stat(a.layout.TokenFile()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("token file: %v %v", fi, err)
	}
	child := st.LANServer.PID
	cmd.Process.Signal(os.Interrupt)
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatalf("daemon did not stop\n%s", logs.String())
	}
	if err := signal0(child); err == nil {
		t.Errorf("iperf3 child %d survived the daemon", child)
	}
	if !strings.Contains(logs.String(), "iperf3: fake iperf3 -s -p "+itoa(iport)) {
		t.Errorf("iperf3 output not in the daemon log:\n%s", logs.String())
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// signal0 reports an error when pid no longer exists.
func signal0(pid int) error { return syscall.Kill(pid, 0) }
