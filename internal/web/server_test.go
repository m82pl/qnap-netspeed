package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/config"
	"github.com/m82pl/qnap-netspeed/internal/history"
	"github.com/m82pl/qnap-netspeed/internal/iperfsrv"
	"github.com/m82pl/qnap-netspeed/internal/link"
	"github.com/m82pl/qnap-netspeed/internal/lock"
)

const testToken = "test-token-0123456789abcdef0123456789abcdef"

// fakeBackend records what the API asks for.
type fakeBackend struct {
	mu       sync.Mutex
	conf     map[string]string
	hist     []history.Record
	filter   history.Filter
	histType string
	warnings []string
	runWAN   func(ctx context.Context, ifaces []string, ids []int) ([]any, error)
	runLAN   func(ctx context.Context, req LANRequest) (any, error)
	appended []any
	saved    map[string]string
	saveErr  error
	confErr  error
}

func (f *fakeBackend) Config() (*config.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.confErr != nil {
		return nil, f.confErr
	}
	c := config.Defaults()
	for k, v := range f.conf {
		if err := c.Set(k, v); err != nil {
			return nil, err
		}
	}
	return c, nil
}
func (f *fakeBackend) SaveConfig(values map[string]string) (SaveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return SaveResult{}, f.saveErr
	}
	f.saved = values
	res := SaveResult{}
	for k := range values {
		res.Saved = append(res.Saved, k)
		if config.RestartRequired(k) {
			res.RestartRequired = append(res.RestartRequired, k)
		}
	}
	return res, nil
}
func (f *fakeBackend) History(typ string, flt history.Filter) ([]history.Record, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.histType, f.filter = typ, flt
	return f.hist, 2, nil
}
func (f *fakeBackend) Link() (link.Record, error) {
	return link.Record{Schema: 1, Type: "LINK", Interfaces: []link.Interface{{Name: "eth0"}}, Warnings: []string{}}, nil
}
func (f *fakeBackend) Routes(all bool) (any, error) {
	return []map[string]any{{"interface": "eth0", "all": all}}, nil
}
func (f *fakeBackend) Warnings() []string { return f.warnings }
func (f *fakeBackend) RunWAN(ctx context.Context, ifaces []string, ids []int) ([]any, error) {
	return f.runWAN(ctx, ifaces, ids)
}
func (f *fakeBackend) RunLAN(ctx context.Context, req LANRequest) (any, error) {
	return f.runLAN(ctx, req)
}
func (f *fakeBackend) AppendLAN(rec any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appended = append(f.appended, rec)
	return nil
}

type fakeLAN struct {
	st            iperfsrv.Status
	starts, stops int
}

func (l *fakeLAN) Status() iperfsrv.Status { return l.st }
func (l *fakeLAN) Start()                  { l.starts++; l.st.State = iperfsrv.StateRunning }
func (l *fakeLAN) Stop()                   { l.stops++; l.st.State = iperfsrv.StateStopped }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	srv  *Server
	ts   *httptest.Server
	be   *fakeBackend
	lan  *fakeLAN
	lock string
	clk  *clock
}

func newEnv(t *testing.T, lang string) *env {
	t.Helper()
	e := &env{be: &fakeBackend{conf: map[string]string{}}, lan: &fakeLAN{st: iperfsrv.Status{State: iperfsrv.StateRunning, Port: 5288, PID: 77}},
		lock: filepath.Join(t.TempDir(), ".test.lock"), clk: &clock{t: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}}
	ctx, cancel := context.WithCancel(context.Background())
	e.srv = New(Options{
		Info:    Info{Version: "0.4.0-test", Arch: "linux/amd64", Lang: lang, Started: e.clk.Now().Add(-5 * time.Second), LockFile: e.lock},
		Backend: e.be, Token: testToken, LANServer: e.lan, Context: ctx, Now: e.clk.Now,
	})
	e.srv.gate.hold = 50 * time.Millisecond
	e.ts = httptest.NewServer(e.srv.Handler())
	t.Cleanup(func() { e.ts.Close(); cancel(); e.srv.Close() })
	return e
}

