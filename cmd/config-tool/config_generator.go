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

package main

import (
	"flag"
	"fmt"
	"maps"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"route-server/cmd/route_server/server"

	"route-server/pkg/common/cluster"
	"route-server/pkg/common/config"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/utils"
	"route-server/pkg/route-server/scheduler"

	"github.com/goccy/go-yaml"
)

var (
	TemplatePath               string
	DeployTargetPath           string
	Name                       string
	Host                       string
	Port                       uint
	EncodeAddrs                string
	PrefillAddrs               string
	StaticInfos                string
	DecodeAddrs                string
	MoeAddrs                   string
	EtcdEndpoints              string
	EtcdAutoSyncSecond         string
	RemoteSchedulerType        string
	RemoteSchedulerEndpoint    string
	LogFilePath                string
	Domain                     string
	UseDomainForLeader         string
	IsCandidate                string
	Mode                       string
	InferMode                  string
	InferModuleVersion         string
	ReadinessCfg               string
	OpsPort                    uint
	OpsHTTPSEnable             string
	OpsMTLSEnable              string
	OpsTLSCertFile             string
	OpsTLSKeyFile              string
	OpsTLSCaCertFile           string
	TLSCertFile                string
	TLSKeyFile                 string
	TLSCaCertFile              string
	TLSSkipVerify              string
	EnableHTTPS                string
	EnableMTLS                 string
	ClientHTTPSEnable          string
	ClientMTLSEnable           string
	ClientCertFile             string
	ClientKeyFile              string
	ClientCaCertFile           string
	EtcdTLSEnable              string
	EtcdMTLSEnable             string
	EtcdCertFile               string
	EtcdKeyFile                string
	EtcdCaCertFile             string
	EnableOpsAPIsOnServicePort string
	ClusterNamespace           string
)

func main() {
	success := mainProc(true)
	if !success {
		os.Exit(1)
	}
}

func mainProc(parseFlag bool) bool {
	if parseFlag {
		parseArgs()
	}

	cfg, configPath, err := loadConfig()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return false
	}

	if !applyServerConfig(cfg) {
		return false
	}

	if !modifyClusterConfig(cfg) {
		return false
	}

	cfg.InstanceManagerCfg.RouteTable.InstanceRoutes = make(map[string]config.Route)
	if !instances(cfg) {
		return false
	}

	applyHybridModeConfig(cfg)

	if !generateServerInferMode(cfg) ||
		!generateMetricsCfg(cfg) ||
		!generateRequiredRoles(cfg) ||
		!generateProbeReadiness(cfg) ||
		!remoteSchedule(cfg) {
		return false
	}

	return writeConfigFile(cfg, configPath)
}

// loadConfig loads and validates the configuration, complexity: 1
func loadConfig() (*config.Config, string, error) {
	if DeployTargetPath == "" {
		return nil, "", fmt.Errorf("target dir must not be empty")
	}
	configPath := targetConfigPath(DeployTargetPath)
	if err := checkValid(configPath); err != nil {
		return nil, "", fmt.Errorf("invalid params, err = %v", err)
	}
	cfg, err := loadFromFile(TemplatePath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to load template file %s, err = %v", TemplatePath, err)
	}
	return cfg, configPath, nil
}

// applyServerConfig applies command line arguments to the configuration struct, complexity: 6
func applyServerConfig(cfg *config.Config) bool {
	cfg.ServerConfig.Name = Name
	cfg.ServerConfig.Addr = Host
	cfg.ServerConfig.HttpPort = strconv.Itoa(int(Port))
	if OpsPort != 0 {
		cfg.ServerConfig.OpsPort = strconv.Itoa(int(OpsPort))
	}
	cfg.ServerConfig.InferenceModuleType = InferModuleVersion

	if !applyPeerClientConfig(cfg) {
		return false
	}
	applyLogConfig(cfg)

	if !applyTLSConfig(cfg) {
		return false
	}

	if !applyOpsTLSConfig(cfg) {
		return false
	}

	if !applyClientTLSConfig(cfg) {
		return false
	}

	if !applyEtcdTLSConfig(cfg) {
		return false
	}

	if EnableOpsAPIsOnServicePort != "" {
		enable, err := strconv.ParseBool(EnableOpsAPIsOnServicePort)
		if err != nil {
			fmt.Printf("Error: invalid enable-ops-apis-on-service-port, err = %v\n", err)
			return false
		}
		cfg.ServerConfig.EnableOpsAPIsOnServicePort = enable
	}

	return true
}

// applyOpsTLSConfig applies Ops TLS command line arguments to the configuration struct
// Ops TLS must be fully configured; it is independent from server.tls.
func applyOpsTLSConfig(cfg *config.Config) bool {
	if OpsHTTPSEnable == "" && OpsMTLSEnable == "" && OpsTLSCertFile == "" && OpsTLSKeyFile == "" && OpsTLSCaCertFile == "" {
		return true
	}

	// If enable-https is explicitly set to false, skip ops TLS configuration entirely, clear template defaults
	if OpsHTTPSEnable != "" {
		enable, err := strconv.ParseBool(OpsHTTPSEnable)
		if err != nil {
			fmt.Printf("Error: invalid ops-enable-https, err = %v\n", err)
			return false
		}
		if !enable {
			if cfg.ServerConfig.OpsTLSConfig != nil {
				cfg.ServerConfig.OpsTLSConfig.EnableHTTPS = false
				cfg.ServerConfig.OpsTLSConfig.EnableMTLS = false
				cfg.ServerConfig.OpsTLSConfig.CertFile = ""
				cfg.ServerConfig.OpsTLSConfig.KeyFile = ""
				cfg.ServerConfig.OpsTLSConfig.CaFile = ""
			}
			return true
		}
	}

	// ops-tls must have both cert-file and key-file configured together when HTTPS is enabled
	if OpsTLSCertFile == "" || OpsTLSKeyFile == "" {
		fmt.Printf("Error: ops-tls-cert-file and ops-tls-key-file must be provided together\n")
		return false
	}

	if cfg.ServerConfig.OpsTLSConfig == nil {
		cfg.ServerConfig.OpsTLSConfig = &config.TLSCertificateConfig{}
	}

	cfg.ServerConfig.OpsTLSConfig.CertFile = OpsTLSCertFile
	cfg.ServerConfig.OpsTLSConfig.KeyFile = OpsTLSKeyFile
	if OpsTLSCaCertFile != "" {
		cfg.ServerConfig.OpsTLSConfig.CaFile = OpsTLSCaCertFile
	}
	// OpsHTTPSEnable has been parsed above, only reach here when it's true or empty
	if OpsHTTPSEnable != "" {
		cfg.ServerConfig.OpsTLSConfig.EnableHTTPS = true
	}
	if OpsMTLSEnable != "" {
		enable, err := strconv.ParseBool(OpsMTLSEnable)
		if err != nil {
			fmt.Printf("Error: invalid ops-enable-mtls, err = %v\n", err)
			return false
		}
		cfg.ServerConfig.OpsTLSConfig.EnableMTLS = enable
	}

	return true
}

