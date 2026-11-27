package discovery

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/noderings/prometheus-noderings-sd/internal/noderings"
)

var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// FuzzVMLabels checks that any tag text from the API yields valid label names
// and a response Prometheus can decode.
func FuzzVMLabels(f *testing.F) {
	f.Add("web", "env:prod", "203.0.113.1")
	f.Add("ünïcödé", "a b/c", "")
	f.Add("", "9lives", "2001:db8::1")
	f.Fuzz(func(t *testing.T, tag1, tag2, ip string) {
		vms := []noderings.VirtualMachine{{ID: "vm", IPv4: ip, IPv6: "2001:db8::2", Tags: []string{tag1, tag2}}}
		groups := BuildVMs(vms, Options{})
		if len(groups) != 1 {
			t.Fatalf("got %d groups", len(groups))
		}
		for k := range groups[0].Labels {
			if !labelName.MatchString(k) || !strings.HasPrefix(k, metaPrefix) {
				t.Fatalf("invalid label name %q", k)
			}
		}
		if _, err := json.Marshal(groups); err != nil {
			t.Fatal(err)
		}
	})
}

// FuzzDNSRecords checks that any record name or value builds targets without
// panicking, and that every target is the record's FQDN.
func FuzzDNSRecords(f *testing.F) {
	f.Add("www", "A", "203.0.113.1", "example.com.")
	f.Add("@", "CNAME", "target.example.net.", "Example.COM")
	f.Add("*.wild", "AAAA", "", "x")
	f.Fuzz(func(t *testing.T, name, typ, value, zone string) {
		zones := []noderings.DNSZone{{ID: "z", Name: zone}}
		recs := map[string][]noderings.DNSRecord{"z": {{Name: name, Type: typ, RData: []string{value}}}}
		for _, g := range BuildDNS(zones, recs, Options{}) {
			if g.Targets[0] != g.Labels[LabelDNSRecordFQDN] {
				t.Fatalf("target %q != fqdn %q", g.Targets[0], g.Labels[LabelDNSRecordFQDN])
			}
		}
	})
}
