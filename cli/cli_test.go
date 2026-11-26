package cli

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestPackageFastAPITemplate(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")
	manifest, err := PackageApp(templateDir)
	if err != nil {
		t.Fatalf("PackageApp failed: %v", err)
	}
	if manifest.Config.App.Name != "fastapi-starter" {
		t.Fatalf("expected app name fastapi-starter, got %q", manifest.Config.App.Name)
	}
	if manifest.Config.Budget.MaxMonthlyUSD != 5.00 {
		t.Fatalf("expected max_monthly_usd 5.00, got %v", manifest.Config.Budget.MaxMonthlyUSD)
	}
	if !strings.Contains(manifest.Caddyfile, "reverse_proxy 127.0.0.1:8000") {
		t.Fatalf("expected Caddyfile reverse_proxy, got:\n%s", manifest.Caddyfile)
	}
	if !strings.Contains(manifest.LitestreamYML, "gs://smallcloud-replicas/org-indie-default/fastapi-starter") {
		t.Fatalf("expected Litestream replica URL, got:\n%s", manifest.LitestreamYML)
	}
}

func TestCLIInitAndErrors(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), "my-micro-saas")
	var out bytes.Buffer
	if err := Run([]string{"init", appDir}, t.TempDir(), &out); err != nil {
		t.Fatalf("cli init failed: %v", err)
	}
	tomlPath := filepath.Join(appDir, "smallcloud.toml")
	if _, err := os.Stat(tomlPath); err != nil {
		t.Fatalf("expected smallcloud.toml to exist: %v", err)
	}
	if err := Run([]string{"init", appDir}, t.TempDir(), &out); err == nil {
		t.Fatal("expected init without --force to refuse overwrite")
	}
	if err := Run([]string{"init", appDir, "--force"}, t.TempDir(), &out); err != nil {
		t.Fatalf("init with --force failed: %v", err)
	}
}

func TestCLIDeployRollbackStatusBudgetAndEval(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")
	stateDir := t.TempDir()

	var deploy1Out bytes.Buffer
	if err := Run([]string{"deploy", templateDir, "v1.0.0"}, stateDir, &deploy1Out); err != nil {
		t.Fatalf("cli deploy v1 failed: %v", err)
	}
	var deployRes1 daemon.DeploymentResult
	if err := json.Unmarshal(deploy1Out.Bytes(), &deployRes1); err != nil {
		t.Fatalf("unmarshal deploy1 result: %v", err)
	}
	if deployRes1.ReleaseID != "v1.0.0" || deployRes1.ActiveColor != daemon.ColorBlue {
		t.Fatalf("unexpected deploy1 result: %+v", deployRes1)
	}

	if err := Run([]string{"rollback", templateDir}, stateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected rollback with single release to fail")
	}

	var deploy2Out bytes.Buffer
	if err := Run([]string{"deploy", templateDir, "v1.1.0"}, stateDir, &deploy2Out); err != nil {
		t.Fatalf("cli deploy v1.1 failed: %v", err)
	}

	var rollbackOut bytes.Buffer
	if err := Run([]string{"rollback", templateDir}, stateDir, &rollbackOut); err != nil {
		t.Fatalf("cli rollback failed: %v", err)
	}
	var routeRes daemon.RouteEntry
	if err := json.Unmarshal(rollbackOut.Bytes(), &routeRes); err != nil {
		t.Fatalf("unmarshal rollback result: %v", err)
	}
	if routeRes.ActiveReleaseID != "v1.0.0" || routeRes.StandbyReleaseID != "v1.1.0" {
		t.Fatalf("expected rollback to restore v1.0.0 from v1.1.0, got %+v", routeRes)
	}

	var statusOut bytes.Buffer
	if err := Run([]string{"status", templateDir}, stateDir, &statusOut); err != nil {
		t.Fatalf("cli status failed: %v", err)
	}
	var st daemon.AppStatus
	if err := json.Unmarshal(statusOut.Bytes(), &st); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if st.Route.ActiveReleaseID != "v1.0.0" {
		t.Fatalf("expected status active v1.0.0, got %+v", st)
	}

	var budgetOut bytes.Buffer
	if err := Run([]string{"budget", "set", templateDir, "12.50"}, stateDir, &budgetOut); err != nil {
		t.Fatalf("cli budget set failed: %v", err)
	}
	var bState daemon.AppBudgetState
	if err := json.Unmarshal(budgetOut.Bytes(), &bState); err != nil {
		t.Fatalf("unmarshal budget state: %v", err)
	}
	if bState.MaxMonthlyUSD != 12.50 {
		t.Fatalf("expected max_monthly_usd 12.50, got %v", bState.MaxMonthlyUSD)
	}

	var evalOut bytes.Buffer
	if err := Run([]string{"eval"}, t.TempDir(), &evalOut); err != nil {
		t.Fatalf("cli eval failed: %v", err)
	}
	var report EvalReport
	if err := json.Unmarshal(evalOut.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal eval report: %v", err)
	}
	if !report.Passed || report.HardStopEnforcement != 100.0 || report.DataLossBytes != 0 {
		t.Fatalf("eval thresholds not met: %+v", report)
	}

	var doctorOut bytes.Buffer
	if err := Run([]string{"doctor", templateDir}, stateDir, &doctorOut); err != nil {
		t.Fatalf("cli doctor failed: %v", err)
	}
	var docReport DoctorReport
	if err := json.Unmarshal(doctorOut.Bytes(), &docReport); err != nil {
		t.Fatalf("unmarshal doctor report: %v", err)
	}
	if !docReport.Passed {
		t.Fatalf("expected doctor Passed=true, got %+v", docReport)
	}
}

