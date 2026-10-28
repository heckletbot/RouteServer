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

package etcd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/metrics"

	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
	"google.golang.org/grpc"
)

const (
	RSLeaderKey                   = "/rs/leader"
	RSPeerRegistrationDir         = "/rs/peers/"
	RSClusterStateDir             = "/rs/cluster/state/"
	RSClusterStateAllPeersDir     = RSClusterStateDir + "peers/all/"
	RSClusterStateAllInstancesDir = RSClusterStateDir + "instances/all/"
	RSClusterStateInitKey         = "/rs/cluster/state/init"
	RSInstanceFailuresKey         = "/rs/instances/failures"
	LeaseKeepAliveMaxRetry        = 3
	WrapperLogFormat              = "etcd iter [%v]: %s"
)

const (
	StateUpdateReasonRestore        = "restore"
	StateUpdateReasonPeerChange     = "peer_change"
	StateUpdateReasonInstanceChange = "instance_change"
	ProcessPeerInfoStageRegister    = "peer_auto_register"
	ProcessPeerInfoStageStateSync   = "sync_peer_from_state"
)

const (
	NodeRoleWorker = iota
	NodeRoleLeader
	NodeRoleCandidate
)

type EtcdStatus string

const (
	StatusConnected    EtcdStatus = "connected"
	StatusDisconnected EtcdStatus = "disconnected"
)

type EtcdWrapper struct {
	status    atomic.Value
	closeOnce sync.Once

	logId int

	localNode          *utils.Peer
	cfg                *config.ClusterConfig
	initRouteCfg       *config.RouteTable
	stateUpdateFunc    func(state *cluster.ClusterState, reason string)
	latestStateFunc    func() *cluster.ClusterState
	failureChangedFunc func(cache cluster.EtcdHealthStatsCache) error

	leaderId              atomic.Value
	waitForLeaderPeerInfo atomic.Bool
	wg                    sync.WaitGroup

	stateChangeListeners *[]cluster.StateApplier

	// etcd client
	etcdClient       *clientv3.Client
	sessionWithLease *concurrency.Session
	election         *concurrency.Election

	// leader watch
	leaderChan           <-chan clientv3.GetResponse
	leaderKeyWatchChan   clientv3.WatchChan
	leaderKeyWatchCancel context.CancelFunc
	activeResign         chan error

	failureInstanceChan   clientv3.WatchChan
	failureInstanceCancel context.CancelFunc

	// leader states
	peerRegisterWatchCancel context.CancelFunc
	peerRegisterWatchChan   clientv3.WatchChan

	// state & applier
	stateChangeLock     sync.RWMutex
	instanceWatchChan   clientv3.WatchChan
	instanceWatchCancel context.CancelFunc
	peerWatchChan       clientv3.WatchChan
	peerWatchCancel     context.CancelFunc

	lastFailureSnapshotRound int32

	closeChan chan struct{}
}

func (ew *EtcdWrapper) CheckConnection() bool {
	return ew.status.Load() == StatusConnected
}

func (ew *EtcdWrapper) Init(localNode *utils.Peer, config *config.ClusterConfig, initRoute *config.RouteTable,
	stateUpdateFunc func(state *cluster.ClusterState, reason string),
	latestStateFunc func() *cluster.ClusterState,
	stateChangeListeners *[]cluster.StateApplier,
	initSeenFailureSnapshotRound int32,
	failureChangedFunc func(cache cluster.EtcdHealthStatsCache) error) error {
	ew.localNode = localNode
	ew.cfg = config
	ew.initRouteCfg = initRoute
	ew.activeResign = make(chan error, 10)
	ew.waitForLeaderPeerInfo.Store(false)
	ew.leaderId.Store("")
	ew.stateChangeListeners = stateChangeListeners
	ew.stateUpdateFunc = stateUpdateFunc
	ew.latestStateFunc = latestStateFunc
	ew.failureChangedFunc = failureChangedFunc
	ew.status.Store(StatusDisconnected)
	ew.lastFailureSnapshotRound = initSeenFailureSnapshotRound
	ew.closeChan = make(chan struct{})

	etcdCfg := ew.cfg.EtcdCfg
	if etcdCfg == nil {
		return fmt.Errorf("etcd cfg is nil")
	}
	client, err := createEtcdClient(ew.cfg, ew.logId)
	if err != nil {
		return fmt.Errorf("failed to create etcd client, err = %v", err)
	}
	ew.etcdClient = client
	session, err := concurrency.NewSession(client, concurrency.WithTTL(config.EtcdCfg.LeaseTtlSec))
	if err != nil {
		return fmt.Errorf("failed to create etcd session, err = %v", err)
	}
	logInfoWithId(ew.logId, "etcd session created")
	ew.sessionWithLease = session
	ew.status.Store(StatusConnected)
	go ew.leaseKeeper()

	retry := etcdCfg.RetryTime
	retryInterval := time.Duration(etcdCfg.RetryInterval) * time.Second
	if err := ew.runWithRetry(retry, retryInterval, "restore state from Etcd",
		ew.restoreStateFromEtcd); err != nil {
		ew.status.Store(StatusDisconnected)
		return err
	}

	ew.initWatchChannels()
	go ew.watchPeerRegisters()
	go ew.watchStatePeers()
	go ew.watchStateInstances()
	if err := ew.runWithRetry(retry, retryInterval, "register self peer to etcd",
		ew.registerToEtcd); err != nil {
		ew.status.Store(StatusDisconnected)
		return err
	}

	ew.election = concurrency.NewElection(ew.sessionWithLease, RSLeaderKey)
	ew.leaderChan = ew.election.Observe(context.Background())
	go ew.watchLeader()
	go ew.watchFailureInstance()
	if ew.cfg.RoleCandidate {
		go ew.campaignLeader()
	}

	if connected := ew.CheckConnection(); !connected {
		// fail fast
		return fmt.Errorf("etcd cluster is disconnected, see previous logs for further details")
	}
	return nil
}

func (ew *EtcdWrapper) Close() {
	ew.closeOnce.Do(func() {
		logInfoWithId(ew.logId, "etcd wrapper close connection")
		if ew.closeChan != nil {
			close(ew.closeChan)
		}
		ew.status.Store(StatusDisconnected)
		if ew.leaderKeyWatchCancel != nil {
			ew.leaderKeyWatchCancel()
		}
		if ew.failureInstanceCancel != nil {
			ew.failureInstanceCancel()
		}
		if ew.peerRegisterWatchCancel != nil {
			ew.peerRegisterWatchCancel()
		}
		if ew.instanceWatchCancel != nil {
			ew.instanceWatchCancel()
		}
		if ew.peerWatchCancel != nil {
			ew.peerWatchCancel()
		}
		if ew.sessionWithLease != nil {
			if err := ew.sessionWithLease.Close(); err != nil {
				logWarnWithId(ew.logId, "Failed to close etcd session: %v", err)
			}
		}
		if ew.etcdClient != nil {
			err := ew.etcdClient.Close()
			if err != nil {
				logWarnWithId(ew.logId, "Failed to close etcd client: %v", err)
			}
		}
		logInfoWithId(ew.logId, "etcd wrapper stopping: waiting for existing loop to finish")
		ew.wg.Wait()
		logInfoWithId(ew.logId, "etcd wrapper stopped")
		if ew.activeResign != nil {
			close(ew.activeResign)
		}
	})
}

