# Production Readiness Roadmap

This roadmap is designed to move the current system from beta-grade to production-grade operation.

## Current Assessment

- Current level: Internal beta / pilot ready
- Not yet sufficient for paid external production usage
- Main gaps: reliability, security hardening, observability, DR, compliance, and load validation

## P0 (Week 0-2): Must Have Before External Users

### 1) Security Baseline

- Enforce TLS for all external traffic (API and web frontend)
- Store secrets in a dedicated secret manager (no static plaintext secrets in runtime configs)
- Add login and send API rate limits (IP + account based)
- Add account lockout and brute-force mitigation
- Restrict CORS to explicit allowlist
- Add dependency vulnerability scanning in CI

Definition of done:
- TLS required in all non-local environments
- Secrets are rotated and not hardcoded in deployment files
- Rate-limit policies are documented and tested
- CI fails on critical/high vulnerabilities

### 2) Data Protection and Backup

- Enable automated backups for SQLite and archive DB
- Define backup retention and restore point objective (RPO)
- Run restore drills and verify data integrity
- Encrypt backup artifacts at rest

Definition of done:
- Daily backup jobs verified for 7 consecutive days
- Successful restore test from latest and older backup snapshots
- Recovery runbook documented

### 3) Observability and Incident Readiness

- Add structured logs (request ID, user ID, action, latency, status)
- Export metrics: auth failures, send throughput, LMTP errors, queue depth, latency percentiles
- Configure alerting (error rate, p95 latency, DB health, disk usage)
- Prepare incident runbook and on-call escalation flow

Definition of done:
- Alerts tested using synthetic failures
- Dashboards exist for API, LMTP, SMTP send path, DB
- Runbook can be followed by a non-author engineer

### 4) Reliability Guardrails

- Add health/readiness checks across all services
- Add retry and timeout policy for external mail relay interactions
- Add graceful shutdown and in-flight request handling checks
- Add idempotency strategy for send API to avoid duplicates

Definition of done:
- Rolling restart does not lose in-flight critical operations
- Verified retry behavior without duplicate sends

## P1 (Week 3-6): Scale and Operability

### 1) Large-File and Throughput Hardening

- Introduce async send pipeline with queue-backed workers
- Add chunked upload or object storage offload for very large attachments
- Stream attachment processing end-to-end where possible
- Define and enforce tenant/account quotas

Definition of done:
- Load test profile includes large attachment scenarios
- System remains within SLO under target concurrency
- Queue and worker backlog alerts in place

### 2) Database and Schema Operations

- Adopt explicit migration tooling for schema changes
- Add zero-downtime migration playbook
- Add index tuning based on real query patterns

Definition of done:
- Migration rollback tested in staging
- Query performance baseline documented

### 3) CI/CD and Release Safety

- Add branch protection and required checks
- Add build, test, lint, security scan, and image scan gates
- Add automated staging deploy and smoke tests
- Define release and rollback checklist

Definition of done:
- Release can be promoted with one command/pipeline
- Rollback completed in rehearsal within agreed target time

## P2 (Week 7-12): Enterprise Readiness

### 1) Compliance and Governance

- Define data retention and deletion policies per mailbox/direction
- Add immutable audit trails and access review process
- Map controls to your target standard (e.g., ISO 27001/SOC2-lite internal controls)

Definition of done:
- Policy docs approved by stakeholders
- Periodic access review workflow is running

### 2) Multi-Environment and Capacity Planning

- Separate dev/stage/prod with immutable deployment templates
- Capacity model and cost forecast for expected growth
- Chaos and failover testing (DB outage, relay outage, disk pressure)

Definition of done:
- Failover test results documented with remediation actions
- Capacity review cadence established

### 3) Product and Support Operations

- Add operational admin tools (search/replay/retry where safe)
- Define support SLAs and customer communication templates
- Add usage analytics for feature adoption and bottlenecks

Definition of done:
- Support team can triage incidents using documented tools and playbooks

## Suggested SLO Starter Set

- API availability: 99.9%
- Send success rate: 99.5% (excluding downstream relay provider incidents)
- p95 send API latency: under 1.5s for metadata-only messages
- p95 inbox listing latency: under 500ms at target data size
- RPO: <= 15 minutes, RTO: <= 60 minutes

## Immediate Next 5 Tasks (This Week)

1. Add rate limiting and account lockout for authentication and send endpoints.
2. Add structured logging and request correlation IDs.
3. Add backup + restore automation and run one restore drill.
4. Add CI vulnerability and container image scanning gates.
5. Define SLO dashboards and wire first alerts.
