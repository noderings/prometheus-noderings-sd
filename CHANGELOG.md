# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- HTTP service discovery endpoint (`/targets`) for `http_sd_configs`, Grafana Alloy and
  VictoriaMetrics, with a `?port=` override per job.
- Optional `file_sd_configs` output (`--file.output`, one file per role with `{role}`), written atomically.
- Roles, chosen with `--discovery.role` and `?role=`: `vm` (VMs, with network, region,
  plan, provider, firewall, backups, instant app and tag labels), `gameserver` (game
  servers at their game port) and `dns` (every A, AAAA and CNAME host name in NodeRings
  DNS zones), with examples for probing the last two with blackbox_exporter.
- Own metrics (`noderings_sd_*`), `/-/healthy`, `/-/ready`, landing page, and TLS and basic
  auth through the Prometheus exporter-toolkit (`--web.config.file`).
- Binaries for Linux, macOS, Windows and FreeBSD, and a multi-arch container image.
