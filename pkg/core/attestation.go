package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ReleaseAttestation struct {
	ReleaseID     string    `json:"release_id"`
	OrgID         string    `json:"org_id"`
	AppName       string    `json:"app_name"`
	ArchiveSHA256 string    `json:"archive_sha256"`
	SignedAt      time.Time `json:"signed_at"`
	Signature     string    `json:"signature"`
}

func validateAttestationInputs(secret string, att ReleaseAttestation) error {
	if strings.TrimSpace(secret) == "" {
		return errors.New("secret is required")
	}
	if strings.TrimSpace(att.OrgID) == "" {
		return errors.New("org_id is required")
	}
	if strings.TrimSpace(att.AppName) == "" {
		return errors.New("app_name is required")
	}
	if strings.TrimSpace(att.ReleaseID) == "" {
		return errors.New("release_id is required")
	}
	if len(att.ArchiveSHA256) != 64 {
		return errors.New("archive_sha256 must be a 64-character hex string")
	}
	if _, err := hex.DecodeString(att.ArchiveSHA256); err != nil {
		return fmt.Errorf("archive_sha256 must be valid hex: %w", err)
	}
	return nil
}

func computeAttestationMAC(secret string, att ReleaseAttestation) []byte {
	payload := fmt.Sprintf("%s|%s|%s|%s|%d", att.OrgID, att.AppName, att.ReleaseID, att.ArchiveSHA256, att.SignedAt.UTC().Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func SignReleaseAttestation(secret string, att ReleaseAttestation) (ReleaseAttestation, error) {
	if err := validateAttestationInputs(secret, att); err != nil {
		return ReleaseAttestation{}, err
	}
	if att.SignedAt.IsZero() {
		att.SignedAt = time.Now().UTC()
	}
	att.Signature = "sha256=" + hex.EncodeToString(computeAttestationMAC(secret, att))
	return att, nil
}

func VerifyReleaseAttestation(secret string, att ReleaseAttestation) error {
	if err := validateAttestationInputs(secret, att); err != nil {
		return err
	}
	if att.SignedAt.IsZero() {
		return errors.New("signed_at is required")
	}
	if !strings.HasPrefix(att.Signature, "sha256=") {
		return errors.New("signature must have sha256= prefix")
	}
	rawSig, err := hex.DecodeString(strings.TrimPrefix(att.Signature, "sha256="))
	if err != nil {
		return fmt.Errorf("signature must be valid hex: %w", err)
	}
	expected := computeAttestationMAC(secret, att)
	if !hmac.Equal(rawSig, expected) {
		return errors.New("release attestation signature mismatch")
	}
	return nil
}
