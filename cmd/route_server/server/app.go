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
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"strconv"

	"github.com/google/uuid"
	"infer-dp-scheduler/cmd/route_server/server/options"
	monitor_client "infer-dp-scheduler/pkg/common/client/monitor-client"
	peer_client "infer-dp-scheduler/pkg/common/client/peer-client"
	"infer-dp-scheduler/pkg/common/cluster"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/events"
	"infer-dp-scheduler/pkg/common/health"
	utils2 "infer-dp-scheduler/pkg/common/infer"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"
	metrics_collector "infer-dp-scheduler/pkg/metrics-collector"
	metrics_logger "infer-dp-scheduler/pkg/metrics-collector/logger"
	"infer-dp-scheduler/pkg/route-server/apc"
	"infer-dp-scheduler/pkg/route-server/handler"
	"infer-dp-scheduler/pkg/route-server/handler/infer"
	"infer-dp-scheduler/pkg/route-server/handler/pipeline"
	"infer-dp-scheduler/pkg/route-server/instances"
	"infer-dp-scheduler/pkg/route-server/metrics"
	"infer-dp-scheduler/pkg/route-server/scheduler"
)

type App struct {
	managers *Managers
}

type Managers struct {
	WorkerBootId           string
	adminHandler           *AdminHandler
	probeHandler           *ProbeHandler
	metricsCollector       *metrics_collector.Collector
	workerEventCollector   *health.WorkerEventCollector
	runningRequestsManager *handler.RunningRequestsManager
	pdLinkBlacklistManager *health.PDLinkBlacklistManager
	pdInferHandlers        map[string]utils2.Inference
	clusterService         cluster.ClusterService
	workerHealthChecker    *cluster.WorkerHealthChecker
	instanceManager        instances.Manager
	schedulerManager       scheduler.Manager
	messageCenter          utils.MessageCenter
	apcManager             *apc.Manager // New: independent APC manager
}

func (m *Managers) Close() {
	if m.schedulerManager != nil {
		m.schedulerManager.Close()
	}

	if m.workerHealthChecker != nil {
		m.workerHealthChecker.Close()
	}

	if m.instanceManager != nil {
		m.instanceManager.Stop()
	}

	if m.apcManager != nil {
		m.apcManager.Close()
	}

	if m.clusterService != nil {
		m.clusterService.Close()
	}

	if m.messageCenter != nil {
		m.messageCenter.Close()
	}
}

func (a *App) appExit() {
	if a.managers != nil {
		a.managers.Close()
	}
	metrics.StopMetric()
	metrics_collector.StopMetricsCollector()
	logger.Log().Infof("app stopped, exiting")
}

func (a *App) Run() error {
	defer a.appExit()
	loggerInited := false
	defer func() {
		if panicErr := recover(); panicErr != nil {
			if loggerInited {
				logger.Log().Infof("got panic err: %v", panicErr)
			} else {
				fmt.Printf("got panic err: %v", panicErr)
			}
		}
	}()

	err := options.InitConfig()
	if err != nil {
		return fmt.Errorf("failed to init config, err = %v, exiting\n", err)
	}

	if err = a.initLogger(); err != nil {
		return err
	}
	loggerInited = true

	parseFlags()
	if err = applyDefaultConfig(options.GetConfig()); err != nil {
		return fmt.Errorf("failed to apply default config: %v", err)
	}

	if _, err := a.initManagers(); err != nil {
		logger.Log().Fatalf("failed to init managers, %v", err)
		return err
	}

	err = RunServerLoop(options.GetConfig(), a.managers)
	if err != nil {
		logger.Log().Fatalf("failed to init server. %v", err)
		return err
	}
	return nil
}

