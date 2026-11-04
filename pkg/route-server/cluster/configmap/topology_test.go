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
	"testing"

	"route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/utils"

	"github.com/stretchr/testify/assert"
)

// ===================== ParseTopology =====================

func TestParseTopology_Success(t *testing.T) {
	data := `{
		"version": "1.0",
		"status": "completed",
		"server_group_count": "3",
		"server_group_list": [
			{
				"group_id": "0",
				"group_type": "master",
				"server_count": "1",
				"server_list": [
					{"server_id": "0", "server_ip": "127.0.0.1", "pod_name": "rs-master-0"}
				]
			},
			{
				"group_id": "1",
				"group_type": "prefill",
				"server_count": "1",
				"server_list": [
					{"server_id": "0", "server_ip": "10.94.122.191", "pod_name": "prefill-0"}
				]
			}
		]
	}`
	topo, err := ParseTopology(data)
	assert.NoError(t, err)
	assert.Equal(t, "1.0", topo.Version)
	assert.Equal(t, "completed", topo.Status)
	assert.Len(t, topo.ServerGroupList, 2)
	assert.Equal(t, "master", topo.ServerGroupList[0].GroupType)
	assert.Equal(t, "127.0.0.1", topo.ServerGroupList[0].ServerList[0].ServerIP)
}

func TestParseTopology_Empty(t *testing.T) {
	topo, err := ParseTopology("")
	assert.NoError(t, err)
	assert.NotNil(t, topo)
	assert.Empty(t, topo.ServerGroupList)
}

func TestParseTopology_WhitespaceOnly(t *testing.T) {
	topo, err := ParseTopology("   \n  ")
	assert.NoError(t, err)
	assert.NotNil(t, topo)
	assert.Empty(t, topo.ServerGroupList)
}

func TestParseTopology_InvalidJSON(t *testing.T) {
	_, err := ParseTopology("{not valid json")
	assert.Error(t, err)
}

// ===================== BuildPeersAndInstances =====================

func sampleConfigMapConfig() *config.ConfigMapConfig {
	return &config.ConfigMapConfig{
		ConfigMapName: "cluster-topology",
		TopologyKey:   "rank_table.json",
		FailuresKey:   "failures.json",
		InstanceGroups: []config.InstanceGroupConfig{
			{GroupType: "prefill", StartingPort: 8000, EndpointCount: 2},
			{GroupType: "decode", StartingPort: 9000, EndpointCount: 4},
		},
	}
}

func sampleTopology() *Topology {
	return &Topology{
		ServerGroupList: []ServerGroup{
			{
				GroupType: "master",
				ServerList: []ServerInfo{
					{ServerIP: "127.0.0.1", PodName: "rs-master-0"},
					{ServerIP: "10.94.96.191", PodName: "rs-master-1"},
				},
			},
			{
				GroupType: "prefill",
				ServerList: []ServerInfo{
					{ServerIP: "10.94.122.191", PodName: "dqaservice-prefill-0"},
				},
			},
			{
				GroupType: "decode",
				ServerList: []ServerInfo{
					{ServerIP: "10.94.80.178", PodName: "dqaservice-decode-0"},
				},
			},
			{
				// worker group is not in instance-groups config -> ignored
				GroupType: "worker",
				ServerList: []ServerInfo{
					{ServerIP: "10.94.80.179", PodName: "worker-0"},
				},
			},
		},
	}
}

func TestBuildPeersAndInstances_MasterGroupGeneratesPeers(t *testing.T) {
	peers, instances := BuildPeersAndInstances(sampleTopology(), sampleConfigMapConfig(), 9800, 9810)
	assert.Len(t, peers, 2)
	assert.Contains(t, peers, "master_127_0_0_1_9800")
	assert.Contains(t, peers, "master_10_94_96_191_9800")
	assert.Equal(t, "127.0.0.1", peers["master_127_0_0_1_9800"].Addr.Host)
	assert.Equal(t, 9800, peers["master_127_0_0_1_9800"].Addr.HttpPort)
	assert.Equal(t, 9810, peers["master_127_0_0_1_9800"].Addr.OpsPort)
	assert.Equal(t, 9810, peers["master_10_94_96_191_9800"].Addr.OpsPort)
	for _, inst := range instances {
		assert.NotEqual(t, "master", inst.Role)
	}
}

