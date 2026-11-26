package discovery

import (
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/noderings/prometheus-noderings-sd/internal/noderings"
)

// Labels of the vm role.
const (
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
	LabelProviderOrgID     = metaPrefix + "provider_organization_id"
	LabelFirewallRulesetID = metaPrefix + "firewall_ruleset_id"
	LabelBackupsEnabled    = metaPrefix + "backups_enabled"
	LabelInstantApp        = metaPrefix + "instant_app"
	LabelTags              = metaPrefix + "tags"
	LabelTagPrefix         = metaPrefix + "tag_"
)

// BuildVMs converts VMs into target groups, one per VM with an address.
// VMs without any public address are left out.
func BuildVMs(vms []noderings.VirtualMachine, opts Options) []TargetGroup {
	opts = opts.withDefaults()
	if opts.Port == 0 {
		opts.Port = 80
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
			Labels:  vmLabels(vm, opts.TagSeparator),
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

func vmLabels(vm *noderings.VirtualMachine, sep string) map[string]string {
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
