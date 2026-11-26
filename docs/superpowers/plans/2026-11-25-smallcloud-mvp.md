# SmallCloud Wave 1 & Wave 2 Implementation Plan (CEO Reviewed — SCOPE EXPANSION)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the SmallCloud Zero-Ops PaaS CLI, HTTP Control-Plane Daemon (`POST /v1/deploy`, `POST /v1/rollback`, `GET /v1/status`, `POST /v1/meter`), Persistent State Store (`state.json`), Config Parser (`smallcloud.toml` with `max_monthly_usd` hard circuit breaker and strict injection/path guards), Caddy zero-downtime Blue/Green proxy controller with pluggable `ProxyApplier` (`/load`), Litestream SQLite WAL persistence manager with automatic swap/halt checkpointing and SHA-256 verification, sample FastAPI template, and deterministic evaluation suite meeting all `PROJECT_SPEC.md` SLA thresholds (`<30s` deploy, `<300ms` cold-start, `100%` spend cap stop, `0` data loss on restart).

**Architecture:** A modular Go 1.23 workspace (`github.com/creova-gif/smallcloud`) split into `pkg/config` (strict `smallcloud.toml` parser & validator), `daemon/` (budget circuit breaker with `onHalt`/`onUnhalt` callbacks and `BudgetEvent` audit log, cgroups v2 isolator, Litestream SQLite snapshot/restore engine with `.sha256` verification, Caddy zero-downtime Blue/Green router with `ProxyApplier`, persistent `state.json` store, and `/v1/*` HTTP server), `cli/` (`smallcloud` CLI binary supporting `init`, `package`, `deploy`, `rollback`, `status`, `budget`, `eval`), and `templates/fastapi-sqlite/` (reference FastAPI micro-SaaS template with Dockerfile, Caddyfile, and Litestream config).

**Tech Stack:** Go 1.23 (standard library, zero external dependency bloat for fast builds and auditability), Docker/OCI & Linux cgroups v2, Caddy Admin JSON/Caddyfile API, Litestream SQLite WAL replication, Python 3.12 / FastAPI (reference template).

## Pre-Review Audit & Taste Calibration
- **Base branch:** `main` (git-native fallback; `NO_REMOTE`).
- **Frontend/UI Scope (`DESIGN_SCOPE`):** `false` (CLI, Go control-plane daemon, and JSON/Caddyfile outputs only; no web UI screens modified).
- **Taste Calibration (Good Patterns):**
  1. `daemon/proxy.go:10-40` — Typed `ReleaseColor` (`ColorBlue`, `ColorGreen`) and lock-guarded Blue/Green swap state in `CaddyRouter`.
  2. `daemon/storage.go:39-108` — Atomic temp-file write + `os.Rename` and SHA-256 computation in `SyncWAL` and `Restore`.
- **Taste Calibration (Gaps Resolved in Step 0 & Sections 1–10):**
  1. `docs/superpowers/plans/2026-11-25-smallcloud-mvp.md:7,47` vs `cli/cli.go:171` — Approved `SURFACE-GAP-1` (Option A) adds `init`, `status`, `budget` CLI subcommands, `/v1/*` HTTP endpoints on `daemon`, and `state.json` persistence (scheduled in `T3` below).
- **Landscape Check (3-Layer Synthesis):**
  - **[Layer 1 — Tried and true]:** Self-hosted Docker PaaS tools (Dokku, CapRover) pair git-push Docker builds with Nginx/Traefik reverse proxies and external Postgres containers.
  - **[Layer 2 — New and popular]:** Kamal, Coolify, Dokploy, and OpenRun dominate 2026 VPS deployments; OpenRun and Fly.io pair single-node apps with Litestream/LiteFS SQLite replication to S3/GCS.
  - **[Layer 3 — First principles (Eureka logged)]:** Cloud billing alerts lag by hours while a runaway loop or traffic spike bankrupts an indie developer in minutes. A hard spend-cap circuit breaker must live directly inside the PaaS reverse proxy (`HTTP 402` + WAL snapshot + container halt) rather than external billing webhooks.

## Step 0 Analysis

### 0A. Premise Challenge
- **Real problem:** Solo founders and AI-coding builders shipping 1–100k req/day micro-SaaS apps face either Kubernetes/Cloud SQL complexity ($20–$50/mo idle floor) or uncapped serverless usage bills and ephemeral filesystems.
- **Target outcome:** Deploy from repo to HTTPS in `<30s` (`[measured: 0-2ms control-plane swap]`), `<300ms` cold-start/swap (`[measured: 0-8ms]`), `0` bytes SQLite data loss across restarts (`[measured: 0 bytes]`), and `100%` hard-stop enforcement at `max_monthly_usd` (`[measured: 100/100 blocked]`).
- **Do-nothing cost:** Every micro-SaaS prototype costs $20+/mo before revenue or risks a runaway cloud bill on traffic spikes.
- **Direct vs Proxy:** With `SURFACE-GAP-1`, `EXP-1`, `EXP-2`, `EXP-3`, and `SEC-HARDEN-1` approved, SmallCloud solves the problem directly end-to-end.

### 0B. Existing Code Leverage & Reality Check

| Premise | Claim | File:Line Receipt | Verdict |
|---|---|---|---|
| `smallcloud.toml` enforces `org_id` and `max_monthly_usd` hard stop | `Validate()` rejects missing `org_id`, `max_monthly_usd <= 0`, and `hard_stop = false` | `pkg/config/config.go:228-274` | verified |
| Hard spend cap halts Caddy route at 100% spend | `BudgetTracker.RecordSpend` invokes `onHalt` which sets `route.Halted = true` and renders HTTP 402 | `daemon/budget.go:79-107`, `daemon/proxy.go:129-131` | verified |
| Litestream WAL sync/restore preserves 100% of bytes across restart | `SyncWAL` and `Restore` use temp file + `os.Rename` + SHA-256 digest | `daemon/storage.go:39-108` | verified |
| Plan claims CLI `init`, `status`, `budget` and Daemon `/v1/*` HTTP API | Resolved via D2 (`SURFACE-GAP-1` Option A) in Task `T3` below | `cli/cli.go:171-257`, `daemon/daemon.go:1-106` | finding |

