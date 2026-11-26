package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrChecksumMismatch = errors.New("replica sha256 checksum mismatch")

type SnapshotMetadata struct {
	OrgID      string    `json:"org_id"`
	AppName    string    `json:"app_name"`
	Generation int       `json:"generation"`
	SizeBytes  int64     `json:"size_bytes"`
	SHA256     string    `json:"sha256"`
	SyncedAt   time.Time `json:"synced_at"`
}

type StorageManager struct {
	mu          sync.Mutex
	replicaDir  string
	generations map[string]int
}

func NewStorageManager(replicaDir string) *StorageManager {
	return &StorageManager{
		replicaDir:  replicaDir,
		generations: make(map[string]int),
	}
}

func (s *StorageManager) ReplicaDir() string {
	return s.replicaDir
}

func (s *StorageManager) replicaPath(orgID, appName string) string {
	return filepath.Join(s.replicaDir, orgID, appName, "replica.db")
}

func (s *StorageManager) HasReplica(orgID, appName string) bool {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return false
	}
	_, err := os.Stat(s.replicaPath(orgID, appName))
	return err == nil
}

func stageFileFromReader(dir string, r io.Reader, perm os.FileMode) (string, int64, string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", 0, "", fmt.Errorf("mkdir %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", 0, "", fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = os.Remove(tmpName)
	}

	hasher := sha256.New()
	n, err := io.Copy(tmp, io.TeeReader(r, hasher))
	if err != nil {
		_ = tmp.Close()
		cleanup()
		return "", 0, "", fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", 0, "", fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", 0, "", fmt.Errorf("close temp file: %w", err)
	}

	return tmpName, n, hex.EncodeToString(hasher.Sum(nil)), nil
}

func writeFileAtomicFromReader(dest string, r io.Reader, perm os.FileMode) (int64, string, error) {
	tmpName, n, hexSum, err := stageFileFromReader(filepath.Dir(dest), r, perm)
	if err != nil {
		return 0, "", err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		_ = os.Remove(tmpName)
		return 0, "", fmt.Errorf("atomic rename %q: %w", dest, err)
	}
	return n, hexSum, nil
}

func writeFileAtomic(dest string, data []byte, perm os.FileMode) error {
	_, _, err := writeFileAtomicFromReader(dest, bytes.NewReader(data), perm)
	return err
}

func (s *StorageManager) SyncWAL(orgID, appName, sqliteHostPath string) (*SnapshotMetadata, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(sqliteHostPath)
	if err != nil {
		return nil, fmt.Errorf("read sqlite file %q: %w", sqliteHostPath, err)
	}
	defer f.Close()

	dest := s.replicaPath(orgID, appName)
	sizeBytes, hexSum, err := writeFileAtomicFromReader(dest, f, 0o600)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(dest+".sha256", []byte(hexSum+"\n"), 0o600); err != nil {
		return nil, err
	}

	key := tenantKey(orgID, appName)
	s.generations[key]++

	return &SnapshotMetadata{
		OrgID:      orgID,
		AppName:    appName,
		Generation: s.generations[key],
		SizeBytes:  sizeBytes,
		SHA256:     hexSum,
		SyncedAt:   time.Now().UTC(),
	}, nil
}

func (s *StorageManager) Restore(orgID, appName, sqliteHostPath string) (*SnapshotMetadata, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	src := s.replicaPath(orgID, appName)

	var expected string
	if expectedBytes, err := os.ReadFile(src + ".sha256"); err == nil {
		expected = strings.TrimSpace(string(expectedBytes))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read replica checksum: %w", err)
	}

	f, err := os.Open(src)
	if err != nil {
		return nil, fmt.Errorf("read replica %q: %w", src, err)
	}
	defer f.Close()

	tmpName, sizeBytes, hexSum, err := stageFileFromReader(filepath.Dir(sqliteHostPath), f, 0o600)
	if err != nil {
		return nil, err
	}

	if expected != "" && expected != hexSum {
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("%w: got %s, want %s", ErrChecksumMismatch, hexSum, expected)
	}

	if err := os.Rename(tmpName, sqliteHostPath); err != nil {
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("atomic rename %q: %w", sqliteHostPath, err)
	}

	key := tenantKey(orgID, appName)

	return &SnapshotMetadata{
		OrgID:      orgID,
		AppName:    appName,
		Generation: s.generations[key],
		SizeBytes:  sizeBytes,
		SHA256:     hexSum,
		SyncedAt:   time.Now().UTC(),
	}, nil
}

func (s *StorageManager) SnapshotGenerations() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[string]int, len(s.generations))
	for k, v := range s.generations {
		out[k] = v
	}
	return out
}

func (s *StorageManager) RestoreGenerations(gens map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.generations = make(map[string]int, len(gens))
	for k, v := range gens {
		s.generations[k] = v
	}
}