// do sends a request; token "" sends none.
func (e *env) do(t *testing.T, method, path, token string, body any) (*http.Response, string) {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (e *env) get(t *testing.T, path string) (*http.Response, string) {
	return e.do(t, "GET", path, "", nil)
}

func decode(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, body)
	}
}

func wantStatus(t *testing.T, resp *http.Response, body string, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("status %d, want %d: %s", resp.StatusCode, want, body)
	}
}

func TestIndex(t *testing.T) {
	e := newEnv(t, "en")
	resp, body := e.get(t, "/")
	wantStatus(t, resp, body, 200)
	for _, want := range []string{"Net Speed", "0.4.0-test", "service is running", `<html lang="en">`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	for h, want := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'self'",
		"Referrer-Policy":         "no-referrer",
	} {
		if got := resp.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
}

func TestIndexPolishAndCSS(t *testing.T) {
	e := newEnv(t, "pl")
	_, body := e.get(t, "/")
	if !strings.Contains(body, "Usługa qnap-netspeed działa") {
		t.Errorf("no Polish text in %q", body)
	}
	resp, css := e.get(t, "/app.css")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") || !strings.Contains(css, "color-scheme") {
		t.Fatalf("css: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

func TestPublicPagesNeedNoTokenEvenWhenReadIsPrivate(t *testing.T) {
	e := newEnv(t, "en")
	e.be.conf["WEB_PUBLIC_READ"] = "0"
	if resp, body := e.get(t, "/"); resp.StatusCode != 200 {
		t.Errorf("index: %d %s", resp.StatusCode, body)
	}
	if resp, _ := e.get(t, "/app.css"); resp.StatusCode != 200 {
		t.Errorf("css: %d", resp.StatusCode)
	}
}

func TestStatus(t *testing.T) {
	e := newEnv(t, "en")
	e.be.warnings = []string{"link_degraded:eth0"}
	resp, body := e.get(t, "/api/v1/status")
	wantStatus(t, resp, body, 200)
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("content type %q", resp.Header.Get("Content-Type"))
	}
	var st Status
	decode(t, body, &st)
	if st.Name != "qnap-netspeed" || st.Version != "0.4.0-test" || st.UptimeSeconds != 5 || st.TestRunning != nil {
		t.Errorf("status = %+v", st)
	}
	if st.LANServer.State != "running" || st.LANServer.PID != 77 || len(st.Warnings) != 1 || st.Warnings[0] != "link_degraded:eth0" {
		t.Errorf("lan_server/warnings = %+v %v", st.LANServer, st.Warnings)
	}
	if !strings.Contains(body, `"test_running":null`) {
		t.Errorf("test_running not null: %s", body)
	}
	l, err := lock.Acquire(e.lock, "wan", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	_, body = e.get(t, "/api/v1/status")
	decode(t, body, &st)
	if st.TestRunning == nil || st.TestRunning.Type != "wan" {
		t.Errorf("test_running = %+v", st.TestRunning)
	}
	e.be.warnings = nil
	_, body = e.get(t, "/api/v1/status")
	if !strings.Contains(body, `"warnings":[]`) {
		t.Errorf("warnings should be an empty list: %s", body)
	}
}

func TestNotFoundAndMethod(t *testing.T) {
	e := newEnv(t, "en")
	resp, _ := e.get(t, "/nope")
	if resp.StatusCode != 404 || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("/nope: %d, headers %v", resp.StatusCode, resp.Header)
	}
	resp, body := e.get(t, "/api/v1/nope")
	wantStatus(t, resp, body, 404)
	if !strings.Contains(body, `"error":"not_found"`) {
		t.Errorf("API 404 should be JSON: %s", body)
	}
	resp, _ = e.do(t, "POST", "/api/v1/status", "", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status: %d", resp.StatusCode)
	}
}

func TestAdminNeedsToken(t *testing.T) {
	e := newEnv(t, "en")
	cases := []struct{ method, path string }{
		{"POST", "/api/v1/tests/wan"}, {"POST", "/api/v1/tests/lan"}, {"POST", "/api/v1/lan-server"},
		{"GET", "/api/v1/config"}, {"PUT", "/api/v1/config"},
	}
	for _, c := range cases {
		resp, body := e.do(t, c.method, c.path, "", "{}")
		if resp.StatusCode != 401 || !strings.Contains(body, "token_required") || resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s %s without token: %d %s", c.method, c.path, resp.StatusCode, body)
		}
		resp, body = e.do(t, c.method, c.path, "wrong", "{}")
		if resp.StatusCode != 401 || !strings.Contains(body, "token_invalid") {
			t.Errorf("%s %s with a wrong token: %d %s", c.method, c.path, resp.StatusCode, body)
		}
	}
	// A prefix of the token, or the token with extra characters, is wrong too.
	for _, bad := range []string{testToken[:10], testToken + "x", strings.ToUpper(testToken)} {
		if resp, _ := e.do(t, "GET", "/api/v1/config", bad, nil); resp.StatusCode != 401 {
			t.Errorf("token %q accepted", bad)
		}
	}
	if resp, body := e.do(t, "GET", "/api/v1/config", testToken, nil); resp.StatusCode != 200 {
		t.Errorf("right token: %d %s", resp.StatusCode, body)
	}
}

func TestTokenInQueryOrCookieIsIgnored(t *testing.T) {
	e := newEnv(t, "en")
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/v1/config?token="+testToken, nil)
	req.AddCookie(&http.Cookie{Name: "token", Value: testToken})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("status %d: only the Authorization header counts (no CSRF)", resp.StatusCode)
	}
}

