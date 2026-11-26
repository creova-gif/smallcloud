package daemon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creova-gif/smallcloud/pkg/config"
)

func newTestServerDaemon(t *testing.T) *Daemon {
	t.Helper()
	dir := t.TempDir()
	return NewDaemon(filepath.Join(dir, "cgroups"), filepath.Join(dir, "replicas"))
}

func TestHealthz(t *testing.T) {
	d := newTestServerDaemon(t)

	for _, h := range []http.Handler{d.HandlerWithAuth(""), d.HandlerWithAuth("secret-token")} {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
		}

		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if body["status"] != "ok" {
			t.Fatalf("expected status ok, got %q", body["status"])
		}
	}
}

func TestRolloverEndpoint(t *testing.T) {
	d := newTestServerDaemon(t)
	h := d.HandlerWithAuth("")

	if err := d.budget.RegisterApp("acme", "web", 100, 80); err != nil {
		t.Fatalf("register app failed: %v", err)
	}
	if _, err := d.RecordSpend("acme", "web", 25); err != nil {
		t.Fatalf("record spend failed: %v", err)
	}

	emptyReq := httptest.NewRequest(http.MethodPost, "/v1/rollover", nil)
	emptyRec := httptest.NewRecorder()
	h.ServeHTTP(emptyRec, emptyReq)
	if emptyRec.Code != http.StatusOK {
		t.Fatalf("empty rollover failed: %d %s", emptyRec.Code, emptyRec.Body.String())
	}
	var emptyResp struct {
		RolledOver int    `json:"rolled_over"`
		Month      string `json:"month"`
	}
	if err := json.Unmarshal(emptyRec.Body.Bytes(), &emptyResp); err != nil {
		t.Fatalf("unmarshal empty rollover: %v", err)
	}
	expectedCurrentMonth := time.Now().UTC().Format("2006-01")
	if emptyResp.Month != expectedCurrentMonth || emptyResp.RolledOver != 0 {
		t.Fatalf("unexpected empty rollover response: %+v (expected month %s, rolled_over 0)", emptyResp, expectedCurrentMonth)
	}

	rolloverReq := httptest.NewRequest(http.MethodPost, "/v1/rollover", bytes.NewBufferString(`{"timestamp":"2099-02-01T00:00:00Z"}`))
	rolloverRec := httptest.NewRecorder()
	h.ServeHTTP(rolloverRec, rolloverReq)
	if rolloverRec.Code != http.StatusOK {
		t.Fatalf("rollover failed: %d %s", rolloverRec.Code, rolloverRec.Body.String())
	}
	var rolloverResp struct {
		RolledOver int    `json:"rolled_over"`
		Month      string `json:"month"`
	}
	if err := json.Unmarshal(rolloverRec.Body.Bytes(), &rolloverResp); err != nil {
		t.Fatalf("unmarshal rollover: %v", err)
	}
	if rolloverResp.RolledOver != 1 || rolloverResp.Month != "2099-02" {
		t.Fatalf("expected rolled_over=1 month=2099-02, got %+v", rolloverResp)
	}

	againReq := httptest.NewRequest(http.MethodPost, "/v1/rollover", bytes.NewBufferString(`{"timestamp":"2099-02-15T12:00:00Z"}`))
	againRec := httptest.NewRecorder()
	h.ServeHTTP(againRec, againReq)
	if againRec.Code != http.StatusOK {
		t.Fatalf("second rollover failed: %d %s", againRec.Code, againRec.Body.String())
	}
	var againResp struct {
		RolledOver int    `json:"rolled_over"`
		Month      string `json:"month"`
	}
	if err := json.Unmarshal(againRec.Body.Bytes(), &againResp); err != nil {
		t.Fatalf("unmarshal second rollover: %v", err)
	}
	if againResp.RolledOver != 0 || againResp.Month != "2099-02" {
		t.Fatalf("expected idempotent rolled_over=0 month=2099-02, got %+v", againResp)
	}

	badReq := httptest.NewRequest(http.MethodPost, "/v1/rollover", bytes.NewBufferString(`{invalid`))
	badRec := httptest.NewRecorder()
	h.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d: %s", badRec.Code, badRec.Body.String())
	}
}

func TestHandlerWithAuth(t *testing.T) {
	d := newTestServerDaemon(t)
	h := d.HandlerWithAuth("top-secret")

	cases := []struct {
		name       string
		method     string
		path       string
		authHeader string
		wantStatus int
	}{
		{
			name:       "root ui open without token",
			method:     http.MethodGet,
			path:       "/",
			wantStatus: http.StatusOK,
		},
		{
			name:       "ui route open without token",
			method:     http.MethodGet,
			path:       "/ui",
			wantStatus: http.StatusOK,
		},
		{
			name:       "healthz open without token",
			method:     http.MethodGet,
			path:       "/healthz",
			wantStatus: http.StatusOK,
		},
		{
			name:       "v1 events missing header",
			method:     http.MethodGet,
			path:       "/v1/events",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "v1 events valid token",
			method:     http.MethodGet,
			path:       "/v1/events",
			authHeader: "Bearer top-secret",
			wantStatus: http.StatusOK,
		},
		{
			name:       "v1 rollover missing header",
			method:     http.MethodPost,
			path:       "/v1/rollover",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "v1 status missing header",
			method:     http.MethodGet,
			path:       "/v1/status?org_id=acme&app_name=web",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "v1 apps missing header",
			method:     http.MethodGet,
			path:       "/v1/apps",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "v1 apps wrong token",
			method:     http.MethodGet,
			path:       "/v1/apps",
			authHeader: "Bearer wrong-token",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "v1 apps valid token",
			method:     http.MethodGet,
			path:       "/v1/apps",
			authHeader: "Bearer top-secret",
			wantStatus: http.StatusOK,
		},
		{
			name:       "v1 rollover wrong scheme",
			method:     http.MethodPost,
			path:       "/v1/rollover",
			authHeader: "Basic top-secret",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "v1 rollover wrong token",
			method:     http.MethodPost,
			path:       "/v1/rollover",
			authHeader: "Bearer wrong-token",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "v1 rollover valid token",
			method:     http.MethodPost,
			path:       "/v1/rollover",
			authHeader: "Bearer top-secret",
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d: %s", tc.wantStatus, rec.Code, rec.Body.String())
			}
			if tc.wantStatus == http.StatusUnauthorized {
				var errBody map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
					t.Fatalf("unmarshal 401 body: %v", err)
				}
				if errBody["error"] != "unauthorized" {
					t.Fatalf("expected error=unauthorized, got %q", errBody["error"])
				}
			}
		})
	}
}