// applyClientTLSConfig applies vLLM instance client TLS command line arguments to the configuration struct
// client-https-enable has the highest priority: when set to false, all TLS config is skipped.
func applyClientTLSConfig(cfg *config.Config) bool {
	if ClientHTTPSEnable == "" && ClientMTLSEnable == "" && ClientCertFile == "" && ClientKeyFile == "" && ClientCaCertFile == "" {
		return true
	}

	// client-https-enable=false → skip all client TLS configuration, clear template defaults
	if ClientHTTPSEnable != "" {
		enable, err := strconv.ParseBool(ClientHTTPSEnable)
		if err != nil {
			fmt.Printf("Error: invalid client-https-enable, err = %v\n", err)
			return false
		}
		if !enable {
			if cfg.HttpConfig.TLSConfig != nil {
				cfg.HttpConfig.TLSConfig.EnableHTTPS = false
				cfg.HttpConfig.TLSConfig.EnableMTLS = false
				cfg.HttpConfig.TLSConfig.CertFile = ""
				cfg.HttpConfig.TLSConfig.KeyFile = ""
				cfg.HttpConfig.TLSConfig.CaFile = ""
				cfg.HttpConfig.TLSConfig.SkipVerify = false
			}
			return true
		}
	}

	if cfg.HttpConfig.TLSConfig == nil {
		cfg.HttpConfig.TLSConfig = &config.TLSCertificateConfig{}
	}

	if ClientCertFile != "" {
		cfg.HttpConfig.TLSConfig.CertFile = ClientCertFile
	}
	if ClientKeyFile != "" {
		cfg.HttpConfig.TLSConfig.KeyFile = ClientKeyFile
	}
	if ClientCaCertFile != "" {
		cfg.HttpConfig.TLSConfig.CaFile = ClientCaCertFile
	}

	// ClientHTTPSEnable has been parsed above, only reach here when it's true or empty
	if ClientHTTPSEnable != "" {
		cfg.HttpConfig.TLSConfig.EnableHTTPS = true
	}
	if ClientMTLSEnable != "" {
		enable, err := strconv.ParseBool(ClientMTLSEnable)
		if err != nil {
			fmt.Printf("Error: invalid client-mtls-enable, err = %v\n", err)
			return false
		}
		cfg.HttpConfig.TLSConfig.EnableMTLS = enable
	}

	return true
}

// applyEtcdTLSConfig applies etcd TLS command line arguments to the configuration struct
// etcd-tls-enable has the highest priority: when set to false, all etcd TLS config is skipped.
func applyEtcdTLSConfig(cfg *config.Config) bool {
	if EtcdTLSEnable == "" && EtcdMTLSEnable == "" && EtcdCertFile == "" && EtcdKeyFile == "" && EtcdCaCertFile == "" {
		return true
	}

	// etcd-tls-enable=false → skip all etcd TLS configuration, clear template defaults
	if EtcdTLSEnable != "" {
		enable, err := strconv.ParseBool(EtcdTLSEnable)
		if err != nil {
			fmt.Printf("Error: invalid etcd-tls-enable, err = %v\n", err)
			return false
		}
		if !enable {
			if cfg.ClusterCfg.EtcdCfg.TLSConfig != nil {
				cfg.ClusterCfg.EtcdCfg.TLSConfig.EnableHTTPS = false
				cfg.ClusterCfg.EtcdCfg.TLSConfig.EnableMTLS = false
				cfg.ClusterCfg.EtcdCfg.TLSConfig.CertFile = ""
				cfg.ClusterCfg.EtcdCfg.TLSConfig.KeyFile = ""
				cfg.ClusterCfg.EtcdCfg.TLSConfig.CaFile = ""
				cfg.ClusterCfg.EtcdCfg.TLSConfig.SkipVerify = false
			}
			return true
		}
	}

	if cfg.ClusterCfg.EtcdCfg.TLSConfig == nil {
		cfg.ClusterCfg.EtcdCfg.TLSConfig = &config.TLSCertificateConfig{}
	}

	if EtcdCertFile != "" {
		cfg.ClusterCfg.EtcdCfg.TLSConfig.CertFile = EtcdCertFile
	}
	if EtcdKeyFile != "" {
		cfg.ClusterCfg.EtcdCfg.TLSConfig.KeyFile = EtcdKeyFile
	}
	if EtcdCaCertFile != "" {
		cfg.ClusterCfg.EtcdCfg.TLSConfig.CaFile = EtcdCaCertFile
	}

	// EtcdTLSEnable has been parsed above, only reach here when it's true or empty
	if EtcdTLSEnable != "" {
		cfg.ClusterCfg.EtcdCfg.TLSConfig.EnableHTTPS = true
	}
	if EtcdMTLSEnable != "" {
		enable, err := strconv.ParseBool(EtcdMTLSEnable)
		if err != nil {
			fmt.Printf("Error: invalid etcd-mtls-enable, err = %v\n", err)
			return false
		}
		cfg.ClusterCfg.EtcdCfg.TLSConfig.EnableMTLS = enable
	}

	return true
}

