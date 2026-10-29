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
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	commoncfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/errs"
	utils2 "infer-dp-scheduler/pkg/common/infer"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
	"infer-dp-scheduler/pkg/route-server/handler/pipeline"
	"infer-dp-scheduler/pkg/route-server/metrics"
	"infer-dp-scheduler/pkg/route-server/middleware"
)

var server *http.Server
var httpsServer *http.Server
var opsServer *http.Server
var version = "Unknown"
var tagOrBranch = "Unknown"
var commitId = "Unknown"
var buildTime = "Unknown"

func RunServerLoop(cfg *commoncfg.Config, managers *Managers) error {
	adminHandler := managers.adminHandler
	handlers := managers.pdInferHandlers
	instanceManager := managers.instanceManager

	engine := gin.New()

	// Apply request body size limit middleware
	bodyLimitMB := int64(cfg.ServerConfig.MaxRequestBodySizeMB)
	engine.Use(middleware.RequestBodyLimit(bodyLimitMB * 1024 * 1024))

	// Apply rate limiting middleware if configured
	if cfg.ServerConfig.RateLimitCfg != nil && cfg.ServerConfig.RateLimitCfg.NonReferenceAPIs != nil && cfg.ServerConfig.RateLimitCfg.NonReferenceAPIs.Enabled {
		middleware.InitRateLimiter(cfg.ServerConfig.RateLimitCfg)
		engine.Use(middleware.RateLimitMiddleware(cfg.ServerConfig.RateLimitCfg))
		logger.Log().Infof("Rate limiting enabled for non-reference APIs: %d RPM",
			cfg.ServerConfig.RateLimitCfg.NonReferenceAPIs.RequestsPerMinute)
	}

	err := registerInferEndPoints(cfg, handlers, engine)
	if err != nil {
		return err
	}
	engine.POST("/instance_requests", instanceManager.HandleBroadcastInstanceRequest)
	engine.GET("/", func(c *gin.Context) {
		printVersion(c, cfg.ServerConfig.Name, string(utils.ServicePort))
	})
	engine.GET(utils.InternalMetricsPath, metrics.StartInternalMetrics)

	scheduleHandler := RemoteScheduleHandler{
		manager:        managers.schedulerManager,
		tracerCfg:      cfg.ServerConfig.TraceCfg,
		clusterService: managers.clusterService,
		selfName:       managers.clusterService.LocalNode().Name,
		requiredRoles:  cfg.InstanceManagerCfg.RequiredRoles,
	}

	leaderApi := engine.Group("")
	leaderApi.Use(adminHandler.adminLeaderProxyMiddleware())
	{
		leaderApi.GET(utils.StatusPath, func(c *gin.Context) {
			instanceManager.GetInstanceStatus(c)
		})
		leaderApi.GET(utils.HealthPath, instanceManager.HandleHealthCheck)
		leaderApi.GET(utils.ReportLoadPath, scheduleHandler.ReportLoads)
		leaderApi.GET(utils.LinkBlackListPath, managers.pdLinkBlacklistManager.HandleGetAll)
		leaderApi.GET(utils.ConsistentStatsPath, adminHandler.handleGetConsistentStats)
		leaderApi.GET(utils.MetricsPath, func(c *gin.Context) {
			managers.metricsCollector.HandleMetricsRequest(c)
		})
	}

	if cfg.ServerConfig.EnableOpsAPIsOnServicePort {
		registerOpsDirectAPIs(engine, adminHandler, &scheduleHandler, managers)
		registerOpsLeaderAPIs(leaderApi, adminHandler)
		logger.Log().Infof("Ops APIs are also registered on service port")
	} else {
		leaderApi.GET(utils.Leader, adminHandler.handleGetLeader)
		leaderApi.GET(utils.LocalState, adminHandler.handleGetLocalState)
	}

	probeHandler := managers.probeHandler
	engine.GET("/internal/liveness", probeHandler.LivenessCheck)
	engine.GET("/internal/leader/readiness", probeHandler.LeaderReadinessCheck)
	engine.GET("/internal/worker/readiness", probeHandler.WorkerReadinessCheck)
	engine.GET("/worker/readiness", probeHandler.WorkerReadinessCheck)

	// Ops port routing (opsPort), listens separately
	opsEngine := gin.New()
	opsEngine.Use(middleware.RequestBodyLimit(bodyLimitMB * 1024 * 1024))

	// Apply rate limiting middleware in ops if configured
	if cfg.ServerConfig.RateLimitCfg != nil && cfg.ServerConfig.RateLimitCfg.NonReferenceAPIs != nil && cfg.ServerConfig.RateLimitCfg.NonReferenceAPIs.Enabled {
		opsEngine.Use(middleware.RateLimitMiddleware(cfg.ServerConfig.RateLimitCfg))
		logger.Log().Infof("Rate limiting enabled in Ops for non-reference APIs: %d RPM",
			cfg.ServerConfig.RateLimitCfg.NonReferenceAPIs.RequestsPerMinute)
	}

	opsEngine.GET("/", func(c *gin.Context) {
		printVersion(c, cfg.ServerConfig.Name, string(utils.OpsPort))
	})
	registerOpsDirectAPIs(opsEngine, adminHandler, &scheduleHandler, managers)

	opsLeaderApi := opsEngine.Group("")
	opsLeaderApi.Use(adminHandler.adminLeaderProxyMiddleware())
	registerOpsLeaderAPIs(opsLeaderApi, adminHandler)

	// Add http path before this line! in case there is path conflict in BroadCastURIWhitelist
	err = registerBroadCastURIWhitelist(engine, managers)
	if err != nil {
		return err
	}

	engine.NoMethod(methodNotAllowed)
	engine.NoRoute(noRouteHandler)

	if cfg.ServerConfig.OpsPort == cfg.ServerConfig.HttpPort {
		return fmt.Errorf("ops-port [%s] conflicts with http-port [%s]", cfg.ServerConfig.OpsPort, cfg.ServerConfig.HttpPort)
	}

	err = metrics.StartMetric()
	if err != nil {
		return err
	}

	if err := startMainServer(cfg, engine); err != nil {
		return err
	}

	if err := startOpsServer(cfg, opsEngine); err != nil {
		return err
	}

	// If server is configured with TLS, start with TLS listener; otherwise use plain HTTP
	if server.TLSConfig != nil {
		addr := fmt.Sprintf("%s:%s", cfg.ServerConfig.Addr, cfg.ServerConfig.HttpPort)
		tcpListener, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("main server listen on %s failed: %v", addr, err)
		}
		tlsListener := tls.NewListener(tcpListener, server.TLSConfig)
		err = server.Serve(tlsListener)
	} else {
		err = server.ListenAndServe()
	}

	/* graceful shutdown */
	if err == http.ErrServerClosed {
		return nil
	}

	return err
}

