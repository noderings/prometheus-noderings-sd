"""Checks used by hack/e2e.sh. Each subcommand exits non-zero on failure."""
import json
import re
import sys

LABEL = re.compile(r"^[a-zA-Z_][a-zA-Z0-9_]*$")


def load(path):
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def expected_vms(api_vms):
    vms = load(api_vms).get("virtualMachines", [])
    return sorted(v["id"] for v in vms if v.get("ipv4") or v.get("ipv6"))


def expected_games(api_games):
    games = load(api_games).get("gameServers", [])
    return sorted(g["id"] for g in games if g.get("allocationIp") and int(g.get("allocationPort") or 0) > 0)


def dns_names(records_file, zone):
    zone = zone.rstrip(".").lower()
    names = set()
    for r in load(records_file).get("records", []):
        if r.get("system") or r["type"] not in ("A", "AAAA", "CNAME") or "*" in r["name"]:
            continue
        n = r["name"].rstrip(".").lower()
        names.add(zone if n in ("", "@") else n + "." + zone)
    return sorted(names)


def check_labels(groups):
    assert isinstance(groups, list), "response is not a list"
    for g in groups:
        assert len(g["targets"]) == 1, g["targets"]
        for k, v in g["labels"].items():
            assert LABEL.match(k) and k.startswith("__meta_noderings_"), k
            assert isinstance(v, str), (k, v)


def cmd_zones(api_zones):
    for z in load(api_zones).get("zones", []):
        print(z["id"], z["name"])


def cmd_dns_names(records_file, zone):
    print("\n".join(dns_names(records_file, zone)))


def cmd_vm(targets, targets_8080, api_vms):
    groups, other = load(targets), load(targets_8080)
    check_labels(groups)
    want = expected_vms(api_vms)
    got = sorted(g["labels"]["__meta_noderings_vm_id"] for g in groups)
    assert got == want, f"VM ids differ: got {got}, want {want}"
    for g in groups:
        assert g["targets"][0].endswith(":9100"), g["targets"]
        for k in ("vm_hostname", "vm_status", "region_id", "plan_code", "provider_name"):
            assert "__meta_noderings_" + k in g["labels"], k
    for g in other:
        assert g["targets"][0].endswith(":8080"), g["targets"]
    print(f"vm: {len(groups)} targets match the API; labels valid; ?port= works")


def cmd_gameserver(targets, api_games):
    groups = load(targets)
    check_labels(groups)
    got = sorted(g["labels"]["__meta_noderings_gameserver_id"] for g in groups)
    want = expected_games(api_games)
    assert got == want, f"game servers differ: got {got}, want {want}"
    for g in groups:
        assert g["targets"][0].endswith(":" + g["labels"]["__meta_noderings_gameserver_port"]), g
    print(f"gameserver: {len(groups)} targets match the API")


def cmd_dns(targets, expected_file):
    groups = load(targets)
    check_labels(groups)
    with open(expected_file, encoding="utf-8") as f:
        want = sorted(line for line in f.read().split("\n") if line)
    got = sorted(g["targets"][0] for g in groups)
    assert got == want, f"DNS targets differ: got {got}, want {want}"
    for g in groups:
        assert g["labels"]["__meta_noderings_dns_record_fqdn"] == g["targets"][0], g
    print(f"dns: {len(groups)} host names match the API")


def cmd_vm_count(api_vms):
    print(len(expected_vms(api_vms)))


def cmd_same(a, b):
    assert load(a) == load(b), f"{a} and {b} differ"


def cmd_prometheus(prom_targets, want_vms, want_dns):
    d = load(prom_targets)["data"]
    by_job = {}
    for t in d["activeTargets"] + d.get("droppedTargets", []):
        labels = t.get("discoveredLabels", {})
        if "__meta_noderings_vm_id" in labels:
            job = labels.get("job") or t.get("scrapePool")
            by_job.setdefault(job, set()).add(labels["__meta_noderings_vm_id"])
    dns = {t["labels"].get("instance") for t in d["activeTargets"] if t.get("scrapePool") == "dns_probe"}
    want_vms, want_dns = int(want_vms), int(want_dns)
    print({j: len(v) for j, v in by_job.items()}, "want", want_vms, "| dns_probe", len(dns), "want", want_dns)
    ok = all(len(by_job.get(j, ())) == want_vms for j in ("http_sd", "file_sd")) and len(dns) == want_dns
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    globals()["cmd_" + sys.argv[1].replace("-", "_")](*sys.argv[2:])