func TestBuildPeersAndInstances_InstanceGroupsGenerateRoutes(t *testing.T) {
	peers, instances := BuildPeersAndInstances(sampleTopology(), sampleConfigMapConfig(), 9800, 9810)
	_ = peers
	// prefill: 1 server * 2 endpoints = 2 routes, ports 8000,8001
	assert.Contains(t, instances, "prefill_10_94_122_191_8000")
	assert.Contains(t, instances, "prefill_10_94_122_191_8001")
	assert.Equal(t, "10.94.122.191", instances["prefill_10_94_122_191_8000"].Ip)
	assert.Equal(t, 8000, instances["prefill_10_94_122_191_8000"].Port)
	assert.Equal(t, "prefill", instances["prefill_10_94_122_191_8000"].Role)
	assert.Equal(t, "dqaservice-prefill-0", instances["prefill_10_94_122_191_8000"].InstanceGroup)

	// decode: 1 server * 4 endpoints = 4 routes, ports 9000..9003
	assert.Contains(t, instances, "decode_10_94_80_178_9000")
	assert.Contains(t, instances, "decode_10_94_80_178_9001")
	assert.Contains(t, instances, "decode_10_94_80_178_9002")
	assert.Contains(t, instances, "decode_10_94_80_178_9003")

	// worker group is not configured in instance-groups -> no routes
	assert.NotContains(t, instances, "worker_10_94_80_179_8000")

	assert.Len(t, instances, 6)
}

func TestBuildPeersAndInstances_SkipsEmptyIP(t *testing.T) {
	topo := &Topology{
		ServerGroupList: []ServerGroup{
			{
				GroupType: "master",
				ServerList: []ServerInfo{
					{ServerIP: "", PodName: "rs-master-0"},
					{ServerIP: "10.94.96.191", PodName: ""},
					{ServerIP: "10.94.96.192", PodName: "rs-master-2"},
				},
			},
			{
				GroupType: "prefill",
				ServerList: []ServerInfo{
					{ServerIP: "", PodName: "prefill-0"},
					{ServerIP: "10.94.122.191", PodName: "prefill-1"},
				},
			},
		},
	}
	peers, instances := BuildPeersAndInstances(topo, sampleConfigMapConfig(), 9800, 9810)
	assert.Len(t, peers, 2)
	assert.Contains(t, peers, "master_10_94_96_191_9800")
	assert.Contains(t, peers, "master_10_94_96_192_9800")
	assert.Len(t, instances, 2)
	assert.Contains(t, instances, "prefill_10_94_122_191_8000")
}

func TestBuildPeersAndInstances_NilTopology(t *testing.T) {
	peers, instances := BuildPeersAndInstances(nil, sampleConfigMapConfig(), 9800, 9810)
	assert.Empty(t, peers)
	assert.Empty(t, instances)
}

func TestBuildPeersAndInstances_NoInstanceGroupsConfigured(t *testing.T) {
	cfg := &config.ConfigMapConfig{
		ConfigMapName: "cluster-topology",
		TopologyKey:   "rank_table.json",
	}
	peers, instances := BuildPeersAndInstances(sampleTopology(), cfg, 9800, 9810)
	// master peers still generated; non-master groups produce no instances
	assert.Len(t, peers, 2)
	assert.Empty(t, instances)
}

func TestBuildPeersAndInstances_EndpointCountZero(t *testing.T) {
	cfg := sampleConfigMapConfig()
	cfg.InstanceGroups[0].EndpointCount = 0
	peers, instances := BuildPeersAndInstances(sampleTopology(), cfg, 9800, 9810)
	_ = peers
	// prefill has EndpointCount=0 -> no routes; decode still has 4
	assert.NotContains(t, instances, "prefill_10_94_122_191_8000")
	assert.Contains(t, instances, "decode_10_94_80_178_9000")
}

