package daemon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const backupManifestVersion = "1"

type BackupManifest struct {
	Version   string            `json:"version"`
	CreatedAt time.Time         `json:"created_at"`
	OrgID     string            `json:"org_id"`
	AppName   string            `json:"app_name"`
	Snapshot  *SnapshotMetadata `json:"snapshot,omitempty"`
	Status    AppStatus         `json:"status"`
}

func ExportBackupBundle(d *Daemon, orgID, appName, destPath string) (BackupManifest, error) {
	if d == nil {
		return BackupManifest{}, errors.New("daemon is required")
	}
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return BackupManifest{}, err
	}
	if strings.TrimSpace(destPath) == "" {
		return BackupManifest{}, errors.New("destPath is required")
	}

	status, err := d.Status(orgID, appName)
	if err != nil {
		return BackupManifest{}, err
	}

	now := time.Now().UTC()
	manifest := BackupManifest{
		Version:   backupManifestVersion,
		CreatedAt: now,
		OrgID:     orgID,
		AppName:   appName,
		Status:    status,
	}

	var replicaPath string
	hasReplica := d.Storage().HasReplica(orgID, appName)
	if hasReplica {
		snap, err := d.Storage().VerifySnapshot(orgID, appName)
		if err != nil {
			return BackupManifest{}, err
		}
		manifest.Snapshot = &snap
		replicaPath = d.Storage().replicaPath(orgID, appName)
	}

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return BackupManifest{}, fmt.Errorf("marshal backup manifest: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')

	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return BackupManifest{}, fmt.Errorf("mkdir %q: %w", destDir, err)
	}

	tmp, err := os.CreateTemp(destDir, ".tmp-backup-*")
	if err != nil {
		return BackupManifest{}, fmt.Errorf("create temp backup file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = os.Remove(tmpName)
	}

	if err := writeBackupArchive(tmp, now, manifestBytes, replicaPath, manifest.Snapshot); err != nil {
		_ = tmp.Close()
		cleanup()
		return BackupManifest{}, err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return BackupManifest{}, fmt.Errorf("chmod backup archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return BackupManifest{}, fmt.Errorf("close backup archive: %w", err)
	}
	if err := os.Rename(tmpName, destPath); err != nil {
		cleanup()
		return BackupManifest{}, fmt.Errorf("rename backup archive %q: %w", destPath, err)
	}

	return manifest, nil
}

func writeBackupArchive(w io.Writer, modTime time.Time, manifestBytes []byte, replicaPath string, snap *SnapshotMetadata) error {
	gzw := gzip.NewWriter(w)
	tw := tar.NewWriter(gzw)

	if err := writeTarBytes(tw, "manifest.json", manifestBytes, 0o600, modTime); err != nil {
		_ = tw.Close()
		_ = gzw.Close()
		return err
	}

	if replicaPath != "" && snap != nil {
		if err := writeTarFile(tw, "replica.db", replicaPath, 0o600); err != nil {
			_ = tw.Close()
			_ = gzw.Close()
			return err
		}
		if err := writeTarBytes(tw, "replica.db.sha256", []byte(snap.SHA256+"\n"), 0o600, modTime); err != nil {
			_ = tw.Close()
			_ = gzw.Close()
			return err
		}
	}

	if err := tw.Close(); err != nil {
		_ = gzw.Close()
		return fmt.Errorf("close tar writer: %w", err)
	}
	if err := gzw.Close(); err != nil {
		return fmt.Errorf("close gzip writer: %w", err)
	}
	return nil
}

func writeTarBytes(tw *tar.Writer, name string, data []byte, mode int64, modTime time.Time) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    mode,
		Size:    int64(len(data)),
		ModTime: modTime,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header %q: %w", name, err)
	}
	if _, err := io.Copy(tw, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("write tar entry %q: %w", name, err)
	}
	return nil
}

func writeTarFile(tw *tar.Writer, archiveName, srcPath string, mode int64) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open replica %q: %w", srcPath, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat replica %q: %w", srcPath, err)
	}

	hdr := &tar.Header{
		Name:    archiveName,
		Mode:    mode,
		Size:    info.Size(),
		ModTime: info.ModTime().UTC(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header %q: %w", archiveName, err)
	}
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("write tar entry %q: %w", archiveName, err)
	}
	return nil
}

