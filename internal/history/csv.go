package history

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Row is the part of a history line that tables and CSV need, for all types.
type Row struct {
	Timestamp time.Time              `json:"timestamp"`
	Type      string                 `json:"type"`
	Trigger   string                 `json:"trigger"`
	Status    string                 `json:"status"`
	Interface string                 `json:"interface"`
	Target    string                 `json:"target"`
	ClientIP  string                 `json:"client_ip"`
	PublicIP  *string                `json:"public_ip"`
	Server    *struct{ Name string } `json:"server"`
	Results   map[string]*float64    `json:"results"`
	Warnings  []string               `json:"warnings"`
	Error     *string                `json:"error"`
}

// Rows decodes records; lines that do not decode as a Row are left out.
func Rows(recs []Record) []Row {
	rows := make([]Row, 0, len(recs))
	for _, r := range recs {
		var x Row
		if json.Unmarshal(r.Raw, &x) == nil {
			rows = append(rows, x)
		}
	}
	return rows
}

// ParseSince parses "90m", "12h", "7d" or "2w" (spec pkt 6, --since).
func ParseSince(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("expected e.g. 90m, 12h, 7d, 2w")
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n < 1 {
		return 0, fmt.Errorf("expected e.g. 90m, 12h, 7d, 2w")
	}
	unit := map[byte]time.Duration{'m': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[s[len(s)-1]]
	if unit == 0 {
		return 0, fmt.Errorf("unit must be m, h, d or w")
	}
	return time.Duration(n) * unit, nil
}

// Num formats an optional number; prec < 0 means the shortest exact form.
func Num(v *float64, prec int) string {
	if v == nil {
		return "-"
	}
	if prec < 0 {
		return strconv.FormatFloat(*v, 'f', -1, 64)
	}
	return strconv.FormatFloat(*v, 'f', prec, 64)
}

func csvNum(v *float64) string { return Num(v, -1) }

// WriteCSV writes rows of one history type ("wan", "lan" or "link") as CSV.
func WriteCSV(out io.Writer, typ string, rows []Row) error {
	w := csv.NewWriter(out)
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	ts := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	switch typ {
	case "wan":
		w.Write([]string{"timestamp", "interface", "trigger", "status", "download_mbps", "upload_mbps", "ping_ms", "jitter_ms", "server", "public_ip", "warnings", "error"})
		for _, r := range rows {
			server := ""
			if r.Server != nil {
				server = r.Server.Name
			}
			w.Write([]string{ts(r.Timestamp), r.Interface, r.Trigger, r.Status, csvNum(r.Results["download_mbps"]), csvNum(r.Results["upload_mbps"]),
				csvNum(r.Results["ping_ms"]), csvNum(r.Results["jitter_ms"]), server, str(r.PublicIP), strings.Join(r.Warnings, " "), str(r.Error)})
		}
	case "lan":
		w.Write([]string{"timestamp", "type", "target", "interface", "trigger", "status", "upload_mbps", "download_mbps", "retransmits_upload", "jitter_ms", "lost_percent", "warnings", "error"})
		for _, r := range rows {
			target := r.Target
			if target == "" {
				target = r.ClientIP
			}
			w.Write([]string{ts(r.Timestamp), r.Type, target, r.Interface, r.Trigger, r.Status, csvNum(r.Results["upload_mbps"]), csvNum(r.Results["download_mbps"]),
				csvNum(r.Results["retransmits_upload"]), csvNum(r.Results["jitter_ms"]), csvNum(r.Results["lost_percent"]), strings.Join(r.Warnings, " "), str(r.Error)})
		}
	default:
		w.Write([]string{"timestamp", "warnings"})
		for _, r := range rows {
			w.Write([]string{ts(r.Timestamp), strings.Join(r.Warnings, " ")})
		}
	}
	w.Flush()
	return w.Error()
}
