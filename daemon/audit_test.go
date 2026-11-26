package daemon

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestAuditLogRingBufferEviction(t *testing.T) {
	log := NewAuditLog(3)

	for i := 1; i <= 5; i++ {
		ev := log.Record(AuditEvent{
			Type:    "deploy",
			OrgID:   "acme",
			AppName: "web",
			Detail:  fmt.Sprintf("step-%d", i),
		})
		if ev.ID != int64(i) {
			t.Fatalf("expected ID %d, got %d", i, ev.ID)
		}
		if ev.Timestamp.IsZero() {
			t.Fatalf("expected non-zero timestamp")
		}
	}

	got := log.List("", "", 0)
	if len(got) != 3 {
		t.Fatalf("expected 3 events after eviction, got %d", len(got))
	}
	wantDetails := []string{"step-5", "step-4", "step-3"}
	for i, want := range wantDetails {
		if got[i].Detail != want {
			t.Fatalf("index %d: expected %q, got %q", i, want, got[i].Detail)
		}
	}
}

func TestAuditLogFilteringAndLimit(t *testing.T) {
	log := NewAuditLog(200)

	log.Record(AuditEvent{Type: "deploy", OrgID: "acme", AppName: "api", ReleaseID: "r1"})
	log.Record(AuditEvent{Type: "deploy", OrgID: "acme", AppName: "web", ReleaseID: "r2"})
	log.Record(AuditEvent{Type: "deploy", OrgID: "beta", AppName: "api", ReleaseID: "r3"})
	log.Record(AuditEvent{Type: "rollback", OrgID: "acme", AppName: "api", ReleaseID: "r0"})

	allAcme := log.List("acme", "", 0)
	if len(allAcme) != 3 {
		t.Fatalf("expected 3 acme events, got %d", len(allAcme))
	}
	if allAcme[0].Type != "rollback" || allAcme[0].ReleaseID != "r0" {
		t.Fatalf("expected newest-first rollback r0, got %+v", allAcme[0])
	}

	acmeAPI := log.List("acme", "api", 0)
	if len(acmeAPI) != 2 {
		t.Fatalf("expected 2 acme/api events, got %d", len(acmeAPI))
	}

	limited := log.List("acme", "", 2)
	if len(limited) != 2 {
		t.Fatalf("expected 2 limited events, got %d", len(limited))
	}
	if limited[0].ReleaseID != "r0" || limited[1].ReleaseID != "r2" {
		t.Fatalf("unexpected limited order: %+v", limited)
	}

	empty := log.List("missing", "", 0)
	if len(empty) != 0 || empty == nil {
		t.Fatalf("expected non-nil empty slice, got %#v", empty)
	}
}

func TestAuditLogSnapshotAndRestoreIsolation(t *testing.T) {
	log := NewAuditLog(10)
	ts := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	log.Record(AuditEvent{Timestamp: ts, Type: "deploy", OrgID: "acme", AppName: "api", ReleaseID: "r1"})
	log.Record(AuditEvent{Timestamp: ts.Add(time.Second), Type: "delete", OrgID: "acme", AppName: "api"})

	snap := log.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 snapshot events, got %d", len(snap))
	}
	snap[0].Detail = "mutated"

	current := log.List("", "", 0)
	if current[1].Detail != "" {
		t.Fatalf("snapshot mutation leaked into audit log: %+v", current[1])
	}

	restored := NewAuditLog(10)
	restored.Restore(snap)
	snap[1].Detail = "mutated-after-restore"

	got := restored.List("", "", 0)
	if len(got) != 2 {
		t.Fatalf("expected 2 restored events, got %d", len(got))
	}
	if got[0].Detail != "" {
		t.Fatalf("restore did not isolate slice: %+v", got[0])
	}

	next := restored.Record(AuditEvent{Type: "rollover"})
	if next.ID != 3 {
		t.Fatalf("expected next ID 3 after restore, got %d", next.ID)
	}
}

func TestAuditLogConcurrency(t *testing.T) {
	log := NewAuditLog(50)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				log.Record(AuditEvent{
					Type:    "deploy",
					OrgID:   "acme",
					AppName: fmt.Sprintf("app-%d", id),
				})
				_ = log.List("acme", "", 5)
				_ = log.Snapshot()
			}
		}(g)
	}
	wg.Wait()

	if len(log.List("", "", 0)) != 50 {
		t.Fatalf("expected 50 events capped, got %d", len(log.List("", "", 0)))
	}
}
