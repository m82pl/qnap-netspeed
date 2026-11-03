package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/history"
)

const historyUsage = "Usage: qnetspeed history [--type wan|lan|link] [--last N] [--since 7d] [--iface IF] [--json|--csv]\n"

// row is the union of the fields history prints from wan.jsonl, lan.jsonl and link.jsonl.
type row = history.Row

// history prints the most recent results (spec pkt 6). Reading needs no root.
func (a *app) history(args []string) int {
	fset := flag.NewFlagSet("history", flag.ContinueOnError)
	var typ, since, iface string
	var last int
	var asJSON, asCSV bool
	fset.StringVar(&typ, "type", "wan", "")
	fset.IntVar(&last, "last", 20, "")
	fset.StringVar(&since, "since", "", "")
	fset.StringVar(&iface, "iface", "", "")
	fset.BoolVar(&asJSON, "json", false, "")
	fset.BoolVar(&asCSV, "csv", false, "")
	pos, err := parseFlags(fset, args)
	if err != nil || len(pos) > 0 || last < 1 || (asJSON && asCSV) {
		fmt.Fprint(a.stderr, historyUsage)
		return exitUsage
	}
	var path string
	switch typ {
	case "wan":
		path = a.layout.WanHistory()
	case "lan":
		path = a.layout.LanHistory()
	case "link":
		path = a.layout.LinkHistory()
	default:
		fmt.Fprint(a.stderr, historyUsage)
		return exitUsage
	}
	f := history.Filter{Interface: iface, Last: last}
	if since != "" {
		d, err := parseSince(since)
		if err != nil {
			fmt.Fprintf(a.stderr, "qnetspeed history: --since %q: %v\n", since, err)
			return exitUsage
		}
		f.Since = a.sys().now().Add(-d)
	}
	c, _, _ := a.loadConfig(false) // only LANG matters here; a broken config must not hide the history
	recs, skipped, err := history.Store{Path: path}.Read(f)
	if err != nil {
		fmt.Fprintf(a.stderr, "qnetspeed history: %v\n", err)
		return exitTestError
	}
	if skipped > 0 && !asJSON {
		fmt.Fprintf(a.stderr, tr(c, "note: %d damaged line(s) skipped\n", "uwaga: pominięto %d uszkodzonych linii\n"), skipped)
	}
	if asJSON {
		raws := make([]json.RawMessage, len(recs))
		for i, r := range recs {
			raws[i] = r.Raw
		}
		b, _ := json.Marshal(struct {
			Records      []json.RawMessage `json:"records"`
			SkippedLines int               `json:"skipped_lines"`
		}{raws, skipped})
		fmt.Fprintf(a.stdout, "%s\n", b)
		return exitOK
	}
	rows := history.Rows(recs)
	if asCSV {
		return a.historyCSV(typ, rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(a.stdout, tr(c, "no results yet", "brak wyników"))
		return exitOK
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	switch typ {
	case "wan":
		fmt.Fprintln(tw, tr(c, "TIME\tIFACE\tSTATUS\tDOWN Mb/s\tUP Mb/s\tPING ms\tNOTES", "CZAS\tINTERFEJS\tSTAN\tPOBR. Mb/s\tWYSŁ. Mb/s\tPING ms\tUWAGI"))
		for _, r := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", localTime(r.Timestamp), r.Interface, r.Status,
				num(r.Results["download_mbps"], 2), num(r.Results["upload_mbps"], 2), num(r.Results["ping_ms"], 1), notes(r))
		}
	case "lan":
		fmt.Fprintln(tw, tr(c, "TIME\tTARGET\tIFACE\tSTATUS\tUP Mb/s\tDOWN Mb/s\tNOTES", "CZAS\tCEL\tINTERFEJS\tSTAN\tWYSŁ. Mb/s\tPOBR. Mb/s\tUWAGI"))
		for _, r := range rows {
			target := r.Target
			if target == "" {
				target = r.ClientIP
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", localTime(r.Timestamp), dash(target), dash(r.Interface), dash(r.Status),
				num(r.Results["upload_mbps"], 2), num(r.Results["download_mbps"], 2), notes(r))
		}
	default:
		fmt.Fprintln(tw, tr(c, "TIME\tWARNINGS", "CZAS\tOSTRZEŻENIA"))
		for _, r := range rows {
			fmt.Fprintf(tw, "%s\t%s\n", localTime(r.Timestamp), dash(strings.Join(r.Warnings, " ")))
		}
	}
	tw.Flush()
	return exitOK
}

func (a *app) historyCSV(typ string, rows []row) int {
	if history.WriteCSV(a.stdout, typ, rows) != nil {
		return exitTestError
	}
	return exitOK
}

func ts(t time.Time) string        { return t.UTC().Format(time.RFC3339) }
func localTime(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }

// num formats an optional number; prec < 0 means the shortest exact form (CSV).
func num(v *float64, prec int) string { return history.Num(v, prec) }

func notes(r row) string {
	var n []string
	n = append(n, r.Warnings...)
	if r.Error != nil && *r.Error != "" {
		n = append(n, *r.Error)
	}
	return dash(strings.Join(n, "; "))
}

// parseSince reads 90m, 12h, 7d or 2w.
func parseSince(s string) (time.Duration, error) { return history.ParseSince(s) }
