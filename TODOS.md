# SmallCloud Deferred Backlog (`TODOS.md`)

## Completed
- [x] **TODO-1: Streaming `io.Copy` + `io.TeeReader` in `StorageManager.SyncWAL` and `Restore`** — Implemented in `daemon/storage.go` with bounded-buffer streaming and `.sha256` verification before `os.Rename`.
- [x] **TODO-2: Automated Monthly Billing-Cycle Rollover (`BudgetTracker`)** — Implemented `BillingMonth`, `MonthlyArchive`, `RolloverIfNewMonth`, and `RolloverAll` in `daemon/budget.go`.
- [x] **TODO-3: Embedded Operator Web UI** — Serve a lightweight operator dashboard from the daemon for inspecting app health, budget burn, and recent deployments.
- [x] **TODO-4: Signed Budget Webhooks** — Deliver HMAC-SHA256 signed webhook payloads when apps cross `alert_at_pct` or trigger `hard_stop`.
- [x] **TODO-5: Local Snapshot Pruning** — Add retention policies and automated pruning for historical local WAL snapshots.

## Completed (Wave 6)
- [x] **TODO-6: Org-Level Aggregate Quotas** — Enforce org-level aggregate spend quotas with soft/hard rollup across apps.
- [x] **TODO-7: Release Artifact Signing** — Sign release artifacts and generate checksum attestations.
- [x] **TODO-8: Canary Traffic Weighting** — Support weighted traffic splitting (`X%` Blue / `Y%` Green) during deployments.

## Completed (Wave 7)
- [x] **TODO-9: Multi-Region SQLite Read Replicas** — Stream Litestream WAL snapshots to read-only edge replicas with sub-second lag bounds.
- [x] **TODO-10: WireGuard Mesh Control-Plane Tunneling** — Establish encrypted peer-to-peer tunnels between control-plane daemon and remote worker nodes.
- [x] **TODO-11: GitHub Webhook Git-Push Deployments** — Implemented HMAC-SHA256 webhook signature verification (`VerifyGitHubSignature`) and push event parsing (`ParseGitHubPushEvent`) in `daemon/gitops.go`.
