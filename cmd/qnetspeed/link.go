package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/m82pl/qnap-netspeed/internal/config"
	"github.com/m82pl/qnap-netspeed/internal/link"
)

const linkUsage = "Usage: qnetspeed link [--iface IF] [--json] [--record]\n"

// link shows the state of network interfaces (spec pkt 5.3). It reads only
// /sys/class/net, so it needs no root; --record appends to link.jsonl.
func (a *app) link(args []string) int {
	fset := flag.NewFlagSet("link", flag.ContinueOnError)
	var iface string
	var asJSON, record bool
	fset.StringVar(&iface, "iface", "", "")
	fset.BoolVar(&asJSON, "json", false, "")
	fset.BoolVar(&record, "record", false, "")
	pos, err := parseFlags(fset, args)
	if err != nil || len(pos) > 0 {
		fmt.Fprint(a.stderr, linkUsage)
		return exitUsage
	}
	c, issues, code := a.loadConfig(false)
	if code != exitOK {
		a.printIssues(issues)
		return code
	}
	sys := a.sys()
	all, err := link.Read(sys.sysfs)
	if err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed link: %v\n", err)
		return exitTestError
	}
	warnings := link.Evaluate(all, link.ParseExpect(c.Get("LINK_EXPECT")))
	shown := all
	var phys []link.Physical
	var sum *int
	if iface != "" {
		if err := config.ValidIface(iface); err != nil {
			fmt.Fprintf(a.stderr, "qnetspeed link: %v\n", err)
			return exitUsage
		}
		shown = nil
		for _, i := range all {
			if i.Name == iface {
				shown = append(shown, i)
			}
		}
		if len(shown) == 0 {
			fmt.Fprintf(a.stderr, "qnetspeed link: interface %s not found\n", iface)
			return exitTestError
		}
		phys, sum = link.Resolve(sys.sysfs, iface)
	}
	rec := link.Record{Schema: 1, Timestamp: sys.now().UTC().Truncate(1e9), Type: "LINK", Interfaces: shown, Warnings: warnings}
	if record {
		if !a.needRoot(c) {
			return exitTestError
		}
		a.store(a.layout.LinkHistory(), c, rec)
	}
	if asJSON {
		out := struct {
			link.Record
			Physical []link.Physical `json:"physical,omitempty"`
			SumMbps  *int            `json:"physical_sum_mbps,omitempty"`
		}{rec, phys, sum}
		b, _ := json.Marshal(out)
		fmt.Fprintf(a.stdout, "%s\n", b)
		return exitOK
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "IFACE\tTYPE\tSTATE\tSPEED\tDUPLEX\tMTU\tMASTER/MEMBERS\tDRIVER")
	for _, i := range shown {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", i.Name, i.Kind, dash(i.Operstate), speedText(c, i),
			dash(i.Duplex), i.MTU, relation(i), dash(i.Driver))
	}
	tw.Flush()
	for _, p := range phys {
		sp := tr(c, "unknown", "nieznana")
		if p.SpeedMbps != nil {
			sp = strconv.Itoa(*p.SpeedMbps) + " Mb/s"
		}
		fmt.Fprintf(a.stdout, tr(c, "physical: %s %s, %s, MTU %d\n", "fizyczny: %s %s, %s, MTU %d\n"), p.Name, sp, dash(p.Duplex), p.MTU)
	}
	if sum != nil {
		fmt.Fprintf(a.stdout, tr(c, "bond total: %d Mb/s\n", "suma bonda: %d Mb/s\n"), *sum)
	}
	for _, w := range warnings {
		if iface == "" || strings.HasSuffix(w, ":"+iface) {
			fmt.Fprintln(a.stderr, linkWarningText(c, w))
		}
	}
	return exitOK
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func speedText(c *config.Config, i link.Interface) string {
	switch {
	case i.SpeedMbps == nil && i.Kind == link.KindPhysical:
		return tr(c, "n/a", "brak")
	case i.SpeedMbps == nil:
		return "-"
	case i.ExpectedMbps != nil:
		return fmt.Sprintf("%d (%s %d)", *i.SpeedMbps, tr(c, "expected", "oczekiwane"), *i.ExpectedMbps)
	}
	return strconv.Itoa(*i.SpeedMbps)
}

func relation(i link.Interface) string {
	switch {
	case len(i.Members) > 0:
		return "[" + strings.Join(i.Members, ",") + "]"
	case i.Master != "":
		return i.Master
	}
	return "-"
}

func linkWarningText(c *config.Config, w string) string {
	code, name, _ := strings.Cut(w, ":")
	switch code {
	case "link_degraded":
		return fmt.Sprintf(tr(c, "warning: %s negotiated less than expected (LINK_EXPECT)", "ostrzeżenie: %s ma niższą prędkość niż oczekiwana (LINK_EXPECT)"), name)
	case "link_down":
		return fmt.Sprintf(tr(c, "warning: %s has no carrier", "ostrzeżenie: %s nie ma nośnej (brak kabla lub wyłączony port)"), name)
	case "duplex_half":
		return fmt.Sprintf(tr(c, "warning: %s runs half duplex", "ostrzeżenie: %s pracuje w half duplex"), name)
	case "mtu_mismatch":
		return fmt.Sprintf(tr(c, "note: %s and its members have different MTU", "uwaga: %s i jego członkowie mają różne MTU"), name)
	}
	return "warning: " + w
}
