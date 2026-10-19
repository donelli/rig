//go:build !windows

package supervisor

import (
	"os/exec"
	"syscall"
)

func platformSysProcAttr() *syscall.SysProcAttr {
	// New process group so signalGraceful/hardKill can target the whole
	// tree (e.g. a `bash script.sh` wrapper and whatever it spawns), not
	// just the direct child.
	return &syscall.SysProcAttr{Setpgid: true}
}

func pgidOf(cmd *exec.Cmd) int {
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return cmd.Process.Pid
	}
	return pgid
}

func signalGraceful(cmd *exec.Cmd) {
	_ = syscall.Kill(-pgidOf(cmd), syscall.SIGTERM)
}

func hardKill(cmd *exec.Cmd) error {
	return syscall.Kill(-pgidOf(cmd), syscall.SIGKILL)
}
