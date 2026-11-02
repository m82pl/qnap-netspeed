package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// version prints qnetspeed's version and those of the bundled engines,
// which live next to the binary ($QPKG_ROOT/bin).
func (a *app) version() int {
	fmt.Fprintf(a.stdout, "qnetspeed %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
	fmt.Fprintf(a.stdout, "librespeed-cli: %s\n", toolVersion(filepath.Join(a.binDir, "librespeed-cli"), "--version"))
	fmt.Fprintf(a.stdout, "iperf3: %s\n", toolVersion(filepath.Join(a.binDir, "iperf3"), "--version"))
	return exitOK
}

// toolVersion runs `path arg` and returns the first non-empty output line.
func toolVersion(path string, arg string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, arg).CombinedOutput()
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			return l
		}
	}
	if err != nil {
		return "not available (" + err.Error() + ")"
	}
	return "unknown"
}
