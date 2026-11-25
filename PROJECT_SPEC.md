# SmallCloud: Zero-Ops Cloud for Small Software & Micro-SaaS

- **Model:** Open-Source Self-Hostable PaaS + Hosted $5/app/mo Cloud
- **Thesis Alignment:** #11 Cloud for Small Software
- **Full Google Doc Spec:** [SmallCloud: Zero-Ops Cloud for Small Software & Micro-SaaS](https://docs.google.com/document/d/1iubHHzeGYTd_kYHKyCuLIc_kNsvzs_cD1i2SvL9xT7I/edit?usp=drivesdk&ouid=110227624285917376281)
- **Portfolio Strategy:** [00 - Startup & Open-Source Cross-Project Strategy](https://docs.google.com/document/d/12Wpg5mMGnjnl2uhMBTOR0QqRzQj8kwfzTLpvLoRuB5Q/edit?usp=drivesdk&ouid=110227624285917376281)

## 1. Goal & Problem
Deploy micro-SaaS apps from git push to HTTPS in <30s with built-in Litestream SQLite persistence and hard monthly spend-cap circuit breakers.

## 2. Target Customer (ICP) & Monetization
Solo founders, indie hackers, and AI-coding builders shipping 1-100k req/day apps.

## 3. Tech Stack
Go/Rust CLI + daemon, Docker/Linux cgroups v2, Caddy auto-HTTPS, Litestream SQLite -> GCS/S3.

## 4. Evaluation & Acceptance Thresholds
Deploy < 30s; cold-start < 300ms; 100% hard-stop enforcement at max_monthly_usd; 0 data loss on container restart.

## 5. Key Architectural Decision (ADR-001)
Optimize specifically for small software (Litestream SQLite + Caddy + cgroups) rather than heavy Kubernetes.

## 6. Day 1–5 Quickstart
- [x] Set up cli/, daemon/, and templates/ directories
- [x] Implement smallcloud.toml parser including max_monthly_usd budget cap
- [x] Build Go CLI packaging a sample FastAPI app into a container with Caddy reverse proxy
- [x] Test smallcloud rollback and verify zero-downtime route swap
