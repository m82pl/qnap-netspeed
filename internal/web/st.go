package web

import (
	"crypto/rand"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/m82pl/qnap-netspeed/internal/history"
	"github.com/m82pl/qnap-netspeed/internal/lock"
)

// Limits of the browser test (spec pkt 5.2.3).
const (
	stBufSize      = 4 << 20
	stDefaultMB    = 100
	stMaxMB        = 1024
	stMaxUploadB   = 1 << 30
	stIdleHold     = 5 * time.Second  // the lock outlives a stream by this long (gap between phases)
	stIOTimeout    = 30 * time.Second // a stalled browser must not hold the lock
	stResultsPerIP = 30               // POST /api/st/result per minute and address
	maxUserAgent   = 200
)

// browserGate holds the test lock for as long as browser streams are running
// and for stIdleHold after the last one: the browser test is many requests,
// but for cron, the CLI and the API it is one test (spec pkt 3.2).
type browserGate struct {
	lockFile string
	hold     time.Duration

	mu     sync.Mutex
	active int
	l      *lock.Lock
	timer  *time.Timer
}

// acquire registers a stream. A *lock.BusyError means that another test runs.
func (g *browserGate) acquire() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.l == nil {
		l, err := lock.Acquire(g.lockFile, "lan-browser", 0)
		if err != nil {
			return err
		}
		g.l = l
	}
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
	g.active++
	return nil
}

// release unregisters a stream; the lock is let go after the hold time.
func (g *browserGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active--
	if g.active > 0 {
		return
	}
	g.timer = time.AfterFunc(g.hold, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.active == 0 && g.l != nil {
			g.l.Release()
			g.l = nil
		}
	})
}

// close lets go of the lock at once (daemon shutdown).
func (g *browserGate) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.timer != nil {
		g.timer.Stop()
	}
	if g.l != nil {
		g.l.Release()
		g.l = nil
	}
}

// GET /api/st/ping
func (s *Server) stPing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/st/download?mb=N: N MiB of random data from a 4 MiB buffer.
func (s *Server) stDownload(w http.ResponseWriter, r *http.Request) {
	mb := stDefaultMB
	if v := r.URL.Query().Get("mb"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > stMaxMB {
			writeError(w, http.StatusBadRequest, "bad_request", "mb must be an integer 1.."+strconv.Itoa(stMaxMB))
			return
		}
		mb = n
	}
	if err := s.gate.acquire(); err != nil {
		writeBusy(w, err)
		return
	}
	defer s.gate.release()
	total := int64(mb) << 20
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(total, 10))
	h.Set("Cache-Control", "no-store")
	rc := http.NewResponseController(w)
	for sent := int64(0); sent < total; {
		n := int64(len(s.buf))
		if total-sent < n {
			n = total - sent
		}
		rc.SetWriteDeadline(time.Now().Add(stIOTimeout))
		if _, err := w.Write(s.buf[:n]); err != nil {
			return // the browser went away
		}
		sent += n
	}
}

// POST /api/st/upload: reads and discards the body, answers with the byte count and the server-side time.
func (s *Server) stUpload(w http.ResponseWriter, r *http.Request) {
	if err := s.gate.acquire(); err != nil {
		writeBusy(w, err)
		return
	}
	defer s.gate.release()
	start := time.Now()
	rc := http.NewResponseController(w)
	body := http.MaxBytesReader(w, r.Body, stMaxUploadB)
	buf := make([]byte, 256<<10)
	var n int64
	for {
		rc.SetReadDeadline(time.Now().Add(stIOTimeout))
		m, err := body.Read(buf)
		n += int64(m)
		if err == io.EOF {
			break
		}
		if err != nil {
			code := http.StatusBadRequest
			if _, ok := err.(*http.MaxBytesError); ok {
				code = http.StatusRequestEntityTooLarge
			}
			writeError(w, code, "upload_failed", err.Error())
			return
		}
	}
	rc.SetReadDeadline(time.Time{})
	writeJSON(w, http.StatusOK, struct {
		Bytes   int64   `json:"bytes"`
		Seconds float64 `json:"seconds"`
	}{n, math.Round(time.Since(start).Seconds()*1000) / 1000})
}

// browserResult is what the page posts after a test.
type browserResult struct {
	DownloadMbps float64 `json:"download_mbps"`
	UploadMbps   float64 `json:"upload_mbps"`
	PingMs       float64 `json:"ping_ms"`
	JitterMs     float64 `json:"jitter_ms"`
	DurationS    int     `json:"duration_s"`
	Parallel     int     `json:"parallel"`
}

func finiteRange(v, hi float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= hi }

// POST /api/st/result: the browser saves its result as LAN_BROWSER in lan.jsonl.
func (s *Server) stResult(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.results.blocked(ip) {
		writeError(w, http.StatusTooManyRequests, "too_many_results", "too many results from this address, try again later")
		return
	}
	var in browserResult
	if !decodeJSON(w, r, &in, false) {
		return
	}
	switch {
	case !finiteRange(in.DownloadMbps, 1e6), !finiteRange(in.UploadMbps, 1e6),
		!finiteRange(in.PingMs, 1e5), !finiteRange(in.JitterMs, 1e5):
		writeError(w, http.StatusUnprocessableEntity, "bad_result", "download_mbps, upload_mbps, ping_ms and jitter_ms must be numbers within a sane range")
		return
	case in.DurationS < 0 || in.DurationS > 600 || in.Parallel < 0 || in.Parallel > 64:
		writeError(w, http.StatusUnprocessableEntity, "bad_result", "duration_s must be 0..600 and parallel 0..64")
		return
	}
	s.results.add(ip)
	now := s.o.Now().UTC().Truncate(time.Second)
	rec := map[string]any{
		"schema": 1, "id": history.NewID(now), "timestamp": now,
		"type": "LAN_BROWSER", "trigger": "web", "status": "ok",
		"client_ip": ip, "user_agent": cleanUA(r.UserAgent()),
		"params": map[string]any{"duration_s": in.DurationS, "parallel": in.Parallel},
		"results": map[string]any{
			"download_mbps": round2(in.DownloadMbps), "upload_mbps": round2(in.UploadMbps),
			"ping_ms": round2(in.PingMs), "jitter_ms": round2(in.JitterMs),
		},
		"warnings": []string{}, "error": nil,
	}
	if err := s.o.Backend.AppendLAN(rec); err != nil {
		s.o.Logf("browser result: cannot write history: %v", err)
		writeError(w, http.StatusInternalServerError, "history_failed", "cannot write the history file")
		return
	}
	b, _ := json.Marshal(rec["id"])
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write([]byte(`{"id":` + string(b) + `}` + "\n"))
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// cleanUA drops control characters and cuts the User-Agent to maxUserAgent bytes.
func cleanUA(ua string) string {
	ua = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return ' '
		}
		return r
	}, ua)
	if len(ua) > maxUserAgent {
		ua = strings.ToValidUTF8(ua[:maxUserAgent], "")
	}
	return strings.TrimSpace(ua)
}

// newTestBuffer fills the download buffer with random bytes once, so the
// stream cannot be compressed away by a proxy and costs the CPU nothing.
func newTestBuffer() []byte {
	b := make([]byte, stBufSize)
	rand.Read(b)
	return b
}