func TestPublicReadSwitch(t *testing.T) {
	e := newEnv(t, "en")
	for _, p := range []string{"/api/v1/status", "/api/v1/history", "/api/v1/link", "/api/v1/routes", "/api/st/ping"} {
		if resp, body := e.get(t, p); resp.StatusCode >= 400 {
			t.Errorf("WEB_PUBLIC_READ=1 %s: %d %s", p, resp.StatusCode, body)
		}
	}
	e.be.conf["WEB_PUBLIC_READ"] = "0"
	for _, p := range []string{"/api/v1/status", "/api/v1/history", "/api/v1/link", "/api/v1/routes", "/api/st/ping", "/api/v1/tests/x", "/api/st/download?mb=1"} {
		if resp, _ := e.get(t, p); resp.StatusCode != 401 {
			t.Errorf("WEB_PUBLIC_READ=0 %s without token: %d", p, resp.StatusCode)
		}
		if resp, _ := e.do(t, "GET", p, testToken, nil); resp.StatusCode == 401 {
			t.Errorf("WEB_PUBLIC_READ=0 %s with token: 401", p)
		}
	}
	if resp, _ := e.do(t, "POST", "/api/st/result", "", `{}`); resp.StatusCode != 401 {
		t.Errorf("browser result without token while private: %d", resp.StatusCode)
	}
}

func TestWrongTokenLimit(t *testing.T) {
	e := newEnv(t, "en")
	for i := 0; i < 10; i++ {
		if resp, _ := e.do(t, "GET", "/api/v1/config", "wrong", nil); resp.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i+1, resp.StatusCode)
		}
	}
	// The 11th attempt is refused, and so is the right token: the address is locked out.
	resp, body := e.do(t, "GET", "/api/v1/config", "wrong", nil)
	wantStatus(t, resp, body, 429)
	if resp.Header.Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	if resp, _ := e.do(t, "GET", "/api/v1/config", testToken, nil); resp.StatusCode != 429 {
		t.Errorf("right token during lockout: %d", resp.StatusCode)
	}
	// Public pages and (public) reads are not affected.
	if resp, _ := e.get(t, "/api/v1/status"); resp.StatusCode != 200 {
		t.Errorf("public read during lockout: %d", resp.StatusCode)
	}
	e.clk.Advance(61 * time.Second)
	if resp, body := e.do(t, "GET", "/api/v1/config", testToken, nil); resp.StatusCode != 200 {
		t.Errorf("after the window: %d %s", resp.StatusCode, body)
	}
}

func TestMissingTokenIsNotCountedAsAttempt(t *testing.T) {
	e := newEnv(t, "en")
	for i := 0; i < 30; i++ {
		e.do(t, "GET", "/api/v1/config", "", nil)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/config", testToken, nil); resp.StatusCode != 200 {
		t.Errorf("the GUI asking without a token locked the address out: %d", resp.StatusCode)
	}
}