// applyTLSConfig applies TLS command line arguments to the configuration struct
// enable-https has the highest priority: when set to false, all TLS config is skipped.
func applyTLSConfig(cfg *config.Config) bool {
	if TLSCertFile == "" && TLSKeyFile == "" && TLSCaCertFile == "" && EnableMTLS == "" && TLSSkipVerify == "" && EnableHTTPS == "" {
		return true
	}

	// enable-https=false → skip all TLS configuration, clear template defaults
	if EnableHTTPS != "" {
		enableHTTPS, err := strconv.ParseBool(EnableHTTPS)
		if err != nil {
			fmt.Printf("Error: invalid enable-https, err = %v\n", err)
			return false
		}
		if !enableHTTPS {
			if cfg.ServerConfig.TLSConfig != nil {
				cfg.ServerConfig.TLSConfig.EnableHTTPS = false
				cfg.ServerConfig.TLSConfig.EnableMTLS = false
				cfg.ServerConfig.TLSConfig.CertFile = ""
				cfg.ServerConfig.TLSConfig.KeyFile = ""
				cfg.ServerConfig.TLSConfig.CaFile = ""
				cfg.ServerConfig.TLSConfig.SkipVerify = false
			}
			return true
		}
	}

	if cfg.ServerConfig.TLSConfig == nil {
		cfg.ServerConfig.TLSConfig = &config.TLSCertificateConfig{}
	}

	if TLSCertFile != "" {
		cfg.ServerConfig.TLSConfig.CertFile = TLSCertFile
	}
	if TLSKeyFile != "" {
		cfg.ServerConfig.TLSConfig.KeyFile = TLSKeyFile
	}
	if TLSCaCertFile != "" {
		cfg.ServerConfig.TLSConfig.CaFile = TLSCaCertFile
	}

	// EnableHTTPS has been parsed above, only reach here when it's true or empty
	if EnableHTTPS != "" {
		cfg.ServerConfig.TLSConfig.EnableHTTPS = true
	}

	if EnableMTLS != "" {
		enableMTLS, err := strconv.ParseBool(EnableMTLS)
		if err != nil {
			fmt.Printf("Error: invalid enable-mtls, err = %v\n", err)
			return false
		}
		cfg.ServerConfig.TLSConfig.EnableMTLS = enableMTLS
	}

	if TLSSkipVerify != "" {
		skipVerify, err := strconv.ParseBool(TLSSkipVerify)
		if err != nil {
			fmt.Printf("Error: invalid tls-skip-verify, err = %v\n", err)
			return false
		}
		cfg.ServerConfig.TLSConfig.SkipVerify = skipVerify
	}

	return true
}

// applyPeerClientConfig configures peer client related parameters, complexity: 3
func applyPeerClientConfig(cfg *config.Config) bool {
	domainForLeader, err := strconv.ParseBool(UseDomainForLeader)
	if err != nil {
		fmt.Printf("Error: invalid useDomainForLeader, err = %v\n", err)
		return false
	}
	if cfg.PeerClientCfg == nil {
		cfg.PeerClientCfg = &config.PeerClientConfig{}
	}
	cfg.PeerClientCfg.SendWithDomain = domainForLeader
	cfg.PeerClientCfg.Domain = Domain

	if cfg.OpsPeerClientCfg == nil {
		cfg.OpsPeerClientCfg = cfg.PeerClientCfg.DeepCopy()
	}
	if cfg.OpsPeerClientCfg.HttpCfg == nil {
		cfg.OpsPeerClientCfg.HttpCfg = &config.HttpConfig{}
	}
	if cfg.OpsPeerClientCfg.HttpCfg.TLSConfig == nil {
		cfg.OpsPeerClientCfg.HttpCfg.TLSConfig = cfg.ServerConfig.OpsTLSConfig
	}
	return true
}

// applyLogConfig configures the log file path, complexity: 2
func applyLogConfig(cfg *config.Config) {
	if LogFilePath == "" {
		cfg.LogConfig.Filename = path.Join(DeployTargetPath, "log", "route-server.log")
		fmt.Printf("log-file is determined automatically to %s\n", cfg.LogConfig.Filename)
	} else {
		cfg.LogConfig.Filename = LogFilePath
		fmt.Printf("log-file is set to %s\n", LogFilePath)
	}
}

// applyHybridModeConfig applies special configuration in Hybrid mode, complexity: 2
func applyHybridModeConfig(cfg *config.Config) {
	if InferMode == constant.HybridMode && cfg.InstanceManagerCfg.HealthCheckConfig != nil {
		fmt.Printf("In hybrid mode, change link down health check to false to fail on linkdown in request failures checker")
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkBlacklistConfig.Enable = false
		cfg.InstanceManagerCfg.HealthCheckConfig.AvailableCheckersCfg.LinkDownCfg.Enable = false
	}
}

// writeConfigFile writes the configuration to a file, complexity: 2
func writeConfigFile(cfg *config.Config, configPath string) bool {
	marshal, err := yaml.Marshal(cfg)
	if err != nil {
		fmt.Println(err)
		return false
	}
	err = os.WriteFile(path.Join(configPath, "route-server.yaml"), marshal, 0600)
	if err != nil {
		fmt.Printf("Error: failed to write target config file %s, err = %v\n", configPath, err)
		return false
	}
	fmt.Printf("success to write config file to %s\n", configPath)
	return true
}

func generateServerInferMode(cfg *config.Config) bool {
	err := server.CheckInferMode(InferMode)
	if err != nil {
		fmt.Printf("invalid infer mode is given, err = %v\n", err)
		return false
	}
	cfg.ServerConfig.InferMode = InferMode
	return true
}