// --------------  start up  ---------------------

func (ew *EtcdWrapper) initWatchChannels() {
	peersRegCtx, peersRegCancel := context.WithCancel(context.Background())
	ew.peerRegisterWatchCancel = peersRegCancel
	ew.peerRegisterWatchChan = ew.etcdClient.Watch(peersRegCtx, RSPeerRegistrationDir, clientv3.WithPrefix())

	peerStateCtx, peerStateCancel := context.WithCancel(context.Background())
	ew.peerWatchCancel = peerStateCancel
	ew.peerWatchChan = ew.etcdClient.Watch(peerStateCtx, RSClusterStateAllPeersDir, clientv3.WithPrefix())

	instanceCtx, instanceCancel := context.WithCancel(context.Background())
	ew.instanceWatchCancel = instanceCancel
	ew.instanceWatchChan = ew.etcdClient.Watch(instanceCtx, RSClusterStateAllInstancesDir, clientv3.WithPrefix())

	leaderCtx, leaderKeyWatchCancel := context.WithCancel(context.Background())
	ew.leaderKeyWatchChan = ew.etcdClient.Watch(leaderCtx, RSLeaderKey, clientv3.WithPrefix())
	ew.leaderKeyWatchCancel = leaderKeyWatchCancel

	failureInstanceCtx, failureInstanceCancel := context.WithCancel(context.Background())
	ew.failureInstanceChan = ew.etcdClient.Watch(failureInstanceCtx, RSInstanceFailuresKey, clientv3.WithPrefix())
	ew.failureInstanceCancel = failureInstanceCancel

}

func (ew *EtcdWrapper) restoreStateFromEtcd() error {
	restoredState := &cluster.ClusterState{
		Initialized:    true,
		SeedPeers:      make([]config.SeedInfo, 0),
		Peers:          make(map[string]*utils.Peer),
		InstanceRoutes: make(map[string]*config.Route),
		Version:        0,
	}
	err := ew.restorePeers(restoredState)
	if err != nil {
		return err
	}

	err = ew.restoreInstances(restoredState)
	if err != nil {
		return err
	}
	restoredState.Version = time.Now().Unix()
	ew.stateUpdateFunc(restoredState, StateUpdateReasonRestore)
	return nil
}

func (ew *EtcdWrapper) restoreInstances(restoredState *cluster.ClusterState) error {
	instanceRes, err := ew.etcdClient.Get(context.Background(), RSClusterStateAllInstancesDir, clientv3.WithPrefix())
	if err != nil {
		logErrorWithId(ew.logId, "Failed to get RSClusterStateAllInstancesDir %s: %v", RSClusterStateAllInstancesDir, err)
		return err
	}
	restoredState.InstanceRoutes = make(map[string]*config.Route, len(instanceRes.Kvs))
	for _, kv := range instanceRes.Kvs {
		if insName, instance, err := ew.parseInstanceFromKv(kv); err != nil {
			logErrorWithId(ew.logId, "failed to parse instance from key %s when restore: %v", string(kv.Key), err)
		} else {
			restoredState.InstanceRoutes[insName] = instance
		}
	}
	return nil
}

func (ew *EtcdWrapper) GetAllInstances() (map[string]*config.Route, error) {
	instanceRes, err := ew.etcdClient.Get(context.Background(), RSClusterStateAllInstancesDir, clientv3.WithPrefix())
	if err != nil {
		logErrorWithId(ew.logId, "Failed to get RSClusterStateAllInstancesDir %s: %v", RSClusterStateAllInstancesDir, err)
		return nil, err
	}
	resultRoutes := make(map[string]*config.Route, len(instanceRes.Kvs))
	for _, kv := range instanceRes.Kvs {
		insName, instance, err := ew.parseInstanceFromKv(kv)
		if err != nil {
			logErrorWithId(ew.logId, "failed to parse instance from key %s when restore: %v", string(kv.Key), err)
			return nil, err
		}
		resultRoutes[insName] = instance
	}
	return resultRoutes, nil
}

func (ew *EtcdWrapper) restorePeers(restoredState *cluster.ClusterState) error {
	peersRes, err := ew.etcdClient.Get(context.Background(), RSClusterStateAllPeersDir, clientv3.WithPrefix())
	if err != nil {
		logErrorWithId(ew.logId, "failed to get cluster state from etcd when get peers: %v", err)
		return err
	}
	restoredState.Peers = make(map[string]*utils.Peer, len(peersRes.Kvs))
	for _, kv := range peersRes.Kvs {
		if peerName, peer, err := ew.parsePeerFromKv(kv); err != nil {
			logErrorWithId(ew.logId, "failed to parse peer [%s] from etcd when restore: %v", peerName, err)
			continue
		} else {
			restoredState.Peers[peer.Name] = peer
		}
	}
	return nil
}

func createEtcdClient(cfg *config.ClusterConfig, logId int) (*clientv3.Client, error) {
	endpoints := cfg.EtcdCfg.EtcdEndpoints
	successEndpoints := make([]string, 0, len(endpoints))
	successEndpoints = tryConnectEndpoints(endpoints, successEndpoints, logId)
	if len(successEndpoints) == 0 {
		return nil, fmt.Errorf("failed to connect to all etcd endpoints: %v", endpoints)
	}
	logInfoWithId(logId, "successfully connect to etcd endpoints: %v, trying to create etcd client", successEndpoints)

	clientCfg := clientv3.Config{
		Endpoints:        endpoints,
		DialTimeout:      time.Duration(cfg.EtcdCfg.DialTimeout) * time.Millisecond,
		AutoSyncInterval: time.Duration(cfg.EtcdCfg.AutoSyncIntervalSec) * time.Second,
		DialOptions: []grpc.DialOption{
			grpc.WithBlock(),
		},
	}
	if cfg.EtcdCfg.TLSConfig != nil && cfg.EtcdCfg.TLSConfig.EnableHTTPS {
		tlsCfg, err := buildEtcdTLSConfig(cfg.EtcdCfg.TLSConfig, constant.TLSEtcdKeyPasswordEnv)
		if err != nil {
			return nil, fmt.Errorf("failed to build etcd TLS config: %v", err)
		}
		clientCfg.TLS = tlsCfg
	}
	cli, err := clientv3.New(clientCfg)

	if err != nil {
		return nil, fmt.Errorf("etcd client creation failed: %w", err)
	}
	// test connection
	hasSuccess := tryEtcdClientStatus(successEndpoints, err, cli, logId)
	if !hasSuccess {
		if err := cli.Close(); err != nil {
			logWarnWithId(logId, "failed to close connection to etcd %s: %v", successEndpoints[0], err)
		}
		return nil, fmt.Errorf("failed to get status from all etcd endpoints: %v", successEndpoints)
	}

	logInfoWithId(logId, "Successfully connected to etcd cluster")
	return cli, nil
}

