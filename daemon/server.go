package daemon

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/creova-gif/smallcloud/pkg/config"
)

const maxRequestBodyBytes = 1 << 20

type deployRequest struct {
	Config    config.Config `json:"config"`
	ReleaseID string        `json:"release_id"`
}

type rollbackRequest struct {
	OrgID   string `json:"org_id"`
	AppName string `json:"app_name"`
}

type meterRequest struct {
	OrgID         string  `json:"org_id"`
	AppName       string  `json:"app_name"`
	DeltaUSD      float64 `json:"delta_usd"`
	MaxMonthlyUSD float64 `json:"max_monthly_usd"`
}

type rolloverRequest struct {
	Timestamp time.Time `json:"timestamp"`
}

type canaryRequest struct {
	OrgID        string `json:"org_id"`
	AppName      string `json:"app_name"`
	CanaryWeight int    `json:"canary_weight"`
}

type canaryReconcileRequest struct {
	OrgID      string `json:"org_id"`
	AppName    string `json:"app_name"`
	HealthPath string `json:"health_path"`
	TimeoutMs  int    `json:"timeout_ms"`
}

type orgQuotaRequest struct {
	OrgID         string  `json:"org_id"`
	MaxMonthlyUSD float64 `json:"max_monthly_usd"`
}

type pruneRequest struct {
	OrgID    string `json:"org_id"`
	AppName  string `json:"app_name"`
	KeepLast int    `json:"keep_last"`
}

type nodeHeartbeatRequest struct {
	ID         string `json:"id"`
	ActiveApps int    `json:"active_apps"`
}

func (d *Daemon) Handler() http.Handler {
	return d.HandlerWithAuth(os.Getenv("SMALLCLOUD_API_TOKEN"))
}

func (d *Daemon) HandlerWithAuth(token string) http.Handler {
	metrics := &HTTPMetrics{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", d.handleUI)
	mux.HandleFunc("GET /ui", d.handleUI)
	mux.HandleFunc("GET /healthz", d.handleHealthz)
	mux.HandleFunc("GET /readyz", d.handleReadyz)
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		d.writeMetrics(w, metrics)
	})
	mux.HandleFunc("POST /v1/deploy", d.handleDeploy)
	mux.HandleFunc("POST /v1/rollback", d.handleRollback)
	mux.HandleFunc("POST /v1/canary", d.handleCanary)
	mux.HandleFunc("POST /v1/canary/reconcile", d.handleCanaryReconcile)
	mux.HandleFunc("GET /v1/apps", d.handleListApps)
	mux.HandleFunc("DELETE /v1/apps", d.handleDeleteApp)
	mux.HandleFunc("GET /v1/status", d.handleStatus)
	mux.HandleFunc("GET /v1/logs", d.handleLogs)
	mux.HandleFunc("GET /v1/events", d.handleEvents)
	mux.HandleFunc("POST /v1/meter", d.handleMeter)
	mux.HandleFunc("POST /v1/org-quota", d.handleSetOrgQuota)
	mux.HandleFunc("GET /v1/org-quota", d.handleGetOrgQuota)
	mux.HandleFunc("POST /v1/prune", d.handlePrune)
	mux.HandleFunc("POST /v1/rollover", d.handleRollover)
	mux.HandleFunc("POST /v1/nodes/register", d.handleNodeRegister)
	mux.HandleFunc("POST /v1/nodes/heartbeat", d.handleNodeHeartbeat)
	mux.HandleFunc("GET /v1/nodes", d.handleListNodes)
	mux.HandleFunc("GET /v1/nodes/select", d.handleSelectNode)
	mux.HandleFunc("DELETE /v1/nodes", d.handleDeleteNode)
	mux.HandleFunc("GET /v1/backup", d.handleBackup)
	mux.HandleFunc("POST /v1/restore", d.handleRestore)
	mux.HandleFunc("POST /webhooks/github", d.handleGitHubWebhook)

	var handler http.Handler = mux
	if token != "" {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/v1/") {
				auth := r.Header.Get("Authorization")
				const prefix = "Bearer "
				if !strings.HasPrefix(auth, prefix) || subtle.ConstantTimeCompare([]byte(auth[len(prefix):]), []byte(token)) != 1 {
					writeHTTPError(w, http.StatusUnauthorized, "unauthorized")
					return
				}
			}
			mux.ServeHTTP(w, r)
		})
	}

	handler = RejectFormContentTypeMiddleware(handler)
	trustedProxies := ParseTrustedProxies(os.Getenv("SMALLCLOUD_TRUSTED_PROXIES"))
	handler = RateLimitMiddlewareWithProxies(NewRateLimiter(rateLimitPerMinute(), time.Minute), trustedProxies, nil)(handler)
	handler = metrics.Middleware(handler)
	handler = SecurityHeadersMiddleware(handler)
	handler = RequestIDMiddleware(handler)
	return handler
}