func TestEndpointName(t *testing.T) {
	got := EndpointName("prefill", "10.94.122.191", 8000)
	assert.Equal(t, "prefill_10_94_122_191_8000", got)
}

// ===================== DiffPeers =====================

func TestDiffPeers_AddAndRemove(t *testing.T) {
	old := map[string]*utils.Peer{
		"a": {Name: "a"},
		"b": {Name: "b"},
	}
	new := map[string]*utils.Peer{
		"b": {Name: "b"},
		"c": {Name: "c"},
	}
	added, removed := DiffPeers(old, new)
	assert.Len(t, added, 1)
	assert.Equal(t, "c", added[0].Name)
	assert.Equal(t, []string{"a"}, removed)
}

func TestDiffPeers_NoChange(t *testing.T) {
	old := map[string]*utils.Peer{"a": {Name: "a"}}
	new := map[string]*utils.Peer{"a": {Name: "a"}}
	added, removed := DiffPeers(old, new)
	assert.Empty(t, added)
	assert.Empty(t, removed)
}

func TestDiffPeers_EmptyOld(t *testing.T) {
	old := map[string]*utils.Peer{}
	new := map[string]*utils.Peer{"a": {Name: "a"}, "b": {Name: "b"}}
	added, removed := DiffPeers(old, new)
	assert.Len(t, added, 2)
	assert.Empty(t, removed)
}

// ===================== DiffInstances =====================

func TestDiffInstances_AddAndRemove(t *testing.T) {
	old := map[string]*config.Route{
		"old_route": {Ip: "1.1.1.1", Port: 8000, Role: "prefill", InstanceGroup: "p-0"},
		"keep":      {Ip: "2.2.2.2", Port: 9000, Role: "decode", InstanceGroup: "d-0"},
	}
	new := map[string]*config.Route{
		"keep":      {Ip: "2.2.2.2", Port: 9000, Role: "decode", InstanceGroup: "d-0"},
		"new_route": {Ip: "3.3.3.3", Port: 8001, Role: "prefill", InstanceGroup: "p-1"},
	}
	added, removed := DiffInstances(old, new)
	assert.Len(t, added, 1)
	assert.Contains(t, added, "new_route")
	assert.Equal(t, []string{"old_route"}, removed)
}

func TestDiffInstances_DetectsIPChange(t *testing.T) {
	// same name, different IP -> should be both added (replaced) and removed
	old := map[string]*config.Route{
		"route_a": {Ip: "1.1.1.1", Port: 8000, Role: "prefill", InstanceGroup: "p-0"},
	}
	new := map[string]*config.Route{
		"route_a": {Ip: "9.9.9.9", Port: 8000, Role: "prefill", InstanceGroup: "p-0"},
	}
	added, removed := DiffInstances(old, new)
	assert.Len(t, added, 1)
	assert.Contains(t, added, "route_a")
	assert.Equal(t, []string{"route_a"}, removed)
}

func TestDiffInstances_DetectsPortChange(t *testing.T) {
	old := map[string]*config.Route{
		"route_a": {Ip: "1.1.1.1", Port: 8000, Role: "prefill", InstanceGroup: "p-0"},
	}
	new := map[string]*config.Route{
		"route_a": {Ip: "1.1.1.1", Port: 8001, Role: "prefill", InstanceGroup: "p-0"},
	}
	added, removed := DiffInstances(old, new)
	assert.Len(t, added, 1)
	assert.Equal(t, []string{"route_a"}, removed)
}

func TestDiffInstances_NoChange(t *testing.T) {
	old := map[string]*config.Route{
		"route_a": {Ip: "1.1.1.1", Port: 8000, Role: "prefill", InstanceGroup: "p-0"},
	}
	new := map[string]*config.Route{
		"route_a": {Ip: "1.1.1.1", Port: 8000, Role: "prefill", InstanceGroup: "p-0"},
	}
	added, removed := DiffInstances(old, new)
	assert.Empty(t, added)
	assert.Empty(t, removed)
}

