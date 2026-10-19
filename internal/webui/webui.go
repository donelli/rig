// Package webui serves a small single-page dashboard: a service list with
// live status on the left, and streamed log output on the right. It's
// backed entirely by REST (service list, log history, manual trigger) plus
// Server-Sent Events for live updates — no WebSocket library needed.
package webui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/eduardopellenz/rig/internal/bus"
	"github.com/eduardopellenz/rig/internal/engine"
)

//go:embed static/index.html
var staticFS embed.FS

const maxLogLines = 2000

// markerStatuses are the status transitions worth a visible marker line in
// the log view — the ones that mean "a process just started or stopped,"
// as opposed to scheduling states like pending/blocked/manual.
var markerStatuses = map[string]bool{
	string(engine.StatusStarting): true,
	string(engine.StatusHealthy):  true,
	string(engine.StatusFailed):   true,
	string(engine.StatusStopped):  true,
}

type LogLine struct {
	Time   time.Time `json:"time"`
	Stream string    `json:"stream,omitempty"`
	Line   string    `json:"line,omitempty"`
	Marker string    `json:"marker,omitempty"` // status name; set instead of Stream/Line for a lifecycle marker
}

type Server struct {
	engine *engine.Engine

	mu   sync.RWMutex
	logs map[string][]LogLine
}

func New(e *engine.Engine) *Server {
	return &Server{engine: e, logs: make(map[string][]LogLine)}
}

// Run buffers logs (for history on load/switch) and serves the dashboard
// until ctx is cancelled.
func (s *Server) Run(ctx context.Context, addr string) error {
	historyCh, unsubscribe := s.engine.Bus().Subscribe()
	go func() {
		defer unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-historyCh:
				if !ok {
					return
				}
				switch {
				case ev.Type == bus.EventLog:
					s.appendLog(ev.Service, LogLine{Time: time.Now(), Stream: ev.Stream, Line: ev.Line})
				case ev.Type == bus.EventStatus && markerStatuses[ev.Status]:
					s.appendLog(ev.Service, LogLine{Time: time.Now(), Marker: ev.Status})
				}
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/services", s.handleServices)
	mux.HandleFunc("/api/services/", s.handleServiceSub)
	mux.HandleFunc("/api/stream", s.handleStream)

	srv := &http.Server{Addr: addr, Handler: mux}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (s *Server) appendLog(service string, line LogLine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lines := append(s.logs[service], line)
	if len(lines) > maxLogLines {
		lines = lines[len(lines)-maxLogLines:]
	}
	s.logs[service] = lines
}

func (s *Server) getLogs(service string) []LogLine {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]LogLine, len(s.logs[service]))
	copy(out, s.logs[service])
	return out
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.engine.Status())
}

// handleServiceSub handles /api/services/{name}/logs (GET) and
// /api/services/{name}/{trigger,restart,stop} (POST).
func (s *Server) handleServiceSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/services/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	name, action := parts[0], parts[1]

	switch action {
	case "logs":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, s.getLogs(name))
	case "trigger":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := s.engine.Trigger(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "restart":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := s.engine.Restart(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "stop":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := s.engine.Stop(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, unsubscribe := s.engine.Bus().Subscribe()
	defer unsubscribe()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
