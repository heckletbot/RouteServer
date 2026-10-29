/*
 * Copyright 2026 The vLLM-Cloud Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package configmap

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/utils"

	corev1 "k8s.io/api/core/v1"
)

// Topology corresponds to the rank_table.json structure stored in the ConfigMap topology key.
type Topology struct {
	Version          string        `json:"version"`
	Status           string        `json:"status"`
	ServerGroupCount string        `json:"server_group_count"`
	ServerGroupList  []ServerGroup `json:"server_group_list"`
}

type ServerGroup struct {
	GroupID     string       `json:"group_id"`
	GroupType   string       `json:"group_type"`
	ServerCount string       `json:"server_count"`
	ServerList  []ServerInfo `json:"server_list"`
}

type ServerInfo struct {
	ServerID string   `json:"server_id"`
	ServerIP string   `json:"server_ip"`
	PodName  string   `json:"pod_name"`
	Device   []Device `json:"device"`
}

type Device struct {
	DeviceID        string `json:"device_id"`
	DeviceIP        string `json:"device_ip"`
	DeviceLogicalID string `json:"device_logical_id"`
	RankID          string `json:"rank_id"`
}

// Defaults for configurable fields. Used when the corresponding ConfigMapConfig
// field is empty. See design §5.3 / §6.6.4.
const (
	// defaultRouterGroupType is the topology group_type value that identifies
	// RouteServer (Router) peers. Servers in this group become Peers, not
	// Instances. "master" matches the rank_table.json convention.
	defaultRouterGroupType = "master"
	// defaultInferGroupRoleTypeLabelKey is the Pod label key holding the
	// group_type (e.g. master/prefill/decode) for Pod List reconciliation.
	defaultInferGroupRoleTypeLabelKey = "apps.red.io/podgroup-role-type"
	// defaultInferGroupLabelKey is the Pod label key holding the infer-group
	// name — the instance-isolation boundary for Pod List reconciliation.
	defaultInferGroupLabelKey = "apps.red.io/podgroup-name"
)

// effectiveRouterGroupType returns the configured RouterGroupType or the
// default when empty. Shared by the ConfigMap Watch path (BuildPeersAndInstances)
// and the health-stats aggregation.
func effectiveRouterGroupType(cfg *config.ConfigMapConfig) string {
	if cfg != nil && cfg.RouterGroupType != "" {
		return cfg.RouterGroupType
	}
	return defaultRouterGroupType
}

// effectiveInferGroupRoleTypeLabelKey returns the configured label key or the
// default when empty. Used only by the Pod List reconciliation path.
func effectiveInferGroupRoleTypeLabelKey(cfg *config.ConfigMapConfig) string {
	if cfg != nil && cfg.InferGroupRoleTypeLabelKey != "" {
		return cfg.InferGroupRoleTypeLabelKey
	}
	return defaultInferGroupRoleTypeLabelKey
}

// effectiveInferGroupLabelKey returns the configured label key or the
// default when empty. Used only by the Pod List reconciliation path.
func effectiveInferGroupLabelKey(cfg *config.ConfigMapConfig) string {
	if cfg != nil && cfg.InferGroupLabelKey != "" {
		return cfg.InferGroupLabelKey
	}
	return defaultInferGroupLabelKey
}

// ParseTopology decodes the topology JSON payload stored in the ConfigMap
// topology key. An empty payload yields an empty topology (no error).
func ParseTopology(data string) (*Topology, error) {
	if strings.TrimSpace(data) == "" {
		return &Topology{}, nil
	}
	var topo Topology
	if err := json.Unmarshal([]byte(data), &topo); err != nil {
		return nil, fmt.Errorf("failed to unmarshal topology json: %v", err)
	}
	return &topo, nil
}

// BuildTopologyFromPods reconstructs a Topology from a Pod List result. It is
// the Pod-List counterpart of ParseTopology: instead of decoding a JSON
// payload, it groups Running Pods by the role-type label (roleTypeLabelKey)
// and fills server_ip from status.podIP and pod_name from metadata.name.
//
// The returned Topology is consumable by BuildPeersAndInstances, so the Pod
// List reconciliation path reuses the same idempotent entry
// (applyTopology) as the ConfigMap Watch path. See design §6.6.
//
// Filtering rules (design §6.6.4):
//   - status.phase must be "Running"
//   - status.podIP must be non-empty
//   - the role-type label (roleTypeLabelKey) must be present and non-empty
//
// The labelSelector on the podgroup-name label is applied server-side by the
// List call, so this function does NOT re-check it — every Pod passed in is
// already part of the configured infer-group.
func BuildTopologyFromPods(pods []*corev1.Pod, roleTypeLabelKey string) *Topology {
	// Group servers by group_type. Use a map for grouping, then emit a stable
	// ordering (sorted by group_type) so the resulting Topology is deterministic
	// and diffing against a previously-built Pod-derived topology yields no
	// spurious changes on identical input.
	groups := make(map[string][]ServerInfo)
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		podIP := pod.Status.PodIP
		if podIP == "" {
			continue
		}
		groupType, ok := pod.Labels[roleTypeLabelKey]
		if !ok || groupType == "" {
			continue
		}
		groups[groupType] = append(groups[groupType], ServerInfo{
			ServerIP: podIP,
			PodName:  pod.Name,
		})
	}

	// Deterministic ordering: sort group_types, then servers within each group
	// by PodName so repeated List calls with the same Pod set produce identical
	// Topology (required for idempotent diffing).
	groupTypes := make([]string, 0, len(groups))
	for gt := range groups {
		groupTypes = append(groupTypes, gt)
	}
	sort.Strings(groupTypes)

	serverGroupList := make([]ServerGroup, 0, len(groupTypes))
	for _, gt := range groupTypes {
		servers := groups[gt]
		sort.Slice(servers, func(i, j int) bool {
			return servers[i].PodName < servers[j].PodName
		})
		serverGroupList = append(serverGroupList, ServerGroup{
			GroupType:   gt,
			ServerList:  servers,
			ServerCount: fmt.Sprintf("%d", len(servers)),
		})
	}
	return &Topology{
		Status:           "completed",
		ServerGroupCount: fmt.Sprintf("%d", len(serverGroupList)),
		ServerGroupList:  serverGroupList,
	}
}

// BuildPeersAndInstances derives Peer list and Instance routes from the parsed
// topology according to the design rules:
//
//   - router group (cfg.RouterGroupType, default "master") ->
//     one utils.Peer per server, with Name set to <pod_name>, Host set to <server_ip>,
//     and HttpPort set to <localHttpPort>. No Instance routes are generated for the router group.
//   - other groups -> look up InstanceGroupConfig by group_type. Each server
//     generates EndpointCount routes starting from StartingPort. Groups whose
//     type is not configured in instance-groups are ignored.
//
// localHttpPort is the RouteServer's own http port, used as the Peer port for
// router group servers (peers are other RouteServers, which share the same
// http port within a deployment).
func BuildPeersAndInstances(topo *Topology, cfg *config.ConfigMapConfig, localHttpPort int) (map[string]*utils.Peer, map[string]*config.Route) {
	peers := make(map[string]*utils.Peer)
	instances := make(map[string]*config.Route)
	if topo == nil {
		return peers, instances
	}

	routerGroupType := effectiveRouterGroupType(cfg)
	instanceCfgByType := make(map[string]config.InstanceGroupConfig, len(cfg.InstanceGroups))
	for _, ig := range cfg.InstanceGroups {
		instanceCfgByType[ig.GroupType] = ig
	}

	for _, group := range topo.ServerGroupList {
		if group.GroupType == routerGroupType {
			for _, server := range group.ServerList {
				if server.ServerIP == "" {
					continue
				}
				peerName := EndpointName(routerGroupType, server.ServerIP, localHttpPort)
				peers[peerName] = &utils.Peer{
					Name: peerName,
					Addr: utils.PeerAddr{
						Host:     server.ServerIP,
						HttpPort: localHttpPort,
					},
					IsCandidate: true,
				}
			}
			continue
		}

		ig, ok := instanceCfgByType[group.GroupType]
		if !ok {
			continue
		}
		if ig.EndpointCount <= 0 {
			continue
		}
		for _, server := range group.ServerList {
			if server.ServerIP == "" {
				continue
			}
			for i := 0; i < ig.EndpointCount; i++ {
				port := ig.StartingPort + i
				name := EndpointName(group.GroupType, server.ServerIP, port)
				instances[name] = &config.Route{
					Ip:            server.ServerIP,
					Port:          port,
					Role:          group.GroupType,
					InstanceGroup: server.PodName,
				}
			}
		}
	}

	return peers, instances
}

// EndpointName builds the name for an instance endpoint or a RouteServer peer.
// e.g. <role>_<ip_underscored>_<port>, like prefill_192_0_2_1_8000 for instance, candidate_192_0_2_1_9800 for peer
func EndpointName(role, ip string, port int) string {
	ipPart := strings.ReplaceAll(ip, ".", "_")
	return fmt.Sprintf("%s_%s_%d", role, ipPart, port)
}

// ParseEndpointName extracts host and httpPort from an EndpointName string.
// Returns ("", 0, false) if the format is invalid.
func ParseEndpointName(name string) (string, int, bool) {
	lastUnderscore := strings.LastIndex(name, "_")
	if lastUnderscore < 0 {
		return "", 0, false
	}
	portStr := name[lastUnderscore+1:]
	port, err := parsePort(portStr)
	if err != nil {
		return "", 0, false
	}
	ipPart := name[:lastUnderscore]
	// Find the role_ip boundary: the first '_' after which the IP part starts.
	// IP part has '_' where '.' was, so we need to split role from ip.
	// Walk from the beginning: role is everything before the first ip octet.
	// Since role cannot start with a digit and ip octets are digits,
	// scan forward to find the first segment that looks like an ip octet.
	parts := strings.SplitN(ipPart, "_", -1)
	// Find the boundary: role consumes parts from the start until we hit a
	// segment that is a valid ip octet (0-255) and followed by enough octets.
	ipStart := -1
	for i := 0; i < len(parts); i++ {
		if isIPoctet(parts[i]) && i+3 < len(parts) {
			// Check if remaining parts form a valid IP (4 octets)
			if isIPoctet(parts[i]) && isIPoctet(parts[i+1]) && isIPoctet(parts[i+2]) && isIPoctet(parts[i+3]) && i+4 == len(parts) {
				ipStart = i
				break
			}
		}
	}
	if ipStart < 0 {
		return "", 0, false
	}
	ip := strings.Join(parts[ipStart:], ".")
	return ip, port, true
}

func isIPoctet(s string) bool {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n < 0 || n > 255 {
		return false
	}
	return true
}

// DiffPeers computes added and removed peer names between old and new peer sets.
func DiffPeers(oldPeers, newPeers map[string]*utils.Peer) (added []*utils.Peer, removed []string) {
	for name, peer := range newPeers {
		if _, exists := oldPeers[name]; !exists {
			added = append(added, peer)
		}
	}
	for name := range oldPeers {
		if _, exists := newPeers[name]; !exists {
			removed = append(removed, name)
		}
	}
	return added, removed
}

// DiffInstances computes added and removed instance routes between old and new
// instance sets. A route is considered changed (remove+add) if its IP, port,
// role, or instance-group differs.
func DiffInstances(oldInst, newInst map[string]*config.Route) (added map[string]*config.Route, removed []string) {
	added = make(map[string]*config.Route)
	for name, route := range newInst {
		oldRoute, exists := oldInst[name]
		if !exists || !routesEqual(oldRoute, route) {
			added[name] = route
		}
	}
	for name, oldRoute := range oldInst {
		newRoute, exists := newInst[name]
		if !exists || !routesEqual(oldRoute, newRoute) {
			removed = append(removed, name)
		}
	}
	return added, removed
}

func routesEqual(a, b *config.Route) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Ip == b.Ip && a.Port == b.Port && a.Role == b.Role && a.InstanceGroup == b.InstanceGroup
}

// ParseIdentity splits a Leader Identity string into host and port.
// Supports two formats:
//   - New format (EndpointName): "candidate_192_0_2_1_9800" -> ("192.0.2.1", 9800, true)
//   - Legacy format (IP:Port): "192.0.2.1:9800" -> ("192.0.2.1", 9800, true)
//
// Returns false if the format is invalid. IPv6 is not handled (per design §9.1).
func ParseIdentity(identity string) (host string, port int, ok bool) {
	if identity == "" {
		return "", 0, false
	}
	// Try EndpointName format first (contains '_' and no ':')
	if !strings.Contains(identity, ":") {
		h, p, parsed := ParseEndpointName(identity)
		if parsed {
			return h, p, true
		}
		return "", 0, false
	}
	// Legacy "IP:Port" format
	idx := strings.LastIndex(identity, ":")
	if idx <= 0 || idx == len(identity)-1 {
		return "", 0, false
	}
	host = identity[:idx]
	portStr := identity[idx+1:]
	port, err := parsePort(portStr)
	if err != nil || host == "" {
		return "", 0, false
	}
	return host, port, true
}

func parsePort(s string) (int, error) {
	var port int
	if _, err := fmt.Sscanf(s, "%d", &port); err != nil {
		return 0, err
	}
	if port < 0 || port > 65535 {
		return 0, fmt.Errorf("port out of range: %d", port)
	}
	return port, nil
}
