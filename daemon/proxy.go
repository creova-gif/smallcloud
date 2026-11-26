package daemon

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ReleaseColor string

const (
	ColorBlue  ReleaseColor = "blue"
	ColorGreen ReleaseColor = "green"
)

type ProxyApplier interface {
	ApplyConfig(domain, caddyfile string) error
}

type NoopApplier struct{}

func (NoopApplier) ApplyConfig(_, _ string) error {
	return nil
}

type HTTPAdminApplier struct {
	AdminURL string
	Client   *http.Client
}

func (a HTTPAdminApplier) ApplyConfig(_ string, caddyfile string) error {
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	endpoint := strings.TrimRight(a.AdminURL, "/") + "/load"
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(caddyfile))
	if err != nil {
		return fmt.Errorf("build caddy admin request: %w", err)
	}
	req.Header.Set("Content-Type", "text/caddyfile")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("apply caddy config: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("caddy admin rejected config (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

type ReleaseRecord struct {
	ReleaseID  string       `json:"release_id"`
	Color      ReleaseColor `json:"color"`
	Upstream   string       `json:"upstream"`
	DeployedAt time.Time    `json:"deployed_at"`
}

const maxReleaseHistory = 5

type RouteEntry struct {
	OrgID            string          `json:"org_id"`
	AppName          string          `json:"app_name"`
	Domain           string          `json:"domain"`
	ActiveColor      ReleaseColor    `json:"active_color"`
	ActiveReleaseID  string          `json:"active_release_id"`
	ActiveUpstream   string          `json:"active_upstream"`
	StandbyColor     ReleaseColor    `json:"standby_color"`
	StandbyReleaseID string          `json:"standby_release_id"`
	StandbyUpstream  string          `json:"standby_upstream"`
	CanaryWeight     int             `json:"canary_weight,omitempty"`
	Halted           bool            `json:"halted"`
	UpdatedAt        time.Time       `json:"updated_at"`
	History          []ReleaseRecord `json:"history,omitempty"`
}

func appendReleaseHistory(history []ReleaseRecord, rec ReleaseRecord) []ReleaseRecord {
	next := make([]ReleaseRecord, 0, len(history)+1)
	next = append(next, history...)
	next = append(next, rec)
	if len(next) > maxReleaseHistory {
		next = next[len(next)-maxReleaseHistory:]
	}
	out := make([]ReleaseRecord, len(next))
	copy(out, next)
	return out
}

func cloneRouteEntry(entry RouteEntry) RouteEntry {
	if entry.History != nil {
		hist := make([]ReleaseRecord, len(entry.History))
		copy(hist, entry.History)
		entry.History = hist
	}
	return entry
}

type CaddyRouter struct {
	mu      sync.RWMutex
	routes  map[string]*RouteEntry
	applier ProxyApplier
}

func NewCaddyRouter() *CaddyRouter {
	return NewCaddyRouterWithApplier(NoopApplier{})
}

func NewCaddyRouterWithApplier(applier ProxyApplier) *CaddyRouter {
	if applier == nil {
		applier = NoopApplier{}
	}
	return &CaddyRouter{
		routes:  make(map[string]*RouteEntry),
		applier: applier,
	}
}

func (r *CaddyRouter) SetApplier(applier ProxyApplier) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if applier == nil {
		applier = NoopApplier{}
	}
	r.applier = applier
}

func (r *CaddyRouter) SwapRoute(orgID, appName, domain, releaseID, upstream string) (RouteEntry, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return RouteEntry{}, err
	}
	if domain == "" || releaseID == "" || upstream == "" {
		return RouteEntry{}, errors.New("domain, releaseID, and upstream are required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := tenantKey(orgID, appName)
	curr, exists := r.routes[key]
	var prevCopy RouteEntry
	if exists {
		prevCopy = *curr
	}
	now := time.Now().UTC()

	var next RouteEntry
	if !exists {
		next = RouteEntry{
			OrgID:           orgID,
			AppName:         appName,
			Domain:          domain,
			ActiveColor:     ColorBlue,
			ActiveReleaseID: releaseID,
			ActiveUpstream:  upstream,
			UpdatedAt:       now,
			History: appendReleaseHistory(nil, ReleaseRecord{
				ReleaseID:  releaseID,
				Color:      ColorBlue,
				Upstream:   upstream,
				DeployedAt: now,
			}),
		}
	} else {
		nextColor := ColorGreen
		if curr.ActiveColor == ColorGreen {
			nextColor = ColorBlue
		}
		next = RouteEntry{
			OrgID:            orgID,
			AppName:          appName,
			Domain:           domain,
			ActiveColor:      nextColor,
			ActiveReleaseID:  releaseID,
			ActiveUpstream:   upstream,
			StandbyColor:     curr.ActiveColor,
			StandbyReleaseID: curr.ActiveReleaseID,
			StandbyUpstream:  curr.ActiveUpstream,
			CanaryWeight:     0,
			Halted:           false,
			UpdatedAt:        now,
			History: appendReleaseHistory(curr.History, ReleaseRecord{
				ReleaseID:  releaseID,
				Color:      nextColor,
				Upstream:   upstream,
				DeployedAt: now,
			}),
		}
	}

	if err := r.applier.ApplyConfig(next.Domain, RenderCaddyfile(next, "")); err != nil {
		if exists {
			restored := prevCopy
			r.routes[key] = &restored
		}
		return RouteEntry{}, err
	}

	r.routes[key] = &next
	return cloneRouteEntry(next), nil
}

var (
	ErrNoRouteFound     = errors.New("no route registered")
	ErrNoStandbyRelease = errors.New("no standby release available to roll back")
)

func (r *CaddyRouter) Rollback(orgID, appName string) (RouteEntry, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return RouteEntry{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := tenantKey(orgID, appName)
	curr, exists := r.routes[key]
	if !exists {
		return RouteEntry{}, fmt.Errorf("%w for %s/%s", ErrNoRouteFound, orgID, appName)
	}
	if curr.Halted {
		return RouteEntry{}, ErrBudgetExceeded
	}
	if curr.StandbyReleaseID == "" || curr.StandbyUpstream == "" {
		return RouteEntry{}, fmt.Errorf("%w for %s/%s", ErrNoStandbyRelease, orgID, appName)
	}

	now := time.Now().UTC()
	prevCopy := *curr
	next := *curr
	next.ActiveColor, next.StandbyColor = curr.StandbyColor, curr.ActiveColor
	next.ActiveReleaseID, next.StandbyReleaseID = curr.StandbyReleaseID, curr.ActiveReleaseID
	next.ActiveUpstream, next.StandbyUpstream = curr.StandbyUpstream, curr.ActiveUpstream
	next.CanaryWeight = 0
	next.UpdatedAt = now
	next.History = appendReleaseHistory(curr.History, ReleaseRecord{
		ReleaseID:  next.ActiveReleaseID,
		Color:      next.ActiveColor,
		Upstream:   next.ActiveUpstream,
		DeployedAt: now,
	})

	if err := r.applier.ApplyConfig(next.Domain, RenderCaddyfile(next, "")); err != nil {
		r.routes[key] = &prevCopy
		return RouteEntry{}, err
	}

	r.routes[key] = &next
	return cloneRouteEntry(next), nil
}

func (r *CaddyRouter) SetCanaryWeight(orgID, appName string, standbyPct int) (RouteEntry, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return RouteEntry{}, err
	}
	if standbyPct < 0 || standbyPct > 100 {
		return RouteEntry{}, errors.New("canary weight must be between 0 and 100")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := tenantKey(orgID, appName)
	curr, exists := r.routes[key]
	if !exists {
		return RouteEntry{}, fmt.Errorf("%w for %s/%s", ErrNoRouteFound, orgID, appName)
	}
	if standbyPct > 0 && curr.StandbyUpstream == "" {
		return RouteEntry{}, errors.New("no standby upstream available for canary routing")
	}

	prevCopy := *curr
	curr.CanaryWeight = standbyPct
	curr.UpdatedAt = time.Now().UTC()

	if err := r.applier.ApplyConfig(curr.Domain, RenderCaddyfile(*curr, "")); err != nil {
		*curr = prevCopy
		return RouteEntry{}, err
	}

	return cloneRouteEntry(*curr), nil
}

func (r *CaddyRouter) HaltRoute(orgID, appName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if curr, exists := r.routes[tenantKey(orgID, appName)]; exists {
		curr.Halted = true
		curr.UpdatedAt = time.Now().UTC()
		_ = r.applier.ApplyConfig(curr.Domain, RenderCaddyfile(*curr, ""))
	}
}

func (r *CaddyRouter) ResumeRoute(orgID, appName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	curr, exists := r.routes[tenantKey(orgID, appName)]
	if !exists {
		return nil
	}
	prev := *curr
	curr.Halted = false
	curr.UpdatedAt = time.Now().UTC()
	if err := r.applier.ApplyConfig(curr.Domain, RenderCaddyfile(*curr, "")); err != nil {
		*curr = prev
		return err
	}
	return nil
}

func (r *CaddyRouter) GetRoute(orgID, appName string) (RouteEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	curr, exists := r.routes[tenantKey(orgID, appName)]
	if !exists {
		return RouteEntry{}, false
	}
	return cloneRouteEntry(*curr), true
}

func (r *CaddyRouter) DeleteRoute(orgID, appName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := tenantKey(orgID, appName)
	if _, exists := r.routes[key]; !exists {
		return false
	}
	delete(r.routes, key)
	return true
}

func (r *CaddyRouter) SnapshotRoutes() map[string]*RouteEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[string]*RouteEntry, len(r.routes))
	for k, v := range r.routes {
		cp := cloneRouteEntry(*v)
		out[k] = &cp
	}
	return out
}

func (r *CaddyRouter) RestoreRoutes(routes map[string]*RouteEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.routes = make(map[string]*RouteEntry, len(routes))
	for k, v := range routes {
		if v == nil {
			continue
		}
		cp := cloneRouteEntry(*v)
		r.routes[k] = &cp
	}
}

func RenderCaddyfile(entry RouteEntry, healthPath string) string {
	if entry.Halted {
		return fmt.Sprintf("%s {\n\theader Retry-After 3600\n\trespond \"402 Payment Required: SmallCloud monthly spend cap reached\" 402\n}\n", entry.Domain)
	}
	if healthPath == "" {
		healthPath = "/healthz"
	}
	if entry.CanaryWeight > 0 && entry.StandbyUpstream != "" {
		activeWeight := 100 - entry.CanaryWeight
		return fmt.Sprintf(`%s {
	encode zstd gzip
	reverse_proxy %s %s {
		lb_policy weighted_round_robin %d %d
		health_uri %s
		health_interval 5s
		lb_try_duration 2s
	}
}
`, entry.Domain, entry.ActiveUpstream, entry.StandbyUpstream, activeWeight, entry.CanaryWeight, healthPath)
	}
	return fmt.Sprintf(`%s {
	encode zstd gzip
	reverse_proxy %s {
		health_uri %s
		health_interval 5s
		lb_try_duration 2s
	}
}
`, entry.Domain, entry.ActiveUpstream, healthPath)
}
