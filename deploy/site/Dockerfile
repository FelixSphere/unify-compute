# The marketing site (compute.unifyai.us) as a static export behind Caddy, and
# the customer app's pages for app.compute.unifyai.us from the same Caddy.
#
# Build from the repository root:
#   docker build -f deploy/site/Dockerfile --build-arg SITE_BUILD_ID=$(git rev-parse HEAD) .
#
# Both base images are pinned by digest: the tag is for the reader, the digest
# is what is pulled. Both are multi-arch indexes that carry linux/arm64, which
# the t4g hosts need. Bump them together with the tag comment.

# node:22.23.3-alpine. On the build platform, not the target: the export is
# static files, identical on every architecture, and building it under QEMU
# for arm64 outran publish-image.yml's 30-minute timeout on 2026-10-02.
FROM --platform=$BUILDPLATFORM node:22.23.3-alpine@sha256:0a7108bf6c7bf5de370ffb1a3ed6be93d405b43ff159f681a8d18c0e2bc2e402 AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
# The *.txt files are the client-side navigation payloads; without them every
# link click degrades to a full page load.
RUN npm run build \
 && test -f dist/client/index.html \
 && test -f dist/client/index.txt \
 && test -f dist/client/privacy.html \
 && test -f dist/client/privacy.txt \
 && test -f dist/client/start.html \
 && test -f dist/client/start.txt \
 && test -f dist/client/marketplace.html \
 && test -f dist/client/marketplace.txt \
 && test -f dist/client/marketplace/batch-embed.html \
 && test -f dist/client/marketplace/batch-embed.txt \
 && test -f dist/client/404.html \
 && for lang in zh zh-hant ja ko es fr de pt-br ar; do \
      for page in "$lang" "$lang/start" "$lang/marketplace" "$lang/marketplace/batch-embed" "$lang/privacy"; do \
        test -f "dist/client/$page.html" || { echo "missing dist/client/$page.html"; exit 1; }; \
      done; \
    done \
 && test -f dist/client/sitemap.xml \
 && test -f dist/client/robots.txt \
 && rm -rf dist/client/.vite dist/client/vinext-client-entry-manifest.json \
 && for page in index signup login verify forgot-password reset-password 404 keys jobs job usage billing account marketplace listings members accept-invitation; do \
      test -f "dist/app/$page.html" || { echo "missing dist/app/$page.html"; exit 1; }; \
    done \
 && test -f dist/app/brand/unifyai-lockup-horizontal-black.svg \
 && ! grep -lE '<script>|<script type="module">|<style' dist/app/*.html

# caddy:2.11.4-alpine
FROM caddy:2.11.4-alpine@sha256:6aeddd44c3078b0f9a35206472a11420648a79c184603ef95957d0a20044cb2b
# Only labels the image; the build does not read it.
ARG SITE_BUILD_ID=dev
# Caddy still opens its data and config dirs with admin and TLS off. The
# image's /data and /config are root-owned volumes; point it somewhere the
# unprivileged user can write instead of running as root.
ENV XDG_DATA_HOME=/tmp/caddy-data XDG_CONFIG_HOME=/tmp/caddy-config
COPY deploy/site/Caddyfile /etc/caddy/Caddyfile
COPY --from=build /src/dist/client /srv
# The app host's root (app.compute.unifyai.us): the dashboard pages, a
# separate Vite build (dashboard/vite.config.ts) with its own copies of the
# brand files, so nothing on that host is served from the marketing root.
COPY --from=build /src/dist/app /srv-app
LABEL org.opencontainers.image.source="https://github.com/FelixSphere/unify-compute" \
      org.opencontainers.image.description="compute.unifyai.us marketing site and app.compute.unifyai.us pages (static, served by Caddy)" \
      org.opencontainers.image.revision="${SITE_BUILD_ID}"
EXPOSE 8080
USER 65534:65534
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
CMD ["caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]
