package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/config"
)

// Access levels of an endpoint (spec pkt 14.3).
type access int

const (
	// accessPublic is open to everyone: the static page that asks for the token.
	accessPublic access = iota
	// accessRead is open when WEB_PUBLIC_READ=1, otherwise it needs the token.
	accessRead
	// accessAdmin always needs the token.
	accessAdmin
)

const (
	authFailLimit  = 10 // wrong tokens per window and address (spec pkt 14.2)
	authFailWindow = time.Minute
)

// LoadOrCreateToken returns the administrator token from path, creating it
// (32 random bytes, base64url, mode 0600) when the file does not exist.
func LoadOrCreateToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if tok := strings.TrimSpace(string(b)); len(tok) >= 32 {
			return tok, nil
		}
		// An empty or truncated file would make the token guessable: replace it.
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw[:])
	if err := config.WriteFileAtomic(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	// WriteFileAtomic keeps the mode of an existing file; make sure it is private.
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// limiter counts events per key inside a sliding window.
type limiter struct {
	mu     sync.Mutex
	now    func() time.Time
	window time.Duration
	max    int
	hits   map[string][]time.Time
}

func newLimiter(now func() time.Time, window time.Duration, max int) *limiter {
	return &limiter{now: now, window: window, max: max, hits: map[string][]time.Time{}}
}

// maxKeys bounds the memory used by the limiter (many addresses are a
// scan, not a user; forgetting them all only resets the counters).
const maxKeys = 4096

func (l *limiter) prune(key string) []time.Time {
	cut := l.now().Add(-l.window)
	h := l.hits[key]
	i := 0
	for i < len(h) && !h[i].After(cut) {
		i++
	}
	h = h[i:]
	if len(h) == 0 {
		delete(l.hits, key)
		return nil
	}
	l.hits[key] = h
	return h
}

// blocked reports whether key has used up its allowance.
func (l *limiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key)) >= l.max
}

// retryAfter is how long until key is allowed again (0 when it is).
func (l *limiter) retryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	h := l.prune(key)
	if len(h) < l.max {
		return 0
	}
	return h[len(h)-l.max].Add(l.window).Sub(l.now())
}

// add records one event for key.
func (l *limiter) add(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.hits) >= maxKeys {
		l.hits = map[string][]time.Time{}
	}
	l.prune(key)
	l.hits[key] = append(l.hits[key], l.now())
}

// clientIP is the address of the peer. X-Forwarded-For is deliberately not
// trusted: there is no proxy in front of the daemon, and a client could set it.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// tokenOK compares in constant time (the digests have equal length).
func (s *Server) tokenOK(got string) bool {
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(s.token))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}
	return strings.TrimSpace(h[7:]), true
}

// guard wraps a handler with the access rules of spec pkt 14.2.
func (s *Server) guard(level access, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if level == accessPublic {
			h(w, r)
			return
		}
		ip := clientIP(r)
		if level == accessRead {
			c, err := s.o.Backend.Config()
			if err != nil {
				writeError(w, http.StatusInternalServerError, "config_invalid", err.Error())
				return
			}
			if c.Get("WEB_PUBLIC_READ") == "1" {
				h(w, r)
				return
			}
		}
		// The token is needed. A blocked address gets no answer about the token at all.
		if s.failures.blocked(ip) {
			w.Header().Set("Retry-After", fmt.Sprint(int(s.failures.retryAfter(ip).Seconds())+1))
			writeError(w, http.StatusTooManyRequests, "too_many_attempts", "too many wrong tokens, try again later")
			return
		}
		tok, ok := bearer(r)
		switch {
		case !ok:
			w.Header().Set("WWW-Authenticate", `Bearer realm="qnap-netspeed"`)
			writeError(w, http.StatusUnauthorized, "token_required", "send the administrator token as 'Authorization: Bearer <token>' (qnetspeed web-token shows it)")
			return
		case !s.tokenOK(tok):
			s.failures.add(ip)
			s.o.Logf("auth: wrong token from %s for %s %s", ip, r.Method, r.URL.Path)
			w.Header().Set("WWW-Authenticate", `Bearer realm="qnap-netspeed"`)
			writeError(w, http.StatusUnauthorized, "token_invalid", "wrong token")
			return
		}
		if level == accessAdmin {
			s.o.Logf("admin: %s %s from %s", r.Method, r.URL.Path, ip)
		}
		h(w, r)
	}
}
