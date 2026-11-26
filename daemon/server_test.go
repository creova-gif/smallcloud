package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
			name:       "healthz open without token",
			method:     http.MethodGet,
			path:       "/healthz",
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
