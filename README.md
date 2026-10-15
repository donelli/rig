# rig

A Tilt.dev-style local dev orchestrator, without the Docker dependency.
Define services (one-shot setup commands or long-running processes) in
plain YAML, wire up a dependency graph, add readiness probes, and run
everything with one command.

The engine never links against Docker's API or daemon socket. If a service
happens to run `docker compose up <name>`, that's just a normal `serve_cmd` —
identical in every way to a service that runs a bash script or a binary.

## Status: v1 (MVP)

Implemented:
- YAML config, with `include:` for splitting large configs into files, and
  `text/template` preprocessing (`{{env "FOO"}}`, `{{if}}`, `{{range}}`)
- Dependency graph (`depends_on`) with cycle detection
- One-shot (`cmd`) and long-running (`serve_cmd`) services
- `max_parallel_updates` — caps concurrent service startups
- `auto_init: false` — manual services, started via `trigger <name>` in the
  `up` REPL
- Readiness probes: `http`, `tcp`, `exec`, with initial delay / period /
  timeout / success threshold
- Process supervision: own process group, graceful stop (SIGTERM on
  Unix / CTRL_BREAK_EVENT on Windows) → grace period → hard kill (SIGKILL /
  TerminateProcess) on shutdown, crash detection after a service goes
  healthy
- `rig check` / `rig up`, live log + status stream, minimal stdin
  REPL (`ps`, `trigger <name>`, `quit`)

Not yet implemented (see Roadmap):
- File-watch-triggered re-run/restart (`deps: [...]` is parsed but inert)
- Restart policies
- TUI (bubbletea) — `up` currently prints a flat log stream instead
- Web dashboard

## Platforms

Linux, macOS, and Windows natively. The `internal/supervisor` package has a
small OS-specific file for each: `supervisor_unix.go` (process groups +
SIGTERM/SIGKILL) and `supervisor_windows.go` (process groups +
CTRL_BREAK_EVENT, falling back to a hard kill). Everything else in the
engine is platform-independent.

## Quick start

```bash
go mod tidy
go run ./cmd/rig check examples/rig.yaml
go run ./cmd/rig up examples/rig.yaml
```

## Config reference

```yaml
settings:
  max_parallel_updates: 5   # concurrent service startups; default 5

include:
  - trowel.yaml              # paths relative to this file
  - flatbed.yaml

services:
  - name: my-service
    cmd: bash ./scripts/setup.sh        # one-shot; exactly one of cmd / serve_cmd
    serve_cmd: bash ./scripts/run.sh    # long-running
    dir: ./some/subdir                  # working directory (default: rig's cwd)
    env:
      FOO: bar                          # merged over the inherited environment
    labels: [backend]                   # free-form grouping tags
    depends_on: [other-service]         # must all reach a successful terminal state first
    auto_init: false                    # default true; false = wait for a manual trigger
    deps: [./some/watched/path]         # parsed, not yet wired to a watcher (roadmap)
    readiness:                          # only meaningful for serve_cmd
      http:
        port: 8080
        path: /health
      # or: tcp: { port: 8080 }
      # or: exec: { cmd: ["curl", "-f", "http://localhost:8080/health"] }
      initial_delay: 3s
      period: 5s
      timeout: 2s
      success_threshold: 1
```

Commands run as a plain argv via `os/exec` — never through a shell. Write
`bash ./script.sh` or `sh -c "a | b"` explicitly when you need shell
features; there's no implicit shell wrapping or quoting footgun.

## Package layout

```
cmd/rig/          CLI entrypoint
internal/
  config/              YAML loading, include resolution, template preprocessing, validation
  graph/                dependency DAG, topological order, cycle detection
  engine/               scheduler: dep-waiting, manual triggers, semaphore, lifecycle
  resource/             argv parsing for cmd/serve_cmd strings
  probe/                http/tcp/exec readiness checks + polling
  supervisor/           process start/stop (process-group signals), log capture
  bus/                  pub/sub event stream (status + log lines) feeding the CLI/TUI
```

## Roadmap

1. File watching (`fsnotify`) wired to `deps: [...]` — re-run `cmd`
   services, restart `serve_cmd` services, on change
2. Restart policies (`on-failure` / `always` / `never`)
3. bubbletea TUI replacing the stdin REPL: resource tree, live per-service
   logs, label filter, trigger keybind
4. Web dashboard over the same `bus.Bus` event stream
