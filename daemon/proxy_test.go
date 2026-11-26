package daemon

import (
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
