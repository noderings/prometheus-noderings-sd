package discovery

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/noderings/prometheus-noderings-sd/internal/noderings"
)

// Labels of the dns role.
const (
	LabelDNSZoneID        = metaPrefix + "dns_zone_id"
	LabelDNSZoneName      = metaPrefix + "dns_zone_name"
	LabelDNSZoneState     = metaPrefix + "dns_zone_state"
	LabelDNSZoneDelegated = metaPrefix + "dns_zone_delegated"
	LabelDNSRecordName    = metaPrefix + "dns_record_name"
	LabelDNSRecordFQDN    = metaPrefix + "dns_record_fqdn"
	LabelDNSRecordTypes   = metaPrefix + "dns_record_types"
	LabelDNSRecordValues  = metaPrefix + "dns_record_values"
	LabelDNSRecordTTL     = metaPrefix + "dns_record_ttl"
)

type dnsZoneRecords struct {
	zone    noderings.DNSZone
	records []noderings.DNSRecord
}

func (d *Discoverer) fetchDNS(ctx context.Context) ([]dnsZoneRecords, error) {
	zones, err := d.lister.ListDNSZones(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(zones, func(i, j int) bool { return zones[i].Name < zones[j].Name })
	out := make([]dnsZoneRecords, 0, len(zones))
	for _, z := range zones {
		recs, err := d.lister.ListDNSRecords(ctx, z.ID)
		if err != nil {
			return nil, fmt.Errorf("zone %s: %w", z.Name, err)
		}
		out = append(out, dnsZoneRecords{zone: z, records: recs})
	}
	return out, nil
}

// BuildDNS converts zones and their records into target groups: one per host
// name that has a record of one of opts.DNSRecordTypes (A, AAAA and CNAME by
// default). The target is the host name, with opts.Port when it is set, ready
// for blackbox_exporter. System and wildcard records are left out.
func BuildDNS(zones []noderings.DNSZone, records map[string][]noderings.DNSRecord, opts Options) []TargetGroup {
	in := make([]dnsZoneRecords, 0, len(zones))
	for _, z := range zones {
		in = append(in, dnsZoneRecords{zone: z, records: records[z.ID]})
	}
	return buildDNS(in, opts)
}

func buildDNS(zones []dnsZoneRecords, opts Options) []TargetGroup {
	opts = opts.withDefaults()
	wanted := map[string]bool{}
	for _, t := range opts.DNSRecordTypes {
		wanted[strings.ToUpper(strings.TrimSpace(t))] = true
	}
	var groups []TargetGroup
	for _, zr := range zones {
		zoneName := strings.TrimSuffix(strings.ToLower(zr.zone.Name), ".")
		type host struct {
			name   string
			types  []string
			values []string
			ttl    int64
		}
		hosts := map[string]*host{}
		for _, r := range zr.records {
			typ := strings.ToUpper(r.Type)
			if r.System || !wanted[typ] || strings.Contains(r.Name, "*") {
				continue
			}
			name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(r.Name)), ".")
			if name == "" {
				name = "@"
			}
			h := hosts[name]
			if h == nil {
				h = &host{name: name, ttl: int64(r.TTL)}
				hosts[name] = h
			}
			h.types = append(h.types, typ)
			for _, v := range r.RData {
				h.values = append(h.values, strings.TrimSuffix(strings.TrimSpace(v), "."))
			}
			if int64(r.TTL) < h.ttl {
				h.ttl = int64(r.TTL)
			}
		}
		names := make([]string, 0, len(hosts))
		for n := range hosts {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			h := hosts[n]
			fqdn := zoneName
			if n != "@" {
				fqdn = n + "." + zoneName
			}
			target := fqdn
			if opts.Port != 0 {
				target = net.JoinHostPort(fqdn, strconv.Itoa(opts.Port))
			}
			sort.Strings(h.types)
			sort.Strings(h.values)
			groups = append(groups, TargetGroup{
				Targets: []string{target},
				Labels: map[string]string{
					LabelDNSZoneID:        zr.zone.ID,
					LabelDNSZoneName:      zoneName,
					LabelDNSZoneState:     enumValue(zr.zone.State, "DNS_ZONE_STATE_"),
					LabelDNSZoneDelegated: strconv.FormatBool(zr.zone.DelegationOK),
					LabelDNSRecordName:    h.name,
					LabelDNSRecordFQDN:    fqdn,
					LabelDNSRecordTypes:   joinList(h.types, opts.TagSeparator),
					LabelDNSRecordValues:  joinList(h.values, opts.TagSeparator),
					LabelDNSRecordTTL:     strconv.FormatInt(h.ttl, 10),
				},
			})
		}
	}
	if groups == nil {
		groups = []TargetGroup{}
	}
	return groups
}
