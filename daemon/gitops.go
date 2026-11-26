package daemon

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const zeroCommitSHA = "0000000000000000000000000000000000000000"

type GitPushEvent struct {
	Repository string `json:"repository"`
	Ref        string `json:"ref"`
	Branch     string `json:"branch"`
	CommitSHA  string `json:"commit_sha"`
	Pusher     string `json:"pusher"`
}

type githubPushPayload struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
}

func VerifyGitHubSignature(secret string, body []byte, signatureHeader string) error {
	if secret == "" {
		return errors.New("github webhook secret is empty")
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return errors.New("malformed github signature header")
	}
	hexSig := strings.TrimPrefix(signatureHeader, prefix)
	if hexSig == "" {
		return errors.New("malformed github signature header")
	}
	sigBytes, err := hex.DecodeString(hexSig)
	if err != nil {
		return errors.New("malformed github signature hex")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)
	if !hmac.Equal(sigBytes, expected) {
		return errors.New("invalid github webhook signature")
	}
	return nil
}

func ParseGitHubPushEvent(body []byte) (GitPushEvent, error) {
	var payload githubPushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return GitPushEvent{}, err
	}
	if strings.TrimSpace(payload.Repository.FullName) == "" {
		return GitPushEvent{}, errors.New("missing repository full_name")
	}
	if strings.TrimSpace(payload.After) == "" {
		return GitPushEvent{}, errors.New("missing after commit sha")
	}
	if payload.After == zeroCommitSHA {
		return GitPushEvent{}, errors.New("deleted or zero-sha commit not deployable")
	}
	branch := ""
	const headsPrefix = "refs/heads/"
	if strings.HasPrefix(payload.Ref, headsPrefix) {
		branch = strings.TrimPrefix(payload.Ref, headsPrefix)
	}
	return GitPushEvent{
		Repository: payload.Repository.FullName,
		Ref:        payload.Ref,
		Branch:     branch,
		CommitSHA:  payload.After,
		Pusher:     payload.Pusher.Name,
	}, nil
}
