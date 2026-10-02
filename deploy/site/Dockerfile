# The marketing site (compute.unifyai.us) as a static export behind Caddy.
#
# Build from the repository root:
#   docker build -f deploy/site/Dockerfile --build-arg SITE_BUILD_ID=$(git rev-parse HEAD) .
#
# Both base images are pinned by digest: the tag is for the reader, the digest
# is what is pulled. Both are multi-arch indexes that carry linux/arm64, which
# the t4g hosts need. Bump them together with the tag comment.

# node:22.23.3-alpine
FROM node:22.23.3-alpine@sha256:0a7108bf6c7bf5de370ffb1a3ed6be93d405b43ff159f681a8d18c0e2bc2e402 AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
# next.config.ts turns this into vinext's deploymentId; the Caddyfile echoes
# it on client-side navigation payloads. They must be the same value.
ARG SITE_BUILD_ID=dev
ENV SITE_BUILD_ID=${SITE_BUILD_ID}
RUN npm run build \
 && test -f dist/client/index.html \
 && test -f dist/client/privacy.html \
 && test -f dist/client/404.html \
 && rm -rf dist/client/.vite dist/client/vinext-client-entry-manifest.json

# caddy:2.11.4-alpine
FROM caddy:2.11.4-alpine@sha256:6aeddd44c3078b0f9a35206472a11420648a79c184603ef95957d0a20044cb2b
ARG SITE_BUILD_ID=dev
ENV SITE_BUILD_ID=${SITE_BUILD_ID}
# Caddy still opens its data and config dirs with admin and TLS off. The
# image's /data and /config are root-owned volumes; point it somewhere the
# unprivileged user can write instead of running as root.
ENV XDG_DATA_HOME=/tmp/caddy-data XDG_CONFIG_HOME=/tmp/caddy-config
COPY deploy/site/Caddyfile /etc/caddy/Caddyfile
COPY --from=build /src/dist/client /srv
LABEL org.opencontainers.image.source="https://github.com/FelixSphere/unify-compute" \
      org.opencontainers.image.description="compute.unifyai.us marketing site (static export served by Caddy)" \
      org.opencontainers.image.revision="${SITE_BUILD_ID}"
EXPOSE 8080
USER 65534:65534
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
CMD ["caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]
