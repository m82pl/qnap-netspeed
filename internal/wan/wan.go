// Package wan runs the internet speed test with librespeed-cli (spec pkt 5.1):
// builds the command line, parses its JSON (docs/F0.md, W1), checks the route
// of every tested interface (multi-WAN) and fills the history record (pkt 13.1).
package wan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/history"
	"github.com/m82pl/qnap-netspeed/internal/netroute"
	"github.com/m82pl/qnap-netspeed/internal/runner"
)

// Engine version reported in history; the binary is pinned in third_party/versions.env.
const engineName = "librespeed-cli"

// Warning codes (spec pkt 13.1).
const (
	WarnRouteMismatch = "route_mismatch"
	WarnSamePublicIP  = "same_public_ip"
	WarnConcurrent    = "concurrent_inbound_test"
)

// Status values.
const (
	StatusOK      = "ok"
	StatusError   = "error"
	StatusSkipped = "skipped"
)

// Result is one librespeed-cli measurement. Speeds are Mb/s.
type Result struct {
	ServerName string
	ServerURL  string
	ClientIP   string
	Download   float64
	Upload     float64
	Ping       float64
	Jitter     float64
}

// ParseOutput interprets what librespeed-cli --json printed (docs/F0.md, W1):
// exit 0 with "null" or "[]" means the chosen server did not respond; exit 1
// carries the reason on stderr; download/upload are already Mb/s.
func ParseOutput(res runner.Result) (Result, error) {
	out := bytes.TrimSpace(res.Stdout)
	if res.ExitCode != 0 {
		return Result{}, errors.New(stderrReason(res.Stderr, res.ExitCode))
	}
	if len(out) == 0 {
		return Result{}, errors.New("librespeed-cli printed nothing")
	}
	var arr []struct {
		Server struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"server"`
		Client struct {
			IP string `json:"ip"`
		} `json:"client"`
		Ping     *float64 `json:"ping"`
		Jitter   *float64 `json:"jitter"`
		Upload   *float64 `json:"upload"`
		Download *float64 `json:"download"`
	}
	if err := json.Unmarshal(out, &arr); err != nil {
		return Result{}, fmt.Errorf("cannot parse librespeed-cli output: %w", err)
	}
	if len(arr) == 0 { // `null` and `[]`
		return Result{}, errors.New("the speed test server did not respond")
	}
	e := arr[0]
	if e.Download == nil || e.Upload == nil || e.Ping == nil || e.Jitter == nil {
		return Result{}, errors.New("librespeed-cli output lacks download, upload, ping or jitter")
	}
	r := Result{
		ServerName: e.Server.Name, ServerURL: e.Server.URL,
		Download: round2(*e.Download), Upload: round2(*e.Upload),
		Ping: round2(*e.Ping), Jitter: round2(*e.Jitter),
	}
	if ip := net.ParseIP(strings.TrimSpace(e.Client.IP)); ip != nil {
		r.ClientIP = ip.String()
	}
	return r, nil
}

func round2(v float64) float64 {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	v, _ = strconv.ParseFloat(s, 64)
	return v
}

// stderrReason picks the useful line of librespeed-cli's error output.
func stderrReason(stderr []byte, code int) string {
	var last string
	for _, l := range strings.Split(string(stderr), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			last = l
		}
	}
	last = strings.TrimPrefix(last, "Terminated due to error: ")
	if last == "" {
		return fmt.Sprintf("librespeed-cli exited with code %d", code)
	}
	if len(last) > 300 {
		last = last[:300]
	}
	return last
}

// Settings are the WAN_* configuration values.
type Settings struct {
	Duration   int
	BindMode   string // "source" or "interface"
	ServerIDs  []int
	ServerList string
	Secure     bool
	ProbeIP    net.IP
	IPEchoURL  string
	IperfPort  int // for the concurrent_inbound_test warning
}

// Engine runs WAN tests.
type Engine struct {
	Runner  runner.Runner
	Net     netroute.System
	Bin     string // path of librespeed-cli
	Version string // from `qnetspeed version`, may be empty
	Now     func() time.Time
	// EchoIP asks WAN_IP_ECHO_URL for the public address seen from src. Tests replace it.
	EchoIP func(ctx context.Context, url string, src net.IP) (string, error)
	// Inbound reports an incoming iperf3 test on the port (spec pkt 3.2). May be nil.
	Inbound func(port int) bool
}

// Timeout is the limit of one librespeed-cli run (spec pkt 5.1).
func Timeout(duration int) time.Duration { return time.Duration(duration*2+60) * time.Second }

// Args builds the librespeed-cli command line for an interface (iface may be
// "" for the default route). addr is the IPv4 address of iface.
func Args(s Settings, iface string, addr net.IP) []string {
	args := []string{"--json", "--no-icmp", "--duration", strconv.Itoa(s.Duration), "--timeout", "15"}
	for _, id := range s.ServerIDs {
		args = append(args, "--server", strconv.Itoa(id))
	}
	switch {
	case iface == "":
	case s.BindMode == "interface":
		args = append(args, "--interface", iface)
	default:
		args = append(args, "--source", addr.String())
	}
	if s.Secure {
		args = append(args, "--secure")
	}
	if s.ServerList != "" {
		args = append(args, "--local-json", s.ServerList)
	}
	return args
}

// Record is a wan.jsonl line (spec pkt 13.1).
type Record struct {
	Schema    int             `json:"schema"`
	ID        string          `json:"id"`
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Trigger   string          `json:"trigger"`
	Status    string          `json:"status"`
	Interface string          `json:"interface"`
	Bind      *Bind           `json:"bind"`
	Route     *netroute.Route `json:"route"`
	PublicIP  *string         `json:"public_ip"`
	Server    *Server         `json:"server"`
	Results   *Results        `json:"results"`
	Warnings  []string        `json:"warnings"`
	Engine    EngineRef       `json:"engine"`
	DurationS float64         `json:"duration_s"`
	Error     *string         `json:"error"`
	// Reason is set only for status "skipped" (no_ipv4, no_iface).
	Reason string `json:"reason,omitempty"`
	// TimedOut marks an error caused by the time limit (exit code 124); not stored.
	TimedOut bool `json:"-"`
}

type Bind struct {
	Mode    string `json:"mode"`
	Address string `json:"address"`
}
type Server struct {
	ID   *int   `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}
type Results struct {
	DownloadMbps float64 `json:"download_mbps"`
	UploadMbps   float64 `json:"upload_mbps"`
	PingMs       float64 `json:"ping_ms"`
	JitterMs     float64 `json:"jitter_ms"`
}
type EngineRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// RunInterface measures one interface ("" = default route, no binding).
// It never returns an error: every outcome is a Record.
func (e *Engine) RunInterface(ctx context.Context, s Settings, iface, trigger string) Record {
	start := e.Now()
	rec := Record{
		Schema: 1, ID: history.NewID(start), Timestamp: start.UTC().Truncate(time.Second), Type: "WAN",
		Trigger: trigger, Interface: iface, Warnings: []string{},
		Engine: EngineRef{Name: engineName, Version: e.Version},
	}
	if iface == "" {
		rec.Interface = "default"
	}
	var addr net.IP
	if iface != "" {
		chk := netroute.Inspect(e.Net, iface, s.ProbeIP)
		if chk.Reason != "" {
			rec.Status, rec.Reason = StatusSkipped, chk.Reason
			msg := skipMessage(chk)
			rec.Error = &msg
			return rec
		}
		addr = chk.Address
		rec.Bind = &Bind{Mode: s.BindMode, Address: addr.String()}
		rec.Route = chk.Route // nil if the lookup failed: the test still runs
		if chk.Route != nil && chk.Route.Mismatch {
			rec.Warnings = append(rec.Warnings, WarnRouteMismatch)
		}
	}
	res, err := e.Runner.Run(ctx, Timeout(s.Duration), e.Bin, Args(s, iface, addr)...)
	rec.DurationS = round1(e.Now().Sub(start).Seconds())
	if err == nil {
		var r Result
		if r, err = ParseOutput(res); err == nil {
			rec.Status = StatusOK
			rec.Results = &Results{r.Download, r.Upload, r.Ping, r.Jitter}
			rec.Server = &Server{Name: r.ServerName, URL: r.ServerURL}
			if len(s.ServerIDs) == 1 { // librespeed-cli does not report the id
				id := s.ServerIDs[0]
				rec.Server.ID = &id
			}
			if r.ClientIP != "" {
				rec.PublicIP = &r.ClientIP
			} else if s.IPEchoURL != "" && addr != nil && e.EchoIP != nil {
				if ip, eerr := e.EchoIP(ctx, s.IPEchoURL, addr); eerr == nil {
					rec.PublicIP = &ip
				}
			}
			if e.Inbound != nil && s.IperfPort > 0 && e.Inbound(s.IperfPort) {
				rec.Warnings = append(rec.Warnings, WarnConcurrent)
			}
			return rec
		}
	}
	rec.Status = StatusError
	msg := err.Error()
	if errors.Is(err, runner.ErrTimeout) {
		rec.TimedOut = true
		msg = fmt.Sprintf("librespeed-cli did not finish within %s", Timeout(s.Duration))
	}
	rec.Error = &msg
	return rec
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

func skipMessage(c netroute.Check) string {
	if c.Reason == netroute.ReasonNoIPv4 {
		if c.Master != "" {
			return fmt.Sprintf("interface %s has no IPv4 address; it is a member of %s, which holds the address: put %s in WAN_IFACES",
				c.Iface, c.Master, c.Master)
		}
		return fmt.Sprintf("interface %s has no usable IPv4 address", c.Iface)
	}
	return fmt.Sprintf("interface %s: %v", c.Iface, c.Err)
}

// RunAll measures the interfaces one after another (tests would distort each
// other otherwise) and adds the same_public_ip warning across the results.
// onDone is called after each interface, so the caller can store and print it.
func (e *Engine) RunAll(ctx context.Context, s Settings, ifaces []string, trigger string, onDone func(Record)) []Record {
	if len(ifaces) == 0 {
		ifaces = []string{""}
	}
	var out []Record
	for _, ifc := range ifaces {
		if ctx.Err() != nil {
			break
		}
		r := e.RunInterface(ctx, s, ifc, trigger)
		out = append(out, r)
		MarkSamePublicIP(out)
		if onDone != nil {
			onDone(out[len(out)-1])
		}
	}
	return out
}

// MarkSamePublicIP adds same_public_ip to the last record when an earlier
// successful record of the same run shows the same public address.
func MarkSamePublicIP(recs []Record) {
	last := &recs[len(recs)-1]
	if last.Status != StatusOK || last.PublicIP == nil {
		return
	}
	for _, p := range recs[:len(recs)-1] {
		if p.Status == StatusOK && p.PublicIP != nil && *p.PublicIP == *last.PublicIP && p.Interface != last.Interface {
			last.Warnings = append(last.Warnings, WarnSamePublicIP)
			return
		}
	}
}

// EchoHTTP asks url for the public address, with the connection bound to src.
func EchoHTTP(ctx context.Context, url string, src net.IP) (string, error) {
	d := &net.Dialer{LocalAddr: &net.TCPAddr{IP: src}, Timeout: 10 * time.Second}
	cl := &http.Client{Transport: &http.Transport{DialContext: d.DialContext}, Timeout: 15 * time.Second}
	defer cl.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(strings.TrimSpace(string(b)))
	if ip == nil {
		return "", errors.New("answer is not an IP address")
	}
	return ip.String(), nil
}

// DefaultBin returns the librespeed-cli path next to qnetspeed.
func DefaultBin(binDir string) string { return filepath.Join(binDir, "librespeed-cli") }
