// Package web is the HTTP server of the daemon (spec pkt 14): REST API
// (/api/v1), the browser speed test (/api/st) and the static GUI.
//
// Phase F4 provides the whole API; the pages arrive in F5.
package web

import (
	"context"
	"embed"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/config"
	"github.com/m82pl/qnap-netspeed/internal/history"
	"github.com/m82pl/qnap-netspeed/internal/iperfsrv"
	"github.com/m82pl/qnap-netspeed/internal/link"
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
	LockFile string // data/.test.lock: shared with cron and the CLI
}

// LANServer is the supervised `iperf3 -s`.
type LANServer interface {
	Status() iperfsrv.Status
	Start()
	Stop()
}

// LANRequest are the parameters of a LAN test started through the API.
// Zero Time and Parallel mean the configured defaults.
type LANRequest struct {
	Target   string `json:"target"`
	Time     int    `json:"time"`
	Parallel int    `json:"parallel"`
	UDP      bool   `json:"udp"`
	Bidir    bool   `json:"bidir"`
	Iface    string `json:"iface"`
	Bitrate  string `json:"bitrate"`
}

// SaveResult is the outcome of a successful configuration save.
type SaveResult struct {
	Saved           []string       `json:"saved"`
	RestartRequired []string       `json:"restart_required"`
	Issues          []config.Issue `json:"-"`
}

// FieldErrors is returned by SaveConfig when values are invalid; nothing is saved then.
type FieldErrors map[string]string

func (FieldErrors) Error() string { return "invalid configuration values" }

// Backend is everything the API needs from the rest of the program.
type Backend interface {
	// Config returns the current configuration (re-read when the file changes).
	Config() (*config.Config, error)
	SaveConfig(values map[string]string) (SaveResult, error)
	History(typ string, f history.Filter) ([]history.Record, int, error)
	Link() (link.Record, error)
	Routes(all bool) (any, error)
	// Warnings lists the open problems of data/state.json ("rule:subject").
	Warnings() []string
	// RunWAN and RunLAN run a test and store its records in the history.
	RunWAN(ctx context.Context, ifaces []string, serverIDs []int) ([]any, error)
	RunLAN(ctx context.Context, req LANRequest) (any, error)
	// AppendLAN stores one record (browser test result) in lan.jsonl.
	AppendLAN(rec any) error
}

// Options configure a Server.
type Options struct {
	Info      Info
	Backend   Backend
	Token     string
	LANServer LANServer
	// Context ends running tests when the daemon stops.
	Context context.Context
	Logf    func(format string, args ...any)
	Now     func() time.Time
}

// Server is the HTTP handler and the state behind it.
type Server struct {
	o        Options
	token    string
	failures *limiter // wrong tokens per address
	results  *limiter // browser results per address
	jobs     *jobManager
	gate     *browserGate
	buf      []byte
}

// New returns a Server. Close it on shutdown.
func New(o Options) *Server {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.Context == nil {
		o.Context = context.Background()
	}
	return &Server{
		o:        o,
		token:    o.Token,
		failures: newLimiter(o.Now, authFailWindow, authFailLimit),
		results:  newLimiter(o.Now, time.Minute, stResultsPerIP),
		jobs:     newJobManager(o.Context, o.Info.LockFile, o.Now),
		gate:     &browserGate{lockFile: o.Info.LockFile, hold: stIdleHold},
		buf:      newTestBuffer(),
	}
}

// Close releases the test lock held for a browser test and waits briefly for API tests to end.
func (s *Server) Close() {
	s.gate.close()
	s.jobs.wait(10 * time.Second)
}

// writeBusy answers 409 for "another test is running".
func writeBusy(w http.ResponseWriter, err error) {
	var b *lock.BusyError
	if asBusy(err, &b) {
		writeJSON(w, http.StatusConflict, struct {
			apiError
			Running lock.Holder `json:"running"`
		}{apiError{"test_running", b.Error()}, b.Holder})
		return
	}
	writeError(w, http.StatusInternalServerError, "lock_failed", err.Error())
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.guard(accessPublic, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		in := s.o.Info
		indexTmpl.Execute(w, struct {
			Lang, Version, Arch, Started string
		}{in.Lang, in.Version, in.Arch, in.Started.UTC().Format(time.RFC3339)})
	}))
	mux.HandleFunc("GET /app.css", s.guard(accessPublic, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		http.ServeFileFS(w, r, static, "static/app.css")
	}))

	mux.HandleFunc("GET /api/v1/status", s.guard(accessRead, s.apiStatus))
	mux.HandleFunc("GET /api/v1/history", s.guard(accessRead, s.apiHistory))
	mux.HandleFunc("GET /api/v1/link", s.guard(accessRead, s.apiLink))
	mux.HandleFunc("GET /api/v1/routes", s.guard(accessRead, s.apiRoutes))
	mux.HandleFunc("GET /api/v1/tests/{id}", s.guard(accessRead, s.apiTestGet))
	mux.HandleFunc("POST /api/v1/tests/wan", s.guard(accessAdmin, s.apiTestWAN))
	mux.HandleFunc("POST /api/v1/tests/lan", s.guard(accessAdmin, s.apiTestLAN))
	mux.HandleFunc("POST /api/v1/lan-server", s.guard(accessAdmin, s.apiLANServer))
	mux.HandleFunc("GET /api/v1/config", s.guard(accessAdmin, s.apiConfigGet))
	mux.HandleFunc("PUT /api/v1/config", s.guard(accessAdmin, s.apiConfigPut))

	mux.HandleFunc("GET /api/st/ping", s.guard(accessRead, s.stPing))
	mux.HandleFunc("GET /api/st/download", s.guard(accessRead, s.stDownload))
	mux.HandleFunc("POST /api/st/upload", s.guard(accessRead, s.stUpload))
	mux.HandleFunc("POST /api/st/result", s.guard(accessRead, s.stResult))

	return securityHeaders(jsonNotFound(mux))
}

// jsonNotFound makes an unknown path under /api answer 404 in JSON, like the
// rest of the API; a wrong method keeps the mux's 405.
func jsonNotFound(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// mux.ServeHTTP, not the handler from mux.Handler: only ServeHTTP fills in the path values.
		if _, pattern := mux.Handler(r); pattern == "" && strings.HasPrefix(r.URL.Path, "/api/") {
			w = &apiNotFound{ResponseWriter: w}
		}
		mux.ServeHTTP(w, r)
	})
}

type apiNotFound struct {
	http.ResponseWriter
	swallow bool
}

func (a *apiNotFound) WriteHeader(code int) {
	if code == http.StatusNotFound {
		a.swallow = true
		writeError(a.ResponseWriter, code, "not_found", "no such endpoint")
		return
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a *apiNotFound) Write(b []byte) (int, error) {
	if a.swallow {
		return len(b), nil
	}
	return a.ResponseWriter.Write(b)
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

// NewServer returns an http.Server with conservative timeouts. There is no
// overall read or write timeout: speed-test streams last as long as they last
// (their handlers set per-chunk deadlines).
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}