func TestHandlerEnvAuth(t *testing.T) {
	d := newTestServerDaemon(t)

	t.Setenv("SMALLCLOUD_API_TOKEN", "env-secret")
	h := d.Handler()

	unauthReq := httptest.NewRequest(http.MethodPost, "/v1/rollover", nil)
	unauthRec := httptest.NewRecorder()
	h.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when SMALLCLOUD_API_TOKEN is set, got %d", unauthRec.Code)
	}

	authReq := httptest.NewRequest(http.MethodPost, "/v1/rollover", nil)
	authReq.Header.Set("Authorization", "Bearer env-secret")
	authRec := httptest.NewRecorder()
	h.ServeHTTP(authRec, authReq)
	if authRec.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid env token, got %d: %s", authRec.Code, authRec.Body.String())
	}

	t.Setenv("SMALLCLOUD_API_TOKEN", "")
	openHandler := d.Handler()
	openReq := httptest.NewRequest(http.MethodPost, "/v1/rollover", nil)
	openRec := httptest.NewRecorder()
	openHandler.ServeHTTP(openRec, openReq)
	if openRec.Code != http.StatusOK {
		t.Fatalf("expected 200 when SMALLCLOUD_API_TOKEN is empty, got %d: %s", openRec.Code, openRec.Body.String())
	}
}

func TestListAppsEndpoint(t *testing.T) {
	d := newTestServerDaemon(t)
	h := d.HandlerWithAuth("")

	emptyReq := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	emptyRec := httptest.NewRecorder()
	h.ServeHTTP(emptyRec, emptyReq)
	if emptyRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", emptyRec.Code, emptyRec.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(emptyRec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal empty apps: %v", err)
	}
	if string(raw["apps"]) != "[]" {
		t.Fatalf("expected empty apps array [], got %s", string(raw["apps"]))
	}

	deploySpecs := []struct {
		orgID   string
		appName string
		domain  string
	}{
		{orgID: "org-beta", appName: "zulu", domain: "zulu.smallcloud.dev"},
		{orgID: "org-alpha", appName: "zeta", domain: "zeta.smallcloud.dev"},
		{orgID: "org-alpha", appName: "api", domain: "api.smallcloud.dev"},
	}

	for _, spec := range deploySpecs {
		cfg := &config.Config{
			App: config.AppConfig{
				Name:        spec.appName,
				OrgID:       spec.orgID,
				Domain:      spec.domain,
				Port:        8000,
				Healthcheck: "/healthz",
				Runtime:     "dockerfile",
			},
			Resources: config.ResourcesConfig{
				CPUMillicores: 250,
				MemoryMB:      128,
			},
			Budget: config.BudgetConfig{
				MaxMonthlyUSD: 10.0,
				AlertAtPct:    80.0,
				HardStop:      true,
			},
			Storage: config.StorageConfig{
				SQLitePath: "/data/" + spec.appName + ".db",
				SyncMs:     1000,
			},
		}
		if _, err := d.Deploy(cfg, "v1.0.0"); err != nil {
			t.Fatalf("deploy %s/%s failed: %v", spec.orgID, spec.appName, err)
		}
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	listRec := httptest.NewRecorder()
	h.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", listRec.Code, listRec.Body.String())
	}

	var resp struct {
		Apps []AppStatus `json:"apps"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal apps response: %v", err)
	}
	if len(resp.Apps) != 3 {
		t.Fatalf("expected 3 apps, got %d", len(resp.Apps))
	}

	wantOrder := []struct {
		orgID   string
		appName string
		domain  string
	}{
		{orgID: "org-alpha", appName: "api", domain: "api.smallcloud.dev"},
		{orgID: "org-alpha", appName: "zeta", domain: "zeta.smallcloud.dev"},
		{orgID: "org-beta", appName: "zulu", domain: "zulu.smallcloud.dev"},
	}
	for i, want := range wantOrder {
		got := resp.Apps[i]
		if got.OrgID != want.orgID || got.AppName != want.appName {
			t.Fatalf("apps[%d]: expected %s/%s, got %s/%s", i, want.orgID, want.appName, got.OrgID, got.AppName)
		}
		if got.Route.Domain != want.domain || got.Route.ActiveReleaseID != "v1.0.0" {
			t.Fatalf("apps[%d]: unexpected route %+v", i, got.Route)
		}
		if got.Budget.MaxMonthlyUSD != 10.0 {
			t.Fatalf("apps[%d]: unexpected budget %+v", i, got.Budget)
		}
	}
}

func TestMetricsEndpoint(t *testing.T) {
	d := newTestServerDaemon(t)
	h := d.HandlerWithAuth("secret")

	emptyReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	emptyRec := httptest.NewRecorder()
	h.ServeHTTP(emptyRec, emptyReq)
	if emptyRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", emptyRec.Code, emptyRec.Body.String())
	}
	if ct := emptyRec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain; version=0.0.4") {
		t.Fatalf("unexpected Content-Type: %q", ct)
	}
	if !strings.Contains(emptyRec.Body.String(), "smallcloud_apps_total 0\n") {
		t.Fatalf("expected smallcloud_apps_total 0, got:\n%s", emptyRec.Body.String())
	}

	specs := []struct {
		orgID string
		app   string
		limit float64
		spend float64
		halt  bool
	}{
		{orgID: "acme", app: "api", limit: 10.0, spend: 15.0, halt: true},
		{orgID: "acme", app: "web", limit: 25.0, spend: 5.0, halt: false},
	}

	for _, s := range specs {
		cfg := &config.Config{
			App: config.AppConfig{
				Name:        s.app,
				OrgID:       s.orgID,
				Domain:      s.app + ".smallcloud.dev",
				Port:        8000,
				Healthcheck: "/healthz",
				Runtime:     "dockerfile",
			},
			Resources: config.ResourcesConfig{
				CPUMillicores: 250,
				MemoryMB:      128,
			},
			Budget: config.BudgetConfig{
				MaxMonthlyUSD: s.limit,
				AlertAtPct:    80.0,
				HardStop:      true,
			},
			Storage: config.StorageConfig{
				SQLitePath: "/data/" + s.app + ".db",
				SyncMs:     1000,
			},
		}
		if _, err := d.Deploy(cfg, "v1.0.0"); err != nil {
			t.Fatalf("deploy %s/%s failed: %v", s.orgID, s.app, err)
		}
		_, err := d.RecordSpend(s.orgID, s.app, s.spend)
		if s.halt {
			if !errors.Is(err, ErrBudgetExceeded) {
				t.Fatalf("expected ErrBudgetExceeded for %s/%s, got %v", s.orgID, s.app, err)
			}
		} else if err != nil {
			t.Fatalf("record spend for %s/%s failed: %v", s.orgID, s.app, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200 without auth, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain; version=0.0.4") {
		t.Fatalf("unexpected Content-Type: %q", ct)
	}

	body := rec.Body.String()
	expectedLines := []string{
		"# TYPE smallcloud_apps_total gauge",
		"smallcloud_apps_total 2",
		"# TYPE smallcloud_app_budget_limit_usd gauge",
		`smallcloud_app_budget_limit_usd{org_id="acme",app="api"} 10`,
		`smallcloud_app_budget_limit_usd{org_id="acme",app="web"} 25`,
		"# TYPE smallcloud_app_budget_spend_usd gauge",
		`smallcloud_app_budget_spend_usd{org_id="acme",app="api"} 15`,
		`smallcloud_app_budget_spend_usd{org_id="acme",app="web"} 5`,
		"# TYPE smallcloud_app_halted gauge",
		`smallcloud_app_halted{org_id="acme",app="api"} 1`,
		`smallcloud_app_halted{org_id="acme",app="web"} 0`,
	}
	for _, want := range expectedLines {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics output missing %q\ngot:\n%s", want, body)
		}
	}

	if got := escapePromLabel("a\\b\"c\nd"); got != `a\\b\"c\nd` {
		t.Fatalf("unexpected escaped label: %q", got)
	}
}