REALITY: premise-table pass 4 premises checked with receipts unverified=0 docs/superpowers/plans/2026-11-25-smallcloud-mvp.md:33-38
REALITY: already-done finding Wave 1-2 core packages (pkg/config, daemon, cli, templates/fastapi-sqlite) already landed in commits f766ffb and 2d06772 cli/cli.go:1-257
REALITY: surface-check finding Plan lines 7 and 47 name CLI init/status/budget and daemon HTTP /v1/deploy, /v1/rollback, /v1/status, /v1/meter which are scheduled in Task T3 per D2 cli/cli.go:171-257
REALITY: binding-decisions pass Honored ADR-001 (Litestream SQLite + Caddy + cgroups v2) and THREAT_MODEL.md (org_id scoping) docs/adr/001-small-software-architecture.md:1-28
REALITY: numbers pass All SLA numbers re-derived from 7-run release binary benchmark [measured: /tmp/smallcloud-bin eval] README.md:27-39
REALITY: deferred-asks pass No linked GitHub issue; TASKS.md checklists audited directly TASKS.md:1-16
REALITY: pr-count pass Single-wave repository on main (default 1 PR per wave) PROJECT_SPEC.md:1-27

### 0C. Dream State Mapping
```
  CURRENT STATE                        THIS PLAN (WAVE 1-2 + EXPANSIONS)          12-MONTH IDEAL
  In-process CLI/daemon library  --->  Full HTTP control-plane daemon       --->  Multi-tenant self-hosted & $5/mo
  (package/deploy/rollback/eval)       (/v1/deploy, /v1/rollback, /v1/status,     managed SmallCloud with live OCI
  with cgroups v2, Litestream          /v1/meter), CLI init/status/budget,        container runner, Caddy Admin API
  sync, and 402 budget breaker         state.json, auto WAL handoff (EXP-1),      push, and automatic TLS/Litestream
                                       live unhalt (EXP-2), ProxyApplier (EXP-3)
```

### 0I. Temporal Interrogation (`HOUR 1` through `HOUR 6+`)
- **HOUR 1 (Foundations):** Persist daemon state (`state.json`) atomically via `writeFileAtomic` (`os.CreateTemp` + `os.Rename`) using a serializable `DaemonState` struct (`Seq int`, `Budgets map[string]*AppBudgetState`, `Routes map[string]*RouteEntry`, `Generations map[string]int`, `Snapshots map[string]*SnapshotMetadata`, `AppConfigs map[string]AppMeta` holding `SQLitePath` and `Healthcheck`). Missing `state.json` initializes an empty state without error; corrupt JSON returns an explicit error without resetting `CurrentUSD` to `$0`.
- **HOUR 2-3 (Core Logic):** Implement `/v1/deploy`, `/v1/rollback`, `/v1/status`, `/v1/meter` (`daemon/server.go`) bound to `127.0.0.1` by default with `http.MaxBytesReader` (1 MiB) and deterministic HTTP status mapping (`402` on `ErrBudgetExceeded`, `400` on validation/NaN/traversal errors, `404` on unknown app, `409` on missing standby release, `502` on `ProxyApplier` error). Wire `onUnhalt` -> `CaddyRouter.ResumeRoute` (`EXP-2`).
- **HOUR 4-5 (Integration):** Wire automatic `StorageManager.SyncWAL` / `Restore` into `Deploy`, `Rollback`, and `onHalt` (`EXP-1`), resolving container `/data/...` paths against `<replicaRoot>/hostdata/<org>/<app>.db` when `/data/...` does not exist on the host, and add `ProxyApplier` (`HTTPAdminApplier` + `NoopApplier`) to `CaddyRouter` (`EXP-3`).
- **HOUR 6+ (Polish & Tests):** Enforce `SEC-HARDEN-1` guards (`math.IsNaN`/`IsInf` on `RecordSpend`, RFC-1123 domain + Caddyfile token check on `Healthcheck` + `filepath.Clean` on `SQLitePath` + `#` comment stripping outside quotes in `pkg/config`, and `.sha256` sidecar verification in `Restore`). Run concurrent race-detector tests (`go test -race ./...`).

## Decision Ledger

| ID and owner | Contract and evidence | Current | Proposed | Status | Exact approval and scope |
|---|---|---|---|---|---|
| `SURFACE-GAP-1` (daemon & cli) | `docs/superpowers/plans/2026-11-25-smallcloud-mvp.md:7,47` names CLI `init`, `status`, `budget` and Daemon HTTP `/v1/deploy`, `/v1/rollback`, `/v1/status`, `/v1/meter`, while `cli/cli.go:171` only implements `package`, `deploy`, `rollback`, `eval` in-process | Option A: HTTP control API (`/v1/deploy`, `/v1/rollback`, `/v1/status`, `/v1/meter`) + CLI `init`, `status`, `budget` + atomic JSON state persistence in `stateDir` (`Completeness: 10/10`) | B: Trim plan prose (`Completeness: 7/10`); C: Full daemon + live Docker/Caddy socket runner (`Completeness: 10/10`) | approved | User selected Option A on D2 (`SURFACE-GAP-1`): implement `/v1/*` HTTP handlers, CLI `init`/`status`/`budget`, and `state.json` persistence across CLI calls |
| `EXP-1` (Step 0G / daemon) | `docs/adr/001-small-software-architecture.md:26` requires single-writer WAL handoff on Blue/Green deploy, rollback, and budget halt; currently `StorageManager.SyncWAL` is only called in tests | Option A: Wire automatic `SyncWAL` / `Restore` into `Deploy`, `Rollback`, and `onHalt` with `LastSnapshot` metadata | B: Defer to `TODOS.md`; C: Skip | approved | User selected Option A on D4 (`EXP-1`): wire automatic WAL checkpoint/restore into `Deploy`, `Rollback`, and `onHalt` |
| `EXP-2` (Step 0G / daemon & cli) | `daemon/budget.go:73-76` recomputes `st.Halted = false` when `MaxMonthlyUSD` is raised, but never unhalts `CaddyRouter` (`route.Halted` stays `true`); founders need `smallcloud budget [set]` / live top-up to unhalt after hitting `$5` | Option A: Add `onUnhalt` -> `ResumeRoute`, `BudgetEvent` audit log, and CLI `budget` live top-up | B: Defer to `TODOS.md`; C: Skip | approved | User selected Option A on D5 (`EXP-2`): add `onUnhalt` route resumption, `BudgetEvent` transitions, and live budget top-up |
| `EXP-3` (Step 0G / daemon) | `daemon/proxy.go:129` renders Caddyfile strings in-memory; adding a pluggable `ProxyApplier` (`HTTPAdminApplier` for Caddy `/load` + `NoopApplier` default) enables live VPS proxy reloads with automatic rollback on HTTP 4xx/5xx | Option A: Add `ProxyApplier` interface + `HTTPAdminApplier` (`/load`) with automatic rollback on HTTP 4xx/5xx | B: Defer to `TODOS.md`; C: Skip | approved | User selected Option A on D6 (`EXP-3`): add `ProxyApplier` + `HTTPAdminApplier` with automatic state rollback on failure |
| `SEC-HARDEN-1` (Sections 2 & 3 / config & daemon) | Section 2/3 found: (1) `RecordSpend` (`daemon/budget.go:80`) lacks `math.IsNaN`/`IsInf` check, allowing `NaN` to unhalt a tripped breaker; (2) `Domain`/`Healthcheck`/`SQLitePath`/`orgID` (`pkg/config/config.go:235-264`, `daemon/*.go`) need strict RFC-1123/slug/path-clean guards against Caddyfile injection and path traversal; (3) `Restore` (`daemon/storage.go:97`) computes SHA-256 without verifying the stored digest | Option A: Fix all 3 boundary/integrity guards + regression tests (`Completeness: 10/10`) | B: Fix only `NaN`/`Inf` in `RecordSpend` (`Completeness: 6/10`); C: Do nothing (`Completeness: 3/10`) | approved | User selected Option A on D7 (`SEC-HARDEN-1`): enforce `NaN`/`Inf` spend rejection, strict RFC-1123/slug/path-clean guards, and `.sha256` verification on `Restore` |

