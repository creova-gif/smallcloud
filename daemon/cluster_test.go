package daemon

import (
	"errors"
	"testing"
	"time"
)

func TestWorkerPoolRegisterValidationAndDefaults(t *testing.T) {
	pool := NewWorkerPool()

	if _, err := pool.RegisterNode(WorkerNode{Address: "10.0.0.1:8080"}); err == nil {
		t.Fatal("expected error for empty node id")
	}
	if _, err := pool.RegisterNode(WorkerNode{ID: "node-1"}); err == nil {
		t.Fatal("expected error for empty node address")
	}
	if _, err := pool.RegisterNode(WorkerNode{ID: "node-1", Address: "10.0.0.1:8080", ActiveApps: -1}); err == nil {
		t.Fatal("expected error for negative active_apps")
	}

	node, err := pool.RegisterNode(WorkerNode{
		ID:      "node-1",
		Address: "10.0.0.1:8080",
		Region:  "us-east",
	})
	if err != nil {
		t.Fatalf("register node failed: %v", err)
	}
	if node.MaxApps != defaultWorkerMaxApps {
		t.Fatalf("expected default MaxApps=%d, got %d", defaultWorkerMaxApps, node.MaxApps)
	}
	if !node.Healthy {
		t.Fatal("expected registered node to be healthy")
	}
	if node.LastHeartbeat.IsZero() {
		t.Fatal("expected LastHeartbeat to be set")
	}
}

func TestWorkerPoolSelectNodeLeastLoadedAndRegion(t *testing.T) {
	pool := NewWorkerPool()

	if _, err := pool.SelectNode(""); !errors.Is(err, ErrNoHealthyNode) {
		t.Fatalf("expected ErrNoHealthyNode on empty pool, got %v", err)
	}

	if _, err := pool.RegisterNode(WorkerNode{ID: "node-b", Address: "10.0.0.2:8080", Region: "us-east", MaxApps: 10, ActiveApps: 5}); err != nil {
		t.Fatalf("register node-b: %v", err)
	}
	if _, err := pool.RegisterNode(WorkerNode{ID: "node-a", Address: "10.0.0.1:8080", Region: "us-east", MaxApps: 10, ActiveApps: 2}); err != nil {
		t.Fatalf("register node-a: %v", err)
	}
	if _, err := pool.RegisterNode(WorkerNode{ID: "node-c", Address: "10.0.0.3:8080", Region: "eu-west", MaxApps: 10, ActiveApps: 1}); err != nil {
		t.Fatalf("register node-c: %v", err)
	}

	anyBest, err := pool.SelectNode("")
	if err != nil {
		t.Fatalf("select any node failed: %v", err)
	}
	if anyBest.ID != "node-c" {
		t.Fatalf("expected least-loaded node-c, got %s", anyBest.ID)
	}

	eastBest, err := pool.SelectNode("us-east")
	if err != nil {
		t.Fatalf("select us-east node failed: %v", err)
	}
	if eastBest.ID != "node-a" {
		t.Fatalf("expected us-east least-loaded node-a, got %s", eastBest.ID)
	}

	if _, err := pool.SelectNode("ap-south"); !errors.Is(err, ErrNoHealthyNode) {
		t.Fatalf("expected ErrNoHealthyNode for unmatched region, got %v", err)
	}

	nodes := pool.ListNodes()
	if len(nodes) != 3 || nodes[0].ID != "node-a" || nodes[1].ID != "node-b" || nodes[2].ID != "node-c" {
		t.Fatalf("expected nodes sorted by ID, got %+v", nodes)
	}
}

func TestWorkerPoolCapacityAndHeartbeat(t *testing.T) {
	pool := NewWorkerPool()

	if _, err := pool.Heartbeat("missing", 1); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("expected ErrNodeNotFound, got %v", err)
	}
	if _, err := pool.RegisterNode(WorkerNode{ID: "node-1", Address: "10.0.0.1:8080", Region: "us-east", MaxApps: 2, ActiveApps: 2}); err != nil {
		t.Fatalf("register node-1: %v", err)
	}
	if _, err := pool.SelectNode("us-east"); !errors.Is(err, ErrNoHealthyNode) {
		t.Fatalf("expected ErrNoHealthyNode when at capacity, got %v", err)
	}

	updated, err := pool.Heartbeat("node-1", 1)
	if err != nil {
		t.Fatalf("heartbeat failed: %v", err)
	}
	if updated.ActiveApps != 1 || !updated.Healthy {
		t.Fatalf("unexpected updated node: %+v", updated)
	}

	selected, err := pool.SelectNode("us-east")
	if err != nil {
		t.Fatalf("select after heartbeat failed: %v", err)
	}
	if selected.ID != "node-1" {
		t.Fatalf("expected node-1, got %s", selected.ID)
	}
}

func TestWorkerPoolPruneStale(t *testing.T) {
	pool := NewWorkerPool()

	if _, err := pool.RegisterNode(WorkerNode{ID: "stale-1", Address: "10.0.0.1:8080"}); err != nil {
		t.Fatalf("register stale-1: %v", err)
	}
	if _, err := pool.RegisterNode(WorkerNode{ID: "fresh-1", Address: "10.0.0.2:8080"}); err != nil {
		t.Fatalf("register fresh-1: %v", err)
	}

	now := time.Now().UTC()
	pool.mu.Lock()
	stale := pool.nodes["stale-1"]
	stale.LastHeartbeat = now.Add(-10 * time.Minute)
	pool.nodes["stale-1"] = stale
	fresh := pool.nodes["fresh-1"]
	fresh.LastHeartbeat = now.Add(-30 * time.Second)
	pool.nodes["fresh-1"] = fresh
	pool.mu.Unlock()

	pruned := pool.PruneStale(2*time.Minute, now)
	if pruned != 1 {
		t.Fatalf("expected pruned=1, got %d", pruned)
	}

	remaining := pool.ListNodes()
	if len(remaining) != 1 || remaining[0].ID != "fresh-1" {
		t.Fatalf("expected only fresh-1 remaining, got %+v", remaining)
	}
}
