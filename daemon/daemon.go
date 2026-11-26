package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/creova-gif/smallcloud/pkg/config"
)

type AppMeta struct {
	OrgID       string `json:"org_id"`
	AppName     string `json:"app_name"`
	Domain      string `json:"domain"`
	Healthcheck string `json:"healthcheck"`
	SQLitePath  string `json:"sqlite_path"`
}

type AppStatus struct {
	OrgID        string            `json:"org_id"`
	AppName      string            `json:"app_name"`
	Route        RouteEntry        `json:"route"`
	Budget       AppBudgetState    `json:"budget"`
	LastSnapshot *SnapshotMetadata `json:"last_snapshot,omitempty"`
}

type DaemonState struct {
	Seq         int                          `json:"seq"`
	Budgets     map[string]*AppBudgetState   `json:"budgets"`
	OrgQuotas   map[string]float64           `json:"org_quotas,omitempty"`
	Routes      map[string]*RouteEntry       `json:"routes"`
	Generations map[string]int               `json:"generations"`
	Snapshots   map[string]*SnapshotMetadata `json:"snapshots"`
	AppMeta     map[string]AppMeta           `json:"app_meta"`
	AuditEvents []AuditEvent                 `json:"audit_events,omitempty"`
	WorkerNodes []WorkerNode                 `json:"worker_nodes,omitempty"`
}

type DeploymentResult struct {
	OrgID        string            `json:"org_id"`
	AppName      string            `json:"app_name"`
	ReleaseID    string            `json:"release_id"`
	ActiveColor  ReleaseColor      `json:"active_color"`
	Upstream     string            `json:"upstream"`
	DeployTimeMs int64             `json:"deploy_time_ms"`
	ColdStartMs  int64             `json:"cold_start_ms"`
	Route        RouteEntry        `json:"route"`
	Cgroup       *CgroupLimits     `json:"cgroup"`
	Budget       AppBudgetState    `json:"budget"`
	LastSnapshot *SnapshotMetadata `json:"last_snapshot,omitempty"`
}

type Daemon struct {
	mu        sync.Mutex
	metaMu    sync.RWMutex
	budget    *BudgetTracker
	cgroups   *CgroupManager
	storage   *StorageManager
	router    *CaddyRouter
	runtime   ContainerRuntime
	notifier  *WebhookNotifier
	audit     *AuditLog
	pool      *WorkerPool
	seq       int
	appMeta   map[string]AppMeta
	snapshots map[string]*SnapshotMetadata
}

func NewDaemon(cgroupRoot, replicaRoot string) *Daemon {
	d := &Daemon{
		cgroups:   NewCgroupManager(cgroupRoot),
		storage:   NewStorageManager(replicaRoot),
		router:    NewCaddyRouter(),
		runtime:   NoopRuntime{},
		audit:     NewAuditLog(defaultAuditMaxEvents),
		pool:      NewWorkerPool(),
		appMeta:   make(map[string]AppMeta),
		snapshots: make(map[string]*SnapshotMetadata),
	}
	d.budget = NewBudgetTrackerWithCallbacks(
		func(orgID, appName string) {
			_, _ = d.syncWALIfPresent(orgID, appName)
			d.router.HaltRoute(orgID, appName)
			if route, ok := d.router.GetRoute(orgID, appName); ok && route.ActiveReleaseID != "" {
				_ = d.Runtime().Stop(context.Background(), orgID, appName, route.ActiveReleaseID)
			}
			d.audit.Record(AuditEvent{
				Type:    "budget_halted",
				OrgID:   orgID,
				AppName: appName,
			})
		},
		func(orgID, appName string) {
			_ = d.router.ResumeRoute(orgID, appName)
			d.audit.Record(AuditEvent{
				Type:    "budget_unhalted",
				OrgID:   orgID,
				AppName: appName,
			})
		},
	)
	d.budget.SetAlertCallback(func(orgID, appName string, state AppBudgetState) {
		d.audit.Record(AuditEvent{
			Type:    "budget_alert",
			OrgID:   orgID,
			AppName: appName,
			Detail:  fmt.Sprintf("spend=%.2f max=%.2f alert_at_pct=%.1f", state.CurrentUSD, state.MaxMonthlyUSD, state.AlertAtPct),
		})
		d.metaMu.RLock()
		notifier := d.notifier
		d.metaMu.RUnlock()
		if notifier != nil && notifier.URL != "" {
			payload := WebhookPayload{
				Event:         "budget_alert",
				OrgID:         orgID,
				AppName:       appName,
				SpendUSD:      state.CurrentUSD,
				MaxMonthlyUSD: state.MaxMonthlyUSD,
				AlertAtPct:    state.AlertAtPct,
				Halted:        state.Halted,
				Timestamp:     time.Now().UTC(),
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = notifier.Notify(ctx, payload)
			}()
		}
	})
	return d
}

