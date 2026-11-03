package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/config"
	"github.com/m82pl/qnap-netspeed/internal/iperfsrv"
	"github.com/m82pl/qnap-netspeed/internal/web"
)

const lanServerUsage = "Usage: qnetspeed lan-server start|stop|status [--json]\n"

// lanServerCmd controls the iperf3 server through the daemon (spec pkt 5.2.1):
// one owner of the process, no races over a PID file.
func (a *app) lanServerCmd(args []string) int {
	if len(args) < 1 || len(args) > 2 || (len(args) == 2 && args[1] != "--json") {
		fmt.Fprint(a.stderr, lanServerUsage)
		return exitUsage
	}
	asJSON := len(args) == 2
	action := args[0]
	if action != "start" && action != "stop" && action != "status" {
		fmt.Fprint(a.stderr, lanServerUsage)
		return exitUsage
	}
	c, issues, code := a.loadConfig(false)
	if code != exitOK {
		a.printIssues(issues)
		return code
	}
	var st iperfsrv.Status
	if action == "status" {
		// The status needs no token unless WEB_PUBLIC_READ=0; send it when we may read it.
		body, status, err := a.daemonCall(c, http.MethodGet, "/api/v1/status", nil)
		if err != nil {
			return a.daemonDown(c, err)
		}
		if status != http.StatusOK {
			return a.daemonRefused(c, status, body)
		}
		var s web.Status
		if json.Unmarshal(body, &s) != nil {
			fmt.Fprintln(a.stderr, "qnetspeed lan-server: unexpected answer from the daemon")
			return exitTestError
		}
		st = s.LANServer
	} else {
		if !a.needRoot(c) {
			return exitTestError
		}
		body, status, err := a.daemonCall(c, http.MethodPost, "/api/v1/lan-server", map[string]string{"action": action})
		if err != nil {
			return a.daemonDown(c, err)
		}
		if status != http.StatusOK {
			return a.daemonRefused(c, status, body)
		}
		if json.Unmarshal(body, &st) != nil {
			fmt.Fprintln(a.stderr, "qnetspeed lan-server: unexpected answer from the daemon")
			return exitTestError
		}
	}
	if asJSON {
		b, _ := json.Marshal(st)
		fmt.Fprintf(a.stdout, "%s\n", b)
	} else {
		fmt.Fprintln(a.stdout, lanServerText(c, st))
	}
	if action == "status" && st.State != iperfsrv.StateRunning {
		return exitTestError
	}
	return exitOK
}

func lanServerText(c *config.Config, st iperfsrv.Status) string {
	where := fmt.Sprintf("%d", st.Port)
	if st.Bind != "" {
		where = net.JoinHostPort(st.Bind, where)
	}
	switch st.State {
	case iperfsrv.StateRunning:
		return fmt.Sprintf(tr(c, "iperf3 server: running (pid %d, port %s)", "serwer iperf3: działa (pid %d, port %s)"), st.PID, where)
	case iperfsrv.StateStopped:
		return tr(c, "iperf3 server: stopped", "serwer iperf3: zatrzymany")
	case iperfsrv.StateStarting:
		return tr(c, "iperf3 server: starting", "serwer iperf3: uruchamia się")
	case iperfsrv.StatePortBusy:
		return fmt.Sprintf(tr(c, "iperf3 server: not started, %s", "serwer iperf3: nie uruchomiony, %s"), st.LastError)
	}
	return fmt.Sprintf(tr(c, "iperf3 server: %s (restarts: %d), last error: %s", "serwer iperf3: %s (restarty: %d), ostatni błąd: %s"),
		st.State, st.Restarts, st.LastError)
}

// daemonAddr is where the daemon listens, as seen from this machine.
func daemonAddr(c *config.Config) string {
	host := c.Get("WEB_BIND")
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, c.Get("WEB_PORT"))
}

// readToken returns the administrator token, or "" when this user may not read it.
func (a *app) readToken() string {
	b, err := os.ReadFile(a.layout.TokenFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// daemonCall calls the local daemon with the administrator token (when readable).
func (a *app) daemonCall(c *config.Config, method, path string, body any) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://"+daemonAddr(c)+path, rd)
	if err != nil {
		return nil, 0, err
	}
	if tok := a.readToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return b, resp.StatusCode, err
}

func (a *app) daemonDown(c *config.Config, err error) int {
	fmt.Fprintf(a.stderr, tr(c, "qnetspeed: the daemon does not answer on %s (is the qnap-netspeed service running?): %v\n",
		"qnetspeed: demon nie odpowiada pod %s (czy usługa qnap-netspeed działa?): %v\n"), daemonAddr(c), err)
	return exitTestError
}

func (a *app) daemonRefused(c *config.Config, status int, body []byte) int {
	var e struct{ Error, Message string }
	json.Unmarshal(body, &e)
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(status)
	}
	fmt.Fprintf(a.stderr, "qnetspeed: %s (HTTP %d)\n", msg, status)
	return exitTestError
}

const webTokenUsage = "Usage: qnetspeed web-token\n"

// webToken prints the administrator token of the web interface (spec pkt 14.2).
func (a *app) webToken(args []string) int {
	if len(args) != 0 {
		fmt.Fprint(a.stderr, webTokenUsage)
		return exitUsage
	}
	c, _, _ := a.loadConfig(false)
	b, err := os.ReadFile(a.layout.TokenFile())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fmt.Fprintln(a.stderr, tr(c, "qnetspeed: no token yet; it is created when the service starts for the first time",
			"qnetspeed: brak tokenu; powstaje przy pierwszym starcie usługi"))
		return exitTestError
	case errors.Is(err, fs.ErrPermission):
		fmt.Fprintln(a.stderr, tr(c, "qnetspeed: the token is readable by root only (use sudo or log in as admin)",
			"qnetspeed: token może przeczytać tylko root (użyj sudo albo zaloguj się jako admin)"))
		return exitTestError
	case err != nil:
		fmt.Fprintf(a.stderr, "qnetspeed: %v\n", err)
		return exitTestError
	}
	fmt.Fprintln(a.stdout, strings.TrimSpace(string(b)))
	return exitOK
}
