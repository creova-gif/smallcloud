package daemon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fuzzTarEntry struct {
	hdr  *tar.Header
	data []byte
}

func buildFuzzTarGz(entries []fuzzTarEntry) []byte {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for _, entry := range entries {
		if err := tw.WriteHeader(entry.hdr); err != nil {
			panic(err)
		}
		if len(entry.data) > 0 {
			if _, err := tw.Write(entry.data); err != nil {
				panic(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	if err := gzw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func buildFuzzTruncatedTarGz(manifestBytes []byte, now time.Time) []byte {
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	_ = tw.WriteHeader(&tar.Header{
		Name:     "manifest.json",
		Typeflag: tar.TypeReg,
		Mode:     0o600,
		Size:     int64(len(manifestBytes)),
		ModTime:  now,
	})
	_, _ = tw.Write(manifestBytes)

	rawTar := tarBuf.Bytes()
	if len(rawTar) > 10 {
		rawTar = rawTar[:len(rawTar)/2]
	}

	var gzBuf bytes.Buffer
	gzw := gzip.NewWriter(&gzBuf)
	_, _ = gzw.Write(rawTar)
	_ = gzw.Close()
	return gzBuf.Bytes()
}

func FuzzVerifyBackupBundle(f *testing.F) {
	now := time.Unix(1700000000, 0).UTC()
	manifestBytes, err := json.Marshal(BackupManifest{
		Version:   backupManifestVersion,
		CreatedAt: now,
		OrgID:     "acme",
		AppName:   "crm",
	})
	if err != nil {
		f.Fatalf("marshal manifest: %v", err)
	}

	validTarGz := buildFuzzTarGz([]fuzzTarEntry{
		{
			hdr: &tar.Header{
				Name:     "manifest.json",
				Typeflag: tar.TypeReg,
				Mode:     0o600,
				Size:     int64(len(manifestBytes)),
				ModTime:  now,
			},
			data: manifestBytes,
		},
	})

	traversalTarGz := buildFuzzTarGz([]fuzzTarEntry{
		{
			hdr: &tar.Header{
				Name:     "manifest.json",
				Typeflag: tar.TypeReg,
				Mode:     0o600,
				Size:     int64(len(manifestBytes)),
				ModTime:  now,
			},
			data: manifestBytes,
		},
		{
			hdr: &tar.Header{
				Name:     "../evil",
				Typeflag: tar.TypeReg,
				Mode:     0o600,
				Size:     4,
				ModTime:  now,
			},
			data: []byte("evil"),
		},
	})

	symlinkTarGz := buildFuzzTarGz([]fuzzTarEntry{
		{
			hdr: &tar.Header{
				Name:     "manifest.json",
				Typeflag: tar.TypeReg,
				Mode:     0o600,
				Size:     int64(len(manifestBytes)),
				ModTime:  now,
			},
			data: manifestBytes,
		},
		{
			hdr: &tar.Header{
				Name:     "link",
				Typeflag: tar.TypeSymlink,
				Linkname: "/etc/passwd",
				Mode:     0o777,
				ModTime:  now,
			},
		},
	})

	truncatedTarGz := buildFuzzTruncatedTarGz(manifestBytes, now)

	f.Add([]byte{})
	f.Add([]byte("not-a-gzip-archive"))
	f.Add([]byte{0x1f, 0x8b, 0x00, 0x00})
	f.Add(validTarGz)
	f.Add(traversalTarGz)
	f.Add(symlinkTarGz)
	f.Add(truncatedTarGz)
	if len(validTarGz) > 10 {
		f.Add(validTarGz[:len(validTarGz)/2])
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		tmpPath := filepath.Join(t.TempDir(), "bundle.tar.gz")
		if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
			t.Fatalf("write fuzz bundle: %v", err)
		}
		_, _ = VerifyBackupBundle(tmpPath)
	})
}