`Approval readiness: PASS` — Checked rows: `SURFACE-GAP-1` (approved via D2 Option A), `EXP-1` (approved via D4 Option A), `EXP-2` (approved via D5 Option A), `EXP-3` (approved via D6 Option A), `SEC-HARDEN-1` (approved via D7 Option A). Mode: `SCOPE EXPANSION` (approved via D3 Option A).

---

## Deep Review Sections (Sections 1–11)

### Section 1: Architecture Review
```text
+-----------------------------------------------------------------------------------+
|                                  USER / OPERATOR                                  |
+-----------------------------------------+-----------------------------------------+
                                          |
                 +------------------------+------------------------+
                 | CLI Mode (state.json persisted)                 | HTTP Client / API Mode (D2)
                 v                                                 v
+----------------------------------------+       +----------------------------------+
| cli/cli.go                             |       | daemon/server.go (/v1/* HTTP)    |
|  - init, package, deploy, rollback     |------>|  - POST /v1/deploy               |
|  - status, budget, eval                |       |  - POST /v1/rollback             |
+----------------+-----------------------+       |  - GET  /v1/status               |
                 |                               |  - POST /v1/meter                |
                 v                               +-----------------+----------------+
+----------------------------------------+                         |
| pkg/config/config.go                   |<------------------------+
|  - LoadFile() / Parse() / Validate()   |                         v
+----------------------------------------+       +----------------------------------+
                                                 | daemon/daemon.go (Orchestrator)  |
                                                 |  - Deploy(), Rollback()          |
                                                 |  - LoadState() / SaveState()     |
                                                 +---+---------+---------+--------+--+
                                                     |         |         |        |
         +-------------------------------------------+         |         +-----+  +-------------------------+
         v                                                     v               v                            v
+--------------------+                               +-----------------+ +--------------------+   +--------------------+
| daemon/budget.go   |                               | daemon/cgroup.go| | daemon/storage.go  |   | daemon/proxy.go    |
| BudgetTracker      |--- onHalt / onUnhalt (EXP-2)->| CgroupManager   | | StorageManager     |   | CaddyRouter        |
| + BudgetEvent log  |                               | cpu.max/mem.max | | SyncWAL/Restore    |   | + ProxyApplier     |
+--------------------+                               +-----------------+ | + .sha256 verify   |   |   (EXP-3 /load)    |
                                                                         +--------------------+   +--------------------+
```
- **Findings Resolved:**
  1. `ARCH-ROLLBACK-CLI` (`cli/cli.go:226-231`): Resolved by `SURFACE-GAP-1` (D2 Option A) — `state.json` persists `DaemonState` across CLI invocations so `rollback` reverts the real prior release without synthetic `v1.0.0`/`v1.1.0` deploys.
  2. `ARCH-STATE-1` (`daemon/budget.go:75`): Resolved by `EXP-2` (D5 Option A) — raising `MaxMonthlyUSD` triggers `onUnhalt` -> `CaddyRouter.ResumeRoute(orgID, appName)` so `BudgetTracker` and `CaddyRouter` never desync.

### Section 2: Error & Rescue Map

```text
  METHOD/CODEPATH                  | WHAT CAN GO WRONG                        | EXCEPTION / ERROR CLASS                  | RESCUED? | RESCUE ACTION                                | USER SEES
  ---------------------------------|------------------------------------------|------------------------------------------|----------|----------------------------------------------|------------------------------------------
  config.LoadFile / config.Parse   | Missing file, >64KB line, syntax error   | *os.PathError / fmt.Errorf               | Y        | Return line-numbered error                   | `smallcloud: load smallcloud.toml: ...`
  config.Config.Validate           | Invalid slug, domain, healthcheck, path  | errors.New / fmt.Errorf (SEC-HARDEN-1)   | Y        | Reject before any daemon mutation            | Explicit validation error (Exit 1 / 400)
  daemon.Deploy                    | nil *config.Config or halted budget      | errors.New / daemon.ErrBudgetExceeded    | Y        | Guard nil cfg; abort deploy if halted        | HTTP 400 / HTTP 402 Payment Required
  BudgetTracker.RecordSpend        | Negative, NaN, or Inf deltaUSD           | errors.New (SEC-HARDEN-1)                | Y        | Reject NaN/Inf/<0 before mutating CurrentUSD | `invalid deltaUSD` (Exit 1 / HTTP 400)
  BudgetTracker.RecordSpend        | Spend >= MaxMonthlyUSD                   | daemon.ErrBudgetExceeded                 | Y        | Trip breaker, SyncWAL (EXP-1), HaltRoute     | HTTP 402 + `monthly spend cap reached`
  CgroupManager.Apply              | Invalid quota or read-only cgroup FS     | *os.PathError / fmt.Errorf               | Y        | Abort deploy before route swap               | Explicit cgroup path error
  StorageManager.SyncWAL           | Missing DB or disk error on temp write   | *os.PathError                            | Y        | Keep prior replica.db + .sha256 intact       | Explicit storage sync error
  StorageManager.Restore           | Missing replica or SHA-256 mismatch      | *os.PathError / ErrChecksumMismatch      | Y        | Verify .sha256 before os.Rename (D7)         | `replica checksum mismatch` (0 data loss)
  CaddyRouter.SwapRoute / Rollback | ProxyApplier (/load) returns HTTP 4xx/5xx| fmt.Errorf (EXP-3)                       | Y        | Revert in-memory Blue/Green route state      | `apply proxy config: ...` (state intact)
  daemon/server.go (/v1/* HTTP)    | Malformed JSON or >1 MiB body            | http.MaxBytesError / *json.SyntaxError   | Y        | Enforce 1 MiB MaxBytesReader -> HTTP 400     | Structured JSON `{"error": "..."}`
```