func (d *Daemon) Budget() *BudgetTracker   { return d.budget }
func (d *Daemon) Cgroups() *CgroupManager  { return d.cgroups }
func (d *Daemon) Storage() *StorageManager { return d.storage }
func (d *Daemon) Router() *CaddyRouter     { return d.router }
func (d *Daemon) Audit() *AuditLog         { return d.audit }

func (d *Daemon) WorkerPool() *WorkerPool {
	d.metaMu.RLock()
	p := d.pool
	d.metaMu.RUnlock()
	if p != nil {
		return p
	}
	d.metaMu.Lock()
	defer d.metaMu.Unlock()
	if d.pool == nil {
		d.pool = NewWorkerPool()
	}
	return d.pool
}

func (d *Daemon) SetRuntime(rt ContainerRuntime) {
	d.metaMu.Lock()
	defer d.metaMu.Unlock()
	if rt == nil {
		rt = NoopRuntime{}
	}
	d.runtime = rt
}

func (d *Daemon) Runtime() ContainerRuntime {
	d.metaMu.RLock()
	defer d.metaMu.RUnlock()
	return d.runtime
}

func (d *Daemon) SetWebhookNotifier(n *WebhookNotifier) {
	d.metaMu.Lock()
	defer d.metaMu.Unlock()
	d.notifier = n
}

func (d *Daemon) resolveHostSQLitePath(orgID, appName, cfgPath string) string {
	if cfgPath == "" {
		return ""
	}
	if _, err := os.Stat(cfgPath); err == nil {
		return cfgPath
	}
	parent := filepath.Dir(cfgPath)
	if info, err := os.Stat(parent); err == nil && info.IsDir() {
		if testFile, err := os.CreateTemp(parent, ".sc-write-*"); err == nil {
			_ = testFile.Close()
			_ = os.Remove(testFile.Name())
			return cfgPath
		}
	}
	if strings.HasPrefix(cfgPath, "/data/") || cfgPath == "/data" {
		return filepath.Join(d.storage.ReplicaDir(), "hostdata", orgID, appName, filepath.Base(cfgPath))
	}
	return cfgPath
}