func generateMetricsCfg(cfg *config.Config) bool {
	if cfg.MetricsCfg.Enable == false {
		return true
	}
	if cfg.MetricsCfg.Duration <= 0 {
		return false
	}
	if cfg.ServerConfig.InferMode == constant.HybridMode {
		cfg.MetricsCfg.MetricsCollectorConfigFileName = constant.HybridModeMetricsYml
	}
	return true
}

func generateRequiredRoles(cfg *config.Config) bool {
	err := server.CheckInferMode(InferMode)
	if err != nil {
		return false
	}
	requiredRoles := []string{constant.Prefill, constant.Decode}
	if InferMode == constant.PDAMMode {
		requiredRoles = []string{constant.Prefill, constant.Decode, constant.MOE}
	} else if InferMode == constant.EPDMode || InferMode == constant.EPDStreamMode {
		requiredRoles = []string{constant.Encode, constant.Prefill, constant.Decode}
	} else if InferMode == constant.HybridMode {
		requiredRoles = []string{constant.Decode}
	}
	cfg.InstanceManagerCfg.RequiredRoles = requiredRoles
	return true
}

func generateProbeReadiness(cfg *config.Config) bool {
	readiness := make(map[string]int)
	requiredRoles := cfg.InstanceManagerCfg.RequiredRoles
	// Parse the input readinessCfg, format: prefill:1#decode:2#moe:3
	parseRes, err := parseStringToReadiness(ReadinessCfg)
	if err != nil {
		fmt.Printf("Error: failed to parse ReadinessCfg, err = %v\n", err)
		return false
	}
	for _, role := range requiredRoles {
		if leastNum, exists := parseRes[role]; exists {
			readiness[role] = leastNum
		} else {
			// If not configured, assign default value 1
			readiness[role] = 1
		}
	}

	cfg.ProbeCfg.Readiness = readiness
	return true
}

func parseStringToReadiness(input string) (map[string]int, error) {
	result := make(map[string]int)
	if len(input) == 0 {
		return result, nil
	}
	pairs := strings.Split(input, "#")
	for _, pair := range pairs {
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, ":", 2)
		var key, value string
		if len(kv) == 2 {
			key = strings.TrimSpace(kv[0])
			value = strings.TrimSpace(kv[1])
		}

		if utils.InMap(key, constant.ValidRoleMap) {
			intValue, err := strconv.ParseInt(value, 10, 0)
			if err == nil {
				if intValue <= 0 {
					return result, fmt.Errorf("parse readiness config for role %s error, value should be greater than 0", key)
				}
				result[key] = int(intValue)
			} else {
				return result, fmt.Errorf("parse readiness config error, err = %v", err)
			}
		}
	}
	return result, nil
}

func remoteSchedule(cfg *config.Config) bool {
	if RemoteSchedulerType == "" {
		return true
	}

	if RemoteSchedulerType == "disable" {
		if cfg.SchedulersCfg.RemoteSchedule == nil {
			cfg.SchedulersCfg.RemoteSchedule = &config.RemoteSchedulerCfg{
				Enabled:    false,
				RemoteType: scheduler.ClusterLeaderType,
			}
		}
		cfg.SchedulersCfg.RemoteSchedule.Enabled = false
		return true
	}

	remoteScheduleCfg := cfg.SchedulersCfg.RemoteSchedule
	switch RemoteSchedulerType {
	case scheduler.EndpointType:
		if remoteScheduleCfg == nil {
			remoteScheduleCfg = &config.RemoteSchedulerCfg{
				Enabled:           true,
				RemoteType:        scheduler.EndpointType,
				RemoteEndpoint:    "",
				NeedContent:       false,
				TimeoutMs:         1000,
				CleanUpIntervalMs: 60000,
			}
		}

		endpoint := RemoteSchedulerEndpoint
		if endpoint == "" {
			endpoint = fmt.Sprintf("http://%s:%v", cfg.ServerConfig.Addr, cfg.ServerConfig.HttpPort)
		}
		remoteScheduleCfg.RemoteType = scheduler.EndpointType
		remoteScheduleCfg.RemoteEndpoint = endpoint
		cfg.SchedulersCfg.RemoteSchedule = remoteScheduleCfg
		return true
	case scheduler.ClusterLeaderType:
		if remoteScheduleCfg == nil {
			remoteScheduleCfg = &config.RemoteSchedulerCfg{
				Enabled:           true,
				RemoteType:        scheduler.ClusterLeaderType,
				RemoteEndpoint:    "",
				NeedContent:       false,
				TimeoutMs:         1000,
				CleanUpIntervalMs: 60000,
			}
		}
		remoteScheduleCfg.RemoteType = scheduler.ClusterLeaderType
		cfg.SchedulersCfg.RemoteSchedule = remoteScheduleCfg
		return true
	default:
		fmt.Printf("unrecognized remote-scheduler-type %s", RemoteSchedulerType)
		return false
	}
}

func instances(cfg *config.Config) (success bool) {
	if InferMode == constant.HybridMode {
		return hybridInstances(cfg)
	}
	if !moeInstances(cfg) {
		return false
	}
	if !encodeInstances(cfg) {
		return false
	}
	if len(PrefillAddrs) == 0 && len(DecodeAddrs) == 0 {
		return true
	}
	prefillAddresses, err := parseNamedInstances(PrefillAddrs, constant.Prefill)
	if err != nil {
		fmt.Printf("Error: invalid prefill addr, err = %v\n", err)
		return false
	}
	decodeAddresses, err := parseNamedInstances(DecodeAddrs, constant.Decode)
	if err != nil {
		fmt.Printf("Error: invalid decode addr, err = %v\n", err)
		return false
	}
	inputStaticInfoMap, err := parseStringToStaticInfo(StaticInfos, func(val string) (string, error) {
		return "", fmt.Errorf("static-infos key must not be empty")
	})
	if err != nil {
		fmt.Printf("Error: invalid static-infos, err = %v\n", err)
		return false
	}

	for instanceName, instance := range prefillAddresses {
		for tag, addr := range instance {
			ip, port, err := parseAddr(addr)
			if err != nil {
				fmt.Printf("Error: invalid addr for prefill addr, err = %v\n", err)
				return false
			}
			staticInfos := make(map[string]string)
			maps.Copy(staticInfos, inputStaticInfoMap)
			cfg.InstanceManagerCfg.RouteTable.InstanceRoutes[instanceName] = config.Route{
				Ip:          ip,
				Port:        port,
				Role:        constant.Prefill,
				StaticInfos: staticInfos,
				Tag:         tag,
			}
		}
	}

	for instanceName, instance := range decodeAddresses {
		for tag, addr := range instance {
			ip, port, err := parseAddr(addr)
			if err != nil {
				fmt.Printf("Error: invalid addr for decode addr, err = %v\n", err)
				return false
			}
			staticInfos := make(map[string]string)
			maps.Copy(staticInfos, inputStaticInfoMap)
			cfg.InstanceManagerCfg.RouteTable.InstanceRoutes[instanceName] = config.Route{
				Ip:          ip,
				Port:        port,
				Role:        constant.Decode,
				StaticInfos: staticInfos,
				Tag:         tag,
			}
		}
	}
	return true
}

