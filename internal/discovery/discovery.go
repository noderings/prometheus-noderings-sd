// Package discovery turns NodeRings virtual machines into Prometheus target
// groups, in the format used by HTTP and file-based service discovery.
package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/noderings/prometheus-noderings-sd/internal/noderings"
)

// Meta label names. Every label starts with __meta_noderings_ and is dropped
// by Prometheus after relabeling unless copied to a target label.
const (
	metaPrefix = "__meta_noderings_"

	LabelVMID              = metaPrefix + "vm_id"
	LabelVMHostname        = metaPrefix + "vm_hostname"
	LabelVMStatus          = metaPrefix + "vm_status"
	LabelVMDistro          = metaPrefix + "vm_distro"
	LabelVMCPUs            = metaPrefix + "vm_cpus"
	LabelVMMemoryBytes     = metaPrefix + "vm_memory_bytes"
	LabelVMDiskBytes       = metaPrefix + "vm_disk_bytes"
	LabelPublicIPv4        = metaPrefix + "public_ipv4"
	LabelPublicIPv6        = metaPrefix + "public_ipv6"
	LabelAdditionalIPv4    = metaPrefix + "additional_ipv4"
	LabelRegionID          = metaPrefix + "region_id"
	LabelRegionCountry     = metaPrefix + "region_country"
	LabelRegionCity        = metaPrefix + "region_city"
	LabelPlanID            = metaPrefix + "plan_id"
	LabelPlanCode          = metaPrefix + "plan_code"
	LabelBillingType       = metaPrefix + "billing_type"
	LabelProviderName      = metaPrefix + "provider_name"
	LabelProviderOrgID     = metaPrefix + "provider_organization_id"
	LabelFirewallRulesetID = metaPrefix + "firewall_ruleset_id"
	LabelBackupsEnabled    = metaPrefix + "backups_enabled"
	LabelInstantApp        = metaPrefix + "instant_app"
	LabelTags              = metaPrefix + "tags"
	LabelTagPrefix         = metaPrefix + "tag_"
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

// Lister lists virtual machines.
type Lister interface {
	ListVirtualMachines(ctx context.Context) ([]noderings.VirtualMachine, error)
}

// Options configure how target groups are built.
type Options struct {
	// Port is added to each target address. Zero means 80.
	Port int
	// AddressFamily picks the address used for __address__: AddressIPv4
	// (default) or AddressIPv6. A VM without that address falls back to the other.
	AddressFamily string
	// TagSeparator joins tags and additional IPs. Empty means ",". The joined
	// value also starts and ends with it, so a relabel regex like .*,web,.* matches.
	TagSeparator string
}

// Discoverer keeps the latest list of VMs and builds target groups from it.
type Discoverer struct {
	lister  Lister
	opts    Options
	log     *slog.Logger
	metrics *metrics

	mu        sync.RWMutex
	vms       []noderings.VirtualMachine
	refreshed bool
	onUpdate  []func([]noderings.VirtualMachine)
}

// New returns a Discoverer. reg may be nil.
func New(lister Lister, opts Options, log *slog.Logger, reg prometheus.Registerer) *Discoverer {
	if opts.Port == 0 {
		opts.Port = 80
	}
	if opts.AddressFamily == "" {
		opts.AddressFamily = AddressIPv4
	}
	if opts.TagSeparator == "" {
		opts.TagSeparator = ","
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Discoverer{lister: lister, opts: opts, log: log, metrics: newMetrics(reg)}
}

// OnUpdate registers f to run after every successful refresh.
func (d *Discoverer) OnUpdate(f func([]noderings.VirtualMachine)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onUpdate = append(d.onUpdate, f)
}

// Refresh fetches the VM list once. On error the previous list is kept.
func (d *Discoverer) Refresh(ctx context.Context) error {
	start := time.Now()
	vms, err := d.lister.ListVirtualMachines(ctx)
	d.metrics.duration.Observe(time.Since(start).Seconds())
	if err != nil {
		d.metrics.failures.Inc()
		return err
	}
	sort.Slice(vms, func(i, j int) bool { return vms[i].ID < vms[j].ID })
	d.mu.Lock()
	d.vms = vms
	d.refreshed = true
	hooks := append([]func([]noderings.VirtualMachine){}, d.onUpdate...)
	d.mu.Unlock()
	d.metrics.vms.Set(float64(len(vms)))
	d.metrics.lastSuccess.SetToCurrentTime()
	for _, f := range hooks {
		f(vms)
	}
	return nil
}

// Run refreshes every interval until ctx ends. The first refresh is immediate.
func (d *Discoverer) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := d.Refresh(ctx); err != nil && ctx.Err() == nil {
			d.log.Error("refreshing the NodeRings VM list failed; serving the previous list", "err", err)
		} else if err == nil {
			d.log.Debug("refreshed the NodeRings VM list", "vms", d.VMCount())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Ready reports whether at least one refresh has succeeded.
func (d *Discoverer) Ready() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.refreshed
}

// VMCount returns the number of VMs in the current list.
func (d *Discoverer) VMCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.vms)
}

