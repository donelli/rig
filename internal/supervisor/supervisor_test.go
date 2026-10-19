package supervisor

import (
	"runtime"
	"testing"
	"time"
)

// longRunningArgv returns a command that runs until killed, on whichever OS
// the test happens to run on.
func longRunningArgv() []string {
	if runtime.GOOS == "windows" {
		return []string{"ping", "-t", "127.0.0.1"}
	}
	return []string{"sleep", "60"}
}

func TestStartAndStopTerminatesProcess(t *testing.T) {
	proc, err := Start(StartOptions{
		Name: "test",
		Argv: longRunningArgv(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give it a moment to actually be running.
	time.Sleep(500 * time.Millisecond)

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- proc.Stop(3 * time.Second) }()

	select {
	case <-done:
		elapsed := time.Since(start)
		t.Logf("Stop returned after %s", elapsed)
		if elapsed > 5*time.Second {
			t.Fatalf("Stop took too long: %s (grace was 3s)", elapsed)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Stop did not return within 6s — process may not have been terminated")
	}

	// proc.Wait() returning inside Stop already means the OS reaped the
	// process; calling it again here should return immediately with the
	// same (cached) result rather than hang, which would indicate the
	// process handle never actually completed.
	waitDone := make(chan struct{})
	go func() {
		_ = proc.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait after Stop hung — process may still be alive")
	}
}
