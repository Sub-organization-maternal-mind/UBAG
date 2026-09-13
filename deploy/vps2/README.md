# UBAG — VPS2 profile (213.163.201.37, ubag2.polytronx.com)

Second in-line production box: dedicated 4-core / 8GB Upcloud VPS running the
same stack as the primary (185.252.233.186) but fully self-contained — its own
Postgres and its own Nginx Proxy Manager edge. Compose:
`../../docker-compose.vps2.yml`.

Differences vs the primary (docker-compose.vps.yml):

| Concern    | Primary (185.252.233.186)      | VPS2 (213.163.201.37)                    |
|------------|--------------------------------|------------------------------------------|
| Postgres   | shared platform stack (/opt/platform) | compose service `postgres:16-alpine`   |
| Ingress    | shared NPM (~15 client sites)  | dedicated NPM at /opt/docker/nginx-proxy-manager |
| Domain     | ubag.polytronx.com             | ubag2.polytronx.com (Cloudflare proxied) |
| OET network| oetwebsite_internal (joined)   | none (OET lives on the primary only)     |
| Secrets    | deploy/vps/env.local           | deploy/vps/env.local (own values — fresh `UBAG_APP_SECRET`, `POSTGRES_PASSWORD`, DSN `postgres://...@postgres:5432/...`) |

Everything else (gateway, nginx-dashboard, live browser, chat-reaper, spool /
artifact / chat-ledger volumes, safe-mode, resource budget ≈2.25 CPU / ~3.5G)
mirrors the primary — see `deploy/vps/README.md`.

## One-time setup on the VPS

```sh
# 1. Docker (done 2026-09-13: get.docker.com → Docker 29.8.0)
curl -fsSL https://get.docker.com | sh

# 2. Repo tarball (git archive = tracked files only; commit first!)
mkdir -p /opt/docker/ubag
#   from the workstation: git archive HEAD -o ubag-vps2.tar && scp ubag-vps2.tar upcloud-prod:/tmp
cd /opt/docker/ubag && tar xf /tmp/ubag-vps2.tar

# 3. Dashboard bundle (dist/ is gitignored — build with the base path, ship tgz)
#   from the workstation, in an isolated worktree at HEAD:
#     git worktree add ../ubag-vps2-wt HEAD && cd ../ubag-vps2-wt && pnpm install --frozen-lockfile
#     cd apps/dashboard && UBAG_BASE_PATH=/dashboard pnpm build
#     tar czf ../../ubag-dist.tgz -C apps/dashboard dist && scp to VPS
#   on the VPS:
tar xzf /tmp/ubag-dist.tgz -C /opt/docker/ubag/apps/dashboard

# 4. env.local (generate secrets ON the VPS, never in chat)
cp deploy/vps/env.example deploy/vps/env.local
#   set:
#     UBAG_APP_SECRET=$(openssl rand -hex 32)
#     POSTGRES_PASSWORD=$(openssl rand -hex 24)
#     POSTGRES_DB=ubag  POSTGRES_USER=ubag
#     UBAG_POSTGRES_DSN=postgres://ubag:$POSTGRES_PASSWORD@postgres:5432/ubag?sslmode=disable
#     UBAG_ACTOR_ROLE / UBAG_PAT_ENABLED / worker knobs — mirror primary values
#   Basic Auth file (operator creds — same pattern as primary):
mkdir -p deploy/vps/nginx-dashboard
#     scp the primary's /opt/docker/ubag/deploy/vps/nginx-dashboard/.htpasswd here

# 5. Nginx Proxy Manager (dedicated edge, publishes host 80/443/81)
mkdir -p /opt/docker/nginx-proxy-manager && cd /opt/docker/nginx-proxy-manager
#   compose: jc21/nginx-proxy-manager:latest, ports 80/443/81, volumes ./data
#   + ./letsencrypt — creates the nginx-proxy-manager_default network the
#   dashboard joins. Admin UI: http://213.163.201.37:81 (set a strong password).

# 6. Bring the stack up
cd /opt/docker/ubag
docker compose -f docker-compose.vps2.yml --env-file deploy/vps/env.local up -d --build
```

## Wiring ubag2.polytronx.com (NPM admin UI, http://213.163.201.37:81)

- Proxy Host → Domain: `ubag2.polytronx.com`
- Forward Hostname/IP: `ubag-vps2-nginx-dashboard`, Forward Port: `80`
- SSL tab: request a new **Let's Encrypt** certificate, Force SSL, HTTP/2
  (same pattern proven on the primary for ubag.polytronx.com behind the
  Cloudflare orange cloud).

## Postgres notes

The gateway entrypoint applies `migrations/postgres/0001-0010` itself before
starting; migrations requiring `pg_partman` (0008+) fail and are tolerated —
same behavior as the small profile. Data persists in the `postgres_data`
named volume; back up with `pg_dump` like any compose service.

## Scope

Full production parity with the primary minus the OET integration: OpenAI
facade on `/v1/openai/*`, operator dashboard + Basic Auth, live manually
authenticated browser adapters (operator logs in via Browser Sessions widget
— provider sessions do NOT carry over from the primary, each box has its own
Chrome profile by design), localfs artifacts, file-spool executor.