func (a *App) initManagers() (*Managers, error) {
	managers := &Managers{
		WorkerBootId: uuid.New().String(),
	}
	messageCenter := utils.NewRegularMessageCenter()
	messageCenter.InitTopic(constant.LocalWorkerEventsTopic)
	messageCenter.InitTopic(constant.InstanceDownTopic)

	// init metrics first
	metrics.InitMetrics(&options.GetConfig().MetricsCfg)
	metrics.InitPerfMetrics(&options.GetConfig().MetricsCfg)

	peerClient, err := peer_client.NewPeerClient(options.GetConfig().ServerConfig.Name, options.GetConfig().PeerClientCfg, options.GetConfig().OpsPeerClientCfg)
	if err != nil {
		return managers, fmt.Errorf("fail to create peer client, err = %v", err)
	}
	clusterService, adminHandler, err := newClusterAdminHandler(peerClient)
	if err != nil {
		return managers, fmt.Errorf("fail to create cluster service, err = %v", err)
	}
	workerHealthChecker := cluster.NewWorkerHealthChecker(clusterService)
	adminHandler.WorkerHealthChecker = workerHealthChecker
	if err = messageCenter.Subscribe(constant.InstanceDownTopic, clusterService.GetInstanceCleanHandler()); err != nil {
		return managers, fmt.Errorf("cluster service fail to subscribe instance down topic, err = %v", err)
	}
	managers.workerHealthChecker = workerHealthChecker

	adminHandler.LeaderServingEventHandler = events.NewServingEventsHandler()
	err = messageCenter.Subscribe(constant.LocalWorkerEventsTopic, adminHandler.LeaderServingEventHandler)
	if err != nil {
		return managers, fmt.Errorf("failed when subscribe message center, err = %v", err)
	}

	err = scheduler.InitBuiltinScheduleAlgorithms()
	if err != nil {
		return managers, fmt.Errorf("fail to init builtin schedule algorithms, err = %v", err)
	}

	pdLinkBlacklistManager := health.NewPDLinkBlacklistManager(&options.GetConfig().InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig)
	remoteScheduledRequestManager := scheduler.NewRemoteScheduledRequestManager(options.GetConfig().SchedulersCfg.RemoteSchedule)

	// The APC manager is created before schedulerInit and is independent of the APC scheduler.
	// The APC manager can be enabled even when the APC scheduler is disabled.
	// Asynchronous processing of cache updates does not affect the forwarding process.
	apcManager, err := apc.NewManagerAndStart(&options.GetConfig().APCManagerConfig, clusterService)
	if err != nil {
		return managers, fmt.Errorf("failed to create APC manager: %v", err)
	}

	schedulerManager, err := scheduler.NewManagerWithBootId(options.GetConfig(), managers.WorkerBootId, clusterService, peerClient, pdLinkBlacklistManager, remoteScheduledRequestManager)
	if err != nil {
		return managers, fmt.Errorf("fail to create scheduler manager, err = %v", err)
	}

	logger.Log().Infof("Created monitor client [%s]", options.GetConfig().MonitorCfg.MonitorClientType)
	monitorClient, err := monitor_client.NewClient(&options.GetConfig().MonitorCfg)
	if err != nil {
		return managers, fmt.Errorf("fail to create monitor client, err = %v", err)
	}

	instanceManager, err := instances.NewManager(options.GetConfig(), schedulerManager, monitorClient, messageCenter)
	if err != nil {
		return managers, fmt.Errorf("fail to create instance-manager, err = %v", err)
	}
	adminHandler.SetInstanceManager(instanceManager)
	adminHandler.LeaderServingEventHandler.RegisterListener(instanceManager)
	if stateApplier, ok := instanceManager.(cluster.StateApplier); ok {
		clusterService.RegisterStateApplier(stateApplier)
	}
	logger.Log().Infof("Cluster service registered appliers %v successfully.", clusterService.GetApplierNames())
	workerEventSyncer := health.NewWorkerEventSyncer(options.GetConfig(), peerClient, managers.WorkerBootId, messageCenter, clusterService)
	if stateApplier, ok := workerEventSyncer.(cluster.StateApplier); ok {
		clusterService.RegisterStateApplier(stateApplier)
	}

	// Register APC Manager as StateApplier
	if apcManager != nil {
		logger.Log().Infof("Registering APC StateApplier to cluster service")
		clusterService.RegisterStateApplier(apcManager)
		logger.Log().Infof("Cluster service registered appliers %v successfully.", clusterService.GetApplierNames())
	}

	workerEventCollector := workerEventSyncer.GetWorkerEventCollector()
	runningRequestsManager := handler.NewRunningRequestsManager(options.GetConfig().ServerConfig)

	pdInferHandlers, err := NewInference(instanceManager, schedulerManager, options.GetConfig().ServerConfig, workerEventCollector, runningRequestsManager)
	if err != nil {
		return managers, fmt.Errorf("fail to create inference, err = %v", err)
	}
	instanceManager.InitHealthyModule(pdInferHandlers[options.GetConfig().ServerConfig.InferMode].TestInstance, clusterService, pdLinkBlacklistManager)

	// Inject health stats updater to WorkerEventSyncer
	if syncerImpl, ok := workerEventSyncer.(*health.WorkerEventSyncer); ok {
		if im, ok := instanceManager.(*instances.InstanceManager); ok {
			syncerImpl.SetHealthStatsUpdater(im.GetHealthStatsUpdater())
		}
	}

	err = clusterService.Start()
	if err != nil {
		return managers, fmt.Errorf("fail to start cluster service, err = %v", err)
	}

	probeHandler := NewProbeHandler(clusterService, instanceManager, options.GetConfig())

	managers.adminHandler = adminHandler
	managers.clusterService = clusterService
	managers.messageCenter = messageCenter
	managers.pdLinkBlacklistManager = pdLinkBlacklistManager
	managers.schedulerManager = schedulerManager
	managers.instanceManager = instanceManager
	managers.apcManager = apcManager
	managers.workerEventCollector = workerEventCollector
	managers.runningRequestsManager = runningRequestsManager
	managers.pdInferHandlers = pdInferHandlers
	managers.probeHandler = probeHandler

	a.managers = managers
	// initMetricsCollector need managers assigned in app for now, refactor later
	managers.metricsCollector, err = initMetricsCollector(a, filepath.Join(os.Getenv(constant.EnvServerConfigPath), options.GetConfig().MetricsCfg.MetricsCollectorConfigFileName))
	if err != nil {
		return managers, fmt.Errorf("fail to init metrics collector, err = %v", err)
	}
	return managers, nil
}

