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

package peer_client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	http2 "infer-dp-scheduler/pkg/common/http"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
)

type PeerClient struct {
	PeerHttpClient    *http2.Client
	OpsPeerHttpClient *http2.Client
	PeerCfg           *config.PeerClientConfig
	OpsPeerCfg        *config.PeerClientConfig
	localPeerName     string
}

func (pc *PeerClient) DoRequest(req *http.Request, uri string) (*http.Response, error) {
	clientType := utils.PortTypeForUri(uri)
	if clientType == utils.OpsPort {
		return pc.OpsPeerHttpClient.DoRequest(req)
	}
	return pc.PeerHttpClient.DoRequest(req)
}

func (pc *PeerClient) UseHttps(uri string) bool {
	clientType := utils.PortTypeForUri(uri)
	if clientType == utils.OpsPort {
		if pc.OpsPeerCfg != nil && pc.OpsPeerCfg.HttpCfg != nil && pc.OpsPeerCfg.HttpCfg.TLSConfig != nil {
			return pc.OpsPeerCfg.HttpCfg.TLSConfig.EnableHTTPS
		}
		return false
	} else {
		if pc.PeerCfg != nil && pc.PeerCfg.HttpCfg != nil && pc.PeerCfg.HttpCfg.TLSConfig != nil {
			return pc.PeerCfg.HttpCfg.TLSConfig.EnableHTTPS
		}
		return false
	}
}

func (pc *PeerClient) SendToPeer(ctx context.Context, transferData []byte, targetPeer *utils.Peer, reqUri string, targetMethod string, timeoutMs int) ([]byte, error) {
	if targetPeer == nil {
		return nil, fmt.Errorf("targetPeer is empty")
	}
	var cancel context.CancelFunc
	if timeoutMs > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
		defer cancel()
	}
	var fullUri string
	var scheme string
	targetUri, _ := strings.CutPrefix(reqUri, "/")
	if pc.UseHttps(targetUri) {
		scheme = "https"
	} else {
		scheme = "http"
	}
	fullUri = pc.selectUri(fullUri, scheme, targetPeer, targetUri)
	req, err := http2.BuildHttpRequest(ctx, targetMethod, fullUri, bytes.NewBuffer(transferData), nil)
	if err != nil {
		logger.Log().Errorf("new peer request failed, err = %v", err)
		return nil, fmt.Errorf("new peer request failed, err = %v", err)
	}
	req.Header.Add("X-Forwarded-For", pc.localPeerName)
	res, err := pc.DoRequest(req, reqUri)
	if err != nil {
		logger.Log().Errorf("exec peer request failed, err = %v", err)
		return nil, fmt.Errorf("exec peer request failed, err = %v", err)
	}
	bodyBytes, err := io.ReadAll(res.Body)
	defer func(Body io.ReadCloser) {
		if err := Body.Close(); err != nil {
			logger.Log().Warnf("failed to close resp body for")
		}
	}(res.Body)
	if res.StatusCode != http.StatusOK {
		if err != nil {
			logger.Log().Errorf("exec peer request failed, err = %v, Status Code = %d", err, res.StatusCode)
			return nil, fmt.Errorf("exec peer request failed, err = %v, Status Code = %d", err, res.StatusCode)
		} else {
			logger.Log().Errorf("exec peer request failed, err = %s, Status Code = %d", string(bodyBytes), res.StatusCode)
			return nil, fmt.Errorf("exec peer request failed, err = %s, Status Code = %d", string(bodyBytes), res.StatusCode)
		}
	}
	return bodyBytes, nil
}

func NewPeerClient(localName string, cfg *config.PeerClientConfig, opsCfg *config.PeerClientConfig) (*PeerClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("failed to create peer Client, config is empty")
	}

	client, err := http2.NewClient(cfg.HttpCfg, false, constant.TLSPrivateKeyPasswordEnv)
	if err != nil {
		return nil, fmt.Errorf("failed to create peer Client: %w", err)
	}

	var opsClient *http2.Client
	if opsCfg == nil || opsCfg.HttpCfg == nil {
		opsClient = client
	} else {
		opsClient, err = http2.NewClient(opsCfg.HttpCfg, false, "TLS_OPS_KEY_PASSWORD")
		if err != nil {
			return nil, fmt.Errorf("failed to create ops peer Client: %w", err)
		}
	}

	peerClient := &PeerClient{
		PeerHttpClient:    client,
		OpsPeerHttpClient: opsClient,
		PeerCfg:           cfg,
		OpsPeerCfg:        opsCfg,
		localPeerName:     localName,
	}
	return peerClient, nil
}

func ApplyDefaultPeerClientConfig(cfg *config.Config) error {
	if cfg.PeerClientCfg == nil {
		defaultCfg := &config.PeerClientConfig{
			HttpCfg:        &cfg.HttpConfig,
			SendWithDomain: false,
			Domain:         "",
		}
		cfg.PeerClientCfg = defaultCfg
	} else {
		if cfg.PeerClientCfg.HttpCfg == nil {
			cfg.PeerClientCfg.HttpCfg = &cfg.HttpConfig
		} else {
			config.ApplyDefaultHttpCfg(cfg.PeerClientCfg.HttpCfg)
		}
	}

	// If peer-client TLS is not configured, server.tlsConfig is used by default to ensure communication between peers.
	if cfg.PeerClientCfg.HttpCfg.TLSConfig == nil || cfg.PeerClientCfg.HttpCfg.TLSConfig.CertFile == "" {
		cfg.PeerClientCfg.HttpCfg.TLSConfig = cfg.ServerConfig.TLSConfig
	}

	// If ops-peer-client TLS is not configured, ops.server.tlsConfig is used by default to ensure communication between peers.
	if cfg.OpsPeerClientCfg.HttpCfg.TLSConfig == nil || cfg.OpsPeerClientCfg.HttpCfg.TLSConfig.CertFile == "" {
		cfg.OpsPeerClientCfg.HttpCfg.TLSConfig = cfg.ServerConfig.OpsTLSConfig
	}

	err := checkConfig(cfg.PeerClientCfg)
	return err
}

func checkConfig(cfg *config.PeerClientConfig) error {
	if cfg.SendWithDomain == true {
		if cfg.Domain == "" {
			return fmt.Errorf("Send reqesut to peer with Domain, but Domain is empty")
		}
	}
	return nil
}

func (pc *PeerClient) selectUri(uri string, scheme string, targetPeer *utils.Peer, targetUri string) string {
	if pc.PeerCfg.SendWithDomain {
		uri = fmt.Sprintf("%s://%s:%d/%s", scheme, targetPeer.Addr.Domain, utils.SelectPort(targetPeer, targetUri), targetUri)
	} else {
		uri = fmt.Sprintf("%s://%s:%d/%s", scheme, targetPeer.Addr.Host, utils.SelectPort(targetPeer, targetUri), targetUri)
	}
	return uri
}
