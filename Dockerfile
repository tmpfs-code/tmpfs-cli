# syntax=docker/dockerfile:1
# Build container for tmpfs-cli (see Taskfile.yml and STEERING.md §6).
#   task build  ->  docker build --target artifacts --output type=local,dest=dist .

FROM golang:1.25.14-bookworm AS test
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
