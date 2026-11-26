package cli

import (
	"context"
	"errors"
	"net/http/httptest"
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