func rateLimitPerMinute() int {
	const defaultRateLimit = 600
	raw := strings.TrimSpace(os.Getenv("SMALLCLOUD_RATE_LIMIT"))
	if raw == "" {
		return defaultRateLimit
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return defaultRateLimit
	}
	return v
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeHTTPError(w, http.StatusRequestEntityTooLarge, "request body exceeds 1 MiB limit")
			return false
		}
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return false
	}
	if dec.More() || dec.Decode(&struct{}{}) != io.EOF {
		writeHTTPError(w, http.StatusBadRequest, "request body must contain a single JSON object")
		return false
	}
	return true
}

func writeHTTPJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeHTTPError(w http.ResponseWriter, status int, msg string) {
	writeHTTPJSON(w, status, map[string]string{"error": msg})
}

func (d *Daemon) handleDeploy(w http.ResponseWriter, r *http.Request) {
	var req deployRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	res, err := d.Deploy(&req.Config, req.ReleaseID)
	if err != nil {
		switch {
		case errors.Is(err, ErrBudgetExceeded):
			writeHTTPError(w, http.StatusPaymentRequired, err.Error())
		case isValidationError(err):
			writeHTTPError(w, http.StatusBadRequest, err.Error())
		default:
			writeHTTPError(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	writeHTTPJSON(w, http.StatusOK, res)
}

func (d *Daemon) handleRollback(w http.ResponseWriter, r *http.Request) {
	var req rollbackRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	route, err := d.Rollback(req.OrgID, req.AppName)
	if err != nil {
		switch {
		case errors.Is(err, ErrBudgetExceeded):
			writeHTTPError(w, http.StatusPaymentRequired, err.Error())
		case errors.Is(err, ErrNoRouteFound):
			writeHTTPError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, ErrNoStandbyRelease):
			writeHTTPError(w, http.StatusConflict, err.Error())
		default:
			writeHTTPError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	writeHTTPJSON(w, http.StatusOK, route)
}

func (d *Daemon) handleStatus(w http.ResponseWriter, r *http.Request) {
	orgID := r.URL.Query().Get("org_id")
	appName := r.URL.Query().Get("app_name")
	if err := validateTenantSlugs(orgID, appName); err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}

	st, err := d.Status(orgID, appName)
	if err != nil {
		if errors.Is(err, ErrNoRouteFound) {
			writeHTTPError(w, http.StatusNotFound, err.Error())
			return
		}
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeHTTPJSON(w, http.StatusOK, st)
}

func (d *Daemon) handleListApps(w http.ResponseWriter, r *http.Request) {
	writeHTTPJSON(w, http.StatusOK, map[string]any{
		"apps": d.ListApps(),
	})
}

func (d *Daemon) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	orgID := r.URL.Query().Get("org_id")
	appName := r.URL.Query().Get("app")
	if appName == "" {
		appName = r.URL.Query().Get("app_name")
	}
	if err := validateTenantSlugs(orgID, appName); err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := d.DeleteApp(orgID, appName); err != nil {
		if errors.Is(err, ErrNoRouteFound) {
			writeHTTPError(w, http.StatusNotFound, err.Error())
			return
		}
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeHTTPJSON(w, http.StatusOK, map[string]any{
		"deleted":  true,
		"org_id":   orgID,
		"app_name": appName,
	})
}

func (d *Daemon) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	orgID := q.Get("org_id")
	appName := q.Get("app")
	eventType := strings.TrimSpace(r.URL.Query().Get("type"))
	limit := 0
	if rawLimit := q.Get("limit"); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil {
			writeHTTPError(w, http.StatusBadRequest, "limit must be an integer")
			return
		}
		limit = parsed
	}
	writeHTTPJSON(w, http.StatusOK, map[string]any{
		"events": d.Audit().ListFiltered(orgID, appName, eventType, limit),
	})
}

func (d *Daemon) handleMeter(w http.ResponseWriter, r *http.Request) {
	var req meterRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if err := validateTenantSlugs(req.OrgID, req.AppName); err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}

	var st AppBudgetState
	var err error
	if req.MaxMonthlyUSD > 0 {
		st, err = d.SetMaxBudget(req.OrgID, req.AppName, req.MaxMonthlyUSD)
		if err != nil {
			writeHTTPError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	if req.DeltaUSD != 0 || req.MaxMonthlyUSD == 0 {
		st, err = d.RecordSpend(req.OrgID, req.AppName, req.DeltaUSD)
		if err != nil {
			if errors.Is(err, ErrBudgetExceeded) {
				writeHTTPJSON(w, http.StatusPaymentRequired, st)
				return
			}
			writeHTTPError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	writeHTTPJSON(w, http.StatusOK, st)
}

func (d *Daemon) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeHTTPJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d *Daemon) handleReadyz(w http.ResponseWriter, r *http.Request) {
	writeHTTPJSON(w, http.StatusOK, map[string]any{
		"ready": true,
		"apps":  len(d.ListApps()),
		"nodes": len(d.WorkerPool().ListNodes()),
	})
}

func escapePromLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func (d *Daemon) writeMetrics(w http.ResponseWriter, metrics *HTTPMetrics) {
	apps := d.ListApps()
	nodes := d.WorkerPool().ListNodes()
	healthyNodes := 0
	for _, node := range nodes {
		if node.Healthy {
			healthyNodes++
		}
	}

	var requestsTotal uint64
	var errorsTotal uint64
	if metrics != nil {
		requestsTotal = metrics.RequestsTotal.Load()
		errorsTotal = metrics.ErrorsTotal.Load()
	}

	var buf bytes.Buffer

	fmt.Fprintf(&buf, "# TYPE smallcloud_apps_total gauge\n")
	fmt.Fprintf(&buf, "smallcloud_apps_total %d\n", len(apps))

	fmt.Fprintf(&buf, "# TYPE smallcloud_app_budget_limit_usd gauge\n")
	for _, app := range apps {
		org := escapePromLabel(app.OrgID)
		name := escapePromLabel(app.AppName)
		fmt.Fprintf(&buf, "smallcloud_app_budget_limit_usd{org_id=\"%s\",app=\"%s\"} %s\n", org, name, strconv.FormatFloat(app.Budget.MaxMonthlyUSD, 'f', -1, 64))
	}

	fmt.Fprintf(&buf, "# TYPE smallcloud_app_budget_spend_usd gauge\n")
	for _, app := range apps {
		org := escapePromLabel(app.OrgID)
		name := escapePromLabel(app.AppName)
		fmt.Fprintf(&buf, "smallcloud_app_budget_spend_usd{org_id=\"%s\",app=\"%s\"} %s\n", org, name, strconv.FormatFloat(app.Budget.CurrentUSD, 'f', -1, 64))
	}

	fmt.Fprintf(&buf, "# TYPE smallcloud_app_halted gauge\n")
	for _, app := range apps {
		org := escapePromLabel(app.OrgID)
		name := escapePromLabel(app.AppName)
		halted := 0
		if app.Budget.Halted {
			halted = 1
		}
		fmt.Fprintf(&buf, "smallcloud_app_halted{org_id=\"%s\",app=\"%s\"} %d\n", org, name, halted)
	}

	fmt.Fprintf(&buf, "# TYPE smallcloud_http_requests_total counter\n")
	fmt.Fprintf(&buf, "smallcloud_http_requests_total %d\n", requestsTotal)

	fmt.Fprintf(&buf, "# TYPE smallcloud_http_errors_total counter\n")
	fmt.Fprintf(&buf, "smallcloud_http_errors_total %d\n", errorsTotal)

	fmt.Fprintf(&buf, "# TYPE smallcloud_worker_nodes_total gauge\n")
	fmt.Fprintf(&buf, "smallcloud_worker_nodes_total %d\n", len(nodes))

	fmt.Fprintf(&buf, "# TYPE smallcloud_worker_nodes_healthy gauge\n")
	fmt.Fprintf(&buf, "smallcloud_worker_nodes_healthy %d\n", healthyNodes)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func (d *Daemon) handleRollover(w http.ResponseWriter, r *http.Request) {
	ts := time.Now().UTC()
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				writeHTTPError(w, http.StatusRequestEntityTooLarge, "request body exceeds 1 MiB limit")
				return
			}
			writeHTTPError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(bytes.TrimSpace(raw)) > 0 {
			var req rolloverRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				writeHTTPError(w, http.StatusBadRequest, err.Error())
				return
			}
			if !req.Timestamp.IsZero() {
				ts = req.Timestamp.UTC()
			}
		}
	}

	count := d.RolloverAll(ts)
	writeHTTPJSON(w, http.StatusOK, map[string]any{
		"rolled_over": count,
		"month":       ts.UTC().Format("2006-01"),
	})
}

func (d *Daemon) handleCanary(w http.ResponseWriter, r *http.Request) {
	var req canaryRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	route, err := d.SetCanaryWeight(req.OrgID, req.AppName, req.CanaryWeight)
	if err != nil {
		if errors.Is(err, ErrNoRouteFound) {
			writeHTTPError(w, http.StatusNotFound, err.Error())
			return
		}
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeHTTPJSON(w, http.StatusOK, route)
}

func (d *Daemon) handleCanaryReconcile(w http.ResponseWriter, r *http.Request) {
	var req canaryReconcileRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	if err := validateTenantSlugs(req.OrgID, req.AppName); err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}

	healthPath := strings.TrimSpace(req.HealthPath)
	if healthPath == "" {
		d.metaMu.RLock()
		meta, ok := d.appMeta[tenantKey(req.OrgID, req.AppName)]
		d.metaMu.RUnlock()
		if ok && strings.TrimSpace(meta.Healthcheck) != "" {
			healthPath = strings.TrimSpace(meta.Healthcheck)
		} else {
			healthPath = "/healthz"
		}
	}

	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 2000
	}
	if timeoutMs > 30000 {
		timeoutMs = 30000
	}

	decision, err := EvaluateAndReconcileCanary(
		r.Context(),
		d,
		req.OrgID,
		req.AppName,
		healthPath,
		25*time.Millisecond,
		time.Duration(timeoutMs)*time.Millisecond,
	)
	if err != nil {
		if errors.Is(err, ErrNoRouteFound) {
			writeHTTPError(w, http.StatusNotFound, err.Error())
			return
		}
		writeHTTPError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeHTTPJSON(w, http.StatusOK, decision)
}

func (d *Daemon) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	orgID := q.Get("org_id")
	appName := q.Get("app")
	if appName == "" {
		appName = q.Get("app_name")
	}
	if err := validateTenantSlugs(orgID, appName); err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}

	tail := 0
	if rawTail := q.Get("tail"); rawTail != "" {
		parsed, err := strconv.Atoi(rawTail)
		if err != nil || parsed < 0 {
			writeHTTPError(w, http.StatusBadRequest, "tail must be a non-negative integer")
			return
		}
		tail = parsed
	}

	releaseID := q.Get("release_id")
	if releaseID == "" {
		route, ok := d.router.GetRoute(orgID, appName)
		if !ok || route.ActiveReleaseID == "" {
			writeHTTPError(w, http.StatusNotFound, fmt.Sprintf("%s for %s/%s", ErrNoRouteFound.Error(), orgID, appName))
			return
		}
		releaseID = route.ActiveReleaseID
	}

	logs, err := d.AppLogs(r.Context(), orgID, appName, releaseID, tail)
	if err != nil {
		if errors.Is(err, ErrNoRouteFound) {
			writeHTTPError(w, http.StatusNotFound, err.Error())
			return
		}
		writeHTTPError(w, http.StatusBadGateway, err.Error())
		return
	}

	writeHTTPJSON(w, http.StatusOK, map[string]any{
		"org_id":     orgID,
		"app_name":   appName,
		"release_id": releaseID,
		"logs":       logs,
	})
}

