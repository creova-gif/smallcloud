# SmallCloud

SmallCloud deploys single-node web applications from a repository to HTTPS in under 30 seconds. It enforces Linux cgroups v2 limits, replicates SQLite with Litestream, swaps routes through Caddy, and stops traffic when an application reaches `max_monthly_usd`.

- [SmallCloud Spec and GTM Doc](https://docs.google.com/document/d/1iubHHzeGYTd_kYHKyCuLIc_kNsvzs_cD1i2SvL9xT7I/edit?usp=drivesdk&ouid=110227624285917376281)
- [Startup and Open-Source Portfolio Strategy](https://docs.google.com/document/d/12Wpg5mMGnjnl2uhMBTOR0QqRzQj8kwfzTLpvLoRuB5Q/edit?usp=drivesdk&ouid=110227624285917376281)
- [ADR-001 Architecture Decision Record](docs/adr/001-small-software-architecture.md)
- [Threat Model](docs/THREAT_MODEL.md)

## Repository layout

- `pkg/config/` parses `smallcloud.toml` and validates `org_id` tenant scoping and `max_monthly_usd` limits.
- `daemon/` writes cgroups v2 files (`cpu.max`, `memory.max`), syncs and restores SQLite files, swaps Blue and Green Caddy routes, and trips the spend circuit breaker.
- `cli/` implements the `smallcloud` commands (`package`, `deploy`, `rollback`, `eval`) with main entrypoint `cli/cmd/smallcloud/main.go`.
- `templates/fastapi-sqlite/` contains a Python 3.12 FastAPI and SQLite starter application with `Dockerfile`, `Caddyfile`, `litestream.yml`, and `smallcloud.toml`.

## Run the CLI

```bash
	go run ./cli/cmd/smallcloud init ./my-app
	go run ./cli/cmd/smallcloud doctor ./templates/fastapi-sqlite
	go run ./cli/cmd/smallcloud package ./templates/fastapi-sqlite
	go run ./cli/cmd/smallcloud deploy ./templates/fastapi-sqlite v1.0.0
	go run ./cli/cmd/smallcloud rollback ./templates/fastapi-sqlite
	go run ./cli/cmd/smallcloud status ./templates/fastapi-sqlite
	go run ./cli/cmd/smallcloud budget set ./templates/fastapi-sqlite 10.00
	go run ./cli/cmd/smallcloud serve --addr 127.0.0.1:7420
	go run ./cli/cmd/smallcloud eval
```

Set `SMALLCLOUD_API_TOKEN` to require `Authorization: Bearer <token>` on `/v1/*` HTTP endpoints (`/healthz` stays open).

## Benchmark results

Generate the release binary and run 7 iterations of `smallcloud eval`:

```bash
	go build -o /tmp/smallcloud-bin ./cli/cmd/smallcloud
	for i in 1 2 3 4 5 6 7; do /tmp/smallcloud-bin eval; done
```

| Metric | Target | Measured (7 runs, release binary) | Limiter |
| :--- | :--- | :--- | :--- |
| Deploy control-plane swap | `< 30,000 ms` | `0 ms` median (`0` to `2 ms` range) | Local filesystem `cpu.max` and `memory.max` writes |
| Route swap and rollback | `< 300 ms` | `0 ms` median (`0` to `8 ms` range) | Mutex acquisition and map update in `CaddyRouter` |
| Spend cap stop (`max_monthly_usd`) | `100%` | `100%` (`100/100` calls rejected with `ErrBudgetExceeded`) | Synchronous check in `BudgetTracker.CheckAllowed` |
| SQLite restart data loss | `0 bytes` | `0 bytes` (`55/55` bytes and SHA-256 verified) | Temp-file write and atomic `os.Rename` |
