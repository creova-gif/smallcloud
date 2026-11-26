package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creova-gif/smallcloud/pkg/config"
)

func TestLoadConcurrentControlPlaneTraffic(t *testing.T) {
	t.Setenv("SMALLCLOUD_RATE_LIMIT", "10000")

	d := newTestServerDaemon(t)
	const authToken = "load-secret"

	type tenantApp struct {
		orgID   string
		appName string
	}

	tenants := []tenantApp{
		{orgID: "org-load-1", appName: "app-load-1"},
		{orgID: "org-load-2", appName: "app-load-2"},
		{orgID: "org-load-3", appName: "app-load-3"},
		{orgID: "org-load-4", appName: "app-load-4"},
	}

	for i, ta := range tenants {
		cfg := &config.Config{
			App: config.AppConfig{
				Name:        ta.appName,
				OrgID:       ta.orgID,
				Domain:      fmt.Sprintf("%s.smallcloud.dev", ta.appName),
				Port:        8000 + i,
				Healthcheck: "/healthz",
				Runtime:     "dockerfile",
			},
			Resources: config.ResourcesConfig{
				CPUMillicores: 500,
				MemoryMB:      256,
			},
			Budget: config.BudgetConfig{
				MaxMonthlyUSD: 100.0,
				AlertAtPct:    80.0,
				HardStop:      true,
			},
			Storage: config.StorageConfig{
				SQLitePath: fmt.Sprintf("/data/%s.db", ta.appName),
				ReplicaURL: fmt.Sprintf("gs://smallcloud-backups/%s/%s", ta.orgID, ta.appName),
				SyncMs:     1000,
			},
		}
		if _, err := d.Deploy(cfg, "v1.0.0"); err != nil {
			t.Fatalf("deploy %s/%s failed: %v", ta.orgID, ta.appName, err)
		}
	}

	nodes := []WorkerNode{
		{ID: "load-node-1", Address: "10.0.10.1:8080", Region: "us-east", MaxApps: 100},
		{ID: "load-node-2", Address: "10.0.10.2:8080", Region: "us-west", MaxApps: 100},
	}
	for _, n := range nodes {
		if _, err := d.WorkerPool().RegisterNode(n); err != nil {
			t.Fatalf("register node %s failed: %v", n.ID, err)
		}
	}

	srv := httptest.NewServer(d.HandlerWithAuth(authToken))
	defer srv.Close()

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        64,
			MaxIdleConnsPerHost: 64,
		},
	}
	defer client.CloseIdleConnections()

	warmReq, err := http.NewRequest(http.MethodGet, srv.URL+"/readyz", nil)
	if err != nil {
		t.Fatalf("warmup request build failed: %v", err)
	}
	warmResp, err := client.Do(warmReq)
	if err != nil {
		t.Fatalf("warmup request failed: %v", err)
	}
	_, _ = io.Copy(io.Discard, warmResp.Body)
	_ = warmResp.Body.Close()

	const workers = 16
	const requestsPerWorker = 20

	var totalRequests atomic.Int64
	var non200Count atomic.Int64
	var requestErrors atomic.Int64

	workerLatencies := make([][]time.Duration, workers)
	var wg sync.WaitGroup
	wg.Add(workers)

	for w := 0; w < workers; w++ {
		workerID := w
		workerLatencies[workerID] = make([]time.Duration, 0, requestsPerWorker)
		go func() {
			defer wg.Done()
			for i := 0; i < requestsPerWorker; i++ {
				ta := tenants[(workerID+i)%len(tenants)]
				op := (workerID + i) % 8

				var req *http.Request
				var err error

				switch op {
				case 0:
					req, err = http.NewRequest(http.MethodGet, srv.URL+"/v1/apps", nil)
				case 1:
					req, err = http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/status?org_id=%s&app_name=%s", srv.URL, ta.orgID, ta.appName), nil)
				case 2:
					req, err = http.NewRequest(http.MethodGet, srv.URL+"/v1/events", nil)
				case 3:
					req, err = http.NewRequest(http.MethodGet, srv.URL+"/v1/nodes", nil)
				case 4:
					req, err = http.NewRequest(http.MethodGet, srv.URL+"/v1/nodes/select", nil)
				case 5:
					req, err = http.NewRequest(http.MethodGet, srv.URL+"/metrics", nil)
				case 6:
					req, err = http.NewRequest(http.MethodGet, srv.URL+"/readyz", nil)
				case 7:
					payload, _ := json.Marshal(map[string]any{
						"org_id":    ta.orgID,
						"app_name":  ta.appName,
						"delta_usd": 0.001,
					})
					req, err = http.NewRequest(http.MethodPost, srv.URL+"/v1/meter", bytes.NewReader(payload))
					if err == nil {
						req.Header.Set("Content-Type", "application/json")
					}
				}

				if err != nil {
					requestErrors.Add(1)
					continue
				}
				req.Header.Set("Authorization", "Bearer "+authToken)

				start := time.Now()
				resp, err := client.Do(req)
				elapsed := time.Since(start)

				totalRequests.Add(1)
				workerLatencies[workerID] = append(workerLatencies[workerID], elapsed)

				if err != nil {
					requestErrors.Add(1)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					non200Count.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	expectedTotal := int64(workers * requestsPerWorker)
	if got := totalRequests.Load(); got != expectedTotal {
		t.Fatalf("expected %d total requests, got %d", expectedTotal, got)
	}
	if errs := requestErrors.Load(); errs != 0 {
		t.Fatalf("expected 0 request errors, got %d", errs)
	}
	if bad := non200Count.Load(); bad != 0 {
		t.Fatalf("expected 0 non-200 responses, got %d", bad)
	}

	allLatencies := make([]time.Duration, 0, workers*requestsPerWorker)
	for _, wl := range workerLatencies {
		allLatencies = append(allLatencies, wl...)
	}
	slices.Sort(allLatencies)

	p95Idx := int(math.Ceil(0.95*float64(len(allLatencies)))) - 1
	if p95Idx < 0 {
		p95Idx = 0
	}
	p95 := allLatencies[p95Idx]
	if p95 >= 100*time.Millisecond {
		t.Fatalf("p95 latency %v exceeded 100ms threshold", p95)
	}
	t.Logf("completed %d requests, p95=%v, max=%v", len(allLatencies), p95, allLatencies[len(allLatencies)-1])
}