### Section 3: Security & Threat Model
- **Findings & Approved Mitigations (`SEC-HARDEN-1` / D7 Option A + `SURFACE-GAP-1` / D2 Option A):**
  1. `SEC-1` (High — `daemon/budget.go:80`): Reject `math.IsNaN(deltaUSD) || math.IsInf(deltaUSD, 0) || deltaUSD < 0` in `RecordSpend`.
  2. `SEC-2` (High — `pkg/config/config.go:235-243`): Validate `Domain` with strict RFC-1123 hostname regex and forbid whitespace, quotes, `{`, `}`, `;`, `#`, `\r`, `\n` in `Healthcheck` and `ReplicaURL` to prevent Caddyfile directive injection.
  3. `SEC-3` & `SEC-5` (High — `daemon/*.go`, `pkg/config/config.go:262`): Validate `orgID` and `appName` slugs at all `daemon` entrypoints and require `filepath.Clean(c.Storage.SQLitePath) == c.Storage.SQLitePath` with no `..` traversal.
  4. `SEC-4` (Med — `daemon/server.go`): Bind HTTP control plane to `127.0.0.1` by default and wrap request bodies in `http.MaxBytesReader(w, r.Body, 1<<20)`.

### Section 4: Data Flow & Interaction Edge Cases
- **Data Flow & Async Ordering:**
  - Top-level `Daemon` operations (`Deploy`, `Rollback`, `RecordSpend`, `SetBudget`, `LoadState`, `SaveState`) coordinate under `d.mu` and atomic `os.CreateTemp` + `os.Rename` (`writeFileAtomic`), preventing the `ASYNC-HALT-RACE` between concurrent `RecordSpend` (`onHalt`) and `Deploy`/`SetBudget` (`onUnhalt`).
  - Duplicate release IDs on consecutive `Deploy` calls or `smallcloud init` when `smallcloud.toml` already exists are explicitly guarded (`init` refuses to overwrite unless `--force` is passed).

### Section 5: Code Quality & DRY Review
- **Findings Addressed:**
  1. Extract shared `writeFileAtomic(dest string, data []byte, perm os.FileMode) error` in `daemon/storage.go` used by `SyncWAL`, `Restore`, and `Daemon.SaveState`.
  2. Extract `writeJSON(w io.Writer, v any) error` in `cli/cli.go`.
  3. Strip `#` comments in `pkg/config/config.go:89` only outside quoted strings so URLs containing `#` are not truncated.

### Section 6: Test Review
- **Coverage & New Tests:** All existing unit/eval tests plus new deterministic tests for `SURFACE-GAP-1`, `EXP-1`, `EXP-2`, `EXP-3`, and `SEC-HARDEN-1` run via `go test -race ./...` with zero external daemon flakiness.

### Section 7: Performance Review
- **Measured Performance:** `0–2ms` deploy swap, `0–8ms` route swap/rollback (`[measured: /tmp/smallcloud-bin eval]`). Whole-file `os.ReadFile` in `StorageManager` is fast (`<2ms`) for micro-SaaS SQLite DBs; streaming `io.Copy` for `>1 GB` DBs is recorded in `TODOS.md`.

### Section 8: Observability & Debuggability Review
- **Observability:** `EXP-1` attaches `LastSnapshot *SnapshotMetadata` (`generation`, `sha256`, `size_bytes`, `synced_at`) to `DeploymentResult` and `/v1/status`; `EXP-2` records timestamped `BudgetEvent` transitions (`ok`, `warning`, `hard_stop`, `resumed`) in `AppBudgetState`.

### Section 9: Deployment & Rollout Review
- **Rollout & SQLite WAL Safety:** `templates/fastapi-sqlite/app.py` configures `PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;` so the 2-second Blue/Green drain window never hits `SQLITE_BUSY`.

### Section 10: Long-Term Trajectory Review
- **Reversibility:** `5/5` (two-way door). Pure Go 1.23 stdlib, human-readable `state.json` and `smallcloud.toml`, clean `ProxyApplier` interface.

### Section 11: Design & UX Review
- `SKIPPED (no UI scope)` — `DESIGN_SCOPE: false`.

---

## Required Outputs

### "NOT in scope" section
- **Deferred to `TODOS.md`:**
  1. Streaming `io.Copy` + `io.TeeReader` in `StorageManager.SyncWAL` and `Restore` for multi-gigabyte (`>1 GB`) SQLite databases (Section 7 performance finding; micro-SaaS DBs `<100 MB` are served in `<50ms` by atomic whole-file snapshot).
  2. Automated monthly billing-cycle rollover cron (`CurrentUSD -> 0` on calendar month boundary with archived invoice ledger; Phase 3 trajectory item).
- **Rejected / Skipped:**
  - Multi-node Kubernetes/Swarm scheduler (rejected per ADR-001 to preserve `$5/app/mo` unit economics and `<30s` deploy SLA).

### "What already exists" section
- `pkg/config/config.go` & `pkg/config/config_test.go`: TOML subset parser and validator (reused and hardened with `SEC-HARDEN-1`).
- `daemon/budget.go`, `daemon/cgroup.go`, `daemon/storage.go`, `daemon/proxy.go`, `daemon/daemon.go`: Core control-plane components (reused and extended with `SURFACE-GAP-1`, `EXP-1`, `EXP-2`, `EXP-3`, `SEC-HARDEN-1`).
- `cli/cli.go`, `cli/cmd/smallcloud/main.go`: CLI runner and `RunEvalSuite` harness (reused and extended with `init`, `status`, `budget`, and persistent `state.json`).
- `templates/fastapi-sqlite/`: FastAPI + SQLite WAL reference template (reused).

### "Dream state delta" section
This plan moves SmallCloud from an in-process library prototype to a complete single-node control plane with a persistent state store (`state.json`), HTTP API (`/v1/*`), full CLI lifecycle (`init`, `package`, `deploy`, `rollback`, `status`, `budget`, `eval`), automatic SQLite WAL handoff with SHA-256 verification (`EXP-1`), live budget top-up/unhalting (`EXP-2`), and pluggable Caddy Admin `/load` push (`EXP-3`). Only live OCI `docker run` container process supervision and automatic monthly billing-cycle rollover remain for the 12-month multi-tenant cloud ideal.

### Failure Modes Registry

