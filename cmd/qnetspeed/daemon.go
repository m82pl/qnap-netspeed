package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/web"
)

// daemon runs the web server until SIGTERM/SIGINT (spec pkt 3.1).
// stdout/stderr go to data/qnetspeed.log via the service script.
func (a *app) daemon() int {
	logger := log.New(a.stderr, "qnetspeed daemon: ", log.LstdFlags)
	c, issues, code := a.loadConfig(false)
	if code != exitOK {
		a.printIssues(issues)
		logger.Print("invalid configuration, not starting")
		return code
	}
	addr := net.JoinHostPort(c.Get("WEB_BIND"), c.Get("WEB_PORT"))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		logger.Printf("listen %s: %v", addr, err)
		return exitTestError
	}
	signal.Ignore(syscall.SIGHUP)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	info := web.Info{
		Version:  version,
		Arch:     runtime.GOOS + "/" + runtime.GOARCH,
		Lang:     c.Get("LANG"),
		Started:  time.Now(),
		LockFile: a.layout.LockFile(),
	}
	logger.Printf("version %s, pid %d, listening on %s", version, os.Getpid(), ln.Addr())
	if err := serve(ctx, ln, web.Handler(info)); err != nil {
		logger.Printf("server: %v", err)
		return exitTestError
	}
	logger.Print("stopped")
	return exitOK
}

// serve runs the HTTP server on ln until ctx is done, then shuts down gracefully.
func serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := web.NewServer(ln.Addr().String(), h)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		srv.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
