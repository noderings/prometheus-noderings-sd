package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/noderings/prometheus-noderings-sd/internal/noderings"
)

func sampleVMs() []noderings.VirtualMachine {
	return []noderings.VirtualMachine{
		{
			ID: "vm-b", Hostname: "web-1", OperationStatus: "OPERATION_STATUS_STARTED",
			CPU: 2, Memory: 2048, Storage: 40960,
			IPv4: "203.0.113.10", IPv6: "2001:db8::10",
			AdditionalIPv4: []noderings.AdditionalIP{{IPAddress: "203.0.113.11"}, {IPAddress: "203.0.113.12"}},
			Distro:         "DISTRO_UBUNTU_26_04", RegionID: "r1", RegionCountry: "United States", RegionCity: "Ashburn",
			PlanID: "p1", PlanCode: "SC-2C-2G", PlanBillingType: "BILLING_TYPE_USAGE",
			ProviderName: "Enzonix", ProviderOrganizationID: "po1", FirewallRulesetID: "fw1",
			HasActiveBackupSubscription: true, Tags: []string{"web", "env:prod", " "},
		},
		{ID: "vm-a", Hostname: "v6-only", OperationStatus: "OPERATION_STATUS_STOPPED", IPv6: "2001:db8::20", Distro: "DISTRO_UNSPECIFIED"},
		{ID: "vm-c", Hostname: "no-address", OperationStatus: "OPERATION_STATUS_PROVISIONING"},
	}
}

func TestBuildLabels(t *testing.T) {
	groups := Build(sampleVMs(), Options{Port: 9100})
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2 (the VM without an address is left out)", len(groups))
	}
	g := groups[0]
	if !reflect.DeepEqual(g.Targets, []string{"203.0.113.10:9100"}) {
		t.Errorf("targets = %v", g.Targets)
	}
	want := map[string]string{
		LabelVMID: "vm-b", LabelVMHostname: "web-1", LabelVMStatus: "started", LabelVMDistro: "ubuntu_26_04",
		LabelVMCPUs: "2", LabelVMMemoryBytes: "2147483648", LabelVMDiskBytes: "42949672960",
		LabelPublicIPv4: "203.0.113.10", LabelPublicIPv6: "2001:db8::10",
		LabelAdditionalIPv4: ",203.0.113.11,203.0.113.12,",
		LabelRegionID:       "r1", LabelRegionCountry: "United States", LabelRegionCity: "Ashburn",
		LabelPlanID: "p1", LabelPlanCode: "SC-2C-2G", LabelBillingType: "usage",
		LabelProviderName: "Enzonix", LabelProviderOrgID: "po1", LabelFirewallRulesetID: "fw1",
		LabelBackupsEnabled: "true", LabelInstantApp: "",
		LabelTags: ",env:prod,web,", LabelTagPrefix + "web": "true", LabelTagPrefix + "env_prod": "true",
	}
	if !reflect.DeepEqual(g.Labels, want) {
		t.Errorf("labels mismatch\n got: %v\nwant: %v", g.Labels, want)
	}
	for k := range g.Labels {
		if !strings.HasPrefix(k, "__meta_noderings_") || SanitizeLabelName(k) != k {
			t.Errorf("invalid label name %q", k)
		}
	}

	v6 := groups[1]
	if !reflect.DeepEqual(v6.Targets, []string{"[2001:db8::20]:9100"}) {
		t.Errorf("IPv6 fallback targets = %v", v6.Targets)
	}
	if v6.Labels[LabelVMDistro] != "" || v6.Labels[LabelTags] != "" || v6.Labels[LabelAdditionalIPv4] != "" {
		t.Errorf("empty values not empty: %v", v6.Labels)
	}
}

