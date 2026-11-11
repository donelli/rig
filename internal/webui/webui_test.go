package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer() *Server {
	s := New(nil) // the log endpoints don't touch the engine
	for _, svc := range []string{"a", "b"} {
		for i := 0; i < 3; i++ {
			s.appendLog(svc, LogLine{Line: svc})
		}
	}
	return s
}

func do(t *testing.T, s *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	if path == "/api/logs" {
		s.handleAllLogs(rec, req)
	} else {
		s.handleServiceSub(rec, req)
	}
	return rec
}

func count(t *testing.T, s *Server, path string) int {
	t.Helper()
	var lines []LogLine
	if err := json.Unmarshal(do(t, s, http.MethodGet, path).Body.Bytes(), &lines); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return len(lines)
}

// Clearing one service empties its history and drops its lines from All,
// leaving the other services alone.
func TestClearServiceLogs(t *testing.T) {
	s := newTestServer()
	if rec := do(t, s, http.MethodDelete, "/api/services/a/logs"); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE a: status %d", rec.Code)
	}
	if n := count(t, s, "/api/services/a/logs"); n != 0 {
		t.Errorf("a has %d lines after clear, want 0", n)
	}
	if n := count(t, s, "/api/services/b/logs"); n != 3 {
		t.Errorf("b has %d lines, want 3", n)
	}
	if n := count(t, s, "/api/logs"); n != 3 {
		t.Errorf("All has %d lines, want b's 3", n)
	}
	// New lines after a clear are kept as usual
	s.appendLog("a", LogLine{Line: "after"})
	if n := count(t, s, "/api/logs"); n != 4 {
		t.Errorf("All has %d lines after a new one, want 4", n)
	}
}

func TestClearAllLogs(t *testing.T) {
	s := newTestServer()
	if rec := do(t, s, http.MethodDelete, "/api/logs"); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE all: status %d", rec.Code)
	}
	for _, path := range []string{"/api/logs", "/api/services/a/logs", "/api/services/b/logs"} {
		if n := count(t, s, path); n != 0 {
			t.Errorf("%s has %d lines after clearing all, want 0", path, n)
		}
	}
}
