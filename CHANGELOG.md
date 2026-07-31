# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[0.1.0]: https://github.com/ngrok/ngrok-socks5-proxy/releases/tag/v0.1.0
