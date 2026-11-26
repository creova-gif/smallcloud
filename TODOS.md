# SmallCloud Deferred Backlog (`TODOS.md`)

## Completed
- [x] **TODO-1: Streaming `io.Copy` + `io.TeeReader` in `StorageManager.SyncWAL` and `Restore`** — Implemented in `daemon/storage.go` with bounded-buffer streaming and `.sha256` verification before `os.Rename`.
- [x] **TODO-2: Automated Monthly Billing-Cycle Rollover (`BudgetTracker`)** — Implemented `BillingMonth`, `MonthlyArchive`, `RolloverIfNewMonth`, and `RolloverAll` in `daemon/budget.go`.
- [x] **TODO-3: Embedded Operator Web UI** — Serve a lightweight operator dashboard from the daemon for inspecting app health, budget burn, and recent deployments.
- [x] **TODO-4: Signed Budget Webhooks** — Deliver HMAC-SHA256 signed webhook payloads when apps cross `alert_at_pct` or trigger `hard_stop`.
- [x] **TODO-5: Local Snapshot Pruning** — Add retention policies and automated pruning for historical local WAL snapshots.

## Planned (Wave 6)
- [ ] **TODO-6: Org-Level Aggregate Quotas** — Enforce org-level aggregate spend quotas with soft/hard rollup across apps.
- [ ] **TODO-7: Release Artifact Signing** — Sign release artifacts and generate checksum attestations.
- [ ] **TODO-8: Canary Traffic Weighting** — Support weighted traffic splitting (`X%` Blue / `Y%` Green) during deployments.
