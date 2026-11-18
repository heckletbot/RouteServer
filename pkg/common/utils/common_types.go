/*
 * Copyright 2026 The RouteServer Authors.
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

package utils

import (
	"fmt"
	"strings"
)

type AllInstanceFailuresSnapshot struct {
	LeaderId     string                            `json:"id"`
	LocalRound   int32                             `json:"local_round"`
	SnapshotTime string                            `json:"snapshot_time"`
	Failures     map[string]*InstanceFailureRecord `json:"failures"`
}

type InstanceFailureRecord struct {
	Role                           string `json:"role"`
	Status                         string `json:"status"`
	Reason                         string `json:"reason"`
	ConsecutiveFailureTime         int32  `json:"consecutive_failures"`
	ConsecutiveUnreachableTime     int32  `json:"consecutive_unreachable"`
	HealthCheckCountAfterUnhealthy int32  `json:"health_check_count"`
}

func (aif *AllInstanceFailuresSnapshot) String() string {
	if failuresBytes, err := SonicApi.Marshal(aif); err != nil {
		return "failed to marshal"
	} else {
		return string(failuresBytes)
	}
}

type PeerAddr struct {
	Host     string `json:"host"`
	HttpPort int    `json:"service_port"`
	OpsPort  int    `json:"ops_port"`
	Domain   string `json:"domain"`
}

type Peer struct {
	Name        string   `json:"node_name"`
	Addr        PeerAddr `json:"peer_addr"`
	IsCandidate bool     `json:"is_candidate"`
}

func (p *Peer) String() string {
	return fmt.Sprintf("{Name:%s, Host: %s, HttpPort: %v, OpsPort: %v, roles: [%s] }",
		p.Name, p.Addr.Host, p.Addr.HttpPort, p.Addr.OpsPort, nodeRoles(p))
}

func nodeRoles(peer *Peer) string {
	builder := strings.Builder{}
	if peer.IsCandidate {
		builder.WriteString("candidate,")
	}
	return builder.String()
}
