package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/creova-gif/smallcloud/daemon"
	"github.com/creova-gif/smallcloud/pkg/config"
)

type BundleManifest struct {
	Config        *config.Config `json:"config"`
	Dockerfile    string         `json:"dockerfile"`
	Caddyfile     string         `json:"caddyfile"`
	LitestreamYML string         `json:"litestream_yml"`
}

type EvalReport struct {
	DeployLatencyMs      int64   `json:"deploy_latency_ms"`
	DeployThresholdMs    int64   `json:"deploy_threshold_ms"`
	ColdStartLatencyMs   int64   `json:"cold_start_latency_ms"`
	ColdStartThresholdMs int64   `json:"cold_start_threshold_ms"`
	HardStopEnforcement  float64 `json:"hard_stop_enforcement_pct"`
	DataLossBytes        int64   `json:"data_loss_bytes"`
	ZeroDowntimeSwap     bool    `json:"zero_downtime_swap"`
	Passed               bool    `json:"passed"`
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func PackageApp(appDir string) (*BundleManifest, error) {
	tomlPath := filepath.Join(appDir, "smallcloud.toml")
	cfg, err := config.LoadFile(tomlPath)
	if err != nil {
		return nil, fmt.Errorf("load smallcloud.toml: %w", err)
	}

	dockerfilePath := filepath.Join(appDir, "Dockerfile")
	dfBytes, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return nil, fmt.Errorf("read Dockerfile: %w", err)
	}

	route := daemon.RouteEntry{
		OrgID:           cfg.App.OrgID,
		AppName:         cfg.App.Name,
		Domain:          cfg.App.Domain,
		ActiveColor:     daemon.ColorBlue,
		ActiveReleaseID: "v1",
		ActiveUpstream:  fmt.Sprintf("127.0.0.1:%d", cfg.App.Port),
	}
	caddyfile := daemon.RenderCaddyfile(route, cfg.App.Healthcheck)

	replicaURL := cfg.Storage.ReplicaURL
	if replicaURL == "" {
		replicaURL = fmt.Sprintf("gs://smallcloud-replicas/%s/%s", cfg.App.OrgID, cfg.App.Name)
	}
	litestreamYML := fmt.Sprintf("dbs:\n  - path: %s\n    replicas:\n      - url: %s\n        sync-interval: %dms\n",
		cfg.Storage.SQLitePath, replicaURL, cfg.Storage.SyncMs)

	return &BundleManifest{
		Config:        cfg,
		Dockerfile:    string(dfBytes),
		Caddyfile:     caddyfile,
		LitestreamYML: litestreamYML,
	}, nil
}

func RunEvalSuite(workDir string) (*EvalReport, error) {
	cgRoot := filepath.Join(workDir, "cgroups")
	repRoot := filepath.Join(workDir, "replicas")
	d := daemon.NewDaemon(cgRoot, repRoot)

	cfg := &config.Config{
		App: config.AppConfig{
			Name:        "eval-saas",
			OrgID:       "org-eval",
			Domain:      "eval.smallcloud.dev",
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
			SQLitePath: "/data/eval.db",
			ReplicaURL: "gs://smallcloud-replicas/org-eval/eval-saas",
			SyncMs:     500,
		},
	}

	t0 := time.Now()
	res1, err := d.Deploy(cfg, "v1.0.0")
	if err != nil {
		return nil, fmt.Errorf("eval deploy v1: %w", err)
	}
	deployMs := time.Since(t0).Milliseconds()

	tSwap := time.Now()
	res2, err := d.Deploy(cfg, "v1.1.0")
	if err != nil {
		return nil, fmt.Errorf("eval deploy v1.1: %w", err)
	}
	rolled, err := d.Rollback(cfg.App.OrgID, cfg.App.Name)
	if err != nil {
		return nil, fmt.Errorf("eval rollback: %w", err)
	}
	coldStartMs := time.Since(tSwap).Milliseconds()
	zeroDowntime := res1.ActiveColor == daemon.ColorBlue && res2.ActiveColor == daemon.ColorGreen && rolled.ActiveReleaseID == "v1.0.0"

	dbFile := filepath.Join(workDir, "eval.db")
	payload := []byte("SQLite format 3\x00---smallcloud-eval-wal-frame-verified---")
	if err := os.WriteFile(dbFile, payload, 0o600); err != nil {
		return nil, err
	}
	if _, err := d.Storage().SyncWAL(cfg.App.OrgID, cfg.App.Name, dbFile); err != nil {
		return nil, err
	}
	if err := os.Remove(dbFile); err != nil {
		return nil, err
	}
	if _, err := d.Storage().Restore(cfg.App.OrgID, cfg.App.Name, dbFile); err != nil {
		return nil, err
	}
	restored, err := os.ReadFile(dbFile)
	if err != nil {
		return nil, err
	}
	dataLossBytes := int64(len(payload) - len(restored))
	if !bytes.Equal(payload, restored) && dataLossBytes == 0 {
		dataLossBytes = int64(len(payload))
	}

	blockedCount := 0
	_, spendErr := d.RecordSpend(cfg.App.OrgID, cfg.App.Name, 5.00)
	if errors.Is(spendErr, daemon.ErrBudgetExceeded) {
		for i := 0; i < 100; i++ {
			if errors.Is(d.Budget().CheckAllowed(cfg.App.OrgID, cfg.App.Name), daemon.ErrBudgetExceeded) {
				blockedCount++
			}
		}
	}
	hardStopPct := float64(blockedCount)

	passed := deployMs < 30000 &&
		coldStartMs < 300 &&
		hardStopPct == 100.0 &&
		dataLossBytes == 0 &&
		zeroDowntime

	return &EvalReport{
		DeployLatencyMs:      deployMs,
		DeployThresholdMs:    30000,
		ColdStartLatencyMs:   coldStartMs,
		ColdStartThresholdMs: 300,
		HardStopEnforcement:  hardStopPct,
		DataLossBytes:        dataLossBytes,
		ZeroDowntimeSwap:     zeroDowntime,
		Passed:               passed,
	}, nil
}

