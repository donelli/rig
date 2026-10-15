// Package engine schedules services according to their dependency graph:
// waiting on depends_on, gating manual (auto_init: false) services behind a
// trigger, limiting concurrent startups to max_parallel_updates, running
// readiness probes, and supervising long-running processes.
package engine

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eduardopellenz/rig/internal/bus"
	"github.com/eduardopellenz/rig/internal/config"
	"github.com/eduardopellenz/rig/internal/graph"
	"github.com/eduardopellenz/rig/internal/probe"
	"github.com/eduardopellenz/rig/internal/resource"
	"github.com/eduardopellenz/rig/internal/supervisor"
)

const defaultStopGrace = 5 * time.Second

type Engine struct {
	cfg      *config.Config
	graph    *graph.Graph
	services map[string]config.Service
	bus      *bus.Bus
	sem      semaphore

	mu         sync.RWMutex
	statuses   map[string]Status
	succeeded  map[string]bool
	settled    map[string]chan struct{}
	triggers   map[string]chan struct{}
	triggerOne map[string]*sync.Once
	processes  map[string]*supervisor.Process

	shuttingDown atomic.Bool
}

func New(cfg *config.Config) (*Engine, error) {
	nodes := make([]graph.Node, 0, len(cfg.Services))
	services := make(map[string]config.Service, len(cfg.Services))
	for _, s := range cfg.Services {
		nodes = append(nodes, graph.Node{Name: s.Name, DependsOn: s.DependsOn})
		services[s.Name] = s
	}

	g, err := graph.Build(nodes)
	if err != nil {
		return nil, fmt.Errorf("building dependency graph: %w", err)
	}

	e := &Engine{
		cfg:        cfg,
		graph:      g,
		services:   services,
		bus:        bus.New(),
		sem:        newSemaphore(cfg.Settings.MaxParallelUpdates),
		statuses:   make(map[string]Status, len(services)),
		succeeded:  make(map[string]bool, len(services)),
		settled:    make(map[string]chan struct{}, len(services)),
		triggers:   make(map[string]chan struct{}, len(services)),
		triggerOne: make(map[string]*sync.Once, len(services)),
		processes:  make(map[string]*supervisor.Process, len(services)),
	}

	for name := range services {
		e.statuses[name] = StatusPending
		e.settled[name] = make(chan struct{})
		e.triggers[name] = make(chan struct{})
		e.triggerOne[name] = &sync.Once{}
	}

	return e, nil
}

func (e *Engine) Bus() *bus.Bus { return e.bus }

// Trigger fires a manual (auto_init: false) service that is waiting to
// start. Safe to call multiple times; only the first call has an effect.
func (e *Engine) Trigger(name string) error {
	e.mu.RLock()
	once, ok := e.triggerOne[name]
	ch := e.triggers[name]
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown service %q", name)
	}
	once.Do(func() { close(ch) })
	return nil
}

// Status returns a point-in-time snapshot of every service, in dependency
// order.
func (e *Engine) Status() []ServiceStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]ServiceStatus, 0, len(e.services))
	for _, name := range e.graph.Order() {
		svc := e.services[name]
		pid := -1
		if p, ok := e.processes[name]; ok {
			pid = p.Pid()
		}
		out = append(out, ServiceStatus{
			Name:   name,
			Status: e.statuses[name],
			Labels: svc.Labels,
			PID:    pid,
		})
	}
	return out
}

// Up runs every service to completion (one-shot) or until healthy (serve),
// then blocks until ctx is cancelled, at which point it stops all running
// processes and returns.
func (e *Engine) Up(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, name := range e.graph.Order() {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.runService(ctx, &wg, name)
		}()
	}

	<-ctx.Done()
	e.shuttingDown.Store(true)
	e.stopAll()
	wg.Wait()
	return ctx.Err()
}

