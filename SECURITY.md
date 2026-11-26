# Security Policy

## Supported Versions

| Version | Supported |
| --- | --- |
| `v0.2.x` | Yes |
| `v0.1.x` | Yes |
| `< v0.1.0` / unreleased snapshots | No |

## Reporting a Vulnerability

Please **do not** open a public GitHub issue for undisclosed security vulnerabilities.

Instead, report security issues privately using one of the following channels:

1. **GitHub Security Advisories (preferred):** Open a private vulnerability report via the **Security -> Advisories -> Report a vulnerability** tab on `https://github.com/creova-gif/smallcloud`.
2. **Maintainer Contact:** If you cannot use GitHub Security Advisories, contact the repository maintainers privately via their GitHub profile contact details on `github.com/creova-gif`.

When reporting, please include:

- A clear description of the vulnerability and affected component or file path.
- Steps to reproduce or a minimal proof-of-concept.
- Potential impact and any suggested remediation if known.

We aim to acknowledge receipt within **48 hours** and provide an initial assessment and remediation timeline within **5 business days**.

## Security Architecture Summary

SmallCloud's control-plane daemon and CLI enforce defense-in-depth across tenant isolation, transport authentication, data integrity, and resource/spend bounds (see [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md) for the full STRIDE matrix):

- **Authentication & Signed Webhooks:** Constant-time Bearer token verification (`subtle.ConstantTimeCompare`) on `/v1/*` endpoints via `SMALLCLOUD_API_TOKEN`, HMAC-SHA256 verification on inbound GitHub webhooks (`SMALLCLOUD_GITHUB_WEBHOOK_SECRET` / `X-Hub-Signature-256`), HMAC-SHA256 signing on outbound alert webhooks (`SMALLCLOUD_WEBHOOK_SECRET` / `X-SmallCloud-Signature-256`), and signed `ReleaseAttestation` manifests.
- **State, Storage & Backup Integrity:** Atomic writes with `.sha256` sidecar verification on SQLite replicas (`StorageManager`), `state.json.sha256` + `state.json.bak` automatic fallback recovery, and pre-restore archive/checksum/path-traversal validation (`VerifyBackupBundle` / `validateTarEntry`) before `RestoreBackupBundle`.
- **Input, Environment & Path Hardening:** Strict lowercase alphanumeric tenant slug validation (`IsValidSlug` / `validateTenantSlugs`), blocking of dynamic linker injection variables (`unsafeEnvKeys` such as `LD_PRELOAD` and `DYLD_INSERT_LIBRARIES`), Caddyfile token injection checks, `Cache-Control: no-store` on `/v1/*`, and credential-free structured access logging (`StructuredAccessLogMiddleware`).
- **DoS & Spend Protection:** Fixed-window `RateLimitMiddleware` (`429` with `Retry-After: 60`, bounded 4096-key map), `http.MaxBytesReader` caps (`1 MiB` JSON/webhook, `64 MiB` restore upload), `io.LimitReader` tar-bomb bounds, `http.Server` timeouts, cgroups v2 / Docker CPU & memory limits, and synchronous monthly spend circuit breakers (`ErrBudgetExceeded` / `ErrOrgQuotaExceeded`, HTTP `402`).
