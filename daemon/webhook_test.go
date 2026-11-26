package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
