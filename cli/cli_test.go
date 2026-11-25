package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

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

func TestCLIDeployRollbackAndEval(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")
	stateDir := t.TempDir()

	var deployOut bytes.Buffer
	if err := Run([]string{"deploy", templateDir, "v2.0.0"}, stateDir, &deployOut); err != nil {
		t.Fatalf("cli deploy failed: %v", err)
	}
	var deployRes daemon.DeploymentResult
	if err := json.Unmarshal(deployOut.Bytes(), &deployRes); err != nil {
		t.Fatalf("unmarshal deploy result: %v", err)
	}
	if deployRes.ReleaseID != "v2.0.0" || deployRes.ActiveColor != "blue" {
		t.Fatalf("unexpected deploy result: %+v", deployRes)
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
