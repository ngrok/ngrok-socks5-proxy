# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[0.1.1]: https://github.com/ngrok/ngrok-socks5-proxy/releases/tag/v0.1.1
[0.1.0]: https://github.com/ngrok/ngrok-socks5-proxy/releases/tag/v0.1.0
