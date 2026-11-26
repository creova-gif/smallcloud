package daemon

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCaddyRouterReleaseHistoryRingBufferAndRollback(t *testing.T) {
	router := NewCaddyRouter()

	for i := 1; i <= 7; i++ {
		rel := fmt.Sprintf("v%d", i)
		upstream := fmt.Sprintf("127.0.0.1:%d", 9000+i)
		entry, err := router.SwapRoute("acme", "api", "api.acme.example", rel, upstream)
		if err != nil {
			t.Fatalf("SwapRoute(%s) failed: %v", rel, err)
		}
		wantLen := i
		if wantLen > 5 {
			wantLen = 5
		}
		if len(entry.History) != wantLen {
			t.Fatalf("after swap %d expected history len %d, got %d", i, wantLen, len(entry.History))
		}
	}

	got, ok := router.GetRoute("acme", "api")
	if !ok {
		t.Fatal("expected route to exist")
	}
	if len(got.History) != 5 {
		t.Fatalf("expected 5 history entries, got %d", len(got.History))
	}
	wantReleases := []string{"v3", "v4", "v5", "v6", "v7"}
	wantColors := []ReleaseColor{ColorBlue, ColorGreen, ColorBlue, ColorGreen, ColorBlue}
	for i, rec := range got.History {
		if rec.ReleaseID != wantReleases[i] {
			t.Fatalf("history[%d] release_id = %s, want %s", i, rec.ReleaseID, wantReleases[i])
		}
		if rec.Color != wantColors[i] {
			t.Fatalf("history[%d] color = %s, want %s", i, rec.Color, wantColors[i])
		}
		wantUpstream := fmt.Sprintf("127.0.0.1:%d", 9003+i)
		if rec.Upstream != wantUpstream {
			t.Fatalf("history[%d] upstream = %s, want %s", i, rec.Upstream, wantUpstream)
		}
		if rec.DeployedAt.IsZero() {
			t.Fatalf("history[%d] deployed_at should be non-zero", i)
		}
	}

	rolled, err := router.Rollback("acme", "api")
	if err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}
	if len(rolled.History) != 5 {
		t.Fatalf("expected 5 history entries after rollback, got %d", len(rolled.History))
	}
	wantAfterRollback := []string{"v4", "v5", "v6", "v7", "v6"}
	for i, rec := range rolled.History {
		if rec.ReleaseID != wantAfterRollback[i] {
			t.Fatalf("post-rollback history[%d] release_id = %s, want %s", i, rec.ReleaseID, wantAfterRollback[i])
		}
	}
	if rolled.History[4].Color != ColorGreen || rolled.History[4].Upstream != "127.0.0.1:9006" {
		t.Fatalf("unexpected rollback history tail: %+v", rolled.History[4])
	}
}

func TestCaddyRouterHistorySliceIsolation(t *testing.T) {
	router := NewCaddyRouter()
	swapped, err := router.SwapRoute("acme", "web", "web.acme.example", "v1", "127.0.0.1:8081")
	if err != nil {
		t.Fatalf("SwapRoute v1 failed: %v", err)
	}
	swapped.History[0].ReleaseID = "mutated-swap"

	got, ok := router.GetRoute("acme", "web")
	if !ok {
		t.Fatal("expected route to exist")
	}
	if got.History[0].ReleaseID != "v1" {
		t.Fatalf("SwapRoute return value mutated internal history: got %s", got.History[0].ReleaseID)
	}
	got.History[0].ReleaseID = "mutated-get"

	gotAgain, ok := router.GetRoute("acme", "web")
	if !ok || gotAgain.History[0].ReleaseID != "v1" {
		t.Fatalf("GetRoute mutated internal history: got %+v", gotAgain.History)
	}

	snap := router.SnapshotRoutes()
	key := tenantKey("acme", "web")
	snap[key].History[0].ReleaseID = "mutated-snap"

	afterSnap, ok := router.GetRoute("acme", "web")
	if !ok || afterSnap.History[0].ReleaseID != "v1" {
		t.Fatalf("SnapshotRoutes mutated internal history: got %+v", afterSnap.History)
	}

	restoredRouter := NewCaddyRouter()
	snap[key].History[0].ReleaseID = "v1-restored"
	restoredRouter.RestoreRoutes(snap)
	snap[key].History[0].ReleaseID = "mutated-after-restore"

	restored, ok := restoredRouter.GetRoute("acme", "web")
	if !ok || restored.History[0].ReleaseID != "v1-restored" {
		t.Fatalf("RestoreRoutes did not isolate history slice: got %+v", restored.History)
	}
}

func TestRenderCaddyfileHaltedIncludesRetryAfter(t *testing.T) {
	out := RenderCaddyfile(RouteEntry{
		Domain: "halted.acme.example",
		Halted: true,
	}, "")

	if !strings.Contains(out, "header Retry-After 3600") {
		t.Fatalf("expected Retry-After 3600 header in halted Caddyfile, got:\n%s", out)
	}
	if !strings.Contains(out, `respond "402 Payment Required: SmallCloud monthly spend cap reached" 402`) {
		t.Fatalf("expected 402 respond directive in halted Caddyfile, got:\n%s", out)
	}
}