// TargetGroups builds the target groups from the current list, with the
// configured port, or with port when it is not zero.
func (d *Discoverer) TargetGroups(port int) []TargetGroup {
	d.mu.RLock()
	vms := d.vms
	d.mu.RUnlock()
	opts := d.opts
	if port != 0 {
		opts.Port = port
	}
	return Build(vms, opts)
}

// Build converts VMs into target groups, one per VM with an address. VMs
// without any public address are left out.
func Build(vms []noderings.VirtualMachine, opts Options) []TargetGroup {
	if opts.Port == 0 {
		opts.Port = 80
	}
	if opts.TagSeparator == "" {
		opts.TagSeparator = ","
	}
	groups := make([]TargetGroup, 0, len(vms))
	for i := range vms {
		vm := &vms[i]
		host := pickAddress(vm, opts.AddressFamily)
		if host == "" {
			continue
		}
		groups = append(groups, TargetGroup{
			Targets: []string{net.JoinHostPort(host, strconv.Itoa(opts.Port))},
			Labels:  labels(vm, opts.TagSeparator),
		})
	}
	return groups
}

func pickAddress(vm *noderings.VirtualMachine, family string) string {
	v4, v6 := strings.TrimSpace(vm.IPv4), strings.TrimSpace(vm.IPv6)
	if family == AddressIPv6 {
		if v6 != "" {
			return v6
		}
		return v4
	}
	if v4 != "" {
		return v4
	}
	return v6
}

func labels(vm *noderings.VirtualMachine, sep string) map[string]string {
	l := map[string]string{
		LabelVMID:              vm.ID,
		LabelVMHostname:        vm.Hostname,
		LabelVMStatus:          enumValue(vm.OperationStatus, "OPERATION_STATUS_"),
		LabelVMDistro:          enumValue(vm.Distro, "DISTRO_"),
		LabelVMCPUs:            strconv.FormatInt(int64(vm.CPU), 10),
		LabelVMMemoryBytes:     strconv.FormatInt(int64(vm.Memory)<<20, 10),
		LabelVMDiskBytes:       strconv.FormatInt(int64(vm.Storage)<<20, 10),
		LabelPublicIPv4:        vm.IPv4,
		LabelPublicIPv6:        vm.IPv6,
		LabelRegionID:          vm.RegionID,
		LabelRegionCountry:     vm.RegionCountry,
		LabelRegionCity:        vm.RegionCity,
		LabelPlanID:            vm.PlanID,
		LabelPlanCode:          vm.PlanCode,
		LabelBillingType:       enumValue(vm.PlanBillingType, "BILLING_TYPE_"),
		LabelProviderName:      vm.ProviderName,
		LabelProviderOrgID:     vm.ProviderOrganizationID,
		LabelFirewallRulesetID: vm.FirewallRulesetID,
		LabelBackupsEnabled:    strconv.FormatBool(vm.HasActiveBackupSubscription),
		LabelInstantApp:        vm.InstantAppSlug,
	}
	ips := make([]string, 0, len(vm.AdditionalIPv4))
	for _, ip := range vm.AdditionalIPv4 {
		if ip.IPAddress != "" {
			ips = append(ips, ip.IPAddress)
		}
	}
	l[LabelAdditionalIPv4] = joinList(ips, sep)
	tags := make([]string, 0, len(vm.Tags))
	for _, t := range vm.Tags {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
			l[LabelTagPrefix+SanitizeLabelName(t)] = "true"
		}
	}
	sort.Strings(tags)
	l[LabelTags] = joinList(tags, sep)
	return l
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
