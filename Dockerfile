# syntax=docker/dockerfile:1@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89

# --- Operator console build stage ---
FROM node:22-bookworm-slim@sha256:53ada149d435c38b14476cb57e4a7da73c15595aba79bd6971b547ceb6d018bf AS operator-console-build

WORKDIR /workspace/apps/operator-console
COPY apps/operator-console/package.json apps/operator-console/package-lock.json ./
RUN npm ci
COPY apps/operator-console/ ./
COPY design/operator-console/contracts /workspace/design/operator-console/contracts
RUN npm run parity && npm run build

# --- Operator console static bundle for server embed ---
FROM operator-console-build AS operator-console-static-build
RUN npm run generate

# --- Go build stage ---
FROM golang:1.25.12-bookworm@sha256:a9c020ee3d1508c7be5435c262434e3d3fc1d0e76a11afeb9ddae7d60bc86aa4 AS builder

WORKDIR /src

ENV CGO_ENABLED=1
ENV GOFLAGS=""

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Copy generated operator-console static bundle into static/ for go:embed.
# This keeps apps/operator-console as the single frontend source of truth.
COPY --from=operator-console-static-build /workspace/apps/operator-console/.output/public/ internal/worker/static/

ARG VERSION
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# VERSION is data, never shell source. Direct Docker builds must satisfy the
# same whitelist as the release workflow before the value reaches ldflags or
# OCI labels. Numeric prerelease identifiers follow the SemVer leading-zero
# rule; build metadata is intentionally unsupported.
RUN set -eu; \
    release_pattern='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'; \
    commit_pattern='^sha-[0-9a-f]{40}$'; \
    if printf '%s\n' "$VERSION" | grep -Eq "$commit_pattern"; then \
        :; \
    elif printf '%s\n' "$VERSION" | grep -Eq "$release_pattern"; then \
        case "$VERSION" in \
            *-*) prerelease="${VERSION#*-}"; old_ifs="$IFS"; IFS='.'; \
                 for identifier in $prerelease; do \
                     if printf '%s\n' "$identifier" | grep -Eq '^[0-9]+$' \
                        && [ "${#identifier}" -gt 1 ] \
                        && [ "${identifier#0}" != "$identifier" ]; then \
                         echo "invalid numeric prerelease identifier in VERSION" >&2; exit 64; \
                     fi; \
                 done; IFS="$old_ifs" ;; \
        esac; \
    else \
        echo "VERSION must be canonical SemVer or sha-<40 lowercase hex>" >&2; exit 64; \
    fi

# Build the accepted CGO server. The ldd transcript is retained in the image as
# auditable proof that every shared-library dependency resolves before the
# binary crosses into the distroless runtime stage.
RUN CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -tags fts5 \
    -ldflags "-X main.Version=${VERSION} -s -w" -o /out/engram-server ./cmd/engram-server \
    && ldd /out/engram-server > /out/engram-server.ldd 2>&1 \
    && ! grep -q "not found" /out/engram-server.ldd \
    && grep -q "=>" /out/engram-server.ldd

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w" -o /out/engram-healthcheck ./cmd/engram-healthcheck \
    && ! ldd /out/engram-healthcheck > /out/engram-healthcheck.ldd 2>&1 \
    && grep -q "not a dynamic executable" /out/engram-healthcheck.ldd \
    && install -d -m 0700 /out/server-home

# Build client-side binary for the existing release target.
RUN CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -tags fts5 \
    -ldflags "-X main.Version=${VERSION} -X github.com/thebtf/engram/internal/version.Daemon=${VERSION} -s -w" \
    -o /out/engram ./cmd/engram

# --- Server image ---
FROM gcr.io/distroless/base-debian13@sha256:b78832f41c8128046807c24840ebee4f1c18ba7870eed423d8750c272c15e147 AS server

COPY --from=builder --chown=65532:65532 --chmod=0755 /out/engram-server /usr/local/bin/engram-server
COPY --from=builder --chown=65532:65532 --chmod=0755 /out/engram-healthcheck /usr/local/bin/engram-healthcheck
COPY --from=builder --chown=65532:65532 --chmod=0444 /out/engram-server.ldd /usr/share/engram/engram-server.ldd
COPY --from=builder --chown=65532:65532 --chmod=0700 /out/server-home /var/lib/engram

ENV ENGRAM_WORKER_HOST=0.0.0.0
ENV ENGRAM_WORKER_PORT=37777
ENV HOME=/var/lib/engram

EXPOSE 37777

HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=3 \
    CMD ["/usr/local/bin/engram-healthcheck", "http://127.0.0.1:37777/api/ready"]

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/engram-server"]

# --- Operator console image ---
FROM gcr.io/distroless/nodejs22-debian13@sha256:773a62fbe24a3f8c8b24b16fd59154627f8b406737bc906f83bf1732bc8907dd AS operator-console

WORKDIR /app

COPY --from=operator-console-build --chown=65532:65532 /workspace/apps/operator-console/.output ./.output
COPY --from=builder --chown=65532:65532 --chmod=0755 /out/engram-healthcheck /usr/local/bin/engram-healthcheck

ENV NITRO_HOST=0.0.0.0
ENV NITRO_PORT=3000
ENV NUXT_PUBLIC_API_BASE=/api
ENV NUXT_OPERATOR_API_TARGET=http://server:37777

EXPOSE 3000

HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=3 \
    CMD ["/usr/local/bin/engram-healthcheck", "http://127.0.0.1:3000/api/ready"]

USER 65532:65532
CMD [".output/server/index.mjs"]
