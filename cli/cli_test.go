package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
