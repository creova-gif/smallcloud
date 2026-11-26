package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestBudgetCircuitBreakerHardStopAndUnhalt(t *testing.T) {
	d := NewDaemon(t.TempDir(), t.TempDir())
	cfg := sampleConfig()

	if _, err := d.Deploy(cfg, "v1.0.0"); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	st, err := d.RecordSpend(cfg.App.OrgID, cfg.App.Name, 4.00)
	if err != nil {
		t.Fatalf("unexpected error at 80%% spend: %v", err)
	}
	if st.Status != BudgetWarning || st.Halted {
		t.Fatalf("expected warning status without halt, got %+v", st)
	}

	st, err = d.RecordSpend(cfg.App.OrgID, cfg.App.Name, 1.00)
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

	resumed, err := d.SetMaxBudget(cfg.App.OrgID, cfg.App.Name, 20.00)
	if err != nil {
		t.Fatalf("SetMaxBudget failed: %v", err)
	}
	if resumed.Halted || resumed.Status != BudgetOK {
		t.Fatalf("expected unhalted ok status after budget top-up, got %+v", resumed)
	}
	route, _ = d.Router().GetRoute(cfg.App.OrgID, cfg.App.Name)
	if route.Halted {
		t.Fatalf("expected CaddyRouter route to be unhalted via onUnhalt, got %+v", route)
	}
	if len(resumed.Events) < 4 || resumed.Events[len(resumed.Events)-1].Status != BudgetResumed {
		t.Fatalf("expected BudgetResumed audit event, got %+v", resumed.Events)
	}
}

func TestSecurityGuardsAndChecksumVerification(t *testing.T) {
	d := NewDaemon(t.TempDir(), t.TempDir())
	cfg := sampleConfig()
	if _, err := d.Deploy(cfg, "v1.0.0"); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	for _, badDelta := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.01} {
		if _, err := d.RecordSpend(cfg.App.OrgID, cfg.App.Name, badDelta); err == nil {
			t.Fatalf("expected error for invalid delta %v", badDelta)
		}
	}

	if _, err := d.Storage().SyncWAL("../etc", "micro-crm", "/tmp/x.db"); err == nil {
		t.Fatal("expected path traversal slug rejection")
	}

	workDir := t.TempDir()
	dbPath := filepath.Join(workDir, "app.db")
	if err := os.WriteFile(dbPath, []byte("valid-sqlite-payload"), 0o600); err != nil {
		t.Fatalf("write db: %v", err)
	}
	if _, err := d.Storage().SyncWAL("org-alpha", "micro-crm", dbPath); err != nil {
		t.Fatalf("SyncWAL failed: %v", err)
	}

	replicaFile := filepath.Join(d.Storage().ReplicaDir(), "org-alpha", "micro-crm", "replica.db")
	if err := os.WriteFile(replicaFile, []byte("corrupted-bytes"), 0o600); err != nil {
		t.Fatalf("corrupt replica: %v", err)
	}
	if _, err := d.Storage().Restore("org-alpha", "micro-crm", dbPath); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", err)
	}
}

func TestLitestreamZeroDataLossAndAutoSwapHandoff(t *testing.T) {
	repDir := t.TempDir()
	d := NewDaemon(t.TempDir(), repDir)
	cfg := sampleConfig()
	hostDB := filepath.Join(repDir, "hostdata", cfg.App.OrgID, cfg.App.Name, "crm.db")
	if err := os.MkdirAll(filepath.Dir(hostDB), 0o750); err != nil {
		t.Fatalf("mkdir hostdata: %v", err)
	}
	originalPayload := []byte("SQLite format 3\x00---tenant:org-alpha---rows:1024---committed-wal-frame-99")
	if err := os.WriteFile(hostDB, originalPayload, 0o600); err != nil {
		t.Fatalf("write host db: %v", err)
	}

	res, err := d.Deploy(cfg, "v1.0.0")
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}
	if res.LastSnapshot == nil || res.LastSnapshot.Generation != 1 || res.LastSnapshot.SizeBytes != int64(len(originalPayload)) {
		t.Fatalf("expected automatic SyncWAL snapshot on Deploy, got %+v", res.LastSnapshot)
	}

	if err := os.Remove(hostDB); err != nil {
		t.Fatalf("remove host db: %v", err)
	}
	res2, err := d.Deploy(cfg, "v1.1.0")
	if err != nil {
		t.Fatalf("Deploy v1.1.0 failed: %v", err)
	}
	if res2.LastSnapshot == nil || res2.LastSnapshot.SHA256 != res.LastSnapshot.SHA256 {
		t.Fatalf("expected automatic Restore on missing host db, got %+v", res2.LastSnapshot)
	}

	restoredBytes, err := os.ReadFile(hostDB)
	if err != nil {
		t.Fatalf("read restored db: %v", err)
	}
	if !bytes.Equal(restoredBytes, originalPayload) {
		t.Fatalf("data loss detected after container restart")
	}
}