func TestInvalidConfigReported(t *testing.T) {
	e := newEnv(t, "en")
	e.be.confErr = errors.New("invalid configuration: line 3: WEB_PORT: must be a port")
	resp, body := e.get(t, "/api/v1/status")
	wantStatus(t, resp, body, 500)
	if !strings.Contains(body, "config_invalid") || !strings.Contains(body, "WEB_PORT") {
		t.Errorf("body %s", body)
	}
}

func TestHistoryParams(t *testing.T) {
	e := newEnv(t, "en")
	raw := json.RawMessage(`{"timestamp":"2026-11-03T10:00:00Z","type":"WAN","interface":"eth0","trigger":"cron","status":"ok","results":{"download_mbps":94.5,"upload_mbps":20,"ping_ms":9.1,"jitter_ms":1},"server":{"Name":"X"},"warnings":[],"error":null}`)
	e.be.hist = []history.Record{{Timestamp: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC), Type: "WAN", Interface: "eth0", Raw: raw}}

	resp, body := e.get(t, "/api/v1/history")
	wantStatus(t, resp, body, 200)
	var out struct {
		Records      []map[string]any `json:"records"`
		SkippedLines int              `json:"skipped_lines"`
	}
	decode(t, body, &out)
	if len(out.Records) != 1 || out.SkippedLines != 2 || out.Records[0]["interface"] != "eth0" {
		t.Errorf("out = %+v", out)
	}
	if e.be.histType != "wan" || e.be.filter.Last != 100 {
		t.Errorf("defaults: type %q filter %+v", e.be.histType, e.be.filter)
	}

	e.get(t, "/api/v1/history?type=lan&last=5&iface=eth1&since=2d")
	f := e.be.filter
	if e.be.histType != "lan" || f.Last != 5 || f.Interface != "eth1" || !f.Since.Equal(e.clk.Now().Add(-48*time.Hour)) {
		t.Errorf("type %q filter %+v", e.be.histType, f)
	}
	e.get(t, "/api/v1/history?since=2026-11-01T00:00:00Z")
	if !e.be.filter.Since.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("RFC 3339 since: %v", e.be.filter.Since)
	}
	for _, q := range []string{"type=xx", "last=0", "last=5001", "last=x", "since=yesterday", "iface=a%20b", "format=xml"} {
		if resp, body := e.get(t, "/api/v1/history?"+q); resp.StatusCode != 400 {
			t.Errorf("?%s: %d %s", q, resp.StatusCode, body)
		}
	}

	resp, body = e.get(t, "/api/v1/history?format=csv")
	wantStatus(t, resp, body, 200)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") || !strings.Contains(resp.Header.Get("Content-Disposition"), "wan-history.csv") {
		t.Errorf("csv headers %v", resp.Header)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "timestamp,interface,trigger,status,download_mbps") ||
		!strings.HasPrefix(lines[1], "2026-11-03T10:00:00Z,eth0,cron,ok,94.5,20,9.1,1,X") {
		t.Errorf("csv:\n%s", body)
	}
}

func TestLinkAndRoutes(t *testing.T) {
	e := newEnv(t, "en")
	_, body := e.get(t, "/api/v1/link")
	if !strings.Contains(body, `"name":"eth0"`) || !strings.Contains(body, `"type":"LINK"`) {
		t.Errorf("link: %s", body)
	}
	_, body = e.get(t, "/api/v1/routes?all=1")
	if !strings.Contains(body, `"all":true`) {
		t.Errorf("routes: %s", body)
	}
}

