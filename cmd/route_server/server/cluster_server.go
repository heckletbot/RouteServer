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

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"route-server/pkg/route-server/instances"

	peer_client "route-server/pkg/common/client/peer-client"
	"route-server/pkg/common/cluster"
	"route-server/pkg/common/config"
	"route-server/pkg/common/events"
	http2 "route-server/pkg/common/http"
	"route-server/pkg/common/logger"
	"route-server/pkg/common/utils"
	"route-server/pkg/route-server/cluster/configmap"
	"route-server/pkg/route-server/cluster/etcd"
	"route-server/pkg/route-server/cluster/singleton"

	"github.com/gin-gonic/gin"
)

func newClusterService(config *config.Config, client *peer_client.PeerClient) (cluster.ClusterService, error) {
	httpPort, err := strconv.Atoi(config.ServerConfig.HttpPort)
	if err != nil {
		return nil, fmt.Errorf(
			"http port in server config is not number, got %s err = %v", config.ServerConfig.HttpPort, err)
	}
	opsPort, err := strconv.Atoi(config.ServerConfig.OpsPort)
	if err != nil {
		return nil, fmt.Errorf(
			"ops port in server config is not number, got %s err = %v", config.ServerConfig.OpsPort, err)
	}
	localPeer := &utils.Peer{
		Name: config.ServerConfig.Name,
		Addr: utils.PeerAddr{
			Host:     config.ServerConfig.Addr,
			HttpPort: httpPort,
			OpsPort:  opsPort,
			Domain:   config.PeerClientCfg.Domain,
		},
		IsCandidate: config.ClusterCfg.RoleCandidate,
	}

	err = cluster.InitClusterConfig(config)
	if err != nil {
		return nil, fmt.Errorf("invalid cluster config, err = %v", err)
	}

	var service cluster.ClusterService
	switch config.ClusterCfg.ClusterServiceType {
	case cluster.TypeSingleton:
		service = &singleton.SingletonClusterService{}
	case cluster.TypeEtcd:
		service = &etcd.EtcdClusterService{}
	case cluster.TypeK8sConfigMap:
		service = &configmap.ConfigMapClusterService{}
	case "":
		service = &singleton.SingletonClusterService{}
	default:
		return nil, fmt.Errorf("unrecognized cluster service type %s", config.ClusterCfg.ClusterServiceType)
	}

	service.Init(config.ClusterCfg, &config.InstanceManagerCfg.RouteTable, localPeer, client)
	return service, nil
}

type AdminHandler struct {
	ClusterService cluster.ClusterService
	// client to send proxy request to leader
	LeaderClient              *peer_client.PeerClient
	LeaderServingEventHandler *events.ServingEventsHandler
	WorkerHealthChecker       *cluster.WorkerHealthChecker
	InstanceManager           instances.Manager
}

func (ceh *AdminHandler) SetInstanceManager(m instances.Manager) {
	ceh.InstanceManager = m
}

func (ceh *AdminHandler) isValidPeer(peerId string) bool {
	state := ceh.ClusterService.State()
	if state == nil {
		return false
	}
	_, exists := state.Peers[peerId]
	return exists
}

