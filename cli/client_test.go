package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creova-gif/smallcloud/daemon"
	"github.com/creova-gif/smallcloud/pkg/config"
)

func testClientConfig() *config.Config {
	return &config.Config{
		App: config.AppConfig{
			Name:        "micro-crm",
			OrgID:       "org-alpha",
			Domain:      "crm.example.com",
			Port:        8000,
			Healthcheck: "/healthz",
			Runtime:     "dockerfile",
		},
		Resources: config.ResourcesConfig{
			CPUMillicores: 250,
			MemoryMB:      256,
		},
		Budget: config.BudgetConfig{
			MaxMonthlyUSD: 5.0,
			AlertAtPct:    80,
			HardStop:      true,
		},
		Storage: config.StorageConfig{
			SQLitePath: "/data/app.db",
			ReplicaURL: "gs://smallcloud-replicas/org-alpha/micro-crm",
			SyncMs:     1000,
		},
	}
}

func TestDaemonClientLifecycleAndAuth(t *testing.T) {
	tmp := t.TempDir()
	d := daemon.NewDaemon(filepath.Join(tmp, "cgroups"), filepath.Join(tmp, "replicas"))
	srv := httptest.NewServer(d.HandlerWithAuth("secret-tok"))
	defer srv.Close()

	client := NewDaemonClient(srv.URL, "secret-tok")
	ctx := context.Background()
	cfg := testClientConfig()

	res1, err := client.Deploy(ctx, cfg, "v1.0.0")
	if err != nil {
		t.Fatalf("deploy v1.0.0 failed: %v", err)
	}
	if res1.ReleaseID != "v1.0.0" || res1.ActiveColor != daemon.ColorBlue {
		t.Fatalf("unexpected first deploy result: %+v", res1)
	}

	res2, err := client.Deploy(ctx, cfg, "v1.1.0")
	if err != nil {
		t.Fatalf("deploy v1.1.0 failed: %v", err)
	}
	if res2.ReleaseID != "v1.1.0" || res2.ActiveColor != daemon.ColorGreen {
		t.Fatalf("unexpected second deploy result: %+v", res2)
	}

	rolledBack, err := client.Rollback(ctx, cfg.App.OrgID, cfg.App.Name)
	if err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	if rolledBack.ActiveReleaseID != "v1.0.0" || rolledBack.ActiveColor != daemon.ColorBlue {
		t.Fatalf("unexpected rollback route: %+v", rolledBack)
	}

	st, err := client.Status(ctx, cfg.App.OrgID, cfg.App.Name)
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if st.Route.ActiveReleaseID != "v1.0.0" || st.Budget.MaxMonthlyUSD != 5.0 {
		t.Fatalf("unexpected status: %+v", st)
	}

	if _, err := d.RecordSpend(cfg.App.OrgID, cfg.App.Name, 5.0); !errors.Is(err, daemon.ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded from RecordSpend, got %v", err)
	}

	if _, err := client.Deploy(ctx, cfg, "v1.2.0"); !errors.Is(err, daemon.ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded on 402 deploy, got %v", err)
	}

	budgetState, err := client.SetMaxBudget(ctx, cfg.App.OrgID, cfg.App.Name, 25.0)
	if err != nil {
		t.Fatalf("set max budget failed: %v", err)
	}
	if budgetState.MaxMonthlyUSD != 25.0 || budgetState.Halted {
		t.Fatalf("expected unhalted budget with max 25.0, got %+v", budgetState)
	}

	rolled, err := client.Rollover(ctx, time.Date(2099, 2, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("rollover failed: %v", err)
	}
	if rolled != 1 {
		t.Fatalf("expected 1 rolled over app, got %d", rolled)
	}

	metered, err := client.Meter(ctx, cfg.App.OrgID, cfg.App.Name, 10.0)
	if err != nil {
		t.Fatalf("meter failed: %v", err)
	}
	if metered.CurrentUSD != 10.0 || metered.Halted {
		t.Fatalf("unexpected metered state: %+v", metered)
	}

	if _, err := client.Meter(ctx, cfg.App.OrgID, cfg.App.Name, 15.0); !errors.Is(err, daemon.ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded on 402 meter, got %v", err)
	}

	apps, err := client.ListApps(ctx)
	if err != nil {
		t.Fatalf("list apps failed: %v", err)
	}
	if len(apps) != 1 || apps[0].OrgID != cfg.App.OrgID || apps[0].AppName != cfg.App.Name {
		t.Fatalf("unexpected listed apps: %+v", apps)
	}

	unauthClient := NewDaemonClient(srv.URL, "wrong-tok")
	_, err = unauthClient.Status(ctx, cfg.App.OrgID, cfg.App.Name)
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("expected 401 unauthorized error, got %v", err)
	}

	noTokenClient := NewDaemonClient(srv.URL, "")
	_, err = noTokenClient.Deploy(ctx, cfg, "v1.3.0")
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("expected 401 unauthorized error without token, got %v", err)
	}
}

