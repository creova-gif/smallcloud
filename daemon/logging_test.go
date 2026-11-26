package daemon

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStructuredAccessLogMiddleware(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	handler := StructuredAccessLogMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "resp-req-123")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/deploy?secret_token=query-secret", nil)
	req.RemoteAddr = "192.0.2.10:4321"
	req.Header.Set("Authorization", "Bearer super-secret-bearer-token")
	req.Header.Set("Cookie", "session=super-secret-cookie")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", rr.Code)
	}

	rawLog := buf.String()
	if strings.Contains(rawLog, "super-secret-bearer-token") {
		t.Fatalf("authorization token leaked in access log: %s", rawLog)
	}
	if strings.Contains(rawLog, "super-secret-cookie") {
		t.Fatalf("cookie leaked in access log: %s", rawLog)
	}
	if strings.Contains(rawLog, "query-secret") {
		t.Fatalf("raw query string leaked in access log: %s", rawLog)
	}

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to unmarshal log JSON: %v (raw: %s)", err, rawLog)
	}

	if entry["method"] != http.MethodPost {
		t.Errorf("expected method POST, got %v", entry["method"])
	}
	if entry["path"] != "/v1/deploy" {
		t.Errorf("expected path /v1/deploy, got %v", entry["path"])
	}
	if entry["status"] != float64(http.StatusCreated) {
		t.Errorf("expected status 201, got %v", entry["status"])
	}
	if entry["bytes"] != float64(len(`{"ok":true}`)) {
		t.Errorf("expected bytes %d, got %v", len(`{"ok":true}`), entry["bytes"])
	}
	if _, ok := entry["duration_ms"].(float64); !ok {
		t.Errorf("expected numeric duration_ms, got %T (%v)", entry["duration_ms"], entry["duration_ms"])
	}
	if entry["request_id"] != "resp-req-123" {
		t.Errorf("expected request_id resp-req-123, got %v", entry["request_id"])
	}
	if entry["remote_addr"] != "192.0.2.10:4321" {
		t.Errorf("expected remote_addr 192.0.2.10:4321, got %v", entry["remote_addr"])
	}
}

func TestStructuredAccessLogMiddlewareDefaultsAndRequestIDFallback(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	handler := StructuredAccessLogMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", "client-req-456")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to unmarshal log JSON: %v", err)
	}

	if entry["status"] != float64(http.StatusOK) {
		t.Errorf("expected default status 200, got %v", entry["status"])
	}
	if entry["bytes"] != float64(5) {
		t.Errorf("expected bytes 5, got %v", entry["bytes"])
	}
	if entry["request_id"] != "client-req-456" {
		t.Errorf("expected fallback request_id client-req-456, got %v", entry["request_id"])
	}
}

func TestStructuredAccessLogMiddlewareNilLogger(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	handler := StructuredAccessLogMiddleware(nil)(inner)
	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Fatal("expected inner handler to be called when logger is nil")
	}
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d", rr.Code)
	}
}
