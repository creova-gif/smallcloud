package daemon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupExportAndVerifyRoundtripWithReplica(t *testing.T) {
	d := NewDaemon(t.TempDir(), t.TempDir())
	cfg := sampleConfig()

	if _, err := d.Deploy(cfg, "v1.0.0"); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	sqliteSrc := filepath.Join(t.TempDir(), "crm.db")
	payload := []byte("sqlite-wal-backup-payload")
	if err := os.WriteFile(sqliteSrc, payload, 0o600); err != nil {
		t.Fatalf("write sqlite src: %v", err)
	}

	syncedMeta, err := d.Storage().SyncWAL(cfg.App.OrgID, cfg.App.Name, sqliteSrc)
	if err != nil {
		t.Fatalf("SyncWAL: %v", err)
	}

	bundlePath := filepath.Join(t.TempDir(), "nested", "backup.tar.gz")
	exported, err := ExportBackupBundle(d, cfg.App.OrgID, cfg.App.Name, bundlePath)
	if err != nil {
		t.Fatalf("ExportBackupBundle: %v", err)
	}

	if exported.Version != backupManifestVersion {
		t.Fatalf("exported version = %q, want %q", exported.Version, backupManifestVersion)
	}
	if exported.OrgID != cfg.App.OrgID || exported.AppName != cfg.App.Name {
		t.Fatalf("exported tenant = %s/%s, want %s/%s", exported.OrgID, exported.AppName, cfg.App.OrgID, cfg.App.Name)
	}
	if exported.Snapshot == nil || exported.Snapshot.SHA256 != syncedMeta.SHA256 {
		t.Fatalf("exported snapshot = %+v, want sha256 %s", exported.Snapshot, syncedMeta.SHA256)
	}

	verified, err := VerifyBackupBundle(bundlePath)
	if err != nil {
		t.Fatalf("VerifyBackupBundle: %v", err)
	}
	if verified.OrgID != exported.OrgID || verified.AppName != exported.AppName {
		t.Fatalf("verified tenant = %s/%s, want %s/%s", verified.OrgID, verified.AppName, exported.OrgID, exported.AppName)
	}
	if verified.Snapshot == nil || verified.Snapshot.SHA256 != syncedMeta.SHA256 {
		t.Fatalf("verified snapshot = %+v, want sha256 %s", verified.Snapshot, syncedMeta.SHA256)
	}
	if verified.Status.Route.ActiveReleaseID != "v1.0.0" {
		t.Fatalf("verified status release = %q, want v1.0.0", verified.Status.Route.ActiveReleaseID)
	}
}

func TestBackupExportAndVerifyWithoutReplica(t *testing.T) {
	d := NewDaemon(t.TempDir(), t.TempDir())
	cfg := sampleConfig()

	if _, err := d.Deploy(cfg, "v1.0.0"); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	bundlePath := filepath.Join(t.TempDir(), "no-replica.tar.gz")
	exported, err := ExportBackupBundle(d, cfg.App.OrgID, cfg.App.Name, bundlePath)
	if err != nil {
		t.Fatalf("ExportBackupBundle: %v", err)
	}
	if exported.Snapshot != nil {
		t.Fatalf("expected nil snapshot when no replica exists, got %+v", exported.Snapshot)
	}

	verified, err := VerifyBackupBundle(bundlePath)
	if err != nil {
		t.Fatalf("VerifyBackupBundle: %v", err)
	}
	if verified.Snapshot != nil {
		t.Fatalf("expected nil verified snapshot, got %+v", verified.Snapshot)
	}
}

func TestBackupValidationErrors(t *testing.T) {
	d := NewDaemon(t.TempDir(), t.TempDir())
	cfg := sampleConfig()

	if _, err := ExportBackupBundle(nil, cfg.App.OrgID, cfg.App.Name, filepath.Join(t.TempDir(), "b.tar.gz")); err == nil {
		t.Fatal("expected error for nil daemon")
	}
	if _, err := ExportBackupBundle(d, "../bad", cfg.App.Name, filepath.Join(t.TempDir(), "b.tar.gz")); err == nil {
		t.Fatal("expected error for invalid orgID")
	}
	if _, err := ExportBackupBundle(d, cfg.App.OrgID, "../bad", filepath.Join(t.TempDir(), "b.tar.gz")); err == nil {
		t.Fatal("expected error for invalid appName")
	}
	if _, err := ExportBackupBundle(d, cfg.App.OrgID, cfg.App.Name, "   "); err == nil {
		t.Fatal("expected error for empty destPath")
	}
	if _, err := ExportBackupBundle(d, cfg.App.OrgID, cfg.App.Name, filepath.Join(t.TempDir(), "missing.tar.gz")); err == nil {
		t.Fatal("expected error for non-existent app")
	}
	if _, err := VerifyBackupBundle("   "); err == nil {
		t.Fatal("expected error for empty bundlePath")
	}
}

