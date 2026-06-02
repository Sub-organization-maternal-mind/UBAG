---
title: GDPR Compliance Mapping
description: How UBAG controls support GDPR Article 25 (privacy by design) and Chapter IV processor obligations.
---

When UBAG is used to automate browser interactions involving EU residents' personal data,
operators act as data controllers (or processors, if acting on behalf of a controller).
This page maps UBAG controls to relevant GDPR obligations.

## Data minimization (Article 5(1)(c))

UBAG does not collect personal data on its own. Operators control what is captured:

- Configure artifact capture to collect only what is necessary: screenshots, DOM extracts, or HAR files can each be individually enabled/disabled per job
- Use `privacy_mode: "minimal"` to suppress artifact capture for jobs that don't need it

```bash
curl -X POST http://localhost:8081/v1/jobs \
  -H "Authorization: Bearer $UBAG_APP_SECRET" \
  -H "Ubag-Api-Version: 2026-05-22" \
  -d '{"job": {"target": "...", "command_type": "send_message", "privacy_mode": "minimal"}}'
```

See [Privacy Modes](/compliance/privacy-modes).

## Purpose limitation (Article 5(1)(b))

Job inputs and outputs are scoped to the submitting application's `app_id`. Cross-app
data access is blocked by RBAC. Use descriptive `purpose` tags on jobs for auditability:

```json
{ "job": { "target": "...", "tags": { "purpose": "user_data_export_request", "gdpr_basis": "data_subject_request" } } }
```

## Storage limitation (Article 5(1)(e))

Configure artifact and job data retention:

```toml
[retention]
job_data_days     = 30    # Job inputs/outputs deleted after 30 days
artifact_days     = 7     # Screenshots/HAR deleted after 7 days
audit_log_years   = 3     # Audit log retained 3 years (adjust per policy)
```

## Integrity and confidentiality (Article 5(1)(f))

| Control | UBAG Feature |
|---------|-------------|
| Encryption in transit | TLS 1.3 (all external); NATS mTLS (internal) |
| Encryption at rest | Garage S3 server-side encryption for artifacts |
| Access controls | RBAC/ABAC; MFA for human access |
| Audit trail | Hash-chained audit log |

## Processor obligations (Article 28)

If operating UBAG on behalf of a data controller:

- Process personal data only on documented instructions
- Ensure sub-processors (cloud, Garage S3 provider) provide equivalent guarantees
- Assist the controller with data subject rights requests (see below)

## Data subject rights

| Right | UBAG capability |
|-------|----------------|
| Access (Article 15) | Query job history by `app_id` + filter; export artifacts |
| Erasure (Article 17) | `DELETE /v1/jobs/{id}` removes job data + artifacts; purge audit log entries subject to legal retention requirements |
| Portability (Article 20) | Export jobs as JSON via `/v1/jobs?format=export` |
| Restriction (Article 18) | Tag jobs with `processing_restricted: true`; ABAC policy blocks further processing |

## Data transfer outside EU

If running UBAG workers outside the EU/EEA, ensure an adequacy decision or SCCs cover the transfer.
Use [region pinning](/cookbook/11-pin-region) to constrain EU resident data to EU regions.

```bash
# Ensure EU data stays in EU
curl -X PATCH http://localhost:8081/v1/apps/$APP_ID \
  -d '{"default_region": "eu-west-1", "region_strict": true}'
```

## Records of processing (Article 30)

The UBAG audit log and job history constitute a partial record of processing activities.
Export and retain them per your Records of Processing Activity (RoPA) requirements.

## Operator checklist

- [ ] Retention policy configured for jobs and artifacts
- [ ] Privacy mode enabled for jobs that don't require artifact capture
- [ ] Region pinning configured for EU-resident data
- [ ] Audit log exported and retained per data retention policy
- [ ] DPA/SCCs in place with infrastructure sub-processors
- [ ] Data subject rights request procedure documented
