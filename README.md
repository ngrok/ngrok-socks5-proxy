# ngrok-socks5-proxy

A SOCKS5/HTTP CONNECT forward proxy that integrates with the [ngrok Go SDK](https://github.com/ngrok/ngrok-go). It lets clients access multiple internal web applications through a single ngrok TCP tunnel — preserving original hostnames so SSO redirects, domain-scoped cookies, and hardcoded URLs all work correctly.

## The Problem

When accessing internal web apps via ngrok, the hostname changes break things:

- **SSO redirects**: `crm.corp.local` redirects to `sso.corp.local` for auth — but `sso.corp.local` is unreachable from the operator's browser
- **Cookies**: session cookies scoped to `.corp.local` don't work when the browser sees `xyz.ngrok.io`
- **Hardcoded URLs**: JavaScript that references `api.corp.local` fails

## The Solution

Run a forward proxy inside the customer's network. The client configures their browser to use the ngrok endpoint as a SOCKS5 proxy. All requests flow through the tunnel with original hostnames intact.

```
Browser (proxy: socks5://ngrok-endpoint)
    → ngrok tunnel
    → forward proxy (customer's network)
    → crm.corp.local, sso.corp.local, etc.
```

One tunnel, many internal destinations. No URL rewriting.

## Installation

```bash
go install github.com/ngrok/ngrok-socks5-proxy/cmd/ngrok-socks5-proxy@latest
```

Or build from source:

```bash
git clone https://github.com/ngrok/ngrok-socks5-proxy.git
cd ngrok-socks5-proxy
go build -o ngrok-socks5-proxy ./cmd/ngrok-socks5-proxy/
```

### Download a Prebuilt Binary

Prebuilt binaries for Linux, macOS, and Windows (amd64 and arm64) are published
on the [GitHub Releases page](https://github.com/ngrok/ngrok-socks5-proxy/releases).

```bash
# Linux/macOS example (adjust OS/ARCH for your platform)
curl -L -o ngrok-socks5-proxy.tar.gz \
  https://github.com/ngrok/ngrok-socks5-proxy/releases/latest/download/ngrok-socks5-proxy_<version>_<os>_<arch>.tar.gz
tar -xzf ngrok-socks5-proxy.tar.gz
sudo mv ngrok-socks5-proxy /usr/local/bin/
```

On Windows, download the `.zip` asset from the Releases page, extract it, and
add the extracted directory to your `PATH`.

Verify a download against the published checksums:

```bash
curl -L -o ngrok-socks5-proxy_SHA256SUMS \
  https://github.com/ngrok/ngrok-socks5-proxy/releases/latest/download/ngrok-socks5-proxy_<version>_SHA256SUMS
sha256sum -c ngrok-socks5-proxy_SHA256SUMS --ignore-missing
```

## Quick Start

### 1. Start the proxy with ngrok

```bash
ngrok-socks5-proxy \
  --authtoken=YOUR_TOKEN \
  --url=tcp://1.tcp.ngrok.io:12345 \
  --allow="*.corp.local"
```

### 2. Generate a PAC file

```bash
ngrok-socks5-proxy pac --proxy 1.tcp.ngrok.io:12345 > proxy.pac
```

### 3. Configure your browser

**Chrome:**
```bash
chrome --proxy-pac-url="file:///path/to/proxy.pac" --user-data-dir=/tmp/proxy-session
```

**Firefox:** Settings → Network → Manual Proxy → SOCKS Host, check "Proxy DNS when using SOCKS v5"

**curl:**
```bash
curl -x socks5h://1.tcp.ngrok.io:12345 http://crm.corp.local/
```

## Configuration

### CLI Flags

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--config` | No | `~/.config/ngrok-socks5-proxy/config.yaml` | Path to YAML config file |
| `--authtoken` | Yes | `$NGROK_AUTHTOKEN` | ngrok auth token |
| `--url` | No | ephemeral TCP | Endpoint URL (e.g., `tcp://1.tcp.ngrok.io:12345`) |
| `--listen` | No | — | Local address, no ngrok (e.g., `127.0.0.1:9080`) |
| `--name` | No | — | Label in the ngrok dashboard |
| `--bindings` | No | `public` | Endpoint bindings: `public`, `internal`, `kubernetes` |
| `--dns` | No | system DNS | Custom DNS server (e.g., `10.0.0.53:53`) |
| `--allow` | Yes (≥1) | — | Hostname pattern (repeatable or comma-separated) |
| `--log-level` | No | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `--dial-timeout` | No | `10s` | Timeout for connecting to targets (e.g., `15s`, `500ms`) |

`--url` and `--listen` are mutually exclusive. CLI flags override config file values. `--allow` flags merge with config file entries.

### Config File

```yaml
authtoken: "xxx"                          # or use $NGROK_AUTHTOKEN
url: "tcp://1.tcp.ngrok.io:12345"         # optional: reserved TCP address
# listen: "127.0.0.1:9080"               # optional: listener mode (no ngrok)
name: "acme-corp-proxy"                   # optional: dashboard label
# bindings:                              # optional: endpoint bindings
#   - "internal"
# dns: "10.0.0.53:53"                   # optional: custom DNS
# dial_timeout: "10s"                   # optional: timeout for connecting to targets
log_level: "info"
allow:
  - "*.corp.local"
  - "sso.partner.com"
  - "db.internal:5432"
```

A default config is auto-created on first run at:
- **macOS**: `~/Library/Application Support/ngrok-socks5-proxy/config.yaml`
- **Linux**: `~/.config/ngrok-socks5-proxy/config.yaml`
- **Windows**: `%AppData%\ngrok-socks5-proxy\config.yaml`

### Subcommands

```bash
ngrok-socks5-proxy config edit    # Open config in $EDITOR
ngrok-socks5-proxy config path    # Print config file path
ngrok-socks5-proxy pac --proxy HOST:PORT  # Generate PAC file
```

## Allowlist Patterns

| Pattern | Matches |
|---------|---------|
| `crm.corp.local` | Exact hostname, any port |
| `*.corp.local` | Any subdomain of corp.local, any port |
| `db.internal:5432` | Exact hostname, port 5432 only |
| `*.corp.local:443` | Any subdomain, port 443 only |

- At least one `--allow` pattern is required
- Global wildcards (`*`, `*.*`) are rejected
- Wildcard must include domain + TLD (e.g., `*.corp.local`, not `*.com`)
- Matching is case-insensitive

## How It Works

The proxy auto-detects the protocol on each connection:
- **First byte `0x05`** → SOCKS5 (RFC 1928, CONNECT command, no auth)
- **First byte is ASCII** → HTTP CONNECT or plain HTTP proxy

A TCP endpoint is used (not HTTP) because SOCKS5 and HTTP CONNECT are raw TCP protocols that would be mangled by an HTTP edge.

## Security Model

1. **ngrok authtoken** controls who can create the tunnel
2. **Allowlist** controls which internal hosts the proxy can reach

The proxy logs every connection attempt (target, allowed/denied) for audit.

## Docker

```bash
docker pull ishanjain8108/ngrok-forward-proxy
```

Or build from source:
```bash
docker build -t ishanjain8108/ngrok-forward-proxy .
```

### With built-in ngrok agent

The proxy creates its own ngrok TCP endpoint — no separate agent needed.

```bash
docker run --rm ishanjain8108/ngrok-forward-proxy \
  --authtoken=YOUR_TOKEN \
  --url=tcp://1.tcp.ngrok.io:12345 \
  --allow="*.corp.local"
```

```bash
curl -x socks5h://1.tcp.ngrok.io:12345 http://crm.corp.local/
```

### Chain to an existing ngrok agent

The proxy listens on a local port and a separate ngrok agent forwards traffic to it.

**On Linux** (production):
```bash
docker run --rm --network host ishanjain8108/ngrok-forward-proxy \
  --listen 0.0.0.0:9080 \
  --allow="*.corp.local"
```

**On macOS** (testing):
```bash
docker run --rm -p 9080:9080 \
  --add-host crm.corp.local:host-gateway \
  --add-host sso.corp.local:host-gateway \
  ishanjain8108/ngrok-forward-proxy \
  --listen 0.0.0.0:9080 \
  --allow="*.corp.local"
```

Then point your ngrok agent at the proxy:
```bash
ngrok tcp 9080

# Test with curl (use the URL from ngrok output)
curl -x socks5h://X.tcp.ngrok.io:XXXXX http://crm.corp.local/
```