func hybridInstances(cfg *config.Config) (success bool) {
	// If infer mode is HybridMode, only add decode addr
	if InferMode == constant.HybridMode {
		decodeAddresses, err := parseNamedInstances(DecodeAddrs, constant.Decode)
		if err != nil {
			fmt.Printf("Error: invalid decode addr, err = %v\n", err)
			return false
		}
		for instanceName, instance := range decodeAddresses {
			for tag, addr := range instance {
				ip, port, err := parseAddr(addr)
				if err != nil {
					fmt.Printf("Error: invalid addr for decode addr, err = %v\n", err)
					return false
				}
				cfg.InstanceManagerCfg.RouteTable.InstanceRoutes[instanceName] = config.Route{
					Ip:   ip,
					Port: port,
					Role: constant.Decode,
					Tag:  tag,
				}
			}
		}
	}
	return true
}

func moeInstances(cfg *config.Config) (success bool) {
	// If infer mode is PD-AM, add moe addr
	if InferMode == constant.PDAMMode {
		moeAddresses, err := parseNamedInstances(MoeAddrs, constant.MOE)
		if err != nil {
			fmt.Printf("Error: invalid moe addr, err = %v\n", err)
			return false
		}
		for instanceName, instance := range moeAddresses {
			for tag, addr := range instance {
				ip, port, err := parseAddr(addr)
				if err != nil {
					fmt.Printf("Error: invalid addr for moe addr, err = %v\n", err)
					return false
				}
				cfg.InstanceManagerCfg.RouteTable.InstanceRoutes[instanceName] = config.Route{
					Ip:   ip,
					Port: port,
					Role: constant.MOE,
					Tag:  tag,
				}
			}
		}
	}
	return true
}

func encodeInstances(cfg *config.Config) (success bool) {
	// If infer mode is EPD, add encode addr
	if InferMode == constant.EPDMode || InferMode == constant.EPDStreamMode {
		encodeAddresses, err := parseNamedInstances(EncodeAddrs, constant.Encode)
		if err != nil {
			fmt.Printf("Error: invalid encode addr, err = %v\n", err)
			return false
		}
		for instanceName, instance := range encodeAddresses {
			for tag, addr := range instance {
				ip, port, err := parseAddr(addr)
				if err != nil {
					fmt.Printf("Error: invalid addr for encode addr, err = %v\n", err)
					return false
				}
				cfg.InstanceManagerCfg.RouteTable.InstanceRoutes[instanceName] = config.Route{
					Ip:   ip,
					Port: port,
					Role: constant.Encode,
					Tag:  tag,
				}
			}
		}
	}
	return true
}

