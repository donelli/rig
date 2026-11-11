// Command rig loads a rig config file and runs its services.
//
// This is the v1 CLI: a single foreground process, plain-text log/status
// output, and a tiny stdin REPL for `ps` / `trigger` / `restart` / `stop` /
// `quit`. A
// bubbletea TUI is the planned replacement for the REPL, built on the same
// engine.Bus() event stream used here.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/eduardopellenz/rig/internal/bus"
	"github.com/eduardopellenz/rig/internal/config"
	"github.com/eduardopellenz/rig/internal/engine"
	"github.com/eduardopellenz/rig/internal/webui"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "check":
		runCheck(os.Args[2:])
	case "up":
		runUp(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `rig <command> [flags] [config file]

Commands:
  up [flags] [rig.yaml] [service...]
                            load config and run all services. When services
                            are named, only they and their dependencies start;
                            the rest wait for "trigger <name>".
  check [rig.yaml]          validate config without running anything

Flags for "up":
  -addr string    address for the web dashboard (default ":7190")
  -no-ui          disable the web dashboard
  -logs           also print every log line and status change in the terminal
                  (always on with -no-ui; otherwise only failures are printed)

Config file defaults to ./rig.yaml.`)
}

func configPath(args []string) string {
	path, _ := splitArgs(args)
	return path
}

// splitArgs separates the optional config file (the first argument, when it
// looks like a YAML file or exists on disk) from service names.
func splitArgs(args []string) (string, []string) {
	if len(args) > 0 {
		first := args[0]
		if strings.HasSuffix(first, ".yaml") || strings.HasSuffix(first, ".yml") {
			return first, args[1:]
		}
		if info, err := os.Stat(first); err == nil && !info.IsDir() {
			return first, args[1:]
		}
	}
	return "rig.yaml", args
}

// limitTo makes every service outside the named ones and their transitive
// depends_on manual, so `rig up rig.yaml api` starts just what api needs.
func limitTo(cfg *config.Config, names []string) error {
	byName := make(map[string]config.Service, len(cfg.Services))
	for _, s := range cfg.Services {
		byName[s.Name] = s
	}
	keep := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if keep[name] {
			return
		}
		keep[name] = true
		for _, dep := range byName[name].DependsOn {
			visit(dep)
		}
	}
	for _, name := range names {
		if _, ok := byName[name]; !ok {
			return fmt.Errorf("unknown service %q", name)
		}
		visit(name)
	}
	manual := false
	for i := range cfg.Services {
		if keep[cfg.Services[i].Name] {
			// Asking for a service by name starts it even if it's manual
			cfg.Services[i].AutoInit = nil
		} else {
			cfg.Services[i].AutoInit = &manual
		}
	}
	return nil
}

func loadOrDie(path string) *config.Config {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid config: %v\n", err)
		os.Exit(1)
	}
	return cfg
}

func runCheck(args []string) {
	path := configPath(args)
	cfg := loadOrDie(path)
	if _, err := engine.New(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "invalid config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s: %d services, OK\n", path, len(cfg.Services))
}

func runUp(args []string) {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	addr := fs.String("addr", ":7190", "address for the web dashboard")
	noUI := fs.Bool("no-ui", false, "disable the web dashboard")
	logs := fs.Bool("logs", false, "also print every log line and status change in the terminal")
	_ = fs.Parse(args)

	path, only := splitArgs(fs.Args())
	cfg := loadOrDie(path)
	if len(only) > 0 {
		if err := limitTo(cfg, only); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	}

	e, err := engine.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid config: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nshutting down...")
		cancel()
	}()

	// With the dashboard on, logs live there; the terminal only flags failures
	go printEvents(e, *noUI || *logs)
	go readStdinCommands(e, cancel)

	if !*noUI {
		srv := webui.New(e)
		go func() {
			if err := srv.Run(ctx, *addr); err != nil {
				fmt.Fprintf(os.Stderr, "web ui: %v\n", err)
			}
		}()
		host := *addr
		if strings.HasPrefix(host, ":") {
			host = "localhost" + host
		}
		fmt.Printf("dashboard:  http://%s\n", host)
	}

	fmt.Printf("rig: %d services loaded from %s\n", len(cfg.Services), path)
	fmt.Println(`type "ps", "trigger <name>", "restart <name>", "stop <name>", or "quit"`)

	if err := e.Up(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "up: %v\n", err)
	}
}

// printEvents prints the event stream: everything when verbose, otherwise
// only services that fail.
func printEvents(e *engine.Engine, verbose bool) {
	ch, unsubscribe := e.Bus().Subscribe()
	defer unsubscribe()
	for ev := range ch {
		switch {
		case ev.Type == bus.EventStatus && (verbose || ev.Status == string(engine.StatusFailed)):
			fmt.Printf("[%s] %s\n", ev.Service, ev.Status)
		case ev.Type == bus.EventLog && verbose:
			fmt.Printf("%-24s %s\n", ev.Service+":", ev.Line)
		}
	}
}

func readStdinCommands(e *engine.Engine, cancel context.CancelFunc) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "":
			continue
		case line == "quit" || line == "exit":
			cancel()
			return
		case line == "ps":
			printStatus(e)
		case strings.HasPrefix(line, "trigger "):
			name := strings.TrimSpace(strings.TrimPrefix(line, "trigger "))
			if err := e.Trigger(name); err != nil {
				fmt.Println(err)
			}
		case strings.HasPrefix(line, "restart "):
			name := strings.TrimSpace(strings.TrimPrefix(line, "restart "))
			// Restart blocks until the service is up again; keep the REPL free
			go func() {
				if err := e.Restart(name); err != nil {
					fmt.Println(err)
				}
			}()
		case strings.HasPrefix(line, "stop "):
			name := strings.TrimSpace(strings.TrimPrefix(line, "stop "))
			go func() {
				if err := e.Stop(name); err != nil {
					fmt.Println(err)
				}
			}()
		default:
			fmt.Printf("unknown command: %q\n", line)
		}
	}
}

func printStatus(e *engine.Engine) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tLABELS\tPID")
	for _, s := range e.Status() {
		pid := "-"
		if s.PID > 0 {
			pid = fmt.Sprintf("%d", s.PID)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, s.Status, strings.Join(s.Labels, ","), pid)
	}
	w.Flush()
}