func (d *Daemon) handleSetOrgQuota(w http.ResponseWriter, r *http.Request) {
	var req orgQuotaRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if !config.IsValidSlug(req.OrgID) {
		writeHTTPError(w, http.StatusBadRequest, "org_id must be a valid lowercase alphanumeric slug")
		return
	}

	summary, err := d.SetOrgQuota(req.OrgID, req.MaxMonthlyUSD)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeHTTPJSON(w, http.StatusOK, summary)
}

func (d *Daemon) handleGetOrgQuota(w http.ResponseWriter, r *http.Request) {
	orgID := r.URL.Query().Get("org_id")
	summary, err := d.OrgSummary(orgID)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeHTTPJSON(w, http.StatusOK, summary)
}

func (d *Daemon) handlePrune(w http.ResponseWriter, r *http.Request) {
	var req pruneRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	pruned, err := d.Storage().PruneSnapshots(req.OrgID, req.AppName, req.KeepLast)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeHTTPJSON(w, http.StatusOK, map[string]int{
		"pruned": pruned,
	})
}

func (d *Daemon) handleNodeRegister(w http.ResponseWriter, r *http.Request) {
	var node WorkerNode
	if !decodeJSONBody(w, r, &node) {
		return
	}

	registered, err := d.WorkerPool().RegisterNode(node)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeHTTPJSON(w, http.StatusOK, registered)
}

