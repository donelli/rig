package probe

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

// listen starts an HTTP server on host (an IPv4 or IPv6 loopback address)
// and returns its port, skipping the test when that address family isn't
// available on this machine.
func listen(t *testing.T, host string, status int) int {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skipf("can't listen on %s: %v", host, err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func check(t *testing.T, c Checker) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return c.Check(ctx)
}

// A server bound only to IPv6 localhost (like Vite on macOS) must pass both
// probes, as must one bound only to IPv4.
func TestProbesReachEitherLoopback(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1"} {
		port := listen(t, host, http.StatusOK)
		if err := check(t, HTTP{Port: port}); err != nil {
			t.Errorf("HTTP probe, server on %s: %v", host, err)
		}
		if err := check(t, TCP{Port: port}); err != nil {
			t.Errorf("TCP probe, server on %s: %v", host, err)
		}
	}
}

func TestHTTPProbeFailsOnErrorStatus(t *testing.T) {
	port := listen(t, "::1", http.StatusInternalServerError)
	if err := check(t, HTTP{Port: port}); err == nil {
		t.Fatal("expected a 500 to fail the probe")
	}
}

func TestProbesFailWhenNothingListens(t *testing.T) {
	// Grab a free port, then close it so nothing is listening there
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if err := check(t, HTTP{Port: port}); err == nil {
		t.Error("HTTP probe passed with nothing listening")
	}
	if err := check(t, TCP{Port: port}); err == nil {
		t.Error("TCP probe passed with nothing listening")
	}
}
