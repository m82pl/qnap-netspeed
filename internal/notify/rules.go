package notify

import (
	"fmt"
	"strings"

	"github.com/m82pl/qnap-netspeed/internal/lan"
	"github.com/m82pl/qnap-netspeed/internal/link"
	"github.com/m82pl/qnap-netspeed/internal/wan"
)

// Rule names (the first part of a state key).
const (
	RuleWANDownload = "wan_dl"
	RuleWANUpload   = "wan_ul"
	RuleWANPing     = "wan_ping"
	RuleWANFail     = "wan_fail"
	RuleWANRoute    = "wan_route"
	RuleWANSameIP   = "wan_same_ip"
	RuleLANMin      = "lan_min"
	RuleLinkDegrade = "link_degraded"
	RuleLinkDown    = "link_down"
	RuleLinkHalf    = "duplex_half"
)

// Thresholds are the MIN_*/MAX_* configuration values. A threshold of 0
// switches its rule off.
type Thresholds struct {
	MinWANDownload float64 // Mb/s
	MinWANUpload   float64 // Mb/s
	MaxWANPing     float64 // ms
	MinLAN         float64 // Mb/s
	FailThreshold  int     // WAN tests failed in a row
}

// WANFindings turns the results of one run into findings (spec pkt 7).
// failStreak(iface) must return how many of the newest WAN tests of iface
// failed in a row, counting the one just made.
func WANFindings(t Thresholds, recs []wan.Record, failStreak func(iface string) int) []Finding {
	var out []Finding
	for _, r := range recs {
		if r.Route != nil { // a route lookup failure says nothing either way
			mismatch := hasWarning(r.Warnings, wan.WarnRouteMismatch)
			via := r.Route.EgressIface
			out = append(out, Finding{Rule: RuleWANRoute, Subject: r.Interface, Problem: mismatch, Level: LevelWarning,
				Message:  fmt.Sprintf("WAN %s: traffic leaves via %s, multi-WAN test not representative", r.Interface, via),
				Recovery: fmt.Sprintf("WAN %s: back to normal (traffic leaves via its own interface)", r.Interface)})
		}
		if r.Status != wan.StatusOK {
			if f := failFinding(t, r, failStreak); f.Rule != "" {
				out = append(out, f)
			}
			continue
		}
		res, srv := r.Results, serverName(r)
		out = append(out,
			Finding{Rule: RuleWANFail, Subject: r.Interface, Level: LevelError,
				Recovery: fmt.Sprintf("WAN %s: test works again (download %.1f Mbps)", r.Interface, res.DownloadMbps)},
			Finding{Rule: RuleWANSameIP, Subject: r.Interface, Level: LevelWarning,
				Problem:  hasWarning(r.Warnings, wan.WarnSamePublicIP),
				Message:  fmt.Sprintf("WAN %s: same public IP as another tested link, multi-WAN test not representative", r.Interface),
				Recovery: fmt.Sprintf("WAN %s: back to normal (own public IP)", r.Interface)},
			Finding{Rule: RuleWANDownload, Subject: r.Interface, Level: LevelWarning,
				Problem:  t.MinWANDownload > 0 && res.DownloadMbps < t.MinWANDownload,
				Message:  fmt.Sprintf("WAN %s: download %.1f Mbps < %s Mbps (server: %s)", r.Interface, res.DownloadMbps, num(t.MinWANDownload), srv),
				Recovery: fmt.Sprintf("WAN %s: back to normal (download %.1f Mbps)", r.Interface, res.DownloadMbps)},
			Finding{Rule: RuleWANUpload, Subject: r.Interface, Level: LevelWarning,
				Problem:  t.MinWANUpload > 0 && res.UploadMbps < t.MinWANUpload,
				Message:  fmt.Sprintf("WAN %s: upload %.1f Mbps < %s Mbps (server: %s)", r.Interface, res.UploadMbps, num(t.MinWANUpload), srv),
				Recovery: fmt.Sprintf("WAN %s: back to normal (upload %.1f Mbps)", r.Interface, res.UploadMbps)},
			Finding{Rule: RuleWANPing, Subject: r.Interface, Level: LevelWarning,
				Problem:  t.MaxWANPing > 0 && res.PingMs > t.MaxWANPing,
				Message:  fmt.Sprintf("WAN %s: ping %.1f ms > %s ms (server: %s)", r.Interface, res.PingMs, num(t.MaxWANPing), srv),
				Recovery: fmt.Sprintf("WAN %s: back to normal (ping %.1f ms)", r.Interface, res.PingMs)},
		)
	}
	return out
}