func (d *Daemon) handleNodeHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req nodeHeartbeatRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	node, err := d.WorkerPool().Heartbeat(req.ID, req.ActiveApps)
	if err != nil {
		if errors.Is(err, ErrNodeNotFound) {
			writeHTTPError(w, http.StatusNotFound, err.Error())
			return
		}
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeHTTPJSON(w, http.StatusOK, node)
}

func (d *Daemon) handleListNodes(w http.ResponseWriter, r *http.Request) {
	writeHTTPJSON(w, http.StatusOK, map[string]any{
		"nodes": d.WorkerPool().ListNodes(),
	})
}

func (d *Daemon) handleSelectNode(w http.ResponseWriter, r *http.Request) {
	region := strings.TrimSpace(r.URL.Query().Get("region"))
	node, err := d.WorkerPool().SelectNode(region)
	if err != nil {
		if errors.Is(err, ErrNoHealthyNode) {
			writeHTTPError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeHTTPError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeHTTPJSON(w, http.StatusOK, node)
}

func (d *Daemon) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeHTTPError(w, http.StatusBadRequest, "worker node id is required")
		return
	}
	if !d.WorkerPool().RemoveNode(id) {
		writeHTTPError(w, http.StatusNotFound, fmt.Sprintf("%s: %s", ErrNodeNotFound.Error(), id))
		return
	}
	d.Audit().Record(AuditEvent{
		Type:   "node_removed",
		Detail: fmt.Sprintf("id=%s", id),
	})
	writeHTTPJSON(w, http.StatusOK, map[string]any{
		"deleted": true,
		"id":      id,
	})
}