func (d *Daemon) syncWALIfPresent(orgID, appName string) (*SnapshotMetadata, error) {
	d.metaMu.RLock()
	meta, ok := d.appMeta[tenantKey(orgID, appName)]
	d.metaMu.RUnlock()
	if !ok || meta.SQLitePath == "" {
		return nil, nil
	}

	resolved := d.resolveHostSQLitePath(orgID, appName, meta.SQLitePath)
	if _, err := os.Stat(resolved); err == nil {
		snap, err := d.storage.SyncWAL(orgID, appName, resolved)
		if err != nil {
			return nil, err
		}
		d.recordSnapshot(orgID, appName, snap)
		return snap, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	if d.storage.HasReplica(orgID, appName) {
		snap, err := d.storage.Restore(orgID, appName, resolved)
		if err != nil {
			return nil, err
		}
		d.recordSnapshot(orgID, appName, snap)
		return snap, nil
	}
	return nil, nil
}

func (d *Daemon) recordSnapshot(orgID, appName string, snap *SnapshotMetadata) {
	if snap == nil {
		return
	}
	d.metaMu.Lock()
	cp := *snap
	d.snapshots[tenantKey(orgID, appName)] = &cp
	d.metaMu.Unlock()
}

func (d *Daemon) Deploy(cfg *config.Config, releaseID string) (*DeploymentResult, error) {
	if cfg == nil {
		return nil, errors.New("config is required")
	}
	start := time.Now()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.budget.RegisterApp(cfg.App.OrgID, cfg.App.Name, cfg.Budget.MaxMonthlyUSD, cfg.Budget.AlertAtPct); err != nil {
		return nil, err
	}
	if err := d.budget.CheckAllowed(cfg.App.OrgID, cfg.App.Name); err != nil {
		return nil, err
	}

	cgLimits, err := d.cgroups.Apply(cfg.App.OrgID, cfg.App.Name, cfg.Resources.CPUMillicores, cfg.Resources.MemoryMB)
	if err != nil {
		return nil, err
	}

	key := tenantKey(cfg.App.OrgID, cfg.App.Name)
	d.metaMu.Lock()
	d.appMeta[key] = AppMeta{
		OrgID:       cfg.App.OrgID,
		AppName:     cfg.App.Name,
		Domain:      cfg.App.Domain,
		Healthcheck: cfg.App.Healthcheck,
		SQLitePath:  cfg.Storage.SQLitePath,
	}
	d.metaMu.Unlock()

	snap, err := d.syncWALIfPresent(cfg.App.OrgID, cfg.App.Name)
	if err != nil {
		return nil, err
	}

	d.seq++
	if releaseID == "" {
		releaseID = fmt.Sprintf("rel-%d", d.seq)
	}
	upstreamPort := 18000 + d.seq

	coldStartBegin := time.Now()
	rt := d.Runtime()
	upstream, err := rt.Start(context.Background(), ContainerSpec{
		OrgID:         cfg.App.OrgID,
		AppName:       cfg.App.Name,
		ReleaseID:     releaseID,
		AppDir:        ".",
		Port:          upstreamPort,
		CPUMillicores: cfg.Resources.CPUMillicores,
		MemoryMB:      cfg.Resources.MemoryMB,
		SQLitePath:    cfg.Storage.SQLitePath,
		Healthcheck:   cfg.App.Healthcheck,
		Env:           cfg.Env,
	})
	if err != nil {
		return nil, err
	}
	route, err := d.router.SwapRoute(cfg.App.OrgID, cfg.App.Name, cfg.App.Domain, releaseID, upstream)
	if err != nil {
		_ = rt.Stop(context.Background(), cfg.App.OrgID, cfg.App.Name, releaseID)
		return nil, err
	}
	coldStartMs := time.Since(coldStartBegin).Milliseconds()
	deployMs := time.Since(start).Milliseconds()

	budgetState, _ := d.budget.State(cfg.App.OrgID, cfg.App.Name)

	d.audit.Record(AuditEvent{
		Type:      "deploy",
		OrgID:     cfg.App.OrgID,
		AppName:   cfg.App.Name,
		ReleaseID: releaseID,
		Detail:    fmt.Sprintf("color=%s upstream=%s", route.ActiveColor, upstream),
	})

	return &DeploymentResult{
		OrgID:        cfg.App.OrgID,
		AppName:      cfg.App.Name,
		ReleaseID:    releaseID,
		ActiveColor:  route.ActiveColor,
		Upstream:     upstream,
		DeployTimeMs: deployMs,
		ColdStartMs:  coldStartMs,
		Route:        route,
		Cgroup:       cgLimits,
		Budget:       budgetState,
		LastSnapshot: snap,
	}, nil
}

func (d *Daemon) Rollback(orgID, appName string) (RouteEntry, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return RouteEntry{}, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.budget.CheckAllowed(orgID, appName); err != nil {
		if _, ok := d.router.GetRoute(orgID, appName); !ok {
			return RouteEntry{}, fmt.Errorf("%w for %s/%s", ErrNoRouteFound, orgID, appName)
		}
		return RouteEntry{}, err
	}

	if _, err := d.syncWALIfPresent(orgID, appName); err != nil {
		return RouteEntry{}, err
	}

	route, err := d.router.Rollback(orgID, appName)
	if err != nil {
		return RouteEntry{}, err
	}
	d.audit.Record(AuditEvent{
		Type:      "rollback",
		OrgID:     orgID,
		AppName:   appName,
		ReleaseID: route.ActiveReleaseID,
		Detail:    fmt.Sprintf("color=%s upstream=%s", route.ActiveColor, route.ActiveUpstream),
	})
	return route, nil
}

func (d *Daemon) RecordSpend(orgID, appName string, deltaUSD float64) (AppBudgetState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.budget.RecordSpend(orgID, appName, deltaUSD)
}

func (d *Daemon) SetMaxBudget(orgID, appName string, maxMonthlyUSD float64) (AppBudgetState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	st, err := d.budget.SetMaxBudget(orgID, appName, maxMonthlyUSD)
	if err != nil {
		return AppBudgetState{}, err
	}
	d.audit.Record(AuditEvent{
		Type:    "budget_updated",
		OrgID:   orgID,
		AppName: appName,
		Detail:  fmt.Sprintf("max_monthly_usd=%.2f", maxMonthlyUSD),
	})
	return st, nil
}

func (d *Daemon) SetCanaryWeight(orgID, appName string, standbyPct int) (RouteEntry, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return RouteEntry{}, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	route, err := d.router.SetCanaryWeight(orgID, appName, standbyPct)
	if err != nil {
		return RouteEntry{}, err
	}
	d.audit.Record(AuditEvent{
		Type:      "canary",
		OrgID:     orgID,
		AppName:   appName,
		ReleaseID: route.StandbyReleaseID,
		Detail:    fmt.Sprintf("canary_weight=%d", route.CanaryWeight),
	})
	return route, nil
}

func (d *Daemon) AppLogs(ctx context.Context, orgID, appName, releaseID string, tail int) (string, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return "", err
	}
	if releaseID == "" {
		route, ok := d.router.GetRoute(orgID, appName)
		if !ok || route.ActiveReleaseID == "" {
			return "", fmt.Errorf("%w for %s/%s", ErrNoRouteFound, orgID, appName)
		}
		releaseID = route.ActiveReleaseID
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return d.Runtime().Logs(ctx, orgID, appName, releaseID, tail)
}

func (d *Daemon) SetOrgQuota(orgID string, maxMonthlyUSD float64) (OrgSpendSummary, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.budget.SetOrgQuota(orgID, maxMonthlyUSD); err != nil {
		return OrgSpendSummary{}, err
	}
	d.audit.Record(AuditEvent{
		Type:   "org_quota_updated",
		OrgID:  orgID,
		Detail: fmt.Sprintf("max_monthly_usd=%.2f", maxMonthlyUSD),
	})
	return d.budget.OrgSummary(orgID), nil
}

func (d *Daemon) OrgSummary(orgID string) (OrgSpendSummary, error) {
	if !config.IsValidSlug(orgID) {
		return OrgSpendSummary{}, errors.New("org_id must be a valid lowercase alphanumeric slug")
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.budget.OrgSummary(orgID), nil
}

func (d *Daemon) RolloverAll(now time.Time) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	count := d.budget.RolloverAll(now)
	if count > 0 {
		d.audit.Record(AuditEvent{
			Type:   "rollover",
			Detail: fmt.Sprintf("rolled_over=%d month=%s", count, now.UTC().Format("2006-01")),
		})
	}
	return count
}

func (d *Daemon) DeleteApp(orgID, appName string) error {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	key := tenantKey(orgID, appName)
	route, hasRoute := d.router.GetRoute(orgID, appName)
	d.metaMu.RLock()
	_, hasMeta := d.appMeta[key]
	d.metaMu.RUnlock()

	if !hasRoute && !hasMeta {
		return fmt.Errorf("%w for %s/%s", ErrNoRouteFound, orgID, appName)
	}

	if hasRoute {
		rt := d.Runtime()
		if route.ActiveReleaseID != "" {
			_ = rt.Stop(context.Background(), orgID, appName, route.ActiveReleaseID)
		}
		if route.StandbyReleaseID != "" && route.StandbyReleaseID != route.ActiveReleaseID {
			_ = rt.Stop(context.Background(), orgID, appName, route.StandbyReleaseID)
		}
	}

	d.router.DeleteRoute(orgID, appName)

	d.metaMu.Lock()
	delete(d.appMeta, key)
	delete(d.snapshots, key)
	d.metaMu.Unlock()

	_ = os.RemoveAll(filepath.Join(d.cgroups.rootDir, "smallcloud", orgID, appName))

	d.audit.Record(AuditEvent{
		Type:      "delete",
		OrgID:     orgID,
		AppName:   appName,
		ReleaseID: route.ActiveReleaseID,
	})

	return nil
}

func (d *Daemon) Status(orgID, appName string) (AppStatus, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return AppStatus{}, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	budgetState, ok := d.budget.State(orgID, appName)
	if !ok {
		return AppStatus{}, fmt.Errorf("%w for %s/%s", ErrNoRouteFound, orgID, appName)
	}
	route, ok := d.router.GetRoute(orgID, appName)
	if !ok {
		return AppStatus{}, fmt.Errorf("%w for %s/%s", ErrNoRouteFound, orgID, appName)
	}

	d.metaMu.RLock()
	snap := d.snapshots[tenantKey(orgID, appName)]
	var snapCopy *SnapshotMetadata
	if snap != nil {
		cp := *snap
		snapCopy = &cp
	}
	d.metaMu.RUnlock()

	return AppStatus{
		OrgID:        orgID,
		AppName:      appName,
		Route:        route,
		Budget:       budgetState,
		LastSnapshot: snapCopy,
	}, nil
}

func (d *Daemon) ListApps() []AppStatus {
	d.mu.Lock()
	defer d.mu.Unlock()

	routes := d.router.SnapshotRoutes()

	d.metaMu.RLock()
	metaCopy := make(map[string]AppMeta, len(d.appMeta))
	for k, v := range d.appMeta {
		metaCopy[k] = v
	}
	snapCopy := make(map[string]*SnapshotMetadata, len(d.snapshots))
	for k, v := range d.snapshots {
		if v == nil {
			continue
		}
		cp := *v
		snapCopy[k] = &cp
	}
	d.metaMu.RUnlock()

	keys := make(map[string]struct{}, len(routes)+len(metaCopy))
	for k := range routes {
		keys[k] = struct{}{}
	}
	for k := range metaCopy {
		keys[k] = struct{}{}
	}

	apps := make([]AppStatus, 0, len(keys))
	for k := range keys {
		meta, hasMeta := metaCopy[k]
		routePtr, hasRoute := routes[k]

		orgID := meta.OrgID
		appName := meta.AppName
		var route RouteEntry
		if hasRoute && routePtr != nil {
			route = *routePtr
			if orgID == "" {
				orgID = route.OrgID
			}
			if appName == "" {
				appName = route.AppName
			}
		}
		if (orgID == "" || appName == "") && hasMeta {
			if parts := strings.SplitN(k, "/", 2); len(parts) == 2 {
				if orgID == "" {
					orgID = parts[0]
				}
				if appName == "" {
					appName = parts[1]
				}
			}
		}

		budgetState, _ := d.budget.State(orgID, appName)

		var snap *SnapshotMetadata
		if s, ok := snapCopy[k]; ok && s != nil {
			cp := *s
			snap = &cp
		}

		apps = append(apps, AppStatus{
			OrgID:        orgID,
			AppName:      appName,
			Route:        route,
			Budget:       budgetState,
			LastSnapshot: snap,
		})
	}

	sort.Slice(apps, func(i, j int) bool {
		if apps[i].OrgID != apps[j].OrgID {
			return apps[i].OrgID < apps[j].OrgID
		}
		return apps[i].AppName < apps[j].AppName
	})

	return apps
}

func (d *Daemon) LoadState(path string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read daemon state %q: %w", path, err)
	}
	if len(data) == 0 {
		return fmt.Errorf("corrupt daemon state %q: empty file", path)
	}

	var st DaemonState
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("decode daemon state %q: %w", path, err)
	}

	d.seq = st.Seq
	d.budget.RestoreStates(st.Budgets)
	d.budget.mu.Lock()
	d.budget.orgQuotas = make(map[string]float64, len(st.OrgQuotas))
	for k, v := range st.OrgQuotas {
		d.budget.orgQuotas[k] = v
	}
	d.budget.mu.Unlock()
	d.router.RestoreRoutes(st.Routes)
	d.storage.RestoreGenerations(st.Generations)
	d.audit.Restore(st.AuditEvents)

	pool := d.WorkerPool()
	pool.mu.Lock()
	pool.nodes = make(map[string]WorkerNode, len(st.WorkerNodes))
	for _, node := range st.WorkerNodes {
		if strings.TrimSpace(node.ID) == "" {
			continue
		}
		pool.nodes[node.ID] = node
	}
	pool.mu.Unlock()

	d.metaMu.Lock()
	d.appMeta = make(map[string]AppMeta, len(st.AppMeta))
	for k, v := range st.AppMeta {
		d.appMeta[k] = v
	}
	d.snapshots = make(map[string]*SnapshotMetadata, len(st.Snapshots))
	for k, v := range st.Snapshots {
		if v == nil {
			continue
		}
		cp := *v
		d.snapshots[k] = &cp
	}
	d.metaMu.Unlock()

	return nil
}

