// Command rig loads a rig config file and runs its services.
//
// This is the v1 CLI: a single foreground process, plain-text log/status
// output, and a tiny stdin REPL for `ps` / `trigger <name>` / `quit`. A
// bubbletea TUI is the planned replacement for the REPL, built on the same
// engine.Bus() event stream used here.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/eduardopellenz/rig/internal/bus"
	"github.com/eduardopellenz/rig/internal/config"
	"github.com/eduardopellenz/rig/internal/engine"
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
	fmt.Fprintln(os.Stderr, `rig <command> [config file]

Commands:
  up [rig.yaml]     load config and run all services
  check [rig.yaml]  validate config without running anything

Config file defaults to ./rig.yaml.`)
}

func configPath(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return "rig.yaml"
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
	path := configPath(args)
	cfg := loadOrDie(path)

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

	go printEvents(e)
	go readStdinCommands(e, cancel)

	fmt.Printf("rig: %d services loaded from %s\n", len(cfg.Services), path)
	fmt.Println(`type "ps", "trigger <name>", or "quit"`)

	if err := e.Up(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "up: %v\n", err)
	}
}

func printEvents(e *engine.Engine) {
	ch, unsubscribe := e.Bus().Subscribe()
	defer unsubscribe()
	for ev := range ch {
		switch ev.Type {
		case bus.EventStatus:
			fmt.Printf("[%s] %s\n", ev.Service, ev.Status)
		case bus.EventLog:
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