func TestProxyApplierRollbackOnFailure(t *testing.T) {
	failNext := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failNext {
			http.Error(w, "invalid caddyfile", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDaemon(t.TempDir(), t.TempDir())
	d.Router().SetApplier(HTTPAdminApplier{AdminURL: srv.URL})
	cfg := sampleConfig()

	if _, err := d.Deploy(cfg, "v1.0.0"); err != nil {
		t.Fatalf("deploy v1 failed: %v", err)
	}
	failNext = true
	if _, err := d.Deploy(cfg, "v1.1.0"); err == nil {
		t.Fatal("expected deploy v1.1.0 to fail when Caddy admin returns 500")
	}

	route, ok := d.Router().GetRoute(cfg.App.OrgID, cfg.App.Name)
	if !ok || route.ActiveReleaseID != "v1.0.0" || route.ActiveColor != ColorBlue {
		t.Fatalf("expected route to remain reverted to v1.0.0 (blue), got %+v", route)
	}
}

func TestDaemonHTTPServerAndStatePersistence(t *testing.T) {
	cgRoot := t.TempDir()
	repRoot := t.TempDir()
	stateFile := filepath.Join(t.TempDir(), "state.json")

	d := NewDaemon(cgRoot, repRoot)
	if err := d.LoadState(stateFile); err != nil {
		t.Fatalf("missing state.json should succeed: %v", err)
	}

	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	cfg := sampleConfig()
	deployBody, _ := json.Marshal(map[string]any{"config": cfg, "release_id": "v1.0.0"})
	resp, err := http.Post(srv.URL+"/v1/deploy", "application/json", bytes.NewReader(deployBody))
	if err != nil {
		t.Fatalf("POST /v1/deploy failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on deploy v1, got %d", resp.StatusCode)
	}

	deployBody2, _ := json.Marshal(map[string]any{"config": cfg, "release_id": "v1.1.0"})
	resp2, err := http.Post(srv.URL+"/v1/deploy", "application/json", bytes.NewReader(deployBody2))
	if err != nil {
		t.Fatalf("POST /v1/deploy v1.1 failed: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on deploy v1.1, got %d", resp2.StatusCode)
	}

	statusResp, err := http.Get(srv.URL + "/v1/status?org_id=org-alpha&app_name=micro-crm")
	if err != nil {
		t.Fatalf("GET /v1/status failed: %v", err)
	}
	var st AppStatus
	if err := json.NewDecoder(statusResp.Body).Decode(&st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	_ = statusResp.Body.Close()
	if st.Route.ActiveReleaseID != "v1.1.0" {
		t.Fatalf("expected active v1.1.0, got %+v", st)
	}

	meterBody, _ := json.Marshal(map[string]any{"org_id": "org-alpha", "app_name": "micro-crm", "delta_usd": 5.0})
	meterResp, err := http.Post(srv.URL+"/v1/meter", "application/json", bytes.NewReader(meterBody))
	if err != nil {
		t.Fatalf("POST /v1/meter failed: %v", err)
	}
	_ = meterResp.Body.Close()
	if meterResp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("expected 402 on budget trip, got %d", meterResp.StatusCode)
	}

	if err := d.SaveState(stateFile); err != nil {
		t.Fatalf("SaveState failed: %v", err)
	}

	d2 := NewDaemon(cgRoot, repRoot)
	if err := d2.LoadState(stateFile); err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	reloaded, err := d2.Status("org-alpha", "micro-crm")
	if err != nil {
		t.Fatalf("Status after reload failed: %v", err)
	}
	if !reloaded.Budget.Halted || reloaded.Route.StandbyReleaseID != "v1.0.0" {
		t.Fatalf("unexpected reloaded state: %+v", reloaded)
	}

	oversized := append([]byte(`{"release_id":"`), bytes.Repeat([]byte("a"), maxRequestBodyBytes+64)...)
	oversized = append(oversized, []byte(`"}`)...)
	respBig, err := http.Post(srv.URL+"/v1/deploy", "application/json", bytes.NewReader(oversized))
	if err != nil {
		t.Fatalf("oversized request failed: %v", err)
	}
	_ = respBig.Body.Close()
	if respBig.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized body, got %d", respBig.StatusCode)
	}
}

func TestConcurrentSpendAndStatusRaceSafety(t *testing.T) {
	d := NewDaemon(t.TempDir(), t.TempDir())
	cfg := sampleConfig()
	if _, err := d.Deploy(cfg, "v1.0.0"); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = d.RecordSpend(cfg.App.OrgID, cfg.App.Name, 0.25)
			_, _ = d.Status(cfg.App.OrgID, cfg.App.Name)
		}()
	}
	wg.Wait()

	st, err := d.Status(cfg.App.OrgID, cfg.App.Name)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if !st.Budget.Halted || !st.Route.Halted {
		t.Fatalf("expected both budget and route halted after 25x$0.25, got %+v", st)
	}
}