func (ceh *AdminHandler) HandleLeaderServingEvents(ctx *gin.Context) {
	if !ceh.ClusterService.IsLeader() {
		ctx.JSON(http.StatusServiceUnavailable,
			http2.GinError(fmt.Sprintf("peer [%s] is not leader anymore", ceh.ClusterService.LocalNode().Name)))
		return
	}
	receivedEvents, err := ParseRequestEvents(ctx)
	defer func(Body io.ReadCloser) {
		if bodyErr := Body.Close(); bodyErr != nil {
			logger.Log().Warnf("failed to close request event req body, err = %v", bodyErr)
		}
	}(ctx.Request.Body)
	if err != nil {
		ctx.JSON(http.StatusBadRequest,
			http2.GinError(fmt.Sprintf("failed to parse request event request, err = %v", err)))
		return
	}
	ceh.WorkerHealthChecker.HandleWorkerPing(receivedEvents.WorkerId)
	ceh.LeaderServingEventHandler.DoHandleRequestEvents(receivedEvents)

	if ceh.InstanceManager == nil {
		logger.Log().Errorf("instanceManager is nil")
		ctx.JSON(http.StatusInternalServerError,
			http2.GinError("instanceManager is nil"))
		return
	}
	if instancesManager, ok := ceh.InstanceManager.(*instances.InstanceManager); !ok {
		logger.Log().Errorf("instanceManager type assertion failed, got type: %T", ceh.InstanceManager)
		ctx.JSON(http.StatusServiceUnavailable,
			http2.GinError("instanceManager type assertion failed"))
	} else {
		everyRoleAvailableCountMap := make(map[string]int)
		for role, instanceMap := range instancesManager.GetAvailable() {
			everyRoleAvailableCountMap[role] = len(instanceMap)
		}
		ctx.JSON(http.StatusOK, everyRoleAvailableCountMap)
	}
}

func (ceh *AdminHandler) handleAddNode(ctx *gin.Context) {
	joinPeer, err := cluster.ParsePeerReq(ctx)
	if err != nil {
		logger.Log().Infof("[AUDIT] src_ip=%s action=add_node result=failed reason=parse_error error=%v", ctx.ClientIP(), err)
		ctx.JSON(http.StatusBadRequest, http2.GinError(err.Error()))
		return
	}
	if err := cluster.CheckPeerValid(joinPeer); err != nil {
		logger.Log().Infof("[AUDIT] src_ip=%s action=add_node result=failed reason=invalid_peer error=%v", ctx.ClientIP(), err)
		ctx.JSON(http.StatusBadRequest,
			http2.GinError(fmt.Sprintf("peer is invalid, err = %v", err)))
		return
	}
	if err := ceh.ClusterService.HandleNewPeer(joinPeer); err != nil {
		logger.Log().Infof("[AUDIT] src_ip=%s action=add_node result=failed target=%s error=%v", ctx.ClientIP(), joinPeer.Name, err)
		ctx.JSON(http.StatusInternalServerError, http2.GinError(err.Error()))
	} else {
		logger.Log().Infof("[AUDIT] src_ip=%s action=add_node result=success target=%s", ctx.ClientIP(), joinPeer.Name)
		ctx.JSON(http.StatusOK, http2.GinAccepted())
	}
}

func (ceh *AdminHandler) handleLeaveNode(ctx *gin.Context) {
	leavePeer, err := cluster.ParseLeaveRequest(ctx)
	if err != nil {
		logger.Log().Infof("[AUDIT] src_ip=%s action=leave_node result=failed reason=parse_error error=%v", ctx.ClientIP(), err)
		ctx.JSON(http.StatusBadRequest, http2.GinError(err.Error()))
		return
	}
	if leavePeer == "" {
		logger.Log().Infof("[AUDIT] src_ip=%s action=leave_node result=failed reason=empty_peer_name", ctx.ClientIP())
		ctx.JSON(http.StatusBadRequest, http2.GinError("invalid empty peer name"))
		return
	}
	if err := ceh.ClusterService.HandleLeavePeer([]string{leavePeer}, cluster.LeaveReasonRequested); err != nil {
		logger.Log().Infof("[AUDIT] src_ip=%s action=leave_node result=failed target=%s error=%v", ctx.ClientIP(), leavePeer, err)
		ctx.JSON(http.StatusInternalServerError, http2.GinError(err.Error()))
	} else {
		logger.Log().Infof("[AUDIT] src_ip=%s action=leave_node result=success target=%s", ctx.ClientIP(), leavePeer)
		ctx.JSON(http.StatusOK, http2.GinAccepted())
	}
}

