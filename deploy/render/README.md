# UBAG on Render

A Render Blueprint (`render.yaml`) covering the gateway API, operator
dashboard, and public docs — the API + dashboard profile, not live-browser
automation, **entirely on Render's free plan ($0/month on Render itself)**.
See the header comment in `render.yaml` for exactly what's excluded relative
to `docker-compose.small.yml` and why, and for the specific tradeoffs the
free plan makes (no persistent disk, spin-down on idle, Postgres expiry).

## What gets deployed

| Service | Render type | Plan | Public? |
|---|---|---|---|
| `gateway` | Web Service (Docker) | Free | Yes — private services have no free tier, so this isn't network-private like a typical small-profile deploy. Every request still needs the `UBAG_APP_SECRET` bearer token regardless. |
| `ubag-dashboard` | Web Service (Docker, nginx) | Free | Yes — the recommended entry point (Basic Auth + secret injection) |
| `ubag-docs` | Static Site | Free (unconditionally — static sites have no paid tier) | Yes |
| `ubag-postgres` | Render Postgres | Free | No |

Free-tier tradeoffs worth knowing before you rely on this:
- `gateway` and `ubag-dashboard` **spin down after 15 minutes idle** and
  cold-start (a few seconds) on the next request.
- Neither can attach a **persistent disk** — that's why the executor runs in
  `noop` mode (jobs are accepted, nothing queues them for execution yet).
- `ubag-postgres` **expires 30 days after creation** (14-day grace period,
  then deleted with all data) unless you upgrade it to a paid instance type
  first. Fine for trying this out; don't leave real data on it unattended.

Object storage (`UBAG_ARTIFACT_STORE=minio`) points at an external
S3-compatible bucket rather than a MinIO container Render would have to run
for you — Cloudflare R2 (own separate free tier: 10GB storage, 1M/10M ops
per month) is the default this profile assumes.

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
  ChatGPT/Gemini/etc. sessions): jobs are accepted but not queued at all
  (`UBAG_EXECUTOR_MODE=noop` — free services can't attach the disk a real
  queue would need). Turning this on for real needs a paid plan (for a disk
  or a managed queue) plus a persistent, manually-logged-in Chrome reachable
  over CDP — which doesn't fit Render's model as cleanly as a dedicated host
  regardless of plan. If/when you need it, that's a follow-up decision, not
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
