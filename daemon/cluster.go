package daemon

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultWorkerMaxApps = 20

var (
	ErrNodeNotFound  = errors.New("worker node not found")
	ErrNoHealthyNode = errors.New("no healthy worker node available")
)

type WorkerNode struct {
	ID            string    `json:"id"`
	Address       string    `json:"address"`
	Region        string    `json:"region"`
	Healthy       bool      `json:"healthy"`
	MaxApps       int       `json:"max_apps"`
	ActiveApps    int       `json:"active_apps"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
}

type WorkerPool struct {
	mu    sync.RWMutex
	nodes map[string]WorkerNode
}

func NewWorkerPool() *WorkerPool {
	return &WorkerPool{
		nodes: make(map[string]WorkerNode),
	}
}

func (p *WorkerPool) RegisterNode(node WorkerNode) (WorkerNode, error) {
	if strings.TrimSpace(node.ID) == "" {
		return WorkerNode{}, errors.New("worker node id is required")
	}
	if strings.TrimSpace(node.Address) == "" {
		return WorkerNode{}, errors.New("worker node address is required")
	}
	if node.ActiveApps < 0 {
		return WorkerNode{}, errors.New("active_apps must be non-negative")
	}
	if node.MaxApps <= 0 {
		node.MaxApps = defaultWorkerMaxApps
	}
	node.Healthy = true
	node.LastHeartbeat = time.Now().UTC()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.nodes[node.ID] = node
	return node, nil
}

func (p *WorkerPool) Heartbeat(id string, activeApps int) (WorkerNode, error) {
	if strings.TrimSpace(id) == "" {
		return WorkerNode{}, errors.New("worker node id is required")
	}
	if activeApps < 0 {
		return WorkerNode{}, errors.New("active_apps must be non-negative")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	node, ok := p.nodes[id]
	if !ok {
		return WorkerNode{}, fmt.Errorf("%w: %s", ErrNodeNotFound, id)
	}
	node.ActiveApps = activeApps
	node.Healthy = true
	node.LastHeartbeat = time.Now().UTC()
	p.nodes[id] = node
	return node, nil
}

func (p *WorkerPool) ListNodes() []WorkerNode {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]WorkerNode, 0, len(p.nodes))
	for _, node := range p.nodes {
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func (p *WorkerPool) SelectNode(region string) (WorkerNode, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	region = strings.TrimSpace(region)
	var selected WorkerNode
	found := false

	for _, node := range p.nodes {
		if !node.Healthy || node.ActiveApps >= node.MaxApps {
			continue
		}
		if region != "" && node.Region != region {
			continue
		}
		if !found || node.ActiveApps < selected.ActiveApps || (node.ActiveApps == selected.ActiveApps && node.ID < selected.ID) {
			selected = node
			found = true
		}
	}

	if !found {
		return WorkerNode{}, ErrNoHealthyNode
	}
	return selected, nil
}

func (p *WorkerPool) PruneStale(maxAge time.Duration, now time.Time) int {
	if maxAge <= 0 {
		return 0
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	pruned := 0
	for id, node := range p.nodes {
		if now.Sub(node.LastHeartbeat) > maxAge {
			delete(p.nodes, id)
			pruned++
		}
	}
	return pruned
}