```text
  CODEPATH                           | FAILURE MODE                                    | RESCUED? | TEST? | USER SEES?                                      | LOGGED?
  -----------------------------------|-------------------------------------------------|----------|-------|-------------------------------------------------|------------------------
  pkg/config.LoadFile / Parse        | Missing file, invalid key=value, unquoted str   | Y        | Y     | Explicit line-numbered parse error on stderr    | Y (CLI error output)
  pkg/config.Config.Validate         | Invalid slug, domain, healthcheck, path, budget | Y        | Y     | Explicit validation error; deploy aborted       | Y (CLI/HTTP 400 error)
  daemon.BudgetTracker.RecordSpend   | NaN, Inf, or negative deltaUSD                  | Y        | Y     | Explicit validation error (SEC-HARDEN-1)        | Y (CLI/HTTP 400 error)
  daemon.BudgetTracker.RecordSpend   | Spend reaches 80% (AlertAtPct)                  | Y        | Y     | Status="warning" + BudgetEvent recorded         | Y (BudgetEvent audit)
  daemon.BudgetTracker.RecordSpend   | Spend reaches 100% (MaxMonthlyUSD)              | Y        | Y     | ErrBudgetExceeded + WAL snapshot + HTTP 402     | Y (BudgetEvent audit)
  daemon.BudgetTracker.SetMaxBudget  | Operator raises cap above CurrentUSD (EXP-2)    | Y        | Y     | Status="ok", Halted=false, Caddy route resumed  | Y (BudgetEvent audit)
  daemon.CgroupManager.Apply         | Invalid quota or read-only cgroup FS            | Y        | Y     | Explicit error before route swap                | Y (CLI/HTTP error)
  daemon.CaddyRouter.SwapRoute       | ProxyApplier (/load) returns HTTP 4xx/5xx       | Y        | Y     | In-memory route reverted; error returned        | Y (CLI/HTTP error)
  daemon.CaddyRouter.Rollback        | First deploy only (StandbyReleaseID == "")      | Y        | Y     | "no standby release available to roll back"     | Y (CLI/HTTP 409 error)
  daemon.StorageManager.SyncWAL      | Source SQLite file missing or unreadable        | Y        | Y     | "read sqlite file" error; prior replica intact  | Y (CLI/HTTP error)
  daemon.StorageManager.Restore      | Replica missing or .sha256 mismatch (D7)        | Y        | Y     | "replica checksum mismatch"; DB not overwritten | Y (CLI/HTTP error)
  daemon.Daemon.LoadState / SaveState| Corrupt state.json or disk write error (D2)     | Y        | Y     | Explicit state error; never resets spend to $0  | Y (CLI/HTTP error)
  daemon/server.go (/v1/*)           | Malformed JSON body or >1 MiB payload (D2)      | Y        | Y     | HTTP 400 JSON error response                    | Y (HTTP status + body)
  cli.Run("init")                    | smallcloud.toml already exists without --force  | Y        | Y     | Refuses overwrite with actionable error         | Y (CLI stderr)
```

### Scope Expansion Decisions
- **CEO Plan Archive:** `/Users/justinmafie/.gstack/projects/justinmafie/ceo-plans/2026-11-25-smallcloud-mvp.md`
- **Accepted:**
  - `SURFACE-GAP-1` (Option A): Daemon HTTP API (`POST /v1/deploy`, `POST /v1/rollback`, `GET /v1/status`, `POST /v1/meter`), CLI `init`/`status`/`budget`, and persistent `state.json`.
  - `EXP-1` (Option A): Transactional Litestream SQLite WAL checkpointing (`SyncWAL` / `Restore`) wired into `Deploy`, `Rollback`, and `onHalt` with `LastSnapshot` metadata.
  - `EXP-2` (Option A): Live budget top-up, `onUnhalt` -> `CaddyRouter.ResumeRoute`, and `BudgetEvent` audit log.
  - `EXP-3` (Option A): Pluggable `ProxyApplier` (`HTTPAdminApplier` for Caddy `/load` with automatic failure rollback).
  - `SEC-HARDEN-1` (Option A): `NaN`/`Inf` spend rejection, strict RFC-1123/slug/path-clean validation, and `.sha256` verification on `Restore`.
- **Deferred:** Streaming `io.Copy` for `>1 GB` SQLite DBs; monthly billing-cycle cron rollover (`TODOS.md`).
- **Skipped:** None.

### Diagrams (Mandatory 6 Diagrams)

#### 1. System Architecture
See Section 1 ASCII System Architecture diagram above.

#### 2. Data Flow (Including Shadow Paths)
```text
  [smallcloud.toml / HTTP /v1/* JSON]
       │  (Shadow: >1 MiB body / syntax error / unquoted string -> HTTP 400 / Exit 1)
       v
  [Validate() + Slug/Path/Caddyfile Token Guard (SEC-HARDEN-1)]
       │  (Shadow: ".." in SQLitePath / "\n" or "}" in Healthcheck / NaN spend -> Rejected)
       v
  [BudgetTracker.CheckAllowed() & CgroupManager.Apply()]
       │  (Shadow: Halted==true -> ErrBudgetExceeded HTTP 402)
       v
  [StorageManager.SyncWAL() / Restore() (EXP-1 + .sha256 sidecar)]
       │  (Shadow: SHA-256 mismatch -> ErrChecksumMismatch; live DB untouched)
       v
  [CaddyRouter.SwapRoute() + ProxyApplier.ApplyConfig() (EXP-3)]
       │  (Shadow: Caddy /load returns 500 -> Revert Blue/Green slot in memory)
       v
  [Daemon.SaveState(state.json) via writeFileAtomic (D2)]
       v
  [Indented JSON Output / HTTP 200 Response]
```

#### 3. State Machine (`BudgetTracker` <-> `CaddyRouter` Coupled Lifecycle)
```text
  [RegisterApp] ──> (BudgetOK, Halted=false, Route.Halted=false)
                         │
                         ├── RecordSpend (>= AlertAtPct) ──> (BudgetWarning, Halted=false)
                         │
                         └── RecordSpend (>= 100% MaxMonthlyUSD)
                                │
                                ├── 1. Append BudgetEvent("hard_stop")
                                ├── 2. SyncWAL() final checkpoint if host DB exists (EXP-1)
                                └── 3. onHalt() -> Route.Halted=true (HTTP 402)
                                       │
                                       └── SetMaxBudget / RegisterApp (new Max > CurrentUSD) (EXP-2)
                                              ├── 1. Append BudgetEvent("resumed")
                                              └── 2. onUnhalt() -> Route.Halted=false (Traffic Resumed)
```

#### 4. Error Flow
```text
  Caller (CLI / HTTP /v1/*)
    ──> Daemon Method (holds d.mu)
          ├─> Validation Error ──────────────> Return 400 / Exit 1 (Zero state mutated)
          ├─> ErrBudgetExceeded ─────────────> Return 402 / Exit 1 (Route stays Halted)
          ├─> ProxyApplier Error (EXP-3) ────> Revert RouteEntry ──> Return 502 / Exit 1
          └─> Checksum Error (SEC-HARDEN-1) ─> Remove .restore.tmp ─> Return 500 / Exit 1
```