func (ceh *AdminHandler) handleAddInstance(ctx *gin.Context) {
	addReq := &cluster.InstanceJoinRequest{}
	if err := json.NewDecoder(ctx.Request.Body).Decode(addReq); err != nil {
		logger.Log().Infof("[AUDIT] src_ip=%s action=add_instance result=failed reason=parse_error error=%v", ctx.ClientIP(), err)
		ctx.JSON(http.StatusBadRequest,
			http2.GinError(fmt.Sprintf("failed to parse add req body, expect InstanceJoinRequest, err = %v", err)))
		return
	}
	if err := addReq.ValidCheck(); err != nil {
		logger.Log().Infof("[AUDIT] src_ip=%s action=add_instance result=failed reason=invalid_instance error=%v", ctx.ClientIP(), err)
		ctx.JSON(http.StatusBadRequest, http2.GinError(fmt.Sprintf("invalid instance, err = %v", err)))
		return
	}
	if err := ceh.ClusterService.HandleNewInstance(addReq); err != nil {
		logger.Log().Infof("[AUDIT] src_ip=%s action=add_instance result=failed target=%s error=%v", ctx.ClientIP(), addReq.InstanceNames(), err)
		ctx.JSON(http.StatusInternalServerError, http2.GinError(err.Error()))
	} else {
		logger.Log().Infof("[AUDIT] src_ip=%s action=add_instance result=success target=%s", ctx.ClientIP(), addReq.InstanceNames())
		ctx.JSON(http.StatusOK, http2.GinAccepted())
	}
}

func (ceh *AdminHandler) handleLeaveInstance(ctx *gin.Context) {
	leaveReq := &cluster.InstanceLeaveRequest{}
	if err := json.NewDecoder(ctx.Request.Body).Decode(leaveReq); err != nil {
		logger.Log().Infof("[AUDIT] src_ip=%s action=leave_instance result=failed reason=parse_error error=%v", ctx.ClientIP(), err)
		ctx.JSON(http.StatusBadRequest,
			http2.GinError(fmt.Sprintf("failed to parse leave req body, expect InstanceLeaveRequest, err = %v", err)))
		return
	}
	if err := leaveReq.ValidCheck(); err != nil {
		logger.Log().Infof("[AUDIT] src_ip=%s action=leave_instance result=failed reason=invalid_request error=%v", ctx.ClientIP(), err)
		ctx.JSON(http.StatusBadRequest, http2.GinError(fmt.Sprintf("invalid request, err = %v", err)))
		return
	}
	if errors.Is(ctx.Request.Context().Err(), context.Canceled) {
		logger.Log().Warnf("request [%s] is cancelled by upstream", ctx.Request.URL)
		return
	}
	if err := ceh.ClusterService.HandleLeaveInstance(leaveReq, cluster.LeaveReasonRequested); err != nil {
		logger.Log().Infof("[AUDIT] src_ip=%s action=leave_instance result=failed target=%v error=%v", ctx.ClientIP(), leaveReq.InstanceNames, err)
		ctx.JSON(http.StatusInternalServerError, http2.GinError(err.Error()))
	} else {
		logger.Log().Infof("[AUDIT] src_ip=%s action=leave_instance result=success target=%v", ctx.ClientIP(), leaveReq.InstanceNames)
		ctx.JSON(http.StatusOK, http2.GinAccepted())
	}
}

func (ceh *AdminHandler) handleGetLocalState(ctx *gin.Context) {
	state := ceh.ClusterService.State()
	if state != nil {
		res := cluster.LocalStateResponse{
			State: state,
		}
		ctx.JSON(http.StatusOK, res)
		return
	}
	_, leaderId := ceh.ClusterService.GetLeader()
	nilReason := ""
	if leaderId == "" {
		nilReason = "No leader is elected yet"
	} else {
		nilReason = "state not init yet, check seed nodes status"
	}
	ctx.JSON(http.StatusServiceUnavailable, http2.GinError("No state found, "+nilReason))
}

func (ceh *AdminHandler) handleGetConsistentStats(ctx *gin.Context) {
	stats := ceh.ClusterService.Stats()
	res := cluster.ConsistentResponse{
		Stats: stats,
	}
	ctx.JSON(http.StatusOK, res)
}

