package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/config"
	"github.com/m82pl/qnap-netspeed/internal/history"
	"github.com/m82pl/qnap-netspeed/internal/lan"
	"github.com/m82pl/qnap-netspeed/internal/link"
	"github.com/m82pl/qnap-netspeed/internal/notify"
	"github.com/m82pl/qnap-netspeed/internal/wan"
	"github.com/m82pl/qnap-netspeed/internal/web"
)

// backend connects the HTTP API to the same engines the CLI uses.
type backend struct {
	a *app

	mu    sync.Mutex
	cfg   *config.Config
	mtime time.Time
	size  int64
	have  bool
}

var _ web.Backend = (*backend)(nil)

// Config returns the configuration, re-read when the file changes, so an edit
// with `qnetspeed config set` reaches tests and WEB_PUBLIC_READ without a restart.
func (b *backend) Config() (*config.Config, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var mtime time.Time
	var size int64 = -1
	if fi, err := os.Stat(b.a.layout.ConfigFile()); err == nil {
		mtime, size = fi.ModTime(), fi.Size()
	}
	if b.have && b.mtime.Equal(mtime) && b.size == size {
		return b.cfg, nil
	}
	c, issues, code := b.a.loadConfig(false)
	if code != exitOK {
		for _, i := range issues {
			if i.Severity == config.Error {
				return nil, fmt.Errorf("invalid configuration: %s", i)
			}
		}
		return nil, fmt.Errorf("invalid configuration")
	}
	b.cfg, b.mtime, b.size, b.have = c, mtime, size, true
	return c, nil
}

func (b *backend) SaveConfig(values map[string]string) (web.SaveResult, error) {
	var res web.SaveResult
	path := b.a.layout.ConfigFile()
	c, issues, err := config.LoadForEdit(path, b.a.layout.DefaultConfig())
	if err != nil {
		return res, err
	}
	fields := web.FieldErrors{}
	for k, v := range values {
		if !config.Known(k) {
			fields[k] = "unknown key"
		} else if err := config.ValidateValue(k, v); err != nil {
			fields[k] = err.Error()
		}
	}
	if len(fields) > 0 {
		return res, fields
	}
	for k, v := range values {
		old := c.Get(k)
		if err := c.Set(k, v); err != nil { // cannot happen after ValidateValue
			return res, err
		}
		res.Saved = append(res.Saved, k)
		if old != v && config.RestartRequired(k) {
			res.RestartRequired = append(res.RestartRequired, k)
		}
	}
	issues = append(issues, c.Validate()...)
	if config.HasErrors(issues) {
		for _, i := range issues {
			if i.Severity == config.Error {
				key := i.Key
				if key == "" {
					key = "_"
				}
				fields[key] = i.Msg
			}
		}
		return res, fields
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return res, err
	}
	if err := config.WriteFileAtomic(path, c.Bytes(), 0o644); err != nil {
		return res, err
	}
	res.Issues = issues
	return res, nil
}

func (b *backend) History(typ string, f history.Filter) ([]history.Record, int, error) {
	var path string
	switch typ {
	case "wan":
		path = b.a.layout.WanHistory()
	case "lan":
		path = b.a.layout.LanHistory()
	default:
		path = b.a.layout.LinkHistory()
	}
	return history.Store{Path: path}.Read(f)
}

func (b *backend) Link() (link.Record, error) {
	c, err := b.Config()
	if err != nil {
		return link.Record{}, err
	}
	sys := b.a.sys()
	all, err := link.Read(sys.sysfs)
	if err != nil {
		return link.Record{}, err
	}
	return link.Record{Schema: 1, Timestamp: sys.now().UTC().Truncate(time.Second), Type: "LINK",
		Interfaces: all, Warnings: link.Evaluate(all, link.ParseExpect(c.Get("LINK_EXPECT")))}, nil
}

func (b *backend) Routes(all bool) (any, error) {
	c, err := b.Config()
	if err != nil {
		return nil, err
	}
	return b.a.routeRows(c, all)
}

func (b *backend) Warnings() []string {
	st, err := notify.LoadState(b.a.layout.StateFile())
	if err != nil || st == nil {
		return nil
	}
	return st.Active()
}

// triggerWeb is the "trigger" of records written for the API and the browser test (spec pkt 13.1).
const triggerWeb = "web"

func (b *backend) RunWAN(ctx context.Context, ifaces []string, serverIDs []int) ([]any, error) {
	c, err := b.Config()
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(serverIDs))
	for i, n := range serverIDs {
		ids[i] = strconv.Itoa(n)
	}
	s, bad := b.a.wanSettings(c, ids)
	if bad != nil {
		return nil, bad
	}
	if len(ifaces) == 0 {
		ifaces = strings.Fields(c.Get("WAN_IFACES"))
	}
	sys := b.a.sys()
	eng := &wan.Engine{Runner: sys.run, Net: sys.net, Bin: wan.DefaultBin(b.a.binDir),
		Version: wanEngineVersion, Now: sys.now, EchoIP: wan.EchoHTTP, Inbound: b.a.inbound}
	recs := eng.RunAll(ctx, s, ifaces, triggerWeb, func(r wan.Record) {
		b.a.store(b.a.layout.WanHistory(), c, r)
	})
	out := make([]any, len(recs))
	for i, r := range recs {
		out[i] = r
	}
	return out, nil
}

func (b *backend) RunLAN(ctx context.Context, req web.LANRequest) (any, error) {
	c, err := b.Config()
	if err != nil {
		return nil, err
	}
	if req.Time == 0 {
		req.Time = c.Int("LAN_TEST_DURATION")
	}
	if req.Parallel == 0 {
		req.Parallel = c.Int("LAN_PARALLEL_STREAMS")
	}
	sys := b.a.sys()
	eng := &lan.Engine{Runner: sys.run, Net: sys.net, Sys: sysAdapter{sys.sysfs},
		Bin: filepath.Join(b.a.binDir, "iperf3"), Now: sys.now, CPUs: runtime.NumCPU(), Inbound: b.a.inbound}
	rec := eng.Run(ctx, lan.Params{Host: req.Target, Port: c.Int("IPERF_PORT"), Duration: req.Time, Parallel: req.Parallel,
		Iface: req.Iface, UDP: req.UDP, Bitrate: req.Bitrate, Bidir: req.Bidir}, triggerWeb)
	b.a.store(b.a.layout.LanHistory(), c, rec)
	return rec, nil
}

func (b *backend) AppendLAN(rec any) error {
	c, err := b.Config()
	if err != nil {
		return err
	}
	s := history.Store{Path: b.a.layout.LanHistory()}
	if err := s.Append(rec); err != nil {
		return err
	}
	return s.Rotate(c.Int("HISTORY_MAX_LINES"))
}
