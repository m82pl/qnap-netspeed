// Package lan is the iperf3 client (spec pkt 5.2.2): it builds the command
// lines, parses `iperf3 -J` and fills the lan.jsonl record (pkt 13.2).
package lan

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/history"
	"github.com/m82pl/qnap-netspeed/internal/link"
	"github.com/m82pl/qnap-netspeed/internal/netroute"
	"github.com/m82pl/qnap-netspeed/internal/runner"
)

// Warning codes.
const (
	WarnCPULimited  = "cpu_limited"
	WarnConcurrent  = "concurrent_inbound_test"
	cpuLimitPercent = 90.0
)

// Params describe one `lan-test` call.
type Params struct {
	Host     string
	Port     int
	Duration int
	Parallel int
	Iface    string // "" = let the routing table decide
	UDP      bool
	Bitrate  string // for UDP, e.g. "1G"
	Bidir    bool
}

// Pass is one iperf3 run.
type Pass struct {
	Name string // "upload", "download", "bidir", "udp"
	Args []string
}

// Passes returns the iperf3 runs for p, in order (spec pkt 5.2.2).
// bindAddr is the -B address for --iface ("" for none).
func Passes(p Params, bindAddr string) []Pass {
	base := func(extra ...string) []string {
		a := []string{"-c", p.Host, "-p", strconv.Itoa(p.Port), "-J", "-t", strconv.Itoa(p.Duration)}
		if bindAddr != "" {
			a = append(a, "-B", bindAddr)
		}
		return append(a, extra...)
	}
	par := strconv.Itoa(p.Parallel)
	var out []Pass
	if p.Bidir {
		out = append(out, Pass{"bidir", base("-P", par, "--bidir")})
	} else {
		out = append(out, Pass{"upload", base("-P", par)}, Pass{"download", base("-P", par, "-R")})
	}
	if p.UDP {
		out = append(out, Pass{"udp", base("-u", "-b", p.Bitrate)})
	}
	return out
}

// Timeout is the limit of one run: T + 30 s (spec pkt 5.2.2).
func Timeout(duration int) time.Duration { return time.Duration(duration+30) * time.Second }

// Output is the part of `iperf3 -J` that qnetspeed uses.
type Output struct {
	Version     string
	Error       string
	ReceivedBps float64  // end.sum_received (TCP) or end.sum (UDP)
	Retransmits *int     // end.sum_sent.retransmits
	ReverseBps  *float64 // end.sum_received_bidir_reverse
	JitterMs    *float64
	LostPercent *float64
	CPUHost     *float64
}

// ParseOutput reads the JSON iperf3 printed with -J.
func ParseOutput(res runner.Result) (Output, error) {
	var j struct {
		Start struct {
			Version string `json:"version"`
		} `json:"start"`
		End struct {
			SumReceived *struct {
				Bps    float64  `json:"bits_per_second"`
				Jitter *float64 `json:"jitter_ms"`
				Lost   *float64 `json:"lost_percent"`
			} `json:"sum_received"`
			SumSent *struct {
				Retransmits *int `json:"retransmits"`
			} `json:"sum_sent"`
			ReverseReceived *struct {
				Bps float64 `json:"bits_per_second"`
			} `json:"sum_received_bidir_reverse"`
			Sum *struct {
				Bps    float64  `json:"bits_per_second"`
				Jitter *float64 `json:"jitter_ms"`
				Lost   *float64 `json:"lost_percent"`
			} `json:"sum"`
			CPU *struct {
				HostTotal *float64 `json:"host_total"`
			} `json:"cpu_utilization_percent"`
		} `json:"end"`
		Error string `json:"error"`
	}
	body := bytes.TrimSpace(res.Stdout)
	if len(body) == 0 {
		msg := strings.TrimSpace(string(res.Stderr))
		if msg == "" {
			msg = fmt.Sprintf("iperf3 printed nothing (exit code %d)", res.ExitCode)
		}
		return Output{}, errors.New(msg)
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return Output{}, fmt.Errorf("cannot parse iperf3 output: %w", err)
	}
	o := Output{Version: strings.TrimPrefix(j.Start.Version, "iperf "), Error: j.Error}
	if j.Error != "" {
		return o, errors.New(strings.TrimPrefix(j.Error, "error - "))
	}
	e := j.End
	switch {
	case e.SumReceived != nil:
		o.ReceivedBps = e.SumReceived.Bps
	case e.Sum != nil:
		o.ReceivedBps = e.Sum.Bps
	default:
		return o, errors.New("iperf3 output has no result (end.sum_received)")
	}
	if e.SumSent != nil {
		o.Retransmits = e.SumSent.Retransmits
	}
	if e.ReverseReceived != nil {
		v := e.ReverseReceived.Bps
		o.ReverseBps = &v
	}
	// UDP: iperf3 3.16+ prints jitter and loss in end.sum_received (the receiver's
	// view, also in end.sum); older versions only in end.sum. TCP has neither.
	switch {
	case e.SumReceived != nil && e.SumReceived.Jitter != nil:
		o.JitterMs, o.LostPercent = e.SumReceived.Jitter, e.SumReceived.Lost
	case e.Sum != nil:
		o.JitterMs, o.LostPercent = e.Sum.Jitter, e.Sum.Lost
	}
	if e.CPU != nil {
		o.CPUHost = e.CPU.HostTotal
	}
	return o, nil
}