func TestCLIServeGracefulShutdownAndPersist(t *testing.T) {
	stateDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var out bytes.Buffer
	if err := ServeWithContext(ctx, "127.0.0.1:0", stateDir, &out); err != nil {
		t.Fatalf("ServeWithContext failed: %v", err)
	}
	if !strings.Contains(out.String(), "listening") {
		t.Fatalf("expected listening JSON output, got %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(stateDir, "state.json")); err != nil {
		t.Fatalf("expected state.json persisted on shutdown: %v", err)
	}
}

func TestCLIServeGracefulShutdownSyncsWAL(t *testing.T) {
	stateDir := t.TempDir()
	statePath := filepath.Join(stateDir, "state.json")
	replicaDir := filepath.Join(stateDir, "replicas")

	seed := daemon.NewDaemon(filepath.Join(stateDir, "cgroups"), replicaDir)
	cfg := &config.Config{
		App: config.AppConfig{
			Name:        "wal-app",
			OrgID:       "org-wal",
			Domain:      "wal.smallcloud.dev",
			Port:        8000,
			Healthcheck: "/healthz",
			Runtime:     "dockerfile",
		},
		Resources: config.ResourcesConfig{
			CPUMillicores: 250,
			MemoryMB:      128,
		},
		Budget: config.BudgetConfig{
			MaxMonthlyUSD: 5.0,
			AlertAtPct:    80.0,
			HardStop:      true,
		},
		Storage: config.StorageConfig{
			SQLitePath: "/data/wal.db",
			SyncMs:     1000,
		},
	}
	if _, err := seed.Deploy(cfg, "v1.0.0"); err != nil {
		t.Fatalf("seed deploy failed: %v", err)
	}
	if err := seed.SaveState(statePath); err != nil {
		t.Fatalf("seed SaveState failed: %v", err)
	}

	hostDBPath := filepath.Join(replicaDir, "hostdata", "org-wal", "wal-app", "wal.db")
	if err := os.MkdirAll(filepath.Dir(hostDBPath), 0o755); err != nil {
		t.Fatalf("mkdir hostDBPath: %v", err)
	}
	payload := []byte("wal-shutdown-flush-data")
	if err := os.WriteFile(hostDBPath, payload, 0o600); err != nil {
		t.Fatalf("write hostDBPath: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var out bytes.Buffer
	if err := ServeWithContext(ctx, "127.0.0.1:0", stateDir, &out); err != nil {
		t.Fatalf("ServeWithContext failed: %v", err)
	}

	reloaded := daemon.NewDaemon(filepath.Join(stateDir, "cgroups"), replicaDir)
	if err := reloaded.LoadState(statePath); err != nil {
		t.Fatalf("reload state failed: %v", err)
	}
	status, err := reloaded.Status("org-wal", "wal-app")
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if status.LastSnapshot == nil || status.LastSnapshot.SizeBytes != int64(len(payload)) {
		t.Fatalf("expected WAL snapshot synced on shutdown, got %+v", status.LastSnapshot)
	}
}

func TestCLIDestroyAndEventsLocalAndRemote(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")

	localStateDir := t.TempDir()
	if err := Run([]string{"deploy", templateDir, "v1.0.0"}, localStateDir, &bytes.Buffer{}); err != nil {
		t.Fatalf("local deploy failed: %v", err)
	}

	var localAllEventsOut bytes.Buffer
	if err := Run([]string{"events"}, localStateDir, &localAllEventsOut); err != nil {
		t.Fatalf("local events failed: %v", err)
	}
	var localAllEvents struct {
		Events []daemon.AuditEvent `json:"events"`
	}
	if err := json.Unmarshal(localAllEventsOut.Bytes(), &localAllEvents); err != nil {
		t.Fatalf("unmarshal local events: %v", err)
	}
	if len(localAllEvents.Events) == 0 {
		t.Fatal("expected non-empty local events")
	}

	var localScopedEventsOut bytes.Buffer
	if err := Run([]string{"events", templateDir}, localStateDir, &localScopedEventsOut); err != nil {
		t.Fatalf("local scoped events failed: %v", err)
	}
	var localScopedEvents struct {
		Events []daemon.AuditEvent `json:"events"`
	}
	if err := json.Unmarshal(localScopedEventsOut.Bytes(), &localScopedEvents); err != nil {
		t.Fatalf("unmarshal local scoped events: %v", err)
	}
	if len(localScopedEvents.Events) == 0 {
		t.Fatal("expected non-empty local scoped events")
	}

	var localDestroyOut bytes.Buffer
	if err := Run([]string{"destroy", templateDir}, localStateDir, &localDestroyOut); err != nil {
		t.Fatalf("local destroy failed: %v", err)
	}
	var localDeleted struct {
		Deleted bool   `json:"deleted"`
		OrgID   string `json:"org_id"`
		AppName string `json:"app_name"`
	}
	if err := json.Unmarshal(localDestroyOut.Bytes(), &localDeleted); err != nil {
		t.Fatalf("unmarshal local destroy: %v", err)
	}
	if !localDeleted.Deleted || localDeleted.OrgID != "org-indie-default" || localDeleted.AppName != "fastapi-starter" {
		t.Fatalf("unexpected local destroy output: %+v", localDeleted)
	}

	var localListAfter bytes.Buffer
	if err := Run([]string{"list"}, localStateDir, &localListAfter); err != nil {
		t.Fatalf("local list after destroy failed: %v", err)
	}
	var listed struct {
		Apps []daemon.AppStatus `json:"apps"`
	}
	if err := json.Unmarshal(localListAfter.Bytes(), &listed); err != nil {
		t.Fatalf("unmarshal local list: %v", err)
	}
	if len(listed.Apps) != 0 {
		t.Fatalf("expected 0 apps after local destroy, got %+v", listed.Apps)
	}

	daemonDir := t.TempDir()
	d := daemon.NewDaemon(filepath.Join(daemonDir, "cgroups"), filepath.Join(daemonDir, "replicas"))
	const token = "cli-remote-destroy-secret"
	srv := httptest.NewServer(d.HandlerWithAuth(token))
	defer srv.Close()

	t.Setenv("SMALLCLOUD_DAEMON_URL", srv.URL)
	t.Setenv("SMALLCLOUD_API_TOKEN", token)
	remoteStateDir := t.TempDir()

	if err := Run([]string{"deploy", templateDir, "v2.0.0"}, remoteStateDir, &bytes.Buffer{}); err != nil {
		t.Fatalf("remote deploy failed: %v", err)
	}

	var remoteEventsOut bytes.Buffer
	if err := Run([]string{"events", templateDir}, remoteStateDir, &remoteEventsOut); err != nil {
		t.Fatalf("remote events failed: %v", err)
	}
	var remoteEvents struct {
		Events []daemon.AuditEvent `json:"events"`
	}
	if err := json.Unmarshal(remoteEventsOut.Bytes(), &remoteEvents); err != nil {
		t.Fatalf("unmarshal remote events: %v", err)
	}
	if len(remoteEvents.Events) == 0 {
		t.Fatal("expected non-empty remote events")
	}

	var remoteDestroyOut bytes.Buffer
	if err := Run([]string{"destroy", templateDir}, remoteStateDir, &remoteDestroyOut); err != nil {
		t.Fatalf("remote destroy failed: %v", err)
	}
	var remoteDeleted struct {
		Deleted bool   `json:"deleted"`
		OrgID   string `json:"org_id"`
		AppName string `json:"app_name"`
	}
	if err := json.Unmarshal(remoteDestroyOut.Bytes(), &remoteDeleted); err != nil {
		t.Fatalf("unmarshal remote destroy: %v", err)
	}
	if !remoteDeleted.Deleted || remoteDeleted.OrgID != "org-indie-default" || remoteDeleted.AppName != "fastapi-starter" {
		t.Fatalf("unexpected remote destroy output: %+v", remoteDeleted)
	}

	var remoteListAfter bytes.Buffer
	if err := Run([]string{"list"}, remoteStateDir, &remoteListAfter); err != nil {
		t.Fatalf("remote list after destroy failed: %v", err)
	}
	var remoteListed struct {
		Apps []daemon.AppStatus `json:"apps"`
	}
	if err := json.Unmarshal(remoteListAfter.Bytes(), &remoteListed); err != nil {
		t.Fatalf("unmarshal remote list: %v", err)
	}
	if len(remoteListed.Apps) != 0 {
		t.Fatalf("expected 0 apps after remote destroy, got %+v", remoteListed.Apps)
	}
}

func TestCLIServeHealthProbeTimeoutValidation(t *testing.T) {
	t.Setenv("SMALLCLOUD_HEALTH_PROBE_TIMEOUT", "0s")
	if err := ServeWithContext(context.Background(), "127.0.0.1:0", t.TempDir(), &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for non-positive SMALLCLOUD_HEALTH_PROBE_TIMEOUT")
	}

	t.Setenv("SMALLCLOUD_HEALTH_PROBE_TIMEOUT", "not-a-duration")
	if err := ServeWithContext(context.Background(), "127.0.0.1:0", t.TempDir(), &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for invalid SMALLCLOUD_HEALTH_PROBE_TIMEOUT")
	}

	t.Setenv("SMALLCLOUD_HEALTH_PROBE_TIMEOUT", "2s")
	t.Setenv("SMALLCLOUD_RUNTIME", "docker")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := ServeWithContext(ctx, "127.0.0.1:0", t.TempDir(), &bytes.Buffer{}); err != nil {
		t.Fatalf("ServeWithContext with valid probe timeout failed: %v", err)
	}
}

func TestCLIRemoteDaemonEnvRouting(t *testing.T) {
	daemonDir := t.TempDir()
	d := daemon.NewDaemon(filepath.Join(daemonDir, "cgroups"), filepath.Join(daemonDir, "replicas"))
	const token = "cli-remote-secret"
	srv := httptest.NewServer(d.HandlerWithAuth(token))
	defer srv.Close()

	t.Setenv("SMALLCLOUD_DAEMON_URL", srv.URL)
	t.Setenv("SMALLCLOUD_API_TOKEN", token)

	localStateDir := t.TempDir()
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")

	var out bytes.Buffer
	if err := Run([]string{"deploy", templateDir, "v2.0.0"}, localStateDir, &out); err != nil {
		t.Fatalf("remote cli deploy failed: %v", err)
	}
	var res daemon.DeploymentResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal remote deploy: %v", err)
	}
	if res.Route.ActiveReleaseID != "v2.0.0" {
		t.Fatalf("expected active release v2.0.0, got %+v", res.Route)
	}
	if _, err := os.Stat(filepath.Join(localStateDir, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no local state.json when routing to remote daemon, got %v", err)
	}

	out.Reset()
	if err := Run([]string{"budget", "set", templateDir, "19.95"}, localStateDir, &out); err != nil {
		t.Fatalf("remote cli budget set failed: %v", err)
	}
	var bState daemon.AppBudgetState
	if err := json.Unmarshal(out.Bytes(), &bState); err != nil {
		t.Fatalf("unmarshal remote budget: %v", err)
	}
	if bState.MaxMonthlyUSD != 19.95 {
		t.Fatalf("expected max_monthly_usd 19.95, got %v", bState.MaxMonthlyUSD)
	}

	out.Reset()
	if err := Run([]string{"status", templateDir}, localStateDir, &out); err != nil {
		t.Fatalf("remote cli status failed: %v", err)
	}
	var st daemon.AppStatus
	if err := json.Unmarshal(out.Bytes(), &st); err != nil {
		t.Fatalf("unmarshal remote status: %v", err)
	}
	if st.Route.ActiveReleaseID != "v2.0.0" || st.Budget.MaxMonthlyUSD != 19.95 {
		t.Fatalf("unexpected remote status: %+v", st)
	}

	out.Reset()
	if err := Run([]string{"deploy", templateDir, "v2.1.0"}, localStateDir, &out); err != nil {
		t.Fatalf("remote cli second deploy failed: %v", err)
	}
	out.Reset()
	if err := Run([]string{"rollback", templateDir}, localStateDir, &out); err != nil {
		t.Fatalf("remote cli rollback failed: %v", err)
	}
	var rolled daemon.RouteEntry
	if err := json.Unmarshal(out.Bytes(), &rolled); err != nil {
		t.Fatalf("unmarshal remote rollback: %v", err)
	}
	if rolled.ActiveReleaseID != "v2.0.0" {
		t.Fatalf("expected rollback to v2.0.0, got %+v", rolled)
	}

	out.Reset()
	if err := Run([]string{"meter", templateDir, "2.5"}, localStateDir, &out); err != nil {
		t.Fatalf("remote cli meter failed: %v", err)
	}
	var metered daemon.AppBudgetState
	if err := json.Unmarshal(out.Bytes(), &metered); err != nil {
		t.Fatalf("unmarshal remote meter: %v", err)
	}
	if metered.CurrentUSD != 2.5 {
		t.Fatalf("expected current_usd 2.5, got %v", metered.CurrentUSD)
	}

	out.Reset()
	if err := Run([]string{"list"}, localStateDir, &out); err != nil {
		t.Fatalf("remote cli list failed: %v", err)
	}
	var listOut struct {
		Apps []daemon.AppStatus `json:"apps"`
	}
	if err := json.Unmarshal(out.Bytes(), &listOut); err != nil {
		t.Fatalf("unmarshal remote list: %v", err)
	}
	if len(listOut.Apps) != 1 || listOut.Apps[0].AppName != "fastapi-starter" {
		t.Fatalf("unexpected remote list apps: %+v", listOut.Apps)
	}

	out.Reset()
	if err := Run([]string{"rollover", "2099-02-01T00:00:00Z"}, localStateDir, &out); err != nil {
		t.Fatalf("remote cli rollover failed: %v", err)
	}
	var rollOut struct {
		RolledOver int       `json:"rolled_over"`
		Timestamp  time.Time `json:"timestamp"`
	}
	if err := json.Unmarshal(out.Bytes(), &rollOut); err != nil {
		t.Fatalf("unmarshal remote rollover: %v", err)
	}
	if rollOut.RolledOver != 1 || !rollOut.Timestamp.Equal(time.Date(2099, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected remote rollover output: %+v", rollOut)
	}
}

func TestCLILocalMeterRolloverAndList(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")
	stateDir := t.TempDir()

	if err := Run([]string{"deploy", templateDir, "v1.0.0"}, stateDir, &bytes.Buffer{}); err != nil {
		t.Fatalf("local deploy failed: %v", err)
	}

	var meterOut bytes.Buffer
	if err := Run([]string{"meter", templateDir, "1.25"}, stateDir, &meterOut); err != nil {
		t.Fatalf("local meter failed: %v", err)
	}
	var metered daemon.AppBudgetState
	if err := json.Unmarshal(meterOut.Bytes(), &metered); err != nil {
		t.Fatalf("unmarshal local meter: %v", err)
	}
	if metered.CurrentUSD != 1.25 {
		t.Fatalf("expected current_usd 1.25, got %v", metered.CurrentUSD)
	}

	var listOut bytes.Buffer
	if err := Run([]string{"list"}, stateDir, &listOut); err != nil {
		t.Fatalf("local list failed: %v", err)
	}
	var listed struct {
		Apps []daemon.AppStatus `json:"apps"`
	}
	if err := json.Unmarshal(listOut.Bytes(), &listed); err != nil {
		t.Fatalf("unmarshal local list: %v", err)
	}
	if len(listed.Apps) != 1 || listed.Apps[0].Budget.CurrentUSD != 1.25 {
		t.Fatalf("unexpected local list output: %+v", listed.Apps)
	}

	var psOut bytes.Buffer
	if err := Run([]string{"ps"}, stateDir, &psOut); err != nil {
		t.Fatalf("local ps failed: %v", err)
	}

	var rollOut bytes.Buffer
	if err := Run([]string{"rollover", "2099-03-01T00:00:00Z"}, stateDir, &rollOut); err != nil {
		t.Fatalf("local rollover failed: %v", err)
	}
	var rolled struct {
		RolledOver int       `json:"rolled_over"`
		Timestamp  time.Time `json:"timestamp"`
	}
	if err := json.Unmarshal(rollOut.Bytes(), &rolled); err != nil {
		t.Fatalf("unmarshal local rollover: %v", err)
	}
	if rolled.RolledOver != 1 || !rolled.Timestamp.Equal(time.Date(2099, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected local rollover output: %+v", rolled)
	}

	var statusOut bytes.Buffer
	if err := Run([]string{"status", templateDir}, stateDir, &statusOut); err != nil {
		t.Fatalf("local status after rollover failed: %v", err)
	}
	var st daemon.AppStatus
	if err := json.Unmarshal(statusOut.Bytes(), &st); err != nil {
		t.Fatalf("unmarshal status after rollover: %v", err)
	}
	if st.Budget.CurrentUSD != 0 {
		t.Fatalf("expected current_usd reset to 0 after rollover, got %v", st.Budget.CurrentUSD)
	}
}

func TestCLIServeBackgroundRolloverAndShutdown(t *testing.T) {
	t.Setenv("SMALLCLOUD_ROLLOVER_INTERVAL", "15ms")
	stateDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	var out bytes.Buffer
	if err := ServeWithContext(ctx, "127.0.0.1:0", stateDir, &out); err != nil {
		t.Fatalf("ServeWithContext failed: %v", err)
	}
	if !strings.Contains(out.String(), "listening") {
		t.Fatalf("expected listening JSON output, got %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(stateDir, "state.json")); err != nil {
		t.Fatalf("expected state.json persisted on shutdown: %v", err)
	}
}

func TestCLIServeWorkerTTLValidationAndReaper(t *testing.T) {
	t.Setenv("SMALLCLOUD_WORKER_TTL", "0s")
	if err := ServeWithContext(context.Background(), "127.0.0.1:0", t.TempDir(), &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for non-positive SMALLCLOUD_WORKER_TTL")
	}

	t.Setenv("SMALLCLOUD_WORKER_TTL", "-5s")
	if err := ServeWithContext(context.Background(), "127.0.0.1:0", t.TempDir(), &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for negative SMALLCLOUD_WORKER_TTL")
	}

	t.Setenv("SMALLCLOUD_WORKER_TTL", "not-a-duration")
	if err := ServeWithContext(context.Background(), "127.0.0.1:0", t.TempDir(), &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for invalid SMALLCLOUD_WORKER_TTL")
	}

	stateDir := t.TempDir()
	statePath := filepath.Join(stateDir, "state.json")
	seed := daemon.NewDaemon(filepath.Join(stateDir, "cgroups"), filepath.Join(stateDir, "replicas"))
	if _, err := seed.WorkerPool().RegisterNode(daemon.WorkerNode{
		ID:      "node-stale",
		Address: "10.0.0.1:7420",
		MaxApps: 4,
	}); err != nil {
		t.Fatalf("register stale node: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, err := seed.WorkerPool().RegisterNode(daemon.WorkerNode{
		ID:      "node-fresh",
		Address: "10.0.0.2:7420",
		MaxApps: 4,
	}); err != nil {
		t.Fatalf("register fresh node: %v", err)
	}
	if err := seed.SaveState(statePath); err != nil {
		t.Fatalf("save seed state: %v", err)
	}

	t.Setenv("SMALLCLOUD_ROLLOVER_INTERVAL", "15ms")
	t.Setenv("SMALLCLOUD_WORKER_TTL", "150ms")
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	var out bytes.Buffer
	if err := ServeWithContext(ctx, "127.0.0.1:0", stateDir, &out); err != nil {
		t.Fatalf("ServeWithContext failed: %v", err)
	}

	reloaded := daemon.NewDaemon(filepath.Join(stateDir, "cgroups"), filepath.Join(stateDir, "replicas"))
	if err := reloaded.LoadState(statePath); err != nil {
		t.Fatalf("reload state: %v", err)
	}
	nodes := reloaded.WorkerPool().ListNodes()
	if len(nodes) != 1 || nodes[0].ID != "node-fresh" {
		t.Fatalf("expected only node-fresh to remain after reaping, got %+v", nodes)
	}
}

func TestCLIDoctorLive(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")
	stateDir := t.TempDir()

	caddySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/config/" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer caddySrv.Close()

	daemonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer daemonSrv.Close()

	var flagOut bytes.Buffer
	if err := Run([]string{"doctor", "--live", "--caddy", caddySrv.URL, "--daemon", daemonSrv.URL, templateDir}, stateDir, &flagOut); err != nil {
		t.Fatalf("doctor --live with flags failed: %v", err)
	}
	var flagReport DoctorReport
	if err := json.Unmarshal(flagOut.Bytes(), &flagReport); err != nil {
		t.Fatalf("unmarshal doctor --live report: %v", err)
	}
	if !flagReport.Passed {
		t.Fatalf("expected doctor --live Passed=true, got %+v", flagReport)
	}

	t.Setenv("SMALLCLOUD_CADDY_ADMIN_URL", caddySrv.URL)
	t.Setenv("SMALLCLOUD_DAEMON_URL", daemonSrv.URL)

	var envOut bytes.Buffer
	if err := Run([]string{"doctor", "--live", templateDir}, stateDir, &envOut); err != nil {
		t.Fatalf("doctor --live with env fallback failed: %v", err)
	}
	var envReport DoctorReport
	if err := json.Unmarshal(envOut.Bytes(), &envReport); err != nil {
		t.Fatalf("unmarshal doctor --live env report: %v", err)
	}
	if !envReport.Passed {
		t.Fatalf("expected doctor --live env Passed=true, got %+v", envReport)
	}

	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failSrv.Close()

	var failOut bytes.Buffer
	if err := Run([]string{"doctor", "--live", "--caddy", failSrv.URL, templateDir}, stateDir, &failOut); err == nil {
		t.Fatal("expected doctor --live with failing caddy to return error")
	}
}

func TestCLILogsAndCanary(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")
	stateDir := t.TempDir()

	if err := Run([]string{"deploy", templateDir, "v1.0.0"}, stateDir, &bytes.Buffer{}); err != nil {
		t.Fatalf("deploy v1.0.0 failed: %v", err)
	}
	if err := Run([]string{"deploy", templateDir, "v1.1.0"}, stateDir, &bytes.Buffer{}); err != nil {
		t.Fatalf("deploy v1.1.0 failed: %v", err)
	}

	var logsActiveOut bytes.Buffer
	if err := Run([]string{"logs", templateDir}, stateDir, &logsActiveOut); err != nil {
		t.Fatalf("cli logs (active) failed: %v", err)
	}
	var logsActive struct {
		OrgID     string `json:"org_id"`
		AppName   string `json:"app_name"`
		ReleaseID string `json:"release_id"`
		Logs      string `json:"logs"`
	}
	if err := json.Unmarshal(logsActiveOut.Bytes(), &logsActive); err != nil {
		t.Fatalf("unmarshal logs output: %v", err)
	}
	if logsActive.OrgID != "org-indie-default" || logsActive.AppName != "fastapi-starter" || logsActive.ReleaseID != "v1.1.0" {
		t.Fatalf("unexpected logs output: %+v", logsActive)
	}

	var logsExplicitOut bytes.Buffer
	if err := Run([]string{"logs", templateDir, "v1.0.0"}, stateDir, &logsExplicitOut); err != nil {
		t.Fatalf("cli logs (explicit) failed: %v", err)
	}
	var logsExplicit struct {
		ReleaseID string `json:"release_id"`
	}
	if err := json.Unmarshal(logsExplicitOut.Bytes(), &logsExplicit); err != nil {
		t.Fatalf("unmarshal explicit logs output: %v", err)
	}
	if logsExplicit.ReleaseID != "v1.0.0" {
		t.Fatalf("expected release_id v1.0.0, got %q", logsExplicit.ReleaseID)
	}

	var canaryOut bytes.Buffer
	if err := Run([]string{"canary", templateDir, "25"}, stateDir, &canaryOut); err != nil {
		t.Fatalf("cli canary failed: %v", err)
	}
	var route daemon.RouteEntry
	if err := json.Unmarshal(canaryOut.Bytes(), &route); err != nil {
		t.Fatalf("unmarshal canary output: %v", err)
	}
	if route.ActiveReleaseID != "v1.1.0" || route.StandbyReleaseID != "v1.0.0" {
		t.Fatalf("unexpected canary route: %+v", route)
	}

	if err := Run([]string{"canary"}, stateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected canary with missing args to fail")
	}
	if err := Run([]string{"canary", templateDir, "not-a-number"}, stateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected canary with invalid pct to fail")
	}
}

func TestCLIInitTemplates(t *testing.T) {
	nodeDir := filepath.Join(t.TempDir(), "node-app")
	var nodeOut bytes.Buffer
	if err := Run([]string{"init", "--template", "node", nodeDir}, t.TempDir(), &nodeOut); err != nil {
		t.Fatalf("init --template node failed: %v", err)
	}
	nodeToml, err := os.ReadFile(filepath.Join(nodeDir, "smallcloud.toml"))
	if err != nil {
		t.Fatalf("read node smallcloud.toml: %v", err)
	}
	if !strings.Contains(string(nodeToml), "port = 8080") {
		t.Fatalf("expected port = 8080 for node template, got:\n%s", string(nodeToml))
	}

	goDir := filepath.Join(t.TempDir(), "go-app")
	var goOut bytes.Buffer
	if err := Run([]string{"init", "--template=go", goDir}, t.TempDir(), &goOut); err != nil {
		t.Fatalf("init --template=go failed: %v", err)
	}
	goToml, err := os.ReadFile(filepath.Join(goDir, "smallcloud.toml"))
	if err != nil {
		t.Fatalf("read go smallcloud.toml: %v", err)
	}
	if !strings.Contains(string(goToml), "port = 8080") {
		t.Fatalf("expected port = 8080 for go template, got:\n%s", string(goToml))
	}

	fastapiDir := filepath.Join(t.TempDir(), "py-app")
	var pyOut bytes.Buffer
	if err := Run([]string{"init", "--template", "fastapi", fastapiDir}, t.TempDir(), &pyOut); err != nil {
		t.Fatalf("init --template fastapi failed: %v", err)
	}
	pyToml, err := os.ReadFile(filepath.Join(fastapiDir, "smallcloud.toml"))
	if err != nil {
		t.Fatalf("read fastapi smallcloud.toml: %v", err)
	}
	if !strings.Contains(string(pyToml), "port = 8000") {
		t.Fatalf("expected port = 8000 for fastapi template, got:\n%s", string(pyToml))
	}

	if err := Run([]string{"init", "--template", "rust", filepath.Join(t.TempDir(), "bad-app")}, t.TempDir(), &bytes.Buffer{}); err == nil {
		t.Fatal("expected unsupported template to fail")
	}
}

func TestCLIOrgQuotaAndPruneLocal(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")
	stateDir := t.TempDir()

	if err := Run([]string{"deploy", templateDir, "v1.0.0"}, stateDir, &bytes.Buffer{}); err != nil {
		t.Fatalf("local deploy failed: %v", err)
	}

	var setOut bytes.Buffer
	if err := Run([]string{"org-quota", "org-indie-default", "50.0"}, stateDir, &setOut); err != nil {
		t.Fatalf("local org-quota set failed: %v", err)
	}
	var setSummary daemon.OrgSpendSummary
	if err := json.Unmarshal(setOut.Bytes(), &setSummary); err != nil {
		t.Fatalf("unmarshal org-quota set: %v", err)
	}
	if setSummary.OrgID != "org-indie-default" || setSummary.MaxMonthlyUSD != 50.0 {
		t.Fatalf("unexpected org-quota set summary: %+v", setSummary)
	}

	var getOut bytes.Buffer
	if err := Run([]string{"org-quota", "org-indie-default"}, stateDir, &getOut); err != nil {
		t.Fatalf("local org-quota get failed: %v", err)
	}
	var getSummary daemon.OrgSpendSummary
	if err := json.Unmarshal(getOut.Bytes(), &getSummary); err != nil {
		t.Fatalf("unmarshal org-quota get: %v", err)
	}
	if getSummary.MaxMonthlyUSD != 50.0 {
		t.Fatalf("expected max_monthly_usd 50.0, got %+v", getSummary)
	}

	if err := Run([]string{"org-quota"}, stateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected org-quota with missing args to fail")
	}
	if err := Run([]string{"org-quota", "org-indie-default", "not-a-float"}, stateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected org-quota with invalid max_monthly_usd to fail")
	}

	var pruneOut bytes.Buffer
	if err := Run([]string{"prune", templateDir, "2"}, stateDir, &pruneOut); err != nil {
		t.Fatalf("local prune failed: %v", err)
	}
	var pruneRes struct {
		OrgID    string `json:"org_id"`
		AppName  string `json:"app_name"`
		KeepLast int    `json:"keep_last"`
		Pruned   int    `json:"pruned"`
	}
	if err := json.Unmarshal(pruneOut.Bytes(), &pruneRes); err != nil {
		t.Fatalf("unmarshal prune output: %v", err)
	}
	if pruneRes.OrgID != "org-indie-default" || pruneRes.AppName != "fastapi-starter" || pruneRes.KeepLast != 2 || pruneRes.Pruned != 0 {
		t.Fatalf("unexpected local prune output: %+v", pruneRes)
	}
}

func TestCLIRemoteCanaryLogsOrgQuotaAndPrune(t *testing.T) {
	const token = "cli-remote-extra-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/status":
			_ = json.NewEncoder(w).Encode(daemon.AppStatus{
				OrgID:   "org-indie-default",
				AppName: "fastapi-starter",
				Route: daemon.RouteEntry{
					OrgID:           "org-indie-default",
					AppName:         "fastapi-starter",
					ActiveReleaseID: "v2.0.0",
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/logs":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"logs": "remote log for " + r.URL.Query().Get("release_id"),
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/canary":
			var req struct {
				OrgID        string `json:"org_id"`
				AppName      string `json:"app_name"`
				CanaryWeight int    `json:"canary_weight"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(daemon.RouteEntry{
				OrgID:            req.OrgID,
				AppName:          req.AppName,
				ActiveReleaseID:  "v2.0.0",
				StandbyReleaseID: "v1.0.0",
				CanaryWeight:     req.CanaryWeight,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/org-quota":
			var req struct {
				OrgID         string  `json:"org_id"`
				MaxMonthlyUSD float64 `json:"max_monthly_usd"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(daemon.OrgSpendSummary{
				OrgID:         req.OrgID,
				MaxMonthlyUSD: req.MaxMonthlyUSD,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/org-quota":
			_ = json.NewEncoder(w).Encode(daemon.OrgSpendSummary{
				OrgID:         r.URL.Query().Get("org_id"),
				MaxMonthlyUSD: 42.5,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/prune":
			var req struct {
				KeepLast int `json:"keep_last"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(map[string]int{"pruned": req.KeepLast})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("SMALLCLOUD_DAEMON_URL", srv.URL)
	t.Setenv("SMALLCLOUD_API_TOKEN", token)
	stateDir := t.TempDir()
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")

	var canaryOut bytes.Buffer
	if err := Run([]string{"canary", templateDir, "30"}, stateDir, &canaryOut); err != nil {
		t.Fatalf("remote canary failed: %v", err)
	}
	var route daemon.RouteEntry
	if err := json.Unmarshal(canaryOut.Bytes(), &route); err != nil {
		t.Fatalf("unmarshal remote canary: %v", err)
	}
	if route.CanaryWeight != 30 {
		t.Fatalf("expected standby weight 30, got %+v", route)
	}

	var logsOut bytes.Buffer
	if err := Run([]string{"logs", templateDir}, stateDir, &logsOut); err != nil {
		t.Fatalf("remote logs failed: %v", err)
	}
	var logsRes struct {
		ReleaseID string `json:"release_id"`
		Logs      string `json:"logs"`
	}
	if err := json.Unmarshal(logsOut.Bytes(), &logsRes); err != nil {
		t.Fatalf("unmarshal remote logs: %v", err)
	}
	if logsRes.ReleaseID != "v2.0.0" || logsRes.Logs != "remote log for v2.0.0" {
		t.Fatalf("unexpected remote logs: %+v", logsRes)
	}

	var quotaSetOut bytes.Buffer
	if err := Run([]string{"org-quota", "org-indie-default", "42.5"}, stateDir, &quotaSetOut); err != nil {
		t.Fatalf("remote org-quota set failed: %v", err)
	}
	var quotaSet daemon.OrgSpendSummary
	if err := json.Unmarshal(quotaSetOut.Bytes(), &quotaSet); err != nil {
		t.Fatalf("unmarshal remote org-quota set: %v", err)
	}
	if quotaSet.MaxMonthlyUSD != 42.5 {
		t.Fatalf("expected max_monthly_usd 42.5, got %+v", quotaSet)
	}

	var quotaGetOut bytes.Buffer
	if err := Run([]string{"org-quota", "org-indie-default"}, stateDir, &quotaGetOut); err != nil {
		t.Fatalf("remote org-quota get failed: %v", err)
	}

	var pruneOut bytes.Buffer
	if err := Run([]string{"prune", templateDir}, stateDir, &pruneOut); err != nil {
		t.Fatalf("remote prune failed: %v", err)
	}
	var pruneRes struct {
		KeepLast int `json:"keep_last"`
		Pruned   int `json:"pruned"`
	}
	if err := json.Unmarshal(pruneOut.Bytes(), &pruneRes); err != nil {
		t.Fatalf("unmarshal remote prune: %v", err)
	}
	if pruneRes.KeepLast != 5 || pruneRes.Pruned != 5 {
		t.Fatalf("unexpected remote prune output: %+v", pruneRes)
	}
}

func TestCLIRemoteNodes(t *testing.T) {
	t.Setenv("SMALLCLOUD_DAEMON_URL", "")
	if err := Run([]string{"nodes"}, t.TempDir(), &bytes.Buffer{}); err == nil {
		t.Fatal("expected nodes without SMALLCLOUD_DAEMON_URL to fail")
	}

	const token = "cli-remote-nodes-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/nodes" {
			_ = json.NewEncoder(w).Encode(map[string][]daemon.WorkerNode{
				"nodes": {{}},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	t.Setenv("SMALLCLOUD_DAEMON_URL", srv.URL)
	t.Setenv("SMALLCLOUD_API_TOKEN", token)

	var out bytes.Buffer
	if err := Run([]string{"nodes"}, t.TempDir(), &out); err != nil {
		t.Fatalf("remote nodes failed: %v", err)
	}
	var res struct {
		Nodes []daemon.WorkerNode `json:"nodes"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal remote nodes: %v", err)
	}
	if len(res.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(res.Nodes))
	}
}

func TestCLIVerifySnapshot(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")
	stateDir := t.TempDir()

	if err := Run([]string{"verify-snapshot", templateDir}, stateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected verify-snapshot to fail when no snapshot exists")
	}

	d := daemon.NewDaemon(filepath.Join(stateDir, "cgroups"), filepath.Join(stateDir, "replicas"))
	dbFile := filepath.Join(t.TempDir(), "app.db")
	payload := []byte("SQLite format 3\x00smallcloud-verify-snapshot-test")
	if err := os.WriteFile(dbFile, payload, 0o600); err != nil {
		t.Fatalf("write db file: %v", err)
	}
	synced, err := d.Storage().SyncWAL("org-indie-default", "fastapi-starter", dbFile)
	if err != nil {
		t.Fatalf("SyncWAL failed: %v", err)
	}

	var out bytes.Buffer
	if err := Run([]string{"verify-snapshot", templateDir}, stateDir, &out); err != nil {
		t.Fatalf("verify-snapshot failed: %v", err)
	}
	var meta daemon.SnapshotMetadata
	if err := json.Unmarshal(out.Bytes(), &meta); err != nil {
		t.Fatalf("unmarshal SnapshotMetadata: %v", err)
	}
	if meta.OrgID != "org-indie-default" || meta.AppName != "fastapi-starter" {
		t.Fatalf("unexpected metadata tenant: %+v", meta)
	}
	if meta.Generation != synced.Generation || meta.SizeBytes != synced.SizeBytes || meta.SHA256 != synced.SHA256 {
		t.Fatalf("verified metadata %+v does not match synced %+v", meta, synced)
	}
}

func TestCLIBackupAndRestoreBundle(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")
	srcStateDir := t.TempDir()

	if err := Run([]string{"deploy", templateDir, "v1.0.0"}, srcStateDir, &bytes.Buffer{}); err != nil {
		t.Fatalf("deploy failed: %v", err)
	}

	d := daemon.NewDaemon(filepath.Join(srcStateDir, "cgroups"), filepath.Join(srcStateDir, "replicas"))
	dbFile := filepath.Join(t.TempDir(), "app.db")
	payload := []byte("SQLite format 3\x00smallcloud-backup-restore-test")
	if err := os.WriteFile(dbFile, payload, 0o600); err != nil {
		t.Fatalf("write db file: %v", err)
	}
	synced, err := d.Storage().SyncWAL("org-indie-default", "fastapi-starter", dbFile)
	if err != nil {
		t.Fatalf("SyncWAL failed: %v", err)
	}

	if err := Run([]string{"backup"}, srcStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected backup with missing args to fail")
	}

	bundlePath := filepath.Join(t.TempDir(), "fastapi-backup.tar.gz")
	var backupOut bytes.Buffer
	if err := Run([]string{"backup", templateDir, bundlePath}, srcStateDir, &backupOut); err != nil {
		t.Fatalf("backup failed: %v", err)
	}
	var backupManifest daemon.BackupManifest
	if err := json.Unmarshal(backupOut.Bytes(), &backupManifest); err != nil {
		t.Fatalf("unmarshal backup manifest: %v", err)
	}
	if backupManifest.OrgID != "org-indie-default" || backupManifest.AppName != "fastapi-starter" {
		t.Fatalf("unexpected backup manifest tenant: %+v", backupManifest)
	}
	if backupManifest.Snapshot == nil || backupManifest.Snapshot.SHA256 != synced.SHA256 {
		t.Fatalf("expected backup snapshot sha256 %s, got %+v", synced.SHA256, backupManifest.Snapshot)
	}

	dstStateDir := t.TempDir()
	if err := Run([]string{"restore-bundle"}, dstStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected restore-bundle with missing args to fail")
	}

	var restoreOut bytes.Buffer
	if err := Run([]string{"restore-bundle", bundlePath}, dstStateDir, &restoreOut); err != nil {
		t.Fatalf("restore-bundle failed: %v", err)
	}
	var restoredManifest daemon.BackupManifest
	if err := json.Unmarshal(restoreOut.Bytes(), &restoredManifest); err != nil {
		t.Fatalf("unmarshal restore manifest: %v", err)
	}
	if restoredManifest.OrgID != "org-indie-default" || restoredManifest.AppName != "fastapi-starter" {
		t.Fatalf("unexpected restored manifest tenant: %+v", restoredManifest)
	}

	var verifyOut bytes.Buffer
	if err := Run([]string{"verify-snapshot", templateDir}, dstStateDir, &verifyOut); err != nil {
		t.Fatalf("verify-snapshot after restore-bundle failed: %v", err)
	}
	var meta daemon.SnapshotMetadata
	if err := json.Unmarshal(verifyOut.Bytes(), &meta); err != nil {
		t.Fatalf("unmarshal verify-snapshot metadata: %v", err)
	}
	if meta.SHA256 != synced.SHA256 || meta.SizeBytes != synced.SizeBytes {
		t.Fatalf("verified metadata %+v does not match synced %+v", meta, synced)
	}
}

func TestCLIVersion(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		var out bytes.Buffer
		if err := Run([]string{arg}, t.TempDir(), &out); err != nil {
			t.Fatalf("Run(%q) failed: %v", arg, err)
		}
		var got map[string]string
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal %q output: %v", arg, err)
		}
		if got["version"] != Version {
			t.Fatalf("Run(%q) version = %q, want %q", arg, got["version"], Version)
		}
	}
}

func TestCLIRemoteBackup(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")
	daemonDir := t.TempDir()
	d := daemon.NewDaemon(filepath.Join(daemonDir, "cgroups"), filepath.Join(daemonDir, "replicas"))

	cfg, err := PackageApp(templateDir)
	if err != nil {
		t.Fatalf("PackageApp failed: %v", err)
	}
	if _, err := d.Deploy(cfg.Config, "v1.0.0"); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}
	dbFile := filepath.Join(t.TempDir(), "app.db")
	if err := os.WriteFile(dbFile, []byte("SQLite format 3\x00remote-backup-test"), 0o600); err != nil {
		t.Fatalf("write db file: %v", err)
	}
	synced, err := d.Storage().SyncWAL(cfg.Config.App.OrgID, cfg.Config.App.Name, dbFile)
	if err != nil {
		t.Fatalf("SyncWAL failed: %v", err)
	}

	const token = "cli-remote-backup-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/backup" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		tmpBundle := filepath.Join(t.TempDir(), "bundle.tar.gz")
		if _, err := daemon.ExportBackupBundle(d, q.Get("org_id"), q.Get("app"), tmpBundle); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		raw, err := os.ReadFile(tmpBundle)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	t.Setenv("SMALLCLOUD_DAEMON_URL", srv.URL)
	t.Setenv("SMALLCLOUD_API_TOKEN", token)

	localStateDir := t.TempDir()
	destPath := filepath.Join(t.TempDir(), "remote-backup.tar.gz")
	var out bytes.Buffer
	if err := Run([]string{"backup", templateDir, destPath}, localStateDir, &out); err != nil {
		t.Fatalf("remote backup failed: %v", err)
	}
	var manifest daemon.BackupManifest
	if err := json.Unmarshal(out.Bytes(), &manifest); err != nil {
		t.Fatalf("unmarshal remote backup manifest: %v", err)
	}
	if manifest.OrgID != cfg.Config.App.OrgID || manifest.AppName != cfg.Config.App.Name {
		t.Fatalf("unexpected manifest tenant: %+v", manifest)
	}
	if manifest.Snapshot == nil || manifest.Snapshot.SHA256 != synced.SHA256 {
		t.Fatalf("expected snapshot sha256 %s, got %+v", synced.SHA256, manifest.Snapshot)
	}
	if _, err := daemon.VerifyBackupBundle(destPath); err != nil {
		t.Fatalf("VerifyBackupBundle on downloaded file failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(localStateDir, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no local state.json when routing backup to remote daemon, got %v", err)
	}
}

func TestCLIRemoteRestoreBundle(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")
	srcDir := t.TempDir()
	srcDaemon := daemon.NewDaemon(filepath.Join(srcDir, "cgroups"), filepath.Join(srcDir, "replicas"))

	pkg, err := PackageApp(templateDir)
	if err != nil {
		t.Fatalf("PackageApp failed: %v", err)
	}
	if _, err := srcDaemon.Deploy(pkg.Config, "v1.0.0"); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}
	dbFile := filepath.Join(t.TempDir(), "app.db")
	if err := os.WriteFile(dbFile, []byte("SQLite format 3\x00remote-restore-roundtrip"), 0o600); err != nil {
		t.Fatalf("write db file: %v", err)
	}
	synced, err := srcDaemon.Storage().SyncWAL(pkg.Config.App.OrgID, pkg.Config.App.Name, dbFile)
	if err != nil {
		t.Fatalf("SyncWAL failed: %v", err)
	}

	const token = "cli-remote-restore-secret"
	srcSrv := httptest.NewServer(srcDaemon.HandlerWithAuth(token))
	defer srcSrv.Close()

	localStateDir := t.TempDir()
	bundlePath := filepath.Join(t.TempDir(), "roundtrip-backup.tar.gz")
	t.Setenv("SMALLCLOUD_DAEMON_URL", srcSrv.URL)
	t.Setenv("SMALLCLOUD_API_TOKEN", token)

	var backupOut bytes.Buffer
	if err := Run([]string{"backup", templateDir, bundlePath}, localStateDir, &backupOut); err != nil {
		t.Fatalf("remote backup failed: %v", err)
	}
	var backupManifest daemon.BackupManifest
	if err := json.Unmarshal(backupOut.Bytes(), &backupManifest); err != nil {
		t.Fatalf("unmarshal backup manifest: %v", err)
	}
	if backupManifest.Snapshot == nil || backupManifest.Snapshot.SHA256 != synced.SHA256 {
		t.Fatalf("unexpected backup manifest snapshot: %+v", backupManifest.Snapshot)
	}

	dstDir := t.TempDir()
	dstDaemon := daemon.NewDaemon(filepath.Join(dstDir, "cgroups"), filepath.Join(dstDir, "replicas"))
	dstSrv := httptest.NewServer(dstDaemon.HandlerWithAuth(token))
	defer dstSrv.Close()

	t.Setenv("SMALLCLOUD_DAEMON_URL", dstSrv.URL)
	var restoreOut bytes.Buffer
	if err := Run([]string{"restore-bundle", bundlePath}, localStateDir, &restoreOut); err != nil {
		t.Fatalf("remote restore-bundle failed: %v", err)
	}
	var restoredManifest daemon.BackupManifest
	if err := json.Unmarshal(restoreOut.Bytes(), &restoredManifest); err != nil {
		t.Fatalf("unmarshal restored manifest: %v", err)
	}
	if restoredManifest.OrgID != pkg.Config.App.OrgID || restoredManifest.AppName != pkg.Config.App.Name {
		t.Fatalf("unexpected restored manifest tenant: %+v", restoredManifest)
	}
	if restoredManifest.Snapshot == nil || restoredManifest.Snapshot.SHA256 != synced.SHA256 {
		t.Fatalf("expected restored snapshot sha256 %s, got %+v", synced.SHA256, restoredManifest.Snapshot)
	}

	verified, err := dstDaemon.Storage().VerifySnapshot(pkg.Config.App.OrgID, pkg.Config.App.Name)
	if err != nil {
		t.Fatalf("VerifySnapshot on destination daemon failed: %v", err)
	}
	if verified.SHA256 != synced.SHA256 || verified.SizeBytes != synced.SizeBytes {
		t.Fatalf("verified metadata %+v does not match synced %+v", verified, synced)
	}
	if _, err := os.Stat(filepath.Join(localStateDir, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no local state.json when routing restore-bundle to remote daemon, got %v", err)
	}
}

func TestCLICanaryReconcileAndEdgeErrors(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")
	missingDir := filepath.Join(t.TempDir(), "does-not-exist")
	localStateDir := t.TempDir()

	if err := Run([]string{"canary-reconcile", missingDir}, localStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected canary-reconcile with missing config dir to fail")
	}
	if err := Run([]string{"canary-reconcile", templateDir, "not-an-int"}, localStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected canary-reconcile with invalid timeout_ms to fail")
	}
	if err := Run([]string{"canary-reconcile", templateDir, "0"}, localStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected canary-reconcile with non-positive timeout_ms to fail")
	}
	if err := Run([]string{"canary-reconcile", templateDir}, localStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected canary-reconcile on undeployed app to fail")
	}

	if err := Run([]string{"deploy", templateDir, "v1.0.0"}, localStateDir, &bytes.Buffer{}); err != nil {
		t.Fatalf("deploy v1.0.0 failed: %v", err)
	}
	if err := Run([]string{"deploy", templateDir, "v1.1.0"}, localStateDir, &bytes.Buffer{}); err != nil {
		t.Fatalf("deploy v1.1.0 failed: %v", err)
	}

	var noopOut bytes.Buffer
	if err := Run([]string{"canary-reconcile", templateDir}, localStateDir, &noopOut); err != nil {
		t.Fatalf("local canary-reconcile noop failed: %v", err)
	}
	var noopDec daemon.CanaryDecision
	if err := json.Unmarshal(noopOut.Bytes(), &noopDec); err != nil {
		t.Fatalf("unmarshal noop CanaryDecision: %v", err)
	}
	if noopDec.Action != "noop" {
		t.Fatalf("expected noop action when canary weight is 0, got %+v", noopDec)
	}

	if err := Run([]string{"canary", templateDir, "25"}, localStateDir, &bytes.Buffer{}); err != nil {
		t.Fatalf("canary set weight failed: %v", err)
	}
	var rollbackOut bytes.Buffer
	if err := Run([]string{"canary-reconcile", templateDir, "40"}, localStateDir, &rollbackOut); err != nil {
		t.Fatalf("local canary-reconcile rollback failed: %v", err)
	}
	var rbDec daemon.CanaryDecision
	if err := json.Unmarshal(rollbackOut.Bytes(), &rbDec); err != nil {
		t.Fatalf("unmarshal rollback CanaryDecision: %v", err)
	}
	if rbDec.Action != "rolled_back" || rbDec.Healthy {
		t.Fatalf("expected rolled_back unhealthy decision, got %+v", rbDec)
	}

	const token = "cli-canary-reconcile-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/canary/reconcile" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req struct {
			OrgID      string `json:"org_id"`
			AppName    string `json:"app_name"`
			HealthPath string `json:"health_path"`
			TimeoutMs  int    `json:"timeout_ms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode remote reconcile request: %v", err)
		}
		if req.OrgID != "org-indie-default" || req.AppName != "fastapi-starter" || req.HealthPath != "/healthz" || req.TimeoutMs != 1200 {
			t.Fatalf("unexpected remote reconcile request: %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(daemon.CanaryDecision{
			OrgID:           req.OrgID,
			AppName:         req.AppName,
			StandbyUpstream: "127.0.0.1:8001",
			Healthy:         true,
			Action:          "promoted",
		})
	}))
	defer srv.Close()

	remoteStateDir := t.TempDir()
	t.Setenv("SMALLCLOUD_DAEMON_URL", srv.URL)
	t.Setenv("SMALLCLOUD_API_TOKEN", token)

	var remoteOut bytes.Buffer
	if err := Run([]string{"canary-reconcile", templateDir, "1200"}, remoteStateDir, &remoteOut); err != nil {
		t.Fatalf("remote canary-reconcile failed: %v", err)
	}
	var remoteDec daemon.CanaryDecision
	if err := json.Unmarshal(remoteOut.Bytes(), &remoteDec); err != nil {
		t.Fatalf("unmarshal remote CanaryDecision: %v", err)
	}
	if remoteDec.Action != "promoted" || !remoteDec.Healthy {
		t.Fatalf("expected promoted healthy decision, got %+v", remoteDec)
	}
	if _, err := os.Stat(filepath.Join(remoteStateDir, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no local state.json when routing canary-reconcile remotely, got %v", err)
	}

	t.Setenv("SMALLCLOUD_API_TOKEN", "wrong-token")
	if err := Run([]string{"canary-reconcile", templateDir}, remoteStateDir, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error on unauthorized remote canary-reconcile, got %v", err)
	}

	t.Setenv("SMALLCLOUD_DAEMON_URL", "")
	t.Setenv("SMALLCLOUD_API_TOKEN", "")
	errStateDir := t.TempDir()

	if err := Run(nil, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected empty args to return usage error")
	}
	if err := Run([]string{"not-a-command"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected unknown command to fail")
	}

	if err := Run([]string{"canary"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected canary without args to fail")
	}
	if err := Run([]string{"canary", templateDir, "abc"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected canary with invalid standby_pct to fail")
	}
	if err := Run([]string{"canary", missingDir, "25"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected canary with missing config to fail")
	}
	if err := Run([]string{"canary", templateDir, "25"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected canary on undeployed app to fail")
	}

	if err := Run([]string{"org-quota"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected org-quota without org_id to fail")
	}
	if err := Run([]string{"org-quota", "org-indie-default", "bad-float"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected org-quota with invalid float to fail")
	}
	if err := Run([]string{"org-quota", "org-indie-default", "-5"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected org-quota with negative quota to fail")
	}

	if err := Run([]string{"prune", templateDir, "not-an-int"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected prune with invalid keep_last to fail")
	}
	if err := Run([]string{"prune", missingDir, "2"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected prune with missing config to fail")
	}
	if err := Run([]string{"prune", templateDir, "-1"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected prune with negative keep_last to fail")
	}

	if err := Run([]string{"destroy", missingDir}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected destroy with missing config to fail")
	}
	if err := Run([]string{"destroy", templateDir}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected destroy on undeployed app to fail")
	}

	fileAsDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileAsDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	if err := Run([]string{"eval"}, fileAsDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected eval with non-directory stateDir to fail")
	}

	if err := Run([]string{"nodes"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected nodes without SMALLCLOUD_DAEMON_URL to fail")
	}
	if err := Run([]string{"rollover", "not-a-timestamp"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected rollover with invalid timestamp to fail")
	}
	if err := Run([]string{"init", t.TempDir(), "--template=unknown"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected init with unsupported template to fail")
	}
	if err := Run([]string{"meter"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected meter without args to fail")
	}
	if err := Run([]string{"meter", templateDir, "bad-float"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected meter with invalid delta_usd to fail")
	}
	if err := Run([]string{"meter", missingDir, "1.0"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected meter with missing config to fail")
	}
	if err := Run([]string{"budget", templateDir, "bad-float"}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected budget with invalid max_monthly_usd to fail")
	}
	if err := Run([]string{"budget", missingDir}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected budget with missing config to fail")
	}
	if err := Run([]string{"budget", templateDir}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected budget read on undeployed app to fail")
	}
	if err := Run([]string{"backup", missingDir, filepath.Join(t.TempDir(), "out.tar.gz")}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected backup with missing config to fail")
	}
	if err := Run([]string{"restore-bundle", filepath.Join(t.TempDir(), "missing.tar.gz")}, errStateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("expected restore-bundle with missing file to fail")
	}
}