// ===================== ParseIdentity / BuildIdentity =====================

func TestBuildAndParseIdentity(t *testing.T) {
	identity := EndpointName("candidate", "127.0.0.1", 9800)
	assert.Equal(t, "candidate_127_0_0_1_9800", identity)
	host, port, ok := ParseIdentity(identity)
	assert.True(t, ok)
	assert.Equal(t, "127.0.0.1", host)
	assert.Equal(t, 9800, port)
}

func TestParseIdentity_LegacyFormat(t *testing.T) {
	host, port, ok := ParseIdentity("127.0.0.1:9800")
	assert.True(t, ok)
	assert.Equal(t, "127.0.0.1", host)
	assert.Equal(t, 9800, port)
}

func TestParseIdentity_Invalid(t *testing.T) {
	cases := []string{"", "no-port", ":1234", "host:", "host:abc", "host:99999", "just_a_word"}
	for _, c := range cases {
		_, _, ok := ParseIdentity(c)
		assert.False(t, ok, "expected %q to be invalid", c)
	}
}

func TestParseIdentity_PortOutOfRange(t *testing.T) {
	_, _, ok := ParseIdentity("127.0.0.1:70000")
	assert.False(t, ok)
}

// ===================== buildHealthStatsFromFailures =====================

func TestBuildHealthStatsFromFailures_AggregatesByRole(t *testing.T) {
	topo := sampleTopology()
	snapshot := &utils.AllInstanceFailuresSnapshot{
		SnapshotTime: "2026-07-13T10:00:00Z",
		Failures: map[string]*utils.InstanceFailureRecord{
			"prefill_10_94_122_191_8000": {Role: "prefill", Status: constant.FaultStatus},
			"decode_10_94_80_178_9000":   {Role: "decode", Status: constant.FaultStatus},
			"decode_10_94_80_178_9001":   {Role: "decode", Status: constant.FaultStatus},
			"decode_10_94_80_178_9002":   {Role: "decode", Status: constant.RunningStatus},
		},
	}
	cache := buildHealthStatsFromFailures(snapshot, topo, nil)
	// prefill: 1 total - 1 faulty = 0 available
	assert.Equal(t, 0, cache.EveryRoleAvailableCountMap["prefill"])
	// decode: 1 total - 2 faulty = -1 (capped at 0 by caller logic? no — design
	// says total - faulty; we keep raw math so callers can detect inconsistency)
	assert.Equal(t, -1, cache.EveryRoleAvailableCountMap["decode"])
	// failure detail recorded only for non-running instances
	assert.Len(t, cache.FailureInstanceDetail["prefill"], 1)
	assert.Len(t, cache.FailureInstanceDetail["decode"], 2)
	assert.Equal(t, "2026-07-13T10:00:00Z", cache.UpdateTime.Format("2006-01-02T15:04:05Z07:00"))
}

func TestBuildHealthStatsFromFailures_EmptySnapshot(t *testing.T) {
	topo := sampleTopology()
	snapshot := &utils.AllInstanceFailuresSnapshot{}
	cache := buildHealthStatsFromFailures(snapshot, topo, nil)
	assert.Equal(t, 1, cache.EveryRoleAvailableCountMap["prefill"])
	assert.Equal(t, 1, cache.EveryRoleAvailableCountMap["decode"])
	assert.Empty(t, cache.FailureInstanceDetail)
}

func TestBuildHealthStatsFromFailures_NilTopology(t *testing.T) {
	snapshot := &utils.AllInstanceFailuresSnapshot{
		Failures: map[string]*utils.InstanceFailureRecord{
			"x": {Role: "prefill", Status: constant.FaultStatus},
		},
	}
	cache := buildHealthStatsFromFailures(snapshot, nil, nil)
	// no topology -> total by role is empty -> available counts not set
	_, ok := cache.EveryRoleAvailableCountMap["prefill"]
	assert.False(t, ok)
	// but failure detail is still recorded
	assert.Len(t, cache.FailureInstanceDetail["prefill"], 1)
}
