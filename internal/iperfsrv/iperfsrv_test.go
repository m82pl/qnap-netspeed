package iperfsrv

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type logbuf struct {
	mu    sync.Mutex
	lines []string
}

func (l *logbuf) logf(f string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSpace(fmt.Sprintf(f, a...)))
}
func (l *logbuf) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "iperf3")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestRunsWithExpectedArgsAndLogsOutput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "args")
	bin := script(t, `echo "$@" >"`+out+`"; echo "Server listening on 5288"; exec sleep 30`)
	lb := &logbuf{}
	s := New(Options{Bin: bin, Port: freePort(t), Bind: "127.0.0.1", Logf: lb.logf})
	s.Start(context.Background())
	defer s.Stop()
	waitFor(t, "running", func() bool {
		return s.Status().State == StateRunning && strings.Contains(lb.joined(), "Server listening")
	})
	waitFor(t, "args file", func() bool { _, err := os.Stat(out); return err == nil })
	b, _ := os.ReadFile(out)
	if got := strings.TrimSpace(string(b)); !strings.HasPrefix(got, "-s -p ") || !strings.HasSuffix(got, "--forceflush -B 127.0.0.1") {
		t.Errorf("args = %q", got)
	}
	st := s.Status()
	if st.PID == 0 || st.Since == nil || !s.Enabled() {
		t.Errorf("status = %+v", st)
	}
	if !strings.Contains(lb.joined(), "iperf3: Server listening on 5288") {
		t.Errorf("iperf3 output not logged:\n%s", lb.joined())
	}
}

func TestStopKillsTheChild(t *testing.T) {
	bin := script(t, `exec sleep 60`)
	s := New(Options{Bin: bin, Port: freePort(t)})
	s.Start(context.Background())
	waitFor(t, "running", func() bool { return s.Status().State == StateRunning })
	pid := s.Status().PID
	s.Stop()
	if alive(pid) {
		// the zombie is reaped by Wait; a live pid here means the child survived
		time.Sleep(50 * time.Millisecond)
		if alive(pid) {
			t.Fatalf("child %d still alive after Stop", pid)
		}
	}
	if st := s.Status(); st.State != StateStopped || s.Enabled() {
		t.Errorf("status after Stop = %+v", st)
	}
	s.Stop() // idempotent
}

func TestRestartsWithGrowingDelay(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	bin := script(t, `echo x >>"`+count+`"; echo "boom"; exit 3`)
	lb := &logbuf{}
	s := New(Options{Bin: bin, Port: freePort(t), Logf: lb.logf,
		MinBackoff: 20 * time.Millisecond, MaxBackoff: 80 * time.Millisecond})
	s.Start(context.Background())
	defer s.Stop()
	waitFor(t, "3 restarts", func() bool { return s.Status().Restarts >= 3 })
	st := s.Status()
	if st.LastError == "" || !strings.Contains(st.LastError, "exit status 3") {
		t.Errorf("last error = %q", st.LastError)
	}
	for _, want := range []string{"iperf3: boom", "next try in 20ms", "next try in 40ms", "next try in 80ms"} {
		if !strings.Contains(lb.joined(), want) {
			t.Errorf("log lacks %q:\n%s", want, lb.joined())
		}
	}
	if strings.Contains(lb.joined(), "next try in 160ms") {
		t.Errorf("delay grew past MaxBackoff:\n%s", lb.joined())
	}
}

func TestStableRunResetsTheDelay(t *testing.T) {
	// Runs 1 and 2 fail at once (delays 20 ms, 40 ms); run 3 lives longer than
	// StableAfter, so the delay after it is MinBackoff again; run 4 fails at once (40 ms).
	count := filepath.Join(t.TempDir(), "count")
	bin := script(t, `echo x >>"`+count+`"; n=$(wc -l <"`+count+`"); if [ "$n" = 3 ]; then sleep 0.15; fi; exit 1`)
	lb := &logbuf{}
	s := New(Options{Bin: bin, Port: freePort(t), Logf: lb.logf,
		MinBackoff: 20 * time.Millisecond, MaxBackoff: time.Second, StableAfter: 100 * time.Millisecond})
	s.Start(context.Background())
	defer s.Stop()
	waitFor(t, "4 failures", func() bool { return strings.Count(lb.joined(), "next try in") >= 4 })
	var delays []string
	for _, l := range strings.Split(lb.joined(), "\n") {
		if _, d, ok := strings.Cut(l, "next try in "); ok {
			delays = append(delays, d)
		}
	}
	if got := strings.Join(delays[:4], " "); got != "20ms 40ms 20ms 40ms" {
		t.Errorf("delays = %s", got)
	}
}

func TestBusyPortGivesClearError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	started := filepath.Join(t.TempDir(), "started")
	bin := script(t, `: >"`+started+`"; exec sleep 30`)
	lb := &logbuf{}
	s := New(Options{Bin: bin, Port: port, Bind: "127.0.0.1", Logf: lb.logf, MinBackoff: 20 * time.Millisecond})
	s.Start(context.Background())
	defer s.Stop()
	waitFor(t, "port_busy", func() bool { return s.Status().State == StatePortBusy })
	st := s.Status()
	if !strings.Contains(st.LastError, "already in use by another program") || !strings.Contains(st.LastError, "IPERF_PORT") {
		t.Errorf("last error = %q", st.LastError)
	}
	if _, err := os.Stat(started); err == nil {
		t.Error("iperf3 was started although the port is busy")
	}
	// The port frees up: the server comes up by itself.
	ln.Close()
	waitFor(t, "running after the port is free", func() bool { return s.Status().State == StateRunning })
}

func TestMissingBinaryIsReported(t *testing.T) {
	s := New(Options{Bin: filepath.Join(t.TempDir(), "nope"), Port: freePort(t), MinBackoff: 10 * time.Millisecond})
	s.Start(context.Background())
	defer s.Stop()
	waitFor(t, "backoff", func() bool { return s.Status().State == StateBackoff })
	if !strings.Contains(s.Status().LastError, "cannot start") {
		t.Errorf("last error = %q", s.Status().LastError)
	}
}

func TestContextCancelStopsTheChild(t *testing.T) {
	bin := script(t, `exec sleep 60`)
	ctx, cancel := context.WithCancel(context.Background())
	s := New(Options{Bin: bin, Port: freePort(t)})
	s.Start(ctx)
	waitFor(t, "running", func() bool { return s.Status().State == StateRunning })
	pid := s.Status().PID
	cancel()
	waitFor(t, "child gone", func() bool { return !alive(pid) })
	s.Stop()
}

func TestStartAfterStop(t *testing.T) {
	bin := script(t, `exec sleep 60`)
	s := New(Options{Bin: bin, Port: freePort(t)})
	s.Start(context.Background())
	waitFor(t, "running", func() bool { return s.Status().State == StateRunning })
	first := s.Status().PID
	s.Stop()
	s.Start(context.Background())
	defer s.Stop()
	waitFor(t, "running again", func() bool { return s.Status().State == StateRunning && s.Status().PID != first })
}
