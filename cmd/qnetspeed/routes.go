package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"strings"
	"text/tabwriter"

	"github.com/m82pl/qnap-netspeed/internal/link"
	"github.com/m82pl/qnap-netspeed/internal/netroute"
)

const routesUsage = "Usage: qnetspeed routes [--json]\n"

type routeRow struct {
	Interface   string `json:"interface"`
	Address     string `json:"address"`
	Gateway     string `json:"gateway"`
	EgressIface string `json:"egress_iface"`
	Table       string `json:"table"`
	PolicyRule  bool   `json:"policy_rule"`
	State       string `json:"state"` // ok | mismatch | no_ip | error
	Error       string `json:"error,omitempty"`
}

// routes shows, per interface, where traffic from its address would leave
// (spec pkt 5.1.1). It lists WAN_IFACES, or every non-loopback interface.
func (a *app) routes(args []string) int {
	fset := flag.NewFlagSet("routes", flag.ContinueOnError)
	var asJSON bool
	fset.BoolVar(&asJSON, "json", false, "")
	pos, err := parseFlags(fset, args)
	if err != nil || len(pos) > 0 {
		fmt.Fprint(a.stderr, routesUsage)
		return exitUsage
	}
	c, issues, code := a.loadConfig(false)
	if code != exitOK {
		a.printIssues(issues)
		return code
	}
	sys := a.sys()
	names := strings.Fields(c.Get("WAN_IFACES"))
	if len(names) == 0 {
		all, err := link.Read(sys.sysfs)
		if err != nil {
			fmt.Fprintf(a.stderr, "qnetspeed routes: %v\n", err)
			return exitTestError
		}
		for _, i := range all {
			if i.Name != "lo" {
				names = append(names, i.Name)
			}
		}
	}
	probe := net.ParseIP(c.Get("WAN_PROBE_IP"))
	var rows []routeRow
	for _, n := range names {
		rows = append(rows, routeRowFor(netroute.Inspect(sys.net, n, probe)))
	}
	if asJSON {
		b, _ := json.Marshal(rows)
		fmt.Fprintf(a.stdout, "%s\n", b)
		return exitOK
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, tr(c, "IFACE\tADDRESS\tGATEWAY\tEGRESS\tTABLE\tRULE\tSTATE", "INTERFEJS\tADRES\tBRAMA\tWYJŚCIE\tTABELA\tREGUŁA\tSTAN"))
	for _, r := range rows {
		rule := "-"
		if r.PolicyRule {
			rule = "from"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Interface, dash(r.Address), dash(r.Gateway), dash(r.EgressIface),
			dash(r.Table), rule, routeStateText(c, r))
	}
	tw.Flush()
	for _, r := range rows {
		if r.State == "mismatch" {
			fmt.Fprintf(a.stderr, tr(c,
				"warning: traffic from %s leaves through %s — set up source-based routing in Network & Virtual Switch\n",
				"ostrzeżenie: ruch z %s wychodzi przez %s — skonfiguruj routing zależny od źródła w Network & Virtual Switch\n"),
				r.Interface, r.EgressIface)
		}
	}
	return exitOK
}

func routeRowFor(k netroute.Check) routeRow {
	r := routeRow{Interface: k.Iface}
	if k.Address != nil {
		r.Address = k.Address.String()
	}
	switch {
	case k.Reason == netroute.ReasonNoIPv4, k.Reason == netroute.ReasonNoIface:
		r.State = "no_ip"
		if k.Err != nil {
			r.Error = k.Err.Error()
		}
	case k.Route == nil:
		r.State = "error"
		if k.Err != nil {
			r.Error = k.Err.Error()
		}
	default:
		r.Gateway, r.EgressIface, r.Table, r.PolicyRule = k.Route.Gateway, k.Route.EgressIface, k.Route.Table, k.Route.PolicyRule
		r.State = "ok"
		if k.Route.Mismatch {
			r.State = "mismatch"
		}
	}
	return r
}

func routeStateText(c interface{ Get(string) string }, r routeRow) string {
	pl := c.Get("LANG") == "pl"
	switch r.State {
	case "ok":
		return "OK"
	case "mismatch":
		return "mismatch"
	case "no_ip":
		if pl {
			return "brak IP"
		}
		return "no IP"
	}
	if pl {
		return "błąd: " + r.Error
	}
	return "error: " + r.Error
}
