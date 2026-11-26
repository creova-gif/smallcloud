package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUIDashboardRoutes(t *testing.T) {
	d := newTestServerDaemon(t)

	for _, tc := range []struct {
		name    string
		handler http.Handler
	}{
		{name: "unauthenticated", handler: d.HandlerWithAuth("")},
		{name: "with_bearer_auth", handler: d.HandlerWithAuth("secret")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/", "/ui"} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				rec := httptest.NewRecorder()
				tc.handler.ServeHTTP(rec, req)

				if rec.Code != http.StatusOK {
					t.Fatalf("GET %s expected 200, got %d: %s", path, rec.Code, rec.Body.String())
				}
				ct := rec.Header().Get("Content-Type")
				if !strings.Contains(ct, "text/html") {
					t.Fatalf("GET %s expected text/html Content-Type, got %q", path, ct)
				}
				body := rec.Body.String()
				for _, want := range []string{
					"SmallCloud",
					"sessionStorage",
					"Refresh",
					"/v1/apps",
					"/v1/events",
					"/v1/rollback",
					"/v1/canary/reconcile",
					"/v1/prune",
					"Rollback",
					"Reconcile Canary",
					"Prune",
					":focus-visible",
					`role="status"`,
					`role="alert"`,
					`aria-live="polite"`,
					"table-wrap",
					`scope="col"`,
					"main-content",
					"event-type-filter",
					"kpi-grid",
					"nodes-heading",
					"/v1/nodes",
					"aria-busy",
					"window.confirm",
				} {
					if !strings.Contains(body, want) {
						t.Fatalf("GET %s body missing %q", path, want)
					}
				}
			}
		})
	}
}

func TestUIAuthEnforcementOnAPIRoutes(t *testing.T) {
	d := newTestServerDaemon(t)
	h := d.HandlerWithAuth("secret")

	for _, path := range []string{"/v1/apps", "/v1/events"} {
		unauthReq := httptest.NewRequest(http.MethodGet, path, nil)
		unauthRec := httptest.NewRecorder()
		h.ServeHTTP(unauthRec, unauthReq)
		if unauthRec.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s without auth expected 401, got %d", path, unauthRec.Code)
		}

		authReq := httptest.NewRequest(http.MethodGet, path, nil)
		authReq.Header.Set("Authorization", "Bearer secret")
		authRec := httptest.NewRecorder()
		h.ServeHTTP(authRec, authReq)
		if authRec.Code != http.StatusOK {
			t.Fatalf("GET %s with valid auth expected 200, got %d: %s", path, authRec.Code, authRec.Body.String())
		}
	}
}
