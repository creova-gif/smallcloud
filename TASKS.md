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
- [ ] Audit Log & Event Stream (`daemon` structured append-only audit log and `GET /v1/events`)
- [ ] App Teardown / `destroy` (`smallcloud destroy <app>` with Caddy route removal, container stop, and optional data purge)
- [ ] `[env]` Config & Runtime Injection (`smallcloud.toml` `[env]` table passed securely into container runtime)
- [x] Go SQLite Template (`templates/go-sqlite/` zero-dependency starter with `smallcloud.toml` and multi-stage `Dockerfile`)
- [ ] HTTP Readiness Gate & Prometheus `/metrics` (`daemon` readiness probe and Prometheus metrics endpoint)

## Wave 5: Full Product Engineering & Scope Expansion
- [ ] Embedded Operator Web UI (lightweight single-binary dashboard for deployments, budgets, and logs)
- [ ] Signed Budget/Alert Webhooks (HMAC-SHA256 signed webhook delivery on budget thresholds and hard-stop events)
- [ ] Live `doctor --live` TLS & Caddy Admin Probe (active Caddy admin API and TLS certificate verification)
- [ ] Automated Snapshot Pruning & S3/GCS Backup Lifecycle (retention policies and scheduled object-store pruning)
- [ ] Multi-Node Worker Pool / Edge Routing (multi-host scheduling and edge proxy route synchronization)