func modifyClusterConfig(cfg *config.Config) (success bool) {
	IsCandidateFlag, err := strconv.ParseBool(IsCandidate)
	if err != nil {
		fmt.Printf("Error: invalid IsCandidate, err = %v\n", err)
		return false
	}
	cfg.ClusterCfg.RoleCandidate = IsCandidateFlag
	fmt.Printf("deploying as candidate=%v\n", cfg.ClusterCfg.RoleCandidate)
	if Mode == cluster.TypeSingleton {
		cfg.InstanceManagerCfg.HealthCheckConfig.EventThresholdIntervalMs = constant.SingletonHealthEventIntervalMs
		cfg.ClusterCfg.ClusterServiceType = cluster.TypeSingleton
		// close the remote schedule when in singleton
		if cfg.SchedulersCfg.RemoteSchedule == nil {
			cfg.SchedulersCfg.RemoteSchedule = &config.RemoteSchedulerCfg{
				Enabled:    false,
				RemoteType: scheduler.ClusterLeaderType,
			}
		}
		cfg.SchedulersCfg.RemoteSchedule.Enabled = false
		return true
	}
	etcdEndpoints := strings.TrimSpace(EtcdEndpoints)

	if Mode == cluster.TypeEtcd {
		if len(etcdEndpoints) == 0 {
			fmt.Printf("Errors: no etcd endpoints for cluster mode etcd\n")
			return false
		}
		cfg.InstanceManagerCfg.HealthCheckConfig.EventThresholdIntervalMs = constant.ClusterHealthEventIntervalMs
		// remote schedule as leader
		remoteScheduleCfg := cfg.SchedulersCfg.RemoteSchedule
		if remoteScheduleCfg == nil {
			remoteScheduleCfg = &config.RemoteSchedulerCfg{
				Enabled:        true,
				RemoteType:     scheduler.ClusterLeaderType,
				RemoteEndpoint: "",
				NeedContent:    false,
				TimeoutMs:      1000,
			}
		}
		remoteScheduleCfg.RemoteType = scheduler.ClusterLeaderType
		cfg.SchedulersCfg.RemoteSchedule = remoteScheduleCfg
		if cfg.MetaCleanupCfg == nil {
			cfg.MetaCleanupCfg = &config.MetaCleanupConfig{
				MaxInstanceDownTimeSec: 900,
				MaxWorkerDownTimeSec:   300,
			}
		}
	} else if Mode == cluster.TypeK8sConfigMap {
		// ConfigMap mode: topology is sourced from a ConfigMap rather than CRDs.
		// The k8s-configmap.yaml file is copied alongside route-server.yaml by
		// deploy.sh and loaded by InitClusterConfig at startup.
		cfg.ClusterCfg.ClusterServiceType = cluster.TypeK8sConfigMap
		cfg.ClusterCfg.ConfigMapConfigFile = "k8s-configmap.yaml"
		cfg.InstanceManagerCfg.HealthCheckConfig.EventThresholdIntervalMs = constant.ClusterHealthEventIntervalMs
		// Namespace is propagated into the ConfigMap config at load time via
		// POD_NAMESPACE env / in-cluster serviceaccount when left empty here.
		// If the operator passed --cluster-namespace, record it so the config
		// file's namespace field wins only when set.
		if ClusterNamespace != "" {
			if cfg.ClusterCfg.ConfigMapCfg == nil {
				cfg.ClusterCfg.ConfigMapCfg = &config.ConfigMapConfig{}
			}
			cfg.ClusterCfg.ConfigMapCfg.Namespace = ClusterNamespace
		}
		remoteScheduleCfg := cfg.SchedulersCfg.RemoteSchedule
		if remoteScheduleCfg == nil {
			remoteScheduleCfg = &config.RemoteSchedulerCfg{
				Enabled:        true,
				RemoteType:     scheduler.ClusterLeaderType,
				RemoteEndpoint: "",
				NeedContent:    false,
				TimeoutMs:      1000,
			}
		}
		remoteScheduleCfg.RemoteType = scheduler.ClusterLeaderType
		cfg.SchedulersCfg.RemoteSchedule = remoteScheduleCfg
		cfg.MetaCleanupCfg = nil
		return true
	} else {
		if cfg.MetaCleanupCfg == nil {
			cfg.MetaCleanupCfg = &config.MetaCleanupConfig{}
		}
		cfg.MetaCleanupCfg.MaxInstanceDownTimeSec = -1
		cfg.MetaCleanupCfg.MaxWorkerDownTimeSec = -1
		if len(etcdEndpoints) == 0 {
			return true
		}
	}
	cfg.ClusterCfg.ClusterServiceType = cluster.TypeEtcd
	etcdAddresses, err := parseDomainAddresses(EtcdEndpoints, "etcd-endpoints")
	if err != nil {
		fmt.Printf("Error: invalid etcd endpoints %s, err = %v\n", TemplatePath, err)
		return false
	}
	autoSync, err := strconv.Atoi(EtcdAutoSyncSecond)
	if err != nil {
		fmt.Printf("Error: invalid etcd-auto-sync-second, err = %v\n", err)
		return false
	}
	if cfg.ClusterCfg.EtcdCfg == nil {
		cfg.ClusterCfg.EtcdCfg = &config.EtcdConfig{
			EtcdEndpoints:       etcdAddresses,
			AutoSyncIntervalSec: autoSync,
		}
	} else {
		cfg.ClusterCfg.EtcdCfg.EtcdEndpoints = etcdAddresses
		cfg.ClusterCfg.EtcdCfg.AutoSyncIntervalSec = autoSync
	}
	return true
}

