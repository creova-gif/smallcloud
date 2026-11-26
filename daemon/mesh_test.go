package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testWGKey(seed byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = seed + byte(i)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestFanoutSnapshotMultiRegion(t *testing.T) {
	dir := t.TempDir()
	primary := NewStorageManager(filepath.Join(dir, "primary"))

	payload := bytes.Repeat([]byte("sqlite-mesh-replica-payload-"), 1024)
	sum := sha256.Sum256(payload)
	expectedHex := hex.EncodeToString(sum[:])

	srcPath := filepath.Join(dir, "host.db")
	if err := os.WriteFile(srcPath, payload, 0o600); err != nil {
		t.Fatalf("write source db: %v", err)
	}
	if _, err := primary.SyncWAL("acme", "orders", srcPath); err != nil {
		t.Fatalf("SyncWAL: %v", err)
	}

	targets := []EdgeReplicaTarget{
		{Region: "us-east", ReplicaDir: filepath.Join(dir, "regions", "us-east"), MaxLagMs: 500},
		{Region: "eu-west", ReplicaDir: filepath.Join(dir, "regions", "eu-west"), MaxLagMs: 500},
		{Region: "ap-south", ReplicaDir: filepath.Join(dir, "regions", "ap-south"), MaxLagMs: 500},
	}

	reports, err := FanoutSnapshot(primary, "acme", "orders", targets)
	if err != nil {
		t.Fatalf("FanoutSnapshot: %v", err)
	}
	if len(reports) != len(targets) {
		t.Fatalf("len(reports) = %d, want %d", len(reports), len(targets))
	}

	for i, target := range targets {
		report := reports[i]
		if report.Region != target.Region {
			t.Fatalf("report[%d].Region = %q, want %q", i, report.Region, target.Region)
		}
		if report.OrgID != "acme" || report.AppName != "orders" {
			t.Fatalf("report[%d] tenant = %s/%s, want acme/orders", i, report.OrgID, report.AppName)
		}
		if report.BytesCopied != int64(len(payload)) {
			t.Fatalf("report[%d].BytesCopied = %d, want %d", i, report.BytesCopied, len(payload))
		}
		if report.SHA256 != expectedHex {
			t.Fatalf("report[%d].SHA256 = %q, want %q", i, report.SHA256, expectedHex)
		}
		if report.LagMs < 0 {
			t.Fatalf("report[%d].LagMs = %d, want >= 0", i, report.LagMs)
		}
		if report.SyncedAt.IsZero() {
			t.Fatalf("report[%d].SyncedAt is zero", i)
		}

		replicaPath := filepath.Join(target.ReplicaDir, "acme", "orders", "replica.db")
		gotBytes, err := os.ReadFile(replicaPath)
		if err != nil {
			t.Fatalf("read replica %q: %v", replicaPath, err)
		}
		if !bytes.Equal(gotBytes, payload) {
			t.Fatalf("replica %q byte mismatch", replicaPath)
		}

		sidecarBytes, err := os.ReadFile(replicaPath + ".sha256")
		if err != nil {
			t.Fatalf("read sidecar %q: %v", replicaPath+".sha256", err)
		}
		if strings.TrimSpace(string(sidecarBytes)) != expectedHex {
			t.Fatalf("sidecar %q = %q, want %q", replicaPath+".sha256", strings.TrimSpace(string(sidecarBytes)), expectedHex)
		}
	}
}

func TestFanoutSnapshotValidationAndCorruptPrimary(t *testing.T) {
	dir := t.TempDir()
	primary := NewStorageManager(filepath.Join(dir, "primary"))
	validTarget := []EdgeReplicaTarget{{Region: "us-east", ReplicaDir: filepath.Join(dir, "us-east")}}

	if _, err := FanoutSnapshot(nil, "acme", "app1", validTarget); err == nil {
		t.Fatal("expected error for nil primary")
	}
	if _, err := FanoutSnapshot(primary, "../bad", "app1", validTarget); err == nil {
		t.Fatal("expected error for invalid orgID")
	}
	if _, err := FanoutSnapshot(primary, "acme", "bad/app", validTarget); err == nil {
		t.Fatal("expected error for invalid appName")
	}
	if _, err := FanoutSnapshot(primary, "acme", "app1", nil); err == nil {
		t.Fatal("expected error for empty targets")
	}
	if _, err := FanoutSnapshot(primary, "acme", "app1", []EdgeReplicaTarget{{Region: "", ReplicaDir: dir}}); err == nil {
		t.Fatal("expected error for empty target region")
	}
	if _, err := FanoutSnapshot(primary, "acme", "app1", []EdgeReplicaTarget{{Region: "us-east", ReplicaDir: ""}}); err == nil {
		t.Fatal("expected error for empty target replica_dir")
	}

	srcPath := filepath.Join(dir, "host.db")
	if err := os.WriteFile(srcPath, []byte("primary-bytes"), 0o600); err != nil {
		t.Fatalf("write source db: %v", err)
	}
	if _, err := primary.SyncWAL("acme", "app1", srcPath); err != nil {
		t.Fatalf("SyncWAL: %v", err)
	}
	if err := os.WriteFile(primary.replicaPath("acme", "app1")+".sha256", []byte(strings.Repeat("f", 64)+"\n"), 0o600); err != nil {
		t.Fatalf("corrupt primary sidecar: %v", err)
	}

	_, err := FanoutSnapshot(primary, "acme", "app1", validTarget)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", err)
	}
}

