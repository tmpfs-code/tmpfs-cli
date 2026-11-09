# syntax=docker/dockerfile:1
# Build container for tmpfs-cli (see Taskfile.yml and STEERING.md §6).
#   task build  ->  docker build --target artifacts --output type=local,dest=dist .

# Pinned by digest (multi-arch index). Go 1.26.10+: older releases miss
# security fixes in crypto/x509, crypto/tls and net/http that the CLI reaches.
FROM golang:1.26.10-bookworm@sha256:5de7c99eb78ca5e515a4c340235d0955ec5964a5071c492c15503d7c267da473 AS test
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN go vet ./... && go test ./...

FROM test AS build
RUN set -eu; \
    VERSION="$(cat VERSION)"; \
    mkdir /out; \
    for target in linux/amd64 windows/amd64 darwin/arm64; do \
      os="${target%/*}"; arch="${target#*/}"; ext=""; \
      [ "$os" = "windows" ] && ext=".exe"; \
      GOOS="$os" GOARCH="$arch" go build -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o "/out/tmpfs-cli-${VERSION}-${os}-${arch}${ext}" . ; \
    done; \
    cd /out && sha256sum tmpfs-cli-* > SHA256SUMS

FROM scratch AS artifacts
COPY --from=build /out/ /