func registerBroadCastURIWhitelist(engine *gin.Engine, managers *Managers) error {
	instanceManager := managers.instanceManager

	routesSet := make(map[string]bool)
	for _, req := range engine.Routes() {
		routesSet[req.Path+req.Method] = true
	}

	for _, req := range instanceManager.GetBroadCastURIWhitelist() {
		for _, method := range req.Methods {
			switch method {
			case http.MethodPost:
				if _, exists := routesSet[req.Path+method]; !exists {
					engine.POST(req.Path, instanceManager.HandleProxyToSingleInstance)
					logger.Log().Infof("Proxy path -X [%s] [%s] is added", method, req.Path)
					routesSet[req.Path+method] = true
				} else {
					return fmt.Errorf("failed to register proxy path -X [%s] [%s] . reason: already registered for internal usage", method, req.Path)
				}
			case http.MethodGet:
				if _, exists := routesSet[req.Path+method]; !exists {
					engine.GET(req.Path, instanceManager.HandleProxyToSingleInstance)
					logger.Log().Infof("Proxy path -X [%s] [%s] is added", method, req.Path)
					routesSet[req.Path+method] = true
				} else {
					return fmt.Errorf("failed to register proxy path -X [%s] [%s] . reason: already registered for internal usage", method, req.Path)
				}
			default:
				logger.Log().Infof("unsupported method[%s] path[%s] ", method, req.Path)

			}
		}
	}

	return nil
}