func (ceh *AdminHandler) handleGetLeader(ctx *gin.Context) {
	leader, leaderId := ceh.ClusterService.GetLeader()

	if leader != nil {
		res := cluster.LeaderResponse{
			Name:           leaderId,
			LeaderPeerInfo: leader,
		}
		ctx.JSON(http.StatusOK, res)
		return
	}

	if leaderId == "" {
		ctx.JSON(http.StatusServiceUnavailable, http2.GinError("No elected leader is found for this endpoint"))
	} else {
		ctx.JSON(http.StatusInternalServerError,
			http2.GinError(fmt.Sprintf("Got elected master id [%s], but not found in cluster state peers", leaderId)))
	}
}

func (ceh *AdminHandler) adminLeaderProxyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, exists := utils.LeaderOperationSet[c.Request.URL.Path]; !exists {
			logger.Log().Tracef(
				"received admin request [%s] forwarded by [%s], match local process requests, deal locally",
				c.Request.URL.RequestURI(), forwardFrom(c))
			c.Next()
			return
		}
		if ceh.ClusterService.IsLeader() {
			logger.Log().Infof("received admin request [%s] forwarded by [%s], deal as leader",
				c.Request.URL.RequestURI(), forwardFrom(c))
			c.Next()
			return
		}
		logger.Log().Debugf("forwarding admin request [%s] to leader", c.Request.URL.RequestURI())
		ceh.forwardRequestToLeader(c)
		c.Abort()
	}
}

func forwardFrom(ctx *gin.Context) string {
	src := ctx.Request.Header.Get("X-Forwarded-For")
	if src == "" {
		src = "User"
	}
	return src
}

func (ceh *AdminHandler) forwardRequestToLeader(ctx *gin.Context) {
	leader, leaderName := ceh.ClusterService.GetLeader()
	if leaderName == "" {
		ctx.JSON(http.StatusServiceUnavailable,
			http2.GinError("no leader elected, can't process cluster admin requests"))
		return
	}
	if leader == nil {
		ctx.JSON(http.StatusInternalServerError, http2.GinError("leader info is missing in current route-server"))
		return
	}

	httpAddr, err := utils.PeerHttpEndpoint(leader, ctx.Request.RequestURI,
		ceh.LeaderClient.UseHttps(ctx.Request.RequestURI))
	if err != nil {
		ctx.JSON(http.StatusInternalServerError,
			http2.GinError(fmt.Sprintf("leader [%s] address is invalid, err = %v", leaderName, err)))
		return
	}

	reqContext := ctx.Request.Context()
	proxyReq := ceh.proxyRequest(ctx, reqContext, httpAddr)
	if proxyReq == nil {
		return
	}
	response, err := ceh.LeaderClient.DoRequest(proxyReq, ctx.Request.RequestURI)
	if err != nil {
		if errors.Is(reqContext.Err(), context.Canceled) {
			logger.Log().Warnf("proxy [%s] request cancelled by client", proxyReq.URL.RequestURI())
		}
		ctx.JSON(http.StatusInternalServerError,
			http2.GinError(fmt.Sprintf("failed to forward proxy request [%s] to leader [%s], err = %v",
				proxyReq.URL.RequestURI(), leaderName, err)))
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			logger.Log().Warnf("failed to close proxy response for request [%s] to leader [%s]",
				proxyReq.URL.RequestURI(), leaderName)
		}
	}(response.Body)

	copyHeaders(ctx.Writer.Header(), response.Header)
	ctx.Writer.WriteHeader(response.StatusCode)
	_, err = io.Copy(ctx.Writer, response.Body)
	if err != nil {
		logger.Log().Errorf("failed to copy response body, err = %v", err)
	}
	logger.Log().Infof("End forwarding request to leader for request [%s] to leader [%s], status code [%v]",
		proxyReq.URL.RequestURI(), leaderName, response.StatusCode)
}

