# Build stage: compiles the Core API's binaries against the pinned toolchain.
# The patch version is pinned deliberately — it is the one govulncheck reports
# clean, and it matches the `go` directive in go.mod.
FROM golang:1.26.9-alpine AS build

WORKDIR /src

# Dependencies are cached separately from the source, so a code change does not
# re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
# CGO is off so the result runs on a distroless base with no libc. That is a
# constraint on the dependency graph, not only on this line: one import of the
# SQL checker anywhere beneath cmd/api pulls PostgreSQL's parser in and this
# stage stops building. `make static-check` is what says so before CI does.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/api       ./cmd/api && \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/migrate   ./cmd/migrate && \
    go build -trimpath -ldflags="-s -w"                            -o /out/bootstrap ./cmd/bootstrap

# Where the API's two named volumes are mounted (GAME_UPLOAD_DIR and COVER_DIR's
# defaults), made here because the runtime stage has no shell to make them.
RUN mkdir -p /out/var/lib/dbcontest/uploads /out/var/lib/dbcontest/covers

# Runtime stage: no shell, no package manager, non-root by default.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

COPY --from=build /out/api       /usr/local/bin/api
COPY --from=build /out/migrate   /usr/local/bin/migrate
COPY --from=build /out/bootstrap /usr/local/bin/bootstrap

# Owned by nonroot (65532), the user the API runs as. Docker initialises a new
# named volume from the image's directory at its mount point, ownership
# included; with no directory there, the volume's root is root's 0755 and the
# API cannot create its upload or cover directories — it restarts forever on a
# fresh install. A volume that already exists is never initialised again: an
# install created before this needs a one-time chown (deploy/.env.example).
# Numeric rather than a name, so it does not depend on the base image's
# /etc/passwd and reads the same as the CI check that asserts it.
COPY --from=build --chown=65532:65532 /out/var/lib/dbcontest /var/lib/dbcontest

USER nonroot:nonroot
EXPOSE 8080 9090

ENTRYPOINT ["/usr/local/bin/api"]

# --- Query Runner ------------------------------------------------------------
#
# Its own build and its own base, and that is not incidental — it is a third
# reason the Query Runner is a separate service.
#
# It links PostgreSQL's own parser (pg_query_go), which is C. Building it needs
# cgo, and the result needs a libc at runtime, so it cannot go in the stage
# above or on the static image below it. Were it part of the Core API binary,
# every one of these — cgo, a compiler in the build, a libc in the runtime —
# would be inherited by the API, the migration job and the bootstrap job as
# well, widening the attack surface of all three for the sake of one component.
#
# Debian rather than Alpine on both sides: the parser is linked dynamically
# against glibc, and a musl build would have to be matched by a musl runtime,
# which distroless does not offer.

FROM golang:1.26.9-bookworm AS build-runner

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ENV CGO_ENABLED=1
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/queryrunner ./cmd/queryrunner && \
    go build -trimpath -ldflags="-s -w"                            -o /out/gamedb      ./cmd/gamedb

# `base` rather than `static`: this one needs libc. Still distroless — no
# shell, no package manager, non-root — which is why the health check is the
# binary dialling itself rather than a probe binary or a curl.
FROM gcr.io/distroless/base-debian12:nonroot AS queryrunner

COPY --from=build-runner /out/queryrunner /usr/local/bin/queryrunner
# The one-shot cluster preparation job: the same image with a different
# entrypoint, because it shares the sensitive catalog list and therefore the
# cgo build.
COPY --from=build-runner /out/gamedb      /usr/local/bin/gamedb

USER nonroot:nonroot
EXPOSE 9100

ENTRYPOINT ["/usr/local/bin/queryrunner"]
