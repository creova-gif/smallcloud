# Changelog

All notable changes to SmallCloud are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Hardening, Observability & Disaster Recovery
- **HTTP Security Middleware**: Added token-bucket `RateLimitMiddleware`, `X-Request-ID` propagation, and HTTP security headers (`X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `Cache-Control`).
- **Readiness Endpoint**: Added `GET /readyz` to verify daemon readiness separately from `/healthz`.
- **Worker Pool Persistence**: Persisted `WorkerPool` node state across daemon restarts.
- **Disaster Recovery Backup Bundles**: Added `ExportBackupBundle`, `VerifyBackupBundle`, and `RestoreBackupBundle` in `daemon/backup.go` for portable `.tar.gz` app and SQLite replica archives with SHA-256 verification.
- **Unsafe Linker `[env]` Blocking**: Blocked unsafe dynamic linker environment variables (e.g. `LD_PRELOAD`, `LD_LIBRARY_PATH`, `DYLD_*`) in `smallcloud.toml` `[env]` validation.
- **Backup & Snapshot CLI Commands**: Added `smallcloud verify-snapshot`, `smallcloud backup`, and `smallcloud restore-bundle` CLI subcommands.
- **Structured Access Logging**: Added structured `log/slog` HTTP access logging in the control-plane daemon, controlled via `SMALLCLOUD_ACCESS_LOG` (default `true`).
- **Backup Download Endpoint & Client**: Added `GET /v1/backup` on the control-plane daemon and `DaemonClient.DownloadBackup` in the CLI client.
- **Archive Extraction Hardening**: Hardened `.tar.gz` backup extraction against tar-bomb path traversal, oversized entries, and unsafe symlinks/hardlinks.
- **CLI Version & Build Metadata**: Added `smallcloud version` subcommand with `cli.Version` ldflags injection in CI builds.
- **CI Coverage Reporting**: Updated GitHub Actions CI to collect Go test coverage with `go test -race -coverprofile=coverage.out ./...` and report total coverage.
- **Remote Restore Endpoint & Client**: Added `POST /v1/restore` on the control-plane daemon, `DaemonClient.RestoreBackup` in the CLI client, and remote `smallcloud restore-bundle` support when `SMALLCLOUD_DAEMON_URL` is configured.
- **Canary Reconciliation Endpoint & CLI**: Added `POST /v1/canary/reconcile`, `DaemonClient.ReconcileCanary`, and the `smallcloud canary-reconcile` CLI command for automated canary health evaluation and reconciliation.
- **State Durability & Fallback Recovery**: Added `state.json.bak` backup and `.sha256` checksum verification with automatic fallback recovery when loading persisted daemon state.
- **Docker Container Resource & Label Flags**: Enforced container limits and metadata via Docker `--memory`, `--cpus`, and `--label` flags in `daemon/runtime.go`.
- **Cross-Platform Release Workflow**: Added `.github/workflows/release.yml` to test, run `smallcloud eval`, and publish `linux`/`darwin` (`amd64`/`arm64`) binaries with `checksums.txt` on `v*` tags.
- **Threat Model & Security Policy**: Expanded `docs/THREAT_MODEL.md` with STRIDE threat analysis and added `SECURITY.md` with vulnerability disclosure and hardening guidance.
- **Webhook Retry Backoff**: Added bounded exponential backoff retries to `WebhookNotifier` for transient webhook delivery failures.
- **Worker Node Lifecycle Endpoints & Client**: Added `DELETE /v1/nodes` and `GET /v1/nodes/select` on the control-plane daemon alongside `DaemonClient.RemoveNode`, `DaemonClient.HeartbeatNode`, and `DaemonClient.SelectNode` in the CLI client.
- **Docker Container Privilege Hardening**: Enforced `--security-opt no-new-privileges:true` and `--cap-drop ALL` when launching containers in `daemon/runtime.go`.
- **Daemon Microbenchmarks**: Added `daemon/bench_test.go` microbenchmarks for core control-plane paths.
- **Stale Worker-Node Reaping**: Added `SMALLCLOUD_WORKER_TTL` stale worker-node reaping in `ServeWithContext` (default `5m`).
- **Cgroup & Config Fuzz Tests**: Added `daemon/cgroup_test.go` and `FuzzConfigParse` in `pkg/config/fuzz_test.go`.

### Wave 7 — Distributed Edge, Zero-Trust Mesh & Developer Platform
- **GitHub Webhook Git-Push Deployments**: HMAC-SHA256 webhook signature verification (`VerifyGitHubSignature`) and push event parsing (`ParseGitHubPushEvent`) in `daemon/gitops.go`.
- **Interactive App Scaffolding & Plugin Templates**: Expanded `smallcloud init` template scaffolding across FastAPI, Node.js, and Go runtimes.
- **Automated Chaos & Canary Auto-Rollback**: Automated canary health evaluation and rollback in `daemon/canary_auto.go`.
- **Multi-Region SQLite Read Replicas**: Read-only edge replica WAL streaming support.
- **WireGuard Mesh Control-Plane Tunneling**: Encrypted peer-to-peer mesh tunneling between control-plane daemon and worker nodes.

### Wave 6 — Enterprise Control Plane, Multi-Tenant Quotas & Release Lifecycle
- **Org-Level Aggregate Spend Quotas**: Soft and hard monthly spend rollups across applications per `org_id`.
- **Release Artifact Signing & Checksum Attestation**: Release artifact signing and checksum verification in `pkg/core/attestation.go`.
- **Streaming Container Logs**: `smallcloud logs` CLI command and `/v1/logs` daemon endpoint.
- **Canary Traffic Weighting**: Weighted traffic splitting (`X%` Blue / `Y%` Green) via `smallcloud canary` and `/v1/canary`.
- **Multi-Node Worker Pool & Edge Route Sync**: Multi-host scheduling and edge proxy route synchronization.

### Wave 5 — Full Product Engineering & Scope Expansion
- **Embedded Operator Web UI**: Single-binary dashboard served at `/` and `/ui` for inspecting app health, budget burn, and deployments.
- **Signed Budget/Alert Webhooks**: HMAC-SHA256 signed webhook deliveries on `alert_at_pct` and `hard_stop` events.
- **Live `doctor --live` TLS & Caddy Admin Probe**: Active Caddy Admin API and daemon readiness verification.
- **Automated Snapshot Pruning**: Retention-based local WAL snapshot pruning via `smallcloud prune` and `/v1/prune`.

### Wave 4 — Production Control Plane & Multi-Runtime Maturity
- **Audit Log & Event Stream**: Append-only structured audit log and `GET /v1/events` endpoint.
- **App Teardown (`destroy`)**: `smallcloud destroy` with Caddy route removal, container stop, and optional data purge.
- **`[env]` Config & Runtime Injection**: `smallcloud.toml` `[env]` table passed into the container runtime.
- **Go SQLite Template**: Zero-dependency Go starter in `templates/go-sqlite/`.
- **HTTP Readiness Gate & Prometheus `/metrics`**: Readiness probing (`pkg/core/health.go`) and Prometheus metrics endpoint.

### Wave 3 — Launch & Design Partner Validation
- Published reproducible `smallcloud eval` benchmark results in `README.md`.
- Added Node.js 22 ESM starter in `templates/node-sqlite/`.

### Wave 2 — MVP & Eval Gate
- Authored `docs/adr/001-small-software-architecture.md` and `docs/THREAT_MODEL.md`.
- Implemented core control-plane pipeline, deterministic unit tests, `smallcloud eval`, and GitHub Actions CI.

### Wave 1 — Initial Setup
- Initialized `cli/`, `daemon/`, `pkg/config/`, and `templates/fastapi-sqlite/`.
- Implemented `smallcloud.toml` parser with `max_monthly_usd` budget cap, cgroups v2 writer, Litestream WAL sync, and Caddy Blue/Green route swapping.
