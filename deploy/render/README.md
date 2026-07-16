# UBAG on Render

A Render Blueprint (`render.yaml`) covering the gateway API, operator
dashboard, and public docs — the API + dashboard profile, not live-browser
automation. See the header comment in `render.yaml` for exactly what's
excluded relative to `docker-compose.small.yml` and why.

## What gets deployed

| Service | Render type | Public? |
|---|---|---|
| `gateway` | Private Service (Docker) | No — only `ubag-dashboard` can reach it |
| `ubag-dashboard` | Web Service (Docker, nginx) | Yes — the only public entry point |
| `ubag-docs` | Static Site | Yes |
| `ubag-postgres` | Render Postgres | No |

Object storage (`UBAG_ARTIFACT_STORE=minio`) points at an external
S3-compatible bucket rather than a MinIO container Render would have to run
for you — Cloudflare R2 is a good default (S3-compatible API, free egress).

## Before you deploy

1. **Create an R2 (or other S3-compatible) bucket** and an API token scoped to
   it. You'll need: endpoint host (no scheme, e.g.
   `<account-id>.r2.cloudflarestorage.com`), access key, secret key.
2. **Generate a strong `UBAG_APP_SECRET`** (e.g. `openssl rand -hex 32`) — you
   set the *same* value for both `UBAG_APP_SECRET` (gateway) and
   `UBAG_GATEWAY_SECRET` (dashboard) when prompted; Render treats them as
   independent secrets and won't cross-wire them for you.
3. **Pick an operator username/password** for the dashboard's Basic Auth gate
   (`UBAG_OPERATOR_USER` / `UBAG_OPERATOR_PASSWORD`).

## Deploy

1. Push this branch to GitHub/GitLab.
2. In the Render Dashboard: **New > Blueprint**, connect the repo, and point
   it at `deploy/render/render.yaml` when asked for the Blueprint file path.
3. Render parses the file and prompts you for every `sync: false` value
   (the secrets from step "Before you deploy" above, plus the Postgres
   database is provisioned automatically — no manual DSN needed).
4. Apply. Render builds the gateway image, builds+serves the dashboard, and
   deploys the docs static site. First build is the slowest (Go module
   download + Node/pnpm install); later pushes are incremental.
5. Visit `https://ubag-dashboard-<random>.onrender.com/dashboard/` (or your
   custom domain), sign in with the operator credentials, and confirm the
   gateway is reachable (dashboard settings should resolve without a manual
   gateway URL — it defaults to same-origin).

## What's intentionally not wired up yet

- **Live-browser automation** (`UBAG_WORKER_CONSUMER_ENABLED`, real
  ChatGPT/Gemini/etc. sessions): jobs are accepted and durably queued
  (`UBAG_EXECUTOR_MODE=file`) but nothing executes them. Turning this on
  needs a persistent, manually-logged-in Chrome reachable over CDP — which
  doesn't fit Render's stateless/public-by-default model as cleanly as a
  dedicated host. If/when you need it, that's a follow-up decision, not
  something to flip silently.
- **Webhooks** (`UBAG_WEBHOOK_WORKER_ENABLED`): off by default; set the
  secret and flip the flag once you have a real target to sign deliveries
  for.
- **Observability** (Prometheus/Grafana/etc. from the small profile): use
  Render's built-in per-service metrics/logs instead of self-hosting a stack
  on top of a PaaS.

## Rotating secrets

Render Blueprints only prompt for `sync: false` values on the *initial*
creation flow — re-syncing an existing Blueprint won't re-prompt. Rotate a
secret from each service's **Environment** tab in the Render Dashboard
directly. For the dashboard operator password specifically, changing
`UBAG_OPERATOR_PASSWORD` takes effect on the next deploy/restart (the hash is
regenerated at container start by `nginx-dashboard/40-generate-htpasswd.sh`).
