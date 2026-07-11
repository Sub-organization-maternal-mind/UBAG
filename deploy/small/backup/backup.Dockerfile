# Backup runner image for the `backup` compose profile.
#
# Carries BOTH toolchains the backup scripts need, pinned and baked in:
#   - Postgres client tools (pg_dump / pg_basebackup) from postgres:16-alpine
#   - the MinIO client (mc) for SigV4-authenticated uploads to OFF-HOST S3
#
# The previous services ran `apk add --no-cache curl` at every container start
# and shelled out `curl -X PUT -u key:secret` — HTTP Basic Auth, which MinIO's
# S3 API rejects (it requires AWS SigV4), so every upload silently failed. mc
# computes SigV4 for us and is copied in at build time (no runtime downloads).
#
# mc is a static (CGO-free) Go binary, so it runs unmodified on the musl-based
# postgres:16-alpine base. Tag pinned to match minio-init in docker-compose.small.yml.
FROM minio/mc:RELEASE.2025-04-16T18-13-26Z AS mc

FROM postgres:16-alpine
COPY --from=mc /usr/bin/mc /usr/local/bin/mc
RUN mc --version
