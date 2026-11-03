package iperfsrv

import "syscall"

// sysProcAttr puts iperf3 in its own process group (so SIGTERM reaches
// everything it started) and makes the kernel kill it if the daemon dies
// without cleaning up (kill -9, OOM): an orphan would keep the port busy.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
