package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"no args", nil, exitUsage, "", "Usage:"},
		{"version", []string{"version"}, exitOK, "qnetspeed dev (linux/" + runtime.GOARCH, ""},
		{"version flag", []string{"--version"}, exitOK, "qnetspeed dev", ""},
		{"version extra arg", []string{"version", "x"}, exitUsage, "", "unexpected argument"},
		{"help", []string{"help"}, exitOK, "Commands:", ""},
		{"unknown", []string{"bogus"}, exitUsage, "", `unknown command "bogus"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if runtime.GOOS != "linux" && strings.Contains(tt.wantStdout, "linux/") {
				t.Skip("binary targets linux")
			}
			var out, errOut bytes.Buffer
			code := run(tt.args, &out, &errOut)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if !strings.Contains(out.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want substring %q", out.String(), tt.wantStdout)
			}
			if !strings.Contains(errOut.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want substring %q", errOut.String(), tt.wantStderr)
			}
			if tt.wantStdout == "" && out.Len() != 0 {
				t.Errorf("unexpected stdout %q", out.String())
			}
		})
	}
}

func TestExitCodesMatchSpec(t *testing.T) {
	got := []int{exitOK, exitTestError, exitUsage, exitConfig, exitBusy, exitTimeout}
	want := []int{0, 1, 2, 3, 75, 124}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("exit code #%d = %d, want %d", i, got[i], want[i])
		}
	}
}