// loadEtcdX509KeyPair loads the etcd client TLS certificate and private key,
// supporting decryption of encrypted private keys via a password from an environment variable.
func loadEtcdX509KeyPair(certFile, keyFile, passwordEnv string) (tls.Certificate, error) {
	password := os.Getenv(passwordEnv)
	if password == "" {
		return tls.LoadX509KeyPair(certFile, keyFile)
	}

	keyPEMBlock, err := os.ReadFile(keyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to read etcd key file %s: %v", keyFile, err)
	}

	block, _ := pem.Decode(keyPEMBlock)
	if block == nil {
		return tls.Certificate{}, fmt.Errorf("failed to decode PEM block from etcd key file %s", keyFile)
	}

	if x509.IsEncryptedPEMBlock(block) {
		decryptedDER, err := x509.DecryptPEMBlock(block, []byte(password))
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("failed to decrypt etcd private key: %v", err)
		}
		decryptedKeyPEM := pem.EncodeToMemory(&pem.Block{
			Type:  block.Type,
			Bytes: decryptedDER,
		})
		certPEM, err := os.ReadFile(certFile)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("failed to read etcd cert file %s: %v", certFile, err)
		}
		return tls.X509KeyPair(certPEM, decryptedKeyPEM)
	}

	return tls.LoadX509KeyPair(certFile, keyFile)
}

// buildEtcdTLSConfig builds a tls.Config for etcd client connection from TLSCertificateConfig.
// passwordEnv specifies the environment variable name for the private key decryption password.
func buildEtcdTLSConfig(tlsCfg *config.TLSCertificateConfig, passwordEnv string) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		},
	}

	if tlsCfg.SkipVerify {
		tlsConfig.InsecureSkipVerify = true
	}

	// Load client certificate if configured (supports encrypted private key)
	if tlsCfg.CertFile != "" && tlsCfg.KeyFile != "" {
		cert, err := loadEtcdX509KeyPair(tlsCfg.CertFile, tlsCfg.KeyFile, passwordEnv)
		if err != nil {
			return nil, fmt.Errorf("failed to load etcd client cert/key: %v", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	// Load CA for server verification
	if tlsCfg.CaFile != "" {
		caCert, err := os.ReadFile(tlsCfg.CaFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read etcd CA cert: %v", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse etcd CA cert")
		}
		tlsConfig.RootCAs = caCertPool
	}

	return tlsConfig, nil
}

func tryEtcdClientStatus(successEndpoints []string, err error, cli *clientv3.Client, logId int) bool {
	hasSuccess := false
	for _, ep := range successEndpoints {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err = cli.Status(ctx, ep)
		if err != nil {
			logWarnWithId(logId, "etcd client endpoint %s get status failed: %v", ep, err)
		} else {
			hasSuccess = true
		}
		cancel()
	}
	return hasSuccess
}

func tryConnectEndpoints(endpoints []string, successEndpoints []string, logId int) []string {
	for _, ep := range endpoints {
		tcpAddr := strings.TrimPrefix(ep, "http://")
		tcpAddr = strings.TrimPrefix(tcpAddr, "https://")
		conn, err := net.DialTimeout("tcp", tcpAddr, 3*time.Second)
		if err != nil {
			logErrorWithId(logId, "Network check failed for etcd endpoint %s: %v", ep, err)
		} else {
			successEndpoints = append(successEndpoints, ep)
			if err := conn.Close(); err != nil {
				logWarnWithId(logId, "failed to close temp connection to etcd %s: %v", ep, err)
			}
			logDebugWithId(logId, "Network connection to etcd %s successful", ep)
		}
	}
	return successEndpoints
}

// ---------- watchers --------

func (ew *EtcdWrapper) watchFailureInstance() {
	ew.wg.Add(1)
	defer ew.wg.Done()
	iter := 1
	for {
		select {
		case <-ew.closeChan:
			logInfoWithId(ew.logId, "stop to watch failureInstance")
			return
		case resp, ok := <-ew.failureInstanceChan:
			if !ok {
				logWarnWithId(ew.logId, "stop watch failureInstance: channel closed")
				return
			}
			if !ew.CheckConnection() {
				logWarnWithId(ew.logId, "stop watch failureInstance : disconnected")
				return
			}

			ew.handleFailureInstanceChanged(resp.Events, iter)
			iter++
		}
	}
}

func (ew *EtcdWrapper) watchPeerRegisters() {
	logInfoWithId(ew.logId, "start to watch peer registers under path %s", RSPeerRegistrationDir)
	ew.wg.Add(1)
	defer ew.wg.Done()
	for {
		select {
		case <-ew.closeChan:
			logInfoWithId(ew.logId, "stop to watch peer registers")
			return
		case resp, ok := <-ew.peerRegisterWatchChan:
			if !ok {
				logWarnWithId(ew.logId, "stop watch peer register: channel closed")
				return
			}
			if !ew.CheckConnection() {
				logWarnWithId(ew.logId, "stop watch peer register: disconnected")
				return
			}
			ew.handlePeerRegistration(resp.Events)
		}
	}
}

func (ew *EtcdWrapper) watchStatePeers() {
	logInfoWithId(ew.logId, "start to watch state peers")
	ew.wg.Add(1)
	iter := 1
	defer ew.wg.Done()
	for {
		select {
		case <-ew.closeChan:
			logInfoWithId(ew.logId, "stop to watch state peers")
			return
		case resp, ok := <-ew.peerWatchChan:
			if !ok {
				logWarnWithId(ew.logId, "stop watch peer state: channel closed")
				return
			}
			if !ew.CheckConnection() {
				logWarnWithId(ew.logId, "stop watch peer state: disconnected")
				return
			}
			ew.handlePeerStateChanges(resp.Events, iter)
			iter++
		}
	}
}

func (ew *EtcdWrapper) watchStateInstances() {
	logInfoWithId(ew.logId, "start to watch state instances")
	ew.wg.Add(1)
	defer ew.wg.Done()
	iter := 1
	for {
		select {
		case <-ew.closeChan:
			logInfoWithId(ew.logId, "stop to watch state instances")
			return
		case resp, ok := <-ew.instanceWatchChan:
			if !ok {
				logWarnWithId(ew.logId, "stop watch instance state: channel closed")
				return
			}
			if !ew.CheckConnection() {
				logWarnWithId(ew.logId, "stop watch instance state: disconnected")
				return
			}
			ew.handleInstanceStateChange(resp.Events, iter)
			iter++
		}
	}
}

func (ew *EtcdWrapper) watchLeader() {
	ew.wg.Add(1)
	defer ew.wg.Done()

	ticker := time.NewTicker(time.Duration(1) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ew.closeChan:
			logInfoWithId(ew.logId, "stop to watch leader")
			return
		case resp, ok := <-ew.leaderChan:
			if !ok {
				logWarnWithId(ew.logId, "stop watch leader: leader watch channel is closed")
				return
			}
			if !ew.CheckConnection() {
				logWarnWithId(ew.logId, "stop watch leader: disconnected")
				return
			}
			var newLeader string
			if len(resp.Kvs) > 0 {
				newLeader = string(resp.Kvs[0].Value)
				logInfoWithId(ew.logId, "observe leader changes, new leader [%s]", newLeader)
			} else {
				newLeader = ""
				logWarnWithId(ew.logId, "observe leader changes, no leader is elected")
			}
			if ew.getLeaderId() == newLeader {
				logWarnWithId(ew.logId, "seen the same leader before, notify listeners anyway")
			}
			ew.onLeaderChange(newLeader, "new leader is observed")
		case resp, ok := <-ew.leaderKeyWatchChan:
			if !ok {
				logWarnWithId(ew.logId, "stop watch leader: channel closed")
				return
			}
			if !ew.CheckConnection() {
				logWarnWithId(ew.logId, "stop watch instance state: disconnected")
				return
			}
			if !ew.checkIfLeaderExists(resp) {
				logWarnWithId(ew.logId, "detect no leader is trying to obtain leader key, entering no leader state")
				ew.onLeaderChange("", "no leader key right now")
			}
		case <-ticker.C:
			ew.maybeDealWithLateLeaderRegistration()
		}
	}
}

