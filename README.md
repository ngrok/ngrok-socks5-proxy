# ngrok-forward-proxy

A SOCKS5/HTTP CONNECT forward proxy that integrates with the [ngrok Go SDK](https://github.com/ngrok/ngrok-go). It lets operators access multiple internal web applications through a single ngrok TCP tunnel — preserving original hostnames so SSO redirects, domain-scoped cookies, and hardcoded URLs all work correctly.

## The Problem

When accessing internal web apps via ngrok, the hostname changes break things:

- **SSO redirects**: `crm.corp.local` redirects to `sso.corp.local` for auth — but `sso.corp.local` is unreachable from the operator's browser
- **Cookies**: session cookies scoped to `.corp.local` don't work when the browser sees `xyz.ngrok.io`
- **Hardcoded URLs**: JavaScript that references `api.corp.local` fails

## The Solution

Run a forward proxy inside the customer's network. The operator configures their browser to use the ngrok endpoint as a SOCKS5 proxy. All requests flow through the tunnel with original hostnames intact.

```
Browser (proxy: socks5://ngrok-endpoint)
    → ngrok tunnel
    → forward proxy (customer's network)
    → crm.corp.local, sso.corp.local, etc.
```

One tunnel, many internal destinations. No URL rewriting.

## Installation

```bash
go install github.com/ngrok/ngrok-forward-proxy/cmd/ngrok-forward-proxy@latest
```

Or build from source:

```bash
git clone https://github.com/ishanj12/ngrok-forward-proxy.git
cd ngrok-forward-proxy
go build -o ngrok-forward-proxy ./cmd/ngrok-forward-proxy/
```

## Quick Start

### 1. Start the proxy with ngrok

```bash
ngrok-forward-proxy \
  --authtoken=YOUR_TOKEN \
  --url=tcp://1.tcp.ngrok.io:12345 \
  --allow="*.corp.local"
```

Or in local mode (no ngrok, for testing):

```bash
ngrok-forward-proxy --listen 127.0.0.1:9080 --allow="*.corp.local"
```

### 2. Generate a PAC file

```bash
ngrok-forward-proxy pac --proxy 1.tcp.ngrok.io:12345 > proxy.pac
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
| `--config` | No | `~/.config/ngrok-forward-proxy/config.yaml` | Path to YAML config file |
| `--authtoken` | Yes* | `$NGROK_AUTHTOKEN` | ngrok auth token (*not needed with `--listen`) |
| `--url` | No | ephemeral TCP | Endpoint URL (e.g., `tcp://1.tcp.ngrok.io:12345`) |
| `--listen` | No | — | Local address, no ngrok (e.g., `127.0.0.1:9080`) |
| `--name` | No | — | Label in the ngrok dashboard |
| `--bindings` | No | `public` | Endpoint bindings: `public`, `internal`, `kubernetes` |
| `--dns` | No | system DNS | Custom DNS server (e.g., `10.0.0.53:53`) |
| `--allow` | Yes (≥1) | — | Hostname pattern (repeatable or comma-separated) |
| `--log-level` | No | `info` | Log level: `debug`, `info`, `warn`, `error` |

`--url` and `--listen` are mutually exclusive. CLI flags override config file values. `--allow` flags merge with config file entries.

### Config File

```yaml
authtoken: "xxx"                          # or use $NGROK_AUTHTOKEN
url: "tcp://1.tcp.ngrok.io:12345"         # optional: reserved TCP address
# listen: "127.0.0.1:9080"               # optional: local mode (no ngrok)
name: "acme-corp-proxy"                   # optional: dashboard label
# bindings:                              # optional: endpoint bindings
#   - "internal"
# dns: "10.0.0.53:53"                   # optional: custom DNS
log_level: "info"
allow:
  - "*.corp.local"
  - "sso.partner.com"
  - "db.internal:5432"
```

A default config is auto-created on first run at:
- **macOS**: `~/Library/Application Support/ngrok-forward-proxy/config.yaml`
- **Linux**: `~/.config/ngrok-forward-proxy/config.yaml`

### Subcommands

```bash
ngrok-forward-proxy config edit    # Open config in $EDITOR
ngrok-forward-proxy config path    # Print config file path
ngrok-forward-proxy pac --proxy HOST:PORT  # Generate PAC file
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
3. **Traffic policies** (optional) can restrict source IPs at the ngrok edge

The proxy logs every connection attempt (target, allowed/denied) for audit.

## Local Testing

```bash
# Start the proxy
ngrok-forward-proxy --listen 127.0.0.1:9080 --allow="*.corp.local"

# Test with curl
curl -x socks5h://127.0.0.1:9080 http://crm.corp.local/

# Or pair with an existing ngrok agent
ngrok tcp 127.0.0.1:9080
```