func mbps(bps float64) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(bps/1e6, 'f', 2, 64), 64)
	return v
}

// Record is a lan.jsonl line (spec pkt 13.2).
type Record struct {
	Schema    int             `json:"schema"`
	ID        string          `json:"id"`
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Trigger   string          `json:"trigger"`
	Status    string          `json:"status"`
	Target    string          `json:"target"`
	Interface string          `json:"interface"`
	Physical  []link.Physical `json:"physical"`
	MTU       int             `json:"mtu"`
	Params    RecordParams    `json:"params"`
	Results   *Results        `json:"results"`
	Warnings  []string        `json:"warnings"`
	Engine    struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"engine"`
	DurationS float64 `json:"duration_s"`
	Error     *string `json:"error"`
}

type RecordParams struct {
	Protocol string `json:"protocol"`
	Duration int    `json:"duration_s"`
	Parallel int    `json:"parallel"`
	Bitrate  string `json:"bitrate,omitempty"`
	Bidir    bool   `json:"bidir,omitempty"`
}

type Results struct {
	UploadMbps          float64  `json:"upload_mbps"`
	DownloadMbps        float64  `json:"download_mbps"`
	RetransmitsUpload   *int     `json:"retransmits_upload"`
	RetransmitsDownload *int     `json:"retransmits_download"` // always null: -R retransmits are counted by the remote side
	JitterMs            *float64 `json:"jitter_ms"`
	LostPercent         *float64 `json:"lost_percent"`
	CPULocalPercent     *float64 `json:"cpu_local_percent"`
}

// Engine runs LAN tests.
type Engine struct {
	Runner runner.Runner
	Net    netroute.System
	Sys    interface { // /sys/class/net access, for physical interfaces and MTU
		Resolve(name string) []link.Physical
		MTU(name string) int
	}
	Bin string
	Now func() time.Time
	// CPUs is the number of cores of the NAS; it scales the cpu_limited check (0 = one core).
	CPUs int
	// Inbound reports an active incoming iperf3 connection (spec pkt 3.2). May be nil.
	Inbound func(port int) bool
}

// Run performs the passes and returns the record (never an error).
func (e *Engine) Run(ctx context.Context, p Params, trigger string) Record {
	start := e.Now()
	rec := Record{Schema: 1, ID: history.NewID(start), Timestamp: start.UTC().Truncate(time.Second),
		Type: "LAN", Trigger: trigger, Target: p.Host, Warnings: []string{}, Physical: []link.Physical{}}
	rec.Engine.Name = "iperf3"
	rec.Params = RecordParams{Protocol: "tcp", Duration: p.Duration, Parallel: p.Parallel, Bidir: p.Bidir}
	if p.UDP {
		rec.Params.Protocol, rec.Params.Bitrate = "tcp+udp", p.Bitrate
	}
	fail := func(err error) Record {
		rec.Status = "error"
		msg := err.Error()
		if errors.Is(err, runner.ErrTimeout) {
			msg = fmt.Sprintf("iperf3 did not finish within %s", Timeout(p.Duration))
		}
		rec.Error = &msg
		rec.DurationS = float64(int(e.Now().Sub(start).Seconds()*10+0.5)) / 10
		return rec
	}

	bind := ""
	rec.Interface = p.Iface
	if p.Iface != "" {
		addrs, err := e.Net.IPv4Addrs(p.Iface)
		if err != nil {
			return fail(fmt.Errorf("interface %s: %w", p.Iface, err))
		}
		a := netroute.FirstGlobal(addrs)
		if a == nil {
			return fail(fmt.Errorf("interface %s has no usable IPv4 address", p.Iface))
		}
		bind = a.String()
	} else if ip := net.ParseIP(p.Host); ip != nil {
		if r, err := e.Net.RouteGet(nil, ip); err == nil {
			rec.Interface = r.EgressIface
		}
	}
	if rec.Interface != "" && e.Sys != nil {
		if ph := e.Sys.Resolve(rec.Interface); ph != nil {
			rec.Physical = ph
		}
		rec.MTU = e.Sys.MTU(rec.Interface)
	}

	var res Results
	for _, pass := range Passes(p, bind) {
		r, err := e.Runner.Run(ctx, Timeout(p.Duration), e.Bin, pass.Args...)
		if err != nil {
			return fail(err)
		}
		o, err := ParseOutput(r)
		if err != nil {
			return fail(fmt.Errorf("%s pass: %w", pass.Name, err))
		}
		rec.Engine.Version = o.Version
		switch pass.Name {
		case "upload":
			res.UploadMbps, res.RetransmitsUpload = mbps(o.ReceivedBps), o.Retransmits
		case "download":
			res.DownloadMbps = mbps(o.ReceivedBps)
		case "bidir":
			res.UploadMbps, res.RetransmitsUpload = mbps(o.ReceivedBps), o.Retransmits
			if o.ReverseBps != nil {
				res.DownloadMbps = mbps(*o.ReverseBps)
			}
		case "udp":
			res.JitterMs, res.LostPercent = o.JitterMs, o.LostPercent
		}
		if o.CPUHost != nil && pass.Name != "udp" && (res.CPULocalPercent == nil || *o.CPUHost > *res.CPULocalPercent) {
			v := *o.CPUHost
			res.CPULocalPercent = &v
		}
	}
	// host_total is the CPU time of all threads relative to one core (a 4-stream
	// test on 4 cores can show 400%); iperf3 3.16+ runs one thread per stream.
	threads := p.Parallel
	if p.Bidir {
		threads *= 2
	}
	cores := e.CPUs
	if cores < 1 {
		cores = 1
	}
	if threads > cores {
		threads = cores
	}
	if threads < 1 {
		threads = 1
	}
	if res.CPULocalPercent != nil && *res.CPULocalPercent > cpuLimitPercent*float64(threads) {
		rec.Warnings = append(rec.Warnings, WarnCPULimited)
	}
	if e.Inbound != nil && e.Inbound(p.Port) {
		rec.Warnings = append(rec.Warnings, WarnConcurrent)
	}
	rec.Status, rec.Results = "ok", &res
	rec.DurationS = float64(int(e.Now().Sub(start).Seconds()*10+0.5)) / 10
	return rec
}

// InboundFromProcTCP reports whether /proc/net/tcp shows an established
// connection to the local port (an incoming iperf3 test, spec pkt 3.2).
func InboundFromProcTCP(r io.Reader, port int) bool {
	want := fmt.Sprintf(":%04X", port)
	sc := bufio.NewScanner(r)
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) > 3 && strings.HasSuffix(f[1], want) && f[3] == "01" {
			return true
		}
	}
	return false
}
