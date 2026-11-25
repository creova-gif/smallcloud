package daemon

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creova-gif/smallcloud/pkg/config"
)

func sampleConfig() *config.Config {
	return &config.Config{
		App: config.AppConfig{
			Name:        "micro-crm",
			OrgID:       "org-alpha",
			Domain:      "crm.smallcloud.dev",
			Port:        8000,
			Healthcheck: "/healthz",
			Runtime:     "dockerfile",
		},
		Resources: config.ResourcesConfig{
			CPUMillicores: 500,
			MemoryMB:      256,
		},
		Budget: config.BudgetConfig{
			MaxMonthlyUSD: 5.00,
			AlertAtPct:    80.0,
			HardStop:      true,
		},
		Storage: config.StorageConfig{
			SQLitePath: "/data/crm.db",
			ReplicaURL: "gs://smallcloud-backups/org-alpha/micro-crm",
			SyncMs:     1000,
		},
	}
}

func TestDaemonDeployRollbackAndZeroDowntimeSwap(t *testing.T) {
	d := NewDaemon(t.TempDir(), t.TempDir())
	cfg := sampleConfig()

	res1, err := d.Deploy(cfg, "v1.0.0")
	if err != nil {
		t.Fatalf("deploy v1 failed: %v", err)
	}
	if res1.ActiveColor != ColorBlue || res1.ReleaseID != "v1.0.0" {
		t.Fatalf("expected blue v1.0.0, got %+v", res1)
	}
	if res1.DeployTimeMs >= 30000 {
		t.Fatalf("deploy exceeded 30s SLA: %dms", res1.DeployTimeMs)
	}
	if res1.ColdStartMs >= 300 {
		t.Fatalf("cold start exceeded 300ms SLA: %dms", res1.ColdStartMs)
	}

	cpuMax, err := os.ReadFile(filepath.Join(res1.Cgroup.CgroupPath, "cpu.max"))
	if err != nil {
		t.Fatalf("read cpu.max: %v", err)
	}
	if strings.TrimSpace(string(cpuMax)) != "50000 100000" {
		t.Fatalf("expected cpu.max '50000 100000', got %q", string(cpuMax))
	}

	res2, err := d.Deploy(cfg, "v1.1.0")
	if err != nil {
		t.Fatalf("deploy v1.1.0 failed: %v", err)
	}
	if res2.ActiveColor != ColorGreen || res2.Route.StandbyReleaseID != "v1.0.0" {
		t.Fatalf("expected green active and v1.0.0 standby, got %+v", res2.Route)
	}

	rolled, err := d.Rollback(cfg.App.OrgID, cfg.App.Name)
	if err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	if rolled.ActiveReleaseID != "v1.0.0" || rolled.ActiveColor != ColorBlue {
		t.Fatalf("expected rollback to v1.0.0 (blue), got %+v", rolled)
	}
}

func TestBudgetCircuitBreakerHardStop(t *testing.T) {
	d := NewDaemon(t.TempDir(), t.TempDir())
	cfg := sampleConfig()

	if _, err := d.Deploy(cfg, "v1.0.0"); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	st, err := d.Budget().RecordSpend(cfg.App.OrgID, cfg.App.Name, 4.00)
	if err != nil {
		t.Fatalf("unexpected error at 80%% spend: %v", err)
	}
	if st.Status != BudgetWarning || st.Halted {
		t.Fatalf("expected warning status without halt, got %+v", st)
	}

	st, err = d.Budget().RecordSpend(cfg.App.OrgID, cfg.App.Name, 1.00)
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}
	if !st.Halted || st.Status != BudgetHardStop {
		t.Fatalf("expected halted hard_stop state, got %+v", st)
	}

	route, ok := d.Router().GetRoute(cfg.App.OrgID, cfg.App.Name)
	if !ok || !route.Halted {
		t.Fatalf("expected route to be halted when budget tripped, got %+v", route)
	}
	caddyCfg := RenderCaddyfile(route, "/healthz")
	if !strings.Contains(caddyCfg, "402 Payment Required") {
		t.Fatalf("expected halted Caddyfile to respond with 402, got:\n%s", caddyCfg)
	}

	if _, err := d.Deploy(cfg, "v1.2.0"); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected deploy to be blocked by ErrBudgetExceeded, got %v", err)
	}
}

func TestLitestreamZeroDataLossOnRestart(t *testing.T) {
	d := NewDaemon(t.TempDir(), t.TempDir())
	workDir := t.TempDir()
	dbPath := filepath.Join(workDir, "app.db")

	originalPayload := []byte("SQLite format 3\x00---tenant:org-alpha---rows:1024---committed-wal-frame-99")
	if err := os.WriteFile(dbPath, originalPayload, 0o600); err != nil {
		t.Fatalf("write db: %v", err)
	}

	snap, err := d.Storage().SyncWAL("org-alpha", "micro-crm", dbPath)
	if err != nil {
		t.Fatalf("SyncWAL failed: %v", err)
	}
	if snap.SizeBytes != int64(len(originalPayload)) || snap.Generation != 1 {
		t.Fatalf("unexpected snapshot metadata: %+v", snap)
	}

	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("remove db: %v", err)
	}

	restoredMeta, err := d.Storage().Restore("org-alpha", "micro-crm", dbPath)
	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	if restoredMeta.SHA256 != snap.SHA256 {
		t.Fatalf("SHA256 mismatch after restore: %s != %s", restoredMeta.SHA256, snap.SHA256)
	}

	restoredBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read restored db: %v", err)
	}
	if !bytes.Equal(restoredBytes, originalPayload) {
		t.Fatalf("data loss detected after container restart")
	}
}
