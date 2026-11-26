package core

import (
	"strings"
	"testing"
	"time"
)

func TestReleaseAttestationSignAndVerifyRoundtrip(t *testing.T) {
	secret := "super-secret-signing-key"
	sha := strings.Repeat("a", 64)
	signedAt := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)

	att, err := SignReleaseAttestation(secret, ReleaseAttestation{
		ReleaseID:     "rel-123",
		OrgID:         "acme",
		AppName:       "web",
		ArchiveSHA256: sha,
		SignedAt:      signedAt,
	})
	if err != nil {
		t.Fatalf("SignReleaseAttestation: %v", err)
	}
	if !strings.HasPrefix(att.Signature, "sha256=") {
		t.Fatalf("expected sha256= prefix, got %q", att.Signature)
	}
	if len(att.Signature) != len("sha256=")+64 {
		t.Fatalf("unexpected signature length %d", len(att.Signature))
	}
	if err := VerifyReleaseAttestation(secret, att); err != nil {
		t.Fatalf("VerifyReleaseAttestation: %v", err)
	}

	defaulted, err := SignReleaseAttestation(secret, ReleaseAttestation{
		ReleaseID:     "rel-124",
		OrgID:         "acme",
		AppName:       "web",
		ArchiveSHA256: sha,
	})
	if err != nil {
		t.Fatalf("SignReleaseAttestation with zero SignedAt: %v", err)
	}
	if defaulted.SignedAt.IsZero() {
		t.Fatal("expected SignedAt to default to non-zero time")
	}
	if err := VerifyReleaseAttestation(secret, defaulted); err != nil {
		t.Fatalf("VerifyReleaseAttestation defaulted: %v", err)
	}
}

func TestReleaseAttestationTamperAndWrongSecret(t *testing.T) {
	secret := "super-secret-signing-key"
	att, err := SignReleaseAttestation(secret, ReleaseAttestation{
		ReleaseID:     "rel-123",
		OrgID:         "acme",
		AppName:       "web",
		ArchiveSHA256: strings.Repeat("b", 64),
		SignedAt:      time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("SignReleaseAttestation: %v", err)
	}

	if err := VerifyReleaseAttestation("wrong-secret", att); err == nil {
		t.Fatal("expected error for wrong secret")
	}

	cases := []struct {
		name   string
		mutate func(*ReleaseAttestation)
	}{
		{
			name: "tampered release_id",
			mutate: func(a *ReleaseAttestation) {
				a.ReleaseID = "rel-999"
			},
		},
		{
			name: "tampered org_id",
			mutate: func(a *ReleaseAttestation) {
				a.OrgID = "evil"
			},
		},
		{
			name: "tampered app_name",
			mutate: func(a *ReleaseAttestation) {
				a.AppName = "other"
			},
		},
		{
			name: "tampered archive_sha256",
			mutate: func(a *ReleaseAttestation) {
				a.ArchiveSHA256 = strings.Repeat("c", 64)
			},
		},
		{
			name: "tampered signed_at",
			mutate: func(a *ReleaseAttestation) {
				a.SignedAt = a.SignedAt.Add(time.Second)
			},
		},
		{
			name: "tampered signature",
			mutate: func(a *ReleaseAttestation) {
				a.Signature = "sha256=" + strings.Repeat("0", 64)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tampered := att
			tc.mutate(&tampered)
			if err := VerifyReleaseAttestation(secret, tampered); err == nil {
				t.Fatalf("expected verification error for %s", tc.name)
			}
		})
	}
}

func TestReleaseAttestationInvalidInputs(t *testing.T) {
	validSHA := strings.Repeat("d", 64)
	validAtt := ReleaseAttestation{
		ReleaseID:     "rel-1",
		OrgID:         "acme",
		AppName:       "web",
		ArchiveSHA256: validSHA,
		SignedAt:      time.Now().UTC(),
	}

	invalidSignCases := []struct {
		name   string
		secret string
		mutate func(*ReleaseAttestation)
	}{
		{name: "empty secret", secret: "", mutate: func(*ReleaseAttestation) {}},
		{name: "whitespace secret", secret: "   ", mutate: func(*ReleaseAttestation) {}},
		{name: "empty release_id", secret: "sec", mutate: func(a *ReleaseAttestation) { a.ReleaseID = "" }},
		{name: "empty org_id", secret: "sec", mutate: func(a *ReleaseAttestation) { a.OrgID = "" }},
		{name: "empty app_name", secret: "sec", mutate: func(a *ReleaseAttestation) { a.AppName = "" }},
		{name: "short sha256", secret: "sec", mutate: func(a *ReleaseAttestation) { a.ArchiveSHA256 = "abcd" }},
		{name: "non-hex sha256", secret: "sec", mutate: func(a *ReleaseAttestation) { a.ArchiveSHA256 = strings.Repeat("z", 64) }},
	}

	for _, tc := range invalidSignCases {
		t.Run("sign_"+tc.name, func(t *testing.T) {
			att := validAtt
			tc.mutate(&att)
			if _, err := SignReleaseAttestation(tc.secret, att); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}

	signed, err := SignReleaseAttestation("sec", validAtt)
	if err != nil {
		t.Fatalf("SignReleaseAttestation: %v", err)
	}

	invalidVerifyCases := []struct {
		name   string
		secret string
		mutate func(*ReleaseAttestation)
	}{
		{name: "empty secret", secret: "", mutate: func(*ReleaseAttestation) {}},
		{name: "zero signed_at", secret: "sec", mutate: func(a *ReleaseAttestation) { a.SignedAt = time.Time{} }},
		{name: "missing prefix", secret: "sec", mutate: func(a *ReleaseAttestation) { a.Signature = strings.Repeat("a", 64) }},
		{name: "invalid hex sig", secret: "sec", mutate: func(a *ReleaseAttestation) { a.Signature = "sha256=" + strings.Repeat("z", 64) }},
	}

	for _, tc := range invalidVerifyCases {
		t.Run("verify_"+tc.name, func(t *testing.T) {
			att := signed
			tc.mutate(&att)
			if err := VerifyReleaseAttestation(tc.secret, att); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}
