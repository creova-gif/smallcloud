# ADR-001: Small-Software Native Architecture (Litestream SQLite + Caddy + Linux cgroups v2)
- **Status:** Accepted
- **Owner:** Justin Mafie

## Context
Micro-SaaS applications, indie-hacker projects, and AI-generated web apps typically serve 1,000 to 100,000 requests per day. Traditional cloud platforms force developers into one of two extremes:
1. **Heavy Orchestrators (Kubernetes / ECS):** High operational complexity, slow deployment cycles (3–10 minutes), expensive control-plane overhead, and mandatory managed relational databases ($15–$50/mo minimum before a single user signs up).
2. **Serverless Functions / PaaS with Usage Traps (Vercel / AWS Lambda):** Instant setup, but ephemeral filesystems (requiring external stateful DBs), cold-start penalties, and uncapped usage billing where a traffic spike or DDoS loop can cause thousands of dollars in runaway cloud bills.

SmallCloud's goal is to deploy micro-SaaS apps from `git push` to HTTPS in `<30s`, with `<300ms` cold-start, zero-ops SQLite persistence with `0` data loss across container restarts, and a `100%` hard-stop monthly spend circuit breaker (`max_monthly_usd`).

## Decision
We optimize SmallCloud specifically for single-node and small-cluster micro-SaaS workloads using four tightly integrated primitives written in Go:
1. **Strict Declarative Config (`smallcloud.toml`):** Every application declares its `app_name`, `org_id`, `domain`, runtime resources (`cpu_millicores`, `memory_mb`), SQLite path + Litestream replica destination, and a mandatory `max_monthly_usd` hard spend cap.
2. **Blue/Green Container Runtime + Linux cgroups v2:** Applications are packaged into lightweight OCI containers and constrained via Linux cgroups v2 (`cpu.max` and `memory.max`) to guarantee deterministic multi-tenant isolation without Kubernetes overhead.
3. **Zero-Downtime Route Swapping via Caddy Admin API:** Caddy provides automatic TLS/HTTPS provisioning and atomic upstream switching between Blue/Green container instances during `deploy` and `rollback`.
4. **Continuous SQLite Replication via Litestream:** Stateful data lives in local WAL-mode SQLite inside the container volume for sub-millisecond reads/writes, continuously replicated via Litestream sidecar/hooks to GCS/S3, and automatically restored prior to container start.
5. **Hard Monthly Spend-Cap Circuit Breaker:** The SmallCloud daemon meters cumulative compute, bandwidth, and storage egress cost per `(org_id, app_name)` against `max_monthly_usd`. At 100% utilization, the circuit breaker trips immediately, halting container traffic and swapping the Caddy route to a static 402/503 maintenance responder until the budget is raised or the billing cycle resets.

## Alternatives Considered
- **Kubernetes (K3s / EKS / GKE) + Cloud SQL:** Rejected. Control-plane memory overhead (>1.5 GB RAM idle), slow pod scheduling, and managed Postgres costs violate our `$5/app/mo` unit economics and `<30s` deploy SLA.
- **Firecracker MicroVMs:** Considered for stronger kernel isolation. Deferred for Wave 1 MVP in favor of OCI containers + cgroups v2 for universal local/Mac/Linux developer ergonomics; the `Runtime` interface in `daemon/` is abstracted so Firecracker can be plugged in later.
- **External Managed Postgres (Neon / Supabase):** Rejected as the default persistence engine because network round-trips add latency and connection-pooling failure modes, whereas local SQLite + Litestream achieves zero-configuration persistence at pennies per month on object storage.

## Consequences & Failure Modes
- **Single-Writer Constraint:** SQLite + Litestream assumes a single active writer per app at any instant. During Blue/Green deploy or rollback, the daemon must flush WAL checkpoints and hand off the SQLite lock cleanly to achieve `0` data loss.
- **Hard Spend Cap Availability Tradeoff:** Enforcing a 100% hard stop at `max_monthly_usd` intentionally sacrifices availability for cost safety when an app exceeds its budget. Warnings are emitted at 80% and 95% thresholds.
- **Host Kernel Dependency:** Native cgroups v2 path writes (`/sys/fs/cgroup/...`) require Linux kernel 5.8+; on macOS/development hosts, the cgroup manager operates in a virtualized/RootFS-backed directory so all limits remain deterministically testable.