func (ew *EtcdWrapper) checkIfLeaderExists(resp clientv3.WatchResponse) bool {
	leaderExists := true
	existDelete := false
	for _, event := range resp.Events {
		if event.Type == clientv3.EventTypeDelete {
			existDelete = true
		}
	}
	if existDelete {
		leaderPeerRes, err := ew.etcdClient.Get(context.Background(), RSLeaderKey,
			clientv3.WithPrefix(), clientv3.WithKeysOnly())
		if err != nil {
			logErrorWithId(ew.logId, "failed to get leaders when check no leaders, err = %v", err)
		} else if len(leaderPeerRes.Kvs) == 0 {
			leaderExists = false
		}
	}

	return leaderExists
}

func (ew *EtcdWrapper) onLeaderChange(newLeader string, reason string) {
	ew.leaderId.Store(newLeader)
	waitForLeaderPeerInfo := false

	if newLeader == ew.localNode.Name {
		if err := ew.onBecomeLeader(); err != nil {
			logErrorWithId(ew.logId, "failed to onBecomeLeader: %v", err)
			ew.activeResign <- err
		}
	}
	ew.recordLeaderChangeMetrics()

	var leaderPeer *utils.Peer
	if newLeader != "" {
		ew.stateChangeLock.RLock()
		state := ew.latestStateFunc()
		if !isPeerInState(state, newLeader, nil) {
			// inform later when leader registered
			waitForLeaderPeerInfo = true
			reason = fmt.Sprintf(
				"leader [%s] is not registered yet, skip for now and entering no leader states, original reason [%s]",
				newLeader, reason)
		}
		leaderPeer = state.Peers[newLeader]
		ew.stateChangeLock.RUnlock()
	}

	ew.notifyLeaderChanges(leaderPeer, reason)

	ew.waitForLeaderPeerInfo.Store(waitForLeaderPeerInfo)
}

func (ew *EtcdWrapper) recordLeaderChangeMetrics() {
	currentRole := NodeRoleWorker
	isLeader := false
	if ew.isLeader() {
		isLeader = true
		currentRole = NodeRoleLeader
	} else if ew.cfg.RoleCandidate {
		currentRole = NodeRoleCandidate
	}
	metrics.RecordLeaderChange(ew.localNode.Name, strconv.FormatBool(isLeader))
	metrics.SetCurrentNodeRole(currentRole)
}

func (ew *EtcdWrapper) notifyLeaderChanges(leaderPeer *utils.Peer, reason string) {
	isMeLeader := leaderPeer != nil && leaderPeer.Name == ew.localNode.Name
	logInfoWithId(ew.logId, "notifying leader change, reason: %v", reason)
	restoredFailures, err := ew.GetRestoreInstanceFailures()

	if err != nil {
		logWarnWithId(ew.logId, "Failed to restore instance failures: %v, ignoring it", err)
		restoredFailures = nil
	} else {
		if shouldResume, resumeReason := ew.shouldResumeInstanceFailures(restoredFailures); !shouldResume {
			restoredFailures = nil
			logInfoWithId(ew.logId, "ignoring health status in etcd, due to [%s]", resumeReason)
		} else {
			logInfoWithId(ew.logId, "resuming health status in etcd, due to [%s], failureDetails [%v]", resumeReason, restoredFailures)
		}
	}

	for _, listener := range *ew.stateChangeListeners {
		listener.OnLeaderChanged(leaderPeer, isMeLeader, restoredFailures)
	}
	logInfoWithId(ew.logId, "successfully notified leader change, reason: %v", reason)
}

// maybeDealWithLateLeaderRegistration: when etcd decide leader too quick, event quicker than peer registration.
// The leader peer info might not be updated to cluster state yet when the new leader name is observed (waitForLeaderPeerInfo will be true at this point).
// Hence, we need to info applier to change leader info after leader peer info is finally registered to cluster state.
func (ew *EtcdWrapper) maybeDealWithLateLeaderRegistration() {
	if ew.waitForLeaderPeerInfo.Load() {
		ew.stateChangeLock.RLock()
		state := ew.latestStateFunc()
		leaderPeer, exists := state.Peers[ew.getLeaderId()]
		ew.stateChangeLock.RUnlock()
		if exists {
			if ew.waitForLeaderPeerInfo.CompareAndSwap(true, false) {
				ew.notifyLeaderChanges(leaderPeer, "notify leader due to late registration")
			}
		}
	}
}

// ---------- leader action --------

func (ew *EtcdWrapper) onBecomeLeader() error {
	init, err := ew.isInitialized()
	if err != nil {
		return fmt.Errorf("when become leader, failed to get initialized state: %v", err)
	}
	if !init {
		if err = ew.initialState(ew.initRouteCfg); err != nil {
			return fmt.Errorf("when become leader, failed to write initialize state: %v", err)
		}
	} else {
		logInfoWithId(ew.logId, "become leader, already initialized")
	}
	if err := ew.processRegistrations(); err != nil {
		return fmt.Errorf("when become leader, failed to process peer registration, err = %v", err)
	}
	return nil
}

func (ew *EtcdWrapper) processRegistrations() error {
	resp, err := ew.etcdClient.Get(context.Background(), RSPeerRegistrationDir, clientv3.WithPrefix())
	if err != nil {
		return fmt.Errorf("when process registrations, get peer from etcd failed: %v", err)
	}
	ew.stateChangeLock.RLock()
	defer ew.stateChangeLock.RUnlock()
	for _, kv := range resp.Kvs {
		peerName, peer, err := ew.parsePeerFromKv(kv)
		if err != nil {
			logErrorWithId(ew.logId, "failed to parse peer [%s], err = %v", peerName, err)
			continue
		}
		if !isPeerInState(ew.latestStateFunc(), peer.Name, peer) {
			if err := ew.writePeerToEtcd(RSClusterStateAllPeersDir, peer, false); err != nil {
				logErrorWithId(ew.logId, "failed to add peer [%s] to state, err = %v", peer.Name, err)
			}
		}
	}
	return nil
}

