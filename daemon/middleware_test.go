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

	expectedCSP := "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; object-src 'none'; frame-ancestors 'none'"

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
	if recOpen.Header().Get("Content-Security-Policy") != expectedCSP {
		t.Fatalf("expected Content-Security-Policy %q, got %q", expectedCSP, recOpen.Header().Get("Content-Security-Policy"))
	}
	if recOpen.Header().Get("Cache-Control") != "" {
		t.Fatalf("expected empty Cache-Control on non-/v1/ path, got %q", recOpen.Header().Get("Cache-Control"))
	}

	reqV1 := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	recV1 := httptest.NewRecorder()
	h.ServeHTTP(recV1, reqV1)
	if recV1.Header().Get("Content-Security-Policy") != expectedCSP {
		t.Fatalf("expected Content-Security-Policy %q on /v1/ path, got %q", expectedCSP, recV1.Header().Get("Content-Security-Policy"))
	}
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

func TestClientIPAndTrustedProxyRateLimiting(t *testing.T) {
	trusted := ParseTrustedProxies("10.0.0.0/8, 192.168.1.5, invalid-entry")
	if len(trusted) != 2 {
		t.Fatalf("expected 2 parsed trusted networks, got %d", len(trusted))
	}

	untrustedReq := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	untrustedReq.RemoteAddr = "198.51.100.77:5000"
	untrustedReq.Header.Set("X-Forwarded-For", "203.0.113.10")
	untrustedReq.Header.Set("X-Real-IP", "203.0.113.11")
	if got := ClientIP(untrustedReq, trusted); got != "198.51.100.77" {
		t.Fatalf("expected untrusted RemoteAddr to ignore forwarded headers, got %q", got)
	}

	trustedXFFReq := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	trustedXFFReq.RemoteAddr = "10.1.2.3:8080"
	trustedXFFReq.Header.Set("X-Forwarded-For", "203.0.113.20, 10.0.0.5")
	if got := ClientIP(trustedXFFReq, trusted); got != "203.0.113.20" {
		t.Fatalf("expected trusted proxy to extract 203.0.113.20 from X-Forwarded-For, got %q", got)
	}

	trustedRealIPReq := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	trustedRealIPReq.RemoteAddr = "192.168.1.5:9000"
	trustedRealIPReq.Header.Set("X-Real-IP", "203.0.113.30")
	if got := ClientIP(trustedRealIPReq, trusted); got != "203.0.113.30" {
		t.Fatalf("expected single trusted IP to honor X-Real-IP 203.0.113.30, got %q", got)
	}

	loopbackReq := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	loopbackReq.RemoteAddr = "127.0.0.1:4321"
	loopbackReq.Header.Set("X-Forwarded-For", "203.0.113.40")
	if got := ClientIP(loopbackReq, nil); got != "203.0.113.40" {
		t.Fatalf("expected loopback RemoteAddr to honor X-Forwarded-For, got %q", got)
	}

	invalidXFFReq := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	invalidXFFReq.RemoteAddr = "10.0.0.9:4321"
	invalidXFFReq.Header.Set("X-Forwarded-For", "not-an-ip")
	if got := ClientIP(invalidXFFReq, trusted); got != "10.0.0.9" {
		t.Fatalf("expected invalid X-Forwarded-For to fall back to proxy IP 10.0.0.9, got %q", got)
	}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	limiter := NewRateLimiter(1, time.Minute)
	h := RateLimitMiddlewareWithProxies(limiter, trusted, func() time.Time { return now })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	spoof1 := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	spoof1.RemoteAddr = "198.51.100.88:1001"
	spoof1.Header.Set("X-Forwarded-For", "203.0.113.1")
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, spoof1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected first untrusted request to pass, got %d", rec1.Code)
	}

	spoof2 := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	spoof2.RemoteAddr = "198.51.100.88:1002"
	spoof2.Header.Set("X-Forwarded-For", "203.0.113.2")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, spoof2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected second untrusted request with spoofed X-Forwarded-For to be rate-limited, got %d", rec2.Code)
	}

	proxy1 := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	proxy1.RemoteAddr = "10.2.3.4:2001"
	proxy1.Header.Set("X-Forwarded-For", "203.0.113.50")
	recP1 := httptest.NewRecorder()
	h.ServeHTTP(recP1, proxy1)
	if recP1.Code != http.StatusOK {
		t.Fatalf("expected first proxied client to pass, got %d", recP1.Code)
	}

	proxy2 := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	proxy2.RemoteAddr = "10.2.3.4:2002"
	proxy2.Header.Set("X-Forwarded-For", "203.0.113.51")
	recP2 := httptest.NewRecorder()
	h.ServeHTTP(recP2, proxy2)
	if recP2.Code != http.StatusOK {
		t.Fatalf("expected second distinct proxied client to pass, got %d", recP2.Code)
	}

	proxy1Again := httptest.NewRequest(http.MethodGet, "/v1/apps", nil)
	proxy1Again.RemoteAddr = "10.2.3.4:2003"
	proxy1Again.Header.Set("X-Real-IP", "203.0.113.50")
	recP1Again := httptest.NewRecorder()
	h.ServeHTTP(recP1Again, proxy1Again)
	if recP1Again.Code != http.StatusTooManyRequests {
		t.Fatalf("expected repeat proxied client 203.0.113.50 to be rate-limited, got %d", recP1Again.Code)
	}
}

func TestRejectFormContentTypeMiddleware(t *testing.T) {
	h := RejectFormContentTypeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name        string
		method      string
		path        string
		contentType string
		wantStatus  int
	}{
		{
			name:        "text plain on deploy rejected",
			method:      http.MethodPost,
			path:        "/v1/deploy",
			contentType: "text/plain",
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "text plain with charset case insensitive rejected",
			method:      http.MethodPost,
			path:        "/v1/deploy",
			contentType: "Text/Plain; charset=utf-8",
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "form urlencoded on deploy rejected",
			method:      http.MethodPost,
			path:        "/v1/deploy",
			contentType: "application/x-www-form-urlencoded",
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "multipart form data on deploy rejected",
			method:      http.MethodPost,
			path:        "/v1/deploy",
			contentType: "multipart/form-data; boundary=----boundary",
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "put with form urlencoded rejected",
			method:      http.MethodPut,
			path:        "/v1/deploy",
			contentType: "application/x-www-form-urlencoded",
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "patch with text plain rejected",
			method:      http.MethodPatch,
			path:        "/v1/deploy",
			contentType: "text/plain",
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "application json on deploy allowed",
			method:      http.MethodPost,
			path:        "/v1/deploy",
			contentType: "application/json",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "empty content type on deploy allowed",
			method:      http.MethodPost,
			path:        "/v1/deploy",
			contentType: "",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "restore path exempt",
			method:      http.MethodPost,
			path:        "/v1/restore",
			contentType: "multipart/form-data; boundary=----boundary",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "get request exempt",
			method:      http.MethodGet,
			path:        "/v1/apps",
			contentType: "text/plain",
			wantStatus:  http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d: %s", tc.wantStatus, rec.Code, rec.Body.String())
			}
			if tc.wantStatus == http.StatusUnsupportedMediaType {
				if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
					t.Fatalf("expected Content-Type application/json, got %q", ct)
				}
				var errBody map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
					t.Fatalf("unmarshal 415 body: %v", err)
				}
				if errBody["error"] != "unsupported content-type; expected application/json" {
					t.Fatalf("expected error 'unsupported content-type; expected application/json', got %q", errBody["error"])
				}
			}
		})
	}
}
