package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

func writeFileAtomic(dest string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("mkdir %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = os.Remove(tmpName)
	}

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		cleanup()
		return fmt.Errorf("atomic rename %q: %w", dest, err)
	}
	return nil
}

func (s *StorageManager) SyncWAL(orgID, appName, sqliteHostPath string) (*SnapshotMetadata, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(sqliteHostPath)
	if err != nil {
		return nil, fmt.Errorf("read sqlite file %q: %w", sqliteHostPath, err)
	}

	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])

	dest := s.replicaPath(orgID, appName)
	if err := writeFileAtomic(dest, data, 0o600); err != nil {
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
		SizeBytes:  int64(len(data)),
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
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("read replica %q: %w", src, err)
	}

	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])

	if expectedBytes, err := os.ReadFile(src + ".sha256"); err == nil {
		expected := strings.TrimSpace(string(expectedBytes))
		if expected != "" && expected != hexSum {
			return nil, fmt.Errorf("%w: got %s, want %s", ErrChecksumMismatch, hexSum, expected)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read replica checksum: %w", err)
	}

	if err := writeFileAtomic(sqliteHostPath, data, 0o600); err != nil {
		return nil, err
	}

	key := tenantKey(orgID, appName)

	return &SnapshotMetadata{
		OrgID:      orgID,
		AppName:    appName,
		Generation: s.generations[key],
		SizeBytes:  int64(len(data)),
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