func NewInference(instanceMgr instances.Manager, schedulerMgr scheduler.Manager,
	cfg config.ServerConfig, workerEventCollector *health.WorkerEventCollector, runningRequestsManager *handler.RunningRequestsManager) (map[string]utils2.Inference, error) {
	inferModuleType, err := constant.InferModuleTypeFromString(cfg.InferenceModuleType)
	if err != nil {
		return nil, err
	}

	handlers := make(map[string]utils2.Inference)

	switch inferModuleType {
	case constant.InferTypeV0:
		// only create one inferHandler
		inferHandler := infer.NewPDSeparateInfer(instanceMgr, schedulerMgr, cfg, workerEventCollector, runningRequestsManager)
		handlers[cfg.InferMode] = inferHandler
	case constant.InferTypePipeline:
		if err = pipeline.InferInit(); err != nil {
			return nil, fmt.Errorf("failed to init pipeline cfgs, err = %v", err)
		}
		mainInferHandler, err := pipeline.NewPipelineInfer(instanceMgr, schedulerMgr, cfg, workerEventCollector, runningRequestsManager)
		if err != nil {
			return nil, fmt.Errorf("failed to init pipeline %v, err = %v", cfg.InferMode, err)
		}
		handlers[cfg.InferMode] = mainInferHandler

		var subPipelineHandlers []*pipeline.PipelineInfer
		if pipelineConfig, err := pipeline.GetPipelineConfig(cfg.InferMode); err == nil {
			for _, subPipeline := range pipelineConfig.SubPipelines {
				subInferHandler, err := pipeline.NewPipelineInferWithMode(subPipeline, false, instanceMgr, schedulerMgr, cfg, workerEventCollector, runningRequestsManager)
				if err != nil {
					return nil, fmt.Errorf("failed to init pipeline %v, err = %v", subPipeline, err)
				}
				handlers[subPipeline] = subInferHandler
				subPipelineHandlers = append(subPipelineHandlers, subInferHandler)
			}
			mainInferHandler.SetSubPipelines(subPipelineHandlers)
		} else {
			return nil, fmt.Errorf("failed to get pipeline %v connfig, err = %v", cfg.InferMode, err)
		}
	default:
		return nil, fmt.Errorf("invalid inference, module type is %v", inferModuleType)
	}
	return handlers, nil
}