func (ew *EtcdWrapper) campaignLeader() {
	logInfoWithId(ew.logId, "start to campaign leader")
	ew.wg.Add(1)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	defer ew.wg.Done()
	for {
		select {
		case <-ew.closeChan:
			logInfoWithId(ew.logId, "stop to campaign leader")
			return
		default:
			select {
			case <-ticker.C:
				if !ew.CheckConnection() {
					logWarnWithId(ew.logId, "stop watch leader: disconnected")
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(),
					time.Duration(ew.cfg.EtcdCfg.DialTimeout)*time.Millisecond)
				if err := ew.election.Campaign(ctx, ew.localNode.Name); err != nil {
					if errors.Is(err, context.DeadlineExceeded) {
						// timeout also happens when not leader
						logTraceWithId(ew.logId, "election timeout, retry later")
					} else {
						logWarnWithId(ew.logId, "Election failed, retry later: %v", err)
					}
				}
				cancel()
			case err, ok := <-ew.activeResign:
				if !ok {
					return
				}
				if err != nil {
					ctx, cancel := context.WithTimeout(context.Background(),
						time.Duration(ew.cfg.EtcdCfg.DialTimeout)*time.Millisecond)
					logErrorWithId(ew.logId, "resign leader. due to err = %v", err)
					if err := ew.election.Resign(ctx); err != nil {
						logErrorWithId(ew.logId, "failed to resign leader. err = %v", err)
					}
					cancel()
				}
			case <-ew.closeChan:
				logInfoWithId(ew.logId, "stop to campaign leader")
				return
			}
		}
	}
}

// ---------- event handlers -------

func (ew *EtcdWrapper) handlePeerRegistration(events []*clientv3.Event) {
	if !ew.isLeader() {
		return
	}
	logInfoWithId(ew.logId, "start to handle peer registration")
	ew.stateChangeLock.RLock()
	addPeers, removePeerIds := ew.parsePeerEtcdEvents(events, ew.latestStateFunc(), ProcessPeerInfoStageRegister)
	ew.stateChangeLock.RUnlock()

	if len(addPeers) > 0 {
		for _, peer := range addPeers {
			err := ew.writePeerToEtcd(RSClusterStateAllPeersDir, peer, false)
			if err != nil {
				logErrorWithId(ew.logId, "failed to add peer [%s] to state, err = %v", peer.Name, err)
				continue
			}
		}
		logInfoWithId(ew.logId, "detect registered peers %v", addPeers)
	}
	if len(removePeerIds) > 0 {
		logInfoWithId(ew.logId, "detect peers lease expired: %v", removePeerIds)
	}
	logInfoWithId(ew.logId, "successfully handled peer registration, add %v peers, see %v peers lease expired",
		len(addPeers), len(removePeerIds))
}

func (ew *EtcdWrapper) handlePeerStateChanges(events []*clientv3.Event, iter int) {
	logInfoWithId(ew.logId, "peer_changes_iter [%v], start to handle peer state changes events", iter)
	ew.stateChangeLock.Lock()
	defer ew.stateChangeLock.Unlock()
	state := ew.latestStateFunc()
	addPeers, removePeerIds := ew.parsePeerEtcdEvents(events, state, ProcessPeerInfoStageStateSync)
	if len(addPeers) == 0 && len(removePeerIds) == 0 {
		logInfoWithId(ew.logId, "peer_changes_iter [%v], no peers actually changed, skip handle peer state", iter)
		return
	}
	newState := cluster.DeepCopyState(state)
	for _, addPeer := range addPeers {
		newState.Peers[addPeer.Name] = addPeer
	}
	for _, peerId := range removePeerIds {
		delete(newState.Peers, peerId)
	}
	ew.stateUpdateFunc(newState, StateUpdateReasonPeerChange)
	for _, addPeer := range addPeers {
		for _, listener := range *ew.stateChangeListeners {
			listener.OnAddPeer(addPeer)
		}
	}
	for _, peerId := range removePeerIds {
		for _, listener := range *ew.stateChangeListeners {
			listener.OnRemovePeer(peerId)
		}
	}
	logInfoWithId(ew.logId, "peer_changes_iter [%v] successfully handled peer state changes events, %v added, %v removed",
		iter, len(addPeers), len(removePeerIds))
}

func (ew *EtcdWrapper) handleInstanceStateChange(events []*clientv3.Event, iter int) {
	logInfoWithId(ew.logId, "ins_change_iter [%v], start to handle instance state changes events", iter)
	ew.stateChangeLock.Lock()
	defer ew.stateChangeLock.Unlock()
	state := ew.latestStateFunc()
	addInstances, removeInstances := ew.parseInstanceEvents(events, state)
	if len(addInstances) == 0 && len(removeInstances) == 0 {
		return
	}

	newState := cluster.DeepCopyState(state)
	for id, instance := range addInstances {
		newState.InstanceRoutes[id] = instance
	}
	for _, removeId := range removeInstances {
		delete(newState.InstanceRoutes, removeId)
	}
	ew.stateUpdateFunc(newState, StateUpdateReasonInstanceChange)
	if len(addInstances) > 0 {
		for _, listener := range *ew.stateChangeListeners {
			listener.OnAddInstance(addInstances, constant.InstanceChangeReasonSynced)
		}
	}
	if len(removeInstances) > 0 {
		for _, listener := range *ew.stateChangeListeners {
			listener.OnRemoveInstance(removeInstances, constant.InstanceChangeReasonSynced)
		}
	}
	logInfoWithId(ew.logId, "ins_change_iter [%v], successfully handled instance state changes events, %v added, %v removed",
		iter, len(addInstances), len(removeInstances))
}

// ---------- instance failures ------------

func (ew *EtcdWrapper) GetRestoreInstanceFailures() (*utils.AllInstanceFailuresSnapshot, error) {
	res, err := ew.etcdClient.Get(context.Background(), RSInstanceFailuresKey)
	if err != nil {
		return nil, fmt.Errorf("failed to restore instance failures from etcd: %v", err)
	}
	var failures = &utils.AllInstanceFailuresSnapshot{
		Failures: make(map[string]*utils.InstanceFailureRecord),
	}
	if res.Kvs == nil || res.Count == 0 {
		return failures, nil
	}
	for _, kv := range res.Kvs {
		if kv.Value == nil {
			continue
		}
		if err = utils.SonicApi.Unmarshal(kv.Value, failures); err != nil {
			return nil, fmt.Errorf("failed to unmarshal instance failures from etcd: %v", err)
		}
		return failures, nil
	}
	return failures, nil
}

func (ew *EtcdWrapper) writeInstanceFailures(failuresMap map[string]*utils.InstanceFailureRecord, failureRound int32) error {
	if !ew.CheckConnection() {
		return fmt.Errorf("failed to write peer to etcd: disconnected")
	}
	if !ew.isLeader() {
		return fmt.Errorf("not leader")
	}

	failures := &utils.AllInstanceFailuresSnapshot{
		LeaderId:     ew.localNode.Name,
		LocalRound:   failureRound,
		SnapshotTime: time.Now().Format(time.RFC3339),
		Failures:     failuresMap,
	}

	marshal, err := utils.SonicApi.Marshal(failures)
	if err != nil {
		return fmt.Errorf("failed to marshal failures json for failed instances [%v], err = %v", failures, err)
	}
	err = ew.safeWriteKvIfLeader(RSInstanceFailuresKey, string(marshal))
	if err != nil {
		return fmt.Errorf("failed to write failures to etcd %s, err = %v", RSInstanceFailuresKey, err)
	}
	ew.lastFailureSnapshotRound = failureRound
	if ew.cfg.Verbose {
		logInfoWithId(ew.logId, "successfully write failures to etcd [%s]", RSInstanceFailuresKey)
	}
	return nil
}

