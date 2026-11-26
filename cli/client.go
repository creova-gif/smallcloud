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
	"os"
	"path/filepath"
	"strconv"
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

func (c *DaemonClient) Events(ctx context.Context, orgID, appName string, limit int) ([]daemon.AuditEvent, error) {
	q := url.Values{}
	if orgID != "" {
		q.Set("org_id", orgID)
	}
	if appName != "" {
		q.Set("app", appName)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/v1/events"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out struct {
		Events []daemon.AuditEvent `json:"events"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Events, nil
}

func (c *DaemonClient) DeleteApp(ctx context.Context, orgID, appName string) error {
	q := url.Values{}
	q.Set("org_id", orgID)
	q.Set("app", appName)
	return c.doJSON(ctx, http.MethodDelete, "/v1/apps?"+q.Encode(), nil, nil)
}

func (c *DaemonClient) SetCanaryWeight(ctx context.Context, orgID, appName string, standbyPct int) (daemon.RouteEntry, error) {
	req := struct {
		OrgID      string `json:"org_id"`
		AppName    string `json:"app_name"`
		StandbyPct int    `json:"standby_pct"`
	}{
		OrgID:      orgID,
		AppName:    appName,
		StandbyPct: standbyPct,
	}
	var out daemon.RouteEntry
	if err := c.doJSON(ctx, http.MethodPost, "/v1/canary", req, &out); err != nil {
		return daemon.RouteEntry{}, err
	}
	return out, nil
}

func (c *DaemonClient) Logs(ctx context.Context, orgID, appName, releaseID string, tail int) (string, error) {
	q := url.Values{}
	q.Set("org_id", orgID)
	q.Set("app_name", appName)
	if releaseID != "" {
		q.Set("release_id", releaseID)
	}
	if tail > 0 {
		q.Set("tail", strconv.Itoa(tail))
	}
	var out struct {
		Logs string `json:"logs"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/logs?"+q.Encode(), nil, &out); err != nil {
		return "", err
	}
	return out.Logs, nil
}

func (c *DaemonClient) SetOrgQuota(ctx context.Context, orgID string, maxMonthlyUSD float64) (daemon.OrgSpendSummary, error) {
	req := struct {
		OrgID         string  `json:"org_id"`
		MaxMonthlyUSD float64 `json:"max_monthly_usd"`
	}{
		OrgID:         orgID,
		MaxMonthlyUSD: maxMonthlyUSD,
	}
	var out daemon.OrgSpendSummary
	if err := c.doJSON(ctx, http.MethodPost, "/v1/org-quota", req, &out); err != nil {
		return daemon.OrgSpendSummary{}, err
	}
	return out, nil
}

func (c *DaemonClient) OrgSummary(ctx context.Context, orgID string) (daemon.OrgSpendSummary, error) {
	q := url.Values{}
	q.Set("org_id", orgID)
	var out daemon.OrgSpendSummary
	if err := c.doJSON(ctx, http.MethodGet, "/v1/org-quota?"+q.Encode(), nil, &out); err != nil {
		return daemon.OrgSpendSummary{}, err
	}
	return out, nil
}

func (c *DaemonClient) PruneSnapshots(ctx context.Context, orgID, appName string, keepLast int) (int, error) {
	req := struct {
		OrgID    string `json:"org_id"`
		AppName  string `json:"app_name"`
		KeepLast int    `json:"keep_last"`
	}{
		OrgID:    orgID,
		AppName:  appName,
		KeepLast: keepLast,
	}
	var out struct {
		Pruned int `json:"pruned"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/v1/prune", req, &out); err != nil {
		return 0, err
	}
	return out.Pruned, nil
}

func (c *DaemonClient) RegisterNode(ctx context.Context, node daemon.WorkerNode) (daemon.WorkerNode, error) {
	var out daemon.WorkerNode
	if err := c.doJSON(ctx, http.MethodPost, "/v1/nodes/register", node, &out); err != nil {
		return daemon.WorkerNode{}, err
	}
	return out, nil
}

func (c *DaemonClient) ListNodes(ctx context.Context) ([]daemon.WorkerNode, error) {
	var out struct {
		Nodes []daemon.WorkerNode `json:"nodes"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/nodes", nil, &out); err != nil {
		return nil, err
	}
	return out.Nodes, nil
}

func (c *DaemonClient) DownloadBackup(ctx context.Context, orgID, appName, destPath string) error {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return errors.New("daemon base URL is empty")
	}
	if strings.TrimSpace(destPath) == "" {
		return errors.New("destPath is required")
	}

	q := url.Values{}
	q.Set("org_id", orgID)
	q.Set("app", appName)
	path := "/v1/backup?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return fmt.Errorf("build GET %s request: %w", path, err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		rawResp, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		var errPayload struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rawResp, &errPayload)
		msg := strings.TrimSpace(errPayload.Error)
		if msg == "" {
			msg = strings.TrimSpace(string(rawResp))
		}
		if msg != "" {
			return fmt.Errorf("daemon GET %s failed (status %d): %s", path, resp.StatusCode, msg)
		}
		return fmt.Errorf("daemon GET %s failed (status %d)", path, resp.StatusCode)
	}

	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return fmt.Errorf("mkdir %q: %w", destDir, err)
	}
	tmpFile, err := os.CreateTemp(destDir, ".smallcloud-backup-*.tar.gz")
	if err != nil {
		return fmt.Errorf("create temp backup file: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write backup archive: %w", err)
	}
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("chmod backup archive: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close backup archive: %w", err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("rename backup archive %q: %w", destPath, err)
	}
	cleanup = false
	return nil
}

func (c *DaemonClient) RestoreBackup(ctx context.Context, bundlePath string) (daemon.BackupManifest, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return daemon.BackupManifest{}, errors.New("daemon base URL is empty")
	}
	if strings.TrimSpace(bundlePath) == "" {
		return daemon.BackupManifest{}, errors.New("bundlePath is required")
	}

	f, err := os.Open(bundlePath)
	if err != nil {
		return daemon.BackupManifest{}, fmt.Errorf("open backup bundle %q: %w", bundlePath, err)
	}
	defer func() {
		_ = f.Close()
	}()

	const path = "/v1/restore"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, f)
	if err != nil {
		return daemon.BackupManifest{}, fmt.Errorf("build POST %s request: %w", path, err)
	}
	if info, statErr := f.Stat(); statErr == nil {
		req.ContentLength = info.Size()
	}
	req.Header.Set("Content-Type", "application/gzip")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return daemon.BackupManifest{}, fmt.Errorf("POST %s: %w", path, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	rawResp, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return daemon.BackupManifest{}, fmt.Errorf("read POST %s response: %w", path, err)
	}

	if resp.StatusCode != http.StatusOK {
		var errPayload struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rawResp, &errPayload)
		msg := strings.TrimSpace(errPayload.Error)
		if msg == "" {
			msg = strings.TrimSpace(string(rawResp))
		}
		if msg != "" {
			return daemon.BackupManifest{}, fmt.Errorf("daemon POST %s failed (status %d): %s", path, resp.StatusCode, msg)
		}
		return daemon.BackupManifest{}, fmt.Errorf("daemon POST %s failed (status %d)", path, resp.StatusCode)
	}

	var manifest daemon.BackupManifest
	if err := json.Unmarshal(rawResp, &manifest); err != nil {
		return daemon.BackupManifest{}, fmt.Errorf("decode POST %s response: %w", path, err)
	}
	return manifest, nil
}
