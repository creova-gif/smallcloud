package daemon

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
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

func (d *Daemon) Handler() http.Handler {
	return d.HandlerWithAuth(os.Getenv("SMALLCLOUD_API_TOKEN"))
}

func (d *Daemon) HandlerWithAuth(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", d.handleHealthz)
	mux.HandleFunc("POST /v1/deploy", d.handleDeploy)
	mux.HandleFunc("POST /v1/rollback", d.handleRollback)
	mux.HandleFunc("GET /v1/status", d.handleStatus)
	mux.HandleFunc("POST /v1/meter", d.handleMeter)
	mux.HandleFunc("POST /v1/rollover", d.handleRollover)

	if token == "" {
		return mux
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func isValidationError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return len(msg) >= 15 && msg[:15] == "validate config"
}
