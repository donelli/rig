// Package supervisor starts and stops subprocesses. Asking a process to
// stop gracefully vs. force-killing it is OS-specific (see
// supervisor_unix.go / supervisor_windows.go); everything else — including
// the actual wait-for-exit logic — is shared here.
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

	// cmd.Wait() may only ever be called once, and never concurrently with
	// itself (see exec.Cmd docs). A single goroutine, started in Start,
	// owns the real call; Wait and Stop both just read from done/err.
	done chan struct{}
	err  error
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

	p := &Process{Name: opts.Name, cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()

	go streamLines(opts.Name, "stdout", stdout, opts.Bus)
	go streamLines(opts.Name, "stderr", stderr, opts.Bus)

	return p, nil
}

// Wait blocks until the process exits and returns its error (nil on exit
// 0). Safe to call any number of times, from any number of goroutines.
func (p *Process) Wait() error {
	<-p.done
	return p.err
}

func (p *Process) Pid() int {
	if p.cmd.Process == nil {
		return -1
	}
	return p.cmd.Process.Pid
}

// Stop asks the process (group) to shut down gracefully and escalates to a
// hard kill if it hasn't exited within grace. Safe to call concurrently
// with itself or with Wait.
func (p *Process) Stop(grace time.Duration) error {
	if p.cmd.Process == nil {
		return nil
	}

	signalGraceful(p.cmd)

	select {
	case <-p.done:
		return p.err
	case <-time.After(grace):
		_ = hardKill(p.cmd)
		<-p.done
		return p.err
	}
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
