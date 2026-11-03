package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/config"
	"github.com/m82pl/qnap-netspeed/internal/history"
	"github.com/m82pl/qnap-netspeed/internal/iperfsrv"
	"github.com/m82pl/qnap-netspeed/internal/lock"
)

func asBusy(err error, target **lock.BusyError) bool { return errors.As(err, target) }

// Status is the body of GET /api/v1/status.
type Status struct {
	Name          string          `json:"name"`
	Version       string          `json:"version"`
	Arch          string          `json:"arch"`
	StartedAt     time.Time       `json:"started_at"`
	UptimeSeconds int64           `json:"uptime_seconds"`
	TestRunning   *lock.Holder    `json:"test_running"`
	LANServer     iperfsrv.Status `json:"lan_server"`
	Warnings      []string        `json:"warnings"`
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	in := s.o.Info
	st := Status{
		Name:          "qnap-netspeed",
		Version:       in.Version,
		Arch:          in.Arch,
		StartedAt:     in.Started.UTC(),
		UptimeSeconds: int64(s.o.Now().Sub(in.Started).Seconds()),
		Warnings:      s.o.Backend.Warnings(),
	}
	if st.Warnings == nil {
		st.Warnings = []string{}
	}
	if in.LockFile != "" {
		st.TestRunning, _ = lock.Current(in.LockFile)
	}
	if s.o.LANServer != nil {
		st.LANServer = s.o.LANServer.Status()
	} else {
		st.LANServer = iperfsrv.Status{State: iperfsrv.StateStopped}
	}
	writeJSON(w, http.StatusOK, st)
}

const (
	historyDefaultLast = 100
	historyMaxLast     = 5000
)

// GET /api/v1/history?type=wan&last=100&since=7d&iface=eth0[&format=csv]
func (s *Server) apiHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	typ := q.Get("type")
	if typ == "" {
		typ = "wan"
	}
	if typ != "wan" && typ != "lan" && typ != "link" {
		writeError(w, http.StatusBadRequest, "bad_request", "type must be wan, lan or link")
		return
	}
	f := history.Filter{Last: historyDefaultLast}
	if v := q.Get("last"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > historyMaxLast {
			writeError(w, http.StatusBadRequest, "bad_request", "last must be an integer 1.."+strconv.Itoa(historyMaxLast))
			return
		}
		f.Last = n
	}
	if v := q.Get("since"); v != "" {
		if d, err := history.ParseSince(v); err == nil {
			f.Since = s.o.Now().Add(-d)
		} else if t, err2 := time.Parse(time.RFC3339, v); err2 == nil {
			f.Since = t
		} else {
			writeError(w, http.StatusBadRequest, "bad_request", "since must be a time like 90m, 12h, 7d, 2w or an RFC 3339 timestamp")
			return
		}
	}
	if v := q.Get("iface"); v != "" {
		if err := config.ValidIface(v); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		f.Interface = v
	}
	format := q.Get("format")
	if format != "" && format != "json" && format != "csv" {
		writeError(w, http.StatusBadRequest, "bad_request", "format must be json or csv")
		return
	}
	recs, skipped, err := s.o.Backend.History(typ, f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "history_failed", err.Error())
		return
	}
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-history.csv"`, typ))
		w.Header().Set("Cache-Control", "no-store")
		history.WriteCSV(w, typ, history.Rows(recs))
		return
	}
	raws := make([]any, len(recs))
	for i, rec := range recs {
		raws[i] = rec.Raw
	}
	writeJSON(w, http.StatusOK, struct {
		Records      []any `json:"records"`
		SkippedLines int   `json:"skipped_lines"`
	}{raws, skipped})
}

func (s *Server) apiLink(w http.ResponseWriter, r *http.Request) {
	rec, err := s.o.Backend.Link()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "link_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) apiRoutes(w http.ResponseWriter, r *http.Request) {
	rows, err := s.o.Backend.Routes(r.URL.Query().Get("all") == "1")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "routes_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) apiTestGet(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no_such_test", "no such test (finished tests are kept for a while only)")
		return
	}
	writeJSON(w, http.StatusOK, j)
}

const maxListItems = 16

// startResponse is the 202 answer of the test endpoints.
func (s *Server) accepted(w http.ResponseWriter, j Job) {
	w.Header().Set("Location", "/api/v1/tests/"+j.ID)
	writeJSON(w, http.StatusAccepted, struct {
		TestID    string    `json:"test_id"`
		Type      string    `json:"type"`
		State     string    `json:"state"`
		StartedAt time.Time `json:"started_at"`
	}{j.ID, j.Type, j.State, j.StartedAt})
}

// POST /api/v1/tests/wan {"ifaces":[...],"server_ids":[...]}
func (s *Server) apiTestWAN(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ifaces    []string `json:"ifaces"`
		ServerIDs []int    `json:"server_ids"`
	}
	if !decodeJSON(w, r, &req, true) {
		return
	}
	if len(req.Ifaces) > maxListItems || len(req.ServerIDs) > maxListItems {
		writeError(w, http.StatusUnprocessableEntity, "bad_request", "at most "+strconv.Itoa(maxListItems)+" interfaces and server ids")
		return
	}
	for _, i := range req.Ifaces {
		if err := config.ValidIface(i); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "bad_request", err.Error())
			return
		}
	}
	for _, id := range req.ServerIDs {
		if id < 1 || id > 1_000_000 {
			writeError(w, http.StatusUnprocessableEntity, "bad_request", "server_ids must be numbers 1..1000000")
			return
		}
	}
	if _, err := s.o.Backend.Config(); err != nil {
		writeError(w, http.StatusInternalServerError, "config_invalid", err.Error())
		return
	}
	j, err := s.jobs.start("wan", func(ctx context.Context) ([]any, error) {
		return s.o.Backend.RunWAN(ctx, req.Ifaces, req.ServerIDs)
	})
	if err != nil {
		writeBusy(w, err)
		return
	}
	s.accepted(w, j)
}