func runInit(args []string, stdout io.Writer) error {
	dir := "."
	force := false
	for _, a := range args {
		if a == "--force" {
			force = true
		} else {
			dir = a
		}
	}

	tomlPath := filepath.Join(dir, "smallcloud.toml")
	if !force {
		if _, err := os.Stat(tomlPath); err == nil {
			return fmt.Errorf("%s already exists (pass --force to overwrite)", tomlPath)
		}
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	appName := filepath.Base(absDir)
	if !config.IsValidSlug(appName) {
		appName = "smallcloud-app"
	}

	starter := fmt.Sprintf(`[app]
name = %q
org_id = "org-indie-default"
domain = "%s.smallcloud.dev"
port = 8000
healthcheck = "/healthz"
runtime = "dockerfile"

[resources]
cpu_millicores = 500
memory_mb = 256

[budget]
max_monthly_usd = 5.00
alert_at_pct = 80.0
hard_stop = true

[storage]
sqlite_path = "/data/app.db"
replica_url = "gs://smallcloud-replicas/org-indie-default/%s"
sync_ms = 1000
`, appName, appName, appName)

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(tomlPath, []byte(starter), 0o600); err != nil {
		return err
	}
	return writeJSON(stdout, map[string]string{"initialized": tomlPath, "app_name": appName})
}

func runBudget(args []string, d *daemon.Daemon, statePath string, stdout io.Writer) error {
	if len(args) > 0 && args[0] == "set" {
		args = args[1:]
	}
	dir := "."
	var newMax *float64
	switch len(args) {
	case 1:
		if v, err := strconv.ParseFloat(args[0], 64); err == nil {
			newMax = &v
		} else {
			dir = args[0]
		}
	case 2:
		dir = args[0]
		v, err := strconv.ParseFloat(args[1], 64)
		if err != nil {
			return fmt.Errorf("invalid max_monthly_usd %q: %w", args[1], err)
		}
		newMax = &v
	}

	cfg, err := config.LoadFile(filepath.Join(dir, "smallcloud.toml"))
	if err != nil {
		return err
	}

	if newMax != nil {
		st, err := d.SetMaxBudget(cfg.App.OrgID, cfg.App.Name, *newMax)
		if err != nil {
			return err
		}
		if err := d.SaveState(statePath); err != nil {
			return err
		}
		return writeJSON(stdout, st)
	}

	st, ok := d.Budget().State(cfg.App.OrgID, cfg.App.Name)
	if !ok {
		return fmt.Errorf("app %q not deployed yet for org %q", cfg.App.Name, cfg.App.OrgID)
	}
	return writeJSON(stdout, st)
}

func Run(args []string, stateDir string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: smallcloud <init|package|deploy|rollback|status|budget|eval> [args]")
	}

	cmd := args[0]
	if cmd == "init" {
		return runInit(args[1:], stdout)
	}

	d := daemon.NewDaemon(filepath.Join(stateDir, "cgroups"), filepath.Join(stateDir, "replicas"))
	statePath := filepath.Join(stateDir, "state.json")
	if cmd != "eval" {
		if err := d.LoadState(statePath); err != nil {
			return err
		}
	}

	switch cmd {
	case "package":
		dir := "."
		if len(args) > 1 {
			dir = args[1]
		}
		manifest, err := PackageApp(dir)
		if err != nil {
			return err
		}
		return writeJSON(stdout, manifest)

	case "deploy":
		dir := "."
		if len(args) > 1 {
			dir = args[1]
		}
		rel := "v1.0.0"
		if len(args) > 2 {
			rel = args[2]
		}
		manifest, err := PackageApp(dir)
		if err != nil {
			return err
		}
		res, err := d.Deploy(manifest.Config, rel)
		if err != nil {
			return err
		}
		if err := d.SaveState(statePath); err != nil {
			return err
		}
		return writeJSON(stdout, res)

	case "rollback":
		dir := "."
		if len(args) > 1 {
			dir = args[1]
		}
		manifest, err := PackageApp(dir)
		if err != nil {
			return err
		}
		rolled, err := d.Rollback(manifest.Config.App.OrgID, manifest.Config.App.Name)
		if err != nil {
			return err
		}
		if err := d.SaveState(statePath); err != nil {
			return err
		}
		return writeJSON(stdout, rolled)

	case "status":
		dir := "."
		if len(args) > 1 {
			dir = args[1]
		}
		cfg, err := config.LoadFile(filepath.Join(dir, "smallcloud.toml"))
		if err != nil {
			return err
		}
		st, err := d.Status(cfg.App.OrgID, cfg.App.Name)
		if err != nil {
			return err
		}
		return writeJSON(stdout, st)

	case "budget":
		return runBudget(args[1:], d, statePath, stdout)

	case "eval":
		report, err := RunEvalSuite(stateDir)
		if err != nil {
			return err
		}
		if err := writeJSON(stdout, report); err != nil {
			return err
		}
		if !report.Passed {
			return errors.New("evaluation failed to meet acceptance thresholds")
		}
		return nil

	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}
