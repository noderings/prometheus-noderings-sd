# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- HTTP service discovery endpoint (`/targets`) for `http_sd_configs`, Grafana Alloy and
  VictoriaMetrics, with a `?port=` override per job.
- Optional `file_sd_configs` output (`--file.output`), written atomically.
- `__meta_noderings_*` labels for VM, network, region, plan, provider, firewall, backups,
  instant app and tags.
- Own metrics (`noderings_sd_*`), `/-/healthy`, `/-/ready`, landing page, and TLS and basic
  auth through the Prometheus exporter-toolkit (`--web.config.file`).
- Binaries for Linux, macOS, Windows and FreeBSD, and a multi-arch container image.
