// Package lock provides the single "active test" lock (spec pkt 3.2):
// flock(2) on data/.test.lock, with a description of the holder in the file.
package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// Holder describes who holds the lock.
type Holder struct {
	Type  string    `json:"type"`
	Since time.Time `json:"since"`
	PID   int       `json:"pid"`
}

// BusyError is returned when the lock is still held after the wait time.
type BusyError struct{ Holder Holder }

func (e *BusyError) Error() string {
	if e.Holder.Type == "" {
		return "another test is running"
	}
	return fmt.Sprintf("another test is running: %s since %s", e.Holder.Type, e.Holder.Since.Format(time.RFC3339))
}

// ErrBusy matches any *BusyError with errors.Is.
var ErrBusy = errors.New("lock busy")

func (e *BusyError) Is(target error) bool { return target == ErrBusy }

// Lock is a held lock.
type Lock struct{ f *os.File }

// pollInterval is how often Acquire retries while waiting.
var pollInterval = 200 * time.Millisecond

// Acquire takes the lock at path for a test of type typ, waiting up to wait.
// wait = 0 means a single attempt.
func Acquire(path, typ string, wait time.Duration) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, fmt.Errorf("flock %s: %w", path, err)
		}
		if !time.Now().Before(deadline) {
			h, _ := readHolder(f)
			f.Close()
			return nil, &BusyError{Holder: h}
		}
		time.Sleep(min(pollInterval, time.Until(deadline)+time.Millisecond))
	}
	h := Holder{Type: typ, Since: time.Now().UTC().Truncate(time.Second), PID: os.Getpid()}
	data, _ := json.Marshal(h)
	if err := f.Truncate(0); err == nil {
		f.WriteAt(append(data, '\n'), 0)
	}
	return &Lock{f: f}, nil
}

// Release clears the holder description and releases the lock.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	l.f.Truncate(0)
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}

// Current returns the holder if the lock at path is held, or nil if it is free.
func Current(path string) (*Holder, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err == nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return nil, nil
	}
	h, _ := readHolder(f)
	return &h, nil
}

func readHolder(f *os.File) (Holder, error) {
	var h Holder
	buf := make([]byte, 512)
	n, _ := f.ReadAt(buf, 0)
	if n == 0 {
		return h, errors.New("empty lock file")
	}
	err := json.Unmarshal(buf[:n], &h)
	return h, err
}
