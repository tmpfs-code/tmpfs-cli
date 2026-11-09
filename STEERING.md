# tmpfs-cli — steering

`tmpfs-cli` is a command-line client for [tmpfs.it](https://tmpfs.it). It
uploads and downloads files with the same end-to-end encryption as the webapp,
so links work in both directions: a file uploaded with the CLI opens in the
browser, and a browser link downloads with the CLI.

This document is the source of truth for **what** to build and the
constraints that apply. Before you change the protocol or the decisions
below, update this file in the same change.

---

## 1. Requirements

| # | Requirement |
|---|---|
| R1 | Written entirely in Go, **Go 1.25+** (`go 1.25` in `go.mod`). |
| R2 | **Memory efficient**: memory use is constant, whatever the file size. Never load a whole file into memory (the webapp does, the CLI must not). |
| R3 | Runs on **Linux, Windows and macOS**. |
| R4 | The binary is named **`tmpfs-cli`** (`tmpfs-cli.exe` on Windows). |
| R5 | Two subcommands for now: **`upload`** (alias `u`) and **`download`** (alias `d`). |
| R6 | A **Taskfile** whose `default` task is `build`. It builds the 3 binaries into `dist/` **inside a dedicated container** defined by the CLI's own `Dockerfile`. |
| R7 | A single **global version number**, starting at **`0.1.0`**. |
| R8 | `upload` optionally accepts a **duration** (default **1 day**) and a **promo code**. |

---

## 2. Command-line interface

```
tmpfs-cli [global flags] <command> [flags] <args>

Commands:
  upload,   u   Encrypt and upload a file, print the share link
  download, d   Download and decrypt a file from a share link

Global flags:
  --server URL    tmpfs server (default https://tmpfs.it, env TMPFS_SERVER)
  -q, --quiet     no progress output
  --version       print the version and exit
  -h, --help
```

### upload

```
tmpfs-cli upload [-t DURATION] [-p PROMO_CODE] FILE
tmpfs-cli u -t 3d -p AB3D-EFGH-2345 ./backup.tar
```

| Flag | Default | Notes |
|---|---|---|
| `-t, --duration` | `1d` | One of `10m 1h 6h 12h 1d 3d 1w`, the only values the server accepts. Reject anything else and list the valid values. |
| `-p, --promo` | none | Needed only for the paid tier (§4.4). If you give one for a free upload, print a warning and don't redeem it, because the server would reject it. |

- **stdout:** only the share link plus a newline, so `link=$(tmpfs-cli u f)` works.
- **stderr:** progress and messages.

### download

```
tmpfs-cli download [-o PATH] [-f] LINK
tmpfs-cli d 'https://tmpfs.it/file/<id>/#<key>'
```

| Flag | Default | Notes |
|---|---|---|
| `-o, --output` | original filename, in the current directory | If `PATH` is an existing directory, save the file inside it under the original name. |
| `-f, --force` | false | Overwrite an existing file. Without it, refuse to overwrite. |

Before downloading, print the filename, size, downloads left and time left to
stderr.

### Conventions

- **Flags:** use the standard library `flag` package with one `FlagSet` per
  subcommand. Don't use a CLI framework.
- **Flags after positionals:** `tmpfs-cli u FILE -t 3d` must work. Reorder the
  arguments before parsing.
- **Exit codes:** `0` success, `1` error, `2` bad usage.
- **Errors:** one line on stderr, prefixed `tmpfs-cli: `. Print no stack
  traces unless the hidden `--debug` flag is set.
- **Progress:** draw it on stderr only when stderr is a terminal and `-q` is
  not set. Throttle it to about 10 updates per second.

---

## 3. Version

- **Where it lives:** the plain-text file `VERSION` (contents `0.1.0`) is
  the only place the version is written down.
- **How it gets in:** the build injects it with
  `-ldflags "-X main.version=$(cat VERSION)"`. In source,
  `var version = "dev"`.
- **Where it shows:** `tmpfs-cli --version` prints `tmpfs-cli 0.1.0`. It is
  also the `User-Agent` header: `tmpfs-cli/0.1.0 (<GOOS>/<GOARCH>)`.
- Bump it by semver whenever a release changes behaviour.

---

## 4. Protocol (must match the server and the webapp exactly)

The reference implementation is the tmpfs.it webapp (its encryption,
upload clearance and promo code code) together with the server it talks to.

All API paths are relative to `--server`, for example
`https://tmpfs.it/api/v1/...`.

### 4.1 Required HTTP headers

On **every** request, send:
- `User-Agent` (see §3)
- `Accept-Language: en`
- `Accept-Encoding`

The clearance endpoint answers `{"err":1}` if any of the three is missing.

Don't set `Accept-Encoding` by hand. Go's transport adds `gzip` itself and
decompresses transparently. If you set it explicitly, transparent
decompression is turned off, and nginx gzips JSON responses.

### 4.2 Encryption format

| Item | Value |
|---|---|
| Key | 32 random bytes (`crypto/rand`), AES-256 |
| IV | 16 random bytes, shared by the body and the metadata |
| Body | AES-256-**CBC**, **no padding**. Pad the plaintext up to a multiple of 16 with **random** bytes, then encrypt. The ciphertext is uploaded as raw bytes. |
| Encrypted size | `ceil(size/16)*16` |
| Sensitive metadata `x` | AES-256-CBC with **PKCS#7** padding, same key and IV, hex encoded. The plaintext is compact JSON `{"n":name,"r":rnd,"s":sizeHex,"c":check}`. |

The fields inside `x`:
- `n`: the base filename with surrounding whitespace trimmed.
- `r`: hex of 10–29 random bytes.
- `s`: the plaintext size as lowercase hex, no `0x`.
- `c`: the first 6 hex characters of `sha256(n + r)`.

On download, verify `c`. A mismatch means the key is wrong.

The share link is `<server>/file/<id>/#<base58(key)>`:
- `id` is a UUID v4.
- base58 uses the Bitcoin alphabet, like the npm `bs58` package.
- A valid key decodes to exactly 32 bytes.

> There is no MAC (CBC without authentication). This is the webapp's format
> and must stay compatible. Integrity checks are limited to the `c` check and
> the size. Don't claim the format protects against tampering.

### 4.3 Upload flow

1. **Validate.** The file must be a regular file with
   `0 < size <= 2_000_000_000` (`MAX_FILE_SIZE` in the webapp,
   `maxEncFileSize` on the server). Work out the tier (§4.4). If the upload is
   paid and there is no `--promo`, stop before sending any request and explain
   why.
2. **Promo (paid tier only).** `POST /api/v1/promo/redeem` with body
   `{"co":CODE,"r":{"r":floor(size/1e6),"d":MAXDL,"e":SECONDS,"c":"eth"}}`.
   The reply is `{"id":UUID}`, and you **must use that id** as the file id.
   For a free upload, generate a UUID v4 yourself.
3. **Clearance.** `POST /api/v1/upload/clearance` returns the challenge as
   plain text `{"s":"<hex>","d":<difficulty>}`, or `{"err":N}`, where `1`
   means headers are missing and `2` means rate-limited or banned. To solve
   it:
   - `secret` is the decimal string of the **sum of the bytes** of
     `hex_decode(s)`.
   - Loop over random 16-byte values `sol` until
     `hex(md5(secret || sol))` starts with `d` zeros.
   - Send `hex(sol)` (32 characters).
   - **Always solve for at least difficulty 4**, whatever `d` says. The
     server re-checks the solution on every first-chunk attempt, against a
     difficulty (3 or 4) that depends on the attempt count at that moment. A
     4-zero hash satisfies both, and a wrong solution means a ban.
4. **Chunks.** For each chunk, `POST /api/v1/upload/<id>/<from>/<to>`, where
   `from`/`to` are **ciphertext** offsets and the body is the ciphertext slice:
   - **Size:** use a fixed chunk size of **1_800_000 bytes** (a multiple of
     16). nginx allows 2 MB and the server 2.1 MB.
   - **First chunk:** must carry the header `x-clearance: <solution>`.
   - **Last chunk:** must carry the query string `?h=<header>`, where
     `header` is `base64.RawURLEncoding` of
     `{"x":X,"i":hex(iv),"d":MAXDL,"e":SECONDS}`. It must be shorter than
     1024 characters.
   - **Order:** send chunks strictly in order, one at a time. The server
     rejects a `from` beyond the current size.
   - **Success** is HTTP 200 with a JSON body that has no `error` field.
5. **Print the link.**

**Max downloads:** fixed at **10** (the webapp default), with no flag for now.
Adding `--max-downloads` (values `1 10 50 100 1000`) is a future option.

### 4.4 Tiers and pricing

| | Free | Paid (needs `--promo`) |
|---|---|---|
| Size | `floor(size/1e6) <= 200` | above that |
| Duration | `10m 1h 6h 12h 1d` | `3d 1w` |
| Max downloads | `1 10 50` | `100 1000` |

- **No crypto payments.** The CLI does not implement ETH payments. A promo
  code is the only way to upload in the paid tier.
- **Single use.** A promo code can be redeemed only once. It is bound to the
  id the server returns and to the `d`/`e` values sent with it.

### 4.5 Download flow

1. **Parse the link.**
   - Take the `id` from `/file/<id>/` and the key from the fragment.
   - Use the link's scheme and host as the server, unless `--server` is set
     explicitly.
   - Reject an invalid UUID or a key that doesn't decode to 32 bytes.
2. **Metadata.** `GET /api/v1/download/<id>/header` returns
   `{"fm":{"x","i","d","c","e"},"dl":downloadsLeft,"tl":secondsLeft}`.
   - A 404 means the link expired or doesn't exist.
   - Decrypt `x`, verify `c`, and read the name and size.
   - This call does **not** use up a download.
3. **Body.** `GET /api/v1/download/<id>` returns the ciphertext with a
   `Content-Length`. This call **uses up one download**.
   - Stream-decrypt it with CBC, write to disk, and cut the output to exactly
     `s` bytes (dropping the random padding).
4. **Completion checks.**
   - Write to `<target>.part` in the target directory. Rename it to the final
     name only after all `Content-Length` bytes have arrived and `s` bytes
     have been written.
   - On any error, delete the `.part` file and exit 1. Never leave a
     truncated file under the final name. That is exactly the bug the webapp
     had in Firefox.

### 4.6 Server limits a client must respect

- **Rate limit.** nginx limits `/api/v1/` to **10 requests/s per IP**
  (burst 20). Fast uploads will get **HTTP 503** from nginx.
  - The server never saw those requests, so **retry the same chunk** with
    backoff: start at 200 ms, cap at 5 s, give up after about 2 minutes.
  - Also pace uploads so you stay at or below about 8 chunk requests/s.
- **Server-side errors are fatal.** Any server-side chunk error (a 500 with
  `{"error":...}`) makes the server **delete the whole upload**. Don't retry:
  fail and tell the user to start again.
- **Network errors.** Retry a chunk after a network error (timeout, connection
  reset) with backoff. Re-sending a chunk whose `from` is already stored is
  allowed; the server overwrites it.
- **Anti-abuse limits.**
  - About 6 clearance/verify attempts per IP per minute. Each upload uses 2
    (clearance plus first-chunk verify), so in practice **3 uploads per
    minute per IP**. On `{"err":2}`, tell the user to wait a minute.
  - Never retry the first chunk after a network error. Each retry is another
    verify attempt, and too many lead to a ban. Retry it only on an nginx
    503.
  - A wrong solution **bans the IP for 60 minutes**.
  - Never send a solution you haven't verified locally.
- **nginx timeouts.** `send_timeout` is 20 s and `client_body_timeout` is 15 s.
  Never let a connection sit idle that long. Write to disk as data arrives,
  and send each chunk in one go.

---

## 5. Design rules

### Memory (R2)

- **Upload:** read the file in chunk-sized pieces with one reused buffer.
  Encrypt in place with `cipher.NewCBCEncrypter`, whose state carries across
  chunks. Send each chunk with `bytes.NewReader`. Memory stays around a few MB,
  whatever the file size.
- **Download:** stream the response body through a decrypter that works on
  whole 16-byte blocks, into a `bufio.Writer` on the `.part` file. Never call
  `io.ReadAll` on a body.
- **Check:** a test or benchmark keeps peak RSS for a 1 GB round trip under
  about 50 MB.

### Cross-platform (R3)

- **Pure Go:** build with `CGO_ENABLED=0`.
- **Paths:** use `path/filepath` everywhere.
- **Downloaded filenames:** the name comes from the uploader, so treat it as
  untrusted. Keep only the base name (no `/`, `\`, `..`, or drive letters).
  Replace the characters Windows forbids (`<>:"/\|?*` and control
  characters). Avoid reserved names (`CON`, `NUL`, …). Fall back to
  `tmpfs-download` if nothing is left.
- **Rename:** on Windows, `os.Rename` won't overwrite. With `--force`, remove
  the target first.
- **Terminal detection:** use `golang.org/x/term`.

### Dependencies

Prefer the standard library. Each dependency outside it needs a reason. The
expected ones:

| Package | Why |
|---|---|
| `golang.org/x/term` | Detecting a terminal for progress output. Pinned to **v0.45.0**, the last version that supports Go 1.25 (v0.46+ needs Go 1.26). |
| `github.com/google/uuid` | UUID v4 (or a small `crypto/rand` implementation) |
| base58 | Small and vetted (e.g. `github.com/mr-tron/base58`), or ~40 lines of our own with tests |

### Project layout

```
tmpfs-cli/
  STEERING.md
  VERSION                 # 0.1.0
  Taskfile.yml
  Dockerfile              # build container (R6)
  go.mod                  # go 1.25
  main.go                 # command dispatch, global flags, version
  internal/
    api/                  # HTTP client: headers, retry/backoff, JSON errors
    tmpcrypto/            # key/IV, metadata x, header h, CBC stream helpers, base58 link
    clearance/            # proof-of-work solver
    upload/               # upload flow (§4.3)
    download/             # download flow (§4.5)
    progress/             # stderr progress bar
  .github/workflows/release.yml  # release on v* tags (§6)
  dist/                   # build output, gitignored
```

The module path is `github.com/tmpfs-code/tmpfs-cli`, the GitHub repository,
so that `go install github.com/tmpfs-code/tmpfs-cli@latest` works (§6).

---

## 6. Build (R6)

### Taskfile

`Taskfile.yml` uses go-task v3.

| Task | What it does |
|---|---|
| `default` | Runs `build`. |
| `build` | Builds the 3 release binaries **inside the container** into `dist/`. The host needs only Docker and `task`, not Go. |
| `test` | `go test ./...`, also in the container. |
| `clean` | Removes `dist/`. |
| `local` (optional) | `go build` on the host, for quick development. |

To get the files out of the container, `build` uses BuildKit:
`docker build --target artifacts --output type=local,dest=dist .`. This
avoids bind mounts and root-owned files on the host.

### Dockerfile

The Dockerfile has two stages:
- A builder stage `FROM golang:1.26.<patch>-bookworm@sha256:<digest>`,
  pinned by digest. It runs `go mod download`, then `go vet`,
  `go test ./...`, and the 3 cross-compiles. The release toolchain must be a
  supported Go release with all security fixes (check with `govulncheck`),
  even though `go.mod` still accepts Go 1.25 for `go install` users.
- An `artifacts` stage `FROM scratch` containing only the binaries.

The build flags are:
`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}"`.
`VERSION` comes from the `VERSION` file.

### Releases

Pushing a tag `v<VERSION>` (e.g. `v0.1.0`) runs
`.github/workflows/release.yml`: it checks that the tag matches `VERSION`,
builds the 3 binaries with the same container as `task build`, and publishes
them with `SHA256SUMS` as a GitHub release.

The same tag makes the module installable with
`go install github.com/tmpfs-code/tmpfs-cli@latest`. That build has no
`-ldflags`, so `main.version` falls back to the module version recorded in
the binary's build info (`v0.1.0` → `0.1.0`).

### Outputs

| Target | File |
|---|---|
| linux/amd64 | `dist/tmpfs-cli-<version>-linux-amd64` |
| windows/amd64 | `dist/tmpfs-cli-<version>-windows-amd64.exe` |
| darwin/arm64 | `dist/tmpfs-cli-<version>-darwin-arm64` |

The build also writes `dist/SHA256SUMS`, the same idea as the webapp's
`checksums.txt`.

- **macOS:** arm64 covers Apple Silicon. Intel Macs (`darwin/amd64`) or a
  universal binary can be added later.
- **Signing:** the binaries are not signed or notarized, so macOS Gatekeeper
  will warn. Document the workaround (`xattr -d com.apple.quarantine`) in the
  README.

---

## 7. Testing

- **Unit tests** cover:
  - base58: round trip, and decoding a known webapp key.
  - Metadata `x` and header `h`: round trip, plus compatibility with
    fixtures produced by the webapp's `crypto.js`.
  - The clearance solver against a copy of the server's verify logic.
  - Filename sanitizing.
  - Duration parsing.
  - The tier calculation.
- **Streaming crypto:** encrypt and decrypt randomly sized files across chunk
  boundaries, including sizes that are not multiples of 16.
- **Interop with the webapp.** This is mandatory before a release, and runs
  in the maintainers' end-to-end harness (not part of this repository):
  - CLI upload → browser download, in Firefox and Chromium.
  - Browser upload → CLI download.
  - CLI upload → CLI download.
  - Cover the free tier and the paid tier with promo codes.
  - Run against a local stack with the production nginx limits, so 503
    retries are exercised.
- **Memory:** a large-file round trip asserts that peak RSS stays bounded
  (§5).

### Test entry points

| Command | What it runs |
|---|---|
| `go test ./...` | Unit tests, plus an in-process round trip against a fake server (`main_test.go`) with injected 503s, truncated downloads, hostile filenames and promo codes. |
| `task test` | The same, inside the build container. |

Against the live server (https://tmpfs.it), test the free tier only and space
uploads at least 70 s apart to respect the anti-abuse limit.

---

## 8. Out of scope (for now)

- ETH payments.
- Choosing max downloads (fixed at 10).
- Resuming interrupted uploads or downloads.
- Uploading several files or directories (users can tar them first).
- Reading from stdin or writing to stdout. This is a likely next step: the
  streaming design must not rule it out, but uploading from stdin needs the
  size in advance, for the tier calculation and the metadata.
- Self-update and package-manager distribution (other than `go install`).
