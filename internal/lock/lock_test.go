package lock

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireBusyRelease(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".test.lock")
	l, err := Acquire(p, "wan", 0)
	if err != nil {
		t.Fatal(err)
	}
	// flock is per open file description, so a second Acquire in the same
	// process conflicts like another process would.
	_, err = Acquire(p, "lan", 0)
	var busy *BusyError
	if !errors.As(err, &busy) || !errors.Is(err, ErrBusy) {
		t.Fatalf("second Acquire err = %v, want BusyError", err)
	}
	if busy.Holder.Type != "wan" || busy.Holder.PID == 0 || busy.Holder.Since.IsZero() {
		t.Errorf("holder = %+v", busy.Holder)
	}
	h, err := Current(p)
	if err != nil || h == nil || h.Type != "wan" {
		t.Fatalf("Current = %+v, %v", h, err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if h, err := Current(p); h != nil || err != nil {
		t.Fatalf("Current after release = %+v, %v", h, err)
	}
	l2, err := Acquire(p, "lan", 0)
	if err != nil {
		t.Fatal(err)
	}
	l2.Release()
	if err := l2.Release(); err != nil {
		t.Errorf("double release: %v", err)
	}
}

func TestAcquireWaits(t *testing.T) {
	pollInterval = 10 * time.Millisecond
	p := filepath.Join(t.TempDir(), ".test.lock")
	l, err := Acquire(p, "cron", 0)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		l.Release()
	}()
	start := time.Now()
	l2, err := Acquire(p, "wan", 2*time.Second)
	if err != nil {
		t.Fatalf("Acquire with wait: %v", err)
	}
	defer l2.Release()
	if d := time.Since(start); d < 40*time.Millisecond {
		t.Errorf("returned after %v, before release", d)
	}
}

func TestAcquireTimesOut(t *testing.T) {
	pollInterval = 10 * time.Millisecond
	p := filepath.Join(t.TempDir(), ".test.lock")
	l, err := Acquire(p, "cron", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	start := time.Now()
	_, err = Acquire(p, "wan", 60*time.Millisecond)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d < 50*time.Millisecond || d > time.Second {
		t.Errorf("waited %v", d)
	}
}

func TestCurrentMissingFile(t *testing.T) {
	h, err := Current(filepath.Join(t.TempDir(), "none"))
	if h != nil || err != nil {
		t.Fatalf("Current = %+v, %v", h, err)
	}
}

func TestAcquireBadDir(t *testing.T) {
	if _, err := Acquire("/nonexistent-dir/x.lock", "wan", 0); err == nil {
		t.Fatal("no error")
	}
}
