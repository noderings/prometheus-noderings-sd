#!/usr/bin/env bash
# End-to-end test against the live NodeRings API (read-only):
#   1. runs bin/prometheus-noderings-sd with NODERINGS_TOKEN,
#   2. checks /targets against the API's own VM list and the HTTP SD rules,
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

step "API: list VMs directly"
curl -fsS -H "Authorization: Bearer $NODERINGS_TOKEN" \
  "$API/v1/virtual-machines?page=0&pageSize=-1" >"$WORK/api.json"
python3 - "$WORK/api.json" >"$WORK/expected" <<'PY'
import json, sys
vms = json.load(open(sys.argv[1])).get("virtualMachines", [])
ids = sorted(v["id"] for v in vms if v.get("ipv4") or v.get("ipv6"))
print("\n".join(ids))
PY
echo "VMs with an address: $(grep -c . "$WORK/expected" || true)"

step "Start prometheus-noderings-sd"
"$ROOT/bin/prometheus-noderings-sd" \
  --web.listen-address="127.0.0.1:$SD_PORT" \
  --noderings.api-url="$API" \
  --target.port=9100 \
  --file.output="$WORK/noderings.json" \
  --log.level=debug >"$WORK/sd.log" 2>&1 &
SD_PID=$!
wait_for "http://127.0.0.1:$SD_PORT/-/ready" 60 || fail "service did not become ready"
"$ROOT/bin/prometheus-noderings-sd" --version | head -1
grep -q 'token' "$WORK/sd.log" && grep -qF "$NODERINGS_TOKEN" "$WORK/sd.log" && fail "token appears in the log"

step "HTTP SD response follows the spec"
curl -fsS -D "$WORK/headers" "http://127.0.0.1:$SD_PORT/targets" >"$WORK/targets.json"
grep -qi '^content-type: application/json' "$WORK/headers" || fail "wrong content type"
curl -fsS "http://127.0.0.1:$SD_PORT/targets?port=8080" >"$WORK/targets-8080.json"
python3 - "$WORK/targets.json" "$WORK/targets-8080.json" "$WORK/expected" <<'PY'
import json, re, sys
groups = json.load(open(sys.argv[1]))
other = json.load(open(sys.argv[2]))
expected = [l for l in open(sys.argv[3]).read().split("\n") if l]
assert isinstance(groups, list), "response is not a list"
got = sorted(g["labels"]["__meta_noderings_vm_id"] for g in groups)
assert got == sorted(expected), f"VM ids differ: got {got}, want {expected}"
name = re.compile(r"^[a-zA-Z_][a-zA-Z0-9_]*$")
for g in groups:
    assert len(g["targets"]) == 1 and g["targets"][0].endswith(":9100"), g["targets"]
    for k, v in g["labels"].items():
        assert name.match(k) and k.startswith("__meta_noderings_"), k
        assert isinstance(v, str), (k, v)
    for k in ("vm_hostname", "vm_status", "region_id", "plan_code", "provider_name"):
        assert "__meta_noderings_" + k in g["labels"], k
for g in other:
    assert g["targets"][0].endswith(":8080"), g["targets"]
print(f"{len(groups)} target groups; labels valid; ?port= override works")
PY
code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$SD_PORT/targets?port=0")
[[ "$code" == 400 ]] || fail "bad port returned $code"

step "file_sd output matches"
python3 - "$WORK/noderings.json" "$WORK/targets.json" <<'PY'
import json, sys
assert json.load(open(sys.argv[1])) == json.load(open(sys.argv[2])), "file and HTTP output differ"
print("file_sd output identical to HTTP SD output")
PY

step "Own metrics"
curl -fsS "http://127.0.0.1:$SD_PORT/metrics" >"$WORK/metrics"
for m in noderings_sd_build_info noderings_sd_virtual_machines noderings_sd_refresh_failures_total \
         noderings_sd_refresh_duration_seconds_count noderings_sd_last_refresh_success_timestamp_seconds; do
  grep -q "^$m" "$WORK/metrics" || fail "metric $m missing"
done
echo "metrics present"

step "Real Prometheus discovers the targets ($PROM_IMAGE)"
cp "$WORK/noderings.json" "$WORK/file_sd.json"
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
EOF
chmod -R a+rX "$WORK"
docker run -d --name "$NAME" --network host -v "$WORK:/work:ro" "$PROM_IMAGE" \
  --config.file=/work/prometheus.yml --web.listen-address="127.0.0.1:$PROM_PORT" \
  --storage.tsdb.path=/tmp/prom >/dev/null
wait_for "http://127.0.0.1:$PROM_PORT/-/ready" 60 || { docker logs "$NAME" | tail -20; fail "Prometheus not ready"; }
want=$(grep -c . "$WORK/expected" || true)
for i in $(seq 1 30); do
  curl -fsS "http://127.0.0.1:$PROM_PORT/api/v1/targets?state=any" >"$WORK/prom-targets.json"
  if python3 - "$WORK/prom-targets.json" "$want" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))["data"]
want = int(sys.argv[2])
by_job = {}
for t in d["activeTargets"] + d.get("droppedTargets", []):
    labels = t.get("discoveredLabels", {})
    if "__meta_noderings_vm_id" in labels:
        job = labels.get("job") or t.get("labels", {}).get("job") or t.get("scrapePool")
        by_job.setdefault(job, set()).add(labels["__meta_noderings_vm_id"])
ok = all(len(by_job.get(j, ())) == want for j in ("http_sd", "file_sd"))
print({j: len(v) for j, v in by_job.items()}, "want", want)
sys.exit(0 if ok else 1)
PY
  then break; fi
  [[ $i == 30 ]] && fail "Prometheus did not discover all VMs"
  sleep 2
done
echo "Prometheus discovered every VM through http_sd_configs and file_sd_configs"

step "Example configurations are valid (promtool)"
for f in prometheus.yml file_sd.yml; do
  docker run --rm --entrypoint promtool -v "$ROOT/examples:/ex:ro" "$PROM_IMAGE" check config --syntax-only "/ex/$f"
done

step "PASS"
