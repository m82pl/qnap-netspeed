package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// key describes one configuration key: its default and its validator.
// The order of Keys is the order of `config show` and of the default file (spec pkt 10).
type key struct {
	Name     string
	Default  string
	Validate func(string) error
}

// Keys lists every known key (spec pkt 10).
var Keys = []key{
	// Ogólne
	{"LANG", "en", oneOf("en", "pl")},

	// WAN
	{"WAN_IFACES", "", ifaceList},
	{"WAN_BIND_MODE", "source", oneOf("source", "interface")},
	{"WAN_SERVER_IDS", "", intList(1, 1_000_000)},
	{"WAN_SERVER_LIST", "", absPathOrEmpty},
	{"WAN_SECURE", "0", boolean},
	{"WAN_DURATION", "10", intRange(1, 120)},
	{"WAN_PROBE_IP", "1.1.1.1", ipAddr},
	{"WAN_IP_ECHO_URL", "", httpURLOrEmpty},
	{"WAN_SCHEDULE_ENABLE", "1", boolean},

	// LAN
	{"IPERF_PORT", "5201", port},
	{"LAN_BIND", "", ipOrEmpty},
	{"LAN_SERVER_ENABLE", "1", boolean},
	{"LAN_TEST_DURATION", "10", intRange(1, 300)},
	{"LAN_PARALLEL_STREAMS", "4", intRange(1, 32)},
	{"LAN_SCHEDULE_TARGETS", "", hostList},

	// Linki
	{"LINK_EXPECT", "", linkExpect},

	// Progi i powiadomienia
	{"MIN_WAN_DL_MBPS", "100", nonNegNumber},
	{"MIN_WAN_UL_MBPS", "30", nonNegNumber},
	{"MAX_WAN_PING_MS", "50", nonNegNumber},
	{"WAN_FAIL_THRESHOLD", "2", intRange(1, 100)},
	{"MIN_LAN_MBPS", "0", nonNegNumber},
	{"NOTIFY_ENABLE", "1", boolean},
	{"NOTIFY_REPEAT_HOURS", "24", intRange(1, 720)},

	// Harmonogram i historia
	{"CRON_SCHEDULE", "17 */6 * * *", cronExpr},
	{"HISTORY_MAX_LINES", "5000", intRange(100, 1_000_000)},

	// GUI
	{"WEB_PORT", "8888", port},
	{"WEB_BIND", "", ipOrEmpty},
	{"WEB_PUBLIC_READ", "1", boolean},
	{"WEB_TEST_DURATION", "10", intRange(1, 60)},
}

var keyIndex = func() map[string]*key {
	m := make(map[string]*key, len(Keys))
	for i := range Keys {
		m[Keys[i].Name] = &Keys[i]
	}
	return m
}()

// Known reports whether name is a configuration key.
func Known(name string) bool { _, ok := keyIndex[name]; return ok }

// Firmware ports that the package must not use (spec pkt 2).
var firmwarePorts = map[int]bool{22: true, 80: true, 443: true, 548: true, 3310: true,
	5000: true, 8007: true, 8080: true, 8081: true, 13131: true}

func oneOf(allowed ...string) func(string) error {
	return func(v string) error {
		for _, a := range allowed {
			if v == a {
				return nil
			}
		}
		return fmt.Errorf("must be one of: %s", strings.Join(allowed, ", "))
	}
}

func boolean(v string) error { return oneOf("0", "1")(v) }

func intRange(lo, hi int) func(string) error {
	return func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			return fmt.Errorf("must be an integer %d..%d", lo, hi)
		}
		return nil
	}
}

func intList(lo, hi int) func(string) error {
	return func(v string) error {
		for _, f := range strings.Fields(v) {
			if err := intRange(lo, hi)(f); err != nil {
				return fmt.Errorf("%q: %v", f, err)
			}
		}
		return nil
	}
}

func nonNegNumber(v string) error {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 1e6 {
		return fmt.Errorf("must be a number 0..1000000")
	}
	return nil
}

func port(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1024 || n > 65535 {
		return fmt.Errorf("must be a port 1024..65535")
	}
	if firmwarePorts[n] {
		return fmt.Errorf("port %d is used by QTS", n)
	}
	return nil
}

func ipAddr(v string) error {
	if net.ParseIP(v) == nil {
		return fmt.Errorf("must be an IPv4 or IPv6 address")
	}
	return nil
}

func ipOrEmpty(v string) error {
	if v == "" {
		return nil
	}
	return ipAddr(v)
}

var ifaceRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)

func ifaceList(v string) error {
	for _, f := range strings.Fields(v) {
		if !ifaceRe.MatchString(f) {
			return fmt.Errorf("%q is not a valid interface name", f)
		}
	}
	return nil
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}$`)

func hostList(v string) error {
	for _, f := range strings.Fields(v) {
		if net.ParseIP(f) == nil && (!hostRe.MatchString(f) || strings.HasPrefix(f, "-")) {
			return fmt.Errorf("%q is not an IP address or host name", f)
		}
	}
	return nil
}

func linkExpect(v string) error {
	for _, f := range strings.Fields(v) {
		name, speed, ok := strings.Cut(f, ":")
		if !ok || !ifaceRe.MatchString(name) || intRange(10, 1_000_000)(speed) != nil {
			return fmt.Errorf("%q: expected IFACE:MBPS, e.g. eth0:2500", f)
		}
	}
	return nil
}

func absPathOrEmpty(v string) error {
	if v != "" && !strings.HasPrefix(v, "/") {
		return fmt.Errorf("must be an absolute path")
	}
	return nil
}

func httpURLOrEmpty(v string) error {
	if v == "" {
		return nil
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("must be an http:// or https:// URL")
	}
	return nil
}

// cronExpr accepts a 5-field numeric cron expression (minute hour day month weekday).
// Only digits, '*', '/', ',' and '-' are allowed: the value is written into
// /etc/config/crontab, which runs as root.
func cronExpr(v string) error {
	fields := strings.Fields(v)
	if len(fields) != 5 {
		return fmt.Errorf("must have 5 fields (minute hour day month weekday)")
	}
	limits := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	names := [5]string{"minute", "hour", "day", "month", "weekday"}
	for i, f := range fields {
		if err := cronField(f, limits[i][0], limits[i][1]); err != nil {
			return fmt.Errorf("%s %q: %v", names[i], f, err)
		}
	}
	return nil
}

var cronFieldRe = regexp.MustCompile(`^[0-9*/,-]+$`)

func cronField(f string, lo, hi int) error {
	if !cronFieldRe.MatchString(f) {
		return fmt.Errorf("only digits and * / , - are allowed")
	}
	for _, part := range strings.Split(f, ",") {
		rng, step, hasStep := strings.Cut(part, "/")
		if hasStep {
			n, err := strconv.Atoi(step)
			if err != nil || n < 1 || n > hi {
				return fmt.Errorf("bad step")
			}
		}
		if rng == "*" {
			continue
		}
		a, b, isRange := strings.Cut(rng, "-")
		x, err := strconv.Atoi(a)
		if err != nil || x < lo || x > hi {
			return fmt.Errorf("value out of range %d..%d", lo, hi)
		}
		if isRange {
			y, err := strconv.Atoi(b)
			if err != nil || y < x || y > hi {
				return fmt.Errorf("bad range")
			}
		} else if hasStep {
			return fmt.Errorf("step needs '*' or a range")
		}
	}
	return nil
}
