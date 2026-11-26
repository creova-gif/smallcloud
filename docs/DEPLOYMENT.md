# Production Linux Deployment Guide & DR Runbook

This guide covers deploying the SmallCloud control-plane daemon (`smallcloud serve`) on a production Linux host behind Caddy with TLS, `systemd` process supervision, preflight verification, and disaster-recovery (DR) backup/restore procedures.

## 1. Binary Installation (`v0.2.0`)

Prebuilt `linux/amd64` and `linux/arm64` binaries with SHA-256 `checksums.txt` are published on GitHub Releases. Download the binary for your architecture and verify its checksum before installing:

```bash
VERSION="v0.2.0"
ARCH="amd64" # or "arm64"

curl -fsSLO "https://github.com/creova-gif/smallcloud/releases/download/${VERSION}/smallcloud-linux-${ARCH}"
curl -fsSLO "https://github.com/creova-gif/smallcloud/releases/download/${VERSION}/checksums.txt"

sha256sum --ignore-missing -c checksums.txt
sudo install -m 0755 "smallcloud-linux-${ARCH}" /usr/local/bin/smallcloud
smallcloud version
```

## 2. Environment & Secret Configuration

Create a dedicated state directory and copy [`.env.example`](../.env.example) to `/etc/smallcloud/smallcloud.env`:

```bash
sudo mkdir -p /etc/smallcloud /var/lib/smallcloud
sudo cp .env.example /etc/smallcloud/smallcloud.env
sudo chmod 0600 /etc/smallcloud/smallcloud.env
```

Generate strong random secrets (for example with `openssl rand -hex 32`) and configure `/etc/smallcloud/smallcloud.env`:

```dotenv
SMALLCLOUD_STATE_DIR=/var/lib/smallcloud
SMALLCLOUD_DAEMON_URL=http://127.0.0.1:7420
SMALLCLOUD_API_TOKEN=<output-of-openssl-rand-hex-32>

SMALLCLOUD_RUNTIME=docker
SMALLCLOUD_CADDY_ADMIN_URL=http://127.0.0.1:2019
SMALLCLOUD_HEALTH_PROBE_TIMEOUT=2s

SMALLCLOUD_ROLLOVER_INTERVAL=1h
SMALLCLOUD_WORKER_TTL=5m

SMALLCLOUD_RATE_LIMIT=600
SMALLCLOUD_TRUSTED_PROXIES=127.0.0.1/32
SMALLCLOUD_ACCESS_LOG=true

SMALLCLOUD_WEBHOOK_URL=https://hooks.example.com/smallcloud/alerts
SMALLCLOUD_WEBHOOK_SECRET=<output-of-openssl-rand-hex-32>
SMALLCLOUD_GITHUB_WEBHOOK_SECRET=<output-of-openssl-rand-hex-32>
```

> Never commit `/etc/smallcloud/smallcloud.env` or `.env` files to version control.

## 3. Caddy TLS Termination

Bind `smallcloud serve` to loopback (`127.0.0.1:7420`) and place Caddy in front of it for automatic HTTPS/TLS termination. Add the following site block to `/etc/caddy/Caddyfile`:

```caddyfile
control.example.com {
    reverse_proxy 127.0.0.1:7420 {
        header_up X-Real-IP {remote_host}
    }
}
```

Reload Caddy after updating the `Caddyfile`:

```bash
sudo systemctl reload caddy
```

Because `SMALLCLOUD_TRUSTED_PROXIES=127.0.0.1/32` is set, `RateLimitMiddleware` honors `X-Forwarded-For` / `X-Real-IP` headers from the local Caddy instance while ignoring spoofed headers from untrusted sources.

## 4. `systemd` Service Unit

Create `/etc/systemd/system/smallcloud.service`:

```ini
[Unit]
Description=SmallCloud Control-Plane Daemon
After=network-online.target docker.service caddy.service
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/smallcloud/smallcloud.env
ExecStart=/usr/local/bin/smallcloud serve --addr 127.0.0.1:7420
Restart=on-failure
RestartSec=5s
KillSignal=SIGTERM
TimeoutStopSec=30s
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

Enable and start the service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now smallcloud
sudo systemctl status smallcloud
```

On `SIGTERM`, `smallcloud serve` triggers graceful shutdown (`ServeWithContext`), flushing SQLite WAL state across tenants via `SyncAllWAL()` and persisting `state.json` + `state.json.bak` + `state.json.sha256`.

## 5. Preflight Verification (`smallcloud doctor --live`)

Before deploying workloads, verify the daemon, Docker runtime, cgroups v2, and Caddy Admin API:

```bash
set -a; source /etc/smallcloud/smallcloud.env; set +a
smallcloud doctor --live ./templates/fastapi-sqlite
```

Confirm `"passed": true` in the JSON output and check `/healthz` and `/readyz`:

```bash
curl -fsS http://127.0.0.1:7420/healthz
curl -fsS http://127.0.0.1:7420/readyz
```

## 6. Disaster Recovery (DR) Runbook

### Verify Latest Local WAL Snapshot

Verify that the latest SQLite replica on disk matches its `.sha256` sidecar:

```bash
smallcloud verify-snapshot ./my-app
```

### Export a Backup Bundle (`backup`)

Export an application's `manifest.json`, `replica.db`, and `replica.db.sha256` into a portable `.tar.gz` archive. When `SMALLCLOUD_DAEMON_URL` and `SMALLCLOUD_API_TOKEN` are set, `backup` downloads the bundle from the running daemon (`GET /v1/backup`) and verifies its integrity locally:

```bash
smallcloud backup ./my-app /var/backups/smallcloud/my-app-$(date -u +%Y%m%dT%H%M%SZ).tar.gz
```

### Restore from a Backup Bundle (`restore-bundle`)

Restore an application's state and SQLite replica from a verified `.tar.gz` bundle. `RestoreBackupBundle` validates archive paths, enforces `io.LimitReader` bounds on metadata/sidecar entries, and verifies SHA-256 checksums before writing to disk. When `SMALLCLOUD_DAEMON_URL` is set, the bundle is uploaded to `POST /v1/restore`:

```bash
smallcloud restore-bundle /var/backups/smallcloud/my-app-20261126T120000Z.tar.gz
```

After restoring, verify the restored snapshot and application status:

```bash
smallcloud verify-snapshot ./my-app
smallcloud status ./my-app
```
