package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/creova-gif/smallcloud/daemon"
)

func TestEndToEndFullProductLifecycle(t *testing.T) {
	ctx := context.Background()
	const token = "e2e-secret"

	d1Root := t.TempDir()
	d1 := daemon.NewDaemon(filepath.Join(d1Root, "cgroups"), filepath.Join(d1Root, "replicas"))
	srv1 := httptest.NewServer(d1.HandlerWithAuth(token))
	defer srv1.Close()

	client1 := NewDaemonClient(srv1.URL, token)

	for _, endpoint := range []string{"/healthz", "/readyz", "/metrics", "/ui"} {
		resp, err := srv1.Client().Get(srv1.URL + endpoint)
		if err != nil {
			t.Fatalf("GET %s failed: %v", endpoint, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", endpoint, resp.StatusCode, http.StatusOK)
		}
	}

	regNode, err := client1.RegisterNode(ctx, daemon.WorkerNode{
		ID:      "node-e2e-1",
		Address: "127.0.0.1:9101",
		Region:  "us-east-1",
		MaxApps: 10,
	})
	if err != nil {
		t.Fatalf("RegisterNode failed: %v", err)
	}
	if regNode.ID != "node-e2e-1" || !regNode.Healthy {
		t.Fatalf("unexpected registered node: %+v", regNode)
	}

	hbNode, err := client1.HeartbeatNode(ctx, "node-e2e-1", 1)
	if err != nil {
		t.Fatalf("HeartbeatNode failed: %v", err)
	}
	if hbNode.ActiveApps != 1 {
		t.Fatalf("HeartbeatNode ActiveApps = %d, want 1", hbNode.ActiveApps)
	}

	selectedNode, err := client1.SelectNode(ctx, "us-east-1")
	if err != nil {
		t.Fatalf("SelectNode failed: %v", err)
	}
	if selectedNode.ID != "node-e2e-1" {
		t.Fatalf("SelectNode ID = %q, want %q", selectedNode.ID, "node-e2e-1")
	}

	pkg, err := PackageApp(filepath.Join("..", "templates", "fastapi-sqlite"))
	if err != nil {
		t.Fatalf("PackageApp failed: %v", err)
	}
	orgID := pkg.Config.App.OrgID
	appName := pkg.Config.App.Name

	res1, err := client1.Deploy(ctx, pkg.Config, "v1.0.0")
	if err != nil {
		t.Fatalf("Deploy v1.0.0 failed: %v", err)
	}
	if res1.Route.ActiveReleaseID != "v1.0.0" {
		t.Fatalf("Deploy v1.0.0 ActiveReleaseID = %q, want %q", res1.Route.ActiveReleaseID, "v1.0.0")
	}

	res2, err := client1.Deploy(ctx, pkg.Config, "v1.1.0")
	if err != nil {
		t.Fatalf("Deploy v1.1.0 failed: %v", err)
	}
	if res2.Route.ActiveReleaseID != "v1.1.0" || res2.Route.StandbyReleaseID != "v1.0.0" {
		t.Fatalf("unexpected Deploy v1.1.0 route: %+v", res2.Route)
	}

	canaryRoute, err := client1.SetCanaryWeight(ctx, orgID, appName, 25)
	if err != nil {
		t.Fatalf("SetCanaryWeight failed: %v", err)
	}
	if canaryRoute.CanaryWeight != 25 {
		t.Fatalf("SetCanaryWeight CanaryWeight = %d, want 25", canaryRoute.CanaryWeight)
	}

	dec, err := client1.ReconcileCanary(ctx, orgID, appName, pkg.Config.App.Healthcheck, 150)
	if err != nil {
		t.Fatalf("ReconcileCanary failed: %v", err)
	}
	if dec.Healthy || dec.Action != "rolled_back" {
		t.Fatalf("expected unhealthy rollback decision, got %+v", dec)
	}

	apps, err := client1.ListApps(ctx)
	if err != nil {
		t.Fatalf("ListApps failed: %v", err)
	}
	if len(apps) != 1 || apps[0].OrgID != orgID || apps[0].AppName != appName {
		t.Fatalf("unexpected ListApps result: %+v", apps)
	}

	st, err := client1.Status(ctx, orgID, appName)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if st.Route.CanaryWeight != 0 {
		t.Fatalf("expected CanaryWeight 0 after rollback, got %d", st.Route.CanaryWeight)
	}

	events, err := client1.Events(ctx, orgID, appName, 50)
	if err != nil {
		t.Fatalf("Events failed: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected non-empty audit events")
	}

	dbPayload := []byte("SQLite format 3\x00e2e-roundtrip-payload-0123456789")
	srcDBPath := filepath.Join(t.TempDir(), "app.db")
	if err := os.WriteFile(srcDBPath, dbPayload, 0o600); err != nil {
		t.Fatalf("write source sqlite file: %v", err)
	}
	synced, err := d1.Storage().SyncWAL(orgID, appName, srcDBPath)
	if err != nil {
		t.Fatalf("SyncWAL failed: %v", err)
	}

	bundlePath := filepath.Join(t.TempDir(), "e2e-backup.tar.gz")
	if err := client1.DownloadBackup(ctx, orgID, appName, bundlePath); err != nil {
		t.Fatalf("DownloadBackup failed: %v", err)
	}

	d2Root := t.TempDir()
	d2 := daemon.NewDaemon(filepath.Join(d2Root, "cgroups"), filepath.Join(d2Root, "replicas"))
	srv2 := httptest.NewServer(d2.HandlerWithAuth(token))
	defer srv2.Close()

	client2 := NewDaemonClient(srv2.URL, token)
	manifest, err := client2.RestoreBackup(ctx, bundlePath)
	if err != nil {
		t.Fatalf("RestoreBackup failed: %v", err)
	}
	if manifest.OrgID != orgID || manifest.AppName != appName {
		t.Fatalf("unexpected restored manifest tenant: %+v", manifest)
	}

	verified, err := d2.Storage().VerifySnapshot(orgID, appName)
	if err != nil {
		t.Fatalf("d2 VerifySnapshot failed: %v", err)
	}
	if verified.SHA256 != synced.SHA256 || verified.SizeBytes != synced.SizeBytes {
		t.Fatalf("VerifySnapshot mismatch: got %+v, want %+v", verified, synced)
	}

	restoredDBPath := filepath.Join(t.TempDir(), "restored.db")
	if _, err := d2.Storage().Restore(orgID, appName, restoredDBPath); err != nil {
		t.Fatalf("d2 Restore failed: %v", err)
	}
	restoredPayload, err := os.ReadFile(restoredDBPath)
	if err != nil {
		t.Fatalf("read restored db file: %v", err)
	}
	if !bytes.Equal(restoredPayload, dbPayload) {
		t.Fatalf("restored payload mismatch: got %q, want %q", restoredPayload, dbPayload)
	}

	_, err = client1.Meter(ctx, orgID, appName, pkg.Config.Budget.MaxMonthlyUSD+1.0)
	if !errors.Is(err, daemon.ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}

	haltedStatus, err := client1.Status(ctx, orgID, appName)
	if err != nil {
		t.Fatalf("Status after budget exceeded failed: %v", err)
	}
	if !haltedStatus.Budget.Halted || !haltedStatus.Route.Halted {
		t.Fatalf("expected app to be halted after exceeding budget: %+v", haltedStatus)
	}

	now := time.Now().UTC()
	nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	rolled, err := client1.Rollover(ctx, nextMonth)
	if err != nil {
		t.Fatalf("Rollover failed: %v", err)
	}
	if rolled < 1 {
		t.Fatalf("Rollover count = %d, want >= 1", rolled)
	}

	unhaltedStatus, err := client1.Status(ctx, orgID, appName)
	if err != nil {
		t.Fatalf("Status after rollover failed: %v", err)
	}
	if unhaltedStatus.Budget.Halted || unhaltedStatus.Route.Halted {
		t.Fatalf("expected app to be unhalted after rollover: %+v", unhaltedStatus)
	}

	if err := client1.DeleteApp(ctx, orgID, appName); err != nil {
		t.Fatalf("DeleteApp failed: %v", err)
	}
	appsAfterDelete, err := client1.ListApps(ctx)
	if err != nil {
		t.Fatalf("ListApps after delete failed: %v", err)
	}
	if len(appsAfterDelete) != 0 {
		t.Fatalf("expected 0 apps after DeleteApp, got %d", len(appsAfterDelete))
	}

	if err := client1.RemoveNode(ctx, "node-e2e-1"); err != nil {
		t.Fatalf("RemoveNode failed: %v", err)
	}
	nodesAfterRemove, err := client1.ListNodes(ctx)
	if err != nil {
		t.Fatalf("ListNodes after remove failed: %v", err)
	}
	if len(nodesAfterRemove) != 0 {
		t.Fatalf("expected 0 nodes after RemoveNode, got %d", len(nodesAfterRemove))
	}
}
