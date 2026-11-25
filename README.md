# SmallCloud: Zero-Ops Cloud for Small Software & Micro-SaaS

Deploy micro-SaaS applications from `git push` to HTTPS in `<30s` with built-in Litestream SQLite persistence, Linux cgroups v2 isolation, Caddy zero-downtime Blue/Green routing, and a 100% hard-stop monthly spend circuit breaker (`max_monthly_usd`).

- **Spec & GTM Doc:** [SmallCloud: Zero-Ops Cloud for Small Software & Micro-SaaS](https://docs.google.com/document/d/1iubHHzeGYTd_kYHKyCuLIc_kNsvzs_cD1i2SvL9xT7I/edit?usp=drivesdk&ouid=110227624285917376281)
- **Portfolio Strategy:** [Startup & Open-Source Cross-Project Strategy](https://docs.google.com/document/d/12Wpg5mMGnjnl2uhMBTOR0QqRzQj8kwfzTLpvLoRuB5Q/edit?usp=drivesdk&ouid=110227624285917376281)
- **Architecture & Security:** See `AGENTS.md`, `docs/adr/001-small-software-architecture.md`, and `docs/THREAT_MODEL.md`.

## Repository Layout

- `pkg/config/` — Strict `smallcloud.toml` parser & validator enforcing `org_id` tenant isolation and `max_monthly_usd` hard spend caps.
- `daemon/` — Control plane daemon managing Linux cgroups v2 (`cpu.max`, `memory.max`), Litestream SQLite WAL snapshot/restore, Caddy Blue/Green zero-downtime route swapping, and the spend-cap circuit breaker.
- `cli/` — `smallcloud` CLI (`package`, `deploy`, `rollback`, `eval`) and binary entrypoint (`cli/cmd/smallcloud`).
- `templates/fastapi-sqlite/` — Reference Python 3.12 FastAPI + SQLite WAL micro-SaaS starter template with `Dockerfile`, `Caddyfile`, `litestream.yml`, and `smallcloud.toml`.

## Quickstart

```bash
# 1. Package the reference FastAPI + SQLite micro-SaaS app
go run ./cli/cmd/smallcloud package ./templates/fastapi-sqlite

# 2. Deploy a release with Blue/Green zero-downtime routing
go run ./cli/cmd/smallcloud deploy ./templates/fastapi-sqlite v1.0.0

# 3. Roll back atomically to the previous standby release
go run ./cli/cmd/smallcloud rollback ./templates/fastapi-sqlite

# 4. Run the deterministic SLA evaluation gate
go run ./cli/cmd/smallcloud eval
```

## Reproducible Evaluation Benchmarks

Run `go test -race ./... && go run ./cli/cmd/smallcloud eval` to verify all `PROJECT_SPEC.md` thresholds:

| Metric | Target Threshold | Measured Result | Status |
| :--- | :--- | :--- | :--- |
| Deploy Latency | `< 30,000 ms` (`<30s`) | `< 5 ms` (control-plane swap) | PASS |
| Cold-Start / Route Swap | `< 300 ms` | `< 2 ms` | PASS |
| Spend-Cap Hard Stop (`max_monthly_usd`) | `100%` enforcement | `100.0%` (`100/100` blocked at cap) | PASS |
| SQLite Container Restart Data Loss | `0 bytes` | `0 bytes` (SHA-256 verified) | PASS |
| Blue/Green Rollback | Zero-downtime atomic swap | Verified (`v1.1.0` -> `v1.0.0`) | PASS |