func TestEventsEndpoint(t *testing.T) {
	d := newTestServerDaemon(t)
	h := d.HandlerWithAuth("secret-token")

	unauthReq := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	unauthRec := httptest.NewRecorder()
	h.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", unauthRec.Code)
	}

	specs := []struct {
		orgID   string
		appName string
		relID   string
	}{
		{orgID: "acme", appName: "api", relID: "rel-a"},
		{orgID: "acme", appName: "web", relID: "rel-b"},
		{orgID: "beta", appName: "api", relID: "rel-c"},
	}
	for _, s := range specs {
		cfg := &config.Config{
			App: config.AppConfig{
				Name:        s.appName,
				OrgID:       s.orgID,
				Domain:      s.appName + "." + s.orgID + ".smallcloud.dev",
				Port:        8000,
				Healthcheck: "/healthz",
				Runtime:     "dockerfile",
			},
			Resources: config.ResourcesConfig{
				CPUMillicores: 250,
				MemoryMB:      128,
			},
			Budget: config.BudgetConfig{
				MaxMonthlyUSD: 10.0,
				AlertAtPct:    80.0,
				HardStop:      true,
			},
			Storage: config.StorageConfig{
				SQLitePath: "/data/" + s.appName + ".db",
				SyncMs:     1000,
			},
		}
		if _, err := d.Deploy(cfg, s.relID); err != nil {
			t.Fatalf("deploy %s/%s failed: %v", s.orgID, s.appName, err)
		}
	}

	if _, err := d.SetMaxBudget("acme", "api", 25.0); err != nil {
		t.Fatalf("set max budget failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/events?org_id=acme&app=api&limit=1", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Events []AuditEvent `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal events: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("expected 1 event with limit=1, got %d", len(resp.Events))
	}
	if resp.Events[0].Type != "budget_updated" || resp.Events[0].OrgID != "acme" || resp.Events[0].AppName != "api" {
		t.Fatalf("unexpected latest event: %+v", resp.Events[0])
	}

	allAcmeReq := httptest.NewRequest(http.MethodGet, "/v1/events?org_id=acme", nil)
	allAcmeReq.Header.Set("Authorization", "Bearer secret-token")
	allAcmeRec := httptest.NewRecorder()
	h.ServeHTTP(allAcmeRec, allAcmeReq)
	if allAcmeRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", allAcmeRec.Code, allAcmeRec.Body.String())
	}
	var allAcmeResp struct {
		Events []AuditEvent `json:"events"`
	}
	if err := json.Unmarshal(allAcmeRec.Body.Bytes(), &allAcmeResp); err != nil {
		t.Fatalf("unmarshal acme events: %v", err)
	}
	if len(allAcmeResp.Events) != 3 {
		t.Fatalf("expected 3 acme events, got %d", len(allAcmeResp.Events))
	}

	badLimitReq := httptest.NewRequest(http.MethodGet, "/v1/events?limit=abc", nil)
	badLimitReq.Header.Set("Authorization", "Bearer secret-token")
	badLimitRec := httptest.NewRecorder()
	h.ServeHTTP(badLimitRec, badLimitReq)
	if badLimitRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid limit, got %d", badLimitRec.Code)
	}
}

func TestDeleteAppEndpoint(t *testing.T) {
	d := newTestServerDaemon(t)
	h := d.HandlerWithAuth("secret-token")

	unauthReq := httptest.NewRequest(http.MethodDelete, "/v1/apps?org_id=acme&app=web", nil)
	unauthRec := httptest.NewRecorder()
	h.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", unauthRec.Code)
	}

	badReq := httptest.NewRequest(http.MethodDelete, "/v1/apps?org_id=INVALID&app=web", nil)
	badReq.Header.Set("Authorization", "Bearer secret-token")
	badRec := httptest.NewRecorder()
	h.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid slug, got %d: %s", badRec.Code, badRec.Body.String())
	}

	missingReq := httptest.NewRequest(http.MethodDelete, "/v1/apps?org_id=acme&app=missing", nil)
	missingReq.Header.Set("Authorization", "Bearer secret-token")
	missingRec := httptest.NewRecorder()
	h.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing app, got %d: %s", missingRec.Code, missingRec.Body.String())
	}

	cfg := &config.Config{
		App: config.AppConfig{
			Name:        "web",
			OrgID:       "acme",
			Domain:      "web.smallcloud.dev",
			Port:        8000,
			Healthcheck: "/healthz",
			Runtime:     "dockerfile",
		},
		Resources: config.ResourcesConfig{
			CPUMillicores: 250,
			MemoryMB:      128,
		},
		Budget: config.BudgetConfig{
			MaxMonthlyUSD: 10.0,
			AlertAtPct:    80.0,
			HardStop:      true,
		},
		Storage: config.StorageConfig{
			SQLitePath: "/data/web.db",
			SyncMs:     1000,
		},
	}
	if _, err := d.Deploy(cfg, "v1"); err != nil {
		t.Fatalf("deploy v1 failed: %v", err)
	}
	if _, err := d.Deploy(cfg, "v2"); err != nil {
		t.Fatalf("deploy v2 failed: %v", err)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/v1/apps?org_id=acme&app=web", nil)
	delReq.Header.Set("Authorization", "Bearer secret-token")
	delRec := httptest.NewRecorder()
	h.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on delete, got %d: %s", delRec.Code, delRec.Body.String())
	}
	var delResp struct {
		Deleted bool   `json:"deleted"`
		OrgID   string `json:"org_id"`
		AppName string `json:"app_name"`
	}
	if err := json.Unmarshal(delRec.Body.Bytes(), &delResp); err != nil {
		t.Fatalf("unmarshal delete response: %v", err)
	}
	if !delResp.Deleted || delResp.OrgID != "acme" || delResp.AppName != "web" {
		t.Fatalf("unexpected delete response: %+v", delResp)
	}

	if apps := d.ListApps(); len(apps) != 0 {
		t.Fatalf("expected 0 apps after delete, got %d", len(apps))
	}
	if _, ok := d.router.GetRoute("acme", "web"); ok {
		t.Fatalf("expected route to be deleted")
	}

	events := d.Audit().List("acme", "web", 0)
	if len(events) == 0 || events[0].Type != "delete" {
		t.Fatalf("expected delete audit event as latest, got %+v", events)
	}

	delAgainReq := httptest.NewRequest(http.MethodDelete, "/v1/apps?org_id=acme&app=web", nil)
	delAgainReq.Header.Set("Authorization", "Bearer secret-token")
	delAgainRec := httptest.NewRecorder()
	h.ServeHTTP(delAgainRec, delAgainReq)
	if delAgainRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on second delete, got %d: %s", delAgainRec.Code, delAgainRec.Body.String())
	}
}

type recordingRuntime struct {
	mu       sync.Mutex
	specs    []ContainerSpec
	logCalls []string
	logOut   string
}

func (r *recordingRuntime) Start(_ context.Context, spec ContainerSpec) (string, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	r.mu.Unlock()
	return fmt.Sprintf("127.0.0.1:%d", spec.Port), nil
}

func (r *recordingRuntime) Stop(_ context.Context, _, _, _ string) error {
	return nil
}

func (r *recordingRuntime) Logs(_ context.Context, orgID, appName, releaseID string, tail int) (string, error) {
	r.mu.Lock()
	r.logCalls = append(r.logCalls, fmt.Sprintf("%s/%s/%s:%d", orgID, appName, releaseID, tail))
	out := r.logOut
	r.mu.Unlock()
	return out, nil
}

func TestDeployPassesEnvAndHealthcheckToContainerSpec(t *testing.T) {
	d := newTestServerDaemon(t)
	rt := &recordingRuntime{}
	d.SetRuntime(rt)

	cfg := &config.Config{
		App: config.AppConfig{
			Name:        "api",
			OrgID:       "acme",
			Domain:      "api.smallcloud.dev",
			Port:        8080,
			Healthcheck: "/readyz",
			Runtime:     "dockerfile",
		},
		Resources: config.ResourcesConfig{
			CPUMillicores: 500,
			MemoryMB:      256,
		},
		Budget: config.BudgetConfig{
			MaxMonthlyUSD: 20.0,
			AlertAtPct:    80.0,
			HardStop:      true,
		},
		Storage: config.StorageConfig{
			SQLitePath: "/data/api.db",
			SyncMs:     1000,
		},
		Env: map[string]string{
			"LOG_LEVEL": "debug",
			"APP_ENV":   "staging",
		},
	}

	if _, err := d.Deploy(cfg, "rel-env-1"); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.specs) != 1 {
		t.Fatalf("expected 1 runtime start call, got %d", len(rt.specs))
	}
	spec := rt.specs[0]
	if spec.Healthcheck != "/readyz" {
		t.Fatalf("expected healthcheck /readyz, got %q", spec.Healthcheck)
	}
	if spec.Env["LOG_LEVEL"] != "debug" || spec.Env["APP_ENV"] != "staging" {
		t.Fatalf("expected env map to be forwarded, got %+v", spec.Env)
	}
}

func TestWebhookNotifierOnBudgetAlert(t *testing.T) {
	d := newTestServerDaemon(t)

	received := make(chan WebhookPayload, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload WebhookPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case received <- payload:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d.SetWebhookNotifier(&WebhookNotifier{
		URL:    srv.URL,
		Secret: "hook-secret",
		Client: srv.Client(),
	})

	if err := d.budget.RegisterApp("acme", "web", 100.0, 80.0); err != nil {
		t.Fatalf("register app failed: %v", err)
	}
	if _, err := d.RecordSpend("acme", "web", 85.0); err != nil {
		t.Fatalf("record spend failed: %v", err)
	}

	select {
	case payload := <-received:
		if payload.Event != "budget_alert" || payload.OrgID != "acme" || payload.AppName != "web" {
			t.Fatalf("unexpected webhook payload: %+v", payload)
		}
		if payload.SpendUSD != 85.0 || payload.MaxMonthlyUSD != 100.0 {
			t.Fatalf("unexpected spend/max in payload: %+v", payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for budget alert webhook")
	}

	events := d.Audit().List("acme", "web", 0)
	found := false
	for _, ev := range events {
		if ev.Type == "budget_alert" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected budget_alert audit event, got %+v", events)
	}
}

func TestCanaryEndpoint(t *testing.T) {
	d := newTestServerDaemon(t)
	h := d.HandlerWithAuth("secret-token")

	unauthReq := httptest.NewRequest(http.MethodPost, "/v1/canary", bytes.NewBufferString(`{"org_id":"acme","app_name":"web","canary_weight":20}`))
	unauthRec := httptest.NewRecorder()
	h.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", unauthRec.Code)
	}

	missingReq := httptest.NewRequest(http.MethodPost, "/v1/canary", bytes.NewBufferString(`{"org_id":"acme","app_name":"web","canary_weight":20}`))
	missingReq.Header.Set("Authorization", "Bearer secret-token")
	missingRec := httptest.NewRecorder()
	h.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when route missing, got %d: %s", missingRec.Code, missingRec.Body.String())
	}

	cfg := &config.Config{
		App: config.AppConfig{
			Name:        "web",
			OrgID:       "acme",
			Domain:      "web.smallcloud.dev",
			Port:        8000,
			Healthcheck: "/healthz",
			Runtime:     "dockerfile",
		},
		Resources: config.ResourcesConfig{
			CPUMillicores: 250,
			MemoryMB:      128,
		},
		Budget: config.BudgetConfig{
			MaxMonthlyUSD: 10.0,
			AlertAtPct:    80.0,
			HardStop:      true,
		},
		Storage: config.StorageConfig{
			SQLitePath: "/data/web.db",
			SyncMs:     1000,
		},
	}
	if _, err := d.Deploy(cfg, "v1"); err != nil {
		t.Fatalf("deploy v1 failed: %v", err)
	}

	noStandbyReq := httptest.NewRequest(http.MethodPost, "/v1/canary", bytes.NewBufferString(`{"org_id":"acme","app_name":"web","canary_weight":25}`))
	noStandbyReq.Header.Set("Authorization", "Bearer secret-token")
	noStandbyRec := httptest.NewRecorder()
	h.ServeHTTP(noStandbyRec, noStandbyReq)
	if noStandbyRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when no standby upstream, got %d: %s", noStandbyRec.Code, noStandbyRec.Body.String())
	}

	if _, err := d.Deploy(cfg, "v2"); err != nil {
		t.Fatalf("deploy v2 failed: %v", err)
	}

	okReq := httptest.NewRequest(http.MethodPost, "/v1/canary", bytes.NewBufferString(`{"org_id":"acme","app_name":"web","canary_weight":25}`))
	okReq.Header.Set("Authorization", "Bearer secret-token")
	okRec := httptest.NewRecorder()
	h.ServeHTTP(okRec, okReq)
	if okRec.Code != http.StatusOK {
		t.Fatalf("expected 200 setting canary weight, got %d: %s", okRec.Code, okRec.Body.String())
	}

	var route RouteEntry
	if err := json.Unmarshal(okRec.Body.Bytes(), &route); err != nil {
		t.Fatalf("unmarshal canary route: %v", err)
	}
	if route.CanaryWeight != 25 || route.ActiveReleaseID != "v2" || route.StandbyReleaseID != "v1" {
		t.Fatalf("unexpected canary route: %+v", route)
	}

	events := d.Audit().List("acme", "web", 1)
	if len(events) != 1 || events[0].Type != "canary" {
		t.Fatalf("expected canary audit event, got %+v", events)
	}
}

func TestLogsEndpoint(t *testing.T) {
	d := newTestServerDaemon(t)
	rt := &recordingRuntime{logOut: "line1\nline2\n"}
	d.SetRuntime(rt)
	h := d.HandlerWithAuth("secret-token")

	unauthReq := httptest.NewRequest(http.MethodGet, "/v1/logs?org_id=acme&app=web", nil)
	unauthRec := httptest.NewRecorder()
	h.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", unauthRec.Code)
	}

	missingReq := httptest.NewRequest(http.MethodGet, "/v1/logs?org_id=acme&app=web", nil)
	missingReq.Header.Set("Authorization", "Bearer secret-token")
	missingRec := httptest.NewRecorder()
	h.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when route missing, got %d: %s", missingRec.Code, missingRec.Body.String())
	}

	cfg := &config.Config{
		App: config.AppConfig{
			Name:        "web",
			OrgID:       "acme",
			Domain:      "web.smallcloud.dev",
			Port:        8000,
			Healthcheck: "/healthz",
			Runtime:     "dockerfile",
		},
		Resources: config.ResourcesConfig{
			CPUMillicores: 250,
			MemoryMB:      128,
		},
		Budget: config.BudgetConfig{
			MaxMonthlyUSD: 10.0,
			AlertAtPct:    80.0,
			HardStop:      true,
		},
		Storage: config.StorageConfig{
			SQLitePath: "/data/web.db",
			SyncMs:     1000,
		},
	}
	if _, err := d.Deploy(cfg, "rel-active"); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	badTailReq := httptest.NewRequest(http.MethodGet, "/v1/logs?org_id=acme&app=web&tail=-5", nil)
	badTailReq.Header.Set("Authorization", "Bearer secret-token")
	badTailRec := httptest.NewRecorder()
	h.ServeHTTP(badTailRec, badTailReq)
	if badTailRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative tail, got %d", badTailRec.Code)
	}

	defaultReq := httptest.NewRequest(http.MethodGet, "/v1/logs?org_id=acme&app=web&tail=50", nil)
	defaultReq.Header.Set("Authorization", "Bearer secret-token")
	defaultRec := httptest.NewRecorder()
	h.ServeHTTP(defaultRec, defaultReq)
	if defaultRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on logs, got %d: %s", defaultRec.Code, defaultRec.Body.String())
	}
	var resp struct {
		OrgID     string `json:"org_id"`
		AppName   string `json:"app_name"`
		ReleaseID string `json:"release_id"`
		Logs      string `json:"logs"`
	}
	if err := json.Unmarshal(defaultRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal logs response: %v", err)
	}
	if resp.OrgID != "acme" || resp.AppName != "web" || resp.ReleaseID != "rel-active" || resp.Logs != "line1\nline2\n" {
		t.Fatalf("unexpected logs response: %+v", resp)
	}

	explicitReq := httptest.NewRequest(http.MethodGet, "/v1/logs?org_id=acme&app=web&release_id=rel-prev&tail=10", nil)
	explicitReq.Header.Set("Authorization", "Bearer secret-token")
	explicitRec := httptest.NewRecorder()
	h.ServeHTTP(explicitRec, explicitReq)
	if explicitRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on explicit release logs, got %d: %s", explicitRec.Code, explicitRec.Body.String())
	}

	rt.mu.Lock()
	calls := append([]string(nil), rt.logCalls...)
	rt.mu.Unlock()
	if len(calls) != 2 || calls[0] != "acme/web/rel-active:50" || calls[1] != "acme/web/rel-prev:10" {
		t.Fatalf("unexpected runtime log calls: %+v", calls)
	}
}

func TestOrgQuotaEndpoints(t *testing.T) {
	d := newTestServerDaemon(t)
	h := d.HandlerWithAuth("secret-token")

	unauthReq := httptest.NewRequest(http.MethodGet, "/v1/org-quota?org_id=acme", nil)
	unauthRec := httptest.NewRecorder()
	h.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", unauthRec.Code)
	}

	if err := d.budget.RegisterApp("acme", "api", 50.0, 80.0); err != nil {
		t.Fatalf("register api failed: %v", err)
	}
	if err := d.budget.RegisterApp("acme", "web", 50.0, 80.0); err != nil {
		t.Fatalf("register web failed: %v", err)
	}
	if _, err := d.RecordSpend("acme", "api", 15.0); err != nil {
		t.Fatalf("record spend api failed: %v", err)
	}
	if _, err := d.RecordSpend("acme", "web", 10.0); err != nil {
		t.Fatalf("record spend web failed: %v", err)
	}

	setReq := httptest.NewRequest(http.MethodPost, "/v1/org-quota", bytes.NewBufferString(`{"org_id":"acme","max_monthly_usd":20.0}`))
	setReq.Header.Set("Authorization", "Bearer secret-token")
	setRec := httptest.NewRecorder()
	h.ServeHTTP(setRec, setReq)
	if setRec.Code != http.StatusOK {
		t.Fatalf("expected 200 setting org quota, got %d: %s", setRec.Code, setRec.Body.String())
	}

	var setSummary OrgSpendSummary
	if err := json.Unmarshal(setRec.Body.Bytes(), &setSummary); err != nil {
		t.Fatalf("unmarshal set org quota: %v", err)
	}
	if setSummary.OrgID != "acme" || setSummary.MaxMonthlyUSD != 20.0 || setSummary.TotalSpendUSD != 25.0 || setSummary.AppCount != 2 || !setSummary.Exceeded {
		t.Fatalf("unexpected set org quota summary: %+v", setSummary)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/org-quota?org_id=acme", nil)
	getReq.Header.Set("Authorization", "Bearer secret-token")
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200 getting org quota, got %d: %s", getRec.Code, getRec.Body.String())
	}
	var getSummary OrgSpendSummary
	if err := json.Unmarshal(getRec.Body.Bytes(), &getSummary); err != nil {
		t.Fatalf("unmarshal get org quota: %v", err)
	}
	if getSummary != setSummary {
		t.Fatalf("GET org-quota mismatch: got %+v, want %+v", getSummary, setSummary)
	}

	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := d.SaveState(statePath); err != nil {
		t.Fatalf("save state failed: %v", err)
	}
	d2 := newTestServerDaemon(t)
	if err := d2.LoadState(statePath); err != nil {
		t.Fatalf("load state failed: %v", err)
	}
	restored, err := d2.OrgSummary("acme")
	if err != nil {
		t.Fatalf("restored org summary failed: %v", err)
	}
	if restored.MaxMonthlyUSD != 20.0 || !restored.Exceeded {
		t.Fatalf("expected restored org quota 20.0 exceeded=true, got %+v", restored)
	}
}

func TestPruneEndpoint(t *testing.T) {
	d := newTestServerDaemon(t)
	h := d.HandlerWithAuth("secret-token")

	unauthReq := httptest.NewRequest(http.MethodPost, "/v1/prune", bytes.NewBufferString(`{"org_id":"acme","app_name":"web","keep_last":1}`))
	unauthRec := httptest.NewRecorder()
	h.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", unauthRec.Code)
	}

	srcFile := filepath.Join(t.TempDir(), "web.db")
	for i := 1; i <= 3; i++ {
		if err := os.WriteFile(srcFile, []byte(fmt.Sprintf("sqlite-data-%d", i)), 0o600); err != nil {
			t.Fatalf("write sqlite fixture: %v", err)
		}
		if _, err := d.Storage().SyncWAL("acme", "web", srcFile); err != nil {
			t.Fatalf("sync wal %d: %v", i, err)
		}
	}

	badReq := httptest.NewRequest(http.MethodPost, "/v1/prune", bytes.NewBufferString(`{"org_id":"acme","app_name":"web","keep_last":0}`))
	badReq.Header.Set("Authorization", "Bearer secret-token")
	badRec := httptest.NewRecorder()
	h.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for keep_last=0, got %d: %s", badRec.Code, badRec.Body.String())
	}

	pruneReq := httptest.NewRequest(http.MethodPost, "/v1/prune", bytes.NewBufferString(`{"org_id":"acme","app_name":"web","keep_last":1}`))
	pruneReq.Header.Set("Authorization", "Bearer secret-token")
	pruneRec := httptest.NewRecorder()
	h.ServeHTTP(pruneRec, pruneReq)
	if pruneRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on prune, got %d: %s", pruneRec.Code, pruneRec.Body.String())
	}

	var pruneResp struct {
		Pruned int `json:"pruned"`
	}
	if err := json.Unmarshal(pruneRec.Body.Bytes(), &pruneResp); err != nil {
		t.Fatalf("unmarshal prune response: %v", err)
	}
	if pruneResp.Pruned != 2 {
		t.Fatalf("expected pruned=2, got %d", pruneResp.Pruned)
	}

	remaining, err := d.Storage().ListSnapshots("acme", "web")
	if err != nil {
		t.Fatalf("list snapshots failed: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Generation != 3 {
		t.Fatalf("expected only generation 3 remaining, got %+v", remaining)
	}
}

func TestWorkerNodesEndpoints(t *testing.T) {
	d := newTestServerDaemon(t)
	h := d.HandlerWithAuth("secret-token")

	unauthReq := httptest.NewRequest(http.MethodGet, "/v1/nodes", nil)
	unauthRec := httptest.NewRecorder()
	h.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", unauthRec.Code)
	}

	regReq := httptest.NewRequest(http.MethodPost, "/v1/nodes/register", bytes.NewBufferString(`{"id":"worker-1","address":"10.0.1.10:8080","region":"us-east"}`))
	regReq.Header.Set("Authorization", "Bearer secret-token")
	regRec := httptest.NewRecorder()
	h.ServeHTTP(regRec, regReq)
	if regRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on register, got %d: %s", regRec.Code, regRec.Body.String())
	}
	var regNode WorkerNode
	if err := json.Unmarshal(regRec.Body.Bytes(), &regNode); err != nil {
		t.Fatalf("unmarshal register response: %v", err)
	}
	if regNode.ID != "worker-1" || regNode.MaxApps != defaultWorkerMaxApps || !regNode.Healthy {
		t.Fatalf("unexpected registered node: %+v", regNode)
	}

	badRegReq := httptest.NewRequest(http.MethodPost, "/v1/nodes/register", bytes.NewBufferString(`{"id":"","address":"10.0.1.10:8080"}`))
	badRegReq.Header.Set("Authorization", "Bearer secret-token")
	badRegRec := httptest.NewRecorder()
	h.ServeHTTP(badRegRec, badRegReq)
	if badRegRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on invalid register, got %d", badRegRec.Code)
	}

	hbReq := httptest.NewRequest(http.MethodPost, "/v1/nodes/heartbeat", bytes.NewBufferString(`{"id":"worker-1","active_apps":4}`))
	hbReq.Header.Set("Authorization", "Bearer secret-token")
	hbRec := httptest.NewRecorder()
	h.ServeHTTP(hbRec, hbReq)
	if hbRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on heartbeat, got %d: %s", hbRec.Code, hbRec.Body.String())
	}
	var hbNode WorkerNode
	if err := json.Unmarshal(hbRec.Body.Bytes(), &hbNode); err != nil {
		t.Fatalf("unmarshal heartbeat response: %v", err)
	}
	if hbNode.ActiveApps != 4 {
		t.Fatalf("expected active_apps=4, got %d", hbNode.ActiveApps)
	}

	missingHbReq := httptest.NewRequest(http.MethodPost, "/v1/nodes/heartbeat", bytes.NewBufferString(`{"id":"missing","active_apps":1}`))
	missingHbReq.Header.Set("Authorization", "Bearer secret-token")
	missingHbRec := httptest.NewRecorder()
	h.ServeHTTP(missingHbRec, missingHbReq)
	if missingHbRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on missing heartbeat node, got %d", missingHbRec.Code)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/nodes", nil)
	listReq.Header.Set("Authorization", "Bearer secret-token")
	listRec := httptest.NewRecorder()
	h.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on list nodes, got %d: %s", listRec.Code, listRec.Body.String())
	}
	var listResp struct {
		Nodes []WorkerNode `json:"nodes"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal list nodes response: %v", err)
	}
	if len(listResp.Nodes) != 1 || listResp.Nodes[0].ID != "worker-1" || listResp.Nodes[0].ActiveApps != 4 {
		t.Fatalf("unexpected listed nodes: %+v", listResp.Nodes)
	}
}

