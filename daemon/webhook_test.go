package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookSignPayload(t *testing.T) {
	if got := SignPayload("", []byte(`{"event":"budget_warning"}`)); got != "" {
		t.Fatalf("expected empty signature for empty secret, got %q", got)
	}

	secret := "top-secret-key"
	body := []byte(`{"event":"budget_warning","org_id":"org-alpha"}`)
	got := SignPayload(secret, body)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("expected signature %q, got %q", want, got)
	}
}

func TestWebhookNotifySuccessAndSignature(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	var gotSig string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		gotContentType = r.Header.Get("Content-Type")
		gotSig = r.Header.Get("X-SmallCloud-Signature-256")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	secret := "webhook-secret-123"
	notifier := &WebhookNotifier{
		URL:    srv.URL,
		Secret: secret,
		Client: srv.Client(),
	}

	ts := time.Date(2026, 11, 26, 8, 30, 0, 0, time.UTC)
	wantPayload := WebhookPayload{
		Event:         "budget_warning",
		OrgID:         "org-alpha",
		AppName:       "app-one",
		SpendUSD:      8.5,
		MaxMonthlyUSD: 10.0,
		AlertAtPct:    80.0,
		Halted:        false,
		Timestamp:     ts,
	}

	if err := notifier.Notify(context.Background(), wantPayload); err != nil {
		t.Fatalf("Notify failed: %v", err)
	}

	if gotContentType != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", gotContentType)
	}
	wantSig := SignPayload(secret, gotBody)
	if gotSig != wantSig {
		t.Fatalf("expected X-SmallCloud-Signature-256 %q, got %q", wantSig, gotSig)
	}

	var gotPayload WebhookPayload
	if err := json.Unmarshal(gotBody, &gotPayload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if gotPayload.Event != wantPayload.Event ||
		gotPayload.OrgID != wantPayload.OrgID ||
		gotPayload.AppName != wantPayload.AppName ||
		gotPayload.SpendUSD != wantPayload.SpendUSD ||
		gotPayload.MaxMonthlyUSD != wantPayload.MaxMonthlyUSD ||
		gotPayload.AlertAtPct != wantPayload.AlertAtPct ||
		gotPayload.Halted != wantPayload.Halted ||
		!gotPayload.Timestamp.Equal(wantPayload.Timestamp) {
		t.Fatalf("unexpected payload: got %+v, want %+v", gotPayload, wantPayload)
	}
}

func TestWebhookNotifyNon2xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	notifier := &WebhookNotifier{
		URL:    srv.URL,
		Secret: "secret",
		Client: srv.Client(),
	}

	err := notifier.Notify(context.Background(), WebhookPayload{
		Event:   "budget_hard_stop",
		OrgID:   "org-alpha",
		AppName: "app-one",
	})
	if err == nil {
		t.Fatalf("expected error on 500 status, got nil")
	}
}

func TestWebhookNotifyEmptyURLNoop(t *testing.T) {
	notifier := &WebhookNotifier{
		URL:    "",
		Secret: "secret",
	}
	if err := notifier.Notify(context.Background(), WebhookPayload{Event: "budget_warning"}); err != nil {
		t.Fatalf("expected nil error for empty URL, got %v", err)
	}

	var nilNotifier *WebhookNotifier
	if err := nilNotifier.Notify(context.Background(), WebhookPayload{Event: "budget_warning"}); err != nil {
		t.Fatalf("expected nil error for nil notifier, got %v", err)
	}
}

func TestWebhookNotifyRetrySuccessOnThirdAttempt(t *testing.T) {
	var attempts atomic.Int32
	var lastBody []byte
	var lastSig string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		lastBody = body
		lastSig = r.Header.Get("X-SmallCloud-Signature-256")
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secret := "retry-secret"
	notifier := &WebhookNotifier{
		URL:            srv.URL,
		Secret:         secret,
		Client:         srv.Client(),
		RetryBaseDelay: time.Millisecond,
	}

	err := notifier.Notify(context.Background(), WebhookPayload{
		Event:   "budget_warning",
		OrgID:   "org-alpha",
		AppName: "app-one",
	})
	if err != nil {
		t.Fatalf("expected Notify to succeed on 3rd attempt, got %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
	if wantSig := SignPayload(secret, lastBody); lastSig != wantSig {
		t.Fatalf("expected signature %q on 3rd attempt, got %q", wantSig, lastSig)
	}
}

func TestWebhookNotifyNoRetryOn4xx(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	notifier := &WebhookNotifier{
		URL:            srv.URL,
		Secret:         "secret",
		Client:         srv.Client(),
		RetryBaseDelay: time.Millisecond,
	}

	err := notifier.Notify(context.Background(), WebhookPayload{
		Event:   "budget_warning",
		OrgID:   "org-alpha",
		AppName: "app-one",
	})
	if err == nil {
		t.Fatalf("expected error on 400 status, got nil")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("expected 1 attempt without retry on 400, got %d", got)
	}
}

func TestWebhookNotifyContextCancelDuringBackoff(t *testing.T) {
	var attempts atomic.Int32
	firstAttemptDone := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			close(firstAttemptDone)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	notifier := &WebhookNotifier{
		URL:            srv.URL,
		Secret:         "secret",
		Client:         srv.Client(),
		MaxRetries:     2,
		RetryBaseDelay: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-firstAttemptDone
		cancel()
	}()

	start := time.Now()
	err := notifier.Notify(ctx, WebhookPayload{
		Event:   "budget_hard_stop",
		OrgID:   "org-alpha",
		AppName: "app-one",
	})
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed >= time.Second {
		t.Fatalf("expected backoff to abort promptly on cancel, took %v", elapsed)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("expected 1 attempt before cancel, got %d", got)
	}
}
