# SmallCloud Wave 1 & Wave 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the SmallCloud Zero-Ops PaaS CLI, Daemon, Config Parser (`smallcloud.toml` with `max_monthly_usd` hard circuit breaker), Caddy zero-downtime Blue/Green proxy controller, Litestream SQLite persistence manager, sample FastAPI template, and deterministic evaluation suite meeting all `PROJECT_SPEC.md` SLA thresholds (`<30s` deploy, `<300ms` cold-start, `100%` spend cap stop, `0` data loss on restart).

**Architecture:** A modular Go 1.23 workspace (`github.com/creova-gif/smallcloud`) split into `pkg/config` (strict `smallcloud.toml` parser & validator), `daemon/` (budget circuit breaker, cgroups v2 isolator, Litestream SQLite snapshot/restore engine, Caddy zero-downtime Blue/Green router, and orchestrator daemon), `cli/` (`smallcloud` CLI binary supporting `init`, `deploy`, `rollback`, `status`, `budget`), and `templates/fastapi-sqlite/` (reference FastAPI micro-SaaS template with Dockerfile, Caddyfile, and Litestream config).

**Tech Stack:** Go 1.23 (standard library, zero external dependency bloat for fast builds and auditability), Docker/OCI & Linux cgroups v2, Caddy Admin JSON API, Litestream SQLite WAL replication, Python 3.12 / FastAPI (reference template).

---

### Task 1: `smallcloud.toml` Config Parser & Validator (`pkg/config`)

**Files:**
- Create: `pkg/config/config.go`
- Create: `pkg/config/config_test.go`

- [ ] **Step 1: Write the failing test (`pkg/config/config_test.go`)**
  Test parsing valid `smallcloud.toml` files, enforcing `org_id` tenant scoping, validating `max_monthly_usd > 0`, defaulting optional fields (`port`, `cpu_millicores`, `memory_mb`, `sqlite_path`), and rejecting missing required fields or negative budgets.
- [ ] **Step 2: Run test to verify it fails**
  Run: `go test -race ./pkg/config/...`
- [ ] **Step 3: Write minimal implementation (`pkg/config/config.go`)**
  Implement a clean, dependency-free TOML subset parser and validator supporting `[app]`, `[resources]`, `[budget]`, and `[storage]` sections with `Parse(r io.Reader)` and `LoadFile(path string)`.
- [ ] **Step 4: Run test to verify it passes**
  Run: `go vet ./pkg/config/... && go test -race -v ./pkg/config/...`

---

### Task 2: Daemon Core — Budget Enforcer, cgroups v2, Caddy Router, Litestream Persistence (`daemon/`)

**Files:**
- Create: `daemon/budget.go`
- Create: `daemon/cgroup.go`
- Create: `daemon/storage.go`
- Create: `daemon/proxy.go`
- Create: `daemon/daemon.go`
- Create: `daemon/daemon_test.go`

- [ ] **Step 1: Write deterministic tests (`daemon/daemon_test.go`)**
  Test:
  1. `BudgetTracker`: records usage increments per `(org_id, app_name)`, emits warnings at 80%, and trips the hard-stop circuit breaker at `100%` of `max_monthly_usd`, blocking requests and triggering container halt.
  2. `CgroupManager`: writes Linux cgroups v2 `cpu.max` and `memory.max` limits into a configurable cgroup root directory.
  3. `StorageManager`: simulates/executes Litestream SQLite WAL snapshot before container stop and restore before container start, verifying `0` bytes lost across restarts.
  4. `CaddyRouter` & `Daemon`: performs Blue/Green deployment (`v1` -> `v2`), swaps active upstream atomically with zero dropped requests, and executes `Rollback()` back to `v1` in `<300ms`.
- [ ] **Step 2: Implement `daemon/` components**
  Implement `budget.go`, `cgroup.go`, `storage.go`, `proxy.go`, and `daemon.go` with full error checking, `sync.RWMutex` concurrency safety (`go test -race` clean), and HTTP control endpoints (`/v1/deploy`, `/v1/rollback`, `/v1/status`, `/v1/meter`).
- [ ] **Step 3: Verify tests and race detector**
  Run: `go vet ./daemon/... && go test -race -v ./daemon/...`

---

### Task 3: Reference FastAPI + Litestream + Caddy Template (`templates/fastapi-sqlite/`)

**Files:**
- Create: `templates/fastapi-sqlite/smallcloud.toml`
- Create: `templates/fastapi-sqlite/app.py`
- Create: `templates/fastapi-sqlite/Dockerfile`
- Create: `templates/fastapi-sqlite/Caddyfile`
- Create: `templates/fastapi-sqlite/litestream.yml`

- [ ] **Step 1: Create template files**
  Build a complete, production-ready FastAPI + SQLite micro-SaaS template with `/healthz` and `/items` SQLite persistence endpoints, multi-stage Dockerfile, Litestream replica config, Caddy reverse proxy config with automatic HTTPS and healthchecks, and a sample `smallcloud.toml` (`max_monthly_usd = 5.00`).

---

### Task 4: Go CLI (`cli/`) for Packaging, Deploy, Rollback, and Eval Gate

**Files:**
- Create: `cli/cli.go`
- Create: `cli/cli_test.go`
- Create: `cli/cmd/smallcloud/main.go`

- [ ] **Step 1: Write CLI & end-to-end evaluation tests (`cli/cli_test.go`)**
  Verify:
  - `smallcloud package` bundles `templates/fastapi-sqlite` with validated `smallcloud.toml`, `Dockerfile`, `Caddyfile`, and `litestream.yml`.
  - `smallcloud deploy` deploys in `<30s` (measured in microseconds/milliseconds locally) with cold start `<300ms`.
  - `smallcloud rollback` atomically swaps Blue/Green routes back to the previous release with zero downtime.
  - `smallcloud eval` runs the full acceptance benchmark gate (`deploy_ms < 30000`, `cold_start_ms < 300`, `budget_hard_stop_rate == 1.0`, `restart_data_loss_bytes == 0`) and outputs structured JSON/table results.
- [ ] **Step 2: Implement `cli/cli.go` and `cli/cmd/smallcloud/main.go`**
- [ ] **Step 3: Run full suite with race detector**
  Run: `gofmt -s -w . && go vet ./... && go test -race -v ./...`

## GSTACK REVIEW REPORT
- **Target:** `docs/superpowers/plans/2026-11-25-smallcloud-mvp.md`
- **Status:** APPROVED — locks in ADR-001 architecture, `org_id` scoping per `docs/THREAT_MODEL.md`, deterministic unit/eval tests without external daemon flakiness, and zero-dependency Go 1.23 stdlib implementation.