func registerInferEndPoints(cfg *commoncfg.Config, handlers map[string]utils2.Inference, engine *gin.Engine) error {
	inferModuleType, err := constant.InferModuleTypeFromString(cfg.ServerConfig.InferenceModuleType)
	if err != nil {
		return err
	}
	if inferModuleType == constant.InferTypeV0 {
		handler, exists := handlers[cfg.ServerConfig.InferMode]
		if !exists || handler == nil {
			return fmt.Errorf("infer mode %s is not supported", cfg.ServerConfig.InferMode)
		}
		registerDefaultEndpoints(engine, handler)
	}
	if inferModuleType == constant.InferTypePipeline {
		// pipeline will register api by pipeline config
		for pipelineName, handler := range handlers {
			success, details := registerEndpoints(engine, handler, pipelineName)
			if !success {
				logger.Log().Warnf("pipeline %s register infer module failed: %s, will use default api binding", pipelineName, details)
				registerDefaultEndpoints(engine, handler)
			}
		}
	}
	return nil
}

func registerEndpoints(engine *gin.Engine, handler utils2.Inference, inferMode string) (bool, string) {
	pipelineConfig, err := pipeline.GetPipelineConfig(inferMode)
	if err != nil {
		details := fmt.Sprintf("failed to get pipeline config for mode %s: %v", inferMode, err)
		return false, details
	}

	if len(pipelineConfig.ApiEndpoints) == 0 {
		details := fmt.Sprintf("no api_endpoints configured for mode %s", inferMode)
		return false, details
	}

	for _, endpoint := range pipelineConfig.ApiEndpoints {
		for _, method := range endpoint.Methods {
			registered := utils2.RegisterEndpoint(engine, handler, endpoint.Path, method, endpoint.Handler)
			if !registered {
				details := fmt.Sprintf("failed to register endpoint %s with pipeline method %s", endpoint.Path, endpoint.Handler)
				return false, details
			}
		}
	}

	return true, ""
}

func registerDefaultEndpoints(engine *gin.Engine, handler utils2.Inference) {
	engine.POST("/v1/chat/completions", handler.ChatCompletionsInfer)
	engine.POST("/v1/completions", handler.CompletionsInfer)
}

// startMainServer starts the main server on http-port.
// Default: HTTPS with mTLS enabled.
// If TLS config's EnableHTTPS is false, falls back to plain HTTP.
func startMainServer(cfg *commoncfg.Config, engine *gin.Engine) error {
	tlsCfg := cfg.ServerConfig.TLSConfig

	if tlsCfg == nil {
		return fmt.Errorf("tls config is not configured: server.tls config section is required")
	}

	// Determine whether to enable HTTPS (default true)
	enableHTTPS := tlsCfg.EnableHTTPS

	if !enableHTTPS {
		// Explicitly disable HTTPS, fallback to HTTP
		server = &http.Server{
			Addr:    fmt.Sprintf("%s:%s", cfg.ServerConfig.Addr, cfg.ServerConfig.HttpPort),
			Handler: engine,
		}
		logger.Log().Infof("Start HTTP server on %s:%s (HTTPS disabled by config)",
			cfg.ServerConfig.Addr, cfg.ServerConfig.HttpPort)
		return nil
	}

	// HTTPS mode: certificates must be provided
	if tlsCfg.CertFile == "" || tlsCfg.KeyFile == "" {
		return fmt.Errorf("HTTPS is enabled but TLS certificate is not configured: cert-file and key-file are required")
	}

	tlsConfig, err := buildTLSConfig(tlsCfg, constant.TLSPrivateKeyPasswordEnv)
	if err != nil {
		return fmt.Errorf("failed to build TLS config: %v", err)
	}

	server = &http.Server{
		Addr:      fmt.Sprintf("%s:%s", cfg.ServerConfig.Addr, cfg.ServerConfig.HttpPort),
		Handler:   engine,
		TLSConfig: tlsConfig,
	}

	logger.Log().Infof("Start HTTPS server on %s:%s (mTLS: %v, skip-verify: %v)",
		cfg.ServerConfig.Addr, cfg.ServerConfig.HttpPort,
		tlsCfg.EnableMTLS, tlsCfg.SkipVerify)
	return nil
}