func parseArgs() {
	flag.StringVar(&TemplatePath, "template", "/opt/route_server/rs-deploy/route-server-template.yaml",
		"path to template file")
	flag.StringVar(&DeployTargetPath, "target", "/opt/route_server/",
		"path to route_server deploy dir")
	flag.StringVar(&Host, "host", "", "Host ip for current container")
	flag.UintVar(&Port, "port", 9800, "non-first token delay")
	flag.StringVar(&Name, "name", "client_unknown", "Name of the client")
	flag.StringVar(&PrefillAddrs, "prefills", "", "list of prefill endpoints, "+
		"format is ${instance_name}#${instance_ip}:${instance_port}, separate with comma, "+
		"eg: prefill1#127.0.0.1:9200,prefill2#127.0.0.1:9201. "+
		"if instance_name is empty, then it will be assign name as prefill_${host}_${no}, "+
		"where ${no} is number for instance with same ip, eg: prefill_${ip}_1")
	flag.StringVar(&DecodeAddrs, "decodes", "", "list of decode endpoints, "+
		"format is ${instance_name}#${instance_ip}:${instance_port}, separate with comma, "+
		"eg: decode1#127.0.0.1:9200,decode2#127.0.0.1:9201. "+
		"if instance_name is empty, then it will be assign name as decode_${host}_${no}, "+
		"where ${no} is number for instance with same ip, eg: decode_${ip}_1")
	flag.StringVar(&MoeAddrs, "moes", "", "list of moe endpoints, "+
		"format is ${instance_name}#${instance_ip}:${instance_port}, separate with comma, "+
		"eg: moe1#127.0.0.1:9200,moe2#127.0.0.1:9201. "+
		"if instance_name is empty, then it will be assign name as moe_${host}_${port}")
	flag.StringVar(&EncodeAddrs, "encodes", "", "list of encode endpoints, "+
		"format is ${instance_name}#${instance_ip}:${instance_port}, separate with comma, "+
		"eg: encode1#127.0.0.1:9200,encode2#127.0.0.1:9201. "+
		"if instance_name is empty, then it will be assign name as encode_${host}_${port}")
	flag.StringVar(&StaticInfos, "static-infos", "", "map of static-infos for all instances. "+
		"format is ${key1}#${val1}, separate with comma, eg: max_num_seqs#54,api_server_count#1")
	flag.StringVar(&EtcdEndpoints, "etcd-endpoints", "", "list of decode endpoints, "+
		"eg: 127.0.0.1:2379,x.x.x.x:yyyy")
	flag.StringVar(&EtcdAutoSyncSecond, "etcd-auto-sync-second", "10", "etcd client auto sync interval in seconds, 0 to disable")
	flag.StringVar(&RemoteSchedulerType, "remote-scheduler-type", "",
		"type of remote scheduler, support (1)endpoint: direct access configured endpoint in yaml; "+
			"(2)leader: always find leader of cluster to remote schedule")
	flag.StringVar(&RemoteSchedulerEndpoint, "remote-scheduler-endpoint", "",
		"used endpoint for remote scheduler when remote-scheduler-type is set to endpoint. "+
			"start with protocol http. set to this route server's own http endpoint by defaults, "+
			"e.g. http://<rs_ip:port>")
	flag.StringVar(&LogFilePath, "log-file-path", "",
		"path to route server log file, if not set, then it will use RS_INSTALL_DIR/<rs_name>/RouteServer/log/route-server.log."+
			" default is empty. must give full real path")
	flag.StringVar(&Domain, "domain", "leader_domain", "domain of the leader")
	flag.StringVar(&UseDomainForLeader, "use-domain-for-leader", "false", "whether to use the domain name to access the leader")
	flag.StringVar(&IsCandidate, "candidate", "true", "whether to config this rs role to candidate (hence can become leader)")
	flag.StringVar(&Mode, "mode", "", "decide the cluster mode of the route server")
	flag.StringVar(&InferMode, "infer-mode", "PD", "infer deploy mode")
	flag.StringVar(&InferModuleVersion, "infer-module-version", "pipeline", "infer orchestration module version")
	flag.StringVar(&ReadinessCfg, "readiness-cfg", "", "readiness config, role least instance num, eg: prefill:1#decode:2#moe:3")
	flag.StringVar(&ClusterNamespace, "cluster-namespace", "", "namespace for cluster mode, defaults to POD_NAMESPACE env if empty")
	flag.UintVar(&OpsPort, "ops-port", 0, "port of the ops server for admin operations (default: http-port + 10)")
	flag.StringVar(&OpsHTTPSEnable, "ops-enable-https", "", "enable HTTPS for ops server. true/false (default: true)")
	flag.StringVar(&OpsMTLSEnable, "ops-enable-mtls", "", "enable mTLS for ops server (requires ops-tls-ca-cert-file). true/false (default: true)")
	flag.StringVar(&OpsTLSCertFile, "ops-tls-cert-file", "", "path to ops TLS certificate file (default: reuse --tls-cert-file)")
	flag.StringVar(&OpsTLSKeyFile, "ops-tls-key-file", "", "path to ops TLS private key file (default: reuse --tls-key-file)")
	flag.StringVar(&OpsTLSCaCertFile, "ops-tls-ca-cert-file", "", "path to ops CA certificate file (default: reuse --tls-ca-cert-file)")
	flag.StringVar(&TLSCertFile, "tls-cert-file", "", "path to TLS certificate file (e.g. /etc/certs/server.crt)")
	flag.StringVar(&TLSKeyFile, "tls-key-file", "", "path to TLS private key file (e.g. /etc/certs/server.key)")
	flag.StringVar(&TLSCaCertFile, "tls-ca-cert-file", "", "path to CA certificate file for mTLS client verification (e.g. /etc/certs/ca.crt)")
	flag.StringVar(&EnableHTTPS, "enable-https", "", "enable HTTPS for the main server port. true/false (default: true)")
	flag.StringVar(&EnableMTLS, "enable-mtls", "", "enable mutual TLS (requires tls-ca-cert-file). true/false (default: true)")
	flag.StringVar(&TLSSkipVerify, "tls-skip-verify", "", "skip TLS certificate verification. true/false (default: false)")
	flag.StringVar(&ClientHTTPSEnable, "client-https-enable", "", "enable HTTPS for vLLM instance client. true/false (default: true)")
	flag.StringVar(&ClientMTLSEnable, "client-mtls-enable", "", "enable mTLS for vLLM instance client. true/false (default: true)")
	flag.StringVar(&ClientCertFile, "client-cert-file", "", "path to vLLM client TLS certificate file (e.g. /etc/certs/client.crt)")
	flag.StringVar(&ClientKeyFile, "client-key-file", "", "path to vLLM client TLS private key file (e.g. /etc/certs/client.key)")
	flag.StringVar(&ClientCaCertFile, "client-ca-cert-file", "", "path to vLLM client CA certificate file (e.g. /etc/certs/ca.crt)")
	flag.StringVar(&EtcdTLSEnable, "etcd-tls-enable", "", "enable TLS for etcd connection. true/false (default: true)")
	flag.StringVar(&EtcdMTLSEnable, "etcd-mtls-enable", "", "enable mTLS for etcd connection. true/false (default: true)")
	flag.StringVar(&EtcdCertFile, "etcd-cert-file", "", "path to etcd client TLS certificate file (e.g. /etc/certs/etcd-client.crt)")
	flag.StringVar(&EtcdKeyFile, "etcd-key-file", "", "path to etcd client TLS private key file (e.g. /etc/certs/etcd-client.key)")
	flag.StringVar(&EtcdCaCertFile, "etcd-ca-cert-file", "", "path to etcd CA certificate file (e.g. /etc/certs/etcd-ca.crt)")
	flag.StringVar(&EnableOpsAPIsOnServicePort, "enable-ops-apis-on-service-port", "", "register ops APIs on service port as well. true/false (default: false)")
	flag.Parse()
}

func checkValid(targetConfigDir string) error {
	if Name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if err := checkTargetDir(targetConfigDir); err != nil {
		return fmt.Errorf("target dir is invalid, err = %v", err)
	}
	if Port < 1 || Port > 65535 {
		return fmt.Errorf("port must be in range of [1, 65535], got %v", Port)
	}
	if Host == "" {
		host, err := getHostIp()
		if err != nil {
			return err
		}
		Host = host
		fmt.Printf("assigning Host ip to %v\n", Host)
	} else {
		ip := net.ParseIP(Host)
		if ip == nil {
			return fmt.Errorf("given Host is invalid %v", Host)
		}
	}
	return nil
}

