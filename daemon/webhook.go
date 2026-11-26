package daemon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type WebhookPayload struct {
	Event         string    `json:"event"`
	OrgID         string    `json:"org_id"`
	AppName       string    `json:"app_name"`
	SpendUSD      float64   `json:"spend_usd"`
	MaxMonthlyUSD float64   `json:"max_monthly_usd"`
	AlertAtPct    float64   `json:"alert_at_pct"`
	Halted        bool      `json:"halted"`
	Timestamp     time.Time `json:"timestamp"`
}

type WebhookNotifier struct {
	URL    string
	Secret string
	Client *http.Client
}

func SignPayload(secret string, body []byte) string {
	if secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (n *WebhookNotifier) Notify(ctx context.Context, payload WebhookPayload) error {
	if n == nil || n.URL == "" {
		return nil
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if sig := SignPayload(n.Secret, body); sig != "" {
		req.Header.Set("X-SmallCloud-Signature-256", sig)
	}

	client := n.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}
