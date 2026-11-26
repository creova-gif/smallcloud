package daemon

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type EdgeReplicaTarget struct {
	Region     string `json:"region"`
	ReplicaDir string `json:"replica_dir"`
	MaxLagMs   int64  `json:"max_lag_ms"`
}

type ReplicaSyncReport struct {
	Region      string    `json:"region"`
	OrgID       string    `json:"org_id"`
	AppName     string    `json:"app_name"`
	BytesCopied int64     `json:"bytes_copied"`
	SHA256      string    `json:"sha256"`
	LagMs       int64     `json:"lag_ms"`
	SyncedAt    time.Time `json:"synced_at"`
}

type WireGuardPeer struct {
	NodeID              string   `json:"node_id"`
	PublicKey           string   `json:"public_key"`
	Endpoint            string   `json:"endpoint"`
	AllowedIPs          []string `json:"allowed_ips"`
	PersistentKeepalive int      `json:"persistent_keepalive"`
}

func FanoutSnapshot(primary *StorageManager, orgID, appName string, targets []EdgeReplicaTarget) ([]ReplicaSyncReport, error) {
	if primary == nil {
		return nil, errors.New("primary storage manager is required")
	}
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, errors.New("at least one replica target is required")
	}
	for i, target := range targets {
		if strings.TrimSpace(target.Region) == "" {
			return nil, fmt.Errorf("target %d region is required", i)
		}
		if strings.TrimSpace(target.ReplicaDir) == "" {
			return nil, fmt.Errorf("target %q replica_dir is required", target.Region)
		}
		if target.MaxLagMs < 0 {
			return nil, fmt.Errorf("target %q max_lag_ms must be >= 0", target.Region)
		}
	}

	if _, err := primary.VerifySnapshot(orgID, appName); err != nil {
		return nil, fmt.Errorf("verify primary snapshot: %w", err)
	}

	reports := make([]ReplicaSyncReport, 0, len(targets))
	for _, target := range targets {
		start := time.Now()
		dest := filepath.Join(target.ReplicaDir, orgID, appName, "replica.db")
		meta, err := primary.Restore(orgID, appName, dest)
		if err != nil {
			return nil, fmt.Errorf("fanout to region %q: %w", target.Region, err)
		}
		if err := writeFileAtomic(dest+".sha256", []byte(meta.SHA256+"\n"), 0o600); err != nil {
			return nil, fmt.Errorf("write sidecar for region %q: %w", target.Region, err)
		}
		reports = append(reports, ReplicaSyncReport{
			Region:      target.Region,
			OrgID:       orgID,
			AppName:     appName,
			BytesCopied: meta.SizeBytes,
			SHA256:      meta.SHA256,
			LagMs:       time.Since(start).Milliseconds(),
			SyncedAt:    time.Now().UTC(),
		})
	}

	return reports, nil
}

func ValidateWireGuardKey(key string) error {
	if key == "" || strings.ContainsAny(key, " \t\r\n") {
		return errors.New("wireguard key must be non-empty base64 without whitespace")
	}
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return fmt.Errorf("decode wireguard key: %w", err)
	}
	if len(decoded) != 32 {
		return fmt.Errorf("wireguard key must decode to 32 bytes, got %d", len(decoded))
	}
	return nil
}

func validateWireGuardEndpoint(endpoint string) error {
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint %q: %w", endpoint, err)
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("invalid endpoint %q: empty host", endpoint)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid endpoint %q: invalid port %q", endpoint, portStr)
	}
	return nil
}

func RenderWireGuardConfig(interfaceAddr, privateKey string, listenPort int, peers []WireGuardPeer) (string, error) {
	if _, _, err := net.ParseCIDR(interfaceAddr); err != nil {
		return "", fmt.Errorf("invalid interface address %q: %w", interfaceAddr, err)
	}
	if err := ValidateWireGuardKey(privateKey); err != nil {
		return "", fmt.Errorf("invalid private key: %w", err)
	}
	if listenPort < 1 || listenPort > 65535 {
		return "", fmt.Errorf("invalid listen port %d: must be between 1 and 65535", listenPort)
	}

	seenNodes := make(map[string]struct{}, len(peers))
	sortedPeers := make([]WireGuardPeer, len(peers))
	copy(sortedPeers, peers)

	for i, peer := range sortedPeers {
		if strings.TrimSpace(peer.NodeID) == "" {
			return "", fmt.Errorf("peer %d node_id is required", i)
		}
		if _, exists := seenNodes[peer.NodeID]; exists {
			return "", fmt.Errorf("duplicate peer node_id %q", peer.NodeID)
		}
		seenNodes[peer.NodeID] = struct{}{}

		if err := ValidateWireGuardKey(peer.PublicKey); err != nil {
			return "", fmt.Errorf("peer %q invalid public key: %w", peer.NodeID, err)
		}
		if err := validateWireGuardEndpoint(peer.Endpoint); err != nil {
			return "", fmt.Errorf("peer %q: %w", peer.NodeID, err)
		}
		if len(peer.AllowedIPs) == 0 {
			return "", fmt.Errorf("peer %q requires at least one allowed_ip", peer.NodeID)
		}
		for _, cidr := range peer.AllowedIPs {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return "", fmt.Errorf("peer %q invalid allowed_ip %q: %w", peer.NodeID, cidr, err)
			}
		}
		if peer.PersistentKeepalive < 0 {
			return "", fmt.Errorf("peer %q persistent_keepalive must be >= 0", peer.NodeID)
		}
	}

	sort.Slice(sortedPeers, func(i, j int) bool {
		return sortedPeers[i].NodeID < sortedPeers[j].NodeID
	})

	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nAddress = %s\nPrivateKey = %s\nListenPort = %d\n", interfaceAddr, privateKey, listenPort)

	for _, peer := range sortedPeers {
		fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = %s\n",
			peer.PublicKey,
			peer.Endpoint,
			strings.Join(peer.AllowedIPs, ", "),
		)
		if peer.PersistentKeepalive > 0 {
			fmt.Fprintf(&b, "PersistentKeepalive = %d\n", peer.PersistentKeepalive)
		}
	}

	return b.String(), nil
}
