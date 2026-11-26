package daemon

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func signGitHubTestBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyGitHubSignature(t *testing.T) {
	secret := "top-secret-key"
	body := []byte(`{"ref":"refs/heads/main","after":"abc123"}`)
	validHeader := signGitHubTestBody(secret, body)

	if err := VerifyGitHubSignature(secret, body, validHeader); err != nil {
		t.Fatalf("expected valid signature to succeed, got %v", err)
	}

	if err := VerifyGitHubSignature("wrong-secret", body, validHeader); err == nil {
		t.Fatal("expected error for wrong secret")
	}

	if err := VerifyGitHubSignature("", body, validHeader); err == nil {
		t.Fatal("expected error for empty secret")
	}

	if err := VerifyGitHubSignature(secret, []byte(`{"ref":"refs/heads/dev"}`), validHeader); err == nil {
		t.Fatal("expected error for tampered body")
	}

	malformedHeaders := []string{
		"",
		"sha256=",
		"sha1=deadbeef",
		"deadbeef",
		"sha256=not-hex!",
		"sha256=abcd",
	}
	for _, h := range malformedHeaders {
		if err := VerifyGitHubSignature(secret, body, h); err == nil {
			t.Fatalf("expected error for malformed header %q", h)
		}
	}
}

func TestParseGitHubPushEvent(t *testing.T) {
	validBody := []byte(`{
		"ref": "refs/heads/feat/gitops",
		"after": "e1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0",
		"repository": {"full_name": "acme/smallcloud-app"},
		"pusher": {"name": "octocat"}
	}`)

	ev, err := ParseGitHubPushEvent(validBody)
	if err != nil {
		t.Fatalf("expected valid push event to parse, got %v", err)
	}
	if ev.Repository != "acme/smallcloud-app" {
		t.Fatalf("unexpected repository: %q", ev.Repository)
	}
	if ev.Ref != "refs/heads/feat/gitops" {
		t.Fatalf("unexpected ref: %q", ev.Ref)
	}
	if ev.Branch != "feat/gitops" {
		t.Fatalf("unexpected branch: %q", ev.Branch)
	}
	if ev.CommitSHA != "e1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0" {
		t.Fatalf("unexpected commit sha: %q", ev.CommitSHA)
	}
	if ev.Pusher != "octocat" {
		t.Fatalf("unexpected pusher: %q", ev.Pusher)
	}

	tagBody := []byte(`{
		"ref": "refs/tags/v1.2.3",
		"after": "1111111111111111111111111111111111111111",
		"repository": {"full_name": "acme/smallcloud-app"},
		"pusher": {"name": "octocat"}
	}`)
	tagEv, err := ParseGitHubPushEvent(tagBody)
	if err != nil {
		t.Fatalf("expected tag push event to parse, got %v", err)
	}
	if tagEv.Branch != "" {
		t.Fatalf("expected empty branch for tag ref, got %q", tagEv.Branch)
	}

	invalidPayloads := []struct {
		name string
		body string
	}{
		{
			name: "invalid_json",
			body: `{not-json`,
		},
		{
			name: "zero_sha_delete",
			body: `{"ref":"refs/heads/main","after":"0000000000000000000000000000000000000000","repository":{"full_name":"acme/app"},"pusher":{"name":"octocat"}}`,
		},
		{
			name: "empty_after",
			body: `{"ref":"refs/heads/main","after":"","repository":{"full_name":"acme/app"},"pusher":{"name":"octocat"}}`,
		},
		{
			name: "empty_repository",
			body: `{"ref":"refs/heads/main","after":"1111111111111111111111111111111111111111","repository":{"full_name":""},"pusher":{"name":"octocat"}}`,
		},
	}

	for _, tc := range invalidPayloads {
		if _, err := ParseGitHubPushEvent([]byte(tc.body)); err == nil {
			t.Fatalf("expected error for %s", tc.name)
		}
	}
}
