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

package http

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"infer-dp-scheduler/pkg/common/config"
)

// loadX509KeyPairWithPassword loads the TLS certificate and private key,
// supporting encrypted private keys (with the password passed via an environment variable).
func loadX509KeyPairWithPassword(certFile, keyFile, envName string) (tls.Certificate, error) {
	password := os.Getenv(envName)
	if password == "" {
		return tls.LoadX509KeyPair(certFile, keyFile)
	}

	keyPEMBlock, err := os.ReadFile(keyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to read key file %s: %v", keyFile, err)
	}

	block, _ := pem.Decode(keyPEMBlock)
	if block == nil {
		return tls.Certificate{}, fmt.Errorf("failed to decode PEM block from %s", keyFile)
	}

	if x509.IsEncryptedPEMBlock(block) {
		decryptedDER, err := x509.DecryptPEMBlock(block, []byte(password))
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("failed to decrypt private key: %v", err)
		}
		decryptedKeyPEM := pem.EncodeToMemory(&pem.Block{
			Type:  block.Type,
			Bytes: decryptedDER,
		})
		certPEM, err := os.ReadFile(certFile)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("failed to read cert file %s: %v", certFile, err)
		}
		return tls.X509KeyPair(certPEM, decryptedKeyPEM)
	}

	return tls.LoadX509KeyPair(certFile, keyFile)
}

// BuildTLSConfig constructs a tls.Config based on TLSCertificateConfig (for outbound client connections).
// When EnableMTLS is true, it loads the client certificate for mutual TLS (mTLS) authentication.
// Returns nil when no certificate is configured, indicating TLS is not enabled.
// passwordEnv specifies the name of the environment variable containing the private key decryption password.
func BuildTLSConfig(tlsCfg *config.TLSCertificateConfig, passwordEnv string) (*tls.Config, error) {
	if tlsCfg == nil || !tlsCfg.EnableHTTPS {
		return nil, nil
	}

	hasCert := tlsCfg.CertFile != "" && tlsCfg.KeyFile != ""

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

	if hasCert {
		cert, err := loadX509KeyPairWithPassword(tlsCfg.CertFile, tlsCfg.KeyFile, passwordEnv)
		if err != nil {
			return nil, fmt.Errorf("failed to load client cert/key: %v", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	// load ca to verify target server if ca or mTLS is configured
	if tlsCfg.CaFile != "" || tlsCfg.EnableMTLS {
		caFile := tlsCfg.CaFile
		if caFile == "" && tlsCfg.EnableMTLS {
			// When mTLS is enabled but no CA is available, the system CA pool is used (only the client verifies the server).
			return tlsConfig, nil
		}
		caCert, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA cert file %s: %v", caFile, err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA cert file %s", caFile)
		}
		tlsConfig.RootCAs = caCertPool
	}

	return tlsConfig, nil
}

type Client struct {
	Client *http.Client
}

func NewClient(cfg *config.HttpConfig, isLongLive bool, passwordEnv string) (*Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("http config cannot be nil")
	}

	if cfg.DialerTimeout < 0 || cfg.IdleConnTimeout < 0 {
		return nil, fmt.Errorf("invalid timeout values: DialerTimeout=%d, IdleConnTimeout=%d", cfg.DialerTimeout, cfg.IdleConnTimeout)
	}
	if cfg.MaxIdleConns < 0 || cfg.MaxIdleConnsPerHost < 0 {
		return nil, fmt.Errorf("invalid connection limits: MaxIdleConns=%d, MaxIdleConnsPerHost=%d", cfg.MaxIdleConns, cfg.MaxIdleConnsPerHost)
	}

	// build TLS config
	tlsConfig, err := BuildTLSConfig(cfg.TLSConfig, passwordEnv)
	if err != nil {
		return nil, fmt.Errorf("failed to build TLS config: %v", err)
	}

	if !isLongLive {
		client := &http.Client{
			Timeout: time.Duration(cfg.RequestTimeout) * time.Second,
		}
		if tlsConfig != nil {
			client.Transport = &http.Transport{
				TLSClientConfig: tlsConfig,
			}
		}
		return &Client{Client: client}, nil
	}

	keepaliveEnable := cfg.DialerKeepAlive > 0
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   time.Duration(cfg.DialerTimeout) * time.Second,
			KeepAlive: time.Duration(cfg.DialerKeepAlive) * time.Second,
		}).DialContext,
		DisableKeepAlives:   !keepaliveEnable,
		MaxIdleConns:        cfg.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:     time.Duration(cfg.IdleConnTimeout) * time.Second,
		MaxConnsPerHost:     cfg.MaxConnsPerHost,
	}
	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig
	}
	return &Client{
		Client: &http.Client{
			Timeout:   time.Duration(cfg.RequestTimeout) * time.Second,
			Transport: transport,
		},
	}, nil
}

func (c *Client) DoRequest(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("nil http request")
	}
	res, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func BuildHttpRequest(ctx context.Context, method string, url string,
	body io.Reader, header http.Header) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if header != nil {
		req.Header = header
	}
	return req, nil
}
