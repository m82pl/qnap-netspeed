package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/lock"
)

func newTestServer(t *testing.T, lang string) (*httptest.Server, string) {
	t.Helper()
	lf := filepath.Join(t.TempDir(), ".test.lock")
	info := Info{Version: "0.1.0-test", Arch: "linux/amd64", Lang: lang,
		Started: time.Now().Add(-5 * time.Second), LockFile: lf}
	srv := httptest.NewServer(Handler(info))
	t.Cleanup(srv.Close)
	return srv, lf
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestIndex(t *testing.T) {
	srv, _ := newTestServer(t, "en")
	resp, body := get(t, srv.URL+"/")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{"Net Speed", "0.1.0-test", "service is running", `<html lang="en">`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	for h, want := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'self'",
	} {
		if got := resp.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
}

func TestIndexPolish(t *testing.T) {
	srv, _ := newTestServer(t, "pl")
	_, body := get(t, srv.URL+"/")
	if !strings.Contains(body, "Usługa qnap-netspeed działa") {
		t.Errorf("no Polish text in %q", body)
	}
}

func TestCSS(t *testing.T) {
	srv, _ := newTestServer(t, "en")
	resp, body := get(t, srv.URL+"/app.css")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") || !strings.Contains(body, "color-scheme") {
		t.Fatalf("css: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

func TestStatus(t *testing.T) {
	srv, lf := newTestServer(t, "en")
	resp, body := get(t, srv.URL+"/api/v1/status")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("status %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var st Status
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if st.Name != "qnap-netspeed" || st.Version != "0.1.0-test" || st.UptimeSeconds < 4 || st.TestRunning != nil {
		t.Errorf("status = %+v", st)
	}
	if !strings.Contains(body, `"test_running":null`) {
		t.Errorf("test_running not null: %s", body)
	}

	l, err := lock.Acquire(lf, "wan", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	_, body = get(t, srv.URL+"/api/v1/status")
	json.Unmarshal([]byte(body), &st)
	if st.TestRunning == nil || st.TestRunning.Type != "wan" {
		t.Errorf("test_running = %+v", st.TestRunning)
	}
}

func TestNotFoundAndMethod(t *testing.T) {
	srv, _ := newTestServer(t, "en")
	resp, _ := get(t, srv.URL+"/nope")
	if resp.StatusCode != 404 {
		t.Errorf("/nope: %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Error("security headers missing on 404")
	}
	r, err := http.Post(srv.URL+"/api/v1/status", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status: %d", r.StatusCode)
	}
}
