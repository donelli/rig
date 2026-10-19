//go:build windows

package supervisor

import (
	"os/exec"
	"syscall"
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

// signalGraceful is best-effort. Some processes ignore CTRL_BREAK_EVENT or
// aren't attached to a console at all (the call then simply fails) — either
// way the caller still falls back to hardKill after the grace period.
func signalGraceful(cmd *exec.Cmd) {
	_, _, _ = procGenerateConsoleCtrlEvent.Call(uintptr(ctrlBreakEvent), uintptr(cmd.Process.Pid))
}

func hardKill(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
