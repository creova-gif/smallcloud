package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func Healthcheck() string { return "ok" }

func ProbeHTTPReady(ctx context.Context, baseURL, path string, interval, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}

	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return errors.New("baseURL is required")
	}

	p := strings.TrimSpace(path)
	if p == "" {
		p = "/healthz"
	} else if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	targetURL := base + p

	if interval <= 0 {
		interval = 50 * time.Millisecond
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastErr error
	for {
		if err := probeCtx.Err(); err != nil {
			if lastErr != nil {
				return fmt.Errorf("probe %s: %w (last error: %v)", targetURL, err, lastErr)
			}
			return fmt.Errorf("probe %s: %w", targetURL, err)
		}

		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, targetURL, nil)
		if err != nil {
			return fmt.Errorf("probe %s: %w", targetURL, err)
		}

		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest {
				return nil
			}
			lastErr = fmt.Errorf("unexpected status %d", resp.StatusCode)
		} else {
			lastErr = err
		}

		select {
		case <-probeCtx.Done():
			if lastErr != nil {
				return fmt.Errorf("probe %s: %w (last error: %v)", targetURL, probeCtx.Err(), lastErr)
			}
			return fmt.Errorf("probe %s: %w", targetURL, probeCtx.Err())
		case <-ticker.C:
		}
	}
}
