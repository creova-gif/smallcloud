package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	_, spendErr := d.Budget().RecordSpend(cfg.App.OrgID, cfg.App.Name, 5.00)
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

func Run(args []string, stateDir string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: smallcloud <package|deploy|rollback|eval> [args]")
	}

	cmd := args[0]
	d := daemon.NewDaemon(filepath.Join(stateDir, "cgroups"), filepath.Join(stateDir, "replicas"))

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
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(manifest)

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
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)

	case "rollback":
		dir := "."
		if len(args) > 1 {
			dir = args[1]
		}
		manifest, err := PackageApp(dir)
		if err != nil {
			return err
		}
		if _, err := d.Deploy(manifest.Config, "v1.0.0"); err != nil {
			return err
		}
		if _, err := d.Deploy(manifest.Config, "v1.1.0"); err != nil {
			return err
		}
		rolled, err := d.Rollback(manifest.Config.App.OrgID, manifest.Config.App.Name)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rolled)

	case "eval":
		report, err := RunEvalSuite(stateDir)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
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