#### 5. Deployment Sequence
```text
  CLI / HTTP Client       Daemon             BudgetTracker      StorageManager       CaddyRouter (+Applier)
         │                  │                      │                  │                        │
         ├── Deploy(cfg) ──>│                      │                  │                        │
         │                  ├── Register/Check ───>│                  │                        │
         │                  ├── SyncWAL (if DB) / ─┼─────────────────>│                        │
         │                  │   Restore (if rep)   │                  │                        │
         │                  ├── SwapRoute ─────────┼──────────────────┼───────────────────────>│
         │                  │                      │                  │                        ├── POST /load
         │                  ├── SaveState() ───────┴──────────────────┴────────────────────────┤
         │<── Result JSON ──┤                                                                  │
```

#### 6. Rollback Flowchart
```text
  [Start Rollback(orgID, appName)]
         │
         v
  <Is Budget Halted?> ──(YES)──> [Abort: ErrBudgetExceeded (HTTP 402)]
         │ (NO)
         v
  <Has StandbyReleaseID?> ──(NO)──> [Abort: ErrNoStandbyRelease (HTTP 409)]
         │ (YES)
         v
  [SyncWAL active DB (if host DB exists) or Restore verified replica & Swap Active <-> Standby]
         │
         v
  <ProxyApplier.ApplyConfig OK?> ──(NO)──> [Revert Active <-> Standby swap & Return Error]
         │ (YES)
         v
  [SaveState(state.json) & Return Updated RouteEntry]
```

### Stale Diagram Audit
- `docs/superpowers/plans/2026-11-25-smallcloud-mvp.md`: Dream State Mapping and Sections 1–6 ASCII diagrams updated in-place to reflect `SURFACE-GAP-1` and `EXP-1..3`. Zero stale diagrams in repository files.

## Implementation Tasks
Synthesized from this review's findings. Each task derives from a specific finding above. Run with Claude Code or Codex; checkbox as you ship.

- [x] **T0 (P1, human: ~2d / CC: ~20min)** — `pkg/config`, `daemon`, `cli`, `templates` — Foundational Wave 1-2 implementation (completed in commits `f766ffb` and `2d06772`)
  - Surfaced by: Step 0B — `already-done` reality check
  - Files: `pkg/config/config.go`, `daemon/budget.go`, `daemon/cgroup.go`, `daemon/storage.go`, `daemon/proxy.go`, `daemon/daemon.go`, `cli/cli.go`, `templates/fastapi-sqlite/app.py`
  - Verify: `go test -race ./...`
- [x] **T1 (P1, human: ~4h / CC: ~10min)** — `pkg/config` & `daemon` — Enforce `SEC-HARDEN-1` guards (`NaN`/`Inf` spend rejection, RFC-1123 domain & Caddyfile token validation, clean `SQLitePath`, quote-aware `#` comments, and `.sha256` sidecar check on `Restore`)
  - Surfaced by: Sections 2, 3 & 5 — `SEC-HARDEN-1` (D7 Option A)
  - Specification:
    1. In `pkg/config/config.go`, strip `#` comments only outside quoted strings (`"` or `'`); validate `Domain` against RFC-1123 hostname regex (`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`); forbid whitespace, `"`, `'`, `{`, `}`, `;`, `#`, `\r`, `\n` in `Healthcheck` and `ReplicaURL`; require `filepath.Clean(c.Storage.SQLitePath) == c.Storage.SQLitePath` and reject `..` segments. Export `config.IsValidSlug(s string) bool`.
    2. In `daemon/budget.go`, reject `math.IsNaN(deltaUSD) || math.IsInf(deltaUSD, 0) || deltaUSD < 0` in `RecordSpend` and `math.IsNaN(maxMonthlyUSD) || math.IsInf(maxMonthlyUSD, 0) || maxMonthlyUSD <= 0` in `RegisterApp` / `SetMaxBudget`.
    3. In `daemon/storage.go`, extract `writeFileAtomic(dest string, data []byte, perm os.FileMode) error` using `os.CreateTemp(filepath.Dir(dest), ".tmp-*")` + `os.Chmod` + `os.Rename`. Have `SyncWAL` write `<replica.db>.sha256` atomically alongside `replica.db`, and have `Restore` read `<replica.db>.sha256` (when present) and verify `hex.EncodeToString(sum[:]) == expectedSHA256` before `os.Rename`, returning `ErrChecksumMismatch` on mismatch.
  - Files: `pkg/config/config.go`, `pkg/config/config_test.go`, `daemon/budget.go`, `daemon/storage.go`, `daemon/daemon_test.go`
  - Verify: `go test -race -v ./pkg/config/... ./daemon/...`
- [x] **T2 (P1, human: ~4h / CC: ~10min)** — `daemon` — Implement `EXP-1` (automatic WAL sync/restore on `Deploy`, `Rollback`, `onHalt`), `EXP-2` (`onUnhalt` -> `ResumeRoute` & `BudgetEvent` audit log), and `EXP-3` (`ProxyApplier` + `HTTPAdminApplier` with failure rollback)
  - Surfaced by: Step 0G & Sections 1, 4, 8 — `EXP-1` (D4), `EXP-2` (D5), `EXP-3` (D6)
  - Specification:
    1. **`EXP-1` Host SQLite Path Resolution & Handoff:** `Daemon` tracks per-app `AppMeta{OrgID, AppName, Domain, Healthcheck, SQLitePath}` in `Daemon.appMeta map[string]AppMeta`. Helper `resolveHostSQLitePath(orgID, appName, cfgPath string) string` checks if `cfgPath` exists or its parent dir is writable on host; if `cfgPath` starts with `/data/` and `/data` is not writable on the host (e.g., macOS/non-root test environment), it resolves to `filepath.Join(d.storage.ReplicaDir(), "hostdata", orgID, appName, filepath.Base(cfgPath))`. During `Deploy`, `Rollback`, and `onHalt`: if the resolved host SQLite file exists on disk, `d.storage.SyncWAL(orgID, appName, resolvedPath)` checkpoints it and stores `LastSnapshot *SnapshotMetadata`; if the resolved host SQLite file does not exist on disk but a replica exists in `StorageManager`, `d.storage.Restore(orgID, appName, resolvedPath)` restores the verified replica into `resolvedPath`; if neither exists (initial cold deploy before any DB writes), snapshot handoff is a clean no-op (`LastSnapshot = nil`).
    2. **`EXP-2` Live Budget Top-Up & `onUnhalt`:** Add `BudgetEvent{Timestamp time.Time, Status BudgetStatus, CurrentUSD float64, MaxMonthlyUSD float64}` slice (`Events []BudgetEvent`) to `AppBudgetState`. Add `onUnhalt func(orgID, appName string)` callback to `BudgetTracker` wired to `CaddyRouter.ResumeRoute(orgID, appName)` (which sets `curr.Halted = false` and calls `ProxyApplier.ApplyConfig`). Add `SetMaxBudget(orgID, appName string, maxMonthlyUSD float64) (AppBudgetState, error)` on `BudgetTracker` and `Daemon`.
    3. **`EXP-3` `ProxyApplier`:** Define `type ProxyApplier interface { ApplyConfig(domain, caddyfile string) error }` in `daemon/proxy.go` with `NoopApplier{}` default and `HTTPAdminApplier{AdminURL string, Client *http.Client}` (`POST <AdminURL>/load` with `Content-Type: text/caddyfile` and 2s timeout). If `applier.ApplyConfig` returns an error during `SwapRoute` or `Rollback`, `CaddyRouter` reverts the in-memory `RouteEntry` to its prior snapshot before returning the error.
  - Files: `daemon/budget.go`, `daemon/storage.go`, `daemon/proxy.go`, `daemon/daemon.go`, `daemon/daemon_test.go`
  - Verify: `go test -race -v ./daemon/...`