func parseStringToRoute(input string, emptyKeyFunc func(val string) (string, error)) (map[string]map[string]string, error) {
	result := make(map[string]map[string]string)

	if len(input) == 0 {
		return result, nil
	}

	pairs := strings.Split(input, ",")

	for _, pair := range pairs {
		if pair == "" {
			continue
		}

		kv := strings.SplitN(pair, "#", 3)
		var key, value, tag string
		if len(kv) == 1 {
			key = ""
			value = strings.TrimSpace(kv[0])
			tag = constant.SLOTagDefault
		} else if len(kv) == 2 {
			// case1: 127.0.0.1:1234#tag1
			if len(strings.SplitN(strings.TrimSpace(kv[0]), ":", 2)) == 2 {
				key = ""
				value = strings.TrimSpace(kv[0])
				tag = strings.TrimSpace(kv[1])
			} else if len(strings.SplitN(strings.TrimSpace(kv[1]), ":", 2)) == 2 {
				// case2: pname#127.0.0.1:1234
				key = strings.TrimSpace(kv[0])
				value = strings.TrimSpace(kv[1])
				tag = constant.SLOTagDefault
			} else {
				return nil, fmt.Errorf("can't find valid addr like ip:host")
			}
		} else {
			key = strings.TrimSpace(kv[0])
			value = strings.TrimSpace(kv[1])
			tag = strings.TrimSpace(kv[2])
		}

		if key == "" {
			newKey, err := emptyKeyFunc(value)
			if err != nil {
				return nil, fmt.Errorf("empty key in pair: %s, err = %v", pair, err)
			}
			if newKey == "" {
				continue
			}
			key = newKey
		}
		result[key] = map[string]string{
			tag: value,
		}
	}
	return result, nil
}

func parseStringToStaticInfo(input string, emptyKeyFunc func(val string) (string, error)) (map[string]string, error) {
	result := make(map[string]string)

	if len(input) == 0 {
		return result, nil
	}

	pairs := strings.Split(input, ",")

	for _, pair := range pairs {
		if pair == "" {
			continue
		}

		kv := strings.SplitN(pair, "#", 2)
		var key, value string
		if len(kv) == 1 {
			key = ""
			value = strings.TrimSpace(kv[0])
		} else {
			key = strings.TrimSpace(kv[0])
			value = strings.TrimSpace(kv[1])
		}

		if key == "" {
			newKey, err := emptyKeyFunc(value)
			if err != nil {
				return nil, fmt.Errorf("empty key in pair: %s, err = %v", pair, err)
			}
			if newKey == "" {
				continue
			}
			key = newKey
		}
		result[key] = value
	}

	return result, nil
}

func parseNamedInstances(namedAddrs string, instanceType string) (map[string]map[string]string, error) {
	if namedAddrs == "" {
		return nil, fmt.Errorf("given %s addrs is empty", instanceType)
	}

	nameAddrPairs, err := parseStringToRoute(namedAddrs, func(addr string) (string, error) {
		ip, port, err := parseAddr(addr)
		if err != nil {
			return "", fmt.Errorf("invalid address %v", err)
		}
		ipStr := strings.ReplaceAll(ip, ".", "_")
		return fmt.Sprintf("%s_%s_%v", instanceType, ipStr, port), nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to parse %s, err = %v", instanceType, err)
	}

	for instanceName, intance := range nameAddrPairs {
		for _, address := range intance {
			_, _, err := parseAddr(address)
			if err != nil {
				return nil, fmt.Errorf("invalid %s, given ip:Port %s for %s is invalid", instanceType, address, instanceName)
			}
		}

	}
	return nameAddrPairs, nil
}

func parseAddr(addr string) (string, int, error) {
	parts := strings.Split(addr, ":")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid address format")
	}

	ip := net.ParseIP(parts[0])
	if ip == nil {
		return "", 0, fmt.Errorf("invalid ip %s", parts[0])
	}

	port, err := strconv.Atoi(parts[1])
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid Port number")
	}

	return ip.String(), port, nil
}

func parseDomainAddresses(addrList string, addrName string) ([]string, error) {
	if addrList == "" {
		return nil, nil
	}

	addresses := strings.Split(addrList, ",")

	for _, addr := range addresses {
		if _, _, err := parseDomainAddr(addr); err != nil {
			return nil, fmt.Errorf("%s: given ip:Port %s is invalid", addrName, addr)
		}
	}
	return addresses, nil
}

func parseDomainAddr(addr string) (string, int, error) {
	parts := strings.Split(addr, ":")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid address format")
	}

	port, err := strconv.Atoi(parts[1])
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid Port number")
	}

	return parts[0], port, nil
}

func checkTargetDir(configPath string) error {
	info, err := os.Stat(configPath)
	if err != nil {
		return err
	}

	if !info.IsDir() {
		return fmt.Errorf("target config dir %s is not dir", configPath)
	}

	tmpFile := filepath.Join(configPath, ".tmp_write_test")
	defer func(name string) {
		err := os.Remove(name)
		if err != nil {
			fmt.Printf("failed to delete temp write test file %s", tmpFile)
		}
	}(tmpFile)
	if err := os.WriteFile(tmpFile, []byte("test"), 0600); err != nil {
		return fmt.Errorf("write test failed for config path %s: %v", configPath, err)
	}
	return nil
}

func targetConfigPath(dir string) string {
	return filepath.Join(dir, "config")
}

func loadFromFile(configPath string) (*config.Config, error) {
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config file failed, err:%v", err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(configBytes, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config file failed, err:%v", err)
	}

	return &cfg, nil
}

func getHostIp() (string, error) {
	// Get all network interfaces
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			switch v := addr.(type) {
			case *net.IPNet:
				ip := v.IP
				if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.To4() == nil {
					continue
				}
				return ip.String(), nil
			default:
				continue
			}
		}
	}
	return "", fmt.Errorf("cannot find valid ip")
}