func (e *Engine) runService(ctx context.Context, wg *sync.WaitGroup, name string) {
	svc := e.services[name]
	success := false
	defer func() {
		e.mu.Lock()
		e.succeeded[name] = success
		ch := e.settled[name]
		e.mu.Unlock()
		close(ch)
	}()

	for _, dep := range svc.DependsOn {
		select {
		case <-e.settledCh(dep):
		case <-ctx.Done():
			return
		}
		if !e.didSucceed(dep) {
			e.setStatus(name, StatusBlocked)
			return
		}
	}

	if !svc.AutoInitEnabled() {
		e.setStatus(name, StatusManual)
		select {
		case <-e.triggerCh(name):
		case <-ctx.Done():
			return
		}
	}

	if err := e.sem.Acquire(ctx); err != nil {
		return
	}
	var semOnce sync.Once
	releaseSem := func() { semOnce.Do(e.sem.Release) }
	defer releaseSem()

	e.setStatus(name, StatusStarting)

	argv, err := resource.SplitArgv(svc.Command())
	if err != nil {
		e.failf(name, "invalid command: %v", err)
		return
	}

	proc, err := supervisor.Start(supervisor.StartOptions{
		Name: name,
		Argv: argv,
		Dir:  svc.Dir,
		Env:  mergeEnv(svc.Env),
		Bus:  e.bus,
	})
	if err != nil {
		e.failf(name, "failed to start: %v", err)
		return
	}

	e.mu.Lock()
	e.processes[name] = proc
	e.mu.Unlock()

	if svc.IsServe() {
		if svc.Readiness != nil {
			err := probe.Wait(ctx, readinessSpec(svc.Dir, *svc.Readiness))
			releaseSem()
			if err != nil {
				e.setStatus(name, StatusFailed)
				return
			}
		} else {
			releaseSem()
		}
		e.setStatus(name, StatusHealthy)
		success = true

		wg.Add(1)
		go func() {
			defer wg.Done()
			e.watchServe(name, proc)
		}()
		return
	}

	// One-shot: the semaphore is held for the whole run, since the command
	// itself is the unit of parallel-limited work.
	if err := proc.Wait(); err != nil {
		e.failf(name, "exited with error: %v", err)
		return
	}
	e.setStatus(name, StatusHealthy)
	success = true
}

func (e *Engine) watchServe(name string, proc *supervisor.Process) {
	_ = proc.Wait()
	if e.shuttingDown.Load() {
		e.setStatus(name, StatusStopped)
		return
	}
	// Crashed after having been healthy. No restart policy in v1 — surface
	// it and let the operator decide (restart via CLI/TUI).
	e.setStatus(name, StatusFailed)
}

func (e *Engine) stopAll() {
	e.mu.RLock()
	procs := make(map[string]*supervisor.Process, len(e.processes))
	for k, v := range e.processes {
		procs[k] = v
	}
	e.mu.RUnlock()

	var wg sync.WaitGroup
	for name, proc := range procs {
		name, proc := name, proc
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = proc.Stop(defaultStopGrace)
			_ = name
		}()
	}
	wg.Wait()
}

func (e *Engine) settledCh(name string) chan struct{} {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.settled[name]
}

func (e *Engine) triggerCh(name string) chan struct{} {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.triggers[name]
}

func (e *Engine) didSucceed(name string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.succeeded[name]
}

func (e *Engine) setStatus(name string, status Status) {
	e.mu.Lock()
	e.statuses[name] = status
	e.mu.Unlock()
	e.bus.Publish(bus.Event{Type: bus.EventStatus, Service: name, Status: string(status)})
}

func (e *Engine) failf(name, format string, args ...any) {
	e.setStatus(name, StatusFailed)
	e.bus.Publish(bus.Event{
		Type:    bus.EventLog,
		Service: name,
		Stream:  "rig",
		Line:    fmt.Sprintf(format, args...),
	})
}

func readinessSpec(dir string, r config.Readiness) probe.Spec {
	var checker probe.Checker
	switch {
	case r.HTTP != nil:
		checker = probe.HTTP{Port: r.HTTP.Port, Path: r.HTTP.Path}
	case r.TCP != nil:
		checker = probe.TCP{Port: r.TCP.Port}
	case r.Exec != nil:
		checker = probe.Exec{Cmd: r.Exec.Cmd, Dir: dir}
	default:
		checker = probe.TCP{Port: 0} // will simply fail fast if misconfigured
	}
	return probe.Spec{
		Checker:          checker,
		InitialDelay:     r.InitialDelay.Duration(),
		Period:           r.Period.Duration(),
		Timeout:          r.Timeout.Duration(),
		SuccessThreshold: r.SuccessThreshold,
	}
}

func mergeEnv(overrides map[string]string) []string {
	env := os.Environ()
	for k, v := range overrides {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env
}
