# Stage 1: Build frontend
FROM node:22-slim AS frontend
RUN corepack enable && corepack prepare pnpm@10.32.1 --activate
WORKDIR /app/web
COPY web/package.json web/pnpm-lock.yaml ./
COPY web/vendor/foliate-js ./vendor/foliate-js
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile
COPY web/ .
RUN pnpm run build

# Simple Node.js proxy that serves the built SPA and forwards /api/ requests
# plus WebSocket upgrades to the Go backend. Replaces vite preview so the
# proxy config in vite.config.ts actually works at runtime.
COPY web/server.js ./server.js
CMD ["node", "server.js"]

# Allow CI to inject prebuilt frontend assets via a named `frontend_dist`
# context while local builds keep using the in-Docker frontend stage.
FROM scratch AS frontend_dist
COPY --from=frontend /app/web/dist/. /

# Stage 2: Build Go binary
FROM golang:1.26 AS build
ENV CGO_ENABLED=1
ENV GOPROXY=https://proxy.golang.org,direct
ENV GOPRIVATE=github.com/Silo-Server/*
ENV GONOSUMDB=github.com/Silo-Server/*
RUN apt-get update && apt-get install -y --no-install-recommends libvips-dev && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY go.mod go.sum ./
COPY internal/compat/zishang520-webtransport-go/ internal/compat/zishang520-webtransport-go/
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    bash -c 'echo "==> go mod download: $(wc -l < go.sum | tr -d " ") module checksum entries" && \
    if getent hosts proxy.golang.org >/dev/null 2>&1; then echo "==> proxy.golang.org resolves"; else echo "==> WARNING: proxy.golang.org does not resolve; direct VCS fallback will be very slow"; fi && \
    timeout 1200 go mod download -x; rc=$?; \
    if [ "$rc" -eq 124 ]; then echo "==> go mod download TIMED OUT after 20 minutes; partial progress is kept in the cache mount, re-run the build to resume"; else echo "==> go mod download complete (exit $rc)"; fi; \
    exit $rc'
COPY web/embed.go web/embed.go
COPY --from=frontend_dist / web/dist
COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
# The settings contract is a Go package (contracts/settings/v1) that embeds the
# manifest, so the binary carries the exact bytes it was built from. It lives
# outside internal/ because clients vendor these files.
COPY contracts/ contracts/
ARG BUILD_REVISION
ARG BUILD_DIRTY=false
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build \
    -ldflags "-X github.com/Silo-Server/silo-server/internal/buildinfo.revisionOverride=${BUILD_REVISION} -X github.com/Silo-Server/silo-server/internal/buildinfo.dirtyOverride=${BUILD_DIRTY}" \
    -o /silo ./cmd/silo/

# Stage 3: Runtime
FROM debian:bookworm-slim
ARG TARGETARCH
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl gnupg && \
    curl -fsSL https://repo.jellyfin.org/jellyfin_team.gpg.key \
      | gpg --dearmor -o /usr/share/keyrings/jellyfin.gpg && \
    echo "deb [signed-by=/usr/share/keyrings/jellyfin.gpg arch=${TARGETARCH}] https://repo.jellyfin.org/debian bookworm main" \
      > /etc/apt/sources.list.d/jellyfin.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends jellyfin-ffmpeg7 git libvips42 fonts-noto-core fonts-noto-cjk && \
    apt-get purge -y gnupg && apt-get autoremove -y && \
    rm -rf /var/lib/apt/lists/*
RUN mkdir -p /tmp/silo-transcode /var/lib/silo/compat/jellyfin-web
COPY --from=frontend /usr/local/bin/node /usr/local/bin/node
COPY --from=frontend /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
RUN ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm && \
    ln -sf ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
COPY --from=build /silo /usr/local/bin/silo
EXPOSE 8080 8096 13378
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:${PORT:-8080}/api/v1/health || exit 1
ENTRYPOINT ["silo"]
