package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creova-gif/smallcloud/daemon"
	"github.com/creova-gif/smallcloud/pkg/config"
)

var Version = "v0.1.0"

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
	tmpl := "fastapi"
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--force":
			force = true
		case a == "--template" && i+1 < len(args):
			tmpl = args[i+1]
			i++
		case strings.HasPrefix(a, "--template="):
			tmpl = strings.TrimPrefix(a, "--template=")
		case !strings.HasPrefix(a, "-"):
			dir = a
		}
	}

	var port int
	switch tmpl {
	case "fastapi":
		port = 8000
	case "node", "go":
		port = 8080
	default:
		return fmt.Errorf("unsupported template %q (expected fastapi, node, or go)", tmpl)
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
port = %d
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
`, appName, appName, port, appName)

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(tomlPath, []byte(starter), 0o600); err != nil {
		return err
	}
	return writeJSON(stdout, map[string]string{
		"initialized": tomlPath,
		"app_name":    appName,
		"template":    tmpl,
	})
}

func runBudget(args []string, d *daemon.Daemon, client *DaemonClient, statePath string, stdout io.Writer) error {
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

	if client != nil {
		ctx := context.Background()
		if newMax != nil {
			st, err := client.SetMaxBudget(ctx, cfg.App.OrgID, cfg.App.Name, *newMax)
			if err != nil {
				return err
			}
			return writeJSON(stdout, st)
		}
		st, err := client.Status(ctx, cfg.App.OrgID, cfg.App.Name)
		if err != nil {
			return err
		}
		return writeJSON(stdout, st.Budget)
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

func runMeter(args []string, d *daemon.Daemon, client *DaemonClient, statePath string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: smallcloud meter [dir] <delta_usd>")
	}
	dir := "."
	rawDelta := args[0]
	if len(args) >= 2 {
		dir = args[0]
		rawDelta = args[1]
	}
	deltaUSD, err := strconv.ParseFloat(rawDelta, 64)
	if err != nil {
		return fmt.Errorf("invalid delta_usd %q: %w", rawDelta, err)
	}

	cfg, err := config.LoadFile(filepath.Join(dir, "smallcloud.toml"))
	if err != nil {
		return err
	}

	if client != nil {
		st, err := client.Meter(context.Background(), cfg.App.OrgID, cfg.App.Name, deltaUSD)
		if err != nil {
			return err
		}
		return writeJSON(stdout, st)
	}

	st, err := d.RecordSpend(cfg.App.OrgID, cfg.App.Name, deltaUSD)
	if err != nil {
		if errors.Is(err, daemon.ErrBudgetExceeded) {
			_ = d.SaveState(statePath)
			_ = writeJSON(stdout, st)
		}
		return err
	}
	if err := d.SaveState(statePath); err != nil {
		return err
	}
	return writeJSON(stdout, st)
}

func ServeWithContext(ctx context.Context, addr, stateDir string, stdout io.Writer) error {
	if addr == "" {
		addr = "127.0.0.1:7420"
	}
	rolloverInterval := time.Hour
	if raw := strings.TrimSpace(os.Getenv("SMALLCLOUD_ROLLOVER_INTERVAL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("invalid SMALLCLOUD_ROLLOVER_INTERVAL %q: %w", raw, err)
		}
		if parsed <= 0 {
			return fmt.Errorf("SMALLCLOUD_ROLLOVER_INTERVAL must be > 0, got %s", parsed)
		}
		rolloverInterval = parsed
	}
	var probeTimeout time.Duration
	if raw := strings.TrimSpace(os.Getenv("SMALLCLOUD_HEALTH_PROBE_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("invalid SMALLCLOUD_HEALTH_PROBE_TIMEOUT %q: %w", raw, err)
		}
		if parsed <= 0 {
			return fmt.Errorf("SMALLCLOUD_HEALTH_PROBE_TIMEOUT must be > 0, got %s", parsed)
		}
		probeTimeout = parsed
	}
	d := daemon.NewDaemon(filepath.Join(stateDir, "cgroups"), filepath.Join(stateDir, "replicas"))
	if caddyURL := strings.TrimSpace(os.Getenv("SMALLCLOUD_CADDY_ADMIN_URL")); caddyURL != "" {
		d.Router().SetApplier(daemon.HTTPAdminApplier{AdminURL: caddyURL})
	}
	if webhookURL := strings.TrimSpace(os.Getenv("SMALLCLOUD_WEBHOOK_URL")); webhookURL != "" {
		d.SetWebhookNotifier(&daemon.WebhookNotifier{
			URL:    webhookURL,
			Secret: os.Getenv("SMALLCLOUD_WEBHOOK_SECRET"),
		})
	}
	if strings.TrimSpace(os.Getenv("SMALLCLOUD_RUNTIME")) == "docker" {
		rt := &daemon.DockerRuntime{}
		if probeTimeout > 0 {
			rt.HealthProbeTimeout = probeTimeout
		}
		d.SetRuntime(rt)
	}
	statePath := filepath.Join(stateDir, "state.json")
	if err := d.LoadState(statePath); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %q: %w", addr, err)
	}
	_ = writeJSON(stdout, map[string]string{"listening": ln.Addr().String(), "state": statePath})

	handler := d.Handler()
	accessLogEnv := strings.ToLower(strings.TrimSpace(os.Getenv("SMALLCLOUD_ACCESS_LOG")))
	if accessLogEnv != "0" && accessLogEnv != "false" {
		handler = daemon.StructuredAccessLogMiddleware(slog.New(slog.NewJSONHandler(os.Stderr, nil)))(handler)
	}

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	rollCtx, rollCancel := context.WithCancel(ctx)
	defer rollCancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(rolloverInterval)
		defer ticker.Stop()
		for {
			select {
			case <-rollCtx.Done():
				return
			case <-ticker.C:
				if rolled := d.RolloverAll(time.Now().UTC()); rolled > 0 {
					_ = d.SaveState(statePath)
				}
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		rollCancel()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		wg.Wait()
		if err := d.SaveState(statePath); err != nil {
			return err
		}
		return <-errCh
	case err := <-errCh:
		rollCancel()
		wg.Wait()
		if saveErr := d.SaveState(statePath); saveErr != nil && err == nil {
			return saveErr
		}
		return err
	}
}

func Run(args []string, stateDir string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: smallcloud <init|doctor|package|deploy|rollback|status|budget|org-quota|meter|rollover|list|destroy|events|logs|canary|prune|verify-snapshot|backup|restore-bundle|nodes|serve|version|eval> [args]")
	}

	cmd := args[0]
	if cmd == "version" || cmd == "--version" {
		return writeJSON(stdout, map[string]string{"version": Version})
	}
	if cmd == "init" {
		return runInit(args[1:], stdout)
	}
	if cmd == "doctor" {
		dir := "."
		live := false
		var caddyURL, daemonURL string
		for i := 1; i < len(args); i++ {
			a := args[i]
			switch {
			case a == "--live":
				live = true
			case a == "--caddy" && i+1 < len(args):
				caddyURL = args[i+1]
				i++
			case strings.HasPrefix(a, "--caddy="):
				caddyURL = strings.TrimPrefix(a, "--caddy=")
			case a == "--daemon" && i+1 < len(args):
				daemonURL = args[i+1]
				i++
			case strings.HasPrefix(a, "--daemon="):
				daemonURL = strings.TrimPrefix(a, "--daemon=")
			case !strings.HasPrefix(a, "-"):
				dir = a
			}
		}
		var report *DoctorReport
		var err error
		if live {
			if caddyURL == "" {
				caddyURL = strings.TrimSpace(os.Getenv("SMALLCLOUD_CADDY_ADMIN_URL"))
			}
			if daemonURL == "" {
				daemonURL = strings.TrimSpace(os.Getenv("SMALLCLOUD_DAEMON_URL"))
			}
			report, err = RunDoctorLive(dir, stateDir, caddyURL, daemonURL)
		} else {
			report, err = RunDoctor(dir, stateDir)
		}
		if err != nil {
			return err
		}
		if err := writeJSON(stdout, report); err != nil {
			return err
		}
		if !report.Passed {
			return errors.New("doctor found failing preflight checks")
		}
		return nil
	}
	if cmd == "serve" {
		addr := "127.0.0.1:7420"
		for i := 1; i < len(args); i++ {
			if args[i] == "--addr" && i+1 < len(args) {
				addr = args[i+1]
				i++
			}
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return ServeWithContext(ctx, addr, stateDir, stdout)
	}

	var d *daemon.Daemon
	var client *DaemonClient
	statePath := filepath.Join(stateDir, "state.json")
	if daemonURL := strings.TrimSpace(os.Getenv("SMALLCLOUD_DAEMON_URL")); daemonURL != "" && (cmd == "deploy" || cmd == "rollback" || cmd == "status" || cmd == "budget" || cmd == "org-quota" || cmd == "meter" || cmd == "rollover" || cmd == "list" || cmd == "ps" || cmd == "destroy" || cmd == "events" || cmd == "logs" || cmd == "canary" || cmd == "prune" || cmd == "backup" || cmd == "restore-bundle" || cmd == "nodes") {
		client = NewDaemonClient(daemonURL, os.Getenv("SMALLCLOUD_API_TOKEN"))
	} else {
		d = daemon.NewDaemon(filepath.Join(stateDir, "cgroups"), filepath.Join(stateDir, "replicas"))
		if cmd != "eval" {
			if err := d.LoadState(statePath); err != nil {
				return err
			}
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
		if client != nil {
			res, err := client.Deploy(context.Background(), manifest.Config, rel)
			if err != nil {
				return err
			}
			return writeJSON(stdout, res)
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
		if client != nil {
			rolled, err := client.Rollback(context.Background(), manifest.Config.App.OrgID, manifest.Config.App.Name)
			if err != nil {
				return err
			}
			return writeJSON(stdout, rolled)
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
		if client != nil {
			st, err := client.Status(context.Background(), cfg.App.OrgID, cfg.App.Name)
			if err != nil {
				return err
			}
			return writeJSON(stdout, st)
		}
		st, err := d.Status(cfg.App.OrgID, cfg.App.Name)
		if err != nil {
			return err
		}
		return writeJSON(stdout, st)

	case "budget":
		return runBudget(args[1:], d, client, statePath, stdout)

	case "org-quota":
		if len(args) < 2 {
			return errors.New("usage: smallcloud org-quota <org_id> [max_monthly_usd]")
		}
		orgID := args[1]
		if len(args) >= 3 {
			maxMonthlyUSD, err := strconv.ParseFloat(args[2], 64)
			if err != nil {
				return fmt.Errorf("invalid max_monthly_usd %q: %w", args[2], err)
			}
			if client != nil {
				summary, err := client.SetOrgQuota(context.Background(), orgID, maxMonthlyUSD)
				if err != nil {
					return err
				}
				return writeJSON(stdout, summary)
			}
			if err := d.Budget().SetOrgQuota(orgID, maxMonthlyUSD); err != nil {
				return err
			}
			if err := d.SaveState(statePath); err != nil {
				return err
			}
			return writeJSON(stdout, d.Budget().OrgSummary(orgID))
		}
		if client != nil {
			summary, err := client.OrgSummary(context.Background(), orgID)
			if err != nil {
				return err
			}
			return writeJSON(stdout, summary)
		}
		return writeJSON(stdout, d.Budget().OrgSummary(orgID))

	case "meter":
		return runMeter(args[1:], d, client, statePath, stdout)

	case "rollover":
		ts := time.Now().UTC()
		if len(args) > 1 {
			parsed, err := time.Parse(time.RFC3339, args[1])
			if err != nil {
				return fmt.Errorf("invalid RFC3339 timestamp %q: %w", args[1], err)
			}
			ts = parsed.UTC()
		}
		if client != nil {
			n, err := client.Rollover(context.Background(), ts)
			if err != nil {
				return err
			}
			return writeJSON(stdout, map[string]any{"rolled_over": n, "timestamp": ts})
		}
		n := d.RolloverAll(ts)
		if err := d.SaveState(statePath); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"rolled_over": n, "timestamp": ts})

	case "list", "ps":
		if client != nil {
			apps, err := client.ListApps(context.Background())
			if err != nil {
				return err
			}
			return writeJSON(stdout, map[string]any{"apps": apps})
		}
		return writeJSON(stdout, map[string]any{"apps": d.ListApps()})

	case "destroy":
		dir := "."
		if len(args) > 1 {
			dir = args[1]
		}
		cfg, err := config.LoadFile(filepath.Join(dir, "smallcloud.toml"))
		if err != nil {
			return err
		}
		if client != nil {
			if err := client.DeleteApp(context.Background(), cfg.App.OrgID, cfg.App.Name); err != nil {
				return err
			}
		} else {
			if err := d.DeleteApp(cfg.App.OrgID, cfg.App.Name); err != nil {
				return err
			}
			if err := d.SaveState(statePath); err != nil {
				return err
			}
		}
		return writeJSON(stdout, map[string]any{
			"deleted":  true,
			"org_id":   cfg.App.OrgID,
			"app_name": cfg.App.Name,
		})

	case "events":
		var orgID, appName string
		if len(args) > 1 {
			cfg, err := config.LoadFile(filepath.Join(args[1], "smallcloud.toml"))
			if err != nil {
				return err
			}
			orgID = cfg.App.OrgID
			appName = cfg.App.Name
		}
		if client != nil {
			events, err := client.Events(context.Background(), orgID, appName, 50)
			if err != nil {
				return err
			}
			return writeJSON(stdout, map[string]any{"events": events})
		}
		return writeJSON(stdout, map[string]any{"events": d.Audit().List(orgID, appName, 50)})

	case "logs":
		dir := "."
		if len(args) > 1 {
			dir = args[1]
		}
		releaseID := ""
		if len(args) > 2 {
			releaseID = args[2]
		}
		cfg, err := config.LoadFile(filepath.Join(dir, "smallcloud.toml"))
		if err != nil {
			return err
		}
		var logs string
		if client != nil {
			ctx := context.Background()
			if releaseID == "" {
				st, err := client.Status(ctx, cfg.App.OrgID, cfg.App.Name)
				if err != nil {
					return err
				}
				releaseID = st.Route.ActiveReleaseID
			}
			logs, err = client.Logs(ctx, cfg.App.OrgID, cfg.App.Name, releaseID, 100)
			if err != nil {
				return err
			}
		} else {
			if releaseID == "" {
				st, err := d.Status(cfg.App.OrgID, cfg.App.Name)
				if err != nil {
					return err
				}
				releaseID = st.Route.ActiveReleaseID
			}
			logs, err = d.Runtime().Logs(context.Background(), cfg.App.OrgID, cfg.App.Name, releaseID, 100)
			if err != nil {
				return err
			}
		}
		return writeJSON(stdout, map[string]string{
			"org_id":     cfg.App.OrgID,
			"app_name":   cfg.App.Name,
			"release_id": releaseID,
			"logs":       logs,
		})

	case "canary":
		if len(args) < 2 {
			return errors.New("usage: smallcloud canary [dir] <standby_pct>")
		}
		dir := "."
		rawPct := args[1]
		if len(args) >= 3 {
			dir = args[1]
			rawPct = args[2]
		}
		pct, err := strconv.Atoi(rawPct)
		if err != nil {
			return fmt.Errorf("invalid standby_pct %q: %w", rawPct, err)
		}
		cfg, err := config.LoadFile(filepath.Join(dir, "smallcloud.toml"))
		if err != nil {
			return err
		}
		if client != nil {
			route, err := client.SetCanaryWeight(context.Background(), cfg.App.OrgID, cfg.App.Name, pct)
			if err != nil {
				return err
			}
			return writeJSON(stdout, route)
		}
		route, err := d.Router().SetCanaryWeight(cfg.App.OrgID, cfg.App.Name, pct)
		if err != nil {
			return err
		}
		if err := d.SaveState(statePath); err != nil {
			return err
		}
		return writeJSON(stdout, route)

	case "prune":
		dir := "."
		keepLast := 5
		switch len(args) {
		case 2:
			if v, err := strconv.Atoi(args[1]); err == nil {
				keepLast = v
			} else {
				dir = args[1]
			}
		default:
			if len(args) >= 3 {
				dir = args[1]
				v, err := strconv.Atoi(args[2])
				if err != nil {
					return fmt.Errorf("invalid keep_last %q: %w", args[2], err)
				}
				keepLast = v
			}
		}
		cfg, err := config.LoadFile(filepath.Join(dir, "smallcloud.toml"))
		if err != nil {
			return err
		}
		var pruned int
		if client != nil {
			pruned, err = client.PruneSnapshots(context.Background(), cfg.App.OrgID, cfg.App.Name, keepLast)
			if err != nil {
				return err
			}
		} else {
			pruned, err = d.Storage().PruneSnapshots(cfg.App.OrgID, cfg.App.Name, keepLast)
			if err != nil {
				return err
			}
		}
		return writeJSON(stdout, map[string]any{
			"org_id":    cfg.App.OrgID,
			"app_name":  cfg.App.Name,
			"keep_last": keepLast,
			"pruned":    pruned,
		})

	case "verify-snapshot":
		dir := "."
		if len(args) > 1 {
			dir = args[1]
		}
		cfg, err := config.LoadFile(filepath.Join(dir, "smallcloud.toml"))
		if err != nil {
			return err
		}
		meta, err := d.Storage().VerifySnapshot(cfg.App.OrgID, cfg.App.Name)
		if err != nil {
			return err
		}
		return writeJSON(stdout, meta)

	case "backup":
		if len(args) < 2 {
			return errors.New("usage: smallcloud backup [dir] <dest.tar.gz>")
		}
		dir := "."
		destPath := args[1]
		if len(args) >= 3 {
			dir = args[1]
			destPath = args[2]
		}
		cfg, err := config.LoadFile(filepath.Join(dir, "smallcloud.toml"))
		if err != nil {
			return err
		}
		if client != nil {
			if err := client.DownloadBackup(context.Background(), cfg.App.OrgID, cfg.App.Name, destPath); err != nil {
				return err
			}
			manifest, err := daemon.VerifyBackupBundle(destPath)
			if err != nil {
				return err
			}
			return writeJSON(stdout, manifest)
		}
		manifest, err := daemon.ExportBackupBundle(d, cfg.App.OrgID, cfg.App.Name, destPath)
		if err != nil {
			return err
		}
		return writeJSON(stdout, manifest)

	case "restore-bundle":
		if len(args) < 2 {
			return errors.New("usage: smallcloud restore-bundle <bundle.tar.gz>")
		}
		if client != nil {
			manifest, err := client.RestoreBackup(context.Background(), args[1])
			if err != nil {
				return err
			}
			return writeJSON(stdout, manifest)
		}
		manifest, err := daemon.RestoreBackupBundle(d, args[1])
		if err != nil {
			return err
		}
		if err := d.SaveState(statePath); err != nil {
			return err
		}
		return writeJSON(stdout, manifest)

	case "nodes":
		if client == nil {
			return errors.New("nodes requires SMALLCLOUD_DAEMON_URL to be set")
		}
		nodes, err := client.ListNodes(context.Background())
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"nodes": nodes})

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