func waitJob(t *testing.T, e *env, id string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, body := e.get(t, "/api/v1/tests/"+id)
		var j Job
		decode(t, body, &j)
		if j.State != JobRunning {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("test did not finish")
	return Job{}
}

func TestWANTestLifecycle(t *testing.T) {
	e := newEnv(t, "en")
	release := make(chan struct{})
	var gotIfaces []string
	var gotIDs []int
	e.be.runWAN = func(ctx context.Context, ifaces []string, ids []int) ([]any, error) {
		gotIfaces, gotIDs = ifaces, ids
		<-release
		return []any{map[string]any{"interface": "eth0", "status": "ok"}}, nil
	}
	resp, body := e.do(t, "POST", "/api/v1/tests/wan", testToken, map[string]any{"ifaces": []string{"eth0"}, "server_ids": []int{12}})
	wantStatus(t, resp, body, 202)
	var acc struct {
		TestID, Type, State string `json:"-"`
	}
	var raw map[string]any
	decode(t, body, &raw)
	id, _ := raw["test_id"].(string)
	if id == "" || raw["type"] != "wan" || raw["state"] != "running" || resp.Header.Get("Location") != "/api/v1/tests/"+id {
		t.Fatalf("accepted = %s (%v)", body, acc)
	}

	// While it runs: status shows it, a second test and a GUI/browser stream get 409 with the holder.
	_, sb := e.get(t, "/api/v1/status")
	if !strings.Contains(sb, `"test_running":{"type":"wan"`) {
		t.Errorf("status while running: %s", sb)
	}
	for _, p := range []struct{ path, body string }{{"/api/v1/tests/wan", "{}"}, {"/api/v1/tests/lan", `{"target":"192.168.1.5"}`}} {
		resp, body = e.do(t, "POST", p.path, testToken, p.body)
		if resp.StatusCode != 409 || !strings.Contains(body, `"error":"test_running"`) || !strings.Contains(body, `"running":{"type":"wan"`) {
			t.Errorf("second test via %s: %d %s", p.path, resp.StatusCode, body)
		}
	}
	if resp, _ := e.get(t, "/api/st/download?mb=1"); resp.StatusCode != 409 {
		t.Errorf("browser download during a WAN test: %d", resp.StatusCode)
	}
	_, jb := e.get(t, "/api/v1/tests/"+id)
	var j Job
	decode(t, jb, &j)
	if j.State != JobRunning || j.FinishedAt != nil || len(j.Results) != 0 {
		t.Errorf("running job = %+v", j)
	}

	close(release)
	j = waitJob(t, e, id)
	if j.State != JobDone || j.FinishedAt == nil || len(j.Results) != 1 {
		t.Errorf("finished job = %+v", j)
	}
	if len(gotIfaces) != 1 || gotIfaces[0] != "eth0" || len(gotIDs) != 1 || gotIDs[0] != 12 {
		t.Errorf("backend got %v %v", gotIfaces, gotIDs)
	}
	// The lock is free again.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if h, _ := lock.Current(e.lock); h == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock still held after the test")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if resp, _ := e.get(t, "/api/v1/tests/nope"); resp.StatusCode != 404 {
		t.Errorf("unknown test: %d", resp.StatusCode)
	}
}

func TestTestFailureIsReported(t *testing.T) {
	e := newEnv(t, "en")
	e.be.runWAN = func(context.Context, []string, []int) ([]any, error) { return nil, errors.New("no librespeed-cli") }
	_, body := e.do(t, "POST", "/api/v1/tests/wan", testToken, "")
	var raw map[string]any
	decode(t, body, &raw)
	j := waitJob(t, e, raw["test_id"].(string))
	if j.State != JobError || j.Error != "no librespeed-cli" {
		t.Errorf("job = %+v", j)
	}
}

func TestWANTestValidation(t *testing.T) {
	e := newEnv(t, "en")
	e.be.runWAN = func(context.Context, []string, []int) ([]any, error) { return nil, nil }
	bad := []string{
		`{"ifaces":["eth0; rm -rf /"]}`, `{"ifaces":["-x y"]}`, `{"server_ids":[0]}`, `{"server_ids":[2000000]}`,
		`{"ifaces":["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q"]}`,
	}
	for _, b := range bad {
		if resp, body := e.do(t, "POST", "/api/v1/tests/wan", testToken, b); resp.StatusCode != 422 {
			t.Errorf("%s: %d %s", b, resp.StatusCode, body)
		}
	}
	for _, b := range []string{`{"unknown":1}`, `[1]`, `{"ifaces":`, `{} {}`} {
		if resp, body := e.do(t, "POST", "/api/v1/tests/wan", testToken, b); resp.StatusCode != 400 {
			t.Errorf("%s: %d %s", b, resp.StatusCode, body)
		}
	}
	big := `{"ifaces":["` + strings.Repeat("a", maxJSONBody) + `"]}`
	if resp, _ := e.do(t, "POST", "/api/v1/tests/wan", testToken, big); resp.StatusCode != 413 {
		t.Errorf("oversized body: %d", resp.StatusCode)
	}
	// Nothing above started a test.
	if h, _ := lock.Current(e.lock); h != nil {
		t.Errorf("lock held after rejected requests: %+v", h)
	}
	// An empty body is fine: all defaults.
	if resp, body := e.do(t, "POST", "/api/v1/tests/wan", testToken, ""); resp.StatusCode != 202 {
		t.Errorf("empty body: %d %s", resp.StatusCode, body)
	}
}

