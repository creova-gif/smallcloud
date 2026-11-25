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
