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

package scheduler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	peer_client "route-server/pkg/common/client/peer-client"

	"route-server/pkg/common/cluster"
	"route-server/pkg/common/config"
	http2 "route-server/pkg/common/http"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/trace"
	"route-server/pkg/common/utils"
)

const (
	EndpointType      = "endpoint"
	ClusterLeaderType = "leader"
)

func NewRemoteScheduler(cfg *config.RemoteSchedulerCfg,
	clusterService cluster.ClusterService, peerClient *peer_client.PeerClient) (*RemoteScheduler, error) {
	if cfg == nil {
		return nil, fmt.Errorf("remote config must not be empty")
	}

	if !cfg.Enabled {
		return nil, nil
	}

	if clusterService == nil {
		return nil, fmt.Errorf("cluster service is nil")
	}

	var leaderProvider LeaderProvider
	switch cfg.RemoteType {
	case EndpointType:
		endpoint := cfg.RemoteEndpoint
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			return nil, fmt.Errorf("endpoint don't contains http:// or https:// prefix")
		}

		client := peerClient.OpsPeerHttpClient

		localEndpoint, err := utils.PeerHttpEndpoint(clusterService.LocalNode(),
			utils.ScheduleRequestPath, peerClient.UseHttps(utils.RequestEventPath))
		if err != nil {
			return nil, fmt.Errorf("can't get local peer http endpoint, err = %v", err)
		}

		leaderProvider = &ConfigLeaderProvider{
			endpoint:          cfg.RemoteEndpoint,
			localNodeEndpoint: localEndpoint,
			httpClient:        client,
		}
	case ClusterLeaderType:
		leaderProvider = &ClusterLeaderProvider{
			clusterService: clusterService,
			peerClient:     peerClient,
		}
	default:
		return nil, fmt.Errorf("unrecognized remote scheduler type %s", cfg.RemoteType)
	}

	remoteScheduler := &RemoteScheduler{
		leaderProvider: leaderProvider,
		cfg:            cfg,
	}
	return remoteScheduler, nil
}

type RemoteScheduler struct {
	cfg            *config.RemoteSchedulerCfg
	leaderProvider LeaderProvider
	closeChan      chan struct{}
}

func (rs *RemoteScheduler) Close() {
	if rs.closeChan != nil {
		close(rs.closeChan)
	}
}

func (rs *RemoteScheduler) ShouldDispatchLocally() (bool, error) {
	return rs.leaderProvider.ShouldDispatchLocally()
}

func (rs *RemoteScheduler) Schedule(request utils.GenerateRequest, ctx context.Context,
	params map[string]string, tracer trace.RequestTracer, blackId string, workerId string, workerBootId string) (*ScheduleResponse, error) {

	remoteRequest := RemoteScheduleRequest{
		Request:       utils.WrapRequest(request, rs.cfg.NeedContent),
		PreInstanceId: blackId,
		Params:        params,
		WorkerId:      workerId,
		WorkerBootId:  workerBootId,
	}

	reqBody, err := utils.SonicApi.Marshal(remoteRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request to json, err = %v", err)
	}
	rawRes, err := rs.leaderProvider.ProcessLeaderReq(ctx, reqBody, rs.cfg.TimeoutMs, tracer, request)
	if err != nil {
		return nil, err
	}
	return parseScheduleResponse(rawRes)
}

func parseScheduleResponse(rawBytes []byte) (*ScheduleResponse, error) {
	res := &ScheduleResponse{}
	if err := utils.SonicApi.Unmarshal(rawBytes, res); err != nil {
		return nil, fmt.Errorf("failed to unmarshal scheduler result, err = %v", err)
	}
	return res, nil
}