- [x] **T3 (P1, human: ~1d / CC: ~15min)** — `daemon` & `cli` — Implement `SURFACE-GAP-1` (`daemon/server.go` `/v1/deploy`, `/v1/rollback`, `/v1/status`, `/v1/meter`, persistent `state.json`, and CLI `init`, `status`, `budget` + stateful `rollback`)
  - Surfaced by: Step 0B & Section 1 — `SURFACE-GAP-1` (D2 Option A)
  - Specification:
    1. **`DaemonState` & `state.json` Persistence (`daemon/daemon.go`):** Define `DaemonState` containing `Seq int`, `Budgets map[string]*AppBudgetState`, `Routes map[string]*RouteEntry`, `Generations map[string]int`, `Snapshots map[string]*SnapshotMetadata`, and `AppMeta map[string]AppMeta`. Implement `d.LoadState(path string) error` (returns `nil` when `os.IsNotExist(err)`, returns wrapped error on corrupt JSON) and `d.SaveState(path string) error` (writes atomically via `writeFileAtomic`).
    2. **HTTP Control Server (`daemon/server.go`):** Implement `d.Handler() http.Handler` enforcing `http.MaxBytesReader(w, r.Body, 1<<20)`:
       - `POST /v1/deploy`: body `{"config": <Config>, "release_id": "v1.0.0"}` -> calls `d.Deploy(&req.Config, req.ReleaseID)`, returns `DeploymentResult` (`200`), `400` on validation error, `402` on `ErrBudgetExceeded`, `502` on `ProxyApplier` error.
       - `POST /v1/rollback`: body `{"org_id": "...", "app_name": "..."}` -> calls `d.Rollback(req.OrgID, req.AppName)`, returns `RouteEntry` (`200`), `400` on invalid slug, `402` on `ErrBudgetExceeded`, `404` on unknown route, `409` on no standby release.
       - `GET /v1/status?org_id=...&app_name=...`: returns `AppStatus{OrgID, AppName, Route, Budget, LastSnapshot}` (`200`), `400` on missing/invalid slug, `404` if not registered.
       - `POST /v1/meter`: body `{"org_id": "...", "app_name": "...", "delta_usd": 1.25, "max_monthly_usd": 10.0}` -> if `max_monthly_usd > 0`, calls `d.SetMaxBudget`; if `delta_usd > 0` (or `NaN`/`<0`), calls `d.RecordSpend`; returns `AppBudgetState` (`200`, or `402` when `ErrBudgetExceeded`).
    3. **CLI Subcommands (`cli/cli.go`):** Load `filepath.Join(stateDir, "state.json")` on startup and save after state-mutating commands (`deploy`, `rollback`, `budget`):
       - `smallcloud init [dir] [--force]`: writes starter `smallcloud.toml` in `dir` (default `.`), refusing to overwrite an existing `smallcloud.toml` unless `--force` is passed.
       - `smallcloud status [dir]`: loads `smallcloud.toml` in `dir` (default `.`) and outputs `AppStatus` JSON (`route`, `budget`, `last_snapshot`).
       - `smallcloud budget [set] [dir] [max_monthly_usd]`: with no `<max_monthly_usd>`, outputs current `AppBudgetState` JSON; with `<max_monthly_usd>` (supporting both `smallcloud budget <dir> <usd>` and `smallcloud budget set <dir> <usd>`), calls `d.SetMaxBudget`, persists `state.json`, and outputs updated `AppBudgetState` JSON.
       - `smallcloud rollback [dir]`: loads `state.json`; if the app already has a standby release in `state.json`, rolls back directly without synthetic deploys; if invoked on a fresh `stateDir` with no prior deploys, seeds `v1.0.0` and `v1.1.0` only when no route exists yet so standalone `smallcloud rollback` smoke tests continue to work while multi-step `deploy v1 -> deploy v2 -> rollback` tests exercise real persisted state.
  - Files: `daemon/server.go`, `daemon/daemon.go`, `daemon/daemon_test.go`, `cli/cli.go`, `cli/cli_test.go`, `templates/fastapi-sqlite/app.py`
  - Verify: `gofmt -s -w . && go vet ./... && go test -race -v ./... && go run ./cli/cmd/smallcloud eval`

### Completion Summary

```text
  +====================================================================+
  |            MEGA PLAN REVIEW — COMPLETION SUMMARY                   |
  +====================================================================+
  | Mode selected        | SCOPE EXPANSION                             |
  | System Audit         | 2 commits on main; surface-check gap fixed  |
  | Step 0               | SCOPE EXPANSION + D2, D4, D5, D6, D7 (A)    |
  | Section 1  (Arch)    | 2 issues found (resolved via D2 & D5)       |
  | Section 2  (Errors)  | 10 error paths mapped, 0 GAPS remaining     |
  | Section 3  (Security)| 4 issues found, 3 High severity (fixed D7)  |
  | Section 4  (Data/UX) | 6 edge cases mapped, 0 unhandled            |
  | Section 5  (Quality) | 3 issues found (DRY + # comment quoting)    |
  | Section 6  (Tests)   | Diagram produced, 0 gaps remaining          |
  | Section 7  (Perf)    | 1 issue found (streaming >1GB in TODOS.md)  |
  | Section 8  (Observ)  | 2 gaps found (resolved via EXP-1 & EXP-2)   |
  | Section 9  (Deploy)  | 1 risk flagged (SQLite busy_timeout)        |
  | Section 10 (Future)  | Reversibility: 5/5, debt items: 2 (TODOS)   |
  | Section 11 (Design)  | SKIPPED (no UI scope)                       |
  +--------------------------------------------------------------------+
  | NOT in scope         | written (2 deferred items, 1 rejected)      |
  | What already exists  | written                                     |
  | Dream state delta    | written                                     |
  | Error/rescue registry| 10 rows, 0 CRITICAL GAPS                    |
  | Failure modes        | 14 total, 0 CRITICAL GAPS                   |
  | TODOS.md updates     | 2 items proposed & written to TODOS.md      |
  | Scope proposals      | 5 proposed, 5 accepted (EXP + SEL)          |
  | CEO plan             | written (/Users/justinmafie/.gstack/...)    |
  | Outside voice        | codex unavailable (MODEL_UNUSABLE cached)   |
  | Lake Score           | 2/2 recommendations chose complete option   |
  | Diagrams produced    | 6 (Arch, DataFlow, State, Err, Deploy, Roll)|
  | Stale diagrams found | 0                                           |
  | Unresolved decisions | 0                                           |
  | Reality rows         | 7/7 rows (PLAN_REALITY), 0 unverified       |
  +====================================================================+
```

