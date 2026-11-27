# prometheus-noderings-sd

[![ci](https://github.com/noderings/prometheus-noderings-sd/actions/workflows/ci.yml/badge.svg)](https://github.com/noderings/prometheus-noderings-sd/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/noderings/prometheus-noderings-sd?sort=semver)](https://github.com/noderings/prometheus-noderings-sd/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/noderings/prometheus-noderings-sd.svg)](https://pkg.go.dev/github.com/noderings/prometheus-noderings-sd)
[![Go version](https://img.shields.io/github/go-mod/go-version/noderings/prometheus-noderings-sd)](go.mod)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/noderings/prometheus-noderings-sd/badge)](https://scorecard.dev/viewer/?uri=github.com/noderings/prometheus-noderings-sd)
[![Container image](https://img.shields.io/badge/ghcr.io-prometheus--noderings--sd-blue?logo=docker&logoColor=white)](https://github.com/noderings/prometheus-noderings-sd/pkgs/container/prometheus-noderings-sd)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

Prometheus service discovery for [NodeRings](https://noderings.com) virtual machines, game servers
and DNS records.

It reads your NodeRings resources from the API and hands them to Prometheus as targets,
with region, plan, provider, status and tags as labels. New VMs are monitored as soon as
they exist; deleted ones disappear. No more hand-written IP lists in `prometheus.yml`.

| Role | Targets | Typical use |
|---|---|---|
| `vm` (default) | Each VM's public IP and a port | Scrape [node_exporter](https://github.com/prometheus/node_exporter) or your app on every VM |
| `gameserver` | Each game server's IP and game port | Check that every game server answers, with [blackbox_exporter](https://github.com/prometheus/blackbox_exporter) |
| `dns` | Every A, AAAA and CNAME host name in your NodeRings DNS zones | Check every site answers over HTTPS and watch certificate expiry, with blackbox_exporter |

Enable roles with `--discovery.role=vm,gameserver,dns`; each scrape job picks one with `?role=`.

It supports both of Prometheus' generic discovery mechanisms:

- **[HTTP SD](https://prometheus.io/docs/prometheus/latest/http_sd/)** (`http_sd_configs`),
  also used by Grafana Alloy (`discovery.http`), Grafana Agent and VictoriaMetrics.
- **[File SD](https://prometheus.io/docs/guides/file-sd/)** (`file_sd_configs`), for any
  Prometheus version.

```
Prometheus ──"which servers exist?"──▶ prometheus-noderings-sd ──▶ NodeRings API
     │      ◀──── targets + labels ────┘
     └── scrapes each VM directly ──▶ 203.0.113.10:9100 (node_exporter on the VM)
```

The service only discovers targets. The metrics come from exporters running on your VMs,
usually [node_exporter](https://github.com/prometheus/node_exporter) on port 9100.
Prometheus must be able to reach that port, so allow it in the VM's firewall ruleset.

## Install

**Binary.** Download an archive for your platform from the
[releases page](https://github.com/noderings/prometheus-noderings-sd/releases), for example
`prometheus-noderings-sd-0.1.0.linux-amd64.tar.gz`. Each release has `sha256sums.txt` and
GitHub build provenance (`gh attestation verify <file> --repo noderings/prometheus-noderings-sd`).

**Container image.** `ghcr.io/noderings/prometheus-noderings-sd` (linux/amd64, arm64, arm/v7).

```sh
docker run -d --name noderings-sd -p 10064:10064 \
  -e NODERINGS_TOKEN=nr_... ghcr.io/noderings/prometheus-noderings-sd:latest --target.port=9100
```

**From source.** `go install github.com/noderings/prometheus-noderings-sd/cmd/prometheus-noderings-sd@latest`

## Quick start

1. In the NodeRings dashboard, create an API token. A service account token with read
   access to virtual machines (and game servers or DNS, for those roles) is enough. The
   service never changes anything.
2. Start the service:

   ```sh
   export NODERINGS_TOKEN=nr_...
   prometheus-noderings-sd --target.port=9100
   curl -s localhost:10064/targets | jq .
   ```

3. Point Prometheus at it:

   ```yaml
   scrape_configs:
     - job_name: noderings-node
       http_sd_configs:
         - url: http://localhost:10064/targets?port=9100
       relabel_configs:
         # Only scrape running VMs.
         - source_labels: [__meta_noderings_vm_status]
           regex: started
           action: keep
         # Show the hostname instead of IP:port.
         - source_labels: [__meta_noderings_vm_hostname]
           target_label: instance
   ```

More in [`examples/`](examples): a full [`prometheus.yml`](examples/prometheus.yml),
[file SD](examples/file_sd.yml), [Grafana Alloy](examples/alloy.river), a
[Docker Compose](examples/docker-compose.yml) setup and a
[Grafana dashboard](examples/grafana-dashboard.json) for the whole fleet (it expects the
`instance`, `city`, `plan` and `provider` relabeling from [`prometheus.yml`](examples/prometheus.yml)).
For game servers and DNS host names, see [`blackbox.yml`](examples/blackbox.yml).

## Endpoints

| Path | Description |
|---|---|
| `/targets` | HTTP SD target list. `?role=vm\|gameserver\|dns` picks the resources (default `vm`). `?port=<n>` sets the port: for VMs it overrides `--target.port`, for game servers it replaces their own port, for DNS it is appended to the host name. |
| `/metrics` | The service's own metrics. |
| `/-/healthy` | Always `200` while the process runs. |
| `/-/ready` | `200` once every enabled role has been fetched, `503` before. |
| `/` | Landing page with links. |

`/targets` follows the HTTP SD requirements: `200` with `Content-Type: application/json`,
the full list on every request, and `[]` when there are no VMs. Before the first successful
refresh of that role it answers `503`, so Prometheus keeps its current targets instead of
dropping them. A role that is not enabled answers `400`.
When a later refresh fails, the last good list is served and the error is logged and counted.

## Labels

### Shared

`__meta_noderings_region_id`, `__meta_noderings_region_country`, `__meta_noderings_region_city`,
`__meta_noderings_plan_id`, `__meta_noderings_plan_code`, `__meta_noderings_billing_type` and
`__meta_noderings_provider_name` have the same names and meaning in the `vm` and `gameserver` roles.

### `role=vm`

Each VM becomes one target group. Its address is the public IPv4 (or IPv6 with
`--target.address-family=ipv6`; a VM without that family falls back to the other) and the
target port. VMs without any public address are left out. These labels are available
during relabeling:

| Label | Example | Description |
|---|---|---|
| `__meta_noderings_vm_id` | `0f2c…` | VM ID |
| `__meta_noderings_vm_hostname` | `web-1` | Hostname |
| `__meta_noderings_vm_status` | `started` | Operation status in lower case, for example `started` |
| `__meta_noderings_vm_distro` | `ubuntu_26_04` | Operating system |
| `__meta_noderings_vm_cpus` | `2` | vCPUs |
| `__meta_noderings_vm_memory_bytes` | `2147483648` | Memory |
| `__meta_noderings_vm_disk_bytes` | `42949672960` | Disk size |
| `__meta_noderings_public_ipv4` | `203.0.113.10` | Main public IPv4 |
| `__meta_noderings_public_ipv6` | `2001:db8::10` | Main public IPv6 |
| `__meta_noderings_additional_ipv4` | `,203.0.113.11,` | Extra IPv4 addresses |
| `__meta_noderings_region_id` | `506a…` | Region ID |
| `__meta_noderings_region_country` | `United States` | Region country |
| `__meta_noderings_region_city` | `Ashburn` | Region city |
| `__meta_noderings_plan_id` | `81de…` | Plan ID |
| `__meta_noderings_plan_code` | `SC-GC-1C-1G-20` | Plan code |
| `__meta_noderings_billing_type` | `usage` | Plan billing type in lower case |
| `__meta_noderings_provider_name` | `Enzonix` | Infrastructure provider running the VM |
| `__meta_noderings_provider_organization_id` | `518e…` | Provider ID |
| `__meta_noderings_firewall_ruleset_id` | `d626…` | Attached firewall ruleset |
| `__meta_noderings_backups_enabled` | `true` | Backup add-on active |
| `__meta_noderings_instant_app` | `wordpress` | Instant app the VM was created from |
| `__meta_noderings_tags` | `,env:prod,web,` | All tags |
| `__meta_noderings_tag_<tag>` | `true` | One label per tag; characters that are not allowed become `_` |

### `role=gameserver`

Each game server with an address becomes one target, `<ip>:<game port>`.

| Label | Example | Description |
|---|---|---|
| `__meta_noderings_gameserver_id` | `5b1e…` | Game server ID |
| `__meta_noderings_gameserver_name` | `survival` | Name |
| `__meta_noderings_gameserver_game` | `Minecraft` | Game |
| `__meta_noderings_gameserver_variant` | `paper` | Variant |
| `__meta_noderings_gameserver_version` | `1.21.4` | Game version |
| `__meta_noderings_gameserver_phase` | `ready` | `pending`, `ready`, `error`, `failed` |
| `__meta_noderings_gameserver_power_state` | `running` | Power state as last reported |
| `__meta_noderings_gameserver_ip` | `198.51.100.7` | Game IP |
| `__meta_noderings_gameserver_port` | `25565` | Game port |
| `__meta_noderings_gameserver_node_id` | `91c0…` | Node the server runs on |
| shared labels | | Region, plan, billing type, provider |

### `role=dns`

Each host name with a record of a type in `--dns.record-types` (default `A,AAAA,CNAME`) becomes
one target: the fully qualified name without a port, ready to be passed to blackbox_exporter.
System records (SOA, NS) and wildcards are left out; an A and an AAAA record of the same name
are one target.

| Label | Example | Description |
|---|---|---|
| `__meta_noderings_dns_zone_id` | `3cd2…` | Zone ID |
| `__meta_noderings_dns_zone_name` | `example.com` | Zone |
| `__meta_noderings_dns_zone_state` | `active` | Zone state, for example `pending_ns` before delegation |
| `__meta_noderings_dns_zone_delegated` | `true` | The zone's delegation to NodeRings is in place |
| `__meta_noderings_dns_record_name` | `www` | Name relative to the zone, `@` for the apex |
| `__meta_noderings_dns_record_fqdn` | `www.example.com` | Full host name |
| `__meta_noderings_dns_record_types` | `,A,AAAA,` | Record types of the name |
| `__meta_noderings_dns_record_values` | `,203.0.113.1,` | Record values |
| `__meta_noderings_dns_record_ttl` | `300` | Lowest TTL of the name's records |

See [`examples/blackbox.yml`](examples/blackbox.yml) for probing game servers (TCP) and every
host name (HTTPS and certificate expiry), with the matching
[blackbox_exporter modules](examples/blackbox-exporter.yml).

### Lists

List labels start and end with the separator (`--target.tag-separator`, default `,`), as in
Prometheus' built-in discoveries, so `regex: .*,web,.*` matches the tag `web` exactly.

## Configuration

All flags, with their environment variables:

| Flag | Default | Description |
|---|---|---|
| `NODERINGS_TOKEN` (env only) | | API token. Never passed as a flag, so it does not show up in `ps`. |
| `--noderings.token-file` (`NODERINGS_TOKEN_FILE`) | | Read the token from a file instead (Docker or Kubernetes secrets). |
| `--noderings.organization-id` (`NODERINGS_ORGANIZATION_ID`) | token's organization | Organization to discover. |
| `--noderings.api-url` (`NODERINGS_API_URL`) | `https://api.noderings.com` | API URL. |
| `--discovery.role` | `vm` | Roles to discover: `vm`, `gameserver`, `dns` (comma-separated or repeated). |
| `--discovery.refresh-interval` | `60s` | How often each role is fetched (minimum `10s`). |
| `--dns.record-types` | `A,AAAA,CNAME` | Record types that become `dns` targets. |
| `--target.port` | `80` | Port of every VM target. Clients can override it with `?port=`. |
| `--target.address-family` | `ipv4` | `ipv4` or `ipv6`. |
| `--target.tag-separator` | `,` | Separator for list labels. |
| `--file.output` | | Also write the targets to this file for `file_sd_configs`. Written atomically, only when it changes. With several roles, put `{role}` in the name (`/etc/prometheus/noderings-{role}.json`) to get one file per role. |
| `--web.listen-address` | `:10064` | Listen address(es). |
| `--web.config.file` | | [TLS and basic auth](https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md) for every endpoint. |
| `--web.targets-path` | `/targets` | Path of the HTTP SD endpoint. |
| `--web.telemetry-path` | `/metrics` | Path of the service's metrics. |
| `--log.level`, `--log.format` | `info`, `logfmt` | Logging. |

Port 10064 is the next free port in the
[Prometheus default port allocations](https://github.com/prometheus/prometheus/wiki/Default-port-allocations).

### Securing it

The target list contains your VMs' IP addresses. Run the service next to Prometheus, keep it
off the public internet, or protect it with `--web.config.file`. Prometheus then
authenticates with `basic_auth` or `tls_config` in `http_sd_configs`.

## Metrics

| Metric | Type | Description |
|---|---|---|
| `noderings_sd_resources{role}` | gauge | VMs, game servers or DNS record sets in the last successful refresh |
| `noderings_sd_refresh_duration_seconds{role}` | histogram | Time to fetch a role from the API |
| `noderings_sd_refresh_failures_total{role}` | counter | Failed refreshes |
| `noderings_sd_last_refresh_success_timestamp_seconds{role}` | gauge | Time of the last successful refresh |
| `noderings_sd_build_info` | gauge | Version, revision, branch and Go version |

Plus the standard `go_*` and `process_*` metrics. A useful alert:

```yaml
- alert: NodeRingsSDStale
  expr: time() - noderings_sd_last_refresh_success_timestamp_seconds > 600
  for: 5m
  annotations:
    summary: NodeRings service discovery has not refreshed for 10 minutes.
```

## Development

```sh
make test     # unit tests, no token needed
make lint     # golangci-lint
NODERINGS_TOKEN=nr_... make e2e   # every role against the live API plus a real Prometheus in Docker (read-only)
```

See [CONTRIBUTING.md](CONTRIBUTING.md). Security reports: [SECURITY.md](SECURITY.md).

## Related

Other NodeRings integrations: [Terraform/OpenTofu](https://github.com/noderings/terraform-provider-noderings),
[Pulumi](https://github.com/noderings/pulumi-noderings),
[Ansible](https://github.com/noderings/ansible-collection-noderings),
[external-dns](https://github.com/noderings/external-dns-noderings-webhook),
[cert-manager](https://github.com/noderings/cert-manager-webhook-noderings).

## License

[Apache 2.0](LICENSE)
