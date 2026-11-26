package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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

func TestMethodNotAllowed(t *testing.T) {
	store := newItemStore(t.TempDir())
	handler := newHandler(store)

	cases := []struct {
		method string
		path   string
	}{
		{method: http.MethodPut, path: "/items"},
		{method: http.MethodPost, path: "/healthz"},
	}

	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected status 405, got %d", rec.Code)
			}
		})
	}
}

func TestEmptyAndMissingStore(t *testing.T) {
	dir := t.TempDir()
	store := newItemStore(dir)
	handler := newHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200 on missing file, got %d", rec.Code)
	}

	var listResp struct {
		Items []Item `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("failed to decode list response: %v", err)
	}
	if len(listResp.Items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(listResp.Items))
	}

	ndjson := "\n  \n" +
		`{"id":"1","org_id":"org-1","name":"first","created_at":"2026-01-01T00:00:00Z"}` + "\n" +
		"\n\t\n"
	if err := os.WriteFile(filepath.Join(dir, "items.ndjson"), []byte(ndjson), 0o644); err != nil {
		t.Fatalf("failed to write items.ndjson: %v", err)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/items", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec2.Code)
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("failed to decode list response: %v", err)
	}
	if len(listResp.Items) != 1 || listResp.Items[0].Name != "first" {
		t.Fatalf("unexpected items: %+v", listResp.Items)
	}
}

func TestCorruptNDJSON(t *testing.T) {
	dir := t.TempDir()
	store := newItemStore(dir)
	handler := newHandler(store)

	corrupt := `{"id":"1","org_id":"org-1","name":"ok","created_at":"2026-01-01T00:00:00Z"}` + "\n{not-json\n"
	if err := os.WriteFile(filepath.Join(dir, "items.ndjson"), []byte(corrupt), 0o644); err != nil {
		t.Fatalf("failed to write corrupt items.ndjson: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/items", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500 for corrupt ndjson, got %d", rec.Code)
	}
}

func TestStoreDefaultAndEnvDir(t *testing.T) {
	for _, blank := range []string{"", "   "} {
		store := newItemStore(blank)
		if store.dataDir != "/data" {
			t.Fatalf("expected default dataDir /data, got %q", store.dataDir)
		}
		if store.storePath != filepath.Join("/data", "items.ndjson") {
			t.Fatalf("expected storePath /data/items.ndjson, got %q", store.storePath)
		}
	}

	customDir := t.TempDir()
	t.Setenv("SMALLCLOUD_DATA_DIR", customDir)
	store := newItemStore(os.Getenv("SMALLCLOUD_DATA_DIR"))
	if store.dataDir != customDir {
		t.Fatalf("expected dataDir %q, got %q", customDir, store.dataDir)
	}
}

func TestPostItemsExplicitOrgEmptyBodyAndWriteError(t *testing.T) {
	dir := t.TempDir()
	store := newItemStore(dir)
	handler := newHandler(store)

	emptyReq := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader("   "))
	emptyRec := httptest.NewRecorder()
	handler.ServeHTTP(emptyRec, emptyReq)
	if emptyRec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400 for empty body, got %d", emptyRec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(`{"name":"beta","org_id":"org-custom"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var created Item
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("failed to decode created item: %v", err)
	}
	if created.OrgID != "org-custom" {
		t.Fatalf("expected org_id org-custom, got %q", created.OrgID)
	}

	fileAsDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileAsDir, []byte("x"), 0o644); err != nil {
		t.Fatalf("failed to create blocking file: %v", err)
	}
	badHandler := newHandler(newItemStore(fileAsDir))
	badReq := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(`{"name":"gamma"}`))
	badRec := httptest.NewRecorder()
	badHandler.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500 on append failure, got %d", badRec.Code)
	}
}

func TestConcurrentPostItems(t *testing.T) {
	store := newItemStore(t.TempDir())
	handler := newHandler(store)

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)

	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			payload := fmt.Sprintf(`{"name":"item-%02d"}`, idx)
			req := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				errs <- fmt.Errorf("worker %d: expected 201, got %d: %s", idx, rec.Code, rec.Body.String())
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
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
	if len(listResp.Items) != workers {
		t.Fatalf("expected %d items, got %d", workers, len(listResp.Items))
	}

	seen := make(map[string]bool, workers)
	for _, item := range listResp.Items {
		if seen[item.Name] {
			t.Fatalf("duplicate item name %q", item.Name)
		}
		seen[item.Name] = true
	}
}

func TestMainEnvAndGracefulShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve free port: %v", err)
	}
	port := fmt.Sprintf("%d", ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()

	dataDir := t.TempDir()
	t.Setenv("PORT", port)
	t.Setenv("SMALLCLOUD_DATA_DIR", dataDir)

	done := make(chan struct{})
	go func() {
		defer close(done)
		main()
	}()

	client := &http.Client{Timeout: 500 * time.Millisecond}
	url := fmt.Sprintf("http://127.0.0.1:%s/healthz", port)
	deadline := time.Now().Add(3 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		<-done
		t.Fatalf("server on port %s did not become ready in time", port)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("failed to send SIGTERM: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("main did not shut down within timeout")
	}
}
