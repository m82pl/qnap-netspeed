// Package iperfsrv supervises `iperf3 -s` as a child of the daemon (spec pkt
// 3.1 and 5.2.1): started without -D, restarted with a growing delay when it
// dies, its output goes to the daemon log, stopped with the daemon.
package iperfsrv

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// States reported by Status.
const (
	StateStopped  = "stopped"   // switched off (LAN_SERVER_ENABLE=0 or lan-server stop)
	StateStarting = "starting"  // just switched on, the process is about to be started
	StateRunning  = "running"   // process alive
	StateBackoff  = "restart"   // died, waiting before the next start
	StatePortBusy = "port_busy" // another program holds the port; retried with backoff
)

// Options configure a Supervisor.
type Options struct {
	Bin  string // path to the iperf3 binary
	Port int
	Bind string // "" = all addresses
	// Logf receives supervisor messages and every line iperf3 prints.
	Logf func(format string, args ...any)

	MinBackoff  time.Duration // delay after the first failure (default 1 s)
	MaxBackoff  time.Duration // upper limit of the delay (default 60 s)
	StableAfter time.Duration // a run this long resets the delay (default 60 s)
	// PortFree reports whether the port can be bound (default: try to listen).
	PortFree func(bind string, port int) error
	// StopTimeout is how long iperf3 gets after SIGTERM before SIGKILL (default 5 s).
	StopTimeout time.Duration
}

// Status is what GET /api/v1/status reports about the iperf3 server.
type Status struct {
	State     string     `json:"state"`
	Port      int        `json:"port"`
	Bind      string     `json:"bind"`
	PID       int        `json:"pid,omitempty"`
	Since     *time.Time `json:"since,omitempty"`
	Restarts  int        `json:"restarts"`
	LastError string     `json:"last_error,omitempty"`
}

// Supervisor owns the iperf3 server process. All methods are safe for concurrent use.
type Supervisor struct {
	o Options

	mu      sync.Mutex
	st      Status
	cancel  context.CancelFunc // stops the current supervise loop
	done    chan struct{}      // closed when the loop has ended
	started bool
}

// New returns a stopped Supervisor.
func New(o Options) *Supervisor {
	if o.MinBackoff <= 0 {
		o.MinBackoff = time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 60 * time.Second
	}
	if o.StableAfter <= 0 {
		o.StableAfter = 60 * time.Second
	}
	if o.StopTimeout <= 0 {
		o.StopTimeout = 5 * time.Second
	}
	if o.PortFree == nil {
		o.PortFree = portFree
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	s := &Supervisor{o: o}
	s.st = Status{State: StateStopped, Port: o.Port, Bind: o.Bind}
	return s
}

// Status returns a snapshot.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

func (s *Supervisor) set(f func(*Status)) {
	s.mu.Lock()
	f(&s.st)
	s.mu.Unlock()
}

// Start begins supervising (a no-op when already started). The process lives
// until Stop, or until ctx is cancelled.
func (s *Supervisor) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	ctx, s.cancel = context.WithCancel(ctx)
	s.done = make(chan struct{})
	s.st = Status{State: StateStarting, Port: s.o.Port, Bind: s.o.Bind}
	done := s.done
	s.mu.Unlock()
	go func() {
		defer close(done)
		s.loop(ctx)
	}()
}

// Stop ends the server and waits until the process is gone (a no-op when stopped).
func (s *Supervisor) Stop() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	cancel()
	<-done
	s.mu.Lock()
	s.started = false
	s.st = Status{State: StateStopped, Port: s.o.Port, Bind: s.o.Bind, Restarts: s.st.Restarts, LastError: s.st.LastError}
	s.mu.Unlock()
}

// Enabled reports whether the supervisor has been started (and not stopped).
func (s *Supervisor) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

func (s *Supervisor) loop(ctx context.Context) {
	// The child dies with its parent thread (Pdeathsig, see procattr_linux.go),
	// so this goroutine keeps its thread for as long as the child may live.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	delay := s.o.MinBackoff
	for ctx.Err() == nil {
		began := time.Now()
		ran, err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if ran && time.Since(began) >= s.o.StableAfter {
			delay = s.o.MinBackoff
		}
		msg := err.Error()
		state := StateBackoff
		var busy *PortBusyError
		if errors.As(err, &busy) {
			state = StatePortBusy
		}
		s.set(func(st *Status) {
			st.State, st.PID, st.Since = state, 0, nil
			st.LastError = msg
			if ran {
				st.Restarts++
			}
		})
		s.o.Logf("iperf3 server: %s; next try in %s", msg, delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, s.o.MaxBackoff)
	}
}

// PortBusyError means that something else already listens on the port.
type PortBusyError struct {
	Bind string
	Port int
	Err  error
}

func (e *PortBusyError) Error() string {
	where := strconv.Itoa(e.Port)
	if e.Bind != "" {
		where = net.JoinHostPort(e.Bind, where)
	}
	return fmt.Sprintf("port %s is already in use by another program (IPERF_PORT)", where)
}
func (e *PortBusyError) Unwrap() error { return e.Err }

func portFree(bind string, port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(port)))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return &PortBusyError{Bind: bind, Port: port, Err: err}
		}
		return err
	}
	return ln.Close()
}

// runOnce starts the process and waits for it. ran reports whether a process
// was started at all. The returned error says why it ended (never nil unless
// ctx ended it).
func (s *Supervisor) runOnce(ctx context.Context) (ran bool, err error) {
	if err := s.o.PortFree(s.o.Bind, s.o.Port); err != nil {
		var busy *PortBusyError
		if errors.As(err, &busy) {
			return false, err
		}
		return false, fmt.Errorf("cannot use port %d: %w", s.o.Port, err)
	}
	args := []string{"-s", "-p", strconv.Itoa(s.o.Port), "--forceflush"}
	if s.o.Bind != "" {
		args = append(args, "-B", s.o.Bind)
	}
	cmd := exec.Command(s.o.Bin, args...)
	cmd.SysProcAttr = sysProcAttr()
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		pw.Close()
		return false, fmt.Errorf("cannot start %s: %w", s.o.Bin, err)
	}
	pid := cmd.Process.Pid
	now := time.Now().UTC().Truncate(time.Second)
	s.set(func(st *Status) { st.State, st.PID, st.Since = StateRunning, pid, &now })
	s.o.Logf("iperf3 server: started (pid %d, port %d)", pid, s.o.Port)

	logDone := make(chan struct{})
	go func() {
		defer close(logDone)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 64<<10)
		for sc.Scan() {
			s.o.Logf("iperf3: %s", sc.Text())
		}
		io.Copy(io.Discard, pr)
	}()
	waitc := make(chan error, 1)
	go func() { waitc <- cmd.Wait(); pw.Close() }()

	select {
	case werr := <-waitc:
		<-logDone
		return true, exitReason(werr)
	case <-ctx.Done():
		s.terminate(cmd, waitc)
		<-logDone
		return true, ctx.Err()
	}
}

// terminate sends SIGTERM to the process group, SIGKILL after StopTimeout.
func (s *Supervisor) terminate(cmd *exec.Cmd, waitc <-chan error) {
	pgid := cmd.Process.Pid
	syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-waitc:
	case <-time.After(s.o.StopTimeout):
		syscall.Kill(-pgid, syscall.SIGKILL)
		<-waitc
	}
	s.o.Logf("iperf3 server: stopped")
}

func exitReason(err error) error {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return errors.New("iperf3 exited")
	case errors.As(err, &ee):
		return fmt.Errorf("iperf3 exited: %s", ee.ProcessState)
	}
	return fmt.Errorf("iperf3: %w", err)
}
