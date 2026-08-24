package health

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// probeTimeout bounds a self-check when the caller passes no deadline.
const probeTimeout = 3 * time.Second

// Probe performs one health request against url and reports whether the
// service answered with 2xx.
//
// It exists so the container image can check itself: the runtime image carries
// no shell and no curl, so `api -healthcheck` is the health check.
func Probe(ctx context.Context, url string) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, probeTimeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build health request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("health request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("health endpoint returned %d", resp.StatusCode)
	}

	return nil
}
