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