func (ceh *AdminHandler) proxyRequest(ctx *gin.Context, reqCtx context.Context, httpAddr string) *http.Request {
	uri := ctx.Request.URL.RequestURI()
	targetUrl := httpAddr + uri
	proxyReq, err := http.NewRequestWithContext(reqCtx, ctx.Request.Method, targetUrl, ctx.Request.Body)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError,
			http2.GinError(fmt.Sprintf("failed to create proxy request for [%s], err = %v", uri, err)))
		return nil
	}
	copyHeaders(proxyReq.Header, ctx.Request.Header)
	if clientIP, _, err := net.SplitHostPort(ctx.Request.RemoteAddr); err == nil {
		proxyReq.Header.Add("X-Forwarded-For", clientIP)
	}
	return proxyReq
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func ParseRequestEvents(ctx *gin.Context) (*utils.WorkerEventsRequest, error) {
	req := &utils.WorkerEventsRequest{}
	if err := json.NewDecoder(ctx.Request.Body).Decode(&req); err != nil {
		return nil, fmt.Errorf("failed to parse req body, expect EventsRequest, err = %v", err)
	}
	return req, nil
}

func (ceh *AdminHandler) handleAbortRequest(ctx *gin.Context) {
	bodyBytes, err := io.ReadAll(ctx.Request.Body)
	if err != nil {
		logger.Log().Infof("[AUDIT] src_ip=%s action=abort_request result=failed reason=read_body_error error=%v", ctx.ClientIP(), err)
		ctx.JSON(http.StatusBadRequest,
			http2.GinError(fmt.Sprintf("failed to read body, err = %v", err)))
		return
	}
	defer ctx.Request.Body.Close()
	var allReqs cluster.AbortRequest
	if err := utils.SonicApi.Unmarshal(bodyBytes, &allReqs); err != nil {
		logger.Log().Infof("[AUDIT] src_ip=%s action=abort_request result=failed reason=parse_error error=%v", ctx.ClientIP(), err)
		ctx.JSON(http.StatusBadRequest,
			http2.GinError(fmt.Sprintf("failed to unmarshal request body, err = %v", err)))
		return
	}
	if len(allReqs.ReqIds) == 0 {
		logger.Log().Infof("[AUDIT] src_ip=%s action=abort_request result=failed reason=empty_req_ids", ctx.ClientIP())
		ctx.JSON(http.StatusBadRequest,
			http2.GinError("request body is empty or contains no req_ids"))
		return
	}
	state := ceh.ClusterService.State()
	if state == nil {
		logger.Log().Infof("[AUDIT] src_ip=%s action=abort_request result=failed target=%v reason=no_state_found", ctx.ClientIP(), allReqs.ReqIds)
		ctx.JSON(http.StatusServiceUnavailable, http2.GinError("no state found"))
		return
	}
	resultChan := make(chan cluster.AbortResponse)
	var wg sync.WaitGroup
	// do best effort send request to all workers
	AbortOnWorker := func(peer *utils.Peer) {

		defer func() {
			wg.Done()
		}()

		timeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		httpAddr, err := utils.PeerHttpEndpoint(peer, ctx.Request.RequestURI,
			ceh.LeaderClient.UseHttps(ctx.Request.RequestURI))
		if err != nil {
			logger.Log().Warnf("Dealing Abort Request: worker [%s] address is invalid, err = %v", peer.Name, err)
			return
		}
		targetUrl := httpAddr + "/internal/abort"
		proxyReq, err := http.NewRequest("POST", targetUrl, bytes.NewBuffer(bodyBytes))
		copyHeaders(proxyReq.Header, ctx.Request.Header)
		if err != nil {
			logger.Log().Warnf("Dealing Abort Request: failed to create request for worker [%s], err = %v", peer.Name, err)
			return
		}
		proxyReq = proxyReq.WithContext(timeoutCtx)
		resp, err := ceh.LeaderClient.DoRequest(proxyReq, ctx.Request.RequestURI)
		if err != nil {
			if timeoutCtx.Err() == context.DeadlineExceeded {
				logger.Log().Warnf("Dealing Abort Request: request to worker [%s] timed out", peer.Name)
			} else {
				logger.Log().Warnf("Dealing Abort Request: failed to send request to worker [%s], err = %v", peer.Name, err)
			}
			return
		}
		defer resp.Body.Close()
		var result cluster.AbortResponse
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			logger.Log().Warnf("Dealing Abort Request: received non-successful status code from worker [%s], status = %d", peer.Name, resp.StatusCode)
			return
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			logger.Log().Warnf("Dealing Abort Request: failed to unmarshal JSON response from worker [%s], err = %v", peer.Name, err)
			return
		}
		resultChan <- result
	}

	for _, peer := range state.Peers {
		wg.Add(1)
		go AbortOnWorker(peer)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	var abortedReqs []string
	for result := range resultChan {
		abortedReqs = append(abortedReqs, result.AbortedReqIds...)
	}
	var notAbortedReqs []string
	abortedReqsMap := make(map[string]struct{})
	for _, reqId := range abortedReqs {
		abortedReqsMap[reqId] = struct{}{}
	}
	for _, reqId := range allReqs.ReqIds {
		if _, exists := abortedReqsMap[reqId]; !exists {
			notAbortedReqs = append(notAbortedReqs, reqId)
		}
	}
	response := cluster.AbortResponse{
		AbortedReqIds:  abortedReqs,
		NotFoundReqIds: notAbortedReqs,
	}
	logger.Log().Infof("[AUDIT] src_ip=%s action=abort_request result=success target=%v aborted=%v not_found=%v", ctx.ClientIP(), allReqs.ReqIds, abortedReqs, notAbortedReqs)
	ctx.JSON(http.StatusOK, response)
}

