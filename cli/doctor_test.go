package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func findCheck(t *testing.T, checks []DoctorCheck, name string) DoctorCheck {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("expected check %q in %+v", name, checks)
	return DoctorCheck{}
}

func TestRunDoctorFastAPITemplate(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")
	stateDir := t.TempDir()

	report, err := RunDoctor(templateDir, stateDir)
	if err != nil {
		t.Fatalf("RunDoctor failed: %v", err)
	}
	if !report.Passed {
		t.Fatalf("expected Passed == true, got false: %+v", report.Checks)
	}

	for _, name := range []string{"smallcloud.toml", "Dockerfile", "Dockerfile EXPOSE", "Caddyfile", "litestream.yml"} {
		c := findCheck(t, report.Checks, name)
		if c.Status != DoctorStatusPass {
			t.Fatalf("expected check %q to pass, got %+v", name, c)
		}
	}

	cg := findCheck(t, report.Checks, "cgroups")
	if cg.Status != DoctorStatusPass && cg.Status != DoctorStatusWarn {
		t.Fatalf("expected cgroups check pass or warn, got %+v", cg)
	}
	if cg.Status == DoctorStatusWarn && !strings.Contains(cg.Detail, "using stateDir cgroups fallback") {
		t.Fatalf("expected cgroups warn detail to mention fallback, got %q", cg.Detail)
	}
}

func TestRunDoctorMismatchedExposePort(t *testing.T) {
	appDir := t.TempDir()
	stateDir := t.TempDir()

	tomlContent := `[app]
name = "warn-app"
org_id = "org-test"
domain = "warn.smallcloud.dev"
port = 8000
healthcheck = "/healthz"
runtime = "dockerfile"

[budget]
max_monthly_usd = 5.0
`
	if err := os.WriteFile(filepath.Join(appDir, "smallcloud.toml"), []byte(tomlContent), 0o600); err != nil {
		t.Fatalf("write smallcloud.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "Dockerfile"), []byte("FROM python:3.12-slim\nEXPOSE 9090\n"), 0o600); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}

	report, err := RunDoctor(appDir, stateDir)
	if err != nil {
		t.Fatalf("RunDoctor failed: %v", err)
	}
	if !report.Passed {
		t.Fatalf("expected Passed == true when only warnings occur, got %+v", report.Checks)
	}

	expose := findCheck(t, report.Checks, "Dockerfile EXPOSE")
	if expose.Status != DoctorStatusWarn {
		t.Fatalf("expected Dockerfile EXPOSE status warn, got %+v", expose)
	}
	if caddy := findCheck(t, report.Checks, "Caddyfile"); caddy.Status != DoctorStatusWarn {
		t.Fatalf("expected missing Caddyfile status warn, got %+v", caddy)
	}
	if litestream := findCheck(t, report.Checks, "litestream.yml"); litestream.Status != DoctorStatusWarn {
		t.Fatalf("expected missing litestream.yml status warn, got %+v", litestream)
	}
}

func TestRunDoctorMissingConfigAndDockerfile(t *testing.T) {
	appDir := t.TempDir()
	stateDir := t.TempDir()

	report, err := RunDoctor(appDir, stateDir)
	if err != nil {
		t.Fatalf("RunDoctor failed: %v", err)
	}
	if report.Passed {
		t.Fatalf("expected Passed == false when smallcloud.toml and Dockerfile are missing, got %+v", report.Checks)
	}

	if c := findCheck(t, report.Checks, "smallcloud.toml"); c.Status != DoctorStatusFail {
		t.Fatalf("expected smallcloud.toml fail, got %+v", c)
	}
	if c := findCheck(t, report.Checks, "Dockerfile"); c.Status != DoctorStatusFail {
		t.Fatalf("expected Dockerfile fail, got %+v", c)
	}
}

func TestRunDoctorLivePass(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")
	stateDir := t.TempDir()

	caddySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer caddySrv.Close()

	daemonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer daemonSrv.Close()

	report, err := RunDoctorLive(templateDir, stateDir, caddySrv.URL, daemonSrv.URL)
	if err != nil {
		t.Fatalf("RunDoctorLive failed: %v", err)
	}
	if !report.Passed {
		t.Fatalf("expected Passed == true, got false: %+v", report.Checks)
	}

	if c := findCheck(t, report.Checks, "caddy_admin"); c.Status != DoctorStatusPass {
		t.Fatalf("expected caddy_admin pass, got %+v", c)
	}
	if c := findCheck(t, report.Checks, "daemon_healthz"); c.Status != DoctorStatusPass {
		t.Fatalf("expected daemon_healthz pass, got %+v", c)
	}
}

func TestRunDoctorLiveFailures(t *testing.T) {
	templateDir := filepath.Join("..", "templates", "fastapi-sqlite")
	stateDir := t.TempDir()

	caddySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer caddySrv.Close()

	daemonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	daemonURL := daemonSrv.URL
	daemonSrv.Close()

	report, err := RunDoctorLive(templateDir, stateDir, caddySrv.URL, daemonURL)
	if err != nil {
		t.Fatalf("RunDoctorLive failed: %v", err)
	}
	if report.Passed {
		t.Fatalf("expected Passed == false when live checks fail, got %+v", report.Checks)
	}

	if c := findCheck(t, report.Checks, "caddy_admin"); c.Status != DoctorStatusFail {
		t.Fatalf("expected caddy_admin fail, got %+v", c)
	}
	if c := findCheck(t, report.Checks, "daemon_healthz"); c.Status != DoctorStatusFail {
		t.Fatalf("expected daemon_healthz fail, got %+v", c)
	}
}