// ---------- utils ------------

func (ew *EtcdWrapper) isLeader() bool {
	curLeaderId := ew.getLeaderId()
	return curLeaderId == ew.localNode.Name
}

func (ew *EtcdWrapper) getLeaderId() string {
	if !ew.CheckConnection() {
		return ""
	}
	curLeaderId, ok := ew.leaderId.Load().(string)
	if !ok {
		logErrorWithId(ew.logId, "leader id is not a string, got %v", ew.leaderId.Load())
		return ""
	}
	return curLeaderId
}

func (ew *EtcdWrapper) runWithRetry(retryTime int, interval time.Duration, funcDesc string, function func() error) error {
	var lastErr error = nil
	for i := range retryTime {
		if i != 0 {
			metrics.RecordETCDConnectionsRetries()
		}
		lastErr = function()
		if lastErr != nil {
			logErrorWithId(ew.logId,
				"failed to execute [%s], err = %v, retry-[%v]th later", funcDesc, lastErr, i+1)
			time.Sleep(interval)
		} else {
			return nil
		}
	}
	return lastErr
}

func (ew *EtcdWrapper) parsePeerFromKv(kv *mvccpb.KeyValue) (string, *utils.Peer, error) {
	key := string(kv.Key)
	peerName := path.Base(key)
	var peer utils.Peer
	e := utils.SonicApi.Unmarshal(kv.Value, &peer)
	if e != nil {
		return peerName, nil, fmt.Errorf("failed to unmarshal peer %s registered in etcd, err = %v", peerName, e)
	} else {
		e = cluster.CheckPeerValid(&peer)
		if e != nil {
			return peerName, nil, fmt.Errorf("peer %s is invalid, %v", peerName, e)
		}
	}
	return peerName, &peer, nil
}

func (ew *EtcdWrapper) parseInstanceFromKv(kv *mvccpb.KeyValue) (string, *config.Route, error) {
	key := string(kv.Key)
	instanceId := path.Base(key)
	var instance *config.Route
	err := utils.SonicApi.Unmarshal(kv.Value, &instance)
	if err != nil {
		return instanceId, nil, fmt.Errorf(
			"failed to unmarshal instance %s registered in etcd, err = %v", instanceId, err)
	}
	err = config.CheckInstanceValid(instance)
	if err != nil {
		return instanceId, nil, fmt.Errorf("instance %s is invalid, %v", instanceId, err)
	}
	return instanceId, instance, nil
}

func (ew *EtcdWrapper) parsePeerEtcdEvents(events []*clientv3.Event,
	state *cluster.ClusterState, logStage string) ([]*utils.Peer, []string) {
	addPeers := make([]*utils.Peer, 0)
	removePeerIds := make([]string, 0)
	for _, ev := range events {
		switch ev.Type {
		case mvccpb.PUT:
			peerName, peer, err := ew.parsePeerFromKv(ev.Kv)
			if err != nil {
				logErrorWithId(ew.logId, "peer event at [%s] failed to parse peer [%s] from event, %v",
					logStage, peerName, err)
				continue
			}
			if !isPeerInState(state, peer.Name, peer) {
				addPeers = append(addPeers, peer)
			}
		case mvccpb.DELETE:
			key := string(ev.Kv.Key)
			peerName := path.Base(key)
			if isPeerInState(state, peerName, nil) {
				removePeerIds = append(removePeerIds, peerName)
			}
		default:
			logErrorWithId(ew.logId, "unknown event type [%v]", ev.Type)
		}
	}
	return addPeers, removePeerIds
}

func (ew *EtcdWrapper) parseInstanceEvents(events []*clientv3.Event,
	state *cluster.ClusterState) (map[string]*config.Route, []string) {
	addInstances := make(map[string]*config.Route)
	removeInstances := make([]string, 0)
	for _, ev := range events {
		key := string(ev.Kv.Key)
		instanceId := path.Base(key)
		switch ev.Type {
		case mvccpb.PUT:
			var instance *config.Route
			err := utils.SonicApi.Unmarshal(ev.Kv.Value, &instance)
			if err != nil {
				logErrorWithId(ew.logId, "failed to unmarshal peer %s registered in etcd, err = %v", instanceId, err)
				continue
			}
			err = config.CheckInstanceValid(instance)
			if err != nil {
				logErrorWithId(ew.logId, "instance %s is invalid, %v", instanceId, err)
			}
			if !isInstanceInState(state, instanceId, instance) {
				addInstances[instanceId] = instance
			}
		case mvccpb.DELETE:
			if isInstanceInState(state, instanceId, nil) {
				removeInstances = append(removeInstances, instanceId)
			}
		default:
			logErrorWithId(ew.logId, "unknown event type [%v]", ev.Type)
		}
	}
	return addInstances, removeInstances
}

func (ew *EtcdWrapper) writePeerToEtcd(dir string, peer *utils.Peer, withLease bool) error {
	if peer == nil {
		return nil
	}

	if !ew.CheckConnection() {
		return fmt.Errorf("failed to write peer to etcd: disconnected")
	}

	key := fmt.Sprintf("%s%s", dir, peer.Name)
	marshal, err := utils.SonicApi.Marshal(peer)
	if err != nil {
		return fmt.Errorf("failed to marshal peer %v, err = %v", peer, err)
	}
	if withLease {
		err = ew.writeWithLease(key, string(marshal))
	} else {
		err = ew.safeWriteKvIfLeader(key, string(marshal))
	}
	if err != nil {
		logErrorWithId(ew.logId, "failed to write peer [%v] to dir [%s], err = %v", peer, dir, err)
		return err
	}
	if ew.cfg.Verbose {
		logInfoWithId(ew.logId, "successfully write peer [%s] to state at [%s]", peer.Name, key)
	}
	return nil
}

func (ew *EtcdWrapper) isPeerRegistered(peerName string) (bool, error) {
	if peerName == "" {
		return false, nil
	}

	if !ew.CheckConnection() {
		return false, fmt.Errorf("failed to read peer from etcd: disconnected")
	}

	key := fmt.Sprintf("%s%s", RSPeerRegistrationDir, peerName)
	res, err := ew.etcdClient.Get(context.Background(), key)
	if err != nil {
		return false, err
	}
	return res.Count > 0, nil
}

func (ew *EtcdWrapper) isInitialized() (bool, error) {
	res, err := ew.etcdClient.Get(context.Background(), RSClusterStateInitKey)
	if err != nil {
		return false, err
	}
	return res.Count > 0, nil
}

