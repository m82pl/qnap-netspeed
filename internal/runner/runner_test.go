package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecCapturesOutputAndExitCode(t *testing.T) {
	res, err := Exec{}.Run(context.Background(), 5*time.Second, "sh", "-c", "echo out; echo err >&2; exit 3")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(res.Stdout)) != "out" || strings.TrimSpace(string(res.Stderr)) != "err" || res.ExitCode != 3 {
		t.Errorf("%+v", res)
	}
}

func TestExecTimeoutKillsTheProgram(t *testing.T) {
	start := time.Now()
	res, err := Exec{}.Run(context.Background(), 200*time.Millisecond, "sh", "-c", "sleep 30")
	if !errors.Is(err, ErrTimeout) || res.ExitCode != -1 {
		t.Fatalf("err %v res %+v", err, res)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
}

func TestExecMissingProgram(t *testing.T) {
	res, err := Exec{}.Run(context.Background(), time.Second, "/nonexistent/prog")
	if err == nil || res.ExitCode != -1 {
		t.Fatalf("err %v res %+v", err, res)
	}
}

func TestExecCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err := Exec{}.Run(ctx, 30*time.Second, "sh", "-c", "sleep 30")
	if err == nil {
		t.Fatal("cancelled run must report an error")
	}
}
