# SmallCloud Deferred Backlog (`TODOS.md`)

## Completed
- [x] **TODO-1: Streaming `io.Copy` + `io.TeeReader` in `StorageManager.SyncWAL` and `Restore`** — Implemented in `daemon/storage.go` with bounded-buffer streaming and `.sha256` verification before `os.Rename`.
- [x] **TODO-2: Automated Monthly Billing-Cycle Rollover (`BudgetTracker`)** — Implemented `BillingMonth`, `MonthlyArchive`, `RolloverIfNewMonth`, and `RolloverAll` in `daemon/budget.go`.

## Planned (Wave 5)
- [ ] **TODO-3: Embedded Operator Web UI** — Serve a lightweight operator dashboard from the daemon for inspecting app health, budget burn, and recent deployments.
- [ ] **TODO-4: Signed Budget Webhooks** — Deliver HMAC-SHA256 signed webhook payloads when apps cross `alert_at_pct` or trigger `hard_stop`.
- [ ] **TODO-5: S3/GCS Lifecycle Pruning** — Add retention policies and automated pruning for historical WAL snapshots and replica objects.