func (ew *EtcdWrapper) initialState(initRoute *config.RouteTable) error {
	if !ew.isLeader() {
		return fmt.Errorf("not leader")
	}
	var routes map[string]config.Route
	if initRoute != nil {
		routes = initRoute.InstanceRoutes
	} else {
		routes = make(map[string]config.Route)
	}
	ops := make([]clientv3.Op, 0, len(routes)+1)
	ops = append(ops, clientv3.OpPut(RSClusterStateInitKey, time.Now().Format(time.RFC3339)))
	if len(routes) > 0 {
		for name, instance := range routes {
			key := fmt.Sprintf("%s%s", RSClusterStateAllInstancesDir, name)
			bytes, err := utils.SonicApi.Marshal(instance)
			if err != nil {
				return fmt.Errorf("marshal instance [%s] failed: %v", name, err)
			}
			ops = append(ops, clientv3.OpPut(key, string(bytes)))
		}
	}

	txnRes, err := ew.safeWriteOpsIfLeader(ops)
	if err != nil {
		return fmt.Errorf(
			"failed to initialize config instances to state in transaction for instances [%s], err = %v",
			utils.MapKeysStr(routes), err)
	}
	if !txnRes.Succeeded {
		return fmt.Errorf(
			"failed to commit transaction to initialize config instances for instances [%s], not leader anymore",
			utils.MapKeysStr(routes))
	}
	if ew.cfg.Verbose {
		logInfoWithId(ew.logId, "success to write initialize config instances [%s] to state", utils.MapKeysStr(routes))
	}
	return nil
}

func isPeerInState(state *cluster.ClusterState, name string, peer *utils.Peer) bool {
	if state == nil || state.Peers == nil || name == "" {
		return false
	}
	oriPeer, exists := state.Peers[name]
	if !exists || peer == nil {
		return exists
	}
	return reflect.DeepEqual(oriPeer, peer)
}

func isInstanceInState(state *cluster.ClusterState, name string, route *config.Route) bool {
	if state == nil || state.Peers == nil || name == "" {
		return false
	}
	oriInstance, exists := state.InstanceRoutes[name]
	if !exists || route == nil {
		return exists
	}
	return reflect.DeepEqual(route, oriInstance)
}

func (ew *EtcdWrapper) writeWithLease(key, value string) error {
	_, err := ew.etcdClient.Put(context.Background(), key, value, clientv3.WithLease(ew.sessionWithLease.Lease()))
	return err
}

func (ew *EtcdWrapper) registerToEtcd() error {
	if ew.localNode == nil {
		return fmt.Errorf("peer is nil when register itself")
	}
	err := ew.writePeerToEtcd(RSPeerRegistrationDir, ew.localNode, true)
	return err
}

func (ew *EtcdWrapper) writeInstancesToEtcd(instances map[string]*config.Route) error {
	if !ew.isLeader() {
		return fmt.Errorf("not leader")
	}
	ops := make([]clientv3.Op, 0, len(instances))
	for name, instance := range instances {
		key := fmt.Sprintf("%s%s", RSClusterStateAllInstancesDir, name)
		bytes, err := utils.SonicApi.Marshal(instance)
		if err != nil {
			return fmt.Errorf("marshal instance [%s] failed: %v", name, err)
		}
		ops = append(ops, clientv3.OpPut(key, string(bytes)))
	}

	txnRes, err := ew.safeWriteOpsIfLeader(ops)
	if err != nil {
		return fmt.Errorf(
			"failed to commit batch instances to state in transaction for instances [%s], err = %v",
			utils.MapKeysStr(instances), err)
	}
	if !txnRes.Succeeded {
		return fmt.Errorf(
			"failed to commit batch instances to state in transaction for instances [%s], err = %v",
			utils.MapKeysStr(instances), err)
	}
	if ew.cfg.Verbose {
		logInfoWithId(ew.logId, "success to write instances [%s] to state", utils.MapKeysStr(instances))
	}
	return nil
}

func (ew *EtcdWrapper) deleteKeysFromEtcd(keys []string) error {
	if !ew.isLeader() {
		return fmt.Errorf("not leader")
	}
	if !ew.CheckConnection() {
		return fmt.Errorf("failed to write peer to etcd: disconnected")
	}
	ops := make([]clientv3.Op, 0, len(keys))
	for _, key := range keys {
		ops = append(ops, clientv3.OpDelete(key))
	}
	txnRes, err := ew.safeWriteOpsIfLeader(ops)
	if err != nil {
		return fmt.Errorf(
			"failed to commit batch instances to state in transaction for keys [%s], err = %v",
			keys, err)
	}
	if !txnRes.Succeeded {
		return fmt.Errorf(
			"failed to commit batch instances to state in transaction for keys [%s], not leader anymore, err = %v",
			keys, err)
	}
	return nil
}

func (ew *EtcdWrapper) safeDeleteFromEtcdIfLeader(key string) error {
	if !ew.isLeader() {
		return fmt.Errorf("not leader")
	}
	if !ew.CheckConnection() {
		return fmt.Errorf("failed to write peer to etcd: disconnected")
	}
	res, err := ew.safeWriteOpsIfLeader([]clientv3.Op{clientv3.OpDelete(key)})
	if err != nil {
		return err
	}
	if !res.Succeeded {
		return fmt.Errorf("failed to delete key [%s] in etcd. not leader anymore", key)
	}
	if ew.cfg.Verbose {
		logInfoWithId(ew.logId, "remote state for key %s", key)
	}
	return err
}

func (ew *EtcdWrapper) safeWriteKvIfLeader(key, value string) error {
	if !ew.isLeader() {
		return fmt.Errorf("not leader")
	}
	if !ew.CheckConnection() {
		return fmt.Errorf("failed to write peer to etcd: disconnected")
	}
	txn := ew.etcdClient.Txn(context.Background())
	leaderKey := ew.election.Key()
	txn.If(
		clientv3.Compare(clientv3.Value(leaderKey), "=", ew.localNode.Name)).
		Then(clientv3.OpPut(key, value))
	res, err := txn.Commit()
	if err != nil {
		return err
	}
	if !res.Succeeded {
		return fmt.Errorf("transaction to write [%s] is failed, not leader anymore", key)
	}
	return nil
}

func (ew *EtcdWrapper) safeWriteOpsIfLeader(ops []clientv3.Op) (*clientv3.TxnResponse, error) {
	if !ew.isLeader() {
		return nil, fmt.Errorf("not leader")
	}
	if !ew.CheckConnection() {
		return nil, fmt.Errorf("failed to write peer to etcd: disconnected")
	}
	txn := ew.etcdClient.Txn(context.Background())
	leaderKey := ew.election.Key()
	txn.If(
		clientv3.Compare(clientv3.Value(leaderKey), "=", ew.localNode.Name)).Then(ops...)
	return txn.Commit()
}