func TestBackupVerifyCorruptAndChecksumMismatch(t *testing.T) {
	corruptGzip := filepath.Join(t.TempDir(), "corrupt.tar.gz")
	if err := os.WriteFile(corruptGzip, []byte("not-a-gzip-stream"), 0o600); err != nil {
		t.Fatalf("write corrupt gzip: %v", err)
	}
	if _, err := VerifyBackupBundle(corruptGzip); err == nil {
		t.Fatal("expected error for corrupt gzip archive")
	}

	missingManifest := filepath.Join(t.TempDir(), "no-manifest.tar.gz")
	writeCustomTarGz(t, missingManifest, map[string][]byte{
		"other.txt": []byte("hello"),
	})
	if _, err := VerifyBackupBundle(missingManifest); err == nil {
		t.Fatal("expected error for missing manifest.json")
	}

	d := NewDaemon(t.TempDir(), t.TempDir())
	cfg := sampleConfig()
	if _, err := d.Deploy(cfg, "v1.0.0"); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	sqliteSrc := filepath.Join(t.TempDir(), "crm.db")
	payload := []byte("sqlite-checksum-test")
	if err := os.WriteFile(sqliteSrc, payload, 0o600); err != nil {
		t.Fatalf("write sqlite src: %v", err)
	}
	if _, err := d.Storage().SyncWAL(cfg.App.OrgID, cfg.App.Name, sqliteSrc); err != nil {
		t.Fatalf("SyncWAL: %v", err)
	}

	validBundle := filepath.Join(t.TempDir(), "valid.tar.gz")
	manifest, err := ExportBackupBundle(d, cfg.App.OrgID, cfg.App.Name, validBundle)
	if err != nil {
		t.Fatalf("ExportBackupBundle: %v", err)
	}

	entries := readTarGzEntries(t, validBundle)
	entries["replica.db.sha256"] = []byte("0000000000000000000000000000000000000000000000000000000000000000\n")
	tamperedSidecar := filepath.Join(t.TempDir(), "tampered-sidecar.tar.gz")
	writeCustomTarGz(t, tamperedSidecar, entries)
	if _, err := VerifyBackupBundle(tamperedSidecar); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("VerifyBackupBundle tampered sidecar err = %v, want ErrChecksumMismatch", err)
	}

	entries = readTarGzEntries(t, validBundle)
	tamperedPayload := []byte("tampered-sqlite-bytes")
	sum := sha256.Sum256(tamperedPayload)
	entries["replica.db"] = tamperedPayload
	entries["replica.db.sha256"] = []byte(hex.EncodeToString(sum[:]) + "\n")
	tamperedDB := filepath.Join(t.TempDir(), "tampered-db.tar.gz")
	writeCustomTarGz(t, tamperedDB, entries)
	if _, err := VerifyBackupBundle(tamperedDB); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("VerifyBackupBundle tampered db vs manifest err = %v, want ErrChecksumMismatch", err)
	}

	badManifest := manifest
	badManifest.Snapshot = nil
	badManifestBytes, err := json.Marshal(badManifest)
	if err != nil {
		t.Fatalf("marshal badManifest: %v", err)
	}
	entries = readTarGzEntries(t, validBundle)
	entries["manifest.json"] = badManifestBytes
	missingSnapMeta := filepath.Join(t.TempDir(), "missing-snap-meta.tar.gz")
	writeCustomTarGz(t, missingSnapMeta, entries)
	if _, err := VerifyBackupBundle(missingSnapMeta); err == nil {
		t.Fatal("expected error when replica.db present without manifest snapshot metadata")
	}
}

func writeCustomTarGz(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	now := time.Now().UTC()
	for name, data := range entries {
		hdr := &tar.Header{
			Name:    name,
			Mode:    0o600,
			Size:    int64(len(data)),
			ModTime: now,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader %q: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("Write %q: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile %q: %v", path, err)
	}
}

func readTarGzEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open %q: %v", path, err)
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	out := make(map[string][]byte)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("ReadAll %q: %v", hdr.Name, err)
		}
		out[hdr.Name] = data
	}
	return out
}