func VerifyBackupBundle(bundlePath string) (BackupManifest, error) {
	if strings.TrimSpace(bundlePath) == "" {
		return BackupManifest{}, errors.New("bundlePath is required")
	}

	f, err := os.Open(bundlePath)
	if err != nil {
		return BackupManifest{}, fmt.Errorf("open backup bundle %q: %w", bundlePath, err)
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return BackupManifest{}, fmt.Errorf("open gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	var (
		hasManifest bool
		manifest    BackupManifest
		hasReplica  bool
		replicaSum  string
		hasSidecar  bool
		sidecarSum  string
	)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return BackupManifest{}, fmt.Errorf("read tar entry: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}

		switch hdr.Name {
		case "manifest.json":
			data, err := io.ReadAll(tr)
			if err != nil {
				return BackupManifest{}, fmt.Errorf("read manifest.json: %w", err)
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				return BackupManifest{}, fmt.Errorf("decode manifest.json: %w", err)
			}
			hasManifest = true
		case "replica.db":
			hasher := sha256.New()
			if _, err := io.Copy(hasher, tr); err != nil {
				return BackupManifest{}, fmt.Errorf("hash replica.db: %w", err)
			}
			replicaSum = hex.EncodeToString(hasher.Sum(nil))
			hasReplica = true
		case "replica.db.sha256":
			data, err := io.ReadAll(tr)
			if err != nil {
				return BackupManifest{}, fmt.Errorf("read replica.db.sha256: %w", err)
			}
			sidecarSum = strings.TrimSpace(string(data))
			hasSidecar = true
		}
	}

	if err := gzr.Close(); err != nil {
		return BackupManifest{}, fmt.Errorf("verify gzip stream: %w", err)
	}

	if !hasManifest {
		return BackupManifest{}, errors.New("backup bundle missing manifest.json")
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return BackupManifest{}, errors.New("backup manifest missing version")
	}
	if err := validateTenantSlugs(manifest.OrgID, manifest.AppName); err != nil {
		return BackupManifest{}, fmt.Errorf("invalid manifest tenant: %w", err)
	}

	if hasReplica {
		if !hasSidecar || sidecarSum == "" {
			return BackupManifest{}, errors.New("backup bundle missing replica.db.sha256")
		}
		if manifest.Snapshot == nil || strings.TrimSpace(manifest.Snapshot.SHA256) == "" {
			return BackupManifest{}, errors.New("backup manifest missing snapshot sha256")
		}
		if replicaSum != sidecarSum {
			return BackupManifest{}, fmt.Errorf("%w: archive replica.db %s != replica.db.sha256 %s", ErrChecksumMismatch, replicaSum, sidecarSum)
		}
		if replicaSum != manifest.Snapshot.SHA256 {
			return BackupManifest{}, fmt.Errorf("%w: archive replica.db %s != manifest snapshot %s", ErrChecksumMismatch, replicaSum, manifest.Snapshot.SHA256)
		}
	} else if hasSidecar || manifest.Snapshot != nil {
		return BackupManifest{}, errors.New("backup bundle missing replica.db")
	}

	return manifest, nil
}

func RestoreBackupBundle(d *Daemon, bundlePath string) (BackupManifest, error) {
	if d == nil {
		return BackupManifest{}, errors.New("daemon is required")
	}

	manifest, err := VerifyBackupBundle(bundlePath)
	if err != nil {
		return BackupManifest{}, err
	}

	if manifest.Snapshot != nil {
		targetDir := filepath.Join(d.Storage().ReplicaDir(), manifest.OrgID, manifest.AppName)
		if err := extractReplicaFromBundle(bundlePath, targetDir); err != nil {
			return BackupManifest{}, err
		}
		if _, err := d.Storage().VerifySnapshot(manifest.OrgID, manifest.AppName); err != nil {
			return BackupManifest{}, err
		}
	}

	detail := "no_replica"
	if manifest.Snapshot != nil {
		detail = fmt.Sprintf("sha256=%s", manifest.Snapshot.SHA256)
	}
	d.Audit().Record(AuditEvent{
		Type:      "backup_restored",
		OrgID:     manifest.OrgID,
		AppName:   manifest.AppName,
		ReleaseID: manifest.Status.Route.ActiveReleaseID,
		Detail:    detail,
	})

	return manifest, nil
}

func extractReplicaFromBundle(bundlePath, targetDir string) error {
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return fmt.Errorf("mkdir %q: %w", targetDir, err)
	}

	f, err := os.Open(bundlePath)
	if err != nil {
		return fmt.Errorf("open backup bundle %q: %w", bundlePath, err)
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		if hdr.Name != "replica.db" && hdr.Name != "replica.db.sha256" {
			continue
		}
		if err := writeAtomicFile(targetDir, filepath.Join(targetDir, hdr.Name), tr); err != nil {
			return err
		}
	}

	if err := gzr.Close(); err != nil {
		return fmt.Errorf("verify gzip stream: %w", err)
	}
	return nil
}

func writeAtomicFile(dir, destPath string, r io.Reader) error {
	tmp, err := os.CreateTemp(dir, ".tmp-restore-*")
	if err != nil {
		return fmt.Errorf("create temp file in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = os.Remove(tmpName)
	}

	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp file %q: %w", destPath, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp file %q: %w", destPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file %q: %w", destPath, err)
	}
	if err := os.Rename(tmpName, destPath); err != nil {
		cleanup()
		return fmt.Errorf("rename %q: %w", destPath, err)
	}
	return nil
}
