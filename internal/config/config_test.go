package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultFileMatchesKeys(t *testing.T) {
	data, err := os.ReadFile("../../qpkg/shared/etc/qnetspeed.conf.default")
	if err != nil {
		t.Fatal(err)
	}
	c, issues := Parse(data)
	if len(issues) != 0 {
		t.Fatalf("default file has issues: %v", issues)
	}
	if v := c.Validate(); len(v) != 0 {
		t.Fatalf("default file does not validate: %v", v)
	}
	for _, k := range Keys {
		got, ok := c.values[k.Name]
		if !ok {
			t.Errorf("%s missing from default file", k.Name)
			continue
		}
		if got != k.Default {
			t.Errorf("%s: default file has %q, code has %q", k.Name, got, k.Default)
		}
	}
	if len(c.values) != len(Keys) {
		t.Errorf("default file has %d keys, code has %d", len(c.values), len(Keys))
	}
}

func TestDefaultsValidate(t *testing.T) {
	if issues := Defaults().Validate(); len(issues) != 0 {
		t.Fatalf("defaults do not validate: %v", issues)
	}
}

func TestParseValues(t *testing.T) {
	tests := []struct {
		line string
		want string
		err  string
	}{
		{`WEB_PORT=9000`, "9000", ""},
		{`WEB_PORT=9000   # comment`, "9000", ""},
		{`WAN_IFACES="eth0 eth1"`, "eth0 eth1", ""},
		{`WAN_IFACES='eth0 eth1' # c`, "eth0 eth1", ""},
		{`WAN_IFACES=""`, "", ""},
		{`WAN_IFACES=`, "", ""},
		{`  WEB_PORT=9001`, "9001", ""},
		{`WAN_IFACES="eth0`, "", "missing closing quote"},
		{`WAN_IFACES="a"b`, "", "unexpected text"},
		{`WAN_IFACES=eth0 eth1`, "", "unexpected text"},
		{`WAN_IFACES="$(reboot)"`, "", "not allowed"},
		{"WAN_IFACES=\"`id`\"", "", "not allowed"},
		{`WAN_IFACES=a;b`, "", "double quotes"},
		{`WAN_IFACES=a*`, "", "double quotes"},
		{`web_port=1`, "", "expected KEY=value"},
		{`WEB_PORT = 1`, "", "expected KEY=value"},
		{`just text`, "", "expected KEY=value"},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			c, issues := Parse([]byte(tt.line + "\n"))
			if tt.err != "" {
				if len(issues) != 1 || issues[0].Severity != Error || !strings.Contains(issues[0].Msg, tt.err) {
					t.Fatalf("issues = %v, want one error containing %q", issues, tt.err)
				}
				if len(c.values) != 0 {
					t.Errorf("bad line stored a value: %v", c.values)
				}
				return
			}
			if len(issues) != 0 {
				t.Fatalf("unexpected issues %v", issues)
			}
			for _, v := range c.values {
				if v != tt.want {
					t.Errorf("value = %q, want %q", v, tt.want)
				}
			}
		})
	}
}

