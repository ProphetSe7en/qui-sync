# qui-sync — multi-stage build
# Runs the HTTP server (cmd/server/) on port 6070.
# PUID/PGID/UMASK env vars honoured by the entrypoint so host-owned files
# don't end up root-owned.

FROM golang:1.24-alpine AS builder

WORKDIR /src

# Bundle the cronstrue client-side cron-to-human library into the static
# assets before `go build` captures them into the embed.FS. Doing this
# in the builder stage (not at runtime) means the final image has no
# network dependency — cronstrue is version-pinned and ships inside.
RUN apk add --no-cache wget

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN wget -q -O cmd/server/web/static/cronstrue.min.js \
    "https://cdn.jsdelivr.net/npm/cronstrue@2.50.0/dist/cronstrue.min.js"

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/qui-sync-server ./cmd/server

# -----------------------------------------------------------------------------

FROM alpine:3.21

# Runtime deps: ca-certificates (HTTPS to Qui), git (for sync subscriptions),
# tzdata (timestamps in changelog), su-exec (drop privileges in entrypoint).
RUN apk add --no-cache ca-certificates git tzdata su-exec && \
    addgroup -g 100 users 2>/dev/null || true && \
    adduser -u 99 -G users -D -H -s /sbin/nologin nobody 2>/dev/null || true

COPY --from=builder /out/qui-sync-server /usr/local/bin/qui-sync-server
COPY entrypoint.sh                       /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

VOLUME ["/config", "/data"]
WORKDIR /config

EXPOSE 6070

ENV PUID=99 \
    PGID=100 \
    UMASK=002

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["--addr", ":6070", "--config", "/config/config.yml"]
