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

// failingCmd exits non-zero almost instantly.
func failingCmd() string {
	if runtime.GOOS == "windows" {
		return "cmd /c exit 1"
	}
	return "false"
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

// TestRestartReplacesProcessWithoutFalseFailure starts one serve_cmd
// service, lets it go healthy, restarts it, and checks two things: the
// running process actually got replaced (different PID), and the
// intentional stop-for-restart never surfaces as a "failed" status (which
// would happen if the old watcher's exit detection didn't know the stop was
// expected).
func TestRestartReplacesProcessWithoutFalseFailure(t *testing.T) {
	cfg := &config.Config{
		Settings: config.Settings{MaxParallelUpdates: 5},
		Services: []config.Service{
			{Name: "a", ServeCmd: longRunningCmd()},
		},
	}

	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ch, unsubscribe := e.Bus().Subscribe()
	defer unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = e.Up(ctx)
	}()

	waitForStatus(t, ch, "a", StatusHealthy, 5*time.Second)

	oldPID := pidOf(t, e.Status(), "a")
	if oldPID <= 0 {
		t.Fatalf("expected a positive PID for a healthy service, got %d", oldPID)
	}

	if err := e.Restart("a"); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	// Drain whatever Restart's synchronous stop+start already published and
	// make sure none of it was a spurious "failed".
	draining := true
	for draining {
		select {
		case ev := <-ch:
			if ev.Type == bus.EventStatus && ev.Service == "a" && ev.Status == string(StatusFailed) {
				t.Fatalf("got a spurious 'failed' status from the intentional stop during Restart")
			}
		case <-time.After(1 * time.Second):
			draining = false
		}
	}

	newPID := pidOf(t, e.Status(), "a")
	if newPID <= 0 {
		t.Fatalf("expected a positive PID after restart, got %d", newPID)
	}
	if newPID == oldPID {
		t.Fatalf("PID did not change after Restart (old=%d new=%d) — process was not actually replaced", oldPID, newPID)
	}
	t.Logf("restarted: pid %d -> %d", oldPID, newPID)

	cancel()
	wg.Wait()
}

// TestStopThenResume starts a serve_cmd service, stops it (asserting it
// lands in "stopped" with no process left), then resumes it via Restart
// (the documented way to resume) and asserts it comes back healthy with a
// new PID.
func TestStopThenResume(t *testing.T) {
	cfg := &config.Config{
		Settings: config.Settings{MaxParallelUpdates: 5},
		Services: []config.Service{
			{Name: "a", ServeCmd: longRunningCmd()},
		},
	}

	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ch, unsubscribe := e.Bus().Subscribe()
	defer unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = e.Up(ctx)
	}()

	waitForStatus(t, ch, "a", StatusHealthy, 5*time.Second)
	oldPID := pidOf(t, e.Status(), "a")

	if err := e.Stop("a"); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	stopped := e.Status()
	if got := statusOf(t, stopped, "a"); got != StatusStopped {
		t.Fatalf("expected status %q after Stop, got %q", StatusStopped, got)
	}
	if pid := pidOf(t, stopped, "a"); pid > 0 {
		t.Fatalf("expected no PID after Stop, got %d", pid)
	}

	// Stopping an already-stopped service should be rejected, not silently
	// a no-op that could race a second Stop against nothing.
	if err := e.Stop("a"); err == nil {
		t.Fatalf("expected Stop on an already-stopped service to error")
	}

	if err := e.Restart("a"); err != nil {
		t.Fatalf("Restart (resume): %v", err)
	}

	resumed := e.Status()
	if got := statusOf(t, resumed, "a"); got != StatusHealthy {
		t.Fatalf("expected status %q after resume, got %q", StatusHealthy, got)
	}
	newPID := pidOf(t, resumed, "a")
	if newPID <= 0 {
		t.Fatalf("expected a positive PID after resume, got %d", newPID)
	}
	if newPID == oldPID {
		t.Fatalf("PID did not change after stop+resume (old=%d new=%d)", oldPID, newPID)
	}
	t.Logf("stop+resume: pid %d -> %d", oldPID, newPID)

	cancel()
	wg.Wait()
}

// TestRestartRejectsNotYetStarted builds a graph where "b" depends_on a
// service that fails, so "b" ends up permanently blocked — and asserts
// Restart refuses to force-start it. Allowing that would race with the
// scheduler's own goroutine, which is still waiting on the (never
// satisfied) dependency and could start the same service a second time
// later.
func TestRestartRejectsNotYetStarted(t *testing.T) {
	cfg := &config.Config{
		Settings: config.Settings{MaxParallelUpdates: 5},
		Services: []config.Service{
			{Name: "will-fail", Cmd: failingCmd()},
			{Name: "b", Cmd: trivialOKCmd(), DependsOn: []string{"will-fail"}},
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

	waitForStatus(t, ch, "b", StatusBlocked, 5*time.Second)

	if err := e.Restart("b"); err == nil {
		t.Fatalf("expected Restart to reject a blocked service, got nil error")
	}

	cancel()
	wg.Wait()
}

func waitForStatus(t *testing.T, ch <-chan bus.Event, service string, want Status, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-ch:
			if ev.Type == bus.EventStatus && ev.Service == service && ev.Status == string(want) {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s to reach status %q", service, want)
		}
	}
}

func pidOf(t *testing.T, statuses []ServiceStatus, name string) int {
	t.Helper()
	for _, s := range statuses {
		if s.Name == name {
			return s.PID
		}
	}
	t.Fatalf("no status found for service %q", name)
	return -1
}

func statusOf(t *testing.T, statuses []ServiceStatus, name string) Status {
	t.Helper()
	for _, s := range statuses {
		if s.Name == name {
			return s.Status
		}
	}
	t.Fatalf("no status found for service %q", name)
	return ""
}
