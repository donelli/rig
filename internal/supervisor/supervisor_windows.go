//go:build windows

package supervisor

import (
	"os/exec"
	"syscall"
	"time"
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procGenerateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
)

const ctrlBreakEvent = 1

func platformSysProcAttr() *syscall.SysProcAttr {
	// A new process group lets us target CTRL_BREAK_EVENT at this process
	// (and whatever it spawns) without also hitting rig itself. On
	// Windows the new group's ID equals the child's PID.
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func stopProcess(cmd *exec.Cmd, grace time.Duration) error {
	if cmd.Process == nil {
		return nil
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Best-effort graceful stop. Some processes ignore CTRL_BREAK_EVENT or
	// aren't attached to a console at all (the call then simply fails) —
	// either way we still fall back to a hard kill after grace.
	_, _, _ = procGenerateConsoleCtrlEvent.Call(uintptr(ctrlBreakEvent), uintptr(cmd.Process.Pid))

	select {
	case err := <-done:
		return err
	case <-time.After(grace):
		_ = cmd.Process.Kill()
		return <-done
	}
}
