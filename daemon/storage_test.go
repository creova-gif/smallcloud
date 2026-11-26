package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageManagerStreamingSyncWALAndRestore(t *testing.T) {
	dir := t.TempDir()
	replicaDir := filepath.Join(dir, "replicas")
	sm := NewStorageManager(replicaDir)

	const payloadSize = 1 << 20
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte((i*31 + 7) & 0xff)
	}
	expectedSum := sha256.Sum256(payload)
	expectedHex := hex.EncodeToString(expectedSum[:])

	srcPath := filepath.Join(dir, "host.db")
	if err := os.WriteFile(srcPath, payload, 0o600); err != nil {
		t.Fatalf("write source db: %v", err)
	}

	meta, err := sm.SyncWAL("acme", "app1", srcPath)
	if err != nil {
		t.Fatalf("SyncWAL: %v", err)
	}
	if meta.SizeBytes != payloadSize {
		t.Fatalf("SyncWAL SizeBytes = %d, want %d", meta.SizeBytes, payloadSize)
	}
	if meta.SHA256 != expectedHex {
		t.Fatalf("SyncWAL SHA256 = %s, want %s", meta.SHA256, expectedHex)
	}
	if meta.Generation != 1 {
		t.Fatalf("SyncWAL Generation = %d, want 1", meta.Generation)
	}

	sidecar, err := os.ReadFile(sm.replicaPath("acme", "app1") + ".sha256")
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if strings.TrimSpace(string(sidecar)) != expectedHex {
		t.Fatalf("sidecar = %q, want %q", strings.TrimSpace(string(sidecar)), expectedHex)
	}

	restorePath := filepath.Join(dir, "restored", "host.db")
	restoredMeta, err := sm.Restore("acme", "app1", restorePath)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restoredMeta.SizeBytes != payloadSize {
		t.Fatalf("Restore SizeBytes = %d, want %d", restoredMeta.SizeBytes, payloadSize)
	}
	if restoredMeta.SHA256 != expectedHex {
		t.Fatalf("Restore SHA256 = %s, want %s", restoredMeta.SHA256, expectedHex)
	}

	got, err := os.ReadFile(restorePath)
	if err != nil {
		t.Fatalf("read restored db: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("restored payload mismatch")
	}
}

func TestStorageManagerRestoreRejectsCorruptChecksumBeforeRename(t *testing.T) {
	dir := t.TempDir()
	sm := NewStorageManager(filepath.Join(dir, "replicas"))

	srcPath := filepath.Join(dir, "host.db")
	if err := os.WriteFile(srcPath, []byte("valid-sqlite-bytes"), 0o600); err != nil {
		t.Fatalf("write source db: %v", err)
	}

	if _, err := sm.SyncWAL("acme", "app1", srcPath); err != nil {
		t.Fatalf("SyncWAL: %v", err)
	}

	sidecarPath := sm.replicaPath("acme", "app1") + ".sha256"
	if err := os.WriteFile(sidecarPath, []byte(strings.Repeat("0", 64)+"\n"), 0o600); err != nil {
		t.Fatalf("corrupt sidecar: %v", err)
	}

	restoreDir := filepath.Join(dir, "restore")
	if err := os.MkdirAll(restoreDir, 0o750); err != nil {
		t.Fatalf("mkdir restoreDir: %v", err)
	}
	restorePath := filepath.Join(restoreDir, "host.db")
	originalSentinel := []byte("existing-sentinel")
	if err := os.WriteFile(restorePath, originalSentinel, 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	_, err := sm.Restore("acme", "app1", restorePath)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Restore error = %v, want ErrChecksumMismatch", err)
	}

	after, err := os.ReadFile(restorePath)
	if err != nil {
		t.Fatalf("read sentinel after failed restore: %v", err)
	}
	if !bytes.Equal(after, originalSentinel) {
		t.Fatalf("destination file was modified despite checksum mismatch")
	}

	entries, err := os.ReadDir(restoreDir)
	if err != nil {
		t.Fatalf("readdir restoreDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "host.db" {
		t.Fatalf("unexpected leftover files in restoreDir: %v", entries)
	}
}
