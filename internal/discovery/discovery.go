// Package discovery turns NodeRings resources (virtual machines, game servers
// and DNS records) into Prometheus target groups, in the format used by HTTP
// and file-based service discovery.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/noderings/prometheus-noderings-sd/internal/noderings"
)

// Roles are the kinds of NodeRings resources that can be discovered, the way
// Prometheus' built-in discoveries use a role (for example hetzner_sd_config).
const (
	RoleVM         = "vm"
	RoleGameServer = "gameserver"
	RoleDNS        = "dns"
)

// AllRoles lists every role in display order.
var AllRoles = []string{RoleVM, RoleGameServer, RoleDNS}

const metaPrefix = "__meta_noderings_"

// Labels shared by roles whose resources run in a region on a plan.
const (
	LabelRegionID      = metaPrefix + "region_id"
	LabelRegionCountry = metaPrefix + "region_country"
	LabelRegionCity    = metaPrefix + "region_city"
	LabelPlanID        = metaPrefix + "plan_id"
	LabelPlanCode      = metaPrefix + "plan_code"
	LabelBillingType   = metaPrefix + "billing_type"
	LabelProviderName  = metaPrefix + "provider_name"
)

// Address families a target address can be built from.
const (
	AddressIPv4 = "ipv4"
	AddressIPv6 = "ipv6"
)

// TargetGroup is one entry of an HTTP or file SD response.
type TargetGroup struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

// Lister reads NodeRings resources. *noderings.Client implements it.
type Lister interface {
	ListVirtualMachines(ctx context.Context) ([]noderings.VirtualMachine, error)
	ListGameServers(ctx context.Context) ([]noderings.GameServer, error)
	ListDNSZones(ctx context.Context) ([]noderings.DNSZone, error)
	ListDNSRecords(ctx context.Context, zoneID string) ([]noderings.DNSRecord, error)
}

// Options configure how target groups are built.
type Options struct {
	// Port is added to each VM target (zero means 80). For game servers it
	// replaces their own port and for DNS host names it is appended, but only
	// when a request passes ?port=.
	Port int
	// AddressFamily picks the VM address used for __address__: AddressIPv4
	// (default) or AddressIPv6. A VM without that address falls back to the other.
	AddressFamily string
	// TagSeparator joins list values. Empty means ",". The joined value also
	// starts and ends with it, so a relabel regex like .*,web,.* matches.
	TagSeparator string
	// DNSRecordTypes are the record types that become DNS targets. Empty
	// means A, AAAA and CNAME.
	DNSRecordTypes []string
}

func (o Options) withDefaults() Options {
	if o.AddressFamily == "" {
		o.AddressFamily = AddressIPv4
	}
	if o.TagSeparator == "" {
		o.TagSeparator = ","
	}
	if len(o.DNSRecordTypes) == 0 {
		o.DNSRecordTypes = []string{"A", "AAAA", "CNAME"}
	}
	return o
}

// snapshot is the last successful fetch of one role.
type snapshot struct {
	ready bool
	data  any // []noderings.VirtualMachine, []noderings.GameServer or []dnsZoneRecords
	count int
}

// Discoverer keeps the latest resources of each enabled role and builds
// target groups from them.
type Discoverer struct {
	lister  Lister
	roles   []string
	opts    Options
	log     *slog.Logger
	metrics *metrics

	mu       sync.RWMutex
	snaps    map[string]snapshot
	onUpdate []func(role string)
}

