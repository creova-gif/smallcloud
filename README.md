# SmallCloud

SmallCloud deploys single-node web applications from a repository to HTTPS in under 30 seconds. It enforces Linux cgroups v2 limits, replicates SQLite with Litestream, swaps routes through Caddy, and stops traffic when an application reaches `max_monthly_usd`.

- [SmallCloud Spec and GTM Doc](https://docs.google.com/document/d/1iubHHzeGYTd_kYHKyCuLIc_kNsvzs_cD1i2SvL9xT7I/edit?usp=drivesdk&ouid=110227624285917376281)
- [Startup and Open-Source Portfolio Strategy](https://docs.google.com/document/d/12Wpg5mMGnjnl2uhMBTOR0QqRzQj8kwfzTLpvLoRuB5Q/edit?usp=drivesdk&ouid=110227624285917376281)
- [ADR-001 Architecture Decision Record](docs/adr/001-small-software-architecture.md)
- [Threat Model](docs/THREAT_MODEL.md)

## Repository layout

- `pkg/config/` parses `smallcloud.toml` and validates `org_id` tenant scoping and `max_monthly_usd` limits.
- `daemon/` writes cgroups v2 files (`cpu.max`, `memory.max`), syncs and restores SQLite files, swaps Blue and Green Caddy routes, and trips the spend circuit breaker.
- `cli/` implements the `smallcloud` commands (`init`, `doctor`, `package`, `deploy`, `rollback`, `canary`, `status`, `list`, `logs`, `events`, `budget`, `org-quota`, `meter`, `rollover`, `prune`, `destroy`, `serve`, `eval`) with main entrypoint `cli/cmd/smallcloud/main.go`.
- `templates/fastapi-sqlite/` contains a Python 3.12 FastAPI and SQLite starter application with `Dockerfile`, `Caddyfile`, `litestream.yml`, and `smallcloud.toml`.
- `templates/node-sqlite/` contains a zero-dependency Node.js 22 ESM HTTP starter application with `Dockerfile`, `smallcloud.toml`, `server.mjs`, and `test_server.mjs`.

## Run the CLI

```bash
	go run ./cli/cmd/smallcloud init --template fastapi-sqlite ./my-app
	go run ./cli/cmd/smallcloud doctor --live ./templates/fastapi-sqlite
	go run ./cli/cmd/smallcloud package ./templates/fastapi-sqlite
	go run ./cli/cmd/smallcloud deploy ./templates/fastapi-sqlite v1.0.0
	go run ./cli/cmd/smallcloud canary ./templates/fastapi-sqlite 20
	go run ./cli/cmd/smallcloud rollback ./templates/fastapi-sqlite
	go run ./cli/cmd/smallcloud status ./templates/fastapi-sqlite
	go run ./cli/cmd/smallcloud list
	go run ./cli/cmd/smallcloud logs ./templates/fastapi-sqlite
	go run ./cli/cmd/smallcloud events
	go run ./cli/cmd/smallcloud budget set ./templates/fastapi-sqlite 10.00
	go run ./cli/cmd/smallcloud org-quota set acme 100.00
	go run ./cli/cmd/smallcloud meter ./templates/fastapi-sqlite 1.25
	go run ./cli/cmd/smallcloud rollover
	go run ./cli/cmd/smallcloud prune ./templates/fastapi-sqlite
	go run ./cli/cmd/smallcloud destroy ./templates/fastapi-sqlite
	go run ./cli/cmd/smallcloud serve --addr 127.0.0.1:7420
	go run ./cli/cmd/smallcloud eval
```

You can also scaffold or deploy the Node.js starter at `templates/node-sqlite/` (for example, `go run ./cli/cmd/smallcloud deploy ./templates/node-sqlite v1.0.0`).

Environment variables:
- `SMALLCLOUD_DAEMON_URL`: base URL of a running `smallcloud serve` daemon (for example, `http://127.0.0.1:7420`) so CLI commands talk to the daemon over HTTP instead of local state files.
- `SMALLCLOUD_API_TOKEN`: require `Authorization: Bearer <token>` on `/v1/*` HTTP endpoints (`/healthz` stays open) and attach the token from the CLI when `SMALLCLOUD_DAEMON_URL` is set.
- `SMALLCLOUD_CADDY_ADMIN_URL`: Caddy Admin API URL used when syncing live Blue/Green routes.
- `SMALLCLOUD_RUNTIME`: container/process runtime backend selection for `smallcloud` deployments (for example, `dockerfile`).
- `SMALLCLOUD_WEBHOOK_URL`: target HTTP endpoint for signed budget/alert webhook deliveries.
- `SMALLCLOUD_WEBHOOK_SECRET`: HMAC-SHA256 signing secret for `X-SmallCloud-Signature-256` webhook headers.
- `SMALLCLOUD_HEALTH_PROBE_TIMEOUT`: timeout duration for HTTP readiness probes (for example, `2s`).
- `SMALLCLOUD_ROLLOVER_INTERVAL`: background interval for automated monthly billing-cycle rollover checks (for example, `1h`).

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
