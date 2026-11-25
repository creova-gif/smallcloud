package daemon

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

type ReleaseColor string

const (
	ColorBlue  ReleaseColor = "blue"
	ColorGreen ReleaseColor = "green"
)

type RouteEntry struct {
	OrgID            string       `json:"org_id"`
	AppName          string       `json:"app_name"`
	Domain           string       `json:"domain"`
	ActiveColor      ReleaseColor `json:"active_color"`
	ActiveReleaseID  string       `json:"active_release_id"`
	ActiveUpstream   string       `json:"active_upstream"`
	StandbyColor     ReleaseColor `json:"standby_color"`
	StandbyReleaseID string       `json:"standby_release_id"`
	StandbyUpstream  string       `json:"standby_upstream"`
	Halted           bool         `json:"halted"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

type CaddyRouter struct {
	mu     sync.RWMutex
	routes map[string]*RouteEntry
}

func NewCaddyRouter() *CaddyRouter {
	return &CaddyRouter{
		routes: make(map[string]*RouteEntry),
	}
}

func (r *CaddyRouter) SwapRoute(orgID, appName, domain, releaseID, upstream string) (RouteEntry, error) {
	if orgID == "" || appName == "" || domain == "" || releaseID == "" || upstream == "" {
		return RouteEntry{}, errors.New("orgID, appName, domain, releaseID, and upstream are required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := tenantKey(orgID, appName)
	curr, exists := r.routes[key]
	now := time.Now().UTC()

	if !exists {
		entry := &RouteEntry{
			OrgID:           orgID,
			AppName:         appName,
			Domain:          domain,
			ActiveColor:     ColorBlue,
			ActiveReleaseID: releaseID,
			ActiveUpstream:  upstream,
			UpdatedAt:       now,
		}
		r.routes[key] = entry
		return *entry, nil
	}

	nextColor := ColorGreen
	if curr.ActiveColor == ColorGreen {
		nextColor = ColorBlue
	}

	curr.StandbyColor = curr.ActiveColor
	curr.StandbyReleaseID = curr.ActiveReleaseID
	curr.StandbyUpstream = curr.ActiveUpstream

	curr.Domain = domain
	curr.ActiveColor = nextColor
	curr.ActiveReleaseID = releaseID
	curr.ActiveUpstream = upstream
	curr.Halted = false
	curr.UpdatedAt = now

	return *curr, nil
}

func (r *CaddyRouter) Rollback(orgID, appName string) (RouteEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := tenantKey(orgID, appName)
	curr, exists := r.routes[key]
	if !exists {
		return RouteEntry{}, fmt.Errorf("no route registered for %s/%s", orgID, appName)
	}
	if curr.StandbyReleaseID == "" || curr.StandbyUpstream == "" {
		return RouteEntry{}, fmt.Errorf("no standby release available to roll back for %s/%s", orgID, appName)
	}

	curr.ActiveColor, curr.StandbyColor = curr.StandbyColor, curr.ActiveColor
	curr.ActiveReleaseID, curr.StandbyReleaseID = curr.StandbyReleaseID, curr.ActiveReleaseID
	curr.ActiveUpstream, curr.StandbyUpstream = curr.StandbyUpstream, curr.ActiveUpstream
	curr.UpdatedAt = time.Now().UTC()

	return *curr, nil
}

func (r *CaddyRouter) HaltRoute(orgID, appName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if curr, exists := r.routes[tenantKey(orgID, appName)]; exists {
		curr.Halted = true
		curr.UpdatedAt = time.Now().UTC()
	}
}

func (r *CaddyRouter) GetRoute(orgID, appName string) (RouteEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	curr, exists := r.routes[tenantKey(orgID, appName)]
	if !exists {
		return RouteEntry{}, false
	}
	return *curr, true
}

func RenderCaddyfile(entry RouteEntry, healthPath string) string {
	if entry.Halted {
		return fmt.Sprintf("%s {\n\trespond \"402 Payment Required: SmallCloud monthly spend cap reached\" 402\n}\n", entry.Domain)
	}
	if healthPath == "" {
		healthPath = "/healthz"
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
