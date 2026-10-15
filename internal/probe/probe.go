// Package probe implements readiness checks (http/tcp/exec) and polls them
// on a schedule until they succeed, fail permanently, or the context is
// cancelled.
package probe

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"time"
)

type Checker interface {
	Check(ctx context.Context) error
}

type HTTP struct {
	Port int
	Path string
}

func (h HTTP) Check(ctx context.Context) error {
	path := h.Path
	if path == "" {
		path = "/"
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", h.Port, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http probe %s: status %d", url, resp.StatusCode)
	}
	return nil
}

type TCP struct {
	Port int
}

func (t TCP) Check(ctx context.Context) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", t.Port))
	if err != nil {
		return err
	}
	return conn.Close()
}

type Exec struct {
	Cmd []string
	Dir string
}

func (e Exec) Check(ctx context.Context) error {
	if len(e.Cmd) == 0 {
		return fmt.Errorf("exec probe: empty command")
	}
	cmd := exec.CommandContext(ctx, e.Cmd[0], e.Cmd[1:]...)
	cmd.Dir = e.Dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("exec probe failed: %w: %s", err, out)
	}
	return nil
}

type Spec struct {
	Checker          Checker
	InitialDelay     time.Duration
	Period           time.Duration
	Timeout          time.Duration
	SuccessThreshold int
}

// Wait blocks until the checker reports SuccessThreshold consecutive
// successes, or ctx is cancelled.
func Wait(ctx context.Context, spec Spec) error {
	if spec.Period <= 0 {
		spec.Period = 5 * time.Second
	}
	if spec.Timeout <= 0 {
		spec.Timeout = 2 * time.Second
	}
	threshold := spec.SuccessThreshold
	if threshold <= 0 {
		threshold = 1
	}

	if spec.InitialDelay > 0 {
		select {
		case <-time.After(spec.InitialDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	successes := 0
	for {
		checkCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
		err := spec.Checker.Check(checkCtx)
		cancel()

		if err == nil {
			successes++
			if successes >= threshold {
				return nil
			}
		} else {
			successes = 0
		}

		select {
		case <-time.After(spec.Period):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
