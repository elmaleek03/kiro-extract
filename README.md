# kiro-extract

Interactive CLI to move Kiro credentials between an **enowxai** proxy daemon
and a **9router** dashboard.

Two flows in one binary:

1. **Extract** Kiro provider credentials from enowxai (reads the proxy JWT
   from `auth.json` and calls `https://api.enowxlabs.com/proxy/accounts/credentials`).
2. **Import** a list of refresh tokens into a 9router dashboard via
   `POST /api/oauth/kiro/import`, with a configurable delay between calls.

Pure stdlib Go. Works on Linux, macOS, and Windows.

## Install

Download a prebuilt binary from `dist/` for your OS/arch, or build from source:

```sh
go build -o kiro-extract .
```

## Run

```sh
./kiro-extract        # interactive menu
go run .              # same, from source
```

You will see:

```
=========================================
 kiro-extract: enowxai <-> 9router tools
=========================================
 1) Extract Kiro credentials from enowxai
 2) Import refresh tokens into 9router
 q) Quit
```

### 1. Extract

Pick a source for the proxy JWT:

- **docker container** — runs `docker exec <container> cat <path>` (defaults
  to `enowxai:/root/.enowxai/auth.json`).
- **host file path** — reads `auth.json` from the local filesystem
  (default `~/.enowxai/auth.json`, or `%USERPROFILE%\.enowxai\auth.json`
  on Windows).
- **paste a proxy JWT directly** — skips reading any file.

Output files (mode `0600`) under the chosen output directory:

| file | contents |
|---|---|
| `all-credentials.json` | raw upstream response (every provider) |
| `kiro-full.json` | full kiro records |
| `kiro-credentials.json` | slim kiro records |
| `kiro-credentials.csv` | slim records as csv |
| `kiro-token-refresh.txt` | `accessToken:refreshToken` per line |
| `kiro-refresh-tokens.txt` | refresh token per line |

### 2. Import

Reads defaults from `.env` (copy `.env.example`). All values can be overridden
interactively before each run.

`.env` keys:

| key | default | description |
|---|---|---|
| `BASE_URL` | `http://localhost:20128` | 9router dashboard URL |
| `AUTH_TOKEN` | _empty_ | value of the `auth_token` cookie |
| `TOKENS_FILE` | `kiro-out/kiro-refresh-tokens.txt` | one refresh token per line |
| `DELAY_SECONDS` | `1` | sleep between requests |
| `INSECURE` | `false` | skip TLS verification (self-signed dashboards) |

Per-line output during import:

- `OK (200) id=<uuid> email=<email|<no email>>` for `{"success":true,...}`
- `FAIL (<status>) <error message>` for `{"error":"..."}`

## Build for all platforms

```sh
GOOS=linux   GOARCH=amd64 go build -ldflags='-s -w' -o dist/kiro-extract-linux-amd64 .
GOOS=linux   GOARCH=arm64 go build -ldflags='-s -w' -o dist/kiro-extract-linux-arm64 .
GOOS=darwin  GOARCH=amd64 go build -ldflags='-s -w' -o dist/kiro-extract-darwin-amd64 .
GOOS=darwin  GOARCH=arm64 go build -ldflags='-s -w' -o dist/kiro-extract-darwin-arm64 .
GOOS=windows GOARCH=amd64 go build -ldflags='-s -w' -o dist/kiro-extract-windows-amd64.exe .
GOOS=windows GOARCH=arm64 go build -ldflags='-s -w' -o dist/kiro-extract-windows-arm64.exe .
```
