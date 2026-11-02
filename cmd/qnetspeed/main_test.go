package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/lock"
	"github.com/m82pl/qnap-netspeed/internal/paths"
	"github.com/m82pl/qnap-netspeed/internal/web"
)

// testApp returns an app rooted in a temporary $QPKG_ROOT with etc/ and data/.
func testApp(t *testing.T) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"etc", "data", "bin"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	return &app{
		layout:   paths.Layout{Root: root},
		binDir:   filepath.Join(root, "bin"),
		stdout:   &out,
		stderr:   &errOut,
		cronWait: 0,
	}, &out, &errOut
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

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
		{"config no sub", []string{"config"}, exitUsage, "", "Usage: qnetspeed config"},
		{"config bad sub", []string{"config", "frob"}, exitUsage, "", "Usage: qnetspeed config"},
		{"config get no key", []string{"config", "get"}, exitUsage, "", "Usage: qnetspeed config"},
		{"config get unknown", []string{"config", "get", "NOPE"}, exitUsage, "", "unknown key NOPE"},
		{"config set unknown", []string{"config", "set", "NOPE", "1"}, exitUsage, "", "unknown key NOPE"},
		{"daemon extra arg", []string{"daemon", "x"}, exitUsage, "", "unexpected argument"},
		{"cron extra arg", []string{"cron", "x"}, exitUsage, "", "unexpected argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if runtime.GOOS != "linux" && strings.Contains(tt.wantStdout, "linux/") {
				t.Skip("binary targets linux")
			}
			a, out, errOut := testApp(t)
			code := a.run(tt.args)
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

func TestVersionTools(t *testing.T) {
	a, out, _ := testApp(t)
	writeFile(t, filepath.Join(a.binDir, "iperf3"), "#!/bin/sh\necho\necho 'iperf 3.22 (cJSON 1.7.15)'\necho 'Linux nas'\n")
	os.Chmod(filepath.Join(a.binDir, "iperf3"), 0o755)
	if code := a.run([]string{"version"}); code != exitOK {
		t.Fatalf("code %d", code)
	}
	s := out.String()
	if !strings.Contains(s, "iperf3: iperf 3.22 (cJSON 1.7.15)\n") {
		t.Errorf("iperf3 line missing: %q", s)
	}
	if !strings.Contains(s, "librespeed-cli: not available") {
		t.Errorf("librespeed-cli line missing: %q", s)
	}
}

func TestConfigCheck(t *testing.T) {
	a, out, errOut := testApp(t)
	if code := a.run([]string{"config", "check"}); code != exitOK {
		t.Fatalf("missing file: code %d, stderr %q", code, errOut)
	}
	if !strings.Contains(errOut.String(), "not found, using defaults") || !strings.Contains(out.String(), "config OK") {
		t.Errorf("missing file: stdout %q stderr %q", out, errOut)
	}

	a, out, errOut = testApp(t)
	writeFile(t, a.layout.ConfigFile(), "WEB_PORT=8888\nTYPO=1\n")
	if code := a.run([]string{"config", "check"}); code != exitOK {
		t.Fatalf("warning only: code %d", code)
	}
	if !strings.Contains(errOut.String(), "warning: line 2: TYPO: unknown key") {
		t.Errorf("stderr %q", errOut)
	}

	a, out, errOut = testApp(t)
	writeFile(t, a.layout.ConfigFile(), "WEB_PORT=80\n")
	if code := a.run([]string{"config", "check"}); code != exitConfig {
		t.Fatalf("bad port: code %d", code)
	}
	if !strings.Contains(errOut.String(), "error: line 1: WEB_PORT") || strings.Contains(out.String(), "OK") {
		t.Errorf("stdout %q stderr %q", out, errOut)
	}
}

