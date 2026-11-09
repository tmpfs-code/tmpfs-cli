# tmpfs-cli

Command-line client for [tmpfs.it](https://tmpfs.it): share files with
end-to-end encryption and links that expire. Links work both ways with the
webapp.

## Install

With Go 1.25 or newer:

```
go install github.com/tmpfs-code/tmpfs-cli@latest
```

The binary lands in `$(go env GOPATH)/bin` (usually `~/go/bin`), so make sure
that directory is on your `PATH`.

No Go? Download a prebuilt binary for Linux (amd64), Windows (amd64) or macOS
(Apple Silicon) from the [releases page](https://github.com/tmpfs-code/tmpfs-cli/releases),
check it against `SHA256SUMS`, rename it to `tmpfs-cli` and put it on your
`PATH`.

The macOS binaries are not signed. If Gatekeeper blocks one:

```
xattr -d com.apple.quarantine ./tmpfs-cli-*-darwin-arm64
```

## Usage

```
tmpfs-cli upload ./report.pdf              # prints the share link
tmpfs-cli u -t 3d -p AB3D-EFGH-2345 big.iso
tmpfs-cli download 'https://tmpfs.it/file/<id>/#<key>'
tmpfs-cli d -o ~/Downloads/ '<link>'
```

- `upload` / `u`: `-t` duration (`10m 1h 6h 12h 1d 3d 1w`, default `1d`),
  `-p` promo code (needed above 200 MB or for `3d`/`1w`).
- `download` / `d`: `-o` output file or directory, `-f` overwrite.
- Global: `--server URL` (or `TMPFS_SERVER`), `-q`, `--version`.

Each download uses up one of the downloads allowed for the link (10 for
uploads made with the CLI).

The encryption key is only in the part of the link after `#`, which is never
sent to the server. Anyone who has the full link can download the file, so
share it the way you would share the file itself.

## Build from source

Requires Docker (with BuildKit) and [task](https://taskfile.dev):

```
task          # = task build -> dist/tmpfs-cli-<version>-{linux-amd64,windows-amd64.exe,darwin-arm64}
task test
```

The version lives in `VERSION`.

## Releasing

Bump `VERSION`, commit, then push a tag named `v` + the version:

```
git tag v0.1.0 && git push origin v0.1.0
```

The [release workflow](.github/workflows/release.yml) builds the 3 binaries
with the same container as `task build` and publishes them, with
`SHA256SUMS`, as a GitHub release. The tag is also what `go install ...@latest`
resolves to.

See [STEERING.md](STEERING.md) for the design and the protocol.
