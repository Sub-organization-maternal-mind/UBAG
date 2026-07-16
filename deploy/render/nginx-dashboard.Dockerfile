# UBAG ingress for Render: builds the dashboard SPA, then serves it via nginx
# alongside the gateway API proxy — see nginx-dashboard/default.conf.template
# for the routing/security model. Adapted from deploy/small/nginx-dashboard's
# docker-compose service, which relies on Docker bind-mounts (dist/, .htpasswd)
# that don't exist on Render: this image builds the dashboard itself and
# generates .htpasswd from env vars at container start instead.

FROM node:25-alpine AS dashboard-build
WORKDIR /src
RUN corepack enable
COPY . .
# Emits hashed assets under /dashboard/_app/, matching the nginx location
# blocks below — must match at build time, nginx can't rewrite this after.
ENV UBAG_BASE_PATH=/dashboard
RUN pnpm install --frozen-lockfile \
 && pnpm --filter @ubag/dashboard build

FROM nginx:1.27-alpine

COPY --from=dashboard-build /src/apps/dashboard/dist /srv/dashboard
COPY deploy/render/nginx-dashboard/default.conf.template /etc/nginx/templates/default.conf.template
COPY deploy/render/nginx-dashboard/40-generate-htpasswd.sh /docker-entrypoint.d/40-generate-htpasswd.sh
RUN chmod +x /docker-entrypoint.d/40-generate-htpasswd.sh

# Restricts nginx:alpine's built-in envsubst-on-templates entrypoint step to
# UBAG_-prefixed vars, so nginx variables like $host/$uri survive untouched.
ENV NGINX_ENVSUBST_FILTER=UBAG_

EXPOSE 80
