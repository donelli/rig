// Package supervisor starts and stops subprocesses. Process-group creation
// and graceful shutdown are OS-specific (see supervisor_unix.go /
// supervisor_windows.go); everything else is shared here.
package supervisor

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/eduardopellenz/rig/internal/bus"
)

type Process struct {
	Name string
	cmd  *exec.Cmd
}

type StartOptions struct {
	Name string
	Argv []string
	Dir  string
	Env  []string
	Bus  *bus.Bus
}

// Start launches the process in its own process group (so Stop can signal
// the whole tree, not just the direct child — important for `bash script.sh`
// wrappers that spawn further children).
func Start(opts StartOptions) (*Process, error) {
	if len(opts.Argv) == 0 {
		return nil, fmt.Errorf("empty command for service %q", opts.Name)
	}

	cmd := exec.Command(opts.Argv[0], opts.Argv[1:]...)
	cmd.Dir = opts.Dir
	cmd.Env = opts.Env
	cmd.SysProcAttr = platformSysProcAttr()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	go streamLines(opts.Name, "stdout", stdout, opts.Bus)
	go streamLines(opts.Name, "stderr", stderr, opts.Bus)

	return &Process{Name: opts.Name, cmd: cmd}, nil
}

// Wait blocks until the process exits and returns its error (nil on exit 0).
func (p *Process) Wait() error {
	return p.cmd.Wait()
}

func (p *Process) Pid() int {
	if p.cmd.Process == nil {
		return -1
	}
	return p.cmd.Process.Pid
}

// Stop asks the process (group) to shut down gracefully and escalates to a
// hard kill if it hasn't exited within grace. Implemented per-OS.
func (p *Process) Stop(grace time.Duration) error {
	return stopProcess(p.cmd, grace)
}

func streamLines(name, stream string, r io.Reader, b *bus.Bus) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if b != nil {
			b.Publish(bus.Event{
				Type:    bus.EventLog,
				Service: name,
				Stream:  stream,
				Line:    scanner.Text(),
			})
		}
	}
}
