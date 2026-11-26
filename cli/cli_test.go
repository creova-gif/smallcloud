package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
