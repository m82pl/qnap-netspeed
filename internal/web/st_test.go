package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/m82pl/qnap-netspeed/internal/lock"
)

func TestSTPing(t *testing.T) {
	e := newEnv(t, "en")
	resp, body := e.get(t, "/api/st/ping")
	if resp.StatusCode != 204 || body != "" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("ping: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	// Ping is not a stream: it must not take the test lock.
	if h, _ := lock.Current(e.lock); h != nil {
		t.Errorf("ping holds the lock: %+v", h)
	}
}

func TestSTDownload(t *testing.T) {
	e := newEnv(t, "en")
	resp, err := http.Get(e.ts.URL + "/api/st/download?mb=5")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/octet-stream" || resp.Header.Get("Content-Length") != "5242880" ||
		resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("headers %v", resp.Header)
	}
	h, _ := lock.Current(e.lock)
	if h == nil || h.Type != "lan-browser" {
		t.Errorf("lock during a download: %+v", h)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil || len(b) != 5<<20 {
		t.Fatalf("read %d bytes: %v", len(b), err)
	}
	// 4 MiB buffer, repeated: the first MiB of the second round equals the start of the first.
	if !bytes.Equal(b[:1<<20], b[4<<20:5<<20]) {
		t.Error("data is not the repeated 4 MiB buffer")
	}
	if bytes.Equal(b[:1<<10], make([]byte, 1<<10)) {
		t.Error("data is not random")
	}
	for _, q := range []string{"mb=0", "mb=1025", "mb=x", "mb=-1", "mb=1.5"} {
		if r, body := e.get(t, "/api/st/download?"+q); r.StatusCode != 400 {
			t.Errorf("?%s: %d %s", q, r.StatusCode, body)
		}
	}
	if r, _ := e.get(t, "/api/st/download?mb=1"); r.StatusCode != 200 {
		t.Errorf("default max edge: %d", r.StatusCode)
	}
}

func TestSTLockLifecycle(t *testing.T) {
	e := newEnv(t, "en")
	e.srv.gate.hold = 400 * time.Millisecond
	// Between the phases of the browser test the lock stays (hold = 50 ms in tests), then it is let go.
	e.get(t, "/api/st/download?mb=1")
	if h, _ := lock.Current(e.lock); h == nil {
		t.Error("the lock was let go at once, a cron run could start between download and upload")
	}
	if r, _ := e.do(t, "POST", "/api/st/upload", "", "abc"); r.StatusCode != 200 {
		t.Errorf("upload right after the download: %d", r.StatusCode)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if h, _ := lock.Current(e.lock); h == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock not released after the hold time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSTBusyWhenAnotherTestRuns(t *testing.T) {
	e := newEnv(t, "en")
	l, err := lock.Acquire(e.lock, "cron", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	for _, c := range []struct{ method, path string }{{"GET", "/api/st/download?mb=1"}, {"POST", "/api/st/upload"}} {
		resp, body := e.do(t, c.method, c.path, "", "x")
		if resp.StatusCode != 409 || !strings.Contains(body, `"running":{"type":"cron"`) {
			t.Errorf("%s %s: %d %s", c.method, c.path, resp.StatusCode, body)
		}
	}
	// Ping works regardless: the page can still measure latency and say that a test is running.
	if r, _ := e.get(t, "/api/st/ping"); r.StatusCode != 204 {
		t.Errorf("ping: %d", r.StatusCode)
	}
}

func TestSTCloseReleasesTheLock(t *testing.T) {
	e := newEnv(t, "en")
	e.srv.gate.hold = time.Hour
	e.get(t, "/api/st/download?mb=1")
	if h, _ := lock.Current(e.lock); h == nil {
		t.Fatal("expected the lock to be held")
	}
	e.srv.Close()
	if h, _ := lock.Current(e.lock); h != nil {
		t.Errorf("lock held after Close: %+v", h)
	}
}

func TestSTUpload(t *testing.T) {
	e := newEnv(t, "en")
	resp, body := e.do(t, "POST", "/api/st/upload", "", strings.Repeat("x", 3<<20))
	wantStatus(t, resp, body, 200)
	var out struct {
		Bytes   int64
		Seconds float64
	}
	decode(t, body, &out)
	if out.Bytes != 3<<20 || out.Seconds < 0 || out.Seconds > 5 {
		t.Errorf("upload answer %+v", out)
	}
	resp, body = e.do(t, "POST", "/api/st/upload", "", nil)
	wantStatus(t, resp, body, 200)
	decode(t, body, &out)
	if out.Bytes != 0 {
		t.Errorf("empty upload: %+v", out)
	}
}

func TestSTResult(t *testing.T) {
	e := newEnv(t, "en")
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/st/result", strings.NewReader(
		`{"download_mbps":512.456,"upload_mbps":300,"ping_ms":3.14159,"jitter_ms":0.5,"duration_s":10,"parallel":4}`))
	req.Header.Set("User-Agent", "Mozilla/5.0 (Phone)\tžółć "+strings.Repeat("A", 500))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	wantStatus(t, resp, string(body), 201)
	if len(e.be.appended) != 1 {
		t.Fatalf("appended %d records", len(e.be.appended))
	}
	b, _ := json.Marshal(e.be.appended[0])
	var rec map[string]any
	decode(t, string(b), &rec)
	if rec["type"] != "LAN_BROWSER" || rec["trigger"] != "web" || rec["status"] != "ok" || rec["client_ip"] != "127.0.0.1" ||
		rec["timestamp"] != "2026-11-03T12:00:00Z" || rec["schema"] != float64(1) || rec["id"] == "" {
		t.Errorf("record = %s", b)
	}
	res := rec["results"].(map[string]any)
	if res["download_mbps"] != 512.46 || res["upload_mbps"] != float64(300) || res["ping_ms"] != 3.14 || res["jitter_ms"] != 0.5 {
		t.Errorf("results = %v", res)
	}
	ua := rec["user_agent"].(string)
	if len(ua) > maxUserAgent || strings.ContainsAny(ua, "\r\n\x00") || !strings.HasPrefix(ua, "Mozilla/5.0 (Phone)") {
		t.Errorf("user agent %q", ua)
	}
	if len(b) > 1024 {
		t.Errorf("record is %d bytes, history lines must stay small", len(b))
	}
	var ack struct{ ID string }
	decode(t, string(body), &ack)
	if ack.ID != rec["id"] {
		t.Errorf("ack %s vs record id %v", body, rec["id"])
	}
}

func TestSTResultValidation(t *testing.T) {
	e := newEnv(t, "en")
	for _, b := range []string{
		`{"download_mbps":-1}`, `{"download_mbps":1e9}`, `{"ping_ms":1e9}`, `{"duration_s":9999}`, `{"parallel":-1}`,
	} {
		if resp, body := e.do(t, "POST", "/api/st/result", "", b); resp.StatusCode != 422 {
			t.Errorf("%s: %d %s", b, resp.StatusCode, body)
		}
	}
	for _, b := range []string{``, `nope`, `{"download_mbps":"fast"}`, `{"extra":1}`} {
		if resp, body := e.do(t, "POST", "/api/st/result", "", b); resp.StatusCode != 400 {
			t.Errorf("%q: %d %s", b, resp.StatusCode, body)
		}
	}
	if len(e.be.appended) != 0 {
		t.Errorf("rejected results were stored: %v", e.be.appended)
	}
}

func TestSTResultRateLimit(t *testing.T) {
	e := newEnv(t, "en")
	for i := 0; i < stResultsPerIP; i++ {
		if resp, body := e.do(t, "POST", "/api/st/result", "", `{"download_mbps":1}`); resp.StatusCode != 201 {
			t.Fatalf("result %d: %d %s", i+1, resp.StatusCode, body)
		}
	}
	if resp, _ := e.do(t, "POST", "/api/st/result", "", `{"download_mbps":1}`); resp.StatusCode != 429 {
		t.Errorf("over the limit: %d", resp.StatusCode)
	}
	e.clk.Advance(61 * time.Second)
	if resp, _ := e.do(t, "POST", "/api/st/result", "", `{"download_mbps":1}`); resp.StatusCode != 201 {
		t.Errorf("after the window: %d", resp.StatusCode)
	}
}

func TestCleanUA(t *testing.T) {
	if got := cleanUA("a\r\nb\x00c\x7fd"); got != "a  b c d" {
		t.Errorf("control characters: %q", got)
	}
	long := strings.Repeat("ż", 300) // two bytes each: the cut must not split a rune
	if got := cleanUA(long); len(got) > maxUserAgent || !utf8.ValidString(got) {
		t.Errorf("long: %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
}