type RemoteScheduleRequest struct {
	Request      *utils.RemoteRequestWrapper `json:"req,omitempty"`
	CandidateNum uint32                      `json:"size"`
	// PreInstanceId is used to avoid pd link down during choosing decode instance, it is nil when prefill
	PreInstanceId string            `json:"pre_id,omitempty"`
	Params        map[string]string `json:"params,omitempty"`
	WorkerId      string            `json:"worker_id,omitempty"`
	WorkerBootId  string            `json:"boot_id,omitempty"`

	PromptKey string `json:"prompt_key,omitempty"` // Used for apc scheduling, promptKey is calculated by worker itself
}

type LeaderProvider interface {
	ProcessLeaderReq(ctx context.Context, body []byte, timeOutMs int, tracer trace.RequestTracer, request utils.GenerateRequest) ([]byte, error)
	ShouldDispatchLocally() (bool, error)
}

type ConfigLeaderProvider struct {
	localNodeEndpoint string
	endpoint          string
	httpClient        *http2.Client
}

func (c *ConfigLeaderProvider) ShouldDispatchLocally() (bool, error) {
	return c.localNodeEndpoint == c.endpoint, nil
}

func (c *ConfigLeaderProvider) ProcessLeaderReq(ctx context.Context, body []byte, timeOutMs int, tracer trace.RequestTracer, request utils.GenerateRequest) ([]byte, error) {
	if tracer != nil {
		tracer.Event("", trace.TraceEventRemoteSchedule, fmt.Sprintf(
			"send scheduler for [%s] request [%s] to remote scheudler [%s]", request.GetType(), request.GetRequestId(), c.endpoint))
	}
	var cancel context.CancelFunc
	if timeOutMs > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeOutMs)*time.Millisecond)
		defer cancel()
	}
	req, err := http2.BuildHttpRequest(ctx, http.MethodPost,
		fmt.Sprintf("%s/%s", c.endpoint, utils.ScheduleRequestPath),
		bytes.NewReader(body), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request, err = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.DoRequest(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %v", err)
	}

	rawBytes, err := io.ReadAll(resp.Body)
	defer func(Body io.ReadCloser) {
		if err := Body.Close(); err != nil {
			logger.Log().Warnf("failed to close resp body for [%s] [%s]", c.endpoint, utils.ScheduleRequestPath)
		}
	}(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read scheduler result, retCode %v, err = %v",
			resp.StatusCode, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to remote schedule, retCode [%v], details: [%s]",
			resp.StatusCode, string(rawBytes))
	}
	return rawBytes, nil
}

type ClusterLeaderProvider struct {
	clusterService cluster.ClusterService
	peerClient     *peer_client.PeerClient
}

func (c *ClusterLeaderProvider) ShouldDispatchLocally() (bool, error) {
	leader, id := c.clusterService.GetLeader()
	if id == "" {
		return true, fmt.Errorf("no leader is available")
	}
	localNode := c.clusterService.LocalNode()
	if leader != nil && localNode != nil {
		return leader.Addr.Host == localNode.Addr.Host && leader.Addr.HttpPort == localNode.Addr.HttpPort, nil
	}
	if localNode != nil {
		return localNode.Name == id, nil
	}
	return false, fmt.Errorf("local node is nil")
}

func (c *ClusterLeaderProvider) ProcessLeaderReq(ctx context.Context, body []byte, timeOutMs int, tracer trace.RequestTracer, request utils.GenerateRequest) ([]byte, error) {
	leader, id := c.clusterService.GetLeader()
	if leader == nil {
		return nil, fmt.Errorf("no leader is available, id = [%s]", id)
	}
	err := utils.CheckAddr(leader)
	if err != nil {
		return nil, err
	}
	if tracer != nil {
		tracer.Event("", trace.TraceEventRemoteSchedule, fmt.Sprintf(
			"send scheduler for [%s] request [%s] to remote scheduler [http://%s:%v]", request.GetType(), request.GetRequestId(), leader.Addr.Host, leader.Addr.OpsPort))
	}
	rawBytes, err := c.peerClient.SendToPeer(ctx, body, leader, utils.ScheduleRequestPath, http.MethodPost, timeOutMs)
	if err != nil {
		return nil, err
	}
	return rawBytes, err
}