func TestDaemonClientEventsAndDeleteApp(t *testing.T) {
	tmp := t.TempDir()
	d := daemon.NewDaemon(filepath.Join(tmp, "cgroups"), filepath.Join(tmp, "replicas"))
	srv := httptest.NewServer(d.HandlerWithAuth("secret-tok"))
	defer srv.Close()

	client := NewDaemonClient(srv.URL, "secret-tok")
	ctx := context.Background()
	cfg := testClientConfig()

	if _, err := client.Deploy(ctx, cfg, "v1.0.0"); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	scopedEvents, err := client.Events(ctx, cfg.App.OrgID, cfg.App.Name, 10)
	if err != nil {
		t.Fatalf("scoped events failed: %v", err)
	}
	if len(scopedEvents) == 0 {
		t.Fatal("expected non-empty scoped events after deploy")
	}

	allEvents, err := client.Events(ctx, "", "", 0)
	if err != nil {
		t.Fatalf("all events failed: %v", err)
	}
	if len(allEvents) == 0 {
		t.Fatal("expected non-empty all events after deploy")
	}

	if err := client.DeleteApp(ctx, cfg.App.OrgID, cfg.App.Name); err != nil {
		t.Fatalf("delete app failed: %v", err)
	}

	apps, err := client.ListApps(ctx)
	if err != nil {
		t.Fatalf("list apps after delete failed: %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("expected 0 apps after delete, got %+v", apps)
	}

	if _, err := client.Status(ctx, cfg.App.OrgID, cfg.App.Name); err == nil {
		t.Fatal("expected status after delete to fail")
	}
}

func TestDaemonClientCanaryLogsOrgQuotaAndPrune(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/canary":
			var req struct {
				OrgID      string `json:"org_id"`
				AppName    string `json:"app_name"`
				StandbyPct int    `json:"standby_pct"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode canary request: %v", err)
			}
			if req.OrgID != "org-alpha" || req.AppName != "micro-crm" || req.StandbyPct != 25 {
				t.Fatalf("unexpected canary request: %+v", req)
			}
			_ = json.NewEncoder(w).Encode(daemon.RouteEntry{
				OrgID:            req.OrgID,
				AppName:          req.AppName,
				ActiveColor:      daemon.ColorBlue,
				ActiveReleaseID:  "v1.1.0",
				StandbyReleaseID: "v1.0.0",
				CanaryWeight:     req.StandbyPct,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/logs":
			q := r.URL.Query()
			if q.Get("org_id") != "org-alpha" || q.Get("app_name") != "micro-crm" || q.Get("release_id") != "v1.0.0" || q.Get("tail") != "50" {
				t.Fatalf("unexpected logs query: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"logs": "line1\nline2"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/org-quota":
			var req struct {
				OrgID         string  `json:"org_id"`
				MaxMonthlyUSD float64 `json:"max_monthly_usd"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode org-quota request: %v", err)
			}
			if req.OrgID != "org-alpha" || req.MaxMonthlyUSD != 25.5 {
				t.Fatalf("unexpected org-quota request: %+v", req)
			}
			_ = json.NewEncoder(w).Encode(daemon.OrgSpendSummary{
				OrgID:         req.OrgID,
				MaxMonthlyUSD: req.MaxMonthlyUSD,
				TotalSpendUSD: 3.25,
				AppCount:      2,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/org-quota":
			if r.URL.Query().Get("org_id") != "org-alpha" {
				t.Fatalf("unexpected org-quota query: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(daemon.OrgSpendSummary{
				OrgID:         "org-alpha",
				MaxMonthlyUSD: 25.5,
				TotalSpendUSD: 3.25,
				AppCount:      2,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/prune":
			var req struct {
				OrgID    string `json:"org_id"`
				AppName  string `json:"app_name"`
				KeepLast int    `json:"keep_last"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode prune request: %v", err)
			}
			if req.OrgID != "org-alpha" || req.AppName != "micro-crm" || req.KeepLast != 3 {
				t.Fatalf("unexpected prune request: %+v", req)
			}
			_ = json.NewEncoder(w).Encode(map[string]int{"pruned": 2})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := NewDaemonClient(srv.URL, "secret-tok")
	ctx := context.Background()

	route, err := client.SetCanaryWeight(ctx, "org-alpha", "micro-crm", 25)
	if err != nil {
		t.Fatalf("SetCanaryWeight failed: %v", err)
	}
	if route.CanaryWeight != 25 || route.ActiveReleaseID != "v1.1.0" {
		t.Fatalf("unexpected canary route: %+v", route)
	}

	logs, err := client.Logs(ctx, "org-alpha", "micro-crm", "v1.0.0", 50)
	if err != nil {
		t.Fatalf("Logs failed: %v", err)
	}
	if logs != "line1\nline2" {
		t.Fatalf("unexpected logs: %q", logs)
	}

	setSummary, err := client.SetOrgQuota(ctx, "org-alpha", 25.5)
	if err != nil {
		t.Fatalf("SetOrgQuota failed: %v", err)
	}
	if setSummary.MaxMonthlyUSD != 25.5 || setSummary.AppCount != 2 {
		t.Fatalf("unexpected SetOrgQuota summary: %+v", setSummary)
	}

	gotSummary, err := client.OrgSummary(ctx, "org-alpha")
	if err != nil {
		t.Fatalf("OrgSummary failed: %v", err)
	}
	if gotSummary.MaxMonthlyUSD != 25.5 || gotSummary.TotalSpendUSD != 3.25 {
		t.Fatalf("unexpected OrgSummary: %+v", gotSummary)
	}

	pruned, err := client.PruneSnapshots(ctx, "org-alpha", "micro-crm", 3)
	if err != nil {
		t.Fatalf("PruneSnapshots failed: %v", err)
	}
	if pruned != 2 {
		t.Fatalf("expected 2 pruned snapshots, got %d", pruned)
	}
}

func TestDaemonClientNodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/nodes/register":
			var req daemon.WorkerNode
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode register node request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(req)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/nodes":
			_ = json.NewEncoder(w).Encode(map[string][]daemon.WorkerNode{
				"nodes": {{}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := NewDaemonClient(srv.URL, "secret-tok")
	ctx := context.Background()

	if _, err := client.RegisterNode(ctx, daemon.WorkerNode{}); err != nil {
		t.Fatalf("RegisterNode failed: %v", err)
	}

	nodes, err := client.ListNodes(ctx)
	if err != nil {
		t.Fatalf("ListNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
}

func TestDaemonClientDownloadBackup(t *testing.T) {
	payload := []byte("fake-gzip-backup-archive-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/backup" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		if q.Get("org_id") != "org-alpha" || q.Get("app") != "micro-crm" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid query"}`))
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	ctx := context.Background()
	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "nested", "backup.tar.gz")

	client := NewDaemonClient(srv.URL, "secret-tok")
	if err := client.DownloadBackup(ctx, "org-alpha", "micro-crm", destPath); err != nil {
		t.Fatalf("DownloadBackup failed: %v", err)
	}
	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read downloaded backup: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("downloaded backup mismatch: got %q, want %q", got, payload)
	}

	entries, err := os.ReadDir(filepath.Dir(destPath))
	if err != nil {
		t.Fatalf("readdir dest: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "backup.tar.gz" {
		t.Fatalf("expected only backup.tar.gz in dest dir, got %+v", entries)
	}

	unauthPath := filepath.Join(destDir, "unauth.tar.gz")
	unauthClient := NewDaemonClient(srv.URL, "wrong-tok")
	err = unauthClient.DownloadBackup(ctx, "org-alpha", "micro-crm", unauthPath)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error, got %v", err)
	}
	if _, statErr := os.Stat(unauthPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected no file written on error, got %v", statErr)
	}
}
