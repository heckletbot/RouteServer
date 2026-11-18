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

package metrics_collector

import (
	"fmt"
	"net/http"

	"route-server/pkg/common/config"
	http2 "route-server/pkg/common/http"
)

// TargetClient describes how to reach one kind of metrics target
type TargetClient struct {
	Transport http.RoundTripper
	scheme    string
}

// NewTargetClient builds the outbound pool used to scrape one kind of metrics
// target, using the same factory as the other outbound clients so it inherits
// their TLS handling and pool limits.
func NewTargetClient(httpCfg *config.HttpConfig, passwordEnv string) (TargetClient, error) {
	if httpCfg == nil {
		return TargetClient{}, nil
	}

	cfg := *httpCfg
	config.ApplyDefaultHttpCfg(&cfg)

	client, err := http2.NewClient(&cfg, true, passwordEnv)
	if err != nil {
		return TargetClient{}, fmt.Errorf("failed to create http client: %w", err)
	}

	scheme := "http"
	if cfg.TLSConfig != nil && cfg.TLSConfig.EnableHTTPS {
		scheme = "https"
	}

	return TargetClient{
		Transport: client.Client.Transport,
		scheme:    scheme,
	}, nil
}
