# SmallCloud Threat Model

This document describes SmallCloud's security assets, trust boundaries, STRIDE threat matrix, implemented mitigations, and residual operational risks. Every control referenced below maps directly to the current Go codebase (`pkg/config`, `pkg/core`, `daemon`, `cli`).

## 1. Assets

| Asset | Description | Primary Locations |
| --- | --- | --- |
| **Tenant Data (`org_id` / `app_name`)** | Per-tenant application configuration, routes, canary weights, spend records, and audit events. | `pkg/config/config.go`, `daemon/daemon.go`, `daemon/budget.go` |
| **SQLite WAL Replicas & Snapshots** | Tenant `replica.db`, generation snapshots (`snapshot-<N>.db`), and `.sha256` checksum sidecars. | `daemon/storage.go`, `daemon/backup.go` |
| **Control-Plane State** | Persisted daemon state (`state.json`), fallback backup (`state.json.bak`), and integrity sidecar (`state.json.sha256`). | `daemon/daemon.go` |
| **API & Webhook Secrets** | `SMALLCLOUD_API_TOKEN` (Bearer auth), `SMALLCLOUD_GITHUB_WEBHOOK_SECRET` (inbound GitOps), `SMALLCLOUD_WEBHOOK_SECRET` (outbound budget alerts), and release attestation signing secrets. | `daemon/server.go`, `daemon/webhook.go`, `cli/cli.go`, `pkg/core/attestation.go` |
| **Host & Container Compute Resources** | cgroups v2 CPU/memory limits (`cpu.max`, `memory.max`) and Docker container runtime quotas. | `daemon/cgroup.go`, `daemon/runtime.go` |

## 2. Trust Boundaries

```text
  [CLI / External Client / GitHub]
                 │
                 ▼  (1) Network / TLS Termination (Caddy -> Daemon HTTP API)
  [Daemon HTTP Server & Middleware]   (cli/cli.go, daemon/server.go, daemon/middleware.go)
     ├─ RequestIDMiddleware, SecurityHeadersMiddleware, StructuredAccessLogMiddleware
     ├─ RateLimitMiddleware (429, bounded 4096-key map)
     ├─ Bearer Auth (subtle.ConstantTimeCompare on /v1/*)
     └─ GitHub Webhook HMAC-SHA256 (POST /webhooks/github)
                 │
                 ▼  (2) Control-Plane Validation & Budget Circuit Breaker
  [Daemon Core, Config & BudgetTracker]  (pkg/config/config.go, daemon/budget.go)
     ├─ validateTenantSlugs / IsValidSlug, unsafeEnvKeys, Caddy token checks
     └─ ErrBudgetExceeded / ErrOrgQuotaExceeded (HTTP 402)
                 │
                 ▼  (3) Runtime, Proxy & Persistence Boundary
  [WorkerPool / DockerRuntime / CgroupManager / CaddyRouter / StorageManager]
     ├─ DockerRuntime (--cpus, --memory, 127.0.0.1 port binding) & CgroupManager
     ├─ CaddyRouter / RenderCaddyfile (Admin API / halted 402 routes)
     └─ StorageManager & Backup Bundles (.sha256 sidecars, tar-bomb bounds)
```

## 3. STRIDE Threat Matrix & Mitigations

### Spoofing

| Threat | Mitigation | Implementation |
| --- | --- | --- |
| Unauthorized control-plane API calls | Constant-time Bearer token verification (`subtle.ConstantTimeCompare`) on all `/v1/*` routes when `SMALLCLOUD_API_TOKEN` is set. | `Daemon.HandlerWithAuth` (`daemon/server.go`) |
| Forged inbound GitHub push webhooks | Mandatory `SMALLCLOUD_GITHUB_WEBHOOK_SECRET` check and HMAC-SHA256 verification (`X-Hub-Signature-256`) using `hmac.Equal`. | `Daemon.handleGitHubWebhook` (`daemon/server.go`), `VerifyGitHubSignature` (`daemon/gitops.go`) |
| Forged outbound budget/alert webhooks | Outbound webhook payloads are signed with HMAC-SHA256 (`SMALLCLOUD_WEBHOOK_SECRET`) in the `X-SmallCloud-Signature-256` header. | `SignPayload`, `WebhookNotifier.Notify` (`daemon/webhook.go`), `cli/cli.go` |
| Tampered or unauthenticated release artifacts | Release metadata (`OrgID`, `AppName`, `ReleaseID`, `ArchiveSHA256`, `SignedAt`) is signed and verified with HMAC-SHA256 (`sha256=<hex>`) using `hmac.Equal`. | `ReleaseAttestation`, `SignReleaseAttestation`, `VerifyReleaseAttestation` (`pkg/core/attestation.go`) |

