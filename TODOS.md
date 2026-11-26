# SmallCloud Deferred Backlog (`TODOS.md`)

## Completed
- [x] **TODO-1: Streaming `io.Copy` + `io.TeeReader` in `StorageManager.SyncWAL` and `Restore`** — Implemented in `daemon/storage.go` with bounded-buffer streaming and `.sha256` verification before `os.Rename`.
- [x] **TODO-2: Automated Monthly Billing-Cycle Rollover (`BudgetTracker`)** — Implemented `BillingMonth`, `MonthlyArchive`, `RolloverIfNewMonth`, and `RolloverAll` in `daemon/budget.go`.