func TestWireGuardKeyValidation(t *testing.T) {
	if err := ValidateWireGuardKey(testWGKey(1)); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}

	invalidKeys := []string{
		"",
		"not-base64!!!",
		base64.StdEncoding.EncodeToString(make([]byte, 31)),
		base64.StdEncoding.EncodeToString(make([]byte, 33)),
		testWGKey(2) + "\n",
		" " + testWGKey(3),
	}
	for _, k := range invalidKeys {
		if err := ValidateWireGuardKey(k); err == nil {
			t.Fatalf("expected error for invalid key %q", k)
		}
	}
}

func TestWireGuardRenderConfigDeterministicAndSorted(t *testing.T) {
	priv := testWGKey(10)
	peerBKey := testWGKey(20)
	peerAKey := testWGKey(30)

	peers := []WireGuardPeer{
		{
			NodeID:              "node-b",
			PublicKey:           peerBKey,
			Endpoint:            "198.51.100.2:51820",
			AllowedIPs:          []string{"10.42.0.2/32", "fd42::2/128"},
			PersistentKeepalive: 25,
		},
		{
			NodeID:     "node-a",
			PublicKey:  peerAKey,
			Endpoint:   "[2001:db8::1]:51821",
			AllowedIPs: []string{"10.42.0.1/32"},
		},
	}

	cfg1, err := RenderWireGuardConfig("10.42.0.10/24", priv, 51820, peers)
	if err != nil {
		t.Fatalf("RenderWireGuardConfig: %v", err)
	}
	cfg2, err := RenderWireGuardConfig("10.42.0.10/24", priv, 51820, peers)
	if err != nil {
		t.Fatalf("RenderWireGuardConfig second call: %v", err)
	}
	if cfg1 != cfg2 {
		t.Fatalf("config output not deterministic:\n%s\nvs\n%s", cfg1, cfg2)
	}

	want := "[Interface]\n" +
		"Address = 10.42.0.10/24\n" +
		"PrivateKey = " + priv + "\n" +
		"ListenPort = 51820\n" +
		"\n[Peer]\n" +
		"PublicKey = " + peerAKey + "\n" +
		"Endpoint = [2001:db8::1]:51821\n" +
		"AllowedIPs = 10.42.0.1/32\n" +
		"\n[Peer]\n" +
		"PublicKey = " + peerBKey + "\n" +
		"Endpoint = 198.51.100.2:51820\n" +
		"AllowedIPs = 10.42.0.2/32, fd42::2/128\n" +
		"PersistentKeepalive = 25\n"

	if cfg1 != want {
		t.Fatalf("rendered config mismatch:\ngot:\n%s\nwant:\n%s", cfg1, want)
	}
}

func TestWireGuardRenderConfigValidationErrors(t *testing.T) {
	priv := testWGKey(1)
	validPeer := WireGuardPeer{
		NodeID:     "node-1",
		PublicKey:  testWGKey(2),
		Endpoint:   "203.0.113.10:51820",
		AllowedIPs: []string{"10.42.0.1/32"},
	}

	if _, err := RenderWireGuardConfig("10.42.0.10", priv, 51820, []WireGuardPeer{validPeer}); err == nil {
		t.Fatal("expected error for invalid interface CIDR")
	}
	if _, err := RenderWireGuardConfig("10.42.0.10/24", "bad-key", 51820, []WireGuardPeer{validPeer}); err == nil {
		t.Fatal("expected error for invalid private key")
	}
	for _, port := range []int{0, -1, 65536} {
		if _, err := RenderWireGuardConfig("10.42.0.10/24", priv, port, []WireGuardPeer{validPeer}); err == nil {
			t.Fatalf("expected error for invalid port %d", port)
		}
	}

	badPeers := [][]WireGuardPeer{
		{{NodeID: "", PublicKey: testWGKey(2), Endpoint: "203.0.113.10:51820", AllowedIPs: []string{"10.42.0.1/32"}}},
		{{NodeID: "n1", PublicKey: "short", Endpoint: "203.0.113.10:51820", AllowedIPs: []string{"10.42.0.1/32"}}},
		{{NodeID: "n1", PublicKey: testWGKey(2), Endpoint: "missing-port", AllowedIPs: []string{"10.42.0.1/32"}}},
		{{NodeID: "n1", PublicKey: testWGKey(2), Endpoint: "203.0.113.10:99999", AllowedIPs: []string{"10.42.0.1/32"}}},
		{{NodeID: "n1", PublicKey: testWGKey(2), Endpoint: "203.0.113.10:51820", AllowedIPs: nil}},
		{{NodeID: "n1", PublicKey: testWGKey(2), Endpoint: "203.0.113.10:51820", AllowedIPs: []string{"10.42.0.1"}}},
		{{NodeID: "n1", PublicKey: testWGKey(2), Endpoint: "203.0.113.10:51820", AllowedIPs: []string{"10.42.0.1/32"}, PersistentKeepalive: -1}},
		{validPeer, validPeer},
	}
	for i, bp := range badPeers {
		if _, err := RenderWireGuardConfig("10.42.0.10/24", priv, 51820, bp); err == nil {
			t.Fatalf("case %d: expected error for invalid peer config", i)
		}
	}
}