func TestConfigShowGet(t *testing.T) {
	a, out, _ := testApp(t)
	writeFile(t, a.layout.ConfigFile(), "WAN_IFACES=\"eth0 eth1\"\n")
	if code := a.run([]string{"config", "get", "WAN_IFACES"}); code != exitOK || out.String() != "eth0 eth1\n" {
		t.Fatalf("get: code %d out %q", code, out)
	}
	out.Reset()
	if code := a.run([]string{"config", "get", "CRON_SCHEDULE"}); code != exitOK || out.String() != "17 */6 * * *\n" {
		t.Fatalf("get default: code %d out %q", code, out)
	}
	out.Reset()
	if code := a.run([]string{"config", "show"}); code != exitOK || !strings.Contains(out.String(), "WAN_IFACES=\"eth0 eth1\"\n") {
		t.Fatalf("show: code %d out %q", code, out)
	}

	a, out, _ = testApp(t)
	writeFile(t, a.layout.ConfigFile(), "WEB_PORT=1\n")
	if code := a.run([]string{"config", "get", "WEB_PORT"}); code != exitConfig || out.Len() != 0 {
		t.Fatalf("get with invalid config: code %d out %q", code, out)
	}
}

func TestConfigSet(t *testing.T) {
	a, out, errOut := testApp(t)
	writeFile(t, a.layout.DefaultConfig(), "# default\nWEB_PORT=8888\n")
	if code := a.run([]string{"config", "set", "WEB_PORT", "9000"}); code != exitOK {
		t.Fatalf("set: code %d stderr %q", code, errOut)
	}
	if !strings.Contains(out.String(), "WEB_PORT=9000 saved") {
		t.Errorf("stdout %q", out)
	}
	got, _ := os.ReadFile(a.layout.ConfigFile())
	if string(got) != "# default\nWEB_PORT=9000\n" {
		t.Errorf("file = %q", got)
	}

	errOut.Reset()
	if code := a.run([]string{"config", "set", "IPERF_PORT", "9000"}); code != exitConfig {
		t.Fatalf("conflicting port: code %d", code)
	}
	if !strings.Contains(errOut.String(), "not saved") {
		t.Errorf("stderr %q", errOut)
	}
	if code := a.run([]string{"config", "set", "CRON_SCHEDULE", "* * * * * reboot"}); code != exitConfig {
		t.Fatalf("bad cron: code %d", code)
	}
	got, _ = os.ReadFile(a.layout.ConfigFile())
	if string(got) != "# default\nWEB_PORT=9000\n" {
		t.Errorf("file changed by rejected set: %q", got)
	}
}

func TestCronBusyAndExtraConfig(t *testing.T) {
	a, _, _ := testApp(t)
	a.system.euid = func() int { return 0 }
	l, err := lock.Acquire(a.layout.LockFile(), "wan", 0)
	if err != nil {
		t.Fatal(err)
	}
	if code := a.run([]string{"cron"}); code != exitBusy {
		t.Fatalf("cron while busy: code %d", code)
	}
	l.Release()
	log, _ := os.ReadFile(a.layout.LogFile())
	if !strings.Contains(string(log), "cron: skipped, another test is running: wan") {
		t.Errorf("log = %q", log)
	}
	writeFile(t, a.layout.ConfigFile(), "WEB_PORT=22\n")
	if code := a.run([]string{"cron"}); code != exitConfig {
		t.Fatalf("bad config: code %d", code)
	}
}

func TestDaemonInvalidConfig(t *testing.T) {
	a, _, errOut := testApp(t)
	writeFile(t, a.layout.ConfigFile(), "WEB_PORT=22\n")
	if code := a.run([]string{"daemon"}); code != exitConfig {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errOut.String(), "not starting") {
		t.Errorf("stderr %q", errOut)
	}
}

func TestServeShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, ln, web.Handler(web.Info{Version: "x", Started: time.Now()}))
	}()
	resp, err := http.Get("http://" + ln.Addr().String() + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"version":"x"`) {
		t.Fatalf("status %d body %s", resp.StatusCode, body)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}
