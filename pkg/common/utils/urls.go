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
	"net"
	"strings"

	"route-server/pkg/common/logger"
)

const (
	// internal path
	InternalAbortPath          = "/internal/abort"
	InternalRunningDetailsPath = "/internal/running_details"
	InternalMetricsPath        = "/internal/metrics"
	ScheduleRequestPath        = "schedule"
	RequestEventPath           = "request_event"

	// cluster path
	AdminGroupPath      = "/cluster"
	JoinHttpPath        = AdminGroupPath + "/peer/join"
	LeaveHttpPath       = AdminGroupPath + "/peer/leave"
	JoinInstancePath    = AdminGroupPath + "/instance/join"
	LeaveInstancePath   = AdminGroupPath + "/instance/leave"
	AbortRequestPath    = AdminGroupPath + "/abort"
	LocalState          = AdminGroupPath + "/local/state"
	Leader              = AdminGroupPath + "/leader"
	ConsistentStatsPath = AdminGroupPath + "/consistent_status"
	RunningDetailsPath  = AdminGroupPath + "/running_details"

	// external path
	MetricsPath       = "/metrics"
	HealthPath        = "/health"
	StatusPath        = "/status"
	LinkBlackListPath = "/link_black_instance"
	ReportLoadPath    = "/scheduler/load"
)

var LeaderOperationSet = map[string]interface{}{
	JoinHttpPath:      nil,
	LeaveHttpPath:     nil,
	JoinInstancePath:  nil,
	LeaveInstancePath: nil,
	MetricsPath:       nil,
	StatusPath:        nil,
	HealthPath:        nil,
	LinkBlackListPath: nil,
	ReportLoadPath:    nil,
}

var OpsUrls = map[string]interface{}{
	unifyUri(InternalAbortPath):          nil,
	unifyUri(InternalRunningDetailsPath): nil,

	unifyUri(ScheduleRequestPath): nil,
	unifyUri(RequestEventPath):    nil,

	unifyUri(JoinHttpPath):       nil,
	unifyUri(LeaveHttpPath):      nil,
	unifyUri(JoinInstancePath):   nil,
	unifyUri(LeaveInstancePath):  nil,
	unifyUri(AbortRequestPath):   nil,
	unifyUri(RunningDetailsPath): nil,
}

func PeerHttpEndpoint(peer *Peer, uri string, enableHTTPS bool) (string, error) {
	if err := CheckAddr(peer); err != nil {
		return "", err
	}
	scheme := "http"
	if enableHTTPS {
		scheme = "https"
	}
	port := SelectPort(peer, uri)
	return fmt.Sprintf("%s://%s:%v", scheme, peer.Addr.Host, port), nil
}

type PortType string

const (
	OpsPort     PortType = "operations_port"
	ServicePort PortType = "service_port"
)

func PortTypeForUri(uri string) PortType {
	targetUri := unifyUri(uri)
	if _, exists := OpsUrls[targetUri]; exists {
		return OpsPort
	} else {
		return ServicePort
	}
}

func SelectPort(targetPeer *Peer, targetUri string) int {
	if clientType := PortTypeForUri(targetUri); clientType == OpsPort {
		logger.Log().Debugf("select port for peer request uri: [%s], ops port: [%v]", targetUri, targetPeer.Addr.OpsPort)
		return targetPeer.Addr.OpsPort
	} else {
		logger.Log().Debugf("select port for peer request uri: [%s], http port: [%v]", targetUri, targetPeer.Addr.HttpPort)
		return targetPeer.Addr.HttpPort
	}
}

func CheckAddr(peer *Peer) error {
	if net.ParseIP(peer.Addr.Host) == nil {
		return fmt.Errorf("invalid host %s for peer %s", peer.Addr.Host, peer.Name)
	}
	if peer.Addr.HttpPort < 0 || peer.Addr.HttpPort > 65535 {
		return fmt.Errorf("invalid port %v for peer %s", peer.Addr.HttpPort, peer.Name)
	}
	if peer.Addr.OpsPort < 0 || peer.Addr.OpsPort > 65535 {
		return fmt.Errorf("invalid ops port %v for peer %s", peer.Addr.OpsPort, peer.Name)
	}
	return nil
}

func unifyUri(uri string) string {
	res, _ := strings.CutPrefix(uri, "/")
	return res
}
