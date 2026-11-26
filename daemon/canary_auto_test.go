package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCanaryAutoNilAndMissingRoute(t *testing.T) {
	if _, err := EvaluateAndReconcileCanary(context.Background(), nil, "acme", "api", "/healthz", 10*time.Millisecond, 50*time.Millisecond); err == nil {
		t.Fatal("expected error for nil daemon")
	}

	d := NewDaemon(t.TempDir(), t.TempDir())
	if _, err := EvaluateAndReconcileCanary(context.Background(), d, "acme", "missing", "/healthz", 10*time.Millisecond, 50*time.Millisecond); !errors.Is(err, ErrNoRouteFound) {
		t.Fatalf("expected ErrNoRouteFound, got %v", err)
	}
}

func TestCanaryAutoNoopWhenWeightZero(t *testing.T) {
	d := NewDaemon(t.TempDir(), t.TempDir())
	if _, err := d.Router().SwapRoute("acme", "api", "api.acme.example", "v1", "127.0.0.1:9001"); err != nil {
		t.Fatalf("SwapRoute v1 failed: %v", err)
	}
	if _, err := d.Router().SwapRoute("acme", "api", "api.acme.example", "v2", "127.0.0.1:9002"); err != nil {
		t.Fatalf("SwapRoute v2 failed: %v", err)
	}

	dec, err := EvaluateAndReconcileCanary(context.Background(), d, "acme", "api", "/healthz", 10*time.Millisecond, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("EvaluateAndReconcileCanary failed: %v", err)
	}
	if dec.Action != "noop" {
		t.Fatalf("expected action noop, got %q", dec.Action)
	}
}

func TestCanaryAutoHealthyPromotion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	standbyAddr := strings.TrimPrefix(srv.URL, "http://")

	d := NewDaemon(t.TempDir(), t.TempDir())
	if _, err := d.Router().SwapRoute("acme", "api", "api.acme.example", "v1", standbyAddr); err != nil {
		t.Fatalf("SwapRoute v1 failed: %v", err)
	}
	if _, err := d.Router().SwapRoute("acme", "api", "api.acme.example", "v2", "127.0.0.1:9002"); err != nil {
		t.Fatalf("SwapRoute v2 failed: %v", err)
	}
	if _, err := d.Router().SetCanaryWeight("acme", "api", 25); err != nil {
		t.Fatalf("SetCanaryWeight failed: %v", err)
	}

	dec, err := EvaluateAndReconcileCanary(context.Background(), d, "acme", "api", "/healthz", 10*time.Millisecond, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("EvaluateAndReconcileCanary failed: %v", err)
	}
	if dec.Action != "promoted" || !dec.Healthy {
		t.Fatalf("expected healthy promotion, got %+v", dec)
	}

	route, ok := d.Router().GetRoute("acme", "api")
	if !ok {
		t.Fatal("expected route to exist")
	}
	if route.ActiveReleaseID != "v1" {
		t.Fatalf("expected ActiveReleaseID v1 after promotion, got %q", route.ActiveReleaseID)
	}
	if route.CanaryWeight != 0 {
		t.Fatalf("expected CanaryWeight 0 after promotion, got %d", route.CanaryWeight)
	}

	events := d.Audit().List("acme", "api", 0)
	if len(events) == 0 || events[0].Type != "canary_promoted" {
		t.Fatalf("expected canary_promoted audit event, got %+v", events)
	}
}

func TestCanaryAutoUnhealthyRollback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	standbyAddr := strings.TrimPrefix(srv.URL, "http://")

	d := NewDaemon(t.TempDir(), t.TempDir())
	if _, err := d.Router().SwapRoute("acme", "api", "api.acme.example", "v1", standbyAddr); err != nil {
		t.Fatalf("SwapRoute v1 failed: %v", err)
	}
	if _, err := d.Router().SwapRoute("acme", "api", "api.acme.example", "v2", "127.0.0.1:9002"); err != nil {
		t.Fatalf("SwapRoute v2 failed: %v", err)
	}
	if _, err := d.Router().SetCanaryWeight("acme", "api", 25); err != nil {
		t.Fatalf("SetCanaryWeight failed: %v", err)
	}

	dec, err := EvaluateAndReconcileCanary(context.Background(), d, "acme", "api", "/healthz", 10*time.Millisecond, 60*time.Millisecond)
	if err != nil {
		t.Fatalf("EvaluateAndReconcileCanary failed: %v", err)
	}
	if dec.Action != "rolled_back" || dec.Healthy || dec.Error == "" {
		t.Fatalf("expected unhealthy rolled_back decision with error, got %+v", dec)
	}

	route, ok := d.Router().GetRoute("acme", "api")
	if !ok {
		t.Fatal("expected route to exist")
	}
	if route.ActiveReleaseID != "v2" {
		t.Fatalf("expected ActiveReleaseID to remain v2, got %q", route.ActiveReleaseID)
	}
	if route.CanaryWeight != 0 {
		t.Fatalf("expected CanaryWeight reset to 0, got %d", route.CanaryWeight)
	}

	events := d.Audit().List("acme", "api", 0)
	if len(events) == 0 || events[0].Type != "canary_rollback" {
		t.Fatalf("expected canary_rollback audit event, got %+v", events)
	}
}
