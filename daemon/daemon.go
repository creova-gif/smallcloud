package daemon

import (
	"fmt"
	"sync"
	"time"

	"github.com/creova-gif/smallcloud/pkg/config"
)

// DeploymentResult summarizes a completed deployment or rollback operation.
type DeploymentResult struct {
	OrgID        string         `json:"org_id"`
	AppName      string         `json:"app_name"`
	ReleaseID    string         `json:"release_id"`
	ActiveColor  string         `json:"active_color"`
	Upstream     string         `json:"upstream"`
	DeployTimeMs int64          `json:"deploy_time_ms"`
	ColdStartMs  int64          `json:"cold_start_ms"`
	Route        RouteEntry     `json:"route"`
	Cgroup       *CgroupLimits  `json:"cgroup"`
	Budget       AppBudgetState `json:"budget"`
}

// Daemon coordinates cgroups v2 isolation, Litestream SQLite persistence, Caddy routing, and spend caps.
type Daemon struct {
	mu      sync.Mutex
	budget  *BudgetTracker
	cgroups *CgroupManager
	storage *StorageManager
	router  *CaddyRouter
	seq     int
}

func NewDaemon(cgroupRoot, replicaRoot string) *Daemon {
	router := NewCaddyRouter()
	budget := NewBudgetTracker(func(orgID, appName string) {
		router.HaltRoute(orgID, appName)
	})
	return &Daemon{
		budget:  budget,
		cgroups: NewCgroupManager(cgroupRoot),
		storage: NewStorageManager(replicaRoot),
		router:  router,
	}
}

func (d *Daemon) Budget() *BudgetTracker   { return d.budget }
func (d *Daemon) Cgroups() *CgroupManager  { return d.cgroups }
func (d *Daemon) Storage() *StorageManager { return d.storage }
func (d *Daemon) Router() *CaddyRouter     { return d.router }

// Deploy validates the config, enforces budget eligibility, configures cgroups v2, and swaps the Blue/Green route.
func (d *Daemon) Deploy(cfg *config.Config, releaseID string) (*DeploymentResult, error) {
	start := time.Now()
	if err := cfg.Validate(true); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

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

	d.mu.Lock()
	d.seq++
	if releaseID == "" {
		releaseID = fmt.Sprintf("rel-%d", d.seq)
	}
	upstreamPort := 18000 + d.seq
	d.mu.Unlock()

	coldStartBegin := time.Now()
	upstream := fmt.Sprintf("127.0.0.1:%d", upstreamPort)
	route, err := d.router.SwapRoute(cfg.App.OrgID, cfg.App.Name, cfg.App.Domain, releaseID, upstream)
	if err != nil {
		return nil, err
	}
	coldStartMs := time.Since(coldStartBegin).Milliseconds()
	deployMs := time.Since(start).Milliseconds()

	budgetState, _ := d.budget.State(cfg.App.OrgID, cfg.App.Name)

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
	}, nil
}

// Rollback atomically reverts traffic to the standby release and returns the updated route.
func (d *Daemon) Rollback(orgID, appName string) (RouteEntry, error) {
	if err := d.budget.CheckAllowed(orgID, appName); err != nil {
		return RouteEntry{}, err
	}
	return d.router.Rollback(orgID, appName)
}
