package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRateLimiterLimitAndWindowReset(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	if !rl.Allow("client-1", base) {
		t.Fatal("expected first request to be allowed")
	}
	if !rl.Allow("client-1", base.Add(10*time.Second)) {
		t.Fatal("expected second request to be allowed")
	}
	if rl.Allow("client-1", base.Add(20*time.Second)) {
		t.Fatal("expected third request within window to be rejected")
	}

	if !rl.Allow("client-2", base.Add(20*time.Second)) {
		t.Fatal("expected separate client key to be allowed")
	}

	if !rl.Allow("client-1", base.Add(time.Minute)) {
		t.Fatal("expected request after window reset to be allowed")
	}
}

func TestRateLimiterBoundedEviction(t *testing.T) {
	rl := NewRateLimiter(5, time.Minute)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	for i := 0; i < maxRateLimiterKeys; i++ {
		key := fmt.Sprintf("key-%d", i)
		if !rl.Allow(key, base.Add(time.Duration(i)*time.Millisecond)) {
			t.Fatalf("expected initial key %s to be allowed", key)
		}
	}

	if len(rl.entries) != maxRateLimiterKeys {
		t.Fatalf("expected %d entries, got %d", maxRateLimiterKeys, len(rl.entries))
	}

	if !rl.Allow("overflow-key", base.Add(5*time.Second)) {
		t.Fatal("expected overflow key to be allowed after eviction")
	}

	if len(rl.entries) > maxRateLimiterKeys {
		t.Fatalf("expected bounded entries <= %d, got %d", maxRateLimiterKeys, len(rl.entries))
	}
	if _, exists := rl.entries["key-0"]; exists {
		t.Fatal("expected oldest key-0 to be evicted")
	}

	expiredTime := base.Add(2 * time.Minute)
	if !rl.Allow("fresh-after-expiry", expiredTime) {
		t.Fatal("expected key after expiry to be allowed")
	}
	if len(rl.entries) != 1 {
		t.Fatalf("expected all expired entries to be evicted leaving 1 entry, got %d", len(rl.entries))
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	h := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	reqNoHeader := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recNoHeader := httptest.NewRecorder()
	h.ServeHTTP(recNoHeader, reqNoHeader)
	generated := recNoHeader.Header().Get("X-Request-ID")
	if len(generated) != 32 || !isValidRequestID(generated) {
		t.Fatalf("expected 32-char valid hex request ID, got %q", generated)
	}

	reqValid := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	reqValid.Header.Set("X-Request-ID", "trace_abc-123")
	recValid := httptest.NewRecorder()
	h.ServeHTTP(recValid, reqValid)
	if got := recValid.Header().Get("X-Request-ID"); got != "trace_abc-123" {
		t.Fatalf("expected preserved request ID trace_abc-123, got %q", got)
	}

	for _, invalid := range []string{"bad id with spaces", "bad/slash", strings.Repeat("a", 65)} {
		reqInvalid := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		reqInvalid.Header.Set("X-Request-ID", invalid)
		recInvalid := httptest.NewRecorder()
		h.ServeHTTP(recInvalid, reqInvalid)
		got := recInvalid.Header().Get("X-Request-ID")
		if got == invalid || len(got) != 32 {
			t.Fatalf("expected invalid header %q to be replaced with 32-char ID, got %q", invalid, got)
		}
	}
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	h := SecurityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	reqOpen := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recOpen := httptest.NewRecorder()
	h.ServeHTTP(recOpen, reqOpen)
	if recOpen.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("expected X-Content-Type-Options nosniff, got %q", recOpen.Header().Get("X-Content-Type-Options"))
	}
	if recOpen.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("expected X-Frame-Options DENY, got %q", recOpen.Header().Get("X-Frame-Options"))
	}
	if recOpen.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("expected Referrer-Policy no-referrer, got %q", recOpen.Header().Get("Referrer-Policy"))
	}
	if recOpen.Header().Get("Cache-Control") != "" {
		t.Fatalf("expected empty Cache-Control on non-/v1/ path, got %q", recOpen.Header().Get("Cache-Control"))
	}

	reqV1 := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	recV1 := httptest.NewRecorder()
	h.ServeHTTP(recV1, reqV1)
	if recV1.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected Cache-Control no-store on /v1/ path, got %q", recV1.Header().Get("Cache-Control"))
	}
}

func TestHTTPMetricsMiddleware(t *testing.T) {
	metrics := &HTTPMetrics{}
	h := metrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
		case "/bad":
			w.WriteHeader(http.StatusBadRequest)
		case "/budget":
			w.WriteHeader(http.StatusPaymentRequired)
		}
	}))

	for _, path := range []string{"/ok", "/bad", "/budget"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}

	if got := metrics.RequestsTotal.Load(); got != 3 {
		t.Fatalf("expected RequestsTotal=3, got %d", got)
	}
	if got := metrics.ErrorsTotal.Load(); got != 2 {
		t.Fatalf("expected ErrorsTotal=2, got %d", got)
	}
	if got := metrics.BudgetExceededTotal.Load(); got != 1 {
		t.Fatalf("expected BudgetExceededTotal=1, got %d", got)
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(2, time.Minute)
	h := RateLimitMiddleware(limiter, func() time.Time { return now })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
		req.RemoteAddr = fmt.Sprintf("192.0.2.10:%d", 1000+i)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	reqLimited := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	reqLimited.RemoteAddr = "192.0.2.10:2000"
	recLimited := httptest.NewRecorder()
	h.ServeHTTP(recLimited, reqLimited)
	if recLimited.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", recLimited.Code)
	}
	if recLimited.Header().Get("Retry-After") != "60" {
		t.Fatalf("expected Retry-After 60, got %q", recLimited.Header().Get("Retry-After"))
	}
	if ct := recLimited.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}
	var errBody map[string]string
	if err := json.Unmarshal(recLimited.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("unmarshal 429 body: %v", err)
	}
	if errBody["error"] != "rate limit exceeded" {
		t.Fatalf("expected error 'rate limit exceeded', got %q", errBody["error"])
	}

	for _, exemptPath := range []string{"/healthz", "/readyz"} {
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest(http.MethodGet, exemptPath, nil)
			req.RemoteAddr = "192.0.2.10:3000"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s request %d: expected 200, got %d", exemptPath, i+1, rec.Code)
			}
		}
	}

	passThrough := RateLimitMiddleware(nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	reqNil := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	recNil := httptest.NewRecorder()
	passThrough.ServeHTTP(recNil, reqNil)
	if recNil.Code != http.StatusOK {
		t.Fatalf("expected 200 when limiter is nil, got %d", recNil.Code)
	}
}
