# smallcloud — Execution Checklist

## Wave 1: Day 1–5 Setup
- [x] Set up cli/, daemon/, and templates/ directories
- [x] Implement smallcloud.toml parser including max_monthly_usd budget cap
- [x] Build Go CLI packaging a sample FastAPI app into a container with Caddy reverse proxy
- [x] Test smallcloud rollback and verify zero-downtime route swap

## Wave 2: MVP & Eval Gate
- [x] Write ADR-001 in `docs/adr/`
- [x] Implement core pipeline + deterministic unit/eval tests
- [x] Verify CI workflow passes (`ruff`/`mypy` or `cargo clippy`/`test` or `go test -race`)

## Wave 3: Launch & Design Partner Validation
- [x] Publish clean README with reproducible benchmark numbers
- [x] Run first external design-partner or open-source release

## Wave 4: Production Control Plane & Multi-Runtime Maturity
- [x] Audit Log & Event Stream (`daemon` structured append-only audit log and `GET /v1/events`)
- [x] App Teardown / `destroy` (`smallcloud destroy <app>` with Caddy route removal, container stop, and optional data purge)
- [x] `[env]` Config & Runtime Injection (`smallcloud.toml` `[env]` table passed securely into container runtime)
- [x] Go SQLite Template (`templates/go-sqlite/` zero-dependency starter with `smallcloud.toml` and multi-stage `Dockerfile`)
- [x] HTTP Readiness Gate & Prometheus `/metrics` (`daemon` readiness probe and Prometheus metrics endpoint)

## Wave 5: Full Product Engineering & Scope Expansion
- [x] Embedded Operator Web UI (lightweight single-binary dashboard for deployments, budgets, and logs)
- [x] Signed Budget/Alert Webhooks (HMAC-SHA256 signed webhook delivery on budget thresholds and hard-stop events)
- [x] Live `doctor --live` TLS & Caddy Admin Probe (active Caddy admin API and TLS certificate verification)
- [x] Automated Snapshot Pruning & S3/GCS Backup Lifecycle (retention policies and scheduled object-store pruning)
- [ ] Multi-Node Worker Pool / Edge Routing (multi-host scheduling and edge proxy route synchronization)

## Wave 6: Enterprise Control Plane, Multi-Tenant Quotas & Release Lifecycle
- [x] Org-Level Aggregate Spend Quotas & Soft/Hard Rollup
- [x] Release Artifact Signing & Checksum Attestation
- [x] Streaming Container Logs (`smallcloud logs`)
- [x] Canary Traffic Weighting (`X%` Blue / `Y%` Green)
- [ ] Multi-Node Worker Pool & Edge Route Sync

## Wave 7: Distributed Edge, Zero-Trust Mesh & Developer Platform
- [ ] Multi-Region SQLite Read Replicas
- [ ] WireGuard Mesh Control-Plane Tunneling
- [x] GitHub Webhook Git-Push Deployments
- [x] Interactive App Scaffolding & Plugin Templates
- [x] Automated Chaos & Canary Auto-Rollback
