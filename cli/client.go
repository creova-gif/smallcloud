package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/creova-gif/smallcloud/daemon"
	"github.com/creova-gif/smallcloud/pkg/config"
)

const defaultClientTimeout = 5 * time.Second

type DaemonClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func NewDaemonClient(baseURL, token string) *DaemonClient {
	return &DaemonClient{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: defaultClientTimeout},
	}
}

func (c *DaemonClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: defaultClientTimeout}
}

func (c *DaemonClient) doJSON(ctx context.Context, method, path string, reqBody, respBody any) error {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return errors.New("daemon base URL is empty")
	}

	var bodyReader io.Reader
	if reqBody != nil {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal %s %s request: %w", method, path, err)
		}
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, base+path, bodyReader)
	if err != nil {
		return fmt.Errorf("build %s %s request: %w", method, path, err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	rawResp, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read %s %s response: %w", method, path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errPayload struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rawResp, &errPayload)
		msg := strings.TrimSpace(errPayload.Error)
		if msg == "" {
			msg = strings.TrimSpace(string(rawResp))
		}
		if resp.StatusCode == http.StatusPaymentRequired {
			if msg != "" {
				return fmt.Errorf("%w: %s", daemon.ErrBudgetExceeded, msg)
			}
			return fmt.Errorf("%w (status %d)", daemon.ErrBudgetExceeded, resp.StatusCode)
		}
		if msg != "" {
			return fmt.Errorf("daemon %s %s failed (status %d): %s", method, path, resp.StatusCode, msg)
		}
		return fmt.Errorf("daemon %s %s failed (status %d)", method, path, resp.StatusCode)
	}

	if respBody != nil && len(bytes.TrimSpace(rawResp)) > 0 {
		if err := json.Unmarshal(rawResp, respBody); err != nil {
			return fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return nil
}

func (c *DaemonClient) Deploy(ctx context.Context, cfg *config.Config, releaseID string) (*daemon.DeploymentResult, error) {
	if cfg == nil {
		return nil, errors.New("deploy config is nil")
	}
	req := struct {
		Config    config.Config `json:"config"`
		ReleaseID string        `json:"release_id"`
	}{
		Config:    *cfg,
		ReleaseID: releaseID,
	}
	var out daemon.DeploymentResult
	if err := c.doJSON(ctx, http.MethodPost, "/v1/deploy", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *DaemonClient) Rollback(ctx context.Context, orgID, appName string) (daemon.RouteEntry, error) {
	req := struct {
		OrgID   string `json:"org_id"`
		AppName string `json:"app_name"`
	}{
		OrgID:   orgID,
		AppName: appName,
	}
	var out daemon.RouteEntry
	if err := c.doJSON(ctx, http.MethodPost, "/v1/rollback", req, &out); err != nil {
		return daemon.RouteEntry{}, err
	}
	return out, nil
}

func (c *DaemonClient) Status(ctx context.Context, orgID, appName string) (daemon.AppStatus, error) {
	q := url.Values{}
	q.Set("org_id", orgID)
	q.Set("app_name", appName)
	var out daemon.AppStatus
	if err := c.doJSON(ctx, http.MethodGet, "/v1/status?"+q.Encode(), nil, &out); err != nil {
		return daemon.AppStatus{}, err
	}
	return out, nil
}

func (c *DaemonClient) SetMaxBudget(ctx context.Context, orgID, appName string, maxMonthlyUSD float64) (daemon.AppBudgetState, error) {
	req := struct {
		OrgID         string  `json:"org_id"`
		AppName       string  `json:"app_name"`
		MaxMonthlyUSD float64 `json:"max_monthly_usd"`
	}{
		OrgID:         orgID,
		AppName:       appName,
		MaxMonthlyUSD: maxMonthlyUSD,
	}
	var out daemon.AppBudgetState
	if err := c.doJSON(ctx, http.MethodPost, "/v1/meter", req, &out); err != nil {
		return daemon.AppBudgetState{}, err
	}
	return out, nil
}

func (c *DaemonClient) Rollover(ctx context.Context, ts time.Time) (int, error) {
	var req any
	if !ts.IsZero() {
		req = struct {
			Timestamp time.Time `json:"timestamp"`
		}{
			Timestamp: ts.UTC(),
		}
	}
	var out struct {
		RolledOver int    `json:"rolled_over"`
		Month      string `json:"month"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/rollover", req, &out); err != nil {
		return 0, err
	}
	return out.RolledOver, nil
}

func (c *DaemonClient) Meter(ctx context.Context, orgID, appName string, deltaUSD float64) (daemon.AppBudgetState, error) {
	req := struct {
		OrgID    string  `json:"org_id"`
		AppName  string  `json:"app_name"`
		DeltaUSD float64 `json:"delta_usd"`
	}{
		OrgID:    orgID,
		AppName:  appName,
		DeltaUSD: deltaUSD,
	}
	var out daemon.AppBudgetState
	if err := c.doJSON(ctx, http.MethodPost, "/v1/meter", req, &out); err != nil {
		return daemon.AppBudgetState{}, err
	}
	return out, nil
}

func (c *DaemonClient) ListApps(ctx context.Context) ([]daemon.AppStatus, error) {
	var out struct {
		Apps []daemon.AppStatus `json:"apps"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/apps", nil, &out); err != nil {
		return nil, err
	}
	return out.Apps, nil
}