func (ew *EtcdWrapper) leaseKeeper() {
	ew.wg.Add(1)
	defer ew.wg.Done()

	refreshInterval := time.Duration(ew.cfg.EtcdCfg.LeaseRefreshIntervalMs) * time.Millisecond
	ticker := time.NewTicker(refreshInterval)
	leaseReqTimeout := time.Duration((ew.cfg.EtcdCfg.LeaseTtlSec*1000)/3) * time.Millisecond
	defer ticker.Stop()

	consecutiveFailures := 0

	for {
		select {
		case <-ew.closeChan:
			logInfoWithId(ew.logId, "stop to keep lease")
			ew.status.Store(StatusDisconnected)
			return
		case <-ticker.C:
			if ew.status.Load() != StatusConnected {
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), leaseReqTimeout)
			_, err := ew.etcdClient.KeepAliveOnce(ctx, ew.sessionWithLease.Lease())
			cancel()

			if err != nil {
				consecutiveFailures++
				if consecutiveFailures >= LeaseKeepAliveMaxRetry {
					logErrorWithId(ew.logId, "etcd keep alive failed after %v retry, entering disconnected mode: %v",
						consecutiveFailures, err)
					ew.status.Store(StatusDisconnected)
				} else {
					logWarnWithId(ew.logId, "etcd keep alive failed: %v, retry %v", err, consecutiveFailures)
					metrics.RecordETCDConnectionsRetries()
				}
			} else {
				consecutiveFailures = 0
			}
		case <-ew.sessionWithLease.Done():
			if ew.status.Load() != StatusConnected {
				return
			}
			logErrorWithId(ew.logId, "etcd session with lease is Done, entering disconnected mode")
			ew.status.Store(StatusDisconnected)
			return
		}
	}
}

func (ew *EtcdWrapper) shouldResumeInstanceFailures(failures *utils.AllInstanceFailuresSnapshot) (shouldResume bool, reason string) {
	if failures == nil {
		return false, "no failures"
	}
	if failures.LeaderId == "" {
		return false, "no failures is recorded yet"
	}
	if failures.LeaderId != ew.localNode.Name {
		return true, "leader changes"
	}
	if ew.lastFailureSnapshotRound == 0 {
		return true, "leader not changed, but may restarted"
	}
	return false, "leader not changed"
}

func logInfoWithId(wrapperId int, format string, args ...interface{}) {
	logger.Log().Infof(fmt.Sprintf(WrapperLogFormat, wrapperId, format), args...)
}

func logWarnWithId(wrapperId int, format string, args ...interface{}) {
	logger.Log().Warnf(fmt.Sprintf(WrapperLogFormat, wrapperId, format), args...)
}

func logErrorWithId(wrapperId int, format string, args ...interface{}) {
	logger.Log().Errorf(fmt.Sprintf(WrapperLogFormat, wrapperId, format), args...)
}

func logDebugWithId(wrapperId int, format string, args ...interface{}) {
	logger.Log().Debugf(fmt.Sprintf(WrapperLogFormat, wrapperId, format), args...)
}

func logTraceWithId(wrapperId int, format string, args ...interface{}) {
	logger.Log().Tracef(fmt.Sprintf(WrapperLogFormat, wrapperId, format), args...)
}

// ---------- failure states --------

func (ew *EtcdWrapper) parseAllInstanceFailuresFromKv(kv *mvccpb.KeyValue) (*utils.AllInstanceFailuresSnapshot, error) {
	key := string(kv.Key)
	var allInstanceFailuresSnapshot utils.AllInstanceFailuresSnapshot
	e := utils.SonicApi.Unmarshal(kv.Value, &allInstanceFailuresSnapshot)
	if e != nil {
		return nil, fmt.Errorf("failed to unmarshal allInstanceFailuresSnapshot %s in etcd, err = %v", key, e)
	} else {
		return &allInstanceFailuresSnapshot, nil
	}
}

func (ew *EtcdWrapper) updateHealthStatsCache(cur *utils.AllInstanceFailuresSnapshot, allInstances map[string]*config.Route, iter int) error {
	everyRoleAvailableCountMap := make(map[string]int)
	if cur == nil || len(cur.Failures) == 0 {
		for _, instance := range allInstances {
			everyRoleAvailableCountMap[instance.Role]++
		}
		if err := ew.failureChangedFunc(
			cluster.EtcdHealthStatsCache{
				UpdateTime:                 time.Now(),
				FailureInstanceDetail:      nil,
				EveryRoleAvailableCountMap: everyRoleAvailableCountMap,
			}); err != nil {
			logger.Log().Errorf("fail to update health stats cache for etcd instance [%v]", err.Error())
			return err
		}
		logInfoWithId(ew.logId, "failure_ins_change_iter [%v] completed, healthy counts: %v, failureInstance : %v",
			iter, everyRoleAvailableCountMap, nil)
	} else {
		for instId, instance := range allInstances {
			if _, exists := cur.Failures[instId]; exists {
				continue
			}
			everyRoleAvailableCountMap[instance.Role]++
		}

		snapshotTime, err := time.Parse(time.RFC3339, cur.SnapshotTime)
		if err != nil {
			logger.Log().Errorf("fail to parse snapshot time [%v]", cur.SnapshotTime)
			return err
		}
		roleGroupedFailures := make(map[string]map[string]*utils.InstanceFailureRecord)
		for id, record := range cur.Failures {
			role := record.Role
			if _, exists := roleGroupedFailures[role]; !exists {
				roleGroupedFailures[role] = make(map[string]*utils.InstanceFailureRecord)
			}
			roleGroupedFailures[role][id] = record
		}
		err = ew.failureChangedFunc(
			cluster.EtcdHealthStatsCache{
				UpdateTime:                 snapshotTime,
				FailureInstanceDetail:      roleGroupedFailures,
				EveryRoleAvailableCountMap: everyRoleAvailableCountMap,
			})
		if err != nil {
			logger.Log().Errorf("fail to update health stats cache for etcd instance [%v]", err.Error())
			return err
		}
		logInfoWithId(ew.logId, "failure_ins_change_iter [%v] completed, healthy counts: %v, failureInstance : %v",
			iter, everyRoleAvailableCountMap, roleGroupedFailures)
	}
	return nil
}
func (ew *EtcdWrapper) handleFailureInstanceChanged(events []*clientv3.Event, iter int) {
	logDebugWithId(ew.logId, "failure_ins_change_iter [%v], start to handle failure instance changes events", iter)

	// retrieve all instances from the cluster cache
	var allInstances map[string]*config.Route
	allInstances = make(map[string]*config.Route)
	ew.stateChangeLock.RLock()
	state := ew.latestStateFunc()
	ew.stateChangeLock.RUnlock()
	if state == nil {
		logErrorWithId(ew.logId, "cluster state is nil, using empty instance routes")
	} else {
		logInfoWithId(ew.logId, "cluster state is %v", state)
		allInstances = state.InstanceRoutes
	}

	for _, ev := range events {
		switch ev.Type {
		case mvccpb.PUT:
			allInstanceFailuresSnapshotPtr, parseErr := ew.parseAllInstanceFailuresFromKv(ev.Kv)
			if parseErr != nil || allInstanceFailuresSnapshotPtr == nil {
				logErrorWithId(ew.logId, "failure instance event failed to parse json from event, %v",
					parseErr)
				continue
			}

			if err := ew.updateHealthStatsCache(allInstanceFailuresSnapshotPtr, allInstances, iter); err != nil {
				logErrorWithId(ew.logId, "updateHealthStatsCache failed, %v", err)
				continue
			}
		case mvccpb.DELETE:
			if err := ew.updateHealthStatsCache(nil, allInstances, iter); err != nil {
				logErrorWithId(ew.logId, "updateHealthStatsCache failed, %v", err)
				continue
			}
		default:
			logErrorWithId(ew.logId, "unknown event type [%v]", ev.Type)
		}
	}
}
