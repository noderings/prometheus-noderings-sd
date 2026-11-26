package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/noderings/prometheus-noderings-sd/internal/noderings"
)

func sampleGames() []noderings.GameServer {
	return []noderings.GameServer{
		{
			ID: "gs-1", Name: "survival", GameName: "Minecraft", Variant: "paper", VersionValue: "1.21.4",
			AllocationIP: "198.51.100.7", AllocationPort: 25565, Phase: "GAME_SERVER_PHASE_READY", PowerState: "running",
			NodeID: "n1", RegionID: "r2", RegionCity: "Frankfurt", RegionCountry: "Germany",
			PlanID: "p2", PlanCode: "GS-4G", PlanBillingType: "BILLING_TYPE_MONTHLY", ProviderName: "Acme",
		},
		{ID: "gs-2", Name: "pending", Phase: "GAME_SERVER_PHASE_PENDING"},
	}
}

func TestBuildGameServers(t *testing.T) {
	groups := BuildGameServers(sampleGames(), Options{})
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1 (no address yet is left out)", len(groups))
	}
	g := groups[0]
	if !reflect.DeepEqual(g.Targets, []string{"198.51.100.7:25565"}) {
		t.Errorf("targets = %v", g.Targets)
	}
	want := map[string]string{
		LabelGameServerID: "gs-1", LabelGameServerName: "survival", LabelGameServerGame: "Minecraft",
		LabelGameServerVariant: "paper", LabelGameServerVersion: "1.21.4", LabelGameServerPhase: "ready",
		LabelGameServerPowerState: "running", LabelGameServerIP: "198.51.100.7", LabelGameServerPort: "25565",
		LabelGameServerNodeID: "n1", LabelRegionID: "r2", LabelRegionCity: "Frankfurt", LabelRegionCountry: "Germany",
		LabelPlanID: "p2", LabelPlanCode: "GS-4G", LabelBillingType: "monthly", LabelProviderName: "Acme",
	}
	if !reflect.DeepEqual(g.Labels, want) {
		t.Errorf("labels mismatch\n got: %v\nwant: %v", g.Labels, want)
	}
	if got := BuildGameServers(sampleGames(), Options{Port: 25575})[0].Targets[0]; got != "198.51.100.7:25575" {
		t.Errorf("port override: %s", got)
	}
}

func sampleDNS() ([]noderings.DNSZone, map[string][]noderings.DNSRecord) {
	zones := []noderings.DNSZone{{ID: "z1", Name: "Example.com.", State: "DNS_ZONE_STATE_ACTIVE", DelegationOK: true}}
	records := map[string][]noderings.DNSRecord{"z1": {
		{Name: "@", Type: "SOA", TTL: 3600, RData: []string{"ns1. host. 0 1 1 1 1"}, System: true},
		{Name: "@", Type: "NS", TTL: 3600, RData: []string{"ans1.nrings.io."}, System: true},
		{Name: "@", Type: "A", TTL: 300, RData: []string{"203.0.113.2", "203.0.113.1"}},
		{Name: "www", Type: "A", TTL: 600, RData: []string{"203.0.113.1"}},
		{Name: "www", Type: "AAAA", TTL: 300, RData: []string{"2001:db8::1"}},
		{Name: "blog", Type: "CNAME", TTL: 300, RData: []string{"www.example.com."}},
		{Name: "*", Type: "A", TTL: 300, RData: []string{"203.0.113.9"}},
		{Name: "mail", Type: "MX", TTL: 300, RData: []string{"10 mx.example.com."}},
		{Name: "a-www", Type: "TXT", TTL: 300, RData: []string{`"heritage=external-dns"`}},
	}}
	return zones, records
}

