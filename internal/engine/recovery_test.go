package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/eduardopellenz/rig/internal/config"
)

// TestServeExitingBeforeReadyFails checks that a serve_cmd whose process
// exits before its readiness probe passes is reported as failed, instead of
// staying "starting" while the probe polls a port nothing will ever open.
func TestServeExitingBeforeReadyFails(t *testing.T) {
	cfg := &config.Config{
		Settings: config.Settings{MaxParallelUpdates: 5},
		Services: []config.Service{
			{
				Name:     "a",
				ServeCmd: failingCmd(),
				Readiness: &config.Readiness{
					TCP:    &config.TCPProbe{Port: 1},
					Period: config.Duration(10 * time.Second),
				},
			},
		},
	}

	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, unsubscribe := e.Bus().Subscribe()
	defer unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = e.Up(ctx)
	}()

	// Well under the 10s probe period: the exit has to cut the wait short
	waitForStatus(t, ch, "a", StatusFailed, 3*time.Second)

	cancel()
	wg.Wait()
}

// TestBlockedDependentStartsAfterRecovery fails a dependency, then fixes and
// restarts it, and checks that the blocked dependent starts on its own.
func TestBlockedDependentStartsAfterRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	ready := filepath.Join(t.TempDir(), "ready")

	cfg := &config.Config{
		Settings: config.Settings{MaxParallelUpdates: 5},
		Services: []config.Service{
			{
				Name:     "a",
				ServeCmd: `sh -c "test -f ` + ready + ` && exec sleep 60"`,
				Readiness: &config.Readiness{
					Exec:         &config.ExecProbe{Cmd: trivialOKArgv()},
					InitialDelay: config.Duration(300 * time.Millisecond),
					Period:       config.Duration(100 * time.Millisecond),
				},
			},
			{Name: "b", Cmd: trivialOKCmd(), DependsOn: []string{"a"}},
		},
	}

	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, unsubscribe := e.Bus().Subscribe()
	defer unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = e.Up(ctx)
	}()

	waitForStatus(t, ch, "b", StatusBlocked, 5*time.Second)

	if err := os.WriteFile(ready, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.Restart("a"); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	waitForStatus(t, ch, "b", StatusHealthy, 5*time.Second)

	cancel()
	wg.Wait()
}