// New returns a Discoverer for the given roles (RoleVM when empty). reg may be nil.
func New(lister Lister, roles []string, opts Options, log *slog.Logger, reg prometheus.Registerer) (*Discoverer, error) {
	if len(roles) == 0 {
		roles = []string{RoleVM}
	}
	seen := map[string]bool{}
	var clean []string
	for _, r := range roles {
		r = strings.TrimSpace(strings.ToLower(r))
		if !isRole(r) {
			return nil, fmt.Errorf("unknown role %q: use %s", r, strings.Join(AllRoles, ", "))
		}
		if !seen[r] {
			seen[r] = true
			clean = append(clean, r)
		}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	m := newMetrics(reg)
	m.init(clean)
	return &Discoverer{
		lister: lister, roles: clean, opts: opts.withDefaults(), log: log,
		metrics: m, snaps: map[string]snapshot{},
	}, nil
}

func isRole(r string) bool {
	for _, x := range AllRoles {
		if r == x {
			return true
		}
	}
	return false
}

// Roles returns the enabled roles.
func (d *Discoverer) Roles() []string { return append([]string(nil), d.roles...) }

// Enabled reports whether role is enabled.
func (d *Discoverer) Enabled(role string) bool {
	for _, r := range d.roles {
		if r == role {
			return true
		}
	}
	return false
}

// OnUpdate registers f to run after every successful refresh of a role.
func (d *Discoverer) OnUpdate(f func(role string)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onUpdate = append(d.onUpdate, f)
}

// Refresh fetches every enabled role once. A role whose fetch fails keeps its
// previous data. The returned error joins the failures.
func (d *Discoverer) Refresh(ctx context.Context) error {
	var errs []error
	for _, role := range d.roles {
		if err := d.refreshRole(ctx, role); err != nil {
			errs = append(errs, fmt.Errorf("role %s: %w", role, err))
		}
	}
	return errors.Join(errs...)
}

func (d *Discoverer) refreshRole(ctx context.Context, role string) error {
	start := time.Now()
	var (
		data  any
		count int
		err   error
	)
	switch role {
	case RoleVM:
		var vms []noderings.VirtualMachine
		vms, err = d.lister.ListVirtualMachines(ctx)
		sort.Slice(vms, func(i, j int) bool { return vms[i].ID < vms[j].ID })
		data, count = vms, len(vms)
	case RoleGameServer:
		var gs []noderings.GameServer
		gs, err = d.lister.ListGameServers(ctx)
		sort.Slice(gs, func(i, j int) bool { return gs[i].ID < gs[j].ID })
		data, count = gs, len(gs)
	case RoleDNS:
		var zones []dnsZoneRecords
		zones, err = d.fetchDNS(ctx)
		data = zones
		for _, z := range zones {
			count += len(z.records)
		}
	}
	d.metrics.duration.WithLabelValues(role).Observe(time.Since(start).Seconds())
	if err != nil {
		d.metrics.failures.WithLabelValues(role).Inc()
		return err
	}
	d.mu.Lock()
	d.snaps[role] = snapshot{ready: true, data: data, count: count}
	hooks := append([]func(string){}, d.onUpdate...)
	d.mu.Unlock()
	d.metrics.resources.WithLabelValues(role).Set(float64(count))
	d.metrics.lastSuccess.WithLabelValues(role).SetToCurrentTime()
	for _, f := range hooks {
		f(role)
	}
	return nil
}

// Run refreshes every interval until ctx ends. The first refresh is immediate.
func (d *Discoverer) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := d.Refresh(ctx); err != nil && ctx.Err() == nil {
			d.log.Error("refreshing from the NodeRings API failed; serving the previous lists", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Ready reports whether role has been fetched successfully at least once.
func (d *Discoverer) Ready(role string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.snaps[role].ready
}

// AllReady reports whether every enabled role is ready.
func (d *Discoverer) AllReady() bool {
	for _, r := range d.roles {
		if !d.Ready(r) {
			return false
		}
	}
	return true
}

// Count returns the number of resources in role's current data.
func (d *Discoverer) Count(role string) int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.snaps[role].count
}

// TargetGroups builds the target groups of role from its current data, with
// port overriding the configured port when it is not zero.
func (d *Discoverer) TargetGroups(role string, port int) []TargetGroup {
	d.mu.RLock()
	snap := d.snaps[role]
	d.mu.RUnlock()
	opts := d.opts
	switch data := snap.data.(type) {
	case []noderings.VirtualMachine:
		if port != 0 {
			opts.Port = port
		}
		return BuildVMs(data, opts)
	case []noderings.GameServer:
		// --target.port is for VMs; game servers keep their own port unless
		// the request asks for another one.
		opts.Port = port
		return BuildGameServers(data, opts)
	case []dnsZoneRecords:
		opts.Port = port
		return buildDNS(data, opts)
	}
	return []TargetGroup{}
}

// joinList joins values with sep and surrounds the result with sep, the way
// Prometheus' own service discoveries expose lists. An empty list is "".
func joinList(values []string, sep string) string {
	if len(values) == 0 {
		return ""
	}
	return sep + strings.Join(values, sep) + sep
}

// enumValue turns an API enum such as OPERATION_STATUS_STARTED into "started".
// Unspecified values become "".
func enumValue(v, prefix string) string {
	v = strings.ToLower(strings.TrimPrefix(v, prefix))
	if v == "unspecified" {
		return ""
	}
	return v
}

// SanitizeLabelName replaces every character that is not allowed in a label
// name with an underscore. The result always follows a prefix, so a leading
// digit is fine.
func SanitizeLabelName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ParsePort validates a TCP port given as text.
func ParsePort(s string) (int, error) {
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("port must be a number from 1 to 65535, got %q", s)
	}
	return p, nil
}
