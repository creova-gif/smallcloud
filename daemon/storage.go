package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

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

func (s *StorageManager) replicaPath(orgID, appName string) string {
	return filepath.Join(s.replicaDir, orgID, appName, "replica.db")
}

func (s *StorageManager) SyncWAL(orgID, appName, sqliteHostPath string) (*SnapshotMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(sqliteHostPath)
	if err != nil {
		return nil, fmt.Errorf("read sqlite file %q: %w", sqliteHostPath, err)
	}

	dest := s.replicaPath(orgID, appName)
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return nil, fmt.Errorf("mkdir replica dir: %w", err)
	}

	tmpDest := dest + ".tmp"
	if err := os.WriteFile(tmpDest, data, 0o600); err != nil {
		return nil, fmt.Errorf("write temp replica: %w", err)
	}
	if err := os.Rename(tmpDest, dest); err != nil {
		return nil, fmt.Errorf("atomic rename replica: %w", err)
	}

	sum := sha256.Sum256(data)
	key := tenantKey(orgID, appName)
	s.generations[key]++

	return &SnapshotMetadata{
		OrgID:      orgID,
		AppName:    appName,
		Generation: s.generations[key],
		SizeBytes:  int64(len(data)),
		SHA256:     hex.EncodeToString(sum[:]),
		SyncedAt:   time.Now().UTC(),
	}, nil
}

func (s *StorageManager) Restore(orgID, appName, sqliteHostPath string) (*SnapshotMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	src := s.replicaPath(orgID, appName)
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("read replica %q: %w", src, err)
	}

	if err := os.MkdirAll(filepath.Dir(sqliteHostPath), 0o750); err != nil {
		return nil, fmt.Errorf("mkdir restore dir: %w", err)
	}

	tmpRestore := sqliteHostPath + ".restore.tmp"
	if err := os.WriteFile(tmpRestore, data, 0o600); err != nil {
		return nil, fmt.Errorf("write restore temp: %w", err)
	}
	if err := os.Rename(tmpRestore, sqliteHostPath); err != nil {
		return nil, fmt.Errorf("atomic rename restored db: %w", err)
	}

	sum := sha256.Sum256(data)
	key := tenantKey(orgID, appName)

	return &SnapshotMetadata{
		OrgID:      orgID,
		AppName:    appName,
		Generation: s.generations[key],
		SizeBytes:  int64(len(data)),
		SHA256:     hex.EncodeToString(sum[:]),
		SyncedAt:   time.Now().UTC(),
	}, nil
}
