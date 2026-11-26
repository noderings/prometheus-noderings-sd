package discovery

import (
	"net"
	"strconv"
	"strings"

	"github.com/noderings/prometheus-noderings-sd/internal/noderings"
)

// Labels of the gameserver role.
const (
	LabelGameServerID         = metaPrefix + "gameserver_id"
	LabelGameServerName       = metaPrefix + "gameserver_name"
	LabelGameServerGame       = metaPrefix + "gameserver_game"
	LabelGameServerVariant    = metaPrefix + "gameserver_variant"
	LabelGameServerVersion    = metaPrefix + "gameserver_version"
	LabelGameServerPhase      = metaPrefix + "gameserver_phase"
	LabelGameServerPowerState = metaPrefix + "gameserver_power_state"
	LabelGameServerIP         = metaPrefix + "gameserver_ip"
	LabelGameServerPort       = metaPrefix + "gameserver_port"
	LabelGameServerNodeID     = metaPrefix + "gameserver_node_id"
)

// BuildGameServers converts game servers into target groups. The target is
// the game's own address and port (for blackbox_exporter probes), unless
// opts.Port is set. Game servers without an address yet are left out.
func BuildGameServers(servers []noderings.GameServer, opts Options) []TargetGroup {
	groups := make([]TargetGroup, 0, len(servers))
	for i := range servers {
		gs := &servers[i]
		ip := strings.TrimSpace(gs.AllocationIP)
		port := int(gs.AllocationPort)
		if opts.Port != 0 {
			port = opts.Port
		}
		if ip == "" || port <= 0 {
			continue
		}
		groups = append(groups, TargetGroup{
			Targets: []string{net.JoinHostPort(ip, strconv.Itoa(port))},
			Labels: map[string]string{
				LabelGameServerID:         gs.ID,
				LabelGameServerName:       gs.Name,
				LabelGameServerGame:       gs.GameName,
				LabelGameServerVariant:    gs.Variant,
				LabelGameServerVersion:    gs.VersionValue,
				LabelGameServerPhase:      enumValue(gs.Phase, "GAME_SERVER_PHASE_"),
				LabelGameServerPowerState: strings.ToLower(strings.TrimPrefix(gs.PowerState, "POWER_STATE_")),
				LabelGameServerIP:         ip,
				LabelGameServerPort:       strconv.FormatInt(int64(gs.AllocationPort), 10),
				LabelGameServerNodeID:     gs.NodeID,
				LabelRegionID:             gs.RegionID,
				LabelRegionCountry:        gs.RegionCountry,
				LabelRegionCity:           gs.RegionCity,
				LabelPlanID:               gs.PlanID,
				LabelPlanCode:             gs.PlanCode,
				LabelBillingType:          enumValue(gs.PlanBillingType, "BILLING_TYPE_"),
				LabelProviderName:         gs.ProviderName,
			},
		})
	}
	return groups
}