func TestLANTest(t *testing.T) {
	e := newEnv(t, "en")
	var got LANRequest
	e.be.runLAN = func(ctx context.Context, req LANRequest) (any, error) {
		got = req
		return map[string]any{"type": "LAN", "target": req.Target}, nil
	}
	resp, body := e.do(t, "POST", "/api/v1/tests/lan", testToken, map[string]any{"target": "192.168.1.50", "time": 5, "parallel": 2, "udp": true})
	wantStatus(t, resp, body, 202)
	var raw map[string]any
	decode(t, body, &raw)
	j := waitJob(t, e, raw["test_id"].(string))
	if j.State != JobDone || len(j.Results) != 1 || j.Type != "lan-test" {
		t.Errorf("job = %+v", j)
	}
	if got.Target != "192.168.1.50" || got.Time != 5 || got.Parallel != 2 || !got.UDP || got.Bitrate != "1G" {
		t.Errorf("request = %+v", got)
	}
	for _, b := range []string{
		`{}`, `{"target":""}`, `{"target":"-oProxyCommand=x"}`, `{"target":"a b"}`, `{"target":"host;ls"}`,
		`{"target":"h","time":301}`, `{"target":"h","time":-1}`, `{"target":"h","parallel":33}`,
		`{"target":"h","iface":"e th"}`, `{"target":"h","bitrate":"fast"}`,
	} {
		if resp, body := e.do(t, "POST", "/api/v1/tests/lan", testToken, b); resp.StatusCode != 422 {
			t.Errorf("%s: %d %s", b, resp.StatusCode, body)
		}
	}
}

func TestLANServerControl(t *testing.T) {
	e := newEnv(t, "en")
	resp, body := e.do(t, "POST", "/api/v1/lan-server", testToken, `{"action":"stop"}`)
	wantStatus(t, resp, body, 200)
	if e.lan.stops != 1 || !strings.Contains(body, `"state":"stopped"`) {
		t.Errorf("stop: %d %s", e.lan.stops, body)
	}
	resp, body = e.do(t, "POST", "/api/v1/lan-server", testToken, `{"action":"start"}`)
	wantStatus(t, resp, body, 200)
	if e.lan.starts != 1 || !strings.Contains(body, `"state":"running"`) {
		t.Errorf("start: %d %s", e.lan.starts, body)
	}
	if resp, _ := e.do(t, "POST", "/api/v1/lan-server", testToken, `{"action":"restart"}`); resp.StatusCode != 422 {
		t.Errorf("bad action: %d", resp.StatusCode)
	}
	e.be.conf["LAN_SERVER_ENABLE"] = "0"
	resp, body = e.do(t, "POST", "/api/v1/lan-server", testToken, `{"action":"start"}`)
	if resp.StatusCode != 409 || !strings.Contains(body, "lan_server_disabled") || e.lan.starts != 1 {
		t.Errorf("start while LAN_SERVER_ENABLE=0: %d %s", resp.StatusCode, body)
	}
	if resp, _ := e.do(t, "POST", "/api/v1/lan-server", testToken, `{"action":"stop"}`); resp.StatusCode != 200 {
		t.Errorf("stop while disabled: %d", resp.StatusCode)
	}
}

