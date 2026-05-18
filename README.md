# kiro-extract

Extract Kiro provider credentials managed by an enowxai proxy daemon.

The enowxai daemon caches a proxy JWT in `auth.json`. That JWT calls
`https://api.enowxlabs.com/proxy/accounts/credentials` and returns every
account the license is paying for, including raw access/refresh tokens.
This tool reads that JWT (from a docker container, a host file, or the
flag) and writes the Kiro accounts to disk in several convenient shapes.

Pure stdlib Go. Works on Linux, macOS, and Windows.

## Install

Download a prebuilt binary from `dist/` for your OS/arch, or build from source:

```sh
go build -o kiro-extract .
```

## Usage

```sh
# from a running enowxai docker container (default)
kiro-extract -mode docker -container enowxai -out ./kiro-out

# from auth.json on the host machine
kiro-extract -mode host -path ~/.enowxai/auth.json -out ./kiro-out
kiro-extract -mode host                       # uses ~/.enowxai/auth.json (or %USERPROFILE%\.enowxai\auth.json on Windows)

# bypass auth.json entirely if you already have the proxy JWT
kiro-extract -token eyJhbGci... -out ./kiro-out
```

### Flags

| flag | default | description |
|---|---|---|
| `-mode` | `docker` | `docker` or `host` |
| `-container` | `enowxai` | docker container name (mode=docker) |
| `-container-path` | `/root/.enowxai/auth.json` | path inside container |
| `-path` | platform default | path to `auth.json` on host |
| `-token` | _empty_ | proxy JWT, skips reading `auth.json` |
| `-api` | `https://api.enowxlabs.com` | upstream API base |
| `-out` | `kiro-out` | output directory |
| `-timeout` | `30s` | HTTP timeout |
| `-quiet` |  | suppress progress output |

## Output

Files written to `-out`, mode `0600`:

| file | contents |
|---|---|
| `all-credentials.json` | raw upstream response (every provider) |
| `kiro-full.json` | full kiro records |
| `kiro-credentials.json` | slim kiro records |
| `kiro-credentials.csv` | slim records as csv |
| `kiro-token-refresh.txt` | `accessToken:refreshToken` per line |
| `kiro-refresh-tokens.txt` | refresh token per line |

## Build for all platforms

```sh
GOOS=linux   GOARCH=amd64 go build -ldflags='-s -w' -o dist/kiro-extract-linux-amd64 .
GOOS=linux   GOARCH=arm64 go build -ldflags='-s -w' -o dist/kiro-extract-linux-arm64 .
GOOS=darwin  GOARCH=amd64 go build -ldflags='-s -w' -o dist/kiro-extract-darwin-amd64 .
GOOS=darwin  GOARCH=arm64 go build -ldflags='-s -w' -o dist/kiro-extract-darwin-arm64 .
GOOS=windows GOARCH=amd64 go build -ldflags='-s -w' -o dist/kiro-extract-windows-amd64.exe .
GOOS=windows GOARCH=arm64 go build -ldflags='-s -w' -o dist/kiro-extract-windows-arm64.exe .
```