// startOpsServer starts the ops server with HTTPS or HTTP based on configuration.
// Default: HTTPS with mTLS enabled.
// Ops TLS must be configured independently; it does NOT fall back to server.tls.
// This ensures ops port has its own CA for permission isolation.
func startOpsServer(cfg *commoncfg.Config, opsEngine *gin.Engine) error {
	opsTLSConfig := cfg.ServerConfig.OpsTLSConfig

	// Determine whether to enable HTTPS (default true when ops-tls is configured)
	enableHTTPS := false
	if opsTLSConfig != nil {
		enableHTTPS = opsTLSConfig.EnableHTTPS
	}

	if !enableHTTPS {
		// Explicitly disable HTTPS or ops-tls not configured, fallback to HTTP
		opsServer = &http.Server{
			Addr:         fmt.Sprintf("%s:%s", cfg.ServerConfig.Addr, cfg.ServerConfig.OpsPort),
			Handler:      opsEngine,
			ReadTimeout:  time.Second * 3600,
			WriteTimeout: time.Second * 3600,
		}
		logger.Log().Infof("Start OPS HTTP server on %s:%s (HTTPS disabled by config)",
			cfg.ServerConfig.Addr, cfg.ServerConfig.OpsPort)
		go func() {
			if err := opsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Log().Errorf("ops server error: %v", err)
			}
		}()
		return nil
	}

	// HTTPS, cert file is required
	if opsTLSConfig.CertFile == "" || opsTLSConfig.KeyFile == "" {
		return fmt.Errorf("ops HTTPS is enabled but ops-tls certificate is not configured: " +
			"ops-tls cert-file and key-file are required (ops-tls must be independent from server.tls)")
	}

	opsTLS, err := buildTLSConfig(opsTLSConfig, constant.TLSOpsKeyPasswordEnv)
	if err != nil {
		return fmt.Errorf("failed to build ops TLS config: %v", err)
	}

	opsServer = &http.Server{
		Addr:         fmt.Sprintf("%s:%s", cfg.ServerConfig.Addr, cfg.ServerConfig.OpsPort),
		Handler:      opsEngine,
		TLSConfig:    opsTLS,
		ReadTimeout:  time.Second * 3600,
		WriteTimeout: time.Second * 3600,
	}

	logger.Log().Infof("Start OPS HTTPS server on %s:%s (mTLS: %v)",
		cfg.ServerConfig.Addr, cfg.ServerConfig.OpsPort, opsTLSConfig.EnableMTLS)

	go func() {
		addr := fmt.Sprintf("%s:%s", cfg.ServerConfig.Addr, cfg.ServerConfig.OpsPort)
		tcpListener, err := net.Listen("tcp", addr)
		if err != nil {
			logger.Log().Errorf("ops https listen on %s failed: %v", addr, err)
			return
		}
		tlsListener := tls.NewListener(tcpListener, opsServer.TLSConfig)
		if err := opsServer.Serve(tlsListener); err != nil && err != http.ErrServerClosed {
			logger.Log().Errorf("ops server error: %v", err)
		}
	}()
	return nil
}