type stubApplier struct {
	err       error
	lastCfg   string
	applyCall int
}

func (s *stubApplier) ApplyConfig(_, caddyfile string) error {
	s.applyCall++
	s.lastCfg = caddyfile
	return s.err
}

func TestCaddyRouterSetCanaryWeightValidation(t *testing.T) {
	router := NewCaddyRouter()

	if _, err := router.SetCanaryWeight("acme", "api", -1); err == nil {
		t.Fatal("expected error for negative canary weight")
	}
	if _, err := router.SetCanaryWeight("acme", "api", 101); err == nil {
		t.Fatal("expected error for canary weight > 100")
	}
	if _, err := router.SetCanaryWeight("acme", "api", 20); !errors.Is(err, ErrNoRouteFound) {
		t.Fatalf("expected ErrNoRouteFound for missing route, got %v", err)
	}

	if _, err := router.SwapRoute("acme", "api", "api.acme.example", "v1", "127.0.0.1:9001"); err != nil {
		t.Fatalf("SwapRoute v1 failed: %v", err)
	}
	if _, err := router.SetCanaryWeight("acme", "api", 25); err == nil {
		t.Fatal("expected error when standby upstream is missing")
	}
}

func TestRenderCaddyfileCanaryWeight(t *testing.T) {
	base := RouteEntry{
		Domain:          "api.acme.example",
		ActiveUpstream:  "127.0.0.1:9002",
		StandbyUpstream: "127.0.0.1:9001",
		CanaryWeight:    0,
	}
	singleOut := RenderCaddyfile(base, "")
	if strings.Contains(singleOut, "weighted_round_robin") {
		t.Fatalf("expected no weighted_round_robin when CanaryWeight == 0, got:\n%s", singleOut)
	}
	if !strings.Contains(singleOut, "reverse_proxy 127.0.0.1:9002 {") {
		t.Fatalf("expected single-upstream reverse_proxy, got:\n%s", singleOut)
	}

	weighted := base
	weighted.CanaryWeight = 20
	weightedOut := RenderCaddyfile(weighted, "/readyz")
	if !strings.Contains(weightedOut, "reverse_proxy 127.0.0.1:9002 127.0.0.1:9001 {") {
		t.Fatalf("expected dual-upstream reverse_proxy, got:\n%s", weightedOut)
	}
	if !strings.Contains(weightedOut, "lb_policy weighted_round_robin 80 20") {
		t.Fatalf("expected weighted_round_robin 80 20, got:\n%s", weightedOut)
	}
	if !strings.Contains(weightedOut, "health_uri /readyz") {
		t.Fatalf("expected health_uri /readyz, got:\n%s", weightedOut)
	}
	if !strings.Contains(weightedOut, "health_interval 5s") {
		t.Fatalf("expected health_interval 5s, got:\n%s", weightedOut)
	}
}

func TestCaddyRouterSetCanaryWeightApplierRollbackAndReset(t *testing.T) {
	applier := &stubApplier{}
	router := NewCaddyRouterWithApplier(applier)

	if _, err := router.SwapRoute("acme", "api", "api.acme.example", "v1", "127.0.0.1:9001"); err != nil {
		t.Fatalf("SwapRoute v1 failed: %v", err)
	}
	if _, err := router.SwapRoute("acme", "api", "api.acme.example", "v2", "127.0.0.1:9002"); err != nil {
		t.Fatalf("SwapRoute v2 failed: %v", err)
	}

	updated, err := router.SetCanaryWeight("acme", "api", 30)
	if err != nil {
		t.Fatalf("SetCanaryWeight(30) failed: %v", err)
	}
	if updated.CanaryWeight != 30 {
		t.Fatalf("expected CanaryWeight 30, got %d", updated.CanaryWeight)
	}
	if !strings.Contains(applier.lastCfg, "lb_policy weighted_round_robin 70 30") {
		t.Fatalf("expected applied config to contain weighted_round_robin 70 30, got:\n%s", applier.lastCfg)
	}

	applier.err = errors.New("caddy reload failed")
	if _, err := router.SetCanaryWeight("acme", "api", 50); err == nil {
		t.Fatal("expected SetCanaryWeight to return applier error")
	}
	got, ok := router.GetRoute("acme", "api")
	if !ok {
		t.Fatal("expected route to exist")
	}
	if got.CanaryWeight != 30 {
		t.Fatalf("expected CanaryWeight rolled back to 30, got %d", got.CanaryWeight)
	}

	applier.err = nil
	swapped, err := router.SwapRoute("acme", "api", "api.acme.example", "v3", "127.0.0.1:9003")
	if err != nil {
		t.Fatalf("SwapRoute v3 failed: %v", err)
	}
	if swapped.CanaryWeight != 0 {
		t.Fatalf("expected CanaryWeight reset to 0 on SwapRoute, got %d", swapped.CanaryWeight)
	}

	if _, err := router.SetCanaryWeight("acme", "api", 15); err != nil {
		t.Fatalf("SetCanaryWeight(15) failed: %v", err)
	}
	rolled, err := router.Rollback("acme", "api")
	if err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}
	if rolled.CanaryWeight != 0 {
		t.Fatalf("expected CanaryWeight reset to 0 on Rollback, got %d", rolled.CanaryWeight)
	}
}