// failFinding is the "N tests failed in a row" rule for a record that is not
// OK. Below the threshold it returns the zero Finding: no verdict yet.
func failFinding(t Thresholds, r wan.Record, failStreak func(string) int) Finding {
	n := 0
	if failStreak != nil {
		n = failStreak(r.Interface)
	}
	if n < t.FailThreshold {
		return Finding{}
	}
	reason := "no details"
	if r.Error != nil {
		reason = *r.Error
	}
	return Finding{Rule: RuleWANFail, Subject: r.Interface, Problem: true, Level: LevelError,
		Message: fmt.Sprintf("WAN %s: test failed %dx in a row: %s", r.Interface, n, reason)}
}

// LANFindings checks scheduled LAN tests against MinLAN: the slower of the
// two directions counts. A failed test gives no verdict.
func LANFindings(t Thresholds, recs []lan.Record) []Finding {
	var out []Finding
	for _, r := range recs {
		if r.Status != "ok" || r.Results == nil {
			continue
		}
		up, down := r.Results.UploadMbps, r.Results.DownloadMbps
		low := up
		if down < low {
			low = down
		}
		out = append(out, Finding{Rule: RuleLANMin, Subject: r.Target, Level: LevelWarning,
			Problem: t.MinLAN > 0 && low < t.MinLAN,
			Message: fmt.Sprintf("LAN %s: upload %.1f / download %.1f Mbps < %s Mbps", r.Target, up, down, num(t.MinLAN)),
			Recovery: fmt.Sprintf("LAN %s: back to normal (upload %.1f / download %.1f Mbps)",
				r.Target, up, down)})
	}
	return out
}

// LinkFindings turns the result of link.Evaluate into findings for every
// interface, also when LINK_EXPECT no longer names it, so that removing the
// expectation closes a problem that is still open. A rule whose input is unknown (speed that the driver does not
// report, no duplex) gives no verdict instead of a false "back to normal".
func LinkFindings(ifaces []link.Interface, warnings []string) []Finding {
	has := map[string]bool{}
	for _, w := range warnings {
		has[w] = true
	}
	var out []Finding
	for _, i := range ifaces {
		speed := "speed unknown"
		if i.SpeedMbps != nil {
			speed = fmt.Sprintf("%d Mb/s", *i.SpeedMbps)
		}
		expected := 0
		if i.ExpectedMbps != nil {
			expected = *i.ExpectedMbps
		}
		if p := has["link_degraded:"+i.Name]; p || i.SpeedMbps != nil {
			out = append(out, Finding{Rule: RuleLinkDegrade, Subject: i.Name, Problem: p, Level: LevelWarning,
				Message:  fmt.Sprintf("Link %s: %s, expected %d Mb/s", i.Name, speed, expected),
				Recovery: fmt.Sprintf("Link %s: back to normal (%s)", i.Name, speed)})
		}
		if p := has["link_down:"+i.Name]; p || i.Operstate == "up" {
			out = append(out, Finding{Rule: RuleLinkDown, Subject: i.Name, Problem: p, Level: LevelWarning,
				Message:  fmt.Sprintf("Link %s: no carrier (cable unplugged or port off)", i.Name),
				Recovery: fmt.Sprintf("Link %s: back to normal (carrier present, %s)", i.Name, speed)})
		}
		if p := has["duplex_half:"+i.Name]; p || (i.Kind == link.KindPhysical && i.Duplex == "full") {
			out = append(out, Finding{Rule: RuleLinkHalf, Subject: i.Name, Problem: p, Level: LevelWarning,
				Message:  fmt.Sprintf("Link %s: half duplex, %s", i.Name, speed),
				Recovery: fmt.Sprintf("Link %s: back to normal (full duplex, %s)", i.Name, speed)})
		}
	}
	return out
}

func hasWarning(ws []string, code string) bool {
	for _, w := range ws {
		if w == code {
			return true
		}
	}
	return false
}

func serverName(r wan.Record) string {
	if r.Server != nil && r.Server.Name != "" {
		return r.Server.Name
	}
	return "unknown"
}

// num prints a threshold without a trailing ".0".
func num(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	return strings.TrimSuffix(s, ".0")
}