func (d *Daemon) SaveState(path string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.metaMu.RLock()
	metaCopy := make(map[string]AppMeta, len(d.appMeta))
	for k, v := range d.appMeta {
		metaCopy[k] = v
	}
	snapCopy := make(map[string]*SnapshotMetadata, len(d.snapshots))
	for k, v := range d.snapshots {
		if v == nil {
			continue
		}
		cp := *v
		snapCopy[k] = &cp
	}
	d.metaMu.RUnlock()

	d.budget.mu.RLock()
	orgQuotasCopy := make(map[string]float64, len(d.budget.orgQuotas))
	for k, v := range d.budget.orgQuotas {
		orgQuotasCopy[k] = v
	}
	d.budget.mu.RUnlock()

	st := DaemonState{
		Seq:         d.seq,
		Budgets:     d.budget.SnapshotStates(),
		OrgQuotas:   orgQuotasCopy,
		Routes:      d.router.SnapshotRoutes(),
		Generations: d.storage.SnapshotGenerations(),
		Snapshots:   snapCopy,
		AppMeta:     metaCopy,
		AuditEvents: d.audit.Snapshot(),
		WorkerNodes: d.WorkerPool().ListNodes(),
	}

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode daemon state: %w", err)
	}
	return writeFileAtomic(path, data, 0o600)
}