func (d *Daemon) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	secret := os.Getenv("SMALLCLOUD_GITHUB_WEBHOOK_SECRET")
	if secret == "" {
		writeHTTPError(w, http.StatusServiceUnavailable, "github webhook secret is not configured")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeHTTPError(w, http.StatusRequestEntityTooLarge, "request body exceeds 1 MiB limit")
			return
		}
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := VerifyGitHubSignature(secret, body, r.Header.Get("X-Hub-Signature-256")); err != nil {
		writeHTTPError(w, http.StatusUnauthorized, err.Error())
		return
	}

	ev, err := ParseGitHubPushEvent(body)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}

	orgID := ""
	appName := ev.Repository
	if parts := strings.SplitN(ev.Repository, "/", 2); len(parts) == 2 {
		orgID = parts[0]
		appName = parts[1]
	}
	d.Audit().Record(AuditEvent{
		Type:      "gitops_push",
		OrgID:     orgID,
		AppName:   appName,
		ReleaseID: ev.CommitSHA,
		Detail:    fmt.Sprintf("repo=%s branch=%s commit=%s pusher=%s", ev.Repository, ev.Branch, ev.CommitSHA, ev.Pusher),
	})

	writeHTTPJSON(w, http.StatusOK, ev)
}

func (d *Daemon) handleBackup(w http.ResponseWriter, r *http.Request) {
	orgID := r.URL.Query().Get("org_id")
	appName := r.URL.Query().Get("app")
	if err := validateTenantSlugs(orgID, appName); err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}

	tmpFile, err := os.CreateTemp(os.TempDir(), "smallcloud-backup-*.tar.gz")
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tmpPath := tmpFile.Name()
	_ = tmpFile.Close()
	defer os.Remove(tmpPath)

	if _, err := ExportBackupBundle(d, orgID, appName, tmpPath); err != nil {
		if errors.Is(err, ErrNoRouteFound) {
			writeHTTPError(w, http.StatusNotFound, err.Error())
			return
		}
		writeHTTPError(w, http.StatusInternalServerError, err.Error())
		return
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s-backup.tar.gz"`, orgID, appName))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func (d *Daemon) handleRestore(w http.ResponseWriter, r *http.Request) {
	const maxRestoreBodyBytes = 64 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxRestoreBodyBytes)

	tmpFile, err := os.CreateTemp(os.TempDir(), "smallcloud-restore-*.tar.gz")
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmpFile, r.Body); err != nil {
		_ = tmpFile.Close()
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeHTTPError(w, http.StatusRequestEntityTooLarge, "request body exceeds 64 MiB limit")
			return
		}
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := tmpFile.Close(); err != nil {
		writeHTTPError(w, http.StatusInternalServerError, err.Error())
		return
	}

	manifest, err := RestoreBackupBundle(d, tmpPath)
	if err != nil {
		writeHTTPError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeHTTPJSON(w, http.StatusOK, manifest)
}

func isValidationError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return len(msg) >= 15 && msg[:15] == "validate config"
}
