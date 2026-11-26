package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

func TestStorageManagerListSnapshotsAndPruneSnapshots(t *testing.T) {
	dir := t.TempDir()
	sm := NewStorageManager(filepath.Join(dir, "replicas"))

	empty, err := sm.ListSnapshots("acme", "app1")
	if err != nil {
		t.Fatalf("ListSnapshots empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("ListSnapshots empty len = %d, want 0", len(empty))
	}

	if _, err := sm.ListSnapshots("../etc", "app1"); err == nil {
		t.Fatal("expected error for invalid orgID in ListSnapshots")
	}
	if _, err := sm.PruneSnapshots("../etc", "app1", 1); err == nil {
		t.Fatal("expected error for invalid orgID in PruneSnapshots")
	}
	for _, invalidKeep := range []int{0, -1, -10} {
		if _, err := sm.PruneSnapshots("acme", "app1", invalidKeep); err == nil {
			t.Fatalf("expected error for keepLast=%d", invalidKeep)
		}
	}

	srcPath := filepath.Join(dir, "host.db")
	var expected []*SnapshotMetadata
	var latestPayload []byte
	for i := 1; i <= 4; i++ {
		payload := []byte(fmt.Sprintf("sqlite-snapshot-payload-gen-%d", i))
		latestPayload = payload
		if err := os.WriteFile(srcPath, payload, 0o600); err != nil {
			t.Fatalf("write source gen %d: %v", i, err)
		}
		meta, err := sm.SyncWAL("acme", "app1", srcPath)
		if err != nil {
			t.Fatalf("SyncWAL gen %d: %v", i, err)
		}
		expected = append(expected, meta)
	}

	listed, err := sm.ListSnapshots("acme", "app1")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(listed) != 4 {
		t.Fatalf("ListSnapshots len = %d, want 4", len(listed))
	}
	for i, snap := range listed {
		want := expected[len(expected)-1-i]
		if snap.Generation != want.Generation || snap.SHA256 != want.SHA256 || snap.SizeBytes != want.SizeBytes {
			t.Fatalf("listed[%d] = %+v, want %+v", i, snap, want)
		}
	}

	pruned, err := sm.PruneSnapshots("acme", "app1", 2)
	if err != nil {
		t.Fatalf("PruneSnapshots: %v", err)
	}
	if pruned != 2 {
		t.Fatalf("PruneSnapshots pruned = %d, want 2", pruned)
	}

	afterPrune, err := sm.ListSnapshots("acme", "app1")
	if err != nil {
		t.Fatalf("ListSnapshots after prune: %v", err)
	}
	if len(afterPrune) != 2 {
		t.Fatalf("ListSnapshots after prune len = %d, want 2", len(afterPrune))
	}
	if afterPrune[0].Generation != 4 || afterPrune[1].Generation != 3 {
		t.Fatalf("unexpected remaining generations: %+v", afterPrune)
	}

	for _, gen := range []int{1, 2} {
		snapPath := sm.snapshotPath("acme", "app1", gen)
		if _, err := os.Stat(snapPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected pruned snapshot %q to be removed, err=%v", snapPath, err)
		}
		if _, err := os.Stat(snapPath + ".sha256"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected pruned sidecar %q to be removed, err=%v", snapPath+".sha256", err)
		}
	}

	for _, gen := range []int{3, 4} {
		snapPath := sm.snapshotPath("acme", "app1", gen)
		if _, err := os.Stat(snapPath); err != nil {
			t.Fatalf("expected kept snapshot %q to exist: %v", snapPath, err)
		}
		if _, err := os.Stat(snapPath + ".sha256"); err != nil {
			t.Fatalf("expected kept sidecar %q to exist: %v", snapPath+".sha256", err)
		}
	}

	restorePath := filepath.Join(dir, "restored", "host.db")
	restoredMeta, err := sm.Restore("acme", "app1", restorePath)
	if err != nil {
		t.Fatalf("Restore after prune: %v", err)
	}
	if restoredMeta.SHA256 != expected[3].SHA256 {
		t.Fatalf("Restore SHA256 = %s, want %s", restoredMeta.SHA256, expected[3].SHA256)
	}
	restoredBytes, err := os.ReadFile(restorePath)
	if err != nil {
		t.Fatalf("read restored db: %v", err)
	}
	if !bytes.Equal(restoredBytes, latestPayload) {
		t.Fatalf("restored payload mismatch after prune")
	}

	prunedAgain, err := sm.PruneSnapshots("acme", "app1", 2)
	if err != nil {
		t.Fatalf("PruneSnapshots idempotent: %v", err)
	}
	if prunedAgain != 0 {
		t.Fatalf("PruneSnapshots idempotent pruned = %d, want 0", prunedAgain)
	}
}

func TestStorageManagerVerifySnapshotSuccessAndCorruption(t *testing.T) {
	dir := t.TempDir()
	sm := NewStorageManager(filepath.Join(dir, "replicas"))

	if _, err := sm.VerifySnapshot("../etc", "app1"); err == nil {
		t.Fatal("expected error for invalid orgID in VerifySnapshot")
	}
	if _, err := sm.VerifySnapshot("acme", "app1"); err == nil {
		t.Fatal("expected error when replica and checksum are missing")
	}

	payload := []byte("sqlite-verify-snapshot-payload")
	expectedSum := sha256.Sum256(payload)
	expectedHex := hex.EncodeToString(expectedSum[:])

	srcPath := filepath.Join(dir, "host.db")
	if err := os.WriteFile(srcPath, payload, 0o600); err != nil {
		t.Fatalf("write source db: %v", err)
	}

	syncedMeta, err := sm.SyncWAL("acme", "app1", srcPath)
	if err != nil {
		t.Fatalf("SyncWAL: %v", err)
	}

	verifiedMeta, err := sm.VerifySnapshot("acme", "app1")
	if err != nil {
		t.Fatalf("VerifySnapshot: %v", err)
	}
	if verifiedMeta.OrgID != "acme" || verifiedMeta.AppName != "app1" {
		t.Fatalf("unexpected tenant metadata: %+v", verifiedMeta)
	}
	if verifiedMeta.Generation != syncedMeta.Generation {
		t.Fatalf("VerifySnapshot Generation = %d, want %d", verifiedMeta.Generation, syncedMeta.Generation)
	}
	if verifiedMeta.SizeBytes != int64(len(payload)) {
		t.Fatalf("VerifySnapshot SizeBytes = %d, want %d", verifiedMeta.SizeBytes, len(payload))
	}
	if verifiedMeta.SHA256 != expectedHex {
		t.Fatalf("VerifySnapshot SHA256 = %s, want %s", verifiedMeta.SHA256, expectedHex)
	}

	sidecarPath := sm.replicaPath("acme", "app1") + ".sha256"
	if err := os.WriteFile(sidecarPath, []byte(strings.Repeat("0", 64)+"\n"), 0o600); err != nil {
		t.Fatalf("corrupt sidecar: %v", err)
	}
	if _, err := sm.VerifySnapshot("acme", "app1"); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("VerifySnapshot corrupted sidecar err = %v, want ErrChecksumMismatch", err)
	}

	if err := os.WriteFile(sidecarPath, []byte(expectedHex+"\n"), 0o600); err != nil {
		t.Fatalf("restore sidecar: %v", err)
	}
	if err := os.WriteFile(sm.replicaPath("acme", "app1"), []byte("corrupted-bytes"), 0o600); err != nil {
		t.Fatalf("corrupt replica: %v", err)
	}
	if _, err := sm.VerifySnapshot("acme", "app1"); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("VerifySnapshot corrupted replica err = %v, want ErrChecksumMismatch", err)
	}

	if err := os.Remove(sidecarPath); err != nil {
		t.Fatalf("remove sidecar: %v", err)
	}
	if _, err := sm.VerifySnapshot("acme", "app1"); err == nil {
		t.Fatal("expected error when checksum sidecar is missing")
	}
}
