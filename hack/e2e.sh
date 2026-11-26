#!/usr/bin/env bash
# End-to-end test against the live NodeRings API (read-only):
#   1. runs bin/prometheus-noderings-sd with NODERINGS_TOKEN and every role,
#   2. checks /targets for vm, gameserver and dns against the API's own lists
#      and the HTTP SD rules,
#   3. checks the file_sd output,
#   4. runs a real Prometheus (Docker) with http_sd_configs and file_sd_configs
#      and waits until it has discovered every VM,
#   5. validates the example configurations with promtool.
# Needs: NODERINGS_TOKEN, docker, curl, python3. Creates nothing in NodeRings.
set -euo pipefail

: "${NODERINGS_TOKEN:?set NODERINGS_TOKEN}"
API=${NODERINGS_API_URL:-https://api.noderings.com}
PROM_IMAGE=${PROM_IMAGE:-prom/prometheus:v3.7.3}
SD_PORT=${SD_PORT:-19064}
PROM_PORT=${PROM_PORT:-19090}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
NAME="noderings-sd-e2e-$$"
SD_PID=""

cleanup() {
  [[ -n "$SD_PID" ]] && kill "$SD_PID" 2>/dev/null || true
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$*"; }
fail() { echo "FAIL: $*" >&2; [[ -f "$WORK/sd.log" ]] && tail -20 "$WORK/sd.log" >&2; exit 1; }

wait_for() { # url, seconds
  for _ in $(seq 1 "$2"); do
    curl -fsS -o /dev/null "$1" 2>/dev/null && return 0
    sleep 1
  done
  return 1
}

CHECK="python3 $ROOT/hack/e2e_check.py"

step "API: list VMs, game servers and DNS records directly"
api() { curl -fsS -H "Authorization: Bearer $NODERINGS_TOKEN" "$API$1"; }
api "/v1/virtual-machines?page=0&pageSize=-1" >"$WORK/api-vms.json"
api "/v1/game-servers?pageSize=-1" >"$WORK/api-games.json"
api "/v1/dns/zones?page=0&pageSize=-1" >"$WORK/api-zones.json"
: >"$WORK/expected-dns"
$CHECK zones "$WORK/api-zones.json" | while read -r zid zname; do
  api "/v1/dns/zones/$zid/records?page=0&pageSize=100" >"$WORK/records.json"
  $CHECK dns-names "$WORK/records.json" "$zname" >>"$WORK/expected-dns"
done
sed -i '/^$/d' "$WORK/expected-dns"
want_dns=$(grep -c . "$WORK/expected-dns" || true)
echo "DNS host names in the API: $want_dns"

step "Start prometheus-noderings-sd with every role"
"$ROOT/bin/prometheus-noderings-sd" \
  --web.listen-address="127.0.0.1:$SD_PORT" \
  --noderings.api-url="$API" \
  --discovery.role=vm,gameserver,dns \
  --target.port=9100 \
  --file.output="$WORK/noderings-{role}.json" \
  --log.level=debug >"$WORK/sd.log" 2>&1 &
SD_PID=$!
wait_for "http://127.0.0.1:$SD_PORT/-/ready" 60 || fail "service did not become ready"
"$ROOT/bin/prometheus-noderings-sd" --version | head -1
grep -qF "$NODERINGS_TOKEN" "$WORK/sd.log" && fail "token appears in the log"

step "HTTP SD responses follow the spec and match the API"
sd() { curl -fsS "http://127.0.0.1:$SD_PORT/targets$1"; }
curl -fsS -D "$WORK/headers" "http://127.0.0.1:$SD_PORT/targets" >"$WORK/http-vm.json"
grep -qi '^content-type: application/json' "$WORK/headers" || fail "wrong content type"
sd "?port=8080" >"$WORK/http-vm-8080.json"
sd "?role=gameserver" >"$WORK/http-gameserver.json"
sd "?role=dns" >"$WORK/http-dns.json"
$CHECK vm "$WORK/http-vm.json" "$WORK/http-vm-8080.json" "$WORK/api-vms.json" || fail "vm role"
$CHECK gameserver "$WORK/http-gameserver.json" "$WORK/api-games.json" || fail "gameserver role"
$CHECK dns "$WORK/http-dns.json" "$WORK/expected-dns" || fail "dns role"
for bad in "?port=0" "?role=nope"; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$SD_PORT/targets$bad")
  [[ "$code" == 400 ]] || fail "$bad returned $code, want 400"
done

step "file_sd output: one file per role, identical to HTTP SD"
for role in vm gameserver dns; do
  $CHECK same "$WORK/noderings-$role.json" "$WORK/http-$role.json" || fail "file_sd output for $role differs"
done
echo "identical for vm, gameserver and dns"

step "Own metrics"
curl -fsS "http://127.0.0.1:$SD_PORT/metrics" >"$WORK/metrics"
for m in noderings_sd_build_info 'noderings_sd_resources{role="dns"}' 'noderings_sd_resources{role="gameserver"}' \
         'noderings_sd_resources{role="vm"}' noderings_sd_refresh_failures_total \
         noderings_sd_refresh_duration_seconds_count noderings_sd_last_refresh_success_timestamp_seconds; do
  grep -qF "$m" "$WORK/metrics" || fail "metric $m missing"
done
echo "metrics present"

step "Real Prometheus discovers the targets ($PROM_IMAGE)"
cp "$WORK/noderings-vm.json" "$WORK/file_sd.json"
cat >"$WORK/prometheus.yml" <<EOF
global:
  scrape_interval: 15s
scrape_configs:
  - job_name: http_sd
    http_sd_configs:
      - url: http://127.0.0.1:$SD_PORT/targets?port=9100
        refresh_interval: 15s
  - job_name: file_sd
    file_sd_configs:
      - files: [/work/file_sd.json]
  # The blackbox pattern from examples/blackbox.yml. No blackbox_exporter runs
  # here, so these targets are down; the test only checks discovery.
  - job_name: dns_probe
    metrics_path: /probe
    params:
      module: [http_2xx]
    http_sd_configs:
      - url: http://127.0.0.1:$SD_PORT/targets?role=dns
        refresh_interval: 15s
    relabel_configs:
      - source_labels: [__address__]
        target_label: __param_target
      - source_labels: [__param_target]
        target_label: instance
      - target_label: __address__
        replacement: 127.0.0.1:9115
EOF
chmod -R a+rX "$WORK"
docker run -d --name "$NAME" --network host -v "$WORK:/work:ro" "$PROM_IMAGE" \
  --config.file=/work/prometheus.yml --web.listen-address="127.0.0.1:$PROM_PORT" \
  --storage.tsdb.path=/tmp/prom >/dev/null
wait_for "http://127.0.0.1:$PROM_PORT/-/ready" 60 || { docker logs "$NAME" | tail -20; fail "Prometheus not ready"; }
want_vms=$($CHECK vm-count "$WORK/api-vms.json")
for i in $(seq 1 30); do
  curl -fsS "http://127.0.0.1:$PROM_PORT/api/v1/targets?state=any" >"$WORK/prom-targets.json"
  $CHECK prometheus "$WORK/prom-targets.json" "$want_vms" "$want_dns" && break
  [[ $i == 30 ]] && fail "Prometheus did not discover every target"
  sleep 2
done
echo "Prometheus discovered every VM (http_sd and file_sd) and every DNS host name (blackbox job)"

step "Example configurations are valid (promtool)"
for f in prometheus.yml file_sd.yml blackbox.yml; do
  docker run --rm --entrypoint promtool -v "$ROOT/examples:/ex:ro" "$PROM_IMAGE" check config --syntax-only "/ex/$f"
done

step "PASS"
