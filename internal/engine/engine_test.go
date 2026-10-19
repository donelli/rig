package engine

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/eduardopellenz/rig/internal/bus"
	"github.com/eduardopellenz/rig/internal/config"
)

// trivialOKCmd is a command that exits 0 almost instantly, used both as a
// one-shot service and as a readiness exec probe.
func trivialOKCmd() string {
	if runtime.GOOS == "windows" {
		return "cmd /c exit 0"
	}
	return "true"
}

func trivialOKArgv() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", "exit", "0"}
	}
	return []string{"true"}
}

// longRunningCmd runs until killed — stands in for a real serve_cmd.
func longRunningCmd() string {
	if runtime.GOOS == "windows" {
		return "ping -t 127.0.0.1"
	}
	return "sleep 60"
}

// TestDependsOnWaitsForReadiness builds a two-service graph — "a" (serve_cmd
// with a readiness probe gated behind a 1.2s initial_delay) and "b"
// (one-shot, depends_on: [a]) — and asserts "b" does not start until "a"
// reports healthy, i.e. depends_on actually gates on readiness, not just on
// process start.
func TestDependsOnWaitsForReadiness(t *testing.T) {
	initialDelay := 1200 * time.Millisecond

	cfg := &config.Config{
		Settings: config.Settings{MaxParallelUpdates: 5},
		Services: []config.Service{
			{
				Name:     "a",
				ServeCmd: longRunningCmd(),
				Readiness: &config.Readiness{
					Exec:             &config.ExecProbe{Cmd: trivialOKArgv()},
					InitialDelay:     config.Duration(initialDelay),
					Period:           config.Duration(200 * time.Millisecond),
					Timeout:          config.Duration(500 * time.Millisecond),
					SuccessThreshold: 1,
				},
			},
			{
				Name:      "b",
				Cmd:       trivialOKCmd(),
				DependsOn: []string{"a"},
			},
		},
	}

	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ch, unsubscribe := e.Bus().Subscribe()
	defer unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = e.Up(ctx)
	}()

	start := time.Now()
	var aHealthyAt, bStartingAt time.Time

	for aHealthyAt.IsZero() || bStartingAt.IsZero() {
		select {
		case ev := <-ch:
			if ev.Type != bus.EventStatus {
				continue
			}
			now := time.Now()
			switch {
			case ev.Service == "a" && ev.Status == string(StatusHealthy) && aHealthyAt.IsZero():
				aHealthyAt = now
			case ev.Service == "b" && ev.Status == string(StatusStarting) && bStartingAt.IsZero():
				bStartingAt = now
			case ev.Service == "b" && ev.Status == string(StatusBlocked):
				t.Fatalf("service b was blocked — dependency 'a' was treated as failed")
			}
		case <-time.After(6 * time.Second):
			t.Fatalf("timed out waiting for events: aHealthyAt=%v bStartingAt=%v", aHealthyAt, bStartingAt)
		}
	}

	cancel()
	wg.Wait()

	sinceStartA := aHealthyAt.Sub(start)
	sinceStartB := bStartingAt.Sub(start)
	t.Logf("a healthy at %s, b starting at %s (initial_delay=%s)", sinceStartA, sinceStartB, initialDelay)

	if bStartingAt.Before(aHealthyAt) {
		t.Fatalf("b started (%s) before a became healthy (%s) — depends_on did not gate on readiness", sinceStartB, sinceStartA)
	}
	if sinceStartB < initialDelay {
		t.Fatalf("b started after only %s, before a's initial_delay (%s) could have elapsed — b did not actually wait on a", sinceStartB, initialDelay)
	}
}