func TestParseUnknownAndDuplicate(t *testing.T) {
	c, issues := Parse([]byte("WEB_PORTT=1\nWEB_PORT=9000\nWEB_PORT=9001\n"))
	if len(issues) != 2 {
		t.Fatalf("issues = %v", issues)
	}
	if issues[0].Severity != Warning || issues[0].Line != 1 || !strings.Contains(issues[0].Msg, "unknown") {
		t.Errorf("issue 0 = %v", issues[0])
	}
	if issues[1].Severity != Warning || issues[1].Line != 3 || !strings.Contains(issues[1].Msg, "duplicate") {
		t.Errorf("issue 1 = %v", issues[1])
	}
	if got := c.Get("WEB_PORT"); got != "9001" {
		t.Errorf("WEB_PORT = %q, want last value", got)
	}
	if HasErrors(issues) {
		t.Error("warnings reported as errors")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		key, value string
		ok         bool
	}{
		{"WEB_PORT", "8888", true},
		{"WEB_PORT", "80", false},
		{"WEB_PORT", "8080", false},
		{"WEB_PORT", "70000", false},
		{"WEB_PORT", "abc", false},
		{"LANG", "pl", true},
		{"LANG", "de", false},
		{"WAN_BIND_MODE", "interface", true},
		{"WAN_SECURE", "2", false},
		{"WAN_IFACES", "eth0 bond0 qvs0", true},
		{"WAN_IFACES", "eth0;reboot", false},
		{"WAN_SERVER_IDS", "50 51", true},
		{"WAN_SERVER_IDS", "50 x", false},
		{"WAN_PROBE_IP", "2606:4700::1111", true},
		{"WAN_PROBE_IP", "one.one", false},
		{"WAN_IP_ECHO_URL", "https://api.ipify.org", true},
		{"WAN_IP_ECHO_URL", "ftp://x", false},
		{"WAN_SERVER_LIST", "/share/Public/servers.json", true},
		{"WAN_SERVER_LIST", "servers.json", false},
		{"LAN_SCHEDULE_TARGETS", "192.168.1.50 pc.local", true},
		{"LAN_SCHEDULE_TARGETS", "-oProxy", false},
		{"LINK_EXPECT", "eth0:2500 eth1:10000", true},
		{"LINK_EXPECT", "eth0", false},
		{"MIN_WAN_DL_MBPS", "12.5", true},
		{"MIN_WAN_DL_MBPS", "-1", false},
		{"CRON_SCHEDULE", "17 */6 * * *", true},
		{"CRON_SCHEDULE", "0,30 8-18 * 1-12 1-5", true},
		{"CRON_SCHEDULE", "*/15 * * * 0", true},
		{"CRON_SCHEDULE", "17 */6 * *", false},
		{"CRON_SCHEDULE", "60 * * * *", false},
		{"CRON_SCHEDULE", "5/2 * * * *", false},
		{"CRON_SCHEDULE", "+5 * * * *", false},
		{"CRON_SCHEDULE", "@daily", false},
		{"CRON_SCHEDULE", "* * * * * rm", false},
		{"HISTORY_MAX_LINES", "10", false},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			c := Defaults()
			c.values[tt.key] = tt.value
			issues := c.Validate()
			if tt.ok && len(issues) != 0 {
				t.Errorf("unexpected issues %v", issues)
			}
			if !tt.ok && !HasErrors(issues) {
				t.Errorf("expected an error")
			}
		})
	}
}

func TestPortsMustDiffer(t *testing.T) {
	c, _ := Parse([]byte("WEB_PORT=5288\n"))
	issues := c.Validate()
	if !HasErrors(issues) || issues[0].Key != "IPERF_PORT" {
		t.Fatalf("issues = %v", issues)
	}
}

func TestSetKeepsOtherLines(t *testing.T) {
	in := "# head\nWEB_PORT=8888 # gui\n\nLANG=en\n"
	c, _ := Parse([]byte(in))
	if err := c.Set("WEB_PORT", "9000"); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("WAN_IFACES", "eth0 eth1"); err != nil {
		t.Fatal(err)
	}
	want := "# head\nWEB_PORT=9000\n\nLANG=en\nWAN_IFACES=\"eth0 eth1\"\n"
	if got := string(c.Bytes()); got != want {
		t.Errorf("Bytes() =\n%s\nwant\n%s", got, want)
	}
	c2, issues := Parse(c.Bytes())
	if len(issues) != 0 || c2.Get("WAN_IFACES") != "eth0 eth1" {
		t.Errorf("round trip: %v %q", issues, c2.Get("WAN_IFACES"))
	}
}

func TestSetRejects(t *testing.T) {
	c := Defaults()
	for _, kv := range [][2]string{{"NOPE", "1"}, {"WEB_PORT", "22"}, {"WAN_IFACES", `"x"`}, {"CRON_SCHEDULE", "* * * * *\n* * * * * id"}} {
		if err := c.Set(kv[0], kv[1]); err == nil {
			t.Errorf("Set(%q, %q) accepted", kv[0], kv[1])
		}
	}
}

func TestGetUnknownPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	Defaults().Get("NOPE")
}

func TestLoadAndWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "qnetspeed.conf")
	c, _, err := Load(p)
	if !errors.Is(err, fs.ErrNotExist) || c.Get("WEB_PORT") != "8888" {
		t.Fatalf("Load missing: %v %q", err, c.Get("WEB_PORT"))
	}
	if err := WriteFileAtomic(p, []byte("WEB_PORT=9000\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("WEB_PORT=9001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want existing 0600 kept", fi.Mode().Perm())
	}
	c, issues, err := Load(p)
	if err != nil || len(issues) != 0 || c.Get("WEB_PORT") != "9001" {
		t.Fatalf("Load: %v %v %q", err, issues, c.Get("WEB_PORT"))
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temporary files left: %v", entries)
	}
}

func TestShow(t *testing.T) {
	out := string(Defaults().Show())
	for _, want := range []string{"LANG=en\n", "WAN_IFACES=\"\"\n", "CRON_SCHEDULE=\"17 */6 * * *\"\n", "WEB_PORT=8888\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("Show() missing %q", want)
		}
	}
	if n := strings.Count(out, "\n"); n != len(Keys) {
		t.Errorf("Show() has %d lines, want %d", n, len(Keys))
	}
}

func TestValidateValueAndRestartKeys(t *testing.T) {
	if err := ValidateValue("WEB_PORT", "8899"); err != nil {
		t.Errorf("good value: %v", err)
	}
	for _, c := range []struct{ k, v, want string }{
		{"WEB_PORT", "8080", "QTS"}, {"NOPE", "1", "unknown key"}, {"WAN_IFACES", "eth0$x", ""}, {"CRON_SCHEDULE", "* * * * * x", "5 fields"},
	} {
		err := ValidateValue(c.k, c.v)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s=%q: %v", c.k, c.v, err)
		}
		if err != nil && strings.HasPrefix(err.Error(), c.k) {
			t.Errorf("message repeats the key: %v", err)
		}
	}
	for k, want := range map[string]bool{"WEB_PORT": true, "IPERF_PORT": true, "CRON_SCHEDULE": true, "LAN_SERVER_ENABLE": true,
		"MIN_WAN_DL_MBPS": false, "WEB_PUBLIC_READ": false, "LANG": false} {
		if RestartRequired(k) != want {
			t.Errorf("RestartRequired(%s) = %v", k, !want)
		}
	}
	kl := KeyList()
	if len(kl) != len(Keys) || kl[0].Name != "LANG" || kl[0].Default != "en" {
		t.Errorf("KeyList = %v", kl[:2])
	}
}

func TestLoadForEdit(t *testing.T) {
	dir := t.TempDir()
	conf, def := filepath.Join(dir, "a.conf"), filepath.Join(dir, "a.conf.default")
	c, _, err := LoadForEdit(conf, def)
	if err != nil || string(c.Bytes()) != "" {
		t.Fatalf("nothing exists: %q %v", c.Bytes(), err)
	}
	os.WriteFile(def, []byte("# default\nLANG=pl\n"), 0o644)
	c, _, err = LoadForEdit(conf, def)
	if err != nil || c.Get("LANG") != "pl" {
		t.Fatalf("default file: %v", err)
	}
	os.WriteFile(conf, []byte("LANG=en\n"), 0o644)
	c, _, err = LoadForEdit(conf, def)
	if err != nil || c.Get("LANG") != "en" {
		t.Fatalf("config file wins: %v", err)
	}
}

func TestValidBitrate(t *testing.T) {
	for _, ok := range []string{"100M", "1G", "500", "64k"} {
		if ValidBitrate(ok) != nil {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "1G;x", "-1", "1T", "fast", "1234567M"} {
		if ValidBitrate(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