### Tampering

| Threat | Mitigation | Implementation |
| --- | --- | --- |
| Silent corruption or tampering of SQLite replicas | Streaming SHA-256 computation via `io.TeeReader`, atomic `os.Rename` with `0o600` permissions, `.sha256` sidecars, and pre-restore verification returning `ErrChecksumMismatch`. | `StorageManager.SyncWAL`, `StorageManager.VerifySnapshot`, `writeFileAtomicFromReader` (`daemon/storage.go`) |
| Corrupted `state.json` on crash or disk fault | `state.json.sha256` checksum verification on load, atomic `.bak` preservation on save, and automatic fallback recovery from `state.json.bak` with `state_recovered_from_backup` audit logging. | `Daemon.LoadState`, `readAndUnmarshalDaemonState`, `Daemon.SaveState` (`daemon/daemon.go`) |
| Malicious or corrupted disaster-recovery bundles | `RestoreBackupBundle` runs `VerifyBackupBundle` first, verifying `manifest.json`, `replica.db`, and `replica.db.sha256` before atomic extraction via `writeAtomicFile`. | `VerifyBackupBundle`, `RestoreBackupBundle` (`daemon/backup.go`) |
| Dynamic linker / runtime injection via `[env]` | Rejects `LD_PRELOAD`, `LD_LIBRARY_PATH`, `DYLD_INSERT_LIBRARIES`, `DYLD_LIBRARY_PATH`, and `LD_AUDIT`, plus NUL bytes and `=`/whitespace in keys. | `unsafeEnvKeys`, `Config.Validate` (`pkg/config/config.go`) |
| Caddyfile configuration injection | Rejects whitespace and Caddyfile control characters (`"`, `'`, `{`, `}`, `;`, `#`) in domains, healthcheck paths, and `replica_url`. | `hasUnsafeCaddyToken`, `Config.Validate` (`pkg/config/config.go`) |

### Repudiation

| Threat | Mitigation | Implementation |
| --- | --- | --- |
| Untraceable deployments, restores, or webhook triggers | Structured `AuditEvent` recording for `gitops_push`, `backup_restored`, `state_recovered_from_backup`, and lifecycle actions. | `AuditLog`, `Daemon.Audit` (`daemon/daemon.go`, `daemon/server.go`, `daemon/backup.go`) |
| Uncorrelated HTTP API requests | `RequestIDMiddleware` validates/propagates `X-Request-ID` (up to 64 `[A-Za-z0-9-_]` chars, or generates a 128-bit random hex ID) and `StructuredAccessLogMiddleware` emits structured `log/slog` records. | `daemon/middleware.go`, `daemon/logging.go` |

### Information Disclosure & Path Traversal

| Threat | Mitigation | Implementation |
| --- | --- | --- |
| Cross-tenant path traversal via `org_id` or `app_name` | Strict slug validation (`^[a-z0-9][a-z0-9-_]{1,62}$`) enforced across HTTP handlers, cgroup paths, storage paths, backup/restore, and runtime containers. | `Config.IsValidSlug` (`pkg/config/config.go`), `validateTenantSlugs` (`daemon/budget.go`, `daemon/cgroup.go`, `daemon/runtime.go`, `daemon/storage.go`, `daemon/backup.go`, `daemon/server.go`) |
| Tar-slip / symlink traversal during backup restore | `validateTarEntry` permits only regular files (`tar.TypeReg`, `tar.TypeRegA`), rejects negative sizes, leading `/`, and `..` after `path.Clean`. | `validateTarEntry` (`daemon/backup.go`) |
| Caching of sensitive control-plane API responses | `SecurityHeadersMiddleware` sets `Cache-Control: no-store` on `/v1/*` and `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer` on all responses. | `SecurityHeadersMiddleware` (`daemon/middleware.go`) |
| Credential leakage in HTTP access logs | `StructuredAccessLogMiddleware` logs only `method`, `path`, `status`, `bytes`, `duration_ms`, `request_id`, and `remote_addr`, omitting `Authorization` headers, cookies, and payloads. | `StructuredAccessLogMiddleware` (`daemon/logging.go`) |
| Direct network exposure of app container ports | `DockerRuntime.Start` binds container ports strictly to loopback (`127.0.0.1:<port>:<port>`). | `DockerRuntime.Start` (`daemon/runtime.go`) |

