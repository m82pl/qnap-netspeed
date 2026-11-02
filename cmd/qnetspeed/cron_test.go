package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/lock"
	"github.com/m82pl/qnap-netspeed/internal/notify"
	"github.com/m82pl/qnap-netspeed/internal/runner"
)

func iperfOut(t *testing.T, name string) runner.Result {
	t.Helper()
	b, err := os.ReadFile("../../testdata/iperf3/3.22/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return runner.Result{Stdout: b}
}

// cronRig is an app with fake librespeed-cli, iperf3 and log_tool and a clock the test moves.
type cronRig struct {
	t   *testing.T
	a   *app
	fr  *fakeRun
	now time.Time
	out *strings.Builder
}

func newCronRig(t *testing.T, conf string) *cronRig {
	fr := &fakeRun{byTool: map[string][]runner.Result{}}
	a, out, _ := measureApp(t, fr)
	r := &cronRig{t: t, a: a, fr: fr, now: fixedNow, out: out}
	a.system.now = func() time.Time { return r.now }
	writeFile(t, a.layout.ConfigFile(), conf)
	return r
}

// run does one cron run with fresh tool output and returns the exit code and the log_tool calls.
func (r *cronRig) run(ls runner.Result) (int, []string) {
	r.fr.calls = nil
	r.fr.byTool = map[string][]runner.Result{
		"librespeed-cli": {ls},
		"iperf3":         {iperfOut(r.t, "synthetic-tcp-upload.json"), iperfOut(r.t, "synthetic-tcp-download.json")},
		"log_tool":       {{}},
	}
	code := r.a.run([]string{"cron"})
	var logs []string
	for _, c := range r.fr.calls {
		if strings.HasPrefix(c, "log_tool ") {
			logs = append(logs, strings.TrimPrefix(c, "log_tool "))
		}
	}
	return code, logs
}

func TestCronFullRunDedupAndRecovery(t *testing.T) {
	r := newCronRig(t, "WAN_IFACES=eth0\nLAN_SCHEDULE_TARGETS=192.168.1.50\nLINK_EXPECT=eth0:2500\nMIN_LAN_MBPS=5000\n")
	code, logs := r.run(lsOK(t))
	if code != exitOK {
		t.Fatalf("code %d\n%s", code, r.out)
	}
	for _, p := range []string{r.a.layout.WanHistory(), r.a.layout.LanHistory(), r.a.layout.LinkHistory()} {
		recs := readLines(t, p)
		if len(recs) != 1 {
			t.Fatalf("%s: %v", filepath.Base(p), recs)
		}
		if tr, ok := recs[0]["trigger"]; ok && tr != "cron" {
			t.Errorf("%s: trigger %v", filepath.Base(p), tr)
		}
	}
	if len(logs) != 2 || !strings.HasPrefix(logs[0], "-t 1 -a [qnap-netspeed] LAN 192.168.1.50: upload ") ||
		!strings.HasSuffix(logs[0], " Mbps < 5000 Mbps") ||
		logs[1] != "-t 1 -a [qnap-netspeed] Link eth0: 1000 Mb/s, expected 2500 Mb/s" {
		t.Fatalf("log_tool calls: %q", logs)
	}
	for _, s := range []string{"cron: link: ", "cron: wan eth0: ok, down 850.4 / up 295.1 Mb/s", "cron: lan 192.168.1.50: ok", "cron: notify (new, level 1)", "cron: done in"} {
		if !strings.Contains(r.out.String(), s) {
			t.Errorf("stdout lacks %q:\n%s", s, r.out)
		}
	}
	if log, _ := os.ReadFile(r.a.layout.LogFile()); !strings.Contains(string(log), "cron: wan eth0: ok") {
		t.Errorf("qnetspeed.log: %s", log)
	}
	st, err := notify.LoadState(r.a.layout.StateFile())
	if err != nil || len(st.Notify) != 2 {
		t.Fatalf("state %+v %v", st, err)
	}

	// A10: the next runs (6 h, 12 h later) write nothing while the problem lasts.
	for _, h := range []int{6, 12} {
		r.now = fixedNow.Add(time.Duration(h) * time.Hour)
		if code, logs = r.run(lsOK(t)); code != exitOK || len(logs) != 0 {
			t.Fatalf("+%d h: code %d, repeated: %v", h, code, logs)
		}
	}
	// After NOTIFY_REPEAT_HOURS (24 h) it is repeated once.
	r.now = fixedNow.Add(24 * time.Hour)
	if _, logs = r.run(lsOK(t)); len(logs) != 2 {
		t.Fatalf("+24 h: %v", logs)
	}
	// Link back at its expected speed and LAN threshold off: one info entry each.
	r.now = fixedNow.Add(30 * time.Hour)
	writeFile(t, r.a.layout.ConfigFile(), "WAN_IFACES=eth0\nLAN_SCHEDULE_TARGETS=192.168.1.50\nLINK_EXPECT=eth0:1000\nMIN_LAN_MBPS=0\n")
	code, logs = r.run(lsOK(t))
	if code != exitOK || len(logs) != 2 {
		t.Fatalf("recovery: code %d %v", code, logs)
	}
	for _, l := range logs {
		if !strings.HasPrefix(l, "-t 0 -a [qnap-netspeed] ") || !strings.Contains(l, "back to normal") {
			t.Errorf("recovery entry %q", l)
		}
	}
	if st, _ = notify.LoadState(r.a.layout.StateFile()); len(st.Notify) != 0 {
		t.Errorf("state not cleared: %+v", st.Notify)
	}
}

func TestCronWANThresholdsAndFailureStreak(t *testing.T) {
	r := newCronRig(t, "WAN_IFACES=eth0\nMIN_WAN_DL_MBPS=900\nMIN_WAN_UL_MBPS=0\nMAX_WAN_PING_MS=5\n")
	_, logs := r.run(lsOK(t)) // 850.4 down < 900, ping 8.2 > 5
	if len(logs) != 2 || !strings.Contains(logs[0], "WAN eth0: download 850.4 Mbps < 900 Mbps (server: Example, PL (Synthetic))") ||
		!strings.Contains(logs[1], "WAN eth0: ping 8.2 ms > 5 ms") {
		t.Fatalf("%v", logs)
	}

	r = newCronRig(t, "WAN_IFACES=eth0\nWAN_FAIL_THRESHOLD=2\n")
	null := runner.Result{Stdout: []byte("null\n")}
	if code, logs := r.run(null); code != exitTestError || len(logs) != 0 {
		t.Fatalf("1st failure: code %d, %v", code, logs)
	}
	r.now = r.now.Add(time.Hour)
	code, logs := r.run(null)
	if code != exitTestError || len(logs) != 1 || logs[0] != "-t 2 -a [qnap-netspeed] WAN eth0: test failed 2x in a row: the speed test server did not respond" {
		t.Fatalf("2nd failure: code %d, %v", code, logs)
	}
	r.now = r.now.Add(time.Hour)
	if _, logs = r.run(null); len(logs) != 0 {
		t.Fatalf("3rd failure repeated: %v", logs)
	}
	r.now = r.now.Add(time.Hour)
	code, logs = r.run(lsOK(t))
	if code != exitOK || len(logs) != 1 || !strings.HasPrefix(logs[0], "-t 0 -a [qnap-netspeed] WAN eth0: test works again") {
		t.Fatalf("recovery: code %d, %v", code, logs)
	}
}

func TestCronSwitches(t *testing.T) {
	r := newCronRig(t, "WAN_SCHEDULE_ENABLE=0\nNOTIFY_ENABLE=0\nLINK_EXPECT=eth0:2500\n")
	code, logs := r.run(lsOK(t))
	if code != exitOK || len(logs) != 0 {
		t.Fatalf("code %d %v", code, logs)
	}
	for _, c := range r.fr.calls {
		t.Errorf("no tool may run: %s", c)
	}
	if _, err := os.Stat(r.a.layout.StateFile()); err == nil {
		t.Error("state.json written with notifications off")
	}
	if _, err := os.Stat(r.a.layout.WanHistory()); err == nil {
		t.Error("wan.jsonl written with WAN_SCHEDULE_ENABLE=0")
	}
	if len(readLines(t, r.a.layout.LinkHistory())) != 1 {
		t.Error("link state is still recorded")
	}
}

func TestCronLogToolFailureIsRetried(t *testing.T) {
	r := newCronRig(t, "WAN_SCHEDULE_ENABLE=0\nLINK_EXPECT=eth0:2500\n")
	r.fr.byTool = map[string][]runner.Result{"log_tool": {{ExitCode: 1}}}
	code := r.a.run([]string{"cron"})
	if code != exitTestError || !strings.Contains(r.out.String(), "cannot write System Logs entry") {
		t.Fatalf("code %d\n%s", code, r.out)
	}
	if st, _ := notify.LoadState(r.a.layout.StateFile()); len(st.Notify) != 0 {
		t.Errorf("a refused entry must not count as written: %+v", st.Notify)
	}
	if code, logs := r.run(lsOK(t)); code != exitOK || len(logs) != 1 {
		t.Errorf("retry: code %d %v", code, logs)
	}
}

// A14: cron started during a running test waits for it and then runs; after
// the wait it gives up with 75 and does not test at the same time.
func TestCronWaitsForRunningTest(t *testing.T) {
	r := newCronRig(t, "WAN_SCHEDULE_ENABLE=0\n")
	l, err := lock.Acquire(r.a.layout.LockFile(), "wan", 0)
	if err != nil {
		t.Fatal(err)
	}
	r.a.cronWait = 5 * time.Second
	go func(held *lock.Lock) {
		time.Sleep(300 * time.Millisecond)
		held.Release()
	}(l)
	if code, _ := r.run(lsOK(t)); code != exitOK {
		t.Fatalf("code %d\n%s", code, r.out)
	}
	if len(readLines(t, r.a.layout.LinkHistory())) != 1 {
		t.Error("cron did not run after the wait")
	}

	l, err = lock.Acquire(r.a.layout.LockFile(), "lan-test", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	r.a.cronWait = 100 * time.Millisecond
	if code, _ := r.run(lsOK(t)); code != exitBusy {
		t.Fatalf("code %d, want %d", code, exitBusy)
	}
	if len(readLines(t, r.a.layout.LinkHistory())) != 1 {
		t.Error("cron ran although another test held the lock")
	}
}

func TestNotifyCommands(t *testing.T) {
	r := newCronRig(t, "")
	r.fr.byTool = map[string][]runner.Result{"log_tool": {{}}}
	if code := r.a.run([]string{"notify", "test", "--level", "1"}); code != exitOK {
		t.Fatalf("code %d", code)
	}
	if len(r.fr.calls) != 1 || r.fr.calls[0] != "log_tool -t 1 -a [qnap-netspeed] test message from qnetspeed notify test" {
		t.Errorf("%v", r.fr.calls)
	}
	for _, args := range [][]string{{"notify"}, {"notify", "x"}, {"notify", "test", "--level", "3"}, {"notify", "status", "x"}} {
		if code := r.a.run(args); code != exitUsage {
			t.Errorf("%v: code %d", args, code)
		}
	}
	r.out.Reset()
	if code := r.a.run([]string{"notify", "status"}); code != exitOK || !strings.Contains(r.out.String(), "No open problems.") {
		t.Errorf("empty status: %q", r.out)
	}
	writeFile(t, r.a.layout.ConfigFile(), "WAN_SCHEDULE_ENABLE=0\nLINK_EXPECT=eth0:2500\n")
	r.run(lsOK(t))
	r.out.Reset()
	if code := r.a.run([]string{"notify", "status"}); code != exitOK || !strings.Contains(r.out.String(), "link_degraded:eth0") ||
		!strings.Contains(r.out.String(), "Link eth0: 1000 Mb/s, expected 2500 Mb/s") {
		t.Errorf("status: %q", r.out)
	}
	r.out.Reset()
	if code := r.a.run([]string{"notify", "status", "--json"}); code != exitOK || !strings.Contains(r.out.String(), `"link_degraded:eth0"`) {
		t.Errorf("json status: %q", r.out)
	}
}