func TestGitHubWebhookEndpoint(t *testing.T) {
	d := newTestServerDaemon(t)
	h := d.HandlerWithAuth("secret-token")

	t.Setenv("SMALLCLOUD_GITHUB_WEBHOOK_SECRET", "")
	unconfiguredReq := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewBufferString(`{}`))
	unconfiguredRec := httptest.NewRecorder()
	h.ServeHTTP(unconfiguredRec, unconfiguredReq)
	if unconfiguredRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when webhook secret is unset, got %d: %s", unconfiguredRec.Code, unconfiguredRec.Body.String())
	}

	const webhookSecret = "gh-webhook-secret"
	t.Setenv("SMALLCLOUD_GITHUB_WEBHOOK_SECRET", webhookSecret)

	payload := []byte(`{"ref":"refs/heads/main","after":"abc1234567890","repository":{"full_name":"acme/web"},"pusher":{"name":"alice"}}`)

	badSigReq := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(payload))
	badSigReq.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	badSigRec := httptest.NewRecorder()
	h.ServeHTTP(badSigRec, badSigReq)
	if badSigRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on invalid signature, got %d: %s", badSigRec.Code, badSigRec.Body.String())
	}

	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	validReq := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(payload))
	validReq.Header.Set("X-Hub-Signature-256", sig)
	validRec := httptest.NewRecorder()
	h.ServeHTTP(validRec, validReq)
	if validRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on valid webhook, got %d: %s", validRec.Code, validRec.Body.String())
	}

	var ev GitPushEvent
	if err := json.Unmarshal(validRec.Body.Bytes(), &ev); err != nil {
		t.Fatalf("unmarshal webhook response: %v", err)
	}
	if ev.Repository != "acme/web" || ev.Branch != "main" || ev.CommitSHA != "abc1234567890" || ev.Pusher != "alice" {
		t.Fatalf("unexpected webhook event: %+v", ev)
	}

	events := d.Audit().List("acme", "web", 0)
	if len(events) != 1 || events[0].Type != "gitops_push" || events[0].ReleaseID != "abc1234567890" {
		t.Fatalf("expected gitops_push audit event, got %+v", events)
	}
}