func TestConfigGetPut(t *testing.T) {
	e := newEnv(t, "en")
	e.be.conf["LANG"] = "pl"
	resp, body := e.do(t, "GET", "/api/v1/config", testToken, nil)
	wantStatus(t, resp, body, 200)
	var got struct {
		Keys []struct {
			Name, Value, Default string
			RestartRequired      bool `json:"restart_required"`
		}
		Issues []map[string]string
	}
	decode(t, body, &got)
	byName := map[string]int{}
	for i, k := range got.Keys {
		byName[k.Name] = i
	}
	if len(got.Keys) != len(config.Keys) || got.Keys[byName["LANG"]].Value != "pl" || got.Keys[byName["LANG"]].Default != "en" ||
		!got.Keys[byName["WEB_PORT"]].RestartRequired || got.Keys[byName["MIN_WAN_DL_MBPS"]].RestartRequired {
		t.Errorf("config keys = %+v", got.Keys)
	}
	if got.Issues == nil {
		t.Error("issues should be a list")
	}

	resp, body = e.do(t, "PUT", "/api/v1/config", testToken, map[string]any{"values": map[string]string{"MIN_WAN_DL_MBPS": "200", "WEB_PORT": "8899"}})
	wantStatus(t, resp, body, 200)
	if !strings.Contains(body, `"saved":["MIN_WAN_DL_MBPS","WEB_PORT"]`) || !strings.Contains(body, `"restart_required":["WEB_PORT"]`) {
		t.Errorf("saved: %s", body)
	}

	e.be.saveErr = FieldErrors{"WEB_PORT": "must be a port 1024..65535"}
	resp, body = e.do(t, "PUT", "/api/v1/config", testToken, map[string]any{"values": map[string]string{"WEB_PORT": "1"}})
	wantStatus(t, resp, body, 422)
	if !strings.Contains(body, `"fields":{"WEB_PORT":"must be a port`) || !strings.Contains(body, "nothing was saved") {
		t.Errorf("field errors: %s", body)
	}
	for _, b := range []string{`{}`, `{"values":{}}`, `{"WEB_PORT":"1"}`} {
		if resp, _ := e.do(t, "PUT", "/api/v1/config", testToken, b); resp.StatusCode != 422 && resp.StatusCode != 400 {
			t.Errorf("%s: %d", b, resp.StatusCode)
		}
	}
}

func TestAdminActionsAreLogged(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	e := newEnv(t, "en")
	e.srv.o.Logf = func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, strings.TrimSpace(fmt.Sprintf(f, a...)))
	}
	e.do(t, "GET", "/api/v1/config", testToken, nil)
	e.do(t, "GET", "/api/v1/config", "wrong", nil)
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "admin: GET /api/v1/config from 127.0.0.1") || !strings.Contains(joined, "auth: wrong token from 127.0.0.1") {
		t.Errorf("log:\n%s", joined)
	}
	if strings.Contains(joined, testToken) || strings.Contains(joined, "wrong token from 127.0.0.1 for GET /api/v1/config wrong") {
		t.Errorf("the log must not contain tokens:\n%s", joined)
	}
}

func TestLoadOrCreateToken(t *testing.T) {
	p := filepath.Join(t.TempDir(), "web.token")
	a, err := LoadOrCreateToken(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 43 || strings.ContainsAny(a, "+/= \n") { // 32 bytes, base64url without padding
		t.Errorf("token %q", a)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	b, err := LoadOrCreateToken(p)
	if err != nil || a != b {
		t.Errorf("second call: %q, %v", b, err)
	}
	// A truncated file is replaced; the new file is private again.
	os.WriteFile(p, []byte("short\n"), 0o644)
	c, err := LoadOrCreateToken(p)
	if err != nil || c == "short" || len(c) != 43 {
		t.Errorf("short file: %q, %v", c, err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode after replace %v", fi.Mode().Perm())
	}
}

func TestLimiter(t *testing.T) {
	clk := &clock{t: time.Unix(1000, 0)}
	l := newLimiter(clk.Now, time.Minute, 3)
	for i := 0; i < 3; i++ {
		if l.blocked("a") {
			t.Fatal("blocked too early")
		}
		l.add("a")
		clk.Advance(10 * time.Second)
	}
	if !l.blocked("a") || l.blocked("b") {
		t.Error("a should be blocked, b not")
	}
	if d := l.retryAfter("a"); d <= 0 || d > 31*time.Second {
		t.Errorf("retryAfter %v", d)
	}
	clk.Advance(31 * time.Second)
	if l.blocked("a") {
		t.Error("the oldest hit should have expired")
	}
}