func TestBackupRestoreRoundtripZeroDataLoss(t *testing.T) {
	dA := NewDaemon(t.TempDir(), t.TempDir())
	cfg := sampleConfig()

	if _, err := dA.Deploy(cfg, "v1.0.0"); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	sqliteSrc := filepath.Join(t.TempDir(), "crm.db")
	payload := append([]byte("SQLite format 3\x00"), bytes.Repeat([]byte{0x42, 0x99, 0x00, 0x13}, 1024)...)
	if err := os.WriteFile(sqliteSrc, payload, 0o600); err != nil {
		t.Fatalf("write sqlite src: %v", err)
	}

	syncedMeta, err := dA.Storage().SyncWAL(cfg.App.OrgID, cfg.App.Name, sqliteSrc)
	if err != nil {
		t.Fatalf("SyncWAL: %v", err)
	}

	bundlePath := filepath.Join(t.TempDir(), "roundtrip.tar.gz")
	exported, err := ExportBackupBundle(dA, cfg.App.OrgID, cfg.App.Name, bundlePath)
	if err != nil {
		t.Fatalf("ExportBackupBundle: %v", err)
	}

	dB := NewDaemon(t.TempDir(), t.TempDir())
	restoredManifest, err := RestoreBackupBundle(dB, bundlePath)
	if err != nil {
		t.Fatalf("RestoreBackupBundle: %v", err)
	}

	if restoredManifest.OrgID != exported.OrgID || restoredManifest.AppName != exported.AppName {
		t.Fatalf("restored tenant = %s/%s, want %s/%s", restoredManifest.OrgID, restoredManifest.AppName, exported.OrgID, exported.AppName)
	}
	if restoredManifest.Snapshot == nil || restoredManifest.Snapshot.SHA256 != syncedMeta.SHA256 {
		t.Fatalf("restored snapshot = %+v, want sha256 %s", restoredManifest.Snapshot, syncedMeta.SHA256)
	}

	recoveredPath := filepath.Join(t.TempDir(), "recovered.db")
	recoveredMeta, err := dB.Storage().Restore(cfg.App.OrgID, cfg.App.Name, recoveredPath)
	if err != nil {
		t.Fatalf("dB.Storage().Restore: %v", err)
	}
	if recoveredMeta.SHA256 != syncedMeta.SHA256 {
		t.Fatalf("recovered sha256 = %s, want %s", recoveredMeta.SHA256, syncedMeta.SHA256)
	}

	recoveredBytes, err := os.ReadFile(recoveredPath)
	if err != nil {
		t.Fatalf("read recovered db: %v", err)
	}
	if !bytes.Equal(recoveredBytes, payload) {
		t.Fatalf("recovered db bytes differ from original payload (got %d bytes, want %d bytes)", len(recoveredBytes), len(payload))
	}

	events := dB.Audit().List(cfg.App.OrgID, cfg.App.Name, 10)
	if len(events) != 1 || events[0].Type != "backup_restored" {
		t.Fatalf("audit events = %+v, want single backup_restored event", events)
	}
}

func TestBackupRestoreRejectsCorruptBundle(t *testing.T) {
	dA := NewDaemon(t.TempDir(), t.TempDir())
	cfg := sampleConfig()
	if _, err := dA.Deploy(cfg, "v1.0.0"); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	sqliteSrc := filepath.Join(t.TempDir(), "crm.db")
	if err := os.WriteFile(sqliteSrc, []byte("original-sqlite-bytes"), 0o600); err != nil {
		t.Fatalf("write sqlite src: %v", err)
	}
	if _, err := dA.Storage().SyncWAL(cfg.App.OrgID, cfg.App.Name, sqliteSrc); err != nil {
		t.Fatalf("SyncWAL: %v", err)
	}

	validBundle := filepath.Join(t.TempDir(), "valid.tar.gz")
	if _, err := ExportBackupBundle(dA, cfg.App.OrgID, cfg.App.Name, validBundle); err != nil {
		t.Fatalf("ExportBackupBundle: %v", err)
	}

	if _, err := RestoreBackupBundle(nil, validBundle); err == nil {
		t.Fatal("expected error for nil daemon")
	}

	dB := NewDaemon(t.TempDir(), t.TempDir())

	corruptGzip := filepath.Join(t.TempDir(), "corrupt.tar.gz")
	if err := os.WriteFile(corruptGzip, []byte("corrupt-data"), 0o600); err != nil {
		t.Fatalf("write corrupt gzip: %v", err)
	}
	if _, err := RestoreBackupBundle(dB, corruptGzip); err == nil {
		t.Fatal("expected error for corrupt gzip bundle")
	}

	entries := readTarGzEntries(t, validBundle)
	entries["replica.db"] = []byte("tampered-bytes")
	tamperedBundle := filepath.Join(t.TempDir(), "tampered.tar.gz")
	writeCustomTarGz(t, tamperedBundle, entries)

	if _, err := RestoreBackupBundle(dB, tamperedBundle); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("RestoreBackupBundle tampered err = %v, want ErrChecksumMismatch", err)
	}

	if dB.Storage().HasReplica(cfg.App.OrgID, cfg.App.Name) {
		t.Fatal("expected local replica storage to remain untouched after rejected bundle")
	}
	if events := dB.Audit().List(cfg.App.OrgID, cfg.App.Name, 10); len(events) != 0 {
		t.Fatalf("expected no audit events on rejected restore, got %+v", events)
	}
}