func TestBuildDNS(t *testing.T) {
	zones, records := sampleDNS()
	groups := BuildDNS(zones, records, Options{})
	var got []string
	for _, g := range groups {
		got = append(got, g.Targets[0])
	}
	if !reflect.DeepEqual(got, []string{"example.com", "blog.example.com", "www.example.com"}) {
		t.Fatalf("targets = %v (apex, then names sorted; system, wildcard, MX and TXT left out)", got)
	}
	www := groups[2].Labels
	want := map[string]string{
		LabelDNSZoneID: "z1", LabelDNSZoneName: "example.com", LabelDNSZoneState: "active", LabelDNSZoneDelegated: "true",
		LabelDNSRecordName: "www", LabelDNSRecordFQDN: "www.example.com", LabelDNSRecordTypes: ",A,AAAA,",
		LabelDNSRecordValues: ",2001:db8::1,203.0.113.1,", LabelDNSRecordTTL: "300",
	}
	if !reflect.DeepEqual(www, want) {
		t.Errorf("www labels\n got: %v\nwant: %v", www, want)
	}
	if v := groups[1].Labels[LabelDNSRecordValues]; v != ",www.example.com," {
		t.Errorf("CNAME value = %q (trailing dot trimmed)", v)
	}
	if got := BuildDNS(zones, records, Options{Port: 443})[2].Targets[0]; got != "www.example.com:443" {
		t.Errorf("port: %s", got)
	}
	if got := BuildDNS(zones, records, Options{DNSRecordTypes: []string{"mx"}}); len(got) != 1 || got[0].Targets[0] != "mail.example.com" {
		t.Errorf("record types filter: %+v", got)
	}
	if got := BuildDNS(nil, nil, Options{}); got == nil || len(got) != 0 {
		t.Errorf("no zones must give an empty, non-nil list: %#v", got)
	}
}

func TestRolesThroughHandler(t *testing.T) {
	zones, records := sampleDNS()
	l := &fakeLister{vms: sampleVMs(), games: sampleGames(), zones: zones, records: records}
	d := mustNew(t, l, []string{"vm", "gameserver", "dns", "vm"}, Options{Port: 9100}, nil)
	if !reflect.DeepEqual(d.Roles(), []string{"vm", "gameserver", "dns"}) {
		t.Errorf("roles = %v", d.Roles())
	}
	h := d.Handler()
	if rec := get(t, h, "/targets?role=dns"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("dns before refresh: %d", rec.Code)
	}
	if err := d.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.Count(RoleDNS) != 9 || d.Count(RoleGameServer) != 2 || !d.AllReady() {
		t.Errorf("counts: dns %d, gameserver %d", d.Count(RoleDNS), d.Count(RoleGameServer))
	}
	var groups []TargetGroup
	for target, want := range map[string]string{
		"/targets":                        "[2001:db8::20]:9100",
		"/targets?role=vm":                "[2001:db8::20]:9100",
		"/targets?role=gameserver":        "198.51.100.7:25565", // not --target.port
		"/targets?role=gameserver&port=1": "198.51.100.7:1",
		"/targets?role=dns":               "example.com",
		"/targets?role=DNS&port=443":      "example.com:443",
	} {
		rec := get(t, h, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", target, rec.Code, rec.Body)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &groups); err != nil || groups[0].Targets[0] != want {
			t.Errorf("%s: first target %v, want %s (%v)", target, groups, want, err)
		}
	}
	if rec := get(t, h, "/targets?role=k8s"); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown role: %d", rec.Code)
	}

	vmOnly := mustNew(t, l, nil, Options{}, nil)
	if rec := get(t, vmOnly.Handler(), "/targets?role=dns"); rec.Code != http.StatusBadRequest {
		t.Errorf("disabled role: %d", rec.Code)
	}
	if _, err := New(l, []string{"bogus"}, Options{}, nil, nil); err == nil {
		t.Error("unknown role accepted by New")
	}
}

func TestRefreshKeepsWorkingRoles(t *testing.T) {
	l := &fakeLister{vms: sampleVMs()}
	d := mustNew(t, l, []string{RoleVM, RoleDNS}, Options{}, nil)
	l.zones = []noderings.DNSZone{{ID: "z", Name: "x.test"}}
	failing := &failingDNS{fakeLister: l}
	d.lister = failing
	err := d.Refresh(context.Background())
	if err == nil || !errors.Is(err, errDNSDown) {
		t.Fatalf("err = %v", err)
	}
	if !d.Ready(RoleVM) || d.Ready(RoleDNS) {
		t.Error("a failing role must not block the others")
	}
}

var errDNSDown = errors.New("dns down")

type failingDNS struct{ *fakeLister }

func (f *failingDNS) ListDNSRecords(context.Context, string) ([]noderings.DNSRecord, error) {
	return nil, errDNSDown
}