func (a *App) initLogger() error {
	if options.GetConfig().LogConfig.Level == "" {
		options.GetConfig().LogConfig.Level = "info"
	}
	rsName := options.GetConfig().ServerConfig.Name
	err := logger.Init(options.GetConfig().LogConfig, rsName)
	if err != nil {
		fmt.Printf("failed to init logger, err = %v, exiting\n", err)
		return err
	}
	return nil
}

func ApplyDefaultMetaCleanupCfg(cfg *config.Config) {
	if cfg.MetaCleanupCfg == nil {
		cfg.MetaCleanupCfg = &config.MetaCleanupConfig{
			MaxInstanceDownTimeSec: constant.DefaultNoTimeout,
			MaxWorkerDownTimeSec:   constant.DefaultNoTimeout,
		}
	}
}

func ApplyDefaultClientWriteTimeout(cfg *config.Config) {
	hangThreshold := cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.HangCheckerCfg.GetThresholdTime(constant.Decode)
	clientWriteTimeoutMs := calculateClientWriteTimeout(hangThreshold, cfg.ServerConfig.ClientWriteTimeoutMs)
	if clientWriteTimeoutMs != cfg.ServerConfig.ClientWriteTimeoutMs {
		logger.Log().Infof("clientWriteTimeoutMs will be set to [%v] from [%v] due to value of hangThreshold[%v]",
			clientWriteTimeoutMs, cfg.ServerConfig.ClientWriteTimeoutMs, hangThreshold)
		cfg.ServerConfig.ClientWriteTimeoutMs = clientWriteTimeoutMs
	}
}

// ApplyDefaultNonReferenceRateLimitCfg applies default values for rate limit configuration
func ApplyDefaultRateLimitCfg(cfg *config.Config) {
	if cfg.ServerConfig.RateLimitCfg == nil {
		cfg.ServerConfig.RateLimitCfg = &config.RateLimitCfg{}
	}

	if cfg.ServerConfig.RateLimitCfg.MaxConcurrentRequests == 0 {
		cfg.ServerConfig.RateLimitCfg.MaxConcurrentRequests = constant.MaxConcurrentRequests
	}

	// Initialize non-reference API rate limit config if not present
	if cfg.ServerConfig.RateLimitCfg.NonReferenceAPIs == nil {
		cfg.ServerConfig.RateLimitCfg.NonReferenceAPIs = &config.NonReferenceAPIRateLimit{
			Enabled:                false, // Disabled by default for backward compatibility
			RequestsPerMinute:      constant.DefaultNonReferenceAPIRPM,
			ExemptPaths:            constant.DefaultExemptPaths,
			CleanupIntervalMinutes: constant.DefaultRateLimitCleanupMinutes,
		}
	} else {
		// Apply defaults for unset fields
		if cfg.ServerConfig.RateLimitCfg.NonReferenceAPIs.RequestsPerMinute <= 0 {
			cfg.ServerConfig.RateLimitCfg.NonReferenceAPIs.RequestsPerMinute = constant.DefaultNonReferenceAPIRPM
		}
		if cfg.ServerConfig.RateLimitCfg.NonReferenceAPIs.CleanupIntervalMinutes <= 0 {
			cfg.ServerConfig.RateLimitCfg.NonReferenceAPIs.CleanupIntervalMinutes = constant.DefaultRateLimitCleanupMinutes
		}
		if len(cfg.ServerConfig.RateLimitCfg.NonReferenceAPIs.ExemptPaths) == 0 {
			cfg.ServerConfig.RateLimitCfg.NonReferenceAPIs.ExemptPaths = constant.DefaultExemptPaths
		}
	}
}

