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

package main

import (
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"infer-dp-scheduler/cmd/mock_server/server"
)

var (
	addr           string
	port           string
	ttft           uint
	tpot           uint
	reportInterval int
	reportDir      string
	name           string
	isEncoder      bool
	isHybrid       bool
	isError        bool
	isHang         bool
	isLinkDown     bool
	isHealth       bool
	enableHTTPS    bool
	caCertFile     string
	certFile       string
	keyFile        string
)

func main() {
	flag.StringVar(&addr, "addr", "0.0.0.0", "server listen address")
	flag.StringVar(&port, "port", "8080", "server listen port")
	flag.UintVar(&ttft, "ttft", 1, "first token delay")
	flag.UintVar(&tpot, "tpot", 1, "non-first token delay")
	flag.StringVar(&name, "name", "client_unknown", "name of the client")
	flag.IntVar(&reportInterval, "report_interval", 10, "interval to report receive status")
	flag.StringVar(&reportDir, "report_file", "/var/log/mock_handler.log", "path to report log file")
	flag.BoolVar(&isEncoder, "isEncoder", false, "indicates if the instance is Encoder")
	flag.BoolVar(&isHybrid, "isHybrid", false, "indicates if the instance is Prefill and Decode")
	flag.BoolVar(&isError, "isError", false, "indicates if the instance is error(faulty status)")
	flag.BoolVar(&isHang, "isHang", false, "indicates if the instance is \"hang\"(faulty status)")
	flag.BoolVar(&isLinkDown, "isLinkDown", false, "indicates if the instance is \"link down\"(faulty status)")
	flag.BoolVar(&isHealth, "isHealth", true, "indicates if the instance is healthy")
	flag.BoolVar(&enableHTTPS, "enable_https", false, "enable HTTPS server (mutually exclusive with HTTP)")
	flag.StringVar(&caCertFile, "ca_cert", "", "CA certificate file path (required for HTTPS)")
	flag.StringVar(&certFile, "cert", "", "server certificate file path (required for HTTPS)")
	flag.StringVar(&keyFile, "key", "", "server private key file path (required for HTTPS)")
	flag.Parse()

	infer := &server.InferPara{
		TTFT:           ttft,
		TPOT:           tpot,
		IsError:        isError,
		IsHang:         isHang,
		IsLinkDown:     isLinkDown,
		IsHealth:       isHealth,
		Collector:      server.NewMetricsCollector(),
		Addr:           addr,
		Port:           port,
		LinkDownList:   make(map[string]struct{}),
		IsEncoder:      isEncoder,
		IsHybrid:       isHybrid,
		HangRequestIds: make(map[string]struct{}),
	}

	engine := gin.New()
	engine.Use(gin.Logger())
	engine.GET("/health", infer.Health)
	engine.GET("/mock_health", infer.MockHealth)
	engine.GET("/mock_unhealth", infer.MockUnHealth)
	engine.POST("/v1/completions", infer.CompletionsInfer)
	engine.POST("/v1/chat/completions", infer.ChatCompletionsInfer)
	engine.GET("/metrics", infer.Collector.HandleMetricsRequest)
	engine.POST("/metrics/update", infer.Collector.UpdateMetricsValue)
	engine.GET("/mock_error", infer.MockErrorInstanceHandler)
	engine.GET("/remove_error", infer.RemoveErrorInstanceHandler)
	engine.GET("/hang", infer.HangInstanceHandler)
	engine.GET("/unhang", infer.UnHangErrorInstanceHandler)
	engine.GET("/hang_request", infer.HangSpecificRequestHandler)
	engine.GET("/unhang_request", infer.UnHangSpecificRequestHandler)
	engine.GET("/v1/models", infer.ModelNameHandler)
	engine.POST("/linkdown", infer.LinkDownHandler)
	engine.GET("/linkup", infer.LinkUpHandler)

	var err error
	if enableHTTPS {
		// Validate HTTPS parameters
		if caCertFile == "" || certFile == "" || keyFile == "" {
			fmt.Printf("HTTPS enabled but missing required certificates: ca_cert, cert, and key must all be provided\n")
			return
		}

		// Validate TLS files
		tlsConfig := &server.TLSConfig{
			CACertFile: caCertFile,
			CertFile:   certFile,
			KeyFile:    keyFile,
		}

		if err := server.ValidateTLSFiles(tlsConfig); err != nil {
			fmt.Printf("TLS certificate validation failed: %v\n", err)
			return
		}

		// Load TLS configuration
		config, err := server.LoadTLSConfig(tlsConfig)
		if err != nil {
			fmt.Printf("Failed to load TLS configuration: %v\n", err)
			return
		}

		httpServer := &http.Server{
			Addr:         fmt.Sprintf("%s:%s", addr, port),
			Handler:      engine,
			TLSConfig:    config,
			ReadTimeout:  time.Second * 3600,
			WriteTimeout: time.Second * 3600,
		}

		fmt.Println("start HTTPS server, listen and serve at ", fmt.Sprintf("%s:%s", addr, port))
		err = httpServer.ListenAndServeTLS(certFile, keyFile) // 使用文件路径，TLSConfig会覆盖
	} else {
		httpServer := &http.Server{
			Addr:         fmt.Sprintf("%s:%s", addr, port),
			Handler:      engine,
			ReadTimeout:  time.Second * 3600,
			WriteTimeout: time.Second * 3600,
		}
		fmt.Println("start HTTP server, listen and serve at ", fmt.Sprintf("%s:%s", addr, port))
		err = httpServer.ListenAndServe()
	}

	if err != nil {
		fmt.Printf("mock engine listen error: %v\n", err)
		return
	}
}