### Denial of Service

| Threat | Mitigation | Implementation |
| --- | --- | --- |
| API request flooding & rate-limiter memory exhaustion | `RateLimitMiddleware` enforces per-client fixed-window limits (default `600`/min or `SMALLCLOUD_RATE_LIMIT`, exempting `/healthz` and `/readyz`), returns HTTP `429` with `Retry-After: 60`, and caps the map at `maxRateLimiterKeys = 4096` with eviction (`evictLocked`). | `RateLimiter`, `RateLimitMiddleware` (`daemon/middleware.go`), `rateLimitPerMinute` (`daemon/server.go`) |
| Oversized request payloads | `http.MaxBytesReader` caps JSON payloads and GitHub webhooks at `1 MiB` (`maxRequestBodyBytes = 1 << 20`) and backup uploads at `64 MiB` (`maxRestoreBodyBytes = 64 << 20`), returning HTTP `413`. | `decodeJSONBody`, `handleGitHubWebhook`, `handleRestore` (`daemon/server.go`) |
| Tar-bomb / oversized metadata entries on restore | `readBoundedTarEntry` wraps tar reads in `io.LimitReader(tr, limit+1)` with `maxManifestBytes = 1 << 20` (`1 MiB`) for `manifest.json` and `maxSidecarBytes = 4096` (`4 KiB`) for `replica.db.sha256`. | `readBoundedTarEntry` (`daemon/backup.go`) |
| Slowloris / stalled HTTP connections | `http.Server` configures `ReadHeaderTimeout: 5s`, `ReadTimeout: 15s`, `WriteTimeout: 30s`, `IdleTimeout: 60s`, and `MaxHeaderBytes: 1 MiB`. | `cli/cli.go` |
| Runaway cloud/compute spend | Hard monthly spend cap (`budget.hard_stop` cannot be disabled; `max_monthly_usd` capped at `10000`) and org quotas trip `ErrBudgetExceeded` / `ErrOrgQuotaExceeded` (HTTP `402`), and halted Caddy routes return `402` with `Retry-After: 3600`. | `BudgetTracker` (`daemon/budget.go`), `Config.Validate` (`pkg/config/config.go`), `RenderCaddyfile` (`daemon/proxy.go`), `daemon/server.go` |
| CPU/memory exhaustion by tenant workloads | cgroups v2 `cpu.max` (`cgroupPeriodMicros = 100000`) and `memory.max`, plus Docker `--cpus` and `--memory` flags (`100..16000` millicores, `64..65536` MiB). | `CgroupManager.Apply` (`daemon/cgroup.go`), `DockerRuntime.Start` (`daemon/runtime.go`), `Config.Validate` (`pkg/config/config.go`) |

## 4. Residual Risks & Operational Guidance

1. **TLS Termination & Token Configuration**: The daemon HTTP server in `cli/cli.go` listens over plain TCP/HTTP. Deploy Caddy (or an equivalent TLS-terminating reverse proxy) in front of the daemon for all non-loopback traffic, and always set `SMALLCLOUD_API_TOKEN`—when `SMALLCLOUD_API_TOKEN` is empty, `HandlerWithAuth` leaves `/v1/*` unauthenticated.
2. **Host Docker Socket Privilege**: `DockerRuntime` (`daemon/runtime.go`) shells out to the host `docker` binary. Access to the Docker daemon socket is effectively root-equivalent on the host; run the control-plane daemon on a dedicated host or VM, restrict socket permissions, and do not run untrusted multi-tenant `docker build` workloads without rootless Docker or microVM isolation.
3. **Rate-Limiter Keying Behind a Reverse Proxy**: `RateLimitMiddleware` (`daemon/middleware.go`) keys on `r.RemoteAddr`. When traffic arrives through a local Caddy reverse proxy, requests share the proxy's loopback address; enforce edge rate limiting in Caddy or size `SMALLCLOUD_RATE_LIMIT` accordingly.
