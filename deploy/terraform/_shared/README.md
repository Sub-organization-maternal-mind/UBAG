# UBAG Shared Terraform Module: Remote Backend + DR Variables

This directory contains shared Terraform configuration used across all UBAG
cloud-provider modules (`aws/`, `gcp/`, `azure/`, `hetzner/`, `digitalocean/`).

---

## 1. Remote State Backend (`backend.tf`)

`backend.tf` contains commented-out backend blocks for AWS S3, GCP GCS, and
Azure Blob. To enable remote state:

1. Copy `backend.tf` into your cloud-specific module directory (e.g. `aws/`).
2. Uncomment the block matching your provider.
3. Replace placeholder resource names (bucket, storage account, etc.) with your
   actual infrastructure names.
4. Run `terraform init -reconfigure` to activate the backend.

**Never commit credentials.** Supply authentication through environment
variables or your cloud provider's identity mechanism:

| Provider | Recommended auth |
|----------|------------------|
| AWS      | `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` env vars, instance role, or OIDC |
| GCP      | `GOOGLE_APPLICATION_CREDENTIALS` or Workload Identity |
| Azure    | `ARM_CLIENT_ID` / `ARM_CLIENT_SECRET` env vars or Managed Identity |

### AWS: Pre-requisite resources

Before running `terraform init` with the S3 backend, create the state bucket
and lock table (one-time setup):

```bash
aws s3api create-bucket \
  --bucket ubag-terraform-state \
  --region us-east-1

aws s3api put-bucket-versioning \
  --bucket ubag-terraform-state \
  --versioning-configuration Status=Enabled

aws dynamodb create-table \
  --table-name ubag-terraform-locks \
  --attribute-definitions AttributeName=LockID,AttributeType=S \
  --key-schema AttributeName=LockID,KeyType=HASH \
  --billing-mode PAY_PER_REQUEST \
  --region us-east-1
```

### Migrating between backends

```bash
terraform state pull > backup.tfstate   # always back up first
terraform init -migrate-state
```

---

## 2. DR Variables (`variables.tf`)

`variables.tf` declares shared disaster-recovery variables. Reference these in
a cloud module by either:

**Option A — copy the file** into the cloud module directory and set values via
`terraform.tfvars` or `-var` flags.

**Option B — module reference** (when `_shared` is treated as a child module):

```hcl
module "dr_vars" {
  source = "../_shared"

  backup_bucket          = "my-ubag-backups"
  wal_archive_bucket     = "my-ubag-wal"
  wal_archive_prefix     = "wal-archive"
  replica_count          = 3
  postgres_replica_count = 2
  rto_minutes            = 15
  rpo_minutes            = 5
}
```

### Variable reference

| Variable | Default | Purpose |
|----------|---------|---------|
| `backup_bucket` | `""` | Object-storage bucket for nightly UBAG backups |
| `wal_archive_bucket` | `""` | Bucket used by Postgres `archive_command` for WAL shipping |
| `wal_archive_prefix` | `"wal-archive"` | Key prefix within `wal_archive_bucket` |
| `replica_count` | `2` | Number of UBAG gateway replicas (HA sizing) |
| `postgres_replica_count` | `1` | Number of Postgres read replicas |
| `rto_minutes` | `30` | Recovery Time Objective (informational) |
| `rpo_minutes` | `5` | Recovery Point Objective (informational) |

---

## 3. DR Runbook

### RTO and RPO

- **RTO (Recovery Time Objective)** — maximum acceptable time between a
  failure event and full service restoration. Default: 30 minutes. Reduce by
  increasing `replica_count` and using active-active multi-region setups.

- **RPO (Recovery Point Objective)** — maximum acceptable data loss measured
  in time. Default: 5 minutes. Achieved by continuous WAL archiving; lower
  values require streaming replication with synchronous commits.

### WAL Archive Setup (PostgreSQL)

1. Set `wal_archive_bucket` and `wal_archive_prefix` to point at your object
   store.
2. Configure Postgres `postgresql.conf`:
   ```
   wal_level = replica
   archive_mode = on
   archive_command = 'aws s3 cp %p s3://<wal_archive_bucket>/<wal_archive_prefix>/%f'
   ```
   (Adjust the command for GCS or Azure Blob.)
3. Verify archiving is active:
   ```sql
   SELECT * FROM pg_stat_archiver;
   ```

### Recovery procedure

1. Provision a new Postgres instance from the latest base backup.
2. Restore WAL segments from `wal_archive_bucket`:
   ```
   restore_command = 'aws s3 cp s3://<bucket>/<prefix>/%f %p'
   ```
3. Apply WAL segments until the target recovery time.
4. Promote the replica: `pg_ctl promote` or `SELECT pg_promote();`
5. Update application DSN to point at the recovered instance.
6. Run smoke tests and re-enable traffic.

Full Phase 7 restore automation will invoke the steps above automatically.
