# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.4] - 2026-10-22

### Security

- Release binaries are now built with Go 1.27.3 (previously 1.26.3),
  picking up the Go standard library security fixes released since.
  Exposure in earlier releases was low: the proxy's only TLS connection
  is to the ngrok edge, verified against ngrok's pinned CA.
- Upgraded `golang.ngrok.com/ngrok/v2` from v2.1.4 to v2.3.0.
- Upgraded indirect dependency `golang.org/x/net` from v0.50.0 to
  v0.61.0 and `golang.org/x/sys` from v0.41.0 to v0.49.0. `govulncheck`
  now reports no known vulnerabilities in any required module. None of
  the earlier `x/net` findings were reachable from this code.

## [0.1.3] - 2026-08-04

### Added

- `service install|uninstall|start|stop|restart|status` subcommand to run
  the proxy as a native background service (systemd on Linux, launchd on
  macOS, Windows Service on Windows).
- `--log-format` (`text`/`logfmt`/`json`) and `--log`
  (`stdout`/`stderr`/`false`/file path) flags and config options,
  matching the ngrok agent's `log_format`/`log` options.

## [0.1.2] - 2026-07-31

### Added

- `--dial-timeout` flag / `dial_timeout` config option to configure the
  timeout for connecting to allowlisted targets (default `10s`, previously
  hardcoded). Found to be worth exposing after manual testing surfaced
  page-load delays when an allowlisted target was reachable but slow or
  unresponsive.

### Fixed

- Target hostname resolution now always uses Go's pure-Go DNS resolver
  (`PreferGo: true`) instead of the OS-native resolver. On macOS, the
  OS-native resolver routes any `.local`-suffixed hostname (e.g. the
  README's own example pattern `*.corp.local`) through mDNS/Bonjour,
  adding ~5 seconds of latency per connection even when the name is
  already present in `/etc/hosts`. Confirmed via a real Chrome
  `--proxy-server` test. This also makes the Docker image's
  `/etc/nsswitch.conf` workaround (added in an earlier fix) unnecessary
  for the proxy's own resolution, though it's left in place as harmless
  defense-in-depth.

## [0.1.1] - 2026-07-31

### Fixed

- `--listen` and `--url` were not actually enforced as mutually exclusive
  when both were passed as CLI flags at the same time: `--listen` silently
  won and `--url` was discarded with no error, because the override logic
  cleared each other's config value before the mutual-exclusivity check
  could run. Found via manual testing on the v0.1.0 release. Now validated
  against the raw flag values before either override is applied.

## [0.1.0] - 2026-07-31

### Added

- Initial release.
- SOCKS5 and HTTP CONNECT forward proxy with hostname allowlisting.
- ngrok SDK integration for creating TCP endpoints (public, internal, and
  Kubernetes bindings).
- Local listen mode for chaining to an existing ngrok agent.
- YAML configuration file support with CLI flag overrides.
- `config` subcommand for editing and locating the config file.
- `pac` subcommand for generating browser PAC files from allowlist patterns.
- Custom DNS resolver support.
- Prebuilt cross-platform binaries (Linux, macOS, Windows; amd64/arm64) via
  GitHub Releases.

[Unreleased]: https://github.com/ngrok/ngrok-socks5-proxy/compare/v0.1.4...HEAD
[0.1.4]: https://github.com/ngrok/ngrok-socks5-proxy/releases/tag/v0.1.4
[0.1.3]: https://github.com/ngrok/ngrok-socks5-proxy/releases/tag/v0.1.3
[0.1.2]: https://github.com/ngrok/ngrok-socks5-proxy/releases/tag/v0.1.2
[0.1.1]: https://github.com/ngrok/ngrok-socks5-proxy/releases/tag/v0.1.1
[0.1.0]: https://github.com/ngrok/ngrok-socks5-proxy/releases/tag/v0.1.0
