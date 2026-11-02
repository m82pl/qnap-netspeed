// Package runner runs external programs behind an interface, so tests can
// replace librespeed-cli and iperf3 with recorded output (plan: fake exec).
package runner

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// Result is the outcome of one program run.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int // -1 when the program did not start or was killed by the time limit
}

// ErrTimeout is returned when the time limit ended the run.
var ErrTimeout = errors.New("time limit exceeded")

// Runner starts a program with a time limit.
type Runner interface {
	Run(ctx context.Context, timeout time.Duration, name string, args ...string) (Result, error)
}

// Exec is the real Runner: SIGTERM at the limit, SIGKILL 5 s later (spec pkt 5.1).
type Exec struct{}

// Run implements Runner. A non-zero exit code is not an error; a program that
// cannot start, or runs past timeout, is.
func (Exec) Run(ctx context.Context, timeout time.Duration, name string, args ...string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	// Own process group, so a limit or Ctrl+C reaches the whole tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		pgid := cmd.Process.Pid
		time.AfterFunc(5*time.Second, func() { syscall.Kill(-pgid, syscall.SIGKILL) })
		return syscall.Kill(-pgid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 6 * time.Second
	err := cmd.Run()
	res := Result{Stdout: out.Bytes(), Stderr: errb.Bytes()}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	} else {
		res.ExitCode = -1
	}
	if cerr := ctx.Err(); cerr != nil {
		res.ExitCode = -1
		if errors.Is(cerr, context.DeadlineExceeded) {
			return res, ErrTimeout
		}
		return res, cerr
	}
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		res.ExitCode = -1
		return res, err
	}
	return res, nil
}
