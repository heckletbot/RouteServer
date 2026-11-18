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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
)

// TLSConfig contains TLS configuration for HTTPS server
type TLSConfig struct {
	CACertFile string
	CertFile   string
	KeyFile    string
}

// LoadTLSConfig loads TLS certificate and CA configuration
func LoadTLSConfig(config *TLSConfig) (*tls.Config, error) {
	if config.CACertFile == "" || config.CertFile == "" || config.KeyFile == "" {
		return nil, fmt.Errorf("missing required TLS certificate files")
	}

	// Load server certificate and private key
	cert, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load server certificate and key: %v", err)
	}

	// Load CA certificate
	caCert, err := ioutil.ReadFile(config.CACertFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA certificate: %v", err)
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	// Create TLS configuration
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caCertPool,
		ClientAuth:   tls.VerifyClientCertIfGiven, // Optional client certificate verification
		MinVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		},
	}

	return tlsConfig, nil
}

// ValidateTLSFiles validates TLS certificate files exist and are readable
func ValidateTLSFiles(config *TLSConfig) error {
	if config == nil {
		return fmt.Errorf("TLS config is nil")
	}

	files := map[string]string{
		"CA certificate":     config.CACertFile,
		"server certificate": config.CertFile,
		"private key":        config.KeyFile,
	}

	for name, file := range files {
		if file == "" {
			return fmt.Errorf("%s file path is empty", name)
		}

		data, err := ioutil.ReadFile(file)
		if err != nil {
			return fmt.Errorf("failed to read %s file '%s': %v", name, file, err)
		}

		if len(data) == 0 {
			return fmt.Errorf("%s file '%s' is empty", name, file)
		}

		// Simple check if it is valid PEM format
		if data[0] != '-' {
			return fmt.Errorf("%s file '%s' does not appear to be in PEM format", name, file)
		}
	}

	return nil
}
