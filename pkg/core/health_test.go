package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHealthcheck(t *testing.T) {
	if Healthcheck() != "ok" {
		t.Fatal("unexpected")
	}
}

func TestProbeHTTPReadyImmediateOK(t *testing.T) {
	var hits atomic.Int32
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := ProbeHTTPReady(context.Background(), srv.URL+"/", "healthz", 10*time.Millisecond, time.Second); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 request, got %d", hits.Load())
	}
	if gotPath != "/healthz" {
		t.Fatalf("expected path /healthz, got %q", gotPath)
	}
}

func TestProbeHTTPReadyEventualOK(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := ProbeHTTPReady(context.Background(), srv.URL, "", 10*time.Millisecond, time.Second); err != nil {
		t.Fatalf("expected eventual readiness, got %v", err)
	}
	if attempts.Load() < 3 {
		t.Fatalf("expected at least 3 attempts, got %d", attempts.Load())
	}
}

func TestProbeHTTPReadyTimeoutOnPersistent503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	err := ProbeHTTPReady(context.Background(), srv.URL, "/ready", 10*time.Millisecond, 60*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
}

func TestProbeHTTPReadyContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(25 * time.Millisecond)
		cancel()
	}()

	err := ProbeHTTPReady(ctx, srv.URL, "/healthz", 10*time.Millisecond, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
