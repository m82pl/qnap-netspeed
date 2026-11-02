// Package web is the HTTP server of the daemon (spec pkt 14).
//
// Phase F1 serves only a status page and GET /api/v1/status; the GUI and the
// rest of the API arrive in F4/F5.
package web

import (
	"embed"
	"encoding/json"
	"html/template"
	"net/http"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/lock"
)

//go:embed static
var static embed.FS

var indexTmpl = template.Must(template.ParseFS(static, "static/index.html"))

// Info is what the server reports about the running program.
type Info struct {
	Version  string
	Arch     string
	Lang     string // "en" or "pl"
	Started  time.Time
	LockFile string // data/.test.lock, for "test_running"
}

// Status is the body of GET /api/v1/status.
type Status struct {
	Name          string       `json:"name"`
	Version       string       `json:"version"`
	Arch          string       `json:"arch"`
	StartedAt     time.Time    `json:"started_at"`
	UptimeSeconds int64        `json:"uptime_seconds"`
	TestRunning   *lock.Holder `json:"test_running"`
}

// Handler returns the HTTP handler.
func Handler(info Info) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		indexTmpl.Execute(w, struct {
			Lang, Version, Arch, Started string
		}{info.Lang, info.Version, info.Arch, info.Started.UTC().Format(time.RFC3339)})
	})
	mux.HandleFunc("GET /app.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		http.ServeFileFS(w, r, static, "static/app.css")
	})
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		st := Status{
			Name:          "qnap-netspeed",
			Version:       info.Version,
			Arch:          info.Arch,
			StartedAt:     info.Started.UTC(),
			UptimeSeconds: int64(time.Since(info.Started).Seconds()),
		}
		if info.LockFile != "" {
			st.TestRunning, _ = lock.Current(info.LockFile)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(st)
	})
	return securityHeaders(mux)
}

// securityHeaders adds the headers from spec pkt 14.2 to every response.
func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Content-Security-Policy", "default-src 'self'")
		hd.Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

// NewServer returns an http.Server with conservative timeouts.
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}