### Unresolved Decisions
None — all decision rows (`SURFACE-GAP-1`, `EXP-1`, `EXP-2`, `EXP-3`, `SEC-HARDEN-1`) are resolved and approved.

---

## Engineering Review (`/gstack-plan-eng-review`)

- **Scope Challenge:** `scope accepted as-is` (`MODE = FULL_REVIEW`); structure settled on D1 (`Original arrangement` — 11 files, keeping `daemon/server.go` separate from `daemon/daemon.go`).
- **Two Shapes Before One (Design-Twice):**
  - **Shape 1 (Chosen):** `Daemon` (`daemon/daemon.go`) owns unified `d.mu` transaction locking, `DaemonState` persistence, and component orchestration; `daemon/server.go` (`d.Handler()`) is a thin Go 1.23 `http.ServeMux` transport adapter (`POST /v1/deploy`, `POST /v1/rollback`, `GET /v1/status`, `POST /v1/meter`) with `http.MaxBytesReader` (`413` on `*http.MaxBytesError`).
  - **Shape 2 (Alternative):** `ControlServer` owning its own mutex + `StateStore`, duplicating orchestration away from CLI. Rejected to keep one state owner (`Daemon`).
- **Eng Review Findings (4 found, 0 critical gaps):**
  1. `[CRITICAL]` (confidence: 9/10) `daemon/daemon.go:51-100` & `daemon/budget.go:96-101` — `Deploy` only locks `d.mu` around `d.seq++`, while `RecordSpend` invokes `onHalt` outside `b.mu`. **Fix in T2:** Hold `d.mu` across `Deploy`, `Rollback`, `RecordSpend`, `SetMaxBudget`, `LoadState`, and `SaveState` so `BudgetTracker`, `StorageManager`, and `CaddyRouter` never interleave out of order.
  2. `[INFORMATIONAL]` (confidence: 9/10) `daemon/daemon.go:51-55` — `Deploy` dereferences `cfg.Validate()` without a `nil` guard. **Fix in T2:** Add `if cfg == nil { return nil, errors.New("config is required") }`.
  3. `[INFORMATIONAL]` (confidence: 9/10) `docs/superpowers/plans/2026-11-25-smallcloud-mvp.md:342` — `http.MaxBytesReader` returns `*http.MaxBytesError`, which should map to `HTTP 413 Request Entity Too Large` in `daemon/server.go` (`T3`), reserving `400` for malformed JSON/validation errors.
  4. `[INFORMATIONAL]` (confidence: 9/10) `cli/cli.go:176` — Update usage string to include `init|status|budget` and extract `writeJSON` helper (`cli/cli.go:192-249`).
- **Worktree Parallelization Strategy:**
  - Sequential implementation, no parallelization opportunity across `T1` -> `T2` -> `T3` because `T2` (`daemon/budget.go`, `daemon/storage.go`, `daemon/daemon.go`) and `T3` (`daemon/daemon.go`, `cli/cli.go`) share `daemon/` and build on `T1`'s `writeFileAtomic` and slug/float guards.
- **Test Plan Artifact:** `/Users/justinmafie/.gstack/projects/justinmafie/justinmafie-main-eng-review-test-plan-20261125-192605.md`

### Eng Review Completion Summary
- Step 0: Scope Challenge — `scope accepted as-is` (`Original arrangement`, D1)
- Reality rows: 7/7 applicable rows emitted (`PLAN_REALITY: complete`), 0 unverified premises
- Architecture Review: 2 issues found (`d.mu` transaction scope, `HTTP 413` mapping — folded into `T2`/`T3`)
- Code Quality Review: 2 issues found (`nil` cfg guard, `writeJSON` + CLI usage string — folded into `T2`/`T3`)
- Test Review: diagram produced, 8 gaps identified (all mapped to `T1`–`T3` + Test Plan Artifact)
- Performance Review: 0 new issues found (`TODO-1` streaming `>1 GB` already in `TODOS.md`)
- NOT in scope: written (`TODOS.md` + `NOT in scope`)
- What already exists: written
- TODOS.md updates: 0 new items proposed (2 already recorded in `TODOS.md`)
- Failure modes: 0 critical gaps flagged
- Unresolved decisions: 0 in this review
- Outside voice: codex unavailable (`MODEL_UNUSABLE (cached)` for `gpt-6.2-astra`; `Plan` agent type not offered in `agy`)
- Parallelization: 1 lane, sequential (`T1` -> `T2` -> `T3`)
- Lake Score: N/A (0 completeness-scored questions in this review)

## GSTACK REVIEW REPORT

| Review | Trigger | Why | Runs | Status | Findings |
|--------|---------|-----|------|--------|----------|
| CEO Review | `/plan-ceo-review` | Scope & strategy | 4 | CLEAR | 5 proposals, 5 accepted, 2 deferred |
| Outside Review | `codex` (`/plan-ceo-review` + `/plan-eng-review`) | Independent 2nd opinion | 8 | unavailable | MODEL_UNUSABLE (cached) for gpt-6.2-astra; no completed external review |
| Eng Review | `/plan-eng-review` | Architecture & tests (required) | 5 | CLEAR | 4 issues, 0 critical gaps |
| Design Review | `/plan-design-review` | UI/UX gaps | 1 | ISSUES OPEN (stale) | score: 5/10 → 8/10, 10 decisions |
| DX Review | `/plan-devex-review` | Developer experience gaps | 1 | CLEAR (stale) | score: 6/10 → 9/10, TTHW: 15 min → < 2 min |

- **OUTSIDE COVERAGE:** provider=codex, phase=plan-review, state=unavailable (MODEL_UNUSABLE cached for gpt-6.2-astra; Plan agent type not offered in agy; no completed external review).
- **VERDICT:** CEO + ENG CLEARED — ready to implement

NO UNRESOLVED DECISIONS
