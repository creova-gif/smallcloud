package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	maxBodyBytes = 64 * 1024
	defaultOrgID = "org-indie-default"
)

type Item struct {
	ID        string `json:"id"`
	OrgID     string `json:"org_id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

type createItemRequest struct {
	OrgID string `json:"org_id"`
	Name  string `json:"name"`
}

type itemStore struct {
	mu        sync.Mutex
	dataDir   string
	storePath string
}

func newItemStore(dataDir string) *itemStore {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = "/data"
	}
	return &itemStore{
		dataDir:   dataDir,
		storePath: filepath.Join(dataDir, "items.ndjson"),
	}
}

func (s *itemStore) List() ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.storePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Item{}, nil
		}
		return nil, err
	}
	defer f.Close()

	items := make([]Item, 0)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var item Item
		if err := json.Unmarshal(line, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *itemStore) Append(item Item) error {
	line, err := json.Marshal(item)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.dataDir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.storePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write(body)
}

func newHandler(store *itemStore) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /items", func(w http.ResponseWriter, r *http.Request) {
		items, err := store.List()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to read items"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	})

	mux.HandleFunc("POST /items", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read request body"})
			return
		}

		var req createItemRequest
		if len(bytes.TrimSpace(raw)) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be a non-empty string"})
			return
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}

		name := strings.TrimSpace(req.Name)
		if name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be a non-empty string"})
			return
		}

		orgID := strings.TrimSpace(req.OrgID)
		if orgID == "" {
			orgID = defaultOrgID
		}

		item := Item{
			ID:        newID(),
			OrgID:     orgID,
			Name:      name,
			CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		if err := store.Append(item); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to store item"})
			return
		}
		writeJSON(w, http.StatusCreated, item)
	})

	return mux
}

func main() {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}
	dataDir := os.Getenv("SMALLCLOUD_DATA_DIR")
	store := newItemStore(dataDir)

	srv := &http.Server{
		Addr:              "0.0.0.0:" + port,
		Handler:           newHandler(store),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		os.Exit(1)
	}
}
