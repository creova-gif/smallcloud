package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	store := newItemStore(t.TempDir())
	handler := newHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("expected status ok, got %q", resp["status"])
	}
}

func TestPostAndGetItems(t *testing.T) {
	store := newItemStore(t.TempDir())
	handler := newHandler(store)

	postReq := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(`{"name":"alpha"}`))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	handler.ServeHTTP(postRec, postReq)

	if postRec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", postRec.Code, postRec.Body.String())
	}

	var created Item
	if err := json.Unmarshal(postRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("failed to decode created item: %v", err)
	}
	if created.Name != "alpha" {
		t.Fatalf("expected name alpha, got %q", created.Name)
	}
	if created.OrgID != defaultOrgID {
		t.Fatalf("expected org_id %q, got %q", defaultOrgID, created.OrgID)
	}
	if created.ID == "" || created.CreatedAt == "" {
		t.Fatalf("expected non-empty id and created_at, got %+v", created)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/items", nil)
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", getRec.Code)
	}

	var listResp struct {
		Items []Item `json:"items"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("failed to decode list response: %v", err)
	}
	if len(listResp.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(listResp.Items))
	}
	if listResp.Items[0].Name != "alpha" {
		t.Fatalf("expected item alpha, got %q", listResp.Items[0].Name)
	}
}

func TestPostItemsValidation(t *testing.T) {
	store := newItemStore(t.TempDir())
	handler := newHandler(store)

	cases := []struct {
		name string
		body []byte
		want int
	}{
		{
			name: "invalid json",
			body: []byte(`{"name":`),
			want: http.StatusBadRequest,
		},
		{
			name: "empty name",
			body: []byte(`{"name":"   "}`),
			want: http.StatusBadRequest,
		},
		{
			name: "oversized body",
			body: bytes.Repeat([]byte("a"), maxBodyBytes+1),
			want: http.StatusRequestEntityTooLarge,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/items", bytes.NewReader(tc.body))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("expected status %d, got %d: %s", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}
