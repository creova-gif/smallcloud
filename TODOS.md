# SmallCloud Deferred Backlog (`TODOS.md`)

## TODO-1: Streaming `io.Copy` + `io.TeeReader` in `StorageManager.SyncWAL` and `Restore`
- **What:** Replace whole-file `os.ReadFile` / `os.WriteFile` in `daemon/storage.go` with streaming `io.Copy` piped through `io.TeeReader` and `sha256.New()`.
- **Why:** Prevents heap RSS spikes when replicating SQLite databases larger than `1 GB` inside memory-constrained cgroups.
- **Pros:** Bounds replication memory usage to a 32 KiB buffer regardless of SQLite database size; eliminates OOM risk at 10x–100x tenant scale.
- **Cons:** Slightly more file-descriptor plumbing than `os.ReadFile`.
- **Context:** Surfaced in `/gstack-plan-ceo-review` Section 7 (`Performance Review`). For Wave 1–2 micro-SaaS workloads (`10–100 MB` SQLite DBs), whole-file atomic copy completes in `<50ms`. Start in `daemon/storage.go` inside `SyncWAL` and `Restore`.
- **Effort estimate:** Human: `S` (~4h) / CC+gstack: `S` (~10m)
- **Priority:** P2
- **Depends on / blocked by:** None (independent enhancement after `T1` `.sha256` sidecar verification lands).

## TODO-2: Automated Monthly Billing-Cycle Rollover Cron (`BudgetTracker`)
- **What:** Add a billing-period rollover ticker/command that archives each tenant's monthly `AppBudgetState` ledger and resets `CurrentUSD -> 0` (and unhalts `BudgetHardStop` routes) on the 1st of each calendar month UTC.
- **Why:** Automates monthly spend-cap resets for the hosted `$5/app/mo` SmallCloud service without manual operator intervention.
- **Pros:** Full multi-month lifecycle automation with immutable monthly spend receipts.
- **Cons:** Requires storing `BillingPeriod string` (`YYYY-MM`) on `AppBudgetState` in `state.json`.
- **Context:** Surfaced in `/gstack-plan-ceo-review` Section 10 (`Long-Term Trajectory Review`). Start in `daemon/budget.go` by adding `BillingMonth string` (`time.Now().UTC().Format("2006-01")`) and a `RolloverIfNewMonth` check.
- **Effort estimate:** Human: `M` (~1d) / CC+gstack: `S` (~15m)
- **Priority:** P3
- **Depends on / blocked by:** Depends on `T2` (`EXP-2` `onUnhalt` callback and `BudgetEvent` audit log) and `T3` (`state.json` persistence).