// loadX509KeyPair loads a TLS certificate and key pair.
// If the corresponding private key password environment variable is set, it attempts to
// decrypt an encrypted PEM-encoded private key before loading.
// Otherwise, it falls back to the standard tls.LoadX509KeyPair.
func loadX509KeyPair(certFile, keyFile, passwordEnv string) (tls.Certificate, error) {
	password := os.Getenv(passwordEnv)
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

// buildTLSConfig builds a tls.Config based on the TLS certificate configuration
// When tlsCfg.EnableMTLS is true, enables mutual TLS (requires CA certificate to verify client certificates)
// When tlsCfg.SkipVerify is true, skips client certificate verification (insecure mode)
// passwordEnv specifies the environment variable name for the private key decryption password.
func buildTLSConfig(tlsCfg *commoncfg.TLSCertificateConfig, passwordEnv string) (*tls.Config, error) {
	// Load server certificate (supports encrypted private key via TLS_PRIVATE_KEY_PASSWORD env)
	cert, err := loadX509KeyPair(tlsCfg.CertFile, tlsCfg.KeyFile, passwordEnv)
	if err != nil {
		return nil, fmt.Errorf("failed to load server cert/key: %v", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		},
	}

	if tlsCfg.SkipVerify {
		tlsConfig.InsecureSkipVerify = true
	}

	if tlsCfg.EnableMTLS {
		if tlsCfg.CaFile == "" {
			return nil, fmt.Errorf("ca-cert-file is required when mTLS is enabled")
		}
		caCert, err := os.ReadFile(tlsCfg.CaFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA cert file %s: %v", tlsCfg.CaFile, err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA cert file %s", tlsCfg.CaFile)
		}
		tlsConfig.ClientCAs = caCertPool
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return tlsConfig, nil
}

func StopServer() {
	logger.Log().Infof("RouteServer shutting down.")

	// Stop rate limiter
	middleware.StopRateLimiter()

	if server != nil {
		if err := server.Shutdown(context.Background()); err != nil {
			logger.Log().Errorf("failed to shutdown server, err = %v", err)
		}
	}
	if httpsServer != nil {
		if err := httpsServer.Shutdown(context.Background()); err != nil {
			logger.Log().Errorf("failed to shutdown https server, err = %v", err)
		}
	}
	if opsServer != nil {
		if err := opsServer.Shutdown(context.Background()); err != nil {
			logger.Log().Errorf("failed to shutdown ops server, err = %v", err)
		}
	}
}

func methodNotAllowed(ctx *gin.Context) {
	ctx.JSON(errs.MethodNotAllowed.Status, errs.MethodNotAllowed.Json())
}

func noRouteHandler(ctx *gin.Context) {
	ctx.JSON(errs.PathNotFound.Status, errs.PathNotFound.Json())
}

type VersionInfo struct {
	SoftwareName    string `json:"name"`
	NodeName        string `json:"node_name"`
	VersionStr      string `json:"version"`
	TagOrBranchStr  string `json:"tag_or_branch"`
	CommitIdStr     string `json:"commit_id"`
	BuildTimeStr    string `json:"build_time"`
	CurrentPortType string `json:"port_type"`
}

func printVersion(ctx *gin.Context, nodeName string, portType string) {
	ctx.JSON(http.StatusOK, VersionInfo{
		SoftwareName:    constant.SoftwareName,
		NodeName:        nodeName,
		VersionStr:      version,
		TagOrBranchStr:  tagOrBranch,
		CommitIdStr:     commitId,
		BuildTimeStr:    buildTime,
		CurrentPortType: portType,
	})
}

func registerOpsDirectAPIs(engine *gin.Engine, adminHandler *AdminHandler, scheduleHandler *RemoteScheduleHandler, managers *Managers) {
	engine.POST("/internal/worker/force-readiness", managers.instanceManager.HandleForceReadiness)
	engine.POST(utils.InternalAbortPath, managers.runningRequestsManager.AbortRequests)
	engine.GET(utils.InternalRunningDetailsPath, managers.runningRequestsManager.HandleRunningDetails)
	engine.POST(utils.ScheduleRequestPath, scheduleHandler.HandleSchedule)
	engine.POST(utils.RequestEventPath, adminHandler.HandleLeaderServingEvents)
}

func registerOpsLeaderAPIs(leaderApi *gin.RouterGroup, adminHandler *AdminHandler) {
	leaderApi.POST(utils.JoinHttpPath, adminHandler.handleAddNode)
	leaderApi.POST(utils.LeaveHttpPath, adminHandler.handleLeaveNode)
	leaderApi.POST(utils.JoinInstancePath, adminHandler.handleAddInstance)
	leaderApi.POST(utils.LeaveInstancePath, adminHandler.handleLeaveInstance)
	leaderApi.POST(utils.AbortRequestPath, adminHandler.handleAbortRequest)
	leaderApi.GET(utils.RunningDetailsPath, adminHandler.handleRunningDetails)
	leaderApi.GET(utils.Leader, adminHandler.handleGetLeader)
	leaderApi.GET(utils.LocalState, adminHandler.handleGetLocalState)
}
