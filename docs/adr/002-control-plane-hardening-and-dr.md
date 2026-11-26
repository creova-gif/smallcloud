# ADR-002: Control-Plane Hardening, State Durability, and Disaster Recovery
- **Status:** Accepted
- **Owner:** Justin Mafie

## Context
As SmallCloud evolved from a single-node CLI prototype into a network-accessible control-plane daemon (`smallcloud serve`) managing multi-tenant applications, several production reliability and security risks emerged:
1. **Concurrent Per-Tenant Deploy Races:** Two simultaneous `POST /v1/deploy` requests for the same `(org_id, app_name)` could interleave cgroup writes, SQLite replica updates, and Blue/Green Caddy route swaps, corrupting container or route state. Conversely, a single global lock across all tenants would stall unrelated organizations behind a slow container build.
2. **Reverse-Proxy Rate-Limit Spoofing:** When `smallcloud serve` sits behind Caddy on `127.0.0.1:7420`, naive `r.RemoteAddr` rate limiting collapses all external clients into `127.0.0.1`, while unconditionally trusting `X-Forwarded-For` allows any client to spoof arbitrary IPs and bypass `RateLimitMiddleware`.
3. **Control-Plane State Corruption:** An unexpected power loss, disk full condition, or partial write on `state.json` could leave the daemon unable to boot or lose budget, route, and worker-node metadata.
4. **Untrusted DR Bundle Ingestion:** `POST /v1/restore` and `smallcloud restore-bundle` accept `.tar.gz` disaster-recovery archives. Without strict archive entry validation and read bounds, a crafted archive could execute path traversal, symlink attacks, or tar-bomb memory exhaustion.
5. **TLS & Network Exposure:** Serving plaintext HTTP directly on `0.0.0.0` exposes `SMALLCLOUD_API_TOKEN` Bearer credentials and webhook secrets on the wire.

## Decision
We harden the control-plane daemon and disaster-recovery pipeline with five coordinated controls:
1. **Per-Tenant Concurrency Locks (`tenantLocks`):** `Daemon` maintains a `sync.Map` (`tenantLocks` in `daemon/daemon.go`) keyed by `tenantKey(orgID, appName)` via `lockTenant(orgID, appName)`. Concurrent `Deploy` operations for the same tenant serialize cleanly, while deployments for distinct `(org_id, app_name)` pairs proceed concurrently.
2. **Trusted-Proxy `ClientIP` Rate Limiting:** `ClientIP(r, trusted)` and `RateLimitMiddlewareWithProxies` (`daemon/middleware.go`) inspect `X-Forwarded-For` and `X-Real-IP` only when `r.RemoteAddr` is loopback or matches a CIDR/IP configured in `SMALLCLOUD_TRUSTED_PROXIES`. `X-Forwarded-For` is walked right-to-left to select the rightmost untrusted client IP, preventing spoofed left-hand header entries from bypassing the fixed-window rate limiter (`429` with `Retry-After: 60`).
3. **Checksums & Fallback Recovery for `state.json`:** `Daemon.SaveState` (`daemon/daemon.go`) atomically preserves the prior state in `state.json.bak`, writes the new state atomically to `state.json`, and writes a hex digest to `state.json.sha256`. On startup, `Daemon.LoadState` verifies `state.json` against `state.json.sha256` and decodes it; if `state.json` is missing, empty, corrupted, or fails SHA-256 verification, `LoadState` automatically falls back to `state.json.bak` and records a `state_recovered_from_backup` audit event.
4. **Bounded, Pre-Verified DR Bundle Extraction:** `VerifyBackupBundle` and `RestoreBackupBundle` (`daemon/backup.go`) enforce `validateTarEntry` (rejecting non-regular files such as symlinks/hardlinks, negative sizes, absolute paths, and `..` traversal) and `readBoundedTarEntry` using `io.LimitReader(tr, limit+1)` (`maxManifestBytes = 1 MiB` for `manifest.json`, `maxSidecarBytes = 4096` for `replica.db.sha256`). `VerifyBackupBundle` streams `replica.db` through SHA-256 and verifies it matches both `replica.db.sha256` and `manifest.json` before `RestoreBackupBundle` writes any files to disk via `writeAtomicFile`.
5. **Loopback Binding + Caddy TLS Termination:** `smallcloud serve` defaults to `127.0.0.1:7420` and relies on Caddy as the edge reverse proxy for automatic HTTPS/TLS termination. Caddy forwards requests over loopback with `X-Forwarded-For`, paired with `SMALLCLOUD_TRUSTED_PROXIES=127.0.0.1/32` and `SMALLCLOUD_API_TOKEN` Bearer authentication on `/v1/*`.

## Alternatives Considered
- **Single Global Daemon Mutex for Deploys:** Rejected. Serializing all tenants behind one lock causes head-of-line blocking across organizations during Docker image builds and readiness probes.
- **Native Go `crypto/tls` / ACME in `smallcloud serve`:** Rejected. Caddy is already a core runtime dependency for Blue/Green and canary routing; delegating public TLS termination and ACME certificate renewal to Caddy avoids duplicating certificate management in the control-plane daemon.
- **SQLite for Control-Plane `DaemonState`:** Considered, but atomic JSON persistence with `.bak` + `.sha256` keeps daemon state human-inspectable, zero-dependency, and portable across backups without schema migrations.

## Consequences & Failure Modes
- **`tenantLocks` Map Growth:** `tenantLocks` retains one `*sync.Mutex` per unique `(org_id, app_name)` seen during a daemon process lifetime. Memory impact is negligible for expected tenant counts and resets on daemon restart.
- **Misconfigured `SMALLCLOUD_TRUSTED_PROXIES`:** If an intermediate load balancer IP is omitted from `SMALLCLOUD_TRUSTED_PROXIES`, all traffic from that proxy shares a single rate-limit bucket. Conversely, adding untrusted public CIDRs allows clients to spoof `X-Forwarded-For`.
- **Dual Corruption (`state.json` and `state.json.bak`):** If both `state.json` and `state.json.bak` are corrupted or unreadable simultaneously, `LoadState` fails fast with an error rather than starting with an empty state that could reset spend tracking.
- **Bundle Size Limits:** `manifest.json` (>1 MiB) and `replica.db.sha256` (>4096 bytes) are rejected as malformed or hostile archives; `replica.db` itself is streamed without buffering the full database in memory.
