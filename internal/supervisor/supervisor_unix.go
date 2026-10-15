//go:build !windows

package supervisor

import (
	"os/exec"
	"syscall"
	"time"
)

func platformSysProcAttr() *syscall.SysProcAttr {
	// New process group so Stop can signal the whole tree (e.g. a
	// `bash script.sh` wrapper and whatever it spawns), not just the
	// direct child.
	return &syscall.SysProcAttr{Setpgid: true}
}

func stopProcess(cmd *exec.Cmd, grace time.Duration) error {
	if cmd.Process == nil {
		return nil
	}

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		pgid = cmd.Process.Pid
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-time.After(grace):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return <-done
	}
}