func TestBuildIPv6AndSeparator(t *testing.T) {
	groups := Build(sampleVMs()[:1], Options{AddressFamily: AddressIPv6, TagSeparator: ";"})
	if got := groups[0].Targets[0]; got != "[2001:db8::10]:80" {
		t.Errorf("target = %q", got)
	}
	if got := groups[0].Labels[LabelTags]; got != ";env:prod;web;" {
		t.Errorf("tags = %q", got)
	}
}

func TestSanitizeLabelName(t *testing.T) {
	for in, want := range map[string]string{"web": "web", "env:prod": "env_prod", "9lives": "9lives", "a-b.c d": "a_b_c_d", "ü": "_"} {
		if got := SanitizeLabelName(in); got != want {
			t.Errorf("SanitizeLabelName(%q) = %q, want %q", in, got, want)
		}
	}
}

type fakeLister struct {
	vms []noderings.VirtualMachine
	err error
}

func (f *fakeLister) ListVirtualMachines(context.Context) ([]noderings.VirtualMachine, error) {
	return f.vms, f.err
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestHandler(t *testing.T) {
	lister := &fakeLister{err: errors.New("down")}
	reg := prometheus.NewRegistry()
	d := New(lister, Options{Port: 9100}, nil, reg)
	h := d.Handler()

	// Before any successful refresh: 503, so Prometheus keeps its targets.
	if err := d.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh error")
	}
	if rec := get(t, h, "/targets"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("before first refresh: status %d", rec.Code)
	}

	// No VMs: 200 with [].
	lister.err = nil
	if err := d.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec := get(t, h, "/targets")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list: %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content type %q", ct)
	}

	// VMs, default and overridden port.
	lister.vms = sampleVMs()
	if err := d.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	var groups []TargetGroup
	if err := json.Unmarshal(get(t, h, "/targets").Body.Bytes(), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Targets[0] != "[2001:db8::20]:9100" {
		t.Fatalf("groups (sorted by VM id) = %+v", groups)
	}
	if err := json.Unmarshal(get(t, h, "/targets?port=8080").Body.Bytes(), &groups); err != nil {
		t.Fatal(err)
	}
	if groups[1].Targets[0] != "203.0.113.10:8080" {
		t.Errorf("port override: %v", groups[1].Targets)
	}
	if rec := get(t, h, "/targets?port=99999"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad port: status %d", rec.Code)
	}

	// A failed refresh keeps the previous list.
	lister.err = errors.New("down again")
	_ = d.Refresh(context.Background())
	if d.VMCount() != 3 || get(t, h, "/targets").Code != http.StatusOK {
		t.Error("previous list not kept after a failed refresh")
	}

	if got := testutil.ToFloat64(d.metrics.failures); got != 2 {
		t.Errorf("failures = %v, want 2", got)
	}
	if got := testutil.ToFloat64(d.metrics.vms); got != 3 {
		t.Errorf("vms gauge = %v, want 3", got)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/targets", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d", rec.Code)
	}
}

func TestMetricsLint(t *testing.T) {
	reg := prometheus.NewRegistry()
	New(&fakeLister{}, Options{}, nil, reg)
	problems, err := testutil.GatherAndLint(reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Errorf("metric %s: %s", p.Metric, p.Text)
	}
}

func TestOnUpdateAndFileWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "noderings.json")
	fw := &FileWriter{Path: path}
	d := New(&fakeLister{vms: sampleVMs()}, Options{}, nil, nil)
	d.OnUpdate(func(vms []noderings.VirtualMachine) {
		if err := fw.Write(Build(vms, Options{})); err != nil {
			t.Error(err)
		}
	})
	if err := d.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var groups []TargetGroup
	if err := json.Unmarshal(b, &groups); err != nil || len(groups) != 2 {
		t.Fatalf("file content: %v %s", err, b)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	// Same content: no rewrite.
	before := st.ModTime()
	if err := fw.Write(d.TargetGroups(0)); err != nil {
		t.Fatal(err)
	}
	st, _ = os.Stat(path)
	if !st.ModTime().Equal(before) {
		t.Error("unchanged content was rewritten")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}
