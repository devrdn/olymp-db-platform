# Build stage: compiles both binaries against the pinned toolchain.
# The patch version is pinned deliberately — it is the one govulncheck reports
# clean, and it matches the `go` directive in go.mod.
FROM golang:1.26.6-alpine AS build

WORKDIR /src

# Dependencies are cached separately from the source, so a code change does not
# re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
# CGO is off so the result runs on a distroless base with no libc.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/api       ./cmd/api && \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/migrate   ./cmd/migrate && \
    go build -trimpath -ldflags="-s -w"                            -o /out/bootstrap ./cmd/bootstrap

# Runtime stage: no shell, no package manager, non-root by default.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

COPY --from=build /out/api       /usr/local/bin/api
COPY --from=build /out/migrate   /usr/local/bin/migrate
COPY --from=build /out/bootstrap /usr/local/bin/bootstrap

USER nonroot:nonroot
EXPOSE 8080 9090

ENTRYPOINT ["/usr/local/bin/api"]