// calculateClientWriteTimeout calculates client write timeout based on hang time
// hangThreshold: Decode hang threshold (milliseconds)
// clientWriteTimeoutMs: server configured client-write-timeout-ms
// Return value: actual timeout to use (milliseconds), -1 means no timeout
func calculateClientWriteTimeout(hangThreshold, clientWriteTimeoutMs int64) (result int64) {
	defer func() {
		if result >= 300_000 {
			result = 300_000
		}
	}()

	// Case 1: client-write-timeout-ms not configured or is 0
	if clientWriteTimeoutMs == 0 {
		if hangThreshold > 0 {
			return hangThreshold / 2
		}
		// hangThreshold <= 0, no timeout
		return -1
	}

	// Case 2: client-write-timeout-ms < 0, no timeout
	if clientWriteTimeoutMs < 0 {
		return -1
	}

	// Case 3: client-write-timeout-ms > 0
	// 3.1: hangThreshold > 0 and client-write-timeout-ms >= hangThreshold, use half of hangThreshold
	if hangThreshold > 0 && clientWriteTimeoutMs >= hangThreshold {
		return hangThreshold / 2
	}
	// 3.2: other cases use configured value
	return clientWriteTimeoutMs
}

func applyDefaultConfig(cfg *config.Config) error {
	InitConfigConstance(options.GetConfig())
	if cfg.ServerConfig.InferMode == "" {
		logger.Log().Warnf("Infer mode not specified, using default [%s]", constant.PDMode)
		cfg.ServerConfig.InferMode = constant.PDMode
	}
	if cfg.ServerConfig.OpsPort == "" {
		if cfg.ServerConfig.HttpPort == "" {
			return fmt.Errorf("http-port is empty, cannot generate ops port")
		}
		httpPortInt, err := strconv.Atoi(cfg.ServerConfig.HttpPort)
		if err != nil {
			return fmt.Errorf("http-port [%s] is not a valid port number: %v", cfg.ServerConfig.HttpPort, err)
		}
		cfg.ServerConfig.OpsPort = strconv.Itoa(httpPortInt + 10)
		logger.Log().Infof("Ops port not specified, using http port + 10 [%s] as ops port", cfg.ServerConfig.OpsPort)
	}
	err := CheckInferMode(cfg.ServerConfig.InferMode)
	if err != nil {
		return err
	}
	if err := cluster.CheckRoutesInsMapValidity(cfg.InstanceManagerCfg.RouteTable.InstanceRoutes); err != nil {
		return fmt.Errorf("invalid routes found in config: %v", err)
	}
	config.ApplyDefaultHttpCfg(&cfg.HttpConfig)
	healthCfg, err := health.ApplyDefaultAndCheckCfg(cfg.InstanceManagerCfg.HealthCheckConfig)
	if err != nil {
		logger.Log().Errorf("invalid health-check-config: %v", err)
		return err
	}

	cfg.InstanceManagerCfg.HealthCheckConfig = healthCfg

	ApplyDefaultRateLimitCfg(cfg)
	ApplyDefaultMetaCleanupCfg(cfg)

	config.ApplyDefaultProbeCfg(&cfg.ProbeCfg)

	err = peer_client.ApplyDefaultPeerClientConfig(cfg)
	if err != nil {
		logger.Log().Errorf("invalid peer-client-config: %v", err)
		return err
	}

	ApplyDefaultClientWriteTimeout(cfg)

	constant.ServerName = cfg.ServerConfig.Name
	if err := config.ApplyMessageErrorConfig(cfg.ServerConfig.AnthropicErrorTypeConfigFileName); err != nil {
		return err
	}
	return nil
}

func InitConfigConstance(cfg *config.Config) {
	initSloConstant(cfg.SchedulersCfg.SLOScheduling)
}

func initSloConstant(cfg *config.SLOSchedulerCfg) {
	// init slo set for validity check
	if cfg == nil {
		return
	}
	if cfg.SloEnable {
		sloSet := make(map[string]interface{})
		for _, sloGroup := range cfg.SloManager {
			sloSet[sloGroup.Tag] = nil
		}
		sloSet[constant.SLOTagDefault] = nil
		constant.SloSet = sloSet
	} else {
		constant.SloSet = nil
	}
}

func CheckInferMode(inferMode string) error {
	if !utils.InMap(inferMode, constant.ValidInferModeMap) {
		return fmt.Errorf("invalid infer mode: %s, must be one of %v", inferMode, utils.MapKeysStr(constant.ValidInferModeMap))
	}
	return nil
}