// POST /api/v1/tests/lan {"target":"host","time":10,"parallel":4,"udp":false}
func (s *Server) apiTestLAN(w http.ResponseWriter, r *http.Request) {
	var req LANRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if err := config.ValidHost(req.Target); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "bad_request", "target: "+err.Error())
		return
	}
	if req.Iface != "" {
		if err := config.ValidIface(req.Iface); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "bad_request", "iface: "+err.Error())
			return
		}
	}
	if req.Bitrate == "" {
		req.Bitrate = "1G"
	}
	if err := config.ValidBitrate(req.Bitrate); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "bad_request", "bitrate: "+err.Error())
		return
	}
	if req.Time < 0 || req.Time > 300 {
		writeError(w, http.StatusUnprocessableEntity, "bad_request", "time must be 1..300 seconds")
		return
	}
	if req.Parallel < 0 || req.Parallel > 32 {
		writeError(w, http.StatusUnprocessableEntity, "bad_request", "parallel must be 1..32")
		return
	}
	if _, err := s.o.Backend.Config(); err != nil {
		writeError(w, http.StatusInternalServerError, "config_invalid", err.Error())
		return
	}
	j, err := s.jobs.start("lan-test", func(ctx context.Context) ([]any, error) {
		rec, err := s.o.Backend.RunLAN(ctx, req)
		if err != nil {
			return nil, err
		}
		return []any{rec}, nil
	})
	if err != nil {
		writeBusy(w, err)
		return
	}
	s.accepted(w, j)
}

// POST /api/v1/lan-server {"action":"start"|"stop"}
func (s *Server) apiLANServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if s.o.LANServer == nil {
		writeError(w, http.StatusConflict, "lan_server_unavailable", "the iperf3 server is not available in this daemon")
		return
	}
	switch req.Action {
	case "start":
		c, err := s.o.Backend.Config()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "config_invalid", err.Error())
			return
		}
		if c.Get("LAN_SERVER_ENABLE") != "1" {
			writeError(w, http.StatusConflict, "lan_server_disabled", "LAN_SERVER_ENABLE=0: the iperf3 server is switched off in the configuration")
			return
		}
		s.o.LANServer.Start()
	case "stop":
		s.o.LANServer.Stop()
	default:
		writeError(w, http.StatusUnprocessableEntity, "bad_request", `action must be "start" or "stop"`)
		return
	}
	writeJSON(w, http.StatusOK, s.o.LANServer.Status())
}

type issueJSON struct {
	Severity string `json:"severity"`
	Key      string `json:"key,omitempty"`
	Message  string `json:"message"`
}

func issuesJSON(is []config.Issue) []issueJSON {
	out := make([]issueJSON, 0, len(is))
	for _, i := range is {
		out = append(out, issueJSON{i.Severity.String(), i.Key, i.Msg})
	}
	return out
}

// GET /api/v1/config
func (s *Server) apiConfigGet(w http.ResponseWriter, r *http.Request) {
	c, err := s.o.Backend.Config()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "config_invalid", err.Error())
		return
	}
	type keyJSON struct {
		Name            string `json:"name"`
		Value           string `json:"value"`
		Default         string `json:"default"`
		RestartRequired bool   `json:"restart_required"`
	}
	keys := []keyJSON{}
	for _, k := range config.KeyList() {
		keys = append(keys, keyJSON{k.Name, c.Get(k.Name), k.Default, config.RestartRequired(k.Name)})
	}
	writeJSON(w, http.StatusOK, struct {
		Keys   []keyJSON   `json:"keys"`
		Issues []issueJSON `json:"issues"`
	}{keys, issuesJSON(c.Validate())})
}

// PUT /api/v1/config {"values":{"KEY":"value",...}}: all or nothing.
func (s *Server) apiConfigPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Values map[string]string `json:"values"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if len(req.Values) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "bad_request", `send {"values": {"KEY": "value", ...}}`)
		return
	}
	res, err := s.o.Backend.SaveConfig(req.Values)
	var fe FieldErrors
	switch {
	case errors.As(err, &fe):
		writeJSON(w, http.StatusUnprocessableEntity, struct {
			apiError
			Fields map[string]string `json:"fields"`
		}{apiError{"invalid_config", "nothing was saved"}, fe})
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "config_failed", err.Error())
		return
	}
	sort.Strings(res.Saved)
	sort.Strings(res.RestartRequired)
	writeJSON(w, http.StatusOK, struct {
		Saved           []string    `json:"saved"`
		RestartRequired []string    `json:"restart_required"`
		Issues          []issueJSON `json:"issues"`
	}{nonNil(res.Saved), nonNil(res.RestartRequired), issuesJSON(res.Issues)})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
