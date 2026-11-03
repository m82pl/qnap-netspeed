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
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/iperfsrv"
	"github.com/m82pl/qnap-netspeed/internal/web"
)

// lanServer lets the API start and stop the supervised iperf3 under the daemon's lifetime.
type lanServer struct {
	*iperfsrv.Supervisor
	ctx context.Context
}

func (l lanServer) Start() { l.Supervisor.Start(l.ctx) }

// daemon runs the web server and the iperf3 server supervisor until
// SIGTERM/SIGINT (spec pkt 3.1). stdout/stderr go to data/qnetspeed.log via
// the service script.
func (a *app) daemon() int {
	logger := log.New(a.stderr, "qnetspeed daemon: ", log.LstdFlags)
	c, issues, code := a.loadConfig(false)
	if code != exitOK {
		a.printIssues(issues)
		logger.Print("invalid configuration, not starting")
		return code
	}
	if err := os.MkdirAll(a.layout.DataDir(), 0o755); err != nil {
		logger.Printf("data directory: %v", err)
		return exitTestError
	}
	token, err := web.LoadOrCreateToken(a.layout.TokenFile())
	if err != nil {
		logger.Printf("administrator token: %v", err)
		return exitTestError
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

	sup := iperfsrv.New(iperfsrv.Options{
		Bin:  filepath.Join(a.binDir, "iperf3"),
		Port: c.Int("IPERF_PORT"),
		Bind: c.Get("LAN_BIND"),
		Logf: logger.Printf,
	})
	srv := web.New(web.Options{
		Info: web.Info{
			Version:  version,
			Arch:     runtime.GOOS + "/" + runtime.GOARCH,
			Lang:     c.Get("LANG"),
			Started:  time.Now(),
			LockFile: a.layout.LockFile(),
		},
		Backend:   &backend{a: a},
		Token:     token,
		LANServer: lanServer{sup, ctx},
		Context:   ctx,
		Logf:      logger.Printf,
	})
	logger.Printf("version %s, pid %d, listening on %s", version, os.Getpid(), ln.Addr())
	logger.Printf("administrator token: %s (qnetspeed web-token prints it)", a.layout.TokenFile())
	if c.Get("LAN_SERVER_ENABLE") == "1" {
		sup.Start(ctx)
	} else {
		logger.Print("iperf3 server is off (LAN_SERVER_ENABLE=0)")
	}

	serveErr := serve(ctx, ln, srv.Handler())

	// Stop the iperf3 server and running tests together; both take a few seconds at most.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); sup.Stop() }()
	go func() { defer wg.Done(); srv.Close() }()
	wg.Wait()
	if serveErr != nil {
		logger.Printf("server: %v", serveErr)
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
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		// Streams still open after the grace period (a speed test in a browser) are cut off.
		srv.Close()
		if !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("shutdown: %w", err)
		}
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