func parseFlags() {
	debugMode := flag.Bool("debug", false, "whether enable pprof debug mode")
	debugPort := flag.Int("debug-port", 20000, "port for pprof debug mode")
	overrideHttpPort := flag.Int("http-port", -1, "port for http service")
	flag.Parse()

	if *overrideHttpPort > 0 {
		options.GetConfig().ServerConfig.HttpPort = strconv.Itoa(*overrideHttpPort)
		logger.Log().Infof("override port to ports in arg http.port=%d", *overrideHttpPort)
	}

	logger.Log().Infof("options debug mode: %v, debug port : %v", *debugMode, *debugPort)
	if *debugMode {
		go func() {
			logger.Log().Infof("debug mode is enabled, listening on port %d", *debugPort)
			err := http.ListenAndServe(fmt.Sprintf("%s:%v", options.GetConfig().ServerConfig.Addr, *debugPort), nil)
			if err != nil {
				logger.Log().Errorf("fail to start debug server. %v", err)
			}
		}()
	} else {
		logger.Log().Infof("debug mode is disabled")
	}
}

func newClusterAdminHandler(peerClient *peer_client.PeerClient) (cluster.ClusterService, *AdminHandler, error) {

	clusterService, err := newClusterService(options.GetConfig(), peerClient)
	if err != nil {
		return nil, nil, fmt.Errorf("fail to create cluster service, err = %v", err)
	}

	adminHandler := AdminHandler{
		ClusterService: clusterService,
		LeaderClient:   peerClient,
	}
	return clusterService, &adminHandler, nil
}

func initMetricsCollector(app *App, metricsYamlPath string) (*metrics_collector.Collector, error) {
	collectInstances := make([]metrics_collector.Instance, 0)
	instanceRoutes := options.GetConfig().InstanceManagerCfg.RouteTable.InstanceRoutes
	for _, route := range instanceRoutes {
		if utils.InMap(route.Role, constant.NoCollectMetricsRoleMap) {
			continue
		}
		collectInstances = append(collectInstances, metrics_collector.Instance{
			Role: route.Role,
			IP:   route.Ip,
			Port: route.Port,
		})
	}

	metrics_logger.SetLogger(&metrics_logger.Log{
		Info:   logger.Log().Info,
		Infof:  logger.Log().Infof,
		Error:  logger.Log().Error,
		Errorf: logger.Log().Errorf,
	})

	metrics_collector.SetRunningFun(app.managers.clusterService.IsLeader)
	metrics_collector.SetGetLatestInstancesFun(app.GetInstances)

	collect, err := metrics_collector.NewMetricsCollector(collectInstances, metricsYamlPath)
	if err != nil {
		logger.Log().Errorf("fail to init metrics collecctor, error: %s", err.Error())
		return collect, err
	}
	return collect, nil
}

func (a *App) GetInstances() map[string]metrics_collector.Instance {
	collectInstances := make([]metrics_collector.Instance, 0)
	clusterState := a.managers.clusterService.State()
	if clusterState == nil {
		return map[string]metrics_collector.Instance{}
	}
	peerAddr := clusterState.Peers
	if peerAddr != nil {
		for _, peer := range peerAddr {
			collectInstances = append(collectInstances, metrics_collector.Instance{
				Role: "Scheduler",
				IP:   peer.Addr.Host,
				Port: peer.Addr.HttpPort,
			})
		}
	}

	allRoutes := a.managers.instanceManager.GetAllInstance()
	if allRoutes != nil {
		for _, instanceRoute := range allRoutes {
			if utils.InMap(instanceRoute.Role, constant.NoCollectMetricsRoleMap) {
				continue
			}
			collectInstances = append(collectInstances, metrics_collector.Instance{
				Role: instanceRoute.Role,
				IP:   instanceRoute.Ip,
				Port: instanceRoute.Port,
			})
		}
	}

	return metrics_collector.TransInstanceMap(collectInstances)
}

func (a *App) Stop() {
	logger.Log().Infof("app stopping")
	StopServer()
}
