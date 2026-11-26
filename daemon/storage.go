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
	"sort"
	"strconv"
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

func (s *StorageManager) tenantDir(orgID, appName string) string {
	return filepath.Join(s.replicaDir, orgID, appName)
}

func (s *StorageManager) replicaPath(orgID, appName string) string {
	return filepath.Join(s.tenantDir(orgID, appName), "replica.db")
}

func (s *StorageManager) snapshotPath(orgID, appName string, generation int) string {
	return filepath.Join(s.tenantDir(orgID, appName), fmt.Sprintf("snapshot-%d.db", generation))
}

func parseSnapshotGeneration(name string) (int, bool) {
	if !strings.HasPrefix(name, "snapshot-") || !strings.HasSuffix(name, ".db") {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(name, "snapshot-"), ".db")
	gen, err := strconv.Atoi(raw)
	if err != nil || gen <= 0 {
		return 0, false
	}
	return gen, true
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
	if s.generations[key] == 0 {
		if existing, err := s.listSnapshotsLocked(orgID, appName); err == nil && len(existing) > 0 {
			s.generations[key] = existing[0].Generation
		}
	}
	s.generations[key]++
	gen := s.generations[key]

	snapDest := s.snapshotPath(orgID, appName, gen)
	snapSrc, err := os.Open(dest)
	if err != nil {
		return nil, fmt.Errorf("reopen replica %q: %w", dest, err)
	}
	_, _, snapErr := writeFileAtomicFromReader(snapDest, snapSrc, 0o600)
	_ = snapSrc.Close()
	if snapErr != nil {
		return nil, snapErr
	}
	if err := writeFileAtomic(snapDest+".sha256", []byte(hexSum+"\n"), 0o600); err != nil {
		return nil, err
	}

	return &SnapshotMetadata{
		OrgID:      orgID,
		AppName:    appName,
		Generation: gen,
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

func (m *StorageManager) listSnapshotsLocked(orgID, appName string) ([]SnapshotMetadata, error) {
	dir := m.tenantDir(orgID, appName)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []SnapshotMetadata{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read snapshot dir %q: %w", dir, err)
	}

	snapshots := make([]SnapshotMetadata, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		gen, ok := parseSnapshotGeneration(entry.Name())
		if !ok {
			continue
		}
		sidecarBytes, err := os.ReadFile(filepath.Join(dir, entry.Name()+".sha256"))
		if err != nil {
			continue
		}
		sha := strings.TrimSpace(string(sidecarBytes))
		if sha == "" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		snapshots = append(snapshots, SnapshotMetadata{
			OrgID:      orgID,
			AppName:    appName,
			Generation: gen,
			SizeBytes:  info.Size(),
			SHA256:     sha,
			SyncedAt:   info.ModTime().UTC(),
		})
	}

	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].Generation != snapshots[j].Generation {
			return snapshots[i].Generation > snapshots[j].Generation
		}
		return snapshots[i].SyncedAt.After(snapshots[j].SyncedAt)
	})

	return snapshots, nil
}

func (m *StorageManager) ListSnapshots(orgID, appName string) ([]SnapshotMetadata, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	return m.listSnapshotsLocked(orgID, appName)
}

func (m *StorageManager) PruneSnapshots(orgID, appName string, keepLast int) (int, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return 0, err
	}
	if keepLast < 1 {
		return 0, fmt.Errorf("keepLast must be >= 1, got %d", keepLast)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	snapshots, err := m.listSnapshotsLocked(orgID, appName)
	if err != nil {
		return 0, err
	}
	if len(snapshots) <= keepLast {
		return 0, nil
	}

	pruned := 0
	for _, snap := range snapshots[keepLast:] {
		snapPath := m.snapshotPath(orgID, appName, snap.Generation)
		if err := os.Remove(snapPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return pruned, fmt.Errorf("remove snapshot %q: %w", snapPath, err)
		}
		sidecarPath := snapPath + ".sha256"
		if err := os.Remove(sidecarPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return pruned, fmt.Errorf("remove snapshot sidecar %q: %w", sidecarPath, err)
		}
		pruned++
	}

	return pruned, nil
}

func (m *StorageManager) VerifySnapshot(orgID, appName string) (SnapshotMetadata, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return SnapshotMetadata{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	src := m.replicaPath(orgID, appName)

	expectedBytes, err := os.ReadFile(src + ".sha256")
	if err != nil {
		return SnapshotMetadata{}, fmt.Errorf("read replica checksum: %w", err)
	}
	expected := strings.TrimSpace(string(expectedBytes))
	if expected == "" {
		return SnapshotMetadata{}, errors.New("empty replica sha256 checksum")
	}

	f, err := os.Open(src)
	if err != nil {
		return SnapshotMetadata{}, fmt.Errorf("read replica %q: %w", src, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return SnapshotMetadata{}, fmt.Errorf("stat replica %q: %w", src, err)
	}

	hasher := sha256.New()
	sizeBytes, err := io.Copy(hasher, f)
	if err != nil {
		return SnapshotMetadata{}, fmt.Errorf("hash replica %q: %w", src, err)
	}
	hexSum := hex.EncodeToString(hasher.Sum(nil))
	if hexSum != expected {
		return SnapshotMetadata{}, fmt.Errorf("%w: got %s, want %s", ErrChecksumMismatch, hexSum, expected)
	}

	key := tenantKey(orgID, appName)
	gen := m.generations[key]
	if gen == 0 {
		if existing, err := m.listSnapshotsLocked(orgID, appName); err == nil && len(existing) > 0 {
			gen = existing[0].Generation
		}
	}

	return SnapshotMetadata{
		OrgID:      orgID,
		AppName:    appName,
		Generation: gen,
		SizeBytes:  sizeBytes,
		SHA256:     hexSum,
		SyncedAt:   info.ModTime().UTC(),
	}, nil
}