func TestReadyzEndpoint(t *testing.T) {
	d := newTestServerDaemon(t)
	cfg := &config.Config{
		App: config.AppConfig{
			Name:        "web",
			OrgID:       "acme",
			Domain:      "web.smallcloud.dev",
			Port:        8000,
			Healthcheck: "/healthz",
			Runtime:     "dockerfile",
		},
		Resources: config.ResourcesConfig{
			CPUMillicores: 250,
			MemoryMB:      128,
		},
		Budget: config.BudgetConfig{
			MaxMonthlyUSD: 10.0,
			AlertAtPct:    80.0,
			HardStop:      true,
		},
		Storage: config.StorageConfig{
			SQLitePath: "/data/web.db",
			SyncMs:     1000,
		},
	}
	if _, err := d.Deploy(cfg, "v1.0.0"); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}
	if _, err := d.WorkerPool().RegisterNode(WorkerNode{
		ID:      "node-1",
		Address: "10.0.0.1:8080",
	}); err != nil {
		t.Fatalf("register node failed: %v", err)
	}

	for _, h := range []http.Handler{d.HandlerWithAuth(""), d.HandlerWithAuth("secret-token")} {
		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 on /readyz, got %d: %s", rec.Code, rec.Body.String())
		}

		var body struct {
			Ready bool `json:"ready"`
			Apps  int  `json:"apps"`
			Nodes int  `json:"nodes"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal /readyz: %v", err)
		}
		if !body.Ready || body.Apps != 1 || body.Nodes != 1 {
			t.Fatalf("unexpected /readyz payload: %+v", body)
		}
	}
}

func TestDecodeJSONBodyRejectsTrailingGarbage(t *testing.T) {
	d := newTestServerDaemon(t)
	h := d.HandlerWithAuth("")

	for _, payload := range []string{
		`{"org_id":"acme","app_name":"web"}{"extra":1}`,
		`{"org_id":"acme","app_name":"web"} trailing`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/meter", bytes.NewBufferString(payload))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for trailing JSON %q, got %d: %s", payload, rec.Code, rec.Body.String())
		}
	}
}

func TestServerMiddlewareHeadersRequestIDAndMetrics(t *testing.T) {
	d := newTestServerDaemon(t)
	h := d.HandlerWithAuth("secret-token")

	if _, err := d.WorkerPool().RegisterNode(WorkerNode{
		ID:      "worker-metrics-1",
		Address: "10.0.2.1:8080",
	}); err != nil {
		t.Fatalf("register node failed: %v", err)
	}

	req1 := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 on /healthz, got %d", rec1.Code)
	}
	if rec1.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("expected nosniff header, got %q", rec1.Header().Get("X-Content-Type-Options"))
	}
	if rec1.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("expected DENY header, got %q", rec1.Header().Get("X-Frame-Options"))
	}
	if rec1.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("expected no-referrer header, got %q", rec1.Header().Get("Referrer-Policy"))
	}
	if rec1.Header().Get("Cache-Control") != "" {
		t.Fatalf("expected empty Cache-Control on /healthz, got %q", rec1.Header().Get("Cache-Control"))
	}
	if reqID := rec1.Header().Get("X-Request-ID"); len(reqID) != 32 {
		t.Fatalf("expected generated 32-char X-Request-ID, got %q", reqID)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	req2.Header.Set("X-Request-ID", "req-custom_42")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on unauthenticated /v1/apps, got %d", rec2.Code)
	}
	if rec2.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected Cache-Control no-store on /v1/apps, got %q", rec2.Header().Get("Cache-Control"))
	}
	if rec2.Header().Get("X-Request-ID") != "req-custom_42" {
		t.Fatalf("expected preserved X-Request-ID req-custom_42, got %q", rec2.Header().Get("X-Request-ID"))
	}

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	h.ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /metrics, got %d", metricsRec.Code)
	}
	body := metricsRec.Body.String()
	for _, expected := range []string{
		"smallcloud_http_requests_total 2\n",
		"smallcloud_http_errors_total 1\n",
		"smallcloud_worker_nodes_total 1\n",
		"smallcloud_worker_nodes_healthy 1\n",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected %q in /metrics output, got:\n%s", expected, body)
		}
	}
}

func TestRateLimitMiddlewareOnServer(t *testing.T) {
	t.Setenv("SMALLCLOUD_RATE_LIMIT", "2")
	d := newTestServerDaemon(t)
	h := d.HandlerWithAuth("")

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
		req.RemoteAddr = "198.51.100.5:4100"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}

	req3 := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	req3.RemoteAddr = "198.51.100.5:4101"
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on 3rd request, got %d: %s", rec3.Code, rec3.Body.String())
	}
	if rec3.Header().Get("Retry-After") != "60" {
		t.Fatalf("expected Retry-After 60, got %q", rec3.Header().Get("Retry-After"))
	}

	for _, probePath := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, probePath, nil)
		req.RemoteAddr = "198.51.100.5:4102"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 on %s, got %d: %s", probePath, rec.Code, rec.Body.String())
		}
	}
}

func TestBackupEndpoint(t *testing.T) {
	d := newTestServerDaemon(t)
	cfg := sampleConfig()
	if _, err := d.Deploy(cfg, "v1.0.0"); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	h := d.HandlerWithAuth("")

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/backup?org_id=%s&app=%s", cfg.App.OrgID, cfg.App.Name), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/gzip" {
		t.Fatalf("Content-Type = %q, want application/gzip", got)
	}
	wantDisp := fmt.Sprintf(`attachment; filename="%s-%s-backup.tar.gz"`, cfg.App.OrgID, cfg.App.Name)
	if got := rec.Header().Get("Content-Disposition"); got != wantDisp {
		t.Fatalf("Content-Disposition = %q, want %q", got, wantDisp)
	}

	bundlePath := filepath.Join(t.TempDir(), "downloaded.tar.gz")
	if err := os.WriteFile(bundlePath, rec.Body.Bytes(), 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	manifest, err := VerifyBackupBundle(bundlePath)
	if err != nil {
		t.Fatalf("VerifyBackupBundle: %v", err)
	}
	if manifest.OrgID != cfg.App.OrgID || manifest.AppName != cfg.App.Name {
		t.Fatalf("manifest tenant = %s/%s, want %s/%s", manifest.OrgID, manifest.AppName, cfg.App.OrgID, cfg.App.Name)
	}

	badReq := httptest.NewRequest(http.MethodGet, "/v1/backup?org_id=../bad&app=web", nil)
	badRec := httptest.NewRecorder()
	h.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid slugs, got %d: %s", badRec.Code, badRec.Body.String())
	}

	missingReq := httptest.NewRequest(http.MethodGet, "/v1/backup?org_id=acme&app=missing", nil)
	missingRec := httptest.NewRecorder()
	h.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown app, got %d: %s", missingRec.Code, missingRec.Body.String())
	}

	authH := d.HandlerWithAuth("secret-token")
	unauthReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/backup?org_id=%s&app=%s", cfg.App.OrgID, cfg.App.Name), nil)
	unauthRec := httptest.NewRecorder()
	authH.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without bearer token, got %d: %s", unauthRec.Code, unauthRec.Body.String())
	}

	authReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/backup?org_id=%s&app=%s", cfg.App.OrgID, cfg.App.Name), nil)
	authReq.Header.Set("Authorization", "Bearer secret-token")
	authRec := httptest.NewRecorder()
	authH.ServeHTTP(authRec, authReq)
	if authRec.Code != http.StatusOK {
		t.Fatalf("expected 200 with bearer token, got %d: %s", authRec.Code, authRec.Body.String())
	}
}
