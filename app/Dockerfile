# syntax=docker/dockerfile:1
#
# Build context for this Dockerfile is the `app/` directory (see
# docker-compose.yml and scripts/bootstrap-local-dev.sh), so paths below
# are relative to app/.

# ---- Build stage ---------------------------------------------------------
# golang:1.26-alpine keeps the builder image itself small and gives a
# libc-free base to build against, which matters because the final stage
# below has no libc at all.
FROM golang:1.26-alpine AS builder

WORKDIR /build

# Copied separately from the rest of the source so `go mod download` is its
# own Docker layer, cached independently of source changes. pulse-api has
# zero third-party dependencies today (see internal/metrics's package doc
# for why), so this step is currently a no-op — it stays in place because
# it becomes exactly the right behavior the moment a real dependency is
# added, with no Dockerfile change required.
COPY go.mod ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a fully static binary with no libc dependency,
# which is what makes running on a "static" distroless base (no shared
# libraries at all) possible. -trimpath removes local build-machine
# filesystem paths from the binary. -ldflags="-s -w" strips the DWARF
# debug/symbol tables pulse-api doesn't need at runtime, shrinking the
# binary. TARGETOS/TARGETARCH are populated automatically by BuildKit,
# including under `docker buildx build --platform=linux/amd64,linux/arm64` —
# the multi-arch path the architecture's Kubernetes Design section calls
# for so the service can run on Graviton/Arm64 nodes later.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/pulse-api ./src/cmd/pulse-api

# ---- Final stage ----------------------------------------------------------
# distroless/static (not distroless/base) contains no libc, no shell, and no
# package manager — there is no attack surface beyond the one static binary
# placed into it. The image never writes to its filesystem at runtime
# (config comes from env vars, logs go to stdout, the store is in-memory),
# so it also runs correctly with `docker run --read-only` / Kubernetes'
# `readOnlyRootFilesystem: true`, which a later milestone's SecurityContext
# will enforce. The ":nonroot" tag ships with a pre-created UID/GID 65532
# non-root user baked in, which is why this Dockerfile never runs its own
# `useradd` — there's no shell here to run that command with anyway.
FROM gcr.io/distroless/static-debian12:nonroot AS final

COPY --from=builder /out/pulse-api /pulse-api

USER nonroot:nonroot

EXPOSE 8080

# Distroless has no curl/wget to shell out to, so the container's own
# HEALTHCHECK runs the same static binary in "-healthcheck" mode instead of
# an external HTTP client (see internal/healthcheck). This is a
# Docker/Compose-level convenience for local development only — Kubernetes
# ignores HEALTHCHECK entirely and uses the liveness/readiness probes a
# later milestone defines in the Helm chart instead.
HEALTHCHECK --interval=10s --timeout=2s --start-period=5s --retries=3 \
    CMD ["/pulse-api", "-healthcheck"]

ENTRYPOINT ["/pulse-api"]
