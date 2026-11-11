package engine

type Status string

const (
	StatusPending  Status = "pending"  // not yet scheduled
	StatusBlocked  Status = "blocked"  // a dependency failed; starts once that dependency is healthy again
	StatusManual   Status = "manual"   // auto_init: false, not triggered yet (whether or not its dependencies are ready)
	StatusStarting Status = "starting" // process launched, not yet healthy (serve) or not yet exited (once)
	StatusHealthy  Status = "healthy"  // serve: passed readiness; once: exited 0
	StatusFailed   Status = "failed"
	StatusStopping Status = "stopping" // stop signal just sent (Stop/Restart); waiting for the process to actually exit
	StatusStopped  Status = "stopped"  // serve: exited after having been healthy (shutdown or crash-after-healthy)
)

type ServiceStatus struct {
	Name   string   `json:"name"`
	Status Status   `json:"status"`
	Labels []string `json:"labels"`
	PID    int      `json:"pid"`
}