func (ceh *AdminHandler) handleRunningDetails(ctx *gin.Context) {
	state := ceh.ClusterService.State()
	if state == nil {
		ctx.JSON(http.StatusServiceUnavailable, http2.GinError("no state found"))
		return
	}
	resultChan := make(chan cluster.ServerDetails)
	var wg sync.WaitGroup

	DetailsOnWorker := func(peer *utils.Peer) {
		defer func() {
			wg.Done()
		}()

		timeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		httpAddr, err := utils.PeerHttpEndpoint(peer, ctx.Request.RequestURI,
			ceh.LeaderClient.UseHttps(ctx.Request.RequestURI))
		if err != nil {
			logger.Log().Warnf("Dealing Running Details: worker [%s] address is invalid, err = %v", peer.Name, err)
			return
		}
		targetUrl := httpAddr + "/internal/running_details"
		proxyReq, err := http.NewRequest("GET", targetUrl, nil)
		copyHeaders(proxyReq.Header, ctx.Request.Header)
		if err != nil {
			logger.Log().Warnf("Dealing Running Details: failed to create request for worker [%s], err = %v", peer.Name, err)
			return
		}
		proxyReq = proxyReq.WithContext(timeoutCtx)
		resp, err := ceh.LeaderClient.DoRequest(proxyReq, ctx.Request.RequestURI)
		if err != nil {
			if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
				logger.Log().Warnf("Dealing Running Details: request to worker [%s] timed out", peer.Name)
			} else {
				logger.Log().Warnf("Dealing Running Details: failed to send request to worker [%s], err = %v", peer.Name, err)
			}
			return
		}
		defer resp.Body.Close()
		var result cluster.ServerDetails
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			logger.Log().Warnf("Dealing Running Details: received non-successful status code from worker [%s], status = %d", peer.Name, resp.StatusCode)
			return
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			logger.Log().Warnf("Dealing Running Details: failed to unmarshal JSON response from worker [%s], err = %v", peer.Name, err)
			resp.Body.Close()
			return
		}
		resultChan <- result
	}

	for _, peer := range state.Peers {
		wg.Add(1)
		go DetailsOnWorker(peer)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	var runningDetails cluster.RunningDetails
	for result := range resultChan {
		runningDetails.ServerLists = append(runningDetails.ServerLists, result)
	}
	ctx.JSON(http.StatusOK, runningDetails)
}
