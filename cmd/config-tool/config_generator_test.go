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
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/route-server/scheduler"

	"github.com/agiledragon/gomonkey/v2"
	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
)

func Test_checkTargetDir(t *testing.T) {
	testDir := t.TempDir()
	normalDir := filepath.Join(testDir, "normal")
	err := os.Mkdir(normalDir, os.ModePerm)
	if err != nil {
		t.Errorf("fail to create directory %s: %v", normalDir, err)
		return
	}
	err = checkTargetDir(normalDir)
	if err != nil {
		t.Errorf("fail to check directory %s: %v", normalDir, err)
		return
	}

	nonDirPath := filepath.Join(testDir, "non_dir")
	testNonDir, err := os.OpenFile(nonDirPath, os.O_CREATE, 0666)
	if err != nil {
		t.Errorf("fail to open directory %s: %v", testDir, err)
		return
	}
	defer func(testNonDir *os.File) {
		err := testNonDir.Close()
		if err != nil {
			t.Errorf("fail to close non directory %s: %v", testNonDir.Name(), err)
		}
	}(testNonDir)
	err = checkTargetDir(nonDirPath)
	if err == nil {
		t.Errorf("expect to fail to check directory %s", nonDirPath)
		return
	}

	patches := gomonkey.NewPatches()

	// Patch BuildHttpRequest
	patches.ApplyFunc(os.WriteFile, func(name string, data []byte, perm os.FileMode) error {
		return fmt.Errorf("fail to write file: %s", name)
	})
	err = checkTargetDir(normalDir)
	assert.NotNil(t, err, "expect to fail to check directory %s, since write failure", normalDir)
	patches.Reset()

	patches.ApplyFunc(os.Stat, func(name string) (os.FileInfo, error) {
		return nil, fmt.Errorf("fail to stat: %s", name)
	})
	err = checkTargetDir(normalDir)
	assert.NotNil(t, err, "fail to stat")
	patches.Reset()
}

func Test_checkValid(t *testing.T) {
	testDir := t.TempDir()
	normalDir := filepath.Join(testDir, "normal")
	err := os.Mkdir(normalDir, os.ModePerm)
	if err != nil {
		t.Errorf("fail to create directory %s: %v", normalDir, err)
		return
	}
	err = checkValid(normalDir)
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "name must not be empty")

	Name = "test"
	err = checkValid(normalDir)
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "port must be in range of [1, 65535]")
	Port = 0
	err = checkValid(normalDir)
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "port must be in range of [1, 65535]")
	Port = 65536
	err = checkValid(normalDir)
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "port must be in range of [1, 65535]")

	Port = 65535
	Host = "abc"
	err = checkValid(normalDir)
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "given Host is invalid ")

	Host = ""
	err = checkValid(normalDir)
	assert.Nil(t, err)
	assert.NotEmpty(t, Host)

	// Patch BuildHttpRequest
	Host = ""
	patches := gomonkey.NewPatches()
	patches.ApplyFunc(net.Interfaces, func() ([]net.Interface, error) {
		return nil, fmt.Errorf("fail to get interfaces")
	})
	err = checkValid(normalDir)
	assert.NotNil(t, err)
	patches.Reset()
}

func Test_getHostIp(t *testing.T) {
	host, err := getHostIp()
	assert.Nil(t, err)
	assert.NotEmpty(t, host)

	patches := gomonkey.NewPatches()

	// Patch BuildHttpRequest
	patches.ApplyFunc(net.Interfaces, func() ([]net.Interface, error) {
		return nil, fmt.Errorf("fail to get interfaces")
	})
	host, err = getHostIp()
	assert.NotNil(t, err)
	assert.Empty(t, host)
	patches.Reset()
}

func Test_getHostIp1(t *testing.T) {
	tests := []struct {
		name              string
		patchedInterfaces func(patches *gomonkey.Patches) []net.Interface
		shouldErr         bool
		expectErrStr      string
		expectHost        string
	}{
		{
			name: "continue on flag loop back",
			patchedInterfaces: func(patches *gomonkey.Patches) []net.Interface {
				inter := net.Interface{}
				patches.ApplyMethod(&inter, "Addrs", func(_ *net.Interface) ([]net.Addr, error) {
					return nil, fmt.Errorf("fail to get interfaces")
				})
				return []net.Interface{
					inter,
				}
			},
			shouldErr:    true,
			expectErrStr: "cannot find valid ip",
			expectHost:   "",
		},
		{
			name: "continue on err",
			patchedInterfaces: func(patches *gomonkey.Patches) []net.Interface {
				inter := net.Interface{
					Flags: net.FlagBroadcast | net.FlagUp,
				}
				patches.ApplyMethod(&inter, "Addrs", func(_ *net.Interface) ([]net.Addr, error) {
					return nil, fmt.Errorf("fail to get interfaces")
				})
				return []net.Interface{
					inter,
				}
			},
			shouldErr:    true,
			expectErrStr: "cannot find valid ip",
			expectHost:   "",
		},
		{
			name: "skip loop back",
			patchedInterfaces: func(patches *gomonkey.Patches) []net.Interface {
				inter := net.Interface{
					Flags: net.FlagBroadcast | net.FlagUp,
				}
				patches.ApplyMethod(&inter, "Addrs", func(_ *net.Interface) ([]net.Addr, error) {
					return []net.Addr{
						&net.IPNet{
							IP: net.IP{127, 0, 0, 1},
						},
						&net.IPNet{
							IP: net.IP{169, 254, 0, 1},
						},
						&net.IPNet{
							IP: net.IPv6loopback,
						},
						&net.TCPAddr{
							IP: net.IP{127, 0, 0, 1},
						},
					}, nil
				})
				return []net.Interface{
					inter,
				}
			},
			shouldErr:    true,
			expectErrStr: "cannot find valid ip",
			expectHost:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patches := gomonkey.NewPatches()
			interfaces := tt.patchedInterfaces(patches)

			patches.ApplyFunc(net.Interfaces, func() ([]net.Interface, error) {
				return interfaces, nil
			})

			got, err := getHostIp()
			if tt.shouldErr {
				assert.NotNil(t, err)
				assert.Contains(t, tt.expectErrStr, err.Error())
			}
			assert.Equal(t, got, tt.expectHost)
			patches.Reset()
		})
	}
}

func Test_parseStringToStaticInfo(t *testing.T) {
	type args struct {
		input        string
		emptyKeyFunc func(val string) (string, error)
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]string
		wantErr bool
	}{
		{
			name: "empty input",
			args: args{
				input: "",
			},
			wantErr: false,
			want:    map[string]string{},
		},
		{
			name: "valid input",
			args: args{
				input:        "abc#vabc,def#vdef",
				emptyKeyFunc: nil,
			},
			wantErr: false,
			want: map[string]string{
				"abc": "vabc",
				"def": "vdef",
			},
		},
		{
			name: "valid input, continue on empty pair",
			args: args{
				input:        ",,,abc#vabc,,def#vdef,,,",
				emptyKeyFunc: nil,
			},
			wantErr: false,
			want: map[string]string{
				"abc": "vabc",
				"def": "vdef",
			},
		},
		{
			name: "valid input, empty key func",
			args: args{
				input: "abc#vabc,vdef",
				emptyKeyFunc: func(val string) (string, error) {
					return "empty_key", nil
				},
			},
			wantErr: false,
			want: map[string]string{
				"abc":       "vabc",
				"empty_key": "vdef",
			},
		},
		{
			name: "invalid input, key func returns error",
			args: args{
				input: "abc#vabc,vdef",
				emptyKeyFunc: func(val string) (string, error) {
					return "", fmt.Errorf("fail to get key")
				},
			},
			wantErr: true,
			want:    nil,
		},
		{
			name: "valid input, continue on key func returns empty",
			args: args{
				input: "abc#vabc,vdef",
				emptyKeyFunc: func(val string) (string, error) {
					return "", nil
				},
			},
			wantErr: false,
			want: map[string]string{
				"abc": "vabc",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStringToStaticInfo(tt.args.input, tt.args.emptyKeyFunc)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseStringToStaticInfo() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseStringToStaticInfo() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_parseStringToRoute(t *testing.T) {
	type args struct {
		input        string
		emptyKeyFunc func(val string) (string, error)
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]map[string]string
		wantErr bool
	}{
		{
			name: "empty input",
			args: args{
				input: "",
			},
			wantErr: false,
			want:    map[string]map[string]string{},
		},
		{
			name: "valid input",
			args: args{
				input:        "p0#127.0.0.1:50001#tag1,p1#127.0.0.1:50002#tag2",
				emptyKeyFunc: nil,
			},
			wantErr: false,
			want: map[string]map[string]string{
				"p0": map[string]string{
					"tag1": "127.0.0.1:50001",
				},
				"p1": map[string]string{
					"tag2": "127.0.0.1:50002",
				},
			},
		},
		{
			name: "valid input with default tag",
			args: args{
				input:        "p0#127.0.0.1:50003",
				emptyKeyFunc: nil,
			},
			wantErr: false,
			want: map[string]map[string]string{
				"p0": map[string]string{
					"default": "127.0.0.1:50003",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStringToRoute(tt.args.input, tt.args.emptyKeyFunc)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseStringToRoute() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseStringToRoute() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_generateRequiredRoles(t *testing.T) {
	type args struct {
		cfg *config.Config
	}
	tests := []struct {
		name          string
		inferMode     string
		args          args
		want          bool
		expectCfgFunc func(cfg *config.Config) bool
	}{
		{
			name:      "invalid inferMode",
			inferMode: "unknown",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{},
				},
			},
			want: false,
			expectCfgFunc: func(cfg *config.Config) bool {
				return cfg == nil
			},
		},
		{
			name:      "inferMode PD",
			inferMode: "PD",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.NotEmpty(t, cfg.InstanceManagerCfg.RequiredRoles)
				assert.Equal(t, []string{constant.Prefill, constant.Decode}, cfg.InstanceManagerCfg.RequiredRoles)
				return true
			},
		},
		{
			name:      "inferMode PD-AM",
			inferMode: "PD-AM",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.NotEmpty(t, cfg.InstanceManagerCfg.RequiredRoles)
				assert.Equal(t, []string{constant.Prefill, constant.Decode, constant.MOE}, cfg.InstanceManagerCfg.RequiredRoles)
				return true
			},
		},
		{
			name:      "inferMode EPD",
			inferMode: "EPD",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.NotEmpty(t, cfg.InstanceManagerCfg.RequiredRoles)
				assert.Equal(t, []string{constant.Encode, constant.Prefill, constant.Decode}, cfg.InstanceManagerCfg.RequiredRoles)
				return true
			},
		},
		{
			name:      "inferMode Hybrid",
			inferMode: "Hybrid",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.NotEmpty(t, cfg.InstanceManagerCfg.RequiredRoles)
				assert.Equal(t, []string{constant.Decode}, cfg.InstanceManagerCfg.RequiredRoles)
				return true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			InferMode = tt.inferMode
			success := generateRequiredRoles(tt.args.cfg)
			assert.Equal(t, tt.want, success)
			if success {
				assert.True(t, tt.expectCfgFunc(tt.args.cfg))
			}
		})
	}
}

func Test_generateMetricsCfg(t *testing.T) {
	type args struct {
		cfg *config.Config
	}
	tests := []struct {
		name          string
		inferMode     string
		args          args
		want          bool
		expectCfgFunc func(cfg *config.Config) bool
	}{
		{
			name:      "Hybrid Metrics config",
			inferMode: "Hybrid",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{},
					MetricsCfg: config.MetricsCfg{
						Enable:                         true,
						Duration:                       60,
						MetricsCollectorConfigFileName: "metrics_config_vllm_0.12.0_hybrid.yaml",
					},
					ServerConfig: config.ServerConfig{InferMode: constant.HybridMode},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.Equal(t, "metrics_config_vllm_0.12.0_hybrid.yaml", cfg.MetricsCfg.MetricsCollectorConfigFileName)
				return true
			},
		},
		{
			name:      "PD Metrics config",
			inferMode: "PD",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{},
					MetricsCfg: config.MetricsCfg{
						Enable:                         true,
						Duration:                       60,
						MetricsCollectorConfigFileName: "metrics_config_vllm_0.9.0.yaml",
					},
					ServerConfig: config.ServerConfig{InferMode: constant.PDMode},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.Equal(t, "metrics_config_vllm_0.9.0.yaml", cfg.MetricsCfg.MetricsCollectorConfigFileName)
				return true
			},
		},
		{
			name:      "Default Metrics config",
			inferMode: "PD",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.Equal(t, "", cfg.MetricsCfg.MetricsCollectorConfigFileName)
				return true
			},
		},
		{
			name:      "invalid Metrics config",
			inferMode: "PD",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{},
					MetricsCfg: config.MetricsCfg{
						Enable:                         true,
						Duration:                       -1,
						MetricsCollectorConfigFileName: "metrics_config_vllm_0.9.0.yaml",
					},
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			InferMode = tt.inferMode
			success := generateMetricsCfg(tt.args.cfg)
			assert.Equal(t, tt.want, success)
			if success {
				assert.True(t, tt.expectCfgFunc(tt.args.cfg))
			}
		})
	}
}

func Test_generateServerInferMode(t *testing.T) {
	type args struct {
		cfg *config.Config
	}
	tests := []struct {
		name          string
		inferMode     string
		args          args
		want          bool
		expectCfgFunc func(cfg *config.Config) bool
	}{
		{
			name:      "invalid inferMode",
			inferMode: "unknown",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{},
				},
			},
			want: false,
			expectCfgFunc: func(cfg *config.Config) bool {
				return cfg == nil
			},
		},
		{
			name:      "inferMode PD",
			inferMode: "PD",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.Equal(t, "PD", cfg.ServerConfig.InferMode)
				return true
			},
		},
		{
			name:      "inferMode PD-AM",
			inferMode: "PD-AM",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.Equal(t, "PD-AM", cfg.ServerConfig.InferMode)
				return true
			},
		},
		{
			name:      "inferMode EPD",
			inferMode: "EPD",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.Equal(t, "EPD", cfg.ServerConfig.InferMode)
				return true
			},
		},
		{
			name:      "inferMode Hybrid",
			inferMode: "Hybrid",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.Equal(t, "Hybrid", cfg.ServerConfig.InferMode)
				return true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			InferMode = tt.inferMode
			success := generateServerInferMode(tt.args.cfg)
			assert.Equal(t, tt.want, success)
			if success {
				assert.True(t, tt.expectCfgFunc(tt.args.cfg))
			}
		})
	}
}

func Test_generateProbeReadiness(t *testing.T) {
	type args struct {
		cfg *config.Config
	}
	tests := []struct {
		name          string
		readinessCfg  string
		args          args
		want          bool
		expectCfgFunc func(cfg *config.Config) bool
	}{
		{
			name:         "empty readinessCfg",
			readinessCfg: "",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RequiredRoles: []string{constant.Prefill, constant.Decode, constant.MOE},
					},
					ProbeCfg: config.ProbeCfg{},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.NotEmpty(t, cfg.ProbeCfg.Readiness)
				assert.Equal(t, map[string]int{
					"prefill": 1,
					"decode":  1,
					"moe":     1,
				}, cfg.ProbeCfg.Readiness)
				return true
			},
		},
		{
			name:         "error readinessCfg format",
			readinessCfg: "prefill:1%decode:2",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RequiredRoles: []string{constant.Prefill, constant.Decode, constant.MOE},
					},
					ProbeCfg: config.ProbeCfg{},
				},
			},
			want:          false,
			expectCfgFunc: nil,
		},
		{
			name:         "set readinessCfg except role moe",
			readinessCfg: "prefill:1#decode:2",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RequiredRoles: []string{constant.Prefill, constant.Decode, constant.MOE},
					},
					ProbeCfg: config.ProbeCfg{},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.NotEmpty(t, cfg.ProbeCfg.Readiness)
				assert.Equal(t, map[string]int{
					"prefill": 1,
					"decode":  2,
					"moe":     1,
				}, cfg.ProbeCfg.Readiness)
				return true
			},
		},
		{
			name:         "set readinessCfg for all roles",
			readinessCfg: "prefill:1#decode:2#moe:3",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RequiredRoles: []string{constant.Prefill, constant.Decode, constant.MOE},
					},
					ProbeCfg: config.ProbeCfg{},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.NotEmpty(t, cfg.ProbeCfg.Readiness)
				assert.Equal(t, map[string]int{
					"prefill": 1,
					"decode":  2,
					"moe":     3,
				}, cfg.ProbeCfg.Readiness)
				return true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ReadinessCfg = tt.readinessCfg
			success := generateProbeReadiness(tt.args.cfg)
			assert.Equal(t, tt.want, success)
			if success {
				assert.True(t, tt.expectCfgFunc(tt.args.cfg))
			}
		})
	}
}

func Test_parseStringToReadiness(t *testing.T) {
	type args struct {
		input string
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]int
		wantErr bool
	}{
		{
			name: "empty input",
			args: args{
				input: "",
			},
			wantErr: false,
			want:    map[string]int{},
		},
		{
			name: "valid input",
			args: args{
				input: "prefill:1#decode:2#moe:3",
			},
			wantErr: false,
			want: map[string]int{
				"prefill": 1,
				"decode":  2,
				"moe":     3,
			},
		},
		{
			name: "invalid input",
			args: args{
				input: "prefill:num1#decode:2#moe:3",
			},
			wantErr: true,
			want:    map[string]int{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStringToReadiness(tt.args.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseStringToReadiness() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseStringToReadiness() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_parseAddr(t *testing.T) {
	type args struct {
		addr string
	}
	tests := []struct {
		name     string
		args     args
		wantIp   string
		wantPort int
		wantErr  bool
	}{
		{
			name: "normal ip",
			args: args{
				addr: "0.0.0.0:6234",
			},
			wantIp:   "0.0.0.0",
			wantPort: 6234,
			wantErr:  false,
		},
		{
			name: "error ip",
			args: args{
				addr: "0.0.0.0:6234:",
			},
			wantIp:   "",
			wantPort: 0,
			wantErr:  true,
		},
		{
			name: "error port",
			args: args{
				addr: "0.0.0.0:676666",
			},
			wantIp:   "",
			wantPort: 0,
			wantErr:  true,
		},
		{
			name: "error ip",
			args: args{
				addr: "1.2.3.4.5:1234",
			},
			wantIp:   "",
			wantPort: 0,
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1, err := parseAddr(tt.args.addr)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseAddr() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.wantIp {
				t.Errorf("parseAddr() got = %v, want %v", got, tt.wantIp)
			}
			if got1 != tt.wantPort {
				t.Errorf("parseAddr() got1 = %v, want %v", got1, tt.wantPort)
			}
		})
	}
}

func Test_parseDomainAddr(t *testing.T) {
	type args struct {
		addr string
	}
	tests := []struct {
		name       string
		args       args
		wantDomain string
		wantPort   int
		wantErr    bool
	}{
		{
			name: "normal domain",
			args: args{
				addr: "localhost:1234",
			},
			wantDomain: "localhost",
			wantPort:   1234,
		},
		{
			name: "abnormal domain",
			args: args{
				addr: "localhost:123:1234",
			},
			wantErr:    true,
			wantDomain: "",
			wantPort:   0,
		},
		{
			name: "invalid port",
			args: args{
				addr: "localhost:1234555",
			},
			wantErr:    true,
			wantDomain: "",
			wantPort:   0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1, err := parseDomainAddr(tt.args.addr)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseDomainAddr() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.wantDomain {
				t.Errorf("parseDomainAddr() got = %v, want %v", got, tt.wantDomain)
			}
			if got1 != tt.wantPort {
				t.Errorf("parseDomainAddr() got1 = %v, want %v", got1, tt.wantPort)
			}
		})
	}
}

func Test_parseDomainAddresses(t *testing.T) {
	type args struct {
		addrList string
		addrName string
	}
	tests := []struct {
		name    string
		args    args
		want    []string
		wantErr bool
	}{
		{
			name: "normal domain",
			args: args{
				addrList: "localhost:1234,localhost:12345",
			},
			want: []string{"localhost:1234", "localhost:12345"},
		},
		{
			name: "abnormal domain",
			args: args{
				addrList: "localhost:::1234,localhost:12345",
			},
			want:    nil,
			wantErr: true,
		},

		{
			name: "emtpy domain",
			args: args{
				addrList: "",
			},
			want:    nil,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDomainAddresses(tt.args.addrList, tt.args.addrName)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseDomainAddresses() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseDomainAddresses() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_parseNamedInstances(t *testing.T) {
	type args struct {
		namedAddrs   string
		instanceType string
	}
	tests := []struct {
		name    string
		args    args
		want    map[string]map[string]string
		wantErr bool
	}{
		{
			name: "nil addr",
			args: args{
				namedAddrs:   "",
				instanceType: "decode",
			},
			wantErr: true,
			want:    nil,
		},
		{
			name: "normal case",
			args: args{
				namedAddrs:   "1.2.3.4:1234,2.3.4.5:2345",
				instanceType: "decode",
			},
			wantErr: false,
			want: map[string]map[string]string{
				"decode_1_2_3_4_1234": map[string]string{"default": "1.2.3.4:1234"},
				"decode_2_3_4_5_2345": map[string]string{"default": "2.3.4.5:2345"},
			},
		},
		{
			name: "error on invalid addr",
			args: args{
				namedAddrs:   "1.2.3.4:12341111,2.3.4.5:2345",
				instanceType: "decode",
			},
			wantErr: true,
			want:    nil,
		},
		{
			name: "error on invalid ip",
			args: args{
				namedAddrs:   "1.2.3.4.5:1234,2.3.4.5:2345",
				instanceType: "decode",
			},
			wantErr: true,
			want:    nil,
		},
		{
			name: "error on empty name",
			args: args{
				namedAddrs:   "abc#asdf:1234,2.3.4.5:2345",
				instanceType: "decode",
			},
			wantErr: true,
			want:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseNamedInstances(tt.args.namedAddrs, tt.args.instanceType)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseNamedInstances() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseNamedInstances() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_instances(t *testing.T) {
	type args struct {
		cfg *config.Config
	}
	tests := []struct {
		name          string
		inferMode     string
		encodeAddrs   string
		prefillAddrs  string
		decodeAddrs   string
		moeAddrs      string
		staticInfos   string
		args          args
		expectCfgFunc func(cfg *config.Config) bool
		wantSuccess   bool
	}{
		{
			name:         "empty",
			inferMode:    constant.PDMode,
			prefillAddrs: "",
			decodeAddrs:  "",
			wantSuccess:  true,
			args: args{
				cfg: nil,
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				return cfg == nil
			},
		},
		{
			name:         "prefill decode empty, moe not empty",
			inferMode:    constant.PDMode,
			prefillAddrs: "",
			decodeAddrs:  "",
			moeAddrs:     "0.0.0.0:1236",
			wantSuccess:  true,
			args: args{
				cfg: nil,
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				return cfg == nil
			},
		},
		{
			name:         "normal case",
			inferMode:    constant.PDMode,
			prefillAddrs: "0.0.0.0:1234#tag1",
			decodeAddrs:  "0.0.0.0:1235",
			staticInfos:  "api_server_count#13",
			wantSuccess:  true,
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RouteTable: config.RouteTable{
							InstanceRoutes: map[string]config.Route{},
						},
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.Equal(t, map[string]config.Route{
					"prefill_0_0_0_0_1234": {
						Ip:   "0.0.0.0",
						Port: 1234,
						Role: constant.Prefill,
						StaticInfos: map[string]string{
							"api_server_count": "13",
						},
						Tag: "tag1",
					},
					"decode_0_0_0_0_1235": {
						Ip:   "0.0.0.0",
						Port: 1235,
						Role: constant.Decode,
						StaticInfos: map[string]string{
							"api_server_count": "13",
						},
						Tag: "default",
					},
				}, cfg.InstanceManagerCfg.RouteTable.InstanceRoutes)

				return true
			},
		},
		{
			name:         "normal case, infer mode PD-AM",
			inferMode:    constant.PDAMMode,
			prefillAddrs: "0.0.0.0:1234#tag1",
			decodeAddrs:  "0.0.0.0:1235",
			moeAddrs:     "0.0.0.0:1236",
			staticInfos:  "api_server_count#13",
			wantSuccess:  true,
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RouteTable: config.RouteTable{
							InstanceRoutes: map[string]config.Route{},
						},
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.Equal(t, map[string]config.Route{
					"prefill_0_0_0_0_1234": {
						Ip:   "0.0.0.0",
						Port: 1234,
						Role: constant.Prefill,
						StaticInfos: map[string]string{
							"api_server_count": "13",
						},
						Tag: "tag1",
					},
					"decode_0_0_0_0_1235": {
						Ip:   "0.0.0.0",
						Port: 1235,
						Role: constant.Decode,
						StaticInfos: map[string]string{
							"api_server_count": "13",
						},
						Tag: "default",
					},
					"moe_0_0_0_0_1236": {
						Ip:          "0.0.0.0",
						Port:        1236,
						Role:        constant.MOE,
						StaticInfos: nil,
						Tag:         "default",
					},
				}, cfg.InstanceManagerCfg.RouteTable.InstanceRoutes)

				return true
			},
		},
		{
			name:         "normal case, infer mode EPD",
			inferMode:    constant.EPDMode,
			prefillAddrs: "0.0.0.0:1234#tag1",
			decodeAddrs:  "0.0.0.0:1235",
			encodeAddrs:  "0.0.0.0:1236",
			staticInfos:  "api_server_count#13",
			wantSuccess:  true,
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RouteTable: config.RouteTable{
							InstanceRoutes: map[string]config.Route{},
						},
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.Equal(t, map[string]config.Route{
					"prefill_0_0_0_0_1234": {
						Ip:   "0.0.0.0",
						Port: 1234,
						Role: constant.Prefill,
						StaticInfos: map[string]string{
							"api_server_count": "13",
						},
						Tag: "tag1",
					},
					"decode_0_0_0_0_1235": {
						Ip:   "0.0.0.0",
						Port: 1235,
						Role: constant.Decode,
						StaticInfos: map[string]string{
							"api_server_count": "13",
						},
						Tag: "default",
					},
					"encode_0_0_0_0_1236": {
						Ip:          "0.0.0.0",
						Port:        1236,
						Role:        constant.Encode,
						StaticInfos: nil,
						Tag:         "default",
					},
				}, cfg.InstanceManagerCfg.RouteTable.InstanceRoutes)

				return true
			},
		},
		{
			name:        "normal case, infer mode Hybrid",
			inferMode:   constant.HybridMode,
			decodeAddrs: "0.0.0.0:1235",
			wantSuccess: true,
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RouteTable: config.RouteTable{
							InstanceRoutes: map[string]config.Route{},
						},
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.Equal(t, map[string]config.Route{
					"decode_0_0_0_0_1235": {
						Ip:   "0.0.0.0",
						Port: 1235,
						Role: constant.Decode,
						Tag:  "default",
					},
				}, cfg.InstanceManagerCfg.RouteTable.InstanceRoutes)

				return true
			},
		},
		{
			name:         "normal case, infer mode PD-AM, prefill decode addr empty",
			inferMode:    constant.PDAMMode,
			prefillAddrs: "",
			decodeAddrs:  "",
			moeAddrs:     "0.0.0.0:1236",
			staticInfos:  "api_server_count#13",
			wantSuccess:  true,
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RouteTable: config.RouteTable{
							InstanceRoutes: map[string]config.Route{},
						},
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.Equal(t, map[string]config.Route{
					"moe_0_0_0_0_1236": {
						Ip:          "0.0.0.0",
						Port:        1236,
						Role:        constant.MOE,
						StaticInfos: nil,
						Tag:         "default",
					},
				}, cfg.InstanceManagerCfg.RouteTable.InstanceRoutes)

				return true
			},
		},
		{
			name:         "normal case without tag",
			inferMode:    constant.PDMode,
			prefillAddrs: "p0#0.0.0.0:1234",
			decodeAddrs:  "0.0.0.0:1235",
			staticInfos:  "api_server_count#13",
			wantSuccess:  true,
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RouteTable: config.RouteTable{
							InstanceRoutes: map[string]config.Route{},
						},
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.Equal(t, map[string]config.Route{
					"p0": {
						Ip:   "0.0.0.0",
						Port: 1234,
						Role: constant.Prefill,
						StaticInfos: map[string]string{
							"api_server_count": "13",
						},
						Tag: "default",
					},
					"decode_0_0_0_0_1235": {
						Ip:   "0.0.0.0",
						Port: 1235,
						Role: constant.Decode,
						StaticInfos: map[string]string{
							"api_server_count": "13",
						},
						Tag: "default",
					},
				}, cfg.InstanceManagerCfg.RouteTable.InstanceRoutes)

				return true
			},
		},
		{
			name:         "invalid prefills",
			inferMode:    constant.PDMode,
			prefillAddrs: "0.0.0.0:1234,124512",
			decodeAddrs:  "0.0.0.0:1235",
			wantSuccess:  false,
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RouteTable: config.RouteTable{
							InstanceRoutes: map[string]config.Route{},
						},
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				return true
			},
		},
		{
			name:         "invalid decodes",
			inferMode:    constant.PDMode,
			prefillAddrs: "0.0.0.0:1234",
			decodeAddrs:  "0.0.0.0:1235,124512",
			wantSuccess:  false,
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RouteTable: config.RouteTable{
							InstanceRoutes: map[string]config.Route{},
						},
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				return true
			},
		},
		{
			name:         "invalid moes",
			inferMode:    constant.PDAMMode,
			prefillAddrs: "0.0.0.0:1234",
			decodeAddrs:  "0.0.0.0:1235",
			moeAddrs:     "0.0.0.0:1236,124512",
			wantSuccess:  false,
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RouteTable: config.RouteTable{
							InstanceRoutes: map[string]config.Route{},
						},
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				return true
			},
		},
		{
			name:         "invalid encodes",
			inferMode:    constant.EPDMode,
			prefillAddrs: "0.0.0.0:1234",
			decodeAddrs:  "0.0.0.0:1235",
			encodeAddrs:  "0.0.0.0:1236,124512",
			wantSuccess:  false,
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RouteTable: config.RouteTable{
							InstanceRoutes: map[string]config.Route{},
						},
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				return true
			},
		},
		{
			name:        "invalid PDHybrid",
			inferMode:   constant.HybridMode,
			decodeAddrs: "0.0.0.0:1235,12",
			wantSuccess: false,
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RouteTable: config.RouteTable{
							InstanceRoutes: map[string]config.Route{},
						},
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				return true
			},
		},
		{
			name:         "invalid static infos",
			inferMode:    constant.PDMode,
			prefillAddrs: "0.0.0.0:1234",
			decodeAddrs:  "0.0.0.0:1235",
			wantSuccess:  false,
			staticInfos:  "api_server_count13",
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						RouteTable: config.RouteTable{
							InstanceRoutes: map[string]config.Route{},
						},
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				return true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			PrefillAddrs = tt.prefillAddrs
			DecodeAddrs = tt.decodeAddrs
			MoeAddrs = tt.moeAddrs
			EncodeAddrs = tt.encodeAddrs
			InferMode = tt.inferMode
			StaticInfos = tt.staticInfos
			gotSuccess := instances(tt.args.cfg)
			assert.Equal(t, tt.wantSuccess, gotSuccess, "instances() = %v, want %v", gotSuccess, tt.wantSuccess)
			if gotSuccess {
				assert.True(t, tt.expectCfgFunc(tt.args.cfg))
			}
		})
	}
}

func Test_loadFromFile(t *testing.T) {
	t.Run("file_not_found", func(t *testing.T) {
		_, err := loadFromFile("/nonexistent/path/config.yaml")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "load config file failed")
	})

	t.Run("invalid_yaml", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		err := os.WriteFile(configPath, []byte("invalid: [yaml: content"), 0644)
		assert.NoError(t, err)
		_, err = loadFromFile(configPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unmarshal config file failed")
	})

	t.Run("valid_config", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		content := `
server:
  name: test-server
  addr: 127.0.0.1
  http-port: "8080"
`
		err := os.WriteFile(configPath, []byte(content), 0644)
		assert.NoError(t, err)
		cfg, err := loadFromFile(configPath)
		assert.NoError(t, err)
		assert.NotNil(t, cfg)
		assert.Equal(t, "test-server", cfg.ServerConfig.Name)
		assert.Equal(t, "127.0.0.1", cfg.ServerConfig.Addr)
		assert.Equal(t, "8080", cfg.ServerConfig.HttpPort)
	})
}

func Test_modifyClusterConfig(t *testing.T) {
	type args struct {
		cfg *config.Config
	}
	tests := []struct {
		name          string
		args          args
		wantSuccess   bool
		isCandidate   string
		etcdEndpoints string
		mode          string
		expectCfgFunc func(cfg *config.Config) bool
	}{
		{
			name:        "invalid bool",
			isCandidate: "not a bool",
			wantSuccess: false,
		},
		{
			name:          "empty etcd",
			isCandidate:   "True",
			etcdEndpoints: "",
			wantSuccess:   true,
			args: args{
				cfg: &config.Config{
					ClusterCfg: config.ClusterConfig{
						RoleCandidate: false,
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.True(t, cfg.ClusterCfg.RoleCandidate)
				return true
			},
		},
		{
			name:          "etcd normal case",
			isCandidate:   "True",
			etcdEndpoints: "etcd_host:1234,etcd_host:1235",
			wantSuccess:   true,
			args: args{
				cfg: &config.Config{
					ClusterCfg: config.ClusterConfig{
						RoleCandidate: false,
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.True(t, cfg.ClusterCfg.RoleCandidate)
				assert.Equal(t, []string{"etcd_host:1234", "etcd_host:1235"}, cfg.ClusterCfg.EtcdCfg.EtcdEndpoints)
				return true
			},
		},

		{
			name:        "etcd normal case",
			isCandidate: "False",
			wantSuccess: true,
			args: args{
				cfg: &config.Config{
					ClusterCfg: config.ClusterConfig{
						RoleCandidate: true,
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.False(t, cfg.ClusterCfg.RoleCandidate)
				return true
			},
		},
		{
			name:          "etcd etcd not nil cfg",
			isCandidate:   "True",
			etcdEndpoints: "etcd_host:1234,etcd_host:1235",
			wantSuccess:   true,
			args: args{
				cfg: &config.Config{
					ClusterCfg: config.ClusterConfig{
						RoleCandidate: false,
						EtcdCfg: &config.EtcdConfig{
							EtcdEndpoints: []string{},
						},
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.True(t, cfg.ClusterCfg.RoleCandidate)
				assert.Equal(t, []string{"etcd_host:1234", "etcd_host:1235"}, cfg.ClusterCfg.EtcdCfg.EtcdEndpoints)
				return true
			},
		},
		{
			name:          "etcd invalid domain",
			isCandidate:   "True",
			etcdEndpoints: "etcd_host:12345555,etcd_host:1235",
			wantSuccess:   false,
			args: args{
				cfg: &config.Config{
					ClusterCfg: config.ClusterConfig{
						RoleCandidate: false,
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				return true
			},
		},
		{
			name:        "cluster mode singleton normal case",
			isCandidate: "False",
			mode:        "singleton",
			wantSuccess: true,
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						HealthCheckConfig: &config.HealthCheckConfig{
							EventThresholdIntervalMs: 0,
						},
					},
					ClusterCfg: config.ClusterConfig{
						RoleCandidate: false,
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				return true
			},
		},
		{
			name:          "cluster mode etcd normal case",
			etcdEndpoints: "etcd_host:1234,etcd_host:1235",
			isCandidate:   "True",
			mode:          "etcd",
			wantSuccess:   true,
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						HealthCheckConfig: &config.HealthCheckConfig{
							EventThresholdIntervalMs: 0,
						},
					},
					ClusterCfg: config.ClusterConfig{
						RoleCandidate: false,
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				return true
			},
		},
		{
			name:          "cluster mode etcd invalid case",
			etcdEndpoints: "etcd_host:12345555,etcd_host:1235",
			isCandidate:   "True",
			mode:          "etcd",
			wantSuccess:   false,
			args: args{
				cfg: &config.Config{
					InstanceManagerCfg: config.InstanceManagerCfg{
						HealthCheckConfig: &config.HealthCheckConfig{
							EventThresholdIntervalMs: 0,
						},
					},
					ClusterCfg: config.ClusterConfig{
						RoleCandidate: false,
					},
				},
			},
			expectCfgFunc: func(cfg *config.Config) bool {
				return true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			IsCandidate = tt.isCandidate
			EtcdEndpoints = tt.etcdEndpoints
			Mode = tt.mode
			success := modifyClusterConfig(tt.args.cfg)
			assert.Equal(t, tt.wantSuccess, success, "modifyClusterConfig() = %v, want %v", success, tt.wantSuccess)
			if success {
				assert.True(t, tt.expectCfgFunc(tt.args.cfg))
			}
		})
	}
}

func Test_parseArgs(t *testing.T) {
	tests := []struct {
		name string
	}{
		{
			"test",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parseArgs()
		})
	}
}

func Test_remoteSchedule(t *testing.T) {
	type args struct {
		cfg *config.Config
	}
	tests := []struct {
		name          string
		remoteType    string
		endPointVal   string
		args          args
		want          bool
		expectCfgFunc func(cfg *config.Config) bool
	}{
		{
			name:       "empty type",
			remoteType: "",
			args: args{
				cfg: nil,
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				return cfg == nil
			},
		},
		{
			name:       "unknown type",
			remoteType: "unknown",
			args: args{
				cfg: &config.Config{
					SchedulersCfg: config.SchedulersCfg{
						RemoteSchedule: nil,
					},
				},
			},
			want: false,
			expectCfgFunc: func(cfg *config.Config) bool {
				return cfg == nil
			},
		},
		{
			name:       "disable, nil remote",
			remoteType: "disable",
			args: args{
				cfg: &config.Config{
					SchedulersCfg: config.SchedulersCfg{
						RemoteSchedule: nil,
					},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.NotNil(t, cfg.SchedulersCfg.RemoteSchedule)
				assert.False(t, cfg.SchedulersCfg.RemoteSchedule.Enabled)
				return true
			},
		},
		{
			name:        "enable, nil endpoint",
			remoteType:  scheduler.EndpointType,
			endPointVal: "http://test:1234",
			args: args{
				cfg: &config.Config{
					SchedulersCfg: config.SchedulersCfg{
						RemoteSchedule: nil,
					},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.NotNil(t, cfg.SchedulersCfg.RemoteSchedule)
				assert.True(t, cfg.SchedulersCfg.RemoteSchedule.Enabled)
				assert.Equal(t, "http://test:1234", cfg.SchedulersCfg.RemoteSchedule.RemoteEndpoint)
				assert.Equal(t, scheduler.EndpointType, cfg.SchedulersCfg.RemoteSchedule.RemoteType)
				assert.False(t, cfg.SchedulersCfg.RemoteSchedule.NeedContent)
				return true
			},
		},
		{
			name:       "enable, endpoint",
			remoteType: scheduler.EndpointType,
			args: args{
				cfg: &config.Config{
					ServerConfig: config.ServerConfig{
						Addr:     "test",
						HttpPort: "1234",
					},
					SchedulersCfg: config.SchedulersCfg{
						RemoteSchedule: nil,
					},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.NotNil(t, cfg.SchedulersCfg.RemoteSchedule)
				assert.True(t, cfg.SchedulersCfg.RemoteSchedule.Enabled)
				assert.Equal(t, cfg.SchedulersCfg.RemoteSchedule.RemoteEndpoint, "http://test:1234")
				assert.Equal(t, cfg.SchedulersCfg.RemoteSchedule.RemoteType, scheduler.EndpointType)
				assert.False(t, cfg.SchedulersCfg.RemoteSchedule.NeedContent)
				return true
			},
		},
		{
			name:       "enable, cluster",
			remoteType: scheduler.ClusterLeaderType,
			args: args{
				cfg: &config.Config{
					SchedulersCfg: config.SchedulersCfg{
						RemoteSchedule: nil,
					},
				},
			},
			want: true,
			expectCfgFunc: func(cfg *config.Config) bool {
				assert.NotNil(t, cfg.SchedulersCfg.RemoteSchedule)
				assert.True(t, cfg.SchedulersCfg.RemoteSchedule.Enabled)
				assert.Equal(t, cfg.SchedulersCfg.RemoteSchedule.RemoteEndpoint, "")
				assert.Equal(t, cfg.SchedulersCfg.RemoteSchedule.RemoteType, scheduler.ClusterLeaderType)
				assert.False(t, cfg.SchedulersCfg.RemoteSchedule.NeedContent)
				return true
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			RemoteSchedulerType = tt.remoteType
			RemoteSchedulerEndpoint = tt.endPointVal
			success := remoteSchedule(tt.args.cfg)
			assert.Equal(t, tt.want, success)
			if success {
				assert.True(t, tt.expectCfgFunc(tt.args.cfg))
			}
		})
	}
}

func testMainProcs(t *testing.T) {
	testDir := t.TempDir()
	testDir = filepath.FromSlash(testDir)
	err := os.MkdirAll(filepath.Join(testDir, "target_config", "config"), 0755)
	assert.NoError(t, err)
	initCfg := &config.Config{
		ServerConfig: config.ServerConfig{
			Addr:     "127.0.0.1",
			HttpPort: "1234",
		},
		InstanceManagerCfg: config.InstanceManagerCfg{
			HealthCheckConfig: &config.HealthCheckConfig{},
		},
	}
	cfgBytes, err := yaml.Marshal(initCfg)
	assert.NoError(t, err)
	TemplatePath = filepath.Join(testDir, "config.yaml")
	DeployTargetPath = filepath.Join(testDir, "target_config")
	Name = "route1"
	Port = 1234
	Host = "0.0.0.0"
	PrefillAddrs = "1.2.3.4:1234"
	DecodeAddrs = "2.3.4.5:2345"
	UseDomainForLeader = "false"
	IsCandidate = "True"

	expectTargetFilePath := filepath.Join(testDir, "target_config", "config", "model-orch.yaml")
	err = os.WriteFile(filepath.Join(testDir, "config.yaml"), cfgBytes, 0o600)
	assert.NoError(t, err)
	success := mainProc(false)
	assert.True(t, success)

	info, err := os.Stat(expectTargetFilePath)
	assert.NoError(t, err)
	assert.False(t, info.IsDir())
}

func Test_applyServerConfig(t *testing.T) {
	// Save and restore global variables
	saveName := Name
	saveHost := Host
	savePort := Port
	saveOpsPort := OpsPort
	saveUseDomainForLeader := UseDomainForLeader
	saveDomain := Domain
	saveLogFilePath := LogFilePath
	saveDeployTargetPath := DeployTargetPath
	defer func() {
		Name = saveName
		Host = saveHost
		Port = savePort
		OpsPort = saveOpsPort
		UseDomainForLeader = saveUseDomainForLeader
		Domain = saveDomain
		LogFilePath = saveLogFilePath
		DeployTargetPath = saveDeployTargetPath
	}()

	Name = "test-rs"
	Host = "192.168.1.1"
	Port = 9800
	Domain = "my-domain"
	LogFilePath = ""
	DeployTargetPath = "/tmp/test-deploy"

	t.Run("ops_port_not_set", func(t *testing.T) {
		OpsPort = 0
		UseDomainForLeader = "false"
		cfg := &config.Config{
			ServerConfig:  config.ServerConfig{},
			PeerClientCfg: &config.PeerClientConfig{},
		}
		success := applyServerConfig(cfg)
		assert.True(t, success)
		assert.Equal(t, "test-rs", cfg.ServerConfig.Name)
		assert.Equal(t, "192.168.1.1", cfg.ServerConfig.Addr)
		assert.Equal(t, "9800", cfg.ServerConfig.HttpPort)
		assert.Empty(t, cfg.ServerConfig.OpsPort, "OpsPort should be empty when not set")
		assert.False(t, cfg.PeerClientCfg.SendWithDomain)
	})

	t.Run("ops_port_set", func(t *testing.T) {
		OpsPort = 9810
		UseDomainForLeader = "true"
		cfg := &config.Config{
			ServerConfig:  config.ServerConfig{},
			PeerClientCfg: &config.PeerClientConfig{},
		}
		success := applyServerConfig(cfg)
		assert.True(t, success)
		assert.Equal(t, "test-rs", cfg.ServerConfig.Name)
		assert.Equal(t, "192.168.1.1", cfg.ServerConfig.Addr)
		assert.Equal(t, "9800", cfg.ServerConfig.HttpPort)
		assert.Equal(t, "9810", cfg.ServerConfig.OpsPort, "OpsPort should be set when provided")
		assert.True(t, cfg.PeerClientCfg.SendWithDomain)
	})

	t.Run("invalid_use_domain_for_leader", func(t *testing.T) {
		OpsPort = 0
		UseDomainForLeader = "not-a-bool"
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyServerConfig(cfg)
		assert.False(t, success)
	})

	t.Run("peer_client_cfg_nil", func(t *testing.T) {
		OpsPort = 0
		UseDomainForLeader = "false"
		cfg := &config.Config{
			ServerConfig:  config.ServerConfig{},
			PeerClientCfg: nil,
		}
		success := applyServerConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.PeerClientCfg)
		assert.Equal(t, "my-domain", cfg.PeerClientCfg.Domain)
	})

	t.Run("log_file_path_set", func(t *testing.T) {
		OpsPort = 0
		UseDomainForLeader = "false"
		LogFilePath = "/custom/path/route-server.log"
		defer func() { LogFilePath = "" }()
		cfg := &config.Config{
			ServerConfig:  config.ServerConfig{},
			PeerClientCfg: &config.PeerClientConfig{},
		}
		success := applyServerConfig(cfg)
		assert.True(t, success)
		assert.Equal(t, "/custom/path/route-server.log", cfg.LogConfig.Filename)
	})

	t.Run("client_tls_config_fails", func(t *testing.T) {
		OpsPort = 0
		UseDomainForLeader = "false"
		ClientHTTPSEnable = "not-a-bool"
		defer func() { ClientHTTPSEnable = "" }()
		cfg := &config.Config{
			ServerConfig:  config.ServerConfig{},
			PeerClientCfg: &config.PeerClientConfig{},
		}
		success := applyServerConfig(cfg)
		assert.False(t, success)
	})

	t.Run("etcd_tls_config_fails", func(t *testing.T) {
		OpsPort = 0
		UseDomainForLeader = "false"
		EtcdTLSEnable = "not-a-bool"
		defer func() { EtcdTLSEnable = "" }()
		cfg := &config.Config{
			ServerConfig:  config.ServerConfig{},
			PeerClientCfg: &config.PeerClientConfig{},
			ClusterCfg: config.ClusterConfig{
				EtcdCfg: &config.EtcdConfig{},
			},
		}
		success := applyServerConfig(cfg)
		assert.False(t, success)
	})
}

func Test_mainProcsOnHybrid(t *testing.T) {
	InferMode = constant.HybridMode
	testMainProcs(t)
}

func Test_mainProcsOnPD(t *testing.T) {
	InferMode = constant.PDMode
	testMainProcs(t)
}

// ============================================================
// Test_mainProc_error branch coverage
// ============================================================

// saveGlobals / restoreGlobals helper functions to save and restore global variables
func saveGlobals() func() {
	saved := struct {
		templatePath        string
		deployTargetPath    string
		name                string
		port                uint
		host                string
		prefillAddrs        string
		decodeAddrs         string
		moeAddrs            string
		encodeAddrs         string
		staticInfos         string
		useDomainForLeader  string
		isCandidate         string
		domain              string
		logFilePath         string
		mode                string
		inferMode           string
		remoteSchedulerType string
		readinessCfg        string
		opsPort             uint
		opsTLSCertFile      string
		opsTLSKeyFile       string
		opsTLSCaCertFile    string
		tlsCertFile         string
		tlsKeyFile          string
		tlsCaCertFile       string
		tlsSkipVerify       string
		enableMTLS          string
	}{
		templatePath:        TemplatePath,
		deployTargetPath:    DeployTargetPath,
		name:                Name,
		port:                Port,
		host:                Host,
		prefillAddrs:        PrefillAddrs,
		decodeAddrs:         DecodeAddrs,
		moeAddrs:            MoeAddrs,
		encodeAddrs:         EncodeAddrs,
		staticInfos:         StaticInfos,
		useDomainForLeader:  UseDomainForLeader,
		isCandidate:         IsCandidate,
		domain:              Domain,
		logFilePath:         LogFilePath,
		mode:                Mode,
		inferMode:           InferMode,
		remoteSchedulerType: RemoteSchedulerType,
		readinessCfg:        ReadinessCfg,
		opsPort:             OpsPort,
		opsTLSCertFile:      OpsTLSCertFile,
		opsTLSKeyFile:       OpsTLSKeyFile,
		opsTLSCaCertFile:    OpsTLSCaCertFile,
		tlsCertFile:         TLSCertFile,
		tlsKeyFile:          TLSKeyFile,
		tlsCaCertFile:       TLSCaCertFile,
		tlsSkipVerify:       TLSSkipVerify,
		enableMTLS:          EnableMTLS,
	}
	return func() {
		TemplatePath = saved.templatePath
		DeployTargetPath = saved.deployTargetPath
		Name = saved.name
		Port = saved.port
		Host = saved.host
		PrefillAddrs = saved.prefillAddrs
		DecodeAddrs = saved.decodeAddrs
		MoeAddrs = saved.moeAddrs
		EncodeAddrs = saved.encodeAddrs
		StaticInfos = saved.staticInfos
		UseDomainForLeader = saved.useDomainForLeader
		IsCandidate = saved.isCandidate
		Domain = saved.domain
		LogFilePath = saved.logFilePath
		Mode = saved.mode
		InferMode = saved.inferMode
		RemoteSchedulerType = saved.remoteSchedulerType
		ReadinessCfg = saved.readinessCfg
		OpsPort = saved.opsPort
		OpsTLSCertFile = saved.opsTLSCertFile
		OpsTLSKeyFile = saved.opsTLSKeyFile
		OpsTLSCaCertFile = saved.opsTLSCaCertFile
		TLSCertFile = saved.tlsCertFile
		TLSKeyFile = saved.tlsKeyFile
		TLSCaCertFile = saved.tlsCaCertFile
		TLSSkipVerify = saved.tlsSkipVerify
		EnableMTLS = saved.enableMTLS
	}
}

func setupBasicGlobals(testDir string) {
	TemplatePath = filepath.Join(testDir, "config.yaml")
	DeployTargetPath = filepath.Join(testDir, "target_config")
	Name = "route1"
	Port = 1234
	Host = "0.0.0.0"
	PrefillAddrs = "1.2.3.4:1234"
	DecodeAddrs = "2.3.4.5:2345"
	UseDomainForLeader = "false"
	IsCandidate = "True"
	RemoteSchedulerType = "disable"
	ReadinessCfg = ""
	OpsPort = 0
	StaticInfos = ""
	MoeAddrs = ""
	EncodeAddrs = ""
	Domain = ""
	LogFilePath = ""
	Mode = ""
	InferMode = constant.PDMode
	OpsTLSCertFile = ""
	OpsTLSKeyFile = ""
	OpsTLSCaCertFile = ""
	TLSCertFile = ""
	TLSKeyFile = ""
	TLSCaCertFile = ""
	TLSSkipVerify = ""
	EnableMTLS = ""
}

func Test_mainProc_loadConfig_DeployTargetPathEmpty(t *testing.T) {
	defer saveGlobals()()
	testDir := t.TempDir()
	setupBasicGlobals(testDir)
	DeployTargetPath = ""
	success := mainProc(false)
	assert.False(t, success)
}

func Test_mainProc_loadConfig_checkValidFails(t *testing.T) {
	defer saveGlobals()()
	testDir := t.TempDir()
	setupBasicGlobals(testDir)
	// Make checkValid fail: set Host to an invalid IP
	Host = "invalid ip"
	success := mainProc(false)
	assert.False(t, success)
}

func Test_mainProc_loadConfig_loadFromFileFails(t *testing.T) {
	defer saveGlobals()()
	testDir := t.TempDir()
	setupBasicGlobals(testDir)
	// Template file does not exist -> loadFromFile fails
	TemplatePath = filepath.Join(testDir, "nonexistent.yaml")
	success := mainProc(false)
	assert.False(t, success)
}

func Test_applyTLSConfig(t *testing.T) {
	// Save and restore global variables
	saveTLSCertFile := TLSCertFile
	saveTLSKeyFile := TLSKeyFile
	saveTLSCaCertFile := TLSCaCertFile
	saveTLSSkipVerify := TLSSkipVerify
	saveEnableMTLS := EnableMTLS
	defer func() {
		TLSCertFile = saveTLSCertFile
		TLSKeyFile = saveTLSKeyFile
		TLSCaCertFile = saveTLSCaCertFile
		TLSSkipVerify = saveTLSSkipVerify
		EnableMTLS = saveEnableMTLS
	}()

	t.Run("all_empty", func(t *testing.T) {
		TLSCertFile = ""
		TLSKeyFile = ""
		TLSCaCertFile = ""
		TLSSkipVerify = ""
		EnableMTLS = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyTLSConfig(cfg)
		assert.True(t, success)
		assert.Nil(t, cfg.ServerConfig.TLSConfig)
	})

	t.Run("tls_cert_and_key", func(t *testing.T) {
		TLSCertFile = "/etc/certs/server.crt"
		TLSKeyFile = "/etc/certs/server.key"
		TLSCaCertFile = ""
		TLSSkipVerify = ""
		EnableMTLS = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.ServerConfig.TLSConfig)
		assert.Equal(t, "/etc/certs/server.crt", cfg.ServerConfig.TLSConfig.CertFile)
		assert.Equal(t, "/etc/certs/server.key", cfg.ServerConfig.TLSConfig.KeyFile)
		assert.Empty(t, cfg.ServerConfig.TLSConfig.CaFile)
		assert.False(t, cfg.ServerConfig.TLSConfig.EnableMTLS)
		assert.False(t, cfg.ServerConfig.TLSConfig.SkipVerify)
	})

	t.Run("tls_with_ca_and_mtls", func(t *testing.T) {
		TLSCertFile = "/etc/certs/server.crt"
		TLSKeyFile = "/etc/certs/server.key"
		TLSCaCertFile = "/etc/certs/ca.crt"
		TLSSkipVerify = ""
		EnableMTLS = "true"
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.ServerConfig.TLSConfig)
		assert.Equal(t, "/etc/certs/server.crt", cfg.ServerConfig.TLSConfig.CertFile)
		assert.Equal(t, "/etc/certs/server.key", cfg.ServerConfig.TLSConfig.KeyFile)
		assert.Equal(t, "/etc/certs/ca.crt", cfg.ServerConfig.TLSConfig.CaFile)
		assert.True(t, cfg.ServerConfig.TLSConfig.EnableMTLS)
	})

	t.Run("invalid_enable_mtls", func(t *testing.T) {
		TLSCertFile = "/etc/certs/server.crt"
		TLSKeyFile = "/etc/certs/server.key"
		TLSCaCertFile = ""
		TLSSkipVerify = ""
		EnableMTLS = "not-a-bool"
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyTLSConfig(cfg)
		assert.False(t, success)
	})

	t.Run("tls_ca_only_no_cert", func(t *testing.T) {
		TLSCertFile = ""
		TLSKeyFile = ""
		TLSCaCertFile = "/etc/certs/ca.crt"
		TLSSkipVerify = ""
		EnableMTLS = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.ServerConfig.TLSConfig)
		assert.Equal(t, "/etc/certs/ca.crt", cfg.ServerConfig.TLSConfig.CaFile)
	})

	t.Run("existing_tls_config_partial_update", func(t *testing.T) {
		TLSCertFile = "/new/cert.crt"
		TLSKeyFile = ""
		TLSCaCertFile = "/new/ca.crt"
		TLSSkipVerify = ""
		EnableMTLS = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{
				TLSConfig: &config.TLSCertificateConfig{
					CertFile: "/old/cert.crt",
					KeyFile:  "/old/key.key",
				},
			},
		}
		success := applyTLSConfig(cfg)
		assert.True(t, success)
		// CertFile should be updated (non-empty input)
		assert.Equal(t, "/new/cert.crt", cfg.ServerConfig.TLSConfig.CertFile)
		// KeyFile should remain unchanged (empty input)
		assert.Equal(t, "/old/key.key", cfg.ServerConfig.TLSConfig.KeyFile)
		// CaFile should be set to new value
		assert.Equal(t, "/new/ca.crt", cfg.ServerConfig.TLSConfig.CaFile)
	})

	t.Run("skip_verify_true", func(t *testing.T) {
		TLSCertFile = "/etc/certs/server.crt"
		TLSKeyFile = "/etc/certs/server.key"
		TLSCaCertFile = ""
		TLSSkipVerify = "true"
		EnableMTLS = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.ServerConfig.TLSConfig)
		assert.True(t, cfg.ServerConfig.TLSConfig.SkipVerify)
	})

	t.Run("skip_verify_false", func(t *testing.T) {
		TLSCertFile = "/etc/certs/server.crt"
		TLSKeyFile = "/etc/certs/server.key"
		TLSCaCertFile = ""
		TLSSkipVerify = "false"
		EnableMTLS = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.ServerConfig.TLSConfig)
		assert.False(t, cfg.ServerConfig.TLSConfig.SkipVerify)
	})

	t.Run("invalid_skip_verify", func(t *testing.T) {
		TLSCertFile = "/etc/certs/server.crt"
		TLSKeyFile = "/etc/certs/server.key"
		TLSCaCertFile = ""
		TLSSkipVerify = "not-a-bool"
		EnableMTLS = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyTLSConfig(cfg)
		assert.False(t, success)
	})
}

func Test_applyOpsTLSConfig(t *testing.T) {
	saveHTTPSEnable := OpsHTTPSEnable
	saveMTLSEnable := OpsMTLSEnable
	saveTLSCertFile := OpsTLSCertFile
	saveTLSKeyFile := OpsTLSKeyFile
	saveTLSCaCertFile := OpsTLSCaCertFile
	defer func() {
		OpsHTTPSEnable = saveHTTPSEnable
		OpsMTLSEnable = saveMTLSEnable
		OpsTLSCertFile = saveTLSCertFile
		OpsTLSKeyFile = saveTLSKeyFile
		OpsTLSCaCertFile = saveTLSCaCertFile
	}()

	t.Run("all_empty", func(t *testing.T) {
		OpsHTTPSEnable = ""
		OpsMTLSEnable = ""
		OpsTLSCertFile = ""
		OpsTLSKeyFile = ""
		OpsTLSCaCertFile = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyOpsTLSConfig(cfg)
		assert.True(t, success)
		assert.Nil(t, cfg.ServerConfig.OpsTLSConfig)
	})

	t.Run("ops_enable_https_false_skip_all", func(t *testing.T) {
		OpsHTTPSEnable = "false"
		OpsMTLSEnable = ""
		OpsTLSCertFile = "/etc/certs/ops.crt"
		OpsTLSKeyFile = "/etc/certs/ops.key"
		OpsTLSCaCertFile = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyOpsTLSConfig(cfg)
		assert.True(t, success)
		// enable-https=false skips all ops TLS config, OpsTLSConfig should remain nil
		assert.Nil(t, cfg.ServerConfig.OpsTLSConfig)
	})

	t.Run("ops_enable_https_false_with_existing_config", func(t *testing.T) {
		OpsHTTPSEnable = "false"
		OpsMTLSEnable = ""
		OpsTLSCertFile = ""
		OpsTLSKeyFile = ""
		OpsTLSCaCertFile = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{
				OpsTLSConfig: &config.TLSCertificateConfig{
					EnableHTTPS: true,
					EnableMTLS:  true,
					CertFile:    "/old/ops.crt",
					KeyFile:     "/old/ops.key",
					CaFile:      "/old/ca.crt",
				},
			},
		}
		success := applyOpsTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.ServerConfig.OpsTLSConfig)
		assert.False(t, cfg.ServerConfig.OpsTLSConfig.EnableHTTPS)
		assert.False(t, cfg.ServerConfig.OpsTLSConfig.EnableMTLS)
		assert.Equal(t, "", cfg.ServerConfig.OpsTLSConfig.CertFile)
		assert.Equal(t, "", cfg.ServerConfig.OpsTLSConfig.KeyFile)
		assert.Equal(t, "", cfg.ServerConfig.OpsTLSConfig.CaFile)
	})

	t.Run("ops_enable_https_invalid", func(t *testing.T) {
		OpsHTTPSEnable = "not-a-bool"
		OpsMTLSEnable = ""
		OpsTLSCertFile = ""
		OpsTLSKeyFile = ""
		OpsTLSCaCertFile = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyOpsTLSConfig(cfg)
		assert.False(t, success)
	})

	t.Run("ops_enable_mtls_invalid", func(t *testing.T) {
		OpsHTTPSEnable = ""
		OpsMTLSEnable = "not-a-bool"
		OpsTLSCertFile = "/etc/certs/ops.crt"
		OpsTLSKeyFile = "/etc/certs/ops.key"
		OpsTLSCaCertFile = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyOpsTLSConfig(cfg)
		assert.False(t, success)
	})

	t.Run("ops_tls_cert_and_key", func(t *testing.T) {
		OpsHTTPSEnable = ""
		OpsMTLSEnable = ""
		OpsTLSCertFile = "/etc/certs/ops.crt"
		OpsTLSKeyFile = "/etc/certs/ops.key"
		OpsTLSCaCertFile = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyOpsTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.ServerConfig.OpsTLSConfig)
		assert.Equal(t, "/etc/certs/ops.crt", cfg.ServerConfig.OpsTLSConfig.CertFile)
		assert.Equal(t, "/etc/certs/ops.key", cfg.ServerConfig.OpsTLSConfig.KeyFile)
	})

	t.Run("ops_tls_all", func(t *testing.T) {
		OpsHTTPSEnable = ""
		OpsMTLSEnable = ""
		OpsTLSCertFile = "/etc/certs/ops.crt"
		OpsTLSKeyFile = "/etc/certs/ops.key"
		OpsTLSCaCertFile = "/etc/certs/ops-ca.crt"
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyOpsTLSConfig(cfg)
		assert.True(t, success)
		assert.Equal(t, "/etc/certs/ops.crt", cfg.ServerConfig.OpsTLSConfig.CertFile)
		assert.Equal(t, "/etc/certs/ops.key", cfg.ServerConfig.OpsTLSConfig.KeyFile)
		assert.Equal(t, "/etc/certs/ops-ca.crt", cfg.ServerConfig.OpsTLSConfig.CaFile)
	})

	t.Run("ops_enable_https_true", func(t *testing.T) {
		OpsHTTPSEnable = "true"
		OpsMTLSEnable = ""
		OpsTLSCertFile = "/etc/certs/ops.crt"
		OpsTLSKeyFile = "/etc/certs/ops.key"
		OpsTLSCaCertFile = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyOpsTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.ServerConfig.OpsTLSConfig)
		assert.True(t, cfg.ServerConfig.OpsTLSConfig.EnableHTTPS)
	})

	t.Run("ops_enable_mtls_true", func(t *testing.T) {
		OpsHTTPSEnable = ""
		OpsMTLSEnable = "true"
		OpsTLSCertFile = "/etc/certs/ops.crt"
		OpsTLSKeyFile = "/etc/certs/ops.key"
		OpsTLSCaCertFile = "/etc/certs/ops-ca.crt"
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyOpsTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.ServerConfig.OpsTLSConfig)
		assert.True(t, cfg.ServerConfig.OpsTLSConfig.EnableMTLS)
	})

	t.Run("ops_enable_https_then_enable_mtls", func(t *testing.T) {
		OpsHTTPSEnable = "true"
		OpsMTLSEnable = "true"
		OpsTLSCertFile = "/etc/certs/ops.crt"
		OpsTLSKeyFile = "/etc/certs/ops.key"
		OpsTLSCaCertFile = "/etc/certs/ops-ca.crt"
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyOpsTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.ServerConfig.OpsTLSConfig)
		assert.True(t, cfg.ServerConfig.OpsTLSConfig.EnableHTTPS)
		assert.True(t, cfg.ServerConfig.OpsTLSConfig.EnableMTLS)
	})

	t.Run("ops_cert_without_key_returns_false", func(t *testing.T) {
		OpsHTTPSEnable = ""
		OpsMTLSEnable = ""
		OpsTLSCertFile = "/etc/certs/ops.crt"
		OpsTLSKeyFile = ""
		OpsTLSCaCertFile = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyOpsTLSConfig(cfg)
		assert.False(t, success)
	})

	t.Run("ops_key_without_cert_returns_false", func(t *testing.T) {
		OpsHTTPSEnable = ""
		OpsMTLSEnable = ""
		OpsTLSCertFile = ""
		OpsTLSKeyFile = "/etc/certs/ops.key"
		OpsTLSCaCertFile = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyOpsTLSConfig(cfg)
		assert.False(t, success)
	})

	t.Run("ops_enable_https_true_without_cert_returns_false", func(t *testing.T) {
		OpsHTTPSEnable = "true"
		OpsMTLSEnable = ""
		OpsTLSCertFile = ""
		OpsTLSKeyFile = ""
		OpsTLSCaCertFile = ""
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{},
		}
		success := applyOpsTLSConfig(cfg)
		assert.False(t, success)
	})
}

func Test_applyClientTLSConfig(t *testing.T) {
	saveHTTPSEnable := ClientHTTPSEnable
	saveMTLSEnable := ClientMTLSEnable
	saveCertFile := ClientCertFile
	saveKeyFile := ClientKeyFile
	saveCaCertFile := ClientCaCertFile
	defer func() {
		ClientHTTPSEnable = saveHTTPSEnable
		ClientMTLSEnable = saveMTLSEnable
		ClientCertFile = saveCertFile
		ClientKeyFile = saveKeyFile
		ClientCaCertFile = saveCaCertFile
	}()

	t.Run("all_empty", func(t *testing.T) {
		ClientHTTPSEnable = ""
		ClientMTLSEnable = ""
		ClientCertFile = ""
		ClientKeyFile = ""
		ClientCaCertFile = ""
		cfg := &config.Config{}
		success := applyClientTLSConfig(cfg)
		assert.True(t, success)
		assert.Nil(t, cfg.HttpConfig.TLSConfig)
	})

	t.Run("cert_and_key_no_auto_https", func(t *testing.T) {
		ClientHTTPSEnable = ""
		ClientMTLSEnable = ""
		ClientCertFile = "/etc/certs/client.crt"
		ClientKeyFile = "/etc/certs/client.key"
		ClientCaCertFile = ""
		cfg := &config.Config{}
		success := applyClientTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.HttpConfig.TLSConfig)
		assert.Equal(t, "/etc/certs/client.crt", cfg.HttpConfig.TLSConfig.CertFile)
		assert.Equal(t, "/etc/certs/client.key", cfg.HttpConfig.TLSConfig.KeyFile)
		assert.False(t, cfg.HttpConfig.TLSConfig.EnableHTTPS, "HTTPS should not be auto-enabled; enable-https must be explicitly set")
	})

	t.Run("explicit_https_enable", func(t *testing.T) {
		ClientHTTPSEnable = "true"
		ClientMTLSEnable = ""
		ClientCertFile = ""
		ClientKeyFile = ""
		ClientCaCertFile = ""
		cfg := &config.Config{}
		success := applyClientTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.HttpConfig.TLSConfig)
		assert.True(t, cfg.HttpConfig.TLSConfig.EnableHTTPS)
	})

	t.Run("https_disable_with_existing_config", func(t *testing.T) {
		ClientHTTPSEnable = "false"
		ClientMTLSEnable = ""
		ClientCertFile = ""
		ClientKeyFile = ""
		ClientCaCertFile = ""
		cfg := &config.Config{
			HttpConfig: config.HttpConfig{
				TLSConfig: &config.TLSCertificateConfig{
					EnableHTTPS: true,
					EnableMTLS:  true,
					CertFile:    "/old/client.crt",
					KeyFile:     "/old/client.key",
					CaFile:      "/old/ca.crt",
					SkipVerify:  true,
				},
			},
		}
		success := applyClientTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.HttpConfig.TLSConfig)
		assert.False(t, cfg.HttpConfig.TLSConfig.EnableHTTPS)
		assert.False(t, cfg.HttpConfig.TLSConfig.EnableMTLS)
		assert.Equal(t, "", cfg.HttpConfig.TLSConfig.CertFile)
		assert.Equal(t, "", cfg.HttpConfig.TLSConfig.KeyFile)
		assert.Equal(t, "", cfg.HttpConfig.TLSConfig.CaFile)
		assert.False(t, cfg.HttpConfig.TLSConfig.SkipVerify)
	})

	t.Run("explicit_https_disable", func(t *testing.T) {
		ClientHTTPSEnable = "false"
		ClientMTLSEnable = ""
		ClientCertFile = ""
		ClientKeyFile = ""
		ClientCaCertFile = ""
		cfg := &config.Config{}
		success := applyClientTLSConfig(cfg)
		assert.True(t, success)
		// enable-https=false skips all TLS config, TLSConfig should remain nil
		assert.Nil(t, cfg.HttpConfig.TLSConfig)
	})

	t.Run("invalid_https_enable", func(t *testing.T) {
		ClientHTTPSEnable = "not-a-bool"
		ClientMTLSEnable = ""
		ClientCertFile = ""
		ClientKeyFile = ""
		ClientCaCertFile = ""
		cfg := &config.Config{}
		success := applyClientTLSConfig(cfg)
		assert.False(t, success)
	})

	t.Run("mtls_enable", func(t *testing.T) {
		ClientHTTPSEnable = ""
		ClientMTLSEnable = "true"
		ClientCertFile = ""
		ClientKeyFile = ""
		ClientCaCertFile = "/etc/certs/ca.crt"
		cfg := &config.Config{}
		success := applyClientTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.HttpConfig.TLSConfig)
		assert.True(t, cfg.HttpConfig.TLSConfig.EnableMTLS)
		assert.Equal(t, "/etc/certs/ca.crt", cfg.HttpConfig.TLSConfig.CaFile)
	})

	t.Run("invalid_mtls_enable", func(t *testing.T) {
		ClientHTTPSEnable = ""
		ClientMTLSEnable = "not-a-bool"
		ClientCertFile = ""
		ClientKeyFile = ""
		ClientCaCertFile = ""
		cfg := &config.Config{}
		success := applyClientTLSConfig(cfg)
		assert.False(t, success)
	})

	t.Run("existing_tls_config_update", func(t *testing.T) {
		ClientHTTPSEnable = ""
		ClientMTLSEnable = ""
		ClientCertFile = "/new/client.crt"
		ClientKeyFile = ""
		ClientCaCertFile = "/new/ca.crt"
		cfg := &config.Config{
			HttpConfig: config.HttpConfig{
				TLSConfig: &config.TLSCertificateConfig{
					CertFile: "/old/client.crt",
					KeyFile:  "/old/client.key",
				},
			},
		}
		success := applyClientTLSConfig(cfg)
		assert.True(t, success)
		assert.Equal(t, "/new/client.crt", cfg.HttpConfig.TLSConfig.CertFile)
		assert.Equal(t, "/old/client.key", cfg.HttpConfig.TLSConfig.KeyFile)
		assert.Equal(t, "/new/ca.crt", cfg.HttpConfig.TLSConfig.CaFile)
	})
}

func Test_applyEtcdTLSConfig(t *testing.T) {
	saveTLSEnable := EtcdTLSEnable
	saveMTLSEnable := EtcdMTLSEnable
	saveCertFile := EtcdCertFile
	saveKeyFile := EtcdKeyFile
	saveCaCertFile := EtcdCaCertFile
	defer func() {
		EtcdTLSEnable = saveTLSEnable
		EtcdMTLSEnable = saveMTLSEnable
		EtcdCertFile = saveCertFile
		EtcdKeyFile = saveKeyFile
		EtcdCaCertFile = saveCaCertFile
	}()

	t.Run("all_empty", func(t *testing.T) {
		EtcdTLSEnable = ""
		EtcdMTLSEnable = ""
		EtcdCertFile = ""
		EtcdKeyFile = ""
		EtcdCaCertFile = ""
		cfg := &config.Config{}
		success := applyEtcdTLSConfig(cfg)
		assert.True(t, success)
	})

	t.Run("cert_and_key_no_auto_https", func(t *testing.T) {
		EtcdTLSEnable = ""
		EtcdMTLSEnable = ""
		EtcdCertFile = "/etc/certs/etcd-client.crt"
		EtcdKeyFile = "/etc/certs/etcd-client.key"
		EtcdCaCertFile = ""
		cfg := &config.Config{
			ClusterCfg: config.ClusterConfig{
				EtcdCfg: &config.EtcdConfig{},
			},
		}
		success := applyEtcdTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.ClusterCfg.EtcdCfg.TLSConfig)
		assert.Equal(t, "/etc/certs/etcd-client.crt", cfg.ClusterCfg.EtcdCfg.TLSConfig.CertFile)
		assert.Equal(t, "/etc/certs/etcd-client.key", cfg.ClusterCfg.EtcdCfg.TLSConfig.KeyFile)
		assert.False(t, cfg.ClusterCfg.EtcdCfg.TLSConfig.EnableHTTPS, "HTTPS should not be auto-enabled; etcd-tls-enable must be explicitly set")
	})

	t.Run("explicit_tls_enable", func(t *testing.T) {
		EtcdTLSEnable = "true"
		EtcdMTLSEnable = ""
		EtcdCertFile = ""
		EtcdKeyFile = ""
		EtcdCaCertFile = ""
		cfg := &config.Config{
			ClusterCfg: config.ClusterConfig{
				EtcdCfg: &config.EtcdConfig{},
			},
		}
		success := applyEtcdTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.ClusterCfg.EtcdCfg.TLSConfig)
		assert.True(t, cfg.ClusterCfg.EtcdCfg.TLSConfig.EnableHTTPS)
	})

	t.Run("explicit_tls_disable", func(t *testing.T) {
		EtcdTLSEnable = "false"
		EtcdMTLSEnable = ""
		EtcdCertFile = ""
		EtcdKeyFile = ""
		EtcdCaCertFile = ""
		cfg := &config.Config{
			ClusterCfg: config.ClusterConfig{
				EtcdCfg: &config.EtcdConfig{},
			},
		}
		success := applyEtcdTLSConfig(cfg)
		assert.True(t, success)
		// etcd-tls-enable=false skips all TLS config, TLSConfig should remain nil
		assert.Nil(t, cfg.ClusterCfg.EtcdCfg.TLSConfig)
	})

	t.Run("tls_disable_with_existing_config", func(t *testing.T) {
		EtcdTLSEnable = "false"
		EtcdMTLSEnable = ""
		EtcdCertFile = ""
		EtcdKeyFile = ""
		EtcdCaCertFile = ""
		cfg := &config.Config{
			ClusterCfg: config.ClusterConfig{
				EtcdCfg: &config.EtcdConfig{
					TLSConfig: &config.TLSCertificateConfig{
						EnableHTTPS: true,
						EnableMTLS:  true,
						CertFile:    "/old/etcd.crt",
						KeyFile:     "/old/etcd.key",
						CaFile:      "/old/ca.crt",
						SkipVerify:  true,
					},
				},
			},
		}
		success := applyEtcdTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.ClusterCfg.EtcdCfg.TLSConfig)
		assert.False(t, cfg.ClusterCfg.EtcdCfg.TLSConfig.EnableHTTPS)
		assert.False(t, cfg.ClusterCfg.EtcdCfg.TLSConfig.EnableMTLS)
		assert.Equal(t, "", cfg.ClusterCfg.EtcdCfg.TLSConfig.CertFile)
		assert.Equal(t, "", cfg.ClusterCfg.EtcdCfg.TLSConfig.KeyFile)
		assert.Equal(t, "", cfg.ClusterCfg.EtcdCfg.TLSConfig.CaFile)
		assert.False(t, cfg.ClusterCfg.EtcdCfg.TLSConfig.SkipVerify)
	})

	t.Run("invalid_tls_enable", func(t *testing.T) {
		EtcdTLSEnable = "not-a-bool"
		EtcdMTLSEnable = ""
		EtcdCertFile = ""
		EtcdKeyFile = ""
		EtcdCaCertFile = ""
		cfg := &config.Config{
			ClusterCfg: config.ClusterConfig{
				EtcdCfg: &config.EtcdConfig{},
			},
		}
		success := applyEtcdTLSConfig(cfg)
		assert.False(t, success)
	})

	t.Run("mtls_enable", func(t *testing.T) {
		EtcdTLSEnable = ""
		EtcdMTLSEnable = "true"
		EtcdCertFile = ""
		EtcdKeyFile = ""
		EtcdCaCertFile = "/etc/certs/etcd-ca.crt"
		cfg := &config.Config{
			ClusterCfg: config.ClusterConfig{
				EtcdCfg: &config.EtcdConfig{},
			},
		}
		success := applyEtcdTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.ClusterCfg.EtcdCfg.TLSConfig)
		assert.True(t, cfg.ClusterCfg.EtcdCfg.TLSConfig.EnableMTLS)
		assert.Equal(t, "/etc/certs/etcd-ca.crt", cfg.ClusterCfg.EtcdCfg.TLSConfig.CaFile)
	})

	t.Run("invalid_mtls_enable", func(t *testing.T) {
		EtcdTLSEnable = ""
		EtcdMTLSEnable = "not-a-bool"
		EtcdCertFile = ""
		EtcdKeyFile = ""
		EtcdCaCertFile = ""
		cfg := &config.Config{
			ClusterCfg: config.ClusterConfig{
				EtcdCfg: &config.EtcdConfig{},
			},
		}
		success := applyEtcdTLSConfig(cfg)
		assert.False(t, success)
	})
}

func Test_applyTLSConfig_EnableHTTPS(t *testing.T) {
	saveCertFile := TLSCertFile
	saveKeyFile := TLSKeyFile
	saveEnableHTTPS := EnableHTTPS
	defer func() {
		TLSCertFile = saveCertFile
		TLSKeyFile = saveKeyFile
		EnableHTTPS = saveEnableHTTPS
	}()

	t.Run("enable_https_true", func(t *testing.T) {
		TLSCertFile = "/etc/certs/server.crt"
		TLSKeyFile = "/etc/certs/server.key"
		EnableHTTPS = "true"
		cfg := &config.Config{ServerConfig: config.ServerConfig{}}
		success := applyTLSConfig(cfg)
		assert.True(t, success)
		assert.True(t, cfg.ServerConfig.TLSConfig.EnableHTTPS)
	})

	t.Run("enable_https_false", func(t *testing.T) {
		TLSCertFile = "/etc/certs/server.crt"
		TLSKeyFile = "/etc/certs/server.key"
		EnableHTTPS = "false"
		cfg := &config.Config{ServerConfig: config.ServerConfig{}}
		success := applyTLSConfig(cfg)
		assert.True(t, success)
		// enable-https=false skips all TLS config, TLSConfig should remain nil
		assert.Nil(t, cfg.ServerConfig.TLSConfig)
	})

	t.Run("enable_https_false_with_existing_config", func(t *testing.T) {
		TLSCertFile = ""
		TLSKeyFile = ""
		EnableHTTPS = "false"
		cfg := &config.Config{
			ServerConfig: config.ServerConfig{
				TLSConfig: &config.TLSCertificateConfig{
					EnableHTTPS: true,
					EnableMTLS:  true,
					CertFile:    "/old/server.crt",
					KeyFile:     "/old/server.key",
					CaFile:      "/old/ca.crt",
					SkipVerify:  true,
				},
			},
		}
		success := applyTLSConfig(cfg)
		assert.True(t, success)
		assert.NotNil(t, cfg.ServerConfig.TLSConfig)
		assert.False(t, cfg.ServerConfig.TLSConfig.EnableHTTPS)
		assert.False(t, cfg.ServerConfig.TLSConfig.EnableMTLS)
		assert.Equal(t, "", cfg.ServerConfig.TLSConfig.CertFile)
		assert.Equal(t, "", cfg.ServerConfig.TLSConfig.KeyFile)
		assert.Equal(t, "", cfg.ServerConfig.TLSConfig.CaFile)
		assert.False(t, cfg.ServerConfig.TLSConfig.SkipVerify)
	})

	t.Run("invalid_enable_https", func(t *testing.T) {
		TLSCertFile = "/etc/certs/server.crt"
		TLSKeyFile = "/etc/certs/server.key"
		EnableHTTPS = "not-a-bool"
		cfg := &config.Config{ServerConfig: config.ServerConfig{}}
		success := applyTLSConfig(cfg)
		assert.False(t, success)
	})
}

// Test_deployDefaultBehavior verifies the default behavior of deploy.sh for all new parameters
// Simulates the scenario where deploy.sh passes no TLS/HTTPS arguments and verifies the generated config is correct
func Test_deployDefaultBehavior(t *testing.T) {
	defer saveGlobals()()
	testDir := t.TempDir()
	setupBasicGlobals(testDir)

	// Simulate deploy.sh default behavior: all new parameters are empty/default
	// These values are already set by setupBasicGlobals, explicitly re-confirm them
	OpsPort = 0
	OpsTLSCertFile = ""
	OpsTLSKeyFile = ""
	OpsTLSCaCertFile = ""
	TLSCertFile = ""
	TLSKeyFile = ""
	TLSCaCertFile = ""
	TLSSkipVerify = ""
	EnableMTLS = ""

	// Prepare template file
	templateContent := `
server:
  addr: <ip>
  http-port: <port>
  name: <name>
  tls:
    cert-file:
    key-file:
    ca-cert-file:
    skip-verify: false
    enable-mtls: false
  ops-tls:
    cert-file:
    key-file:
    ca-cert-file:
logger:
  enabled: true
  filename: <dir_to_log>/route-server.log
  max-size: 1024
  max-backups: 10
  max-age: 30
  compress: true
`
	templatePath := filepath.Join(testDir, "config.yaml")
	err := os.WriteFile(templatePath, []byte(templateContent), 0644)
	assert.NoError(t, err)

	TemplatePath = templatePath
	DeployTargetPath = filepath.Join(testDir, "target_config")
	err = os.MkdirAll(filepath.Join(DeployTargetPath, "config"), 0755)
	assert.NoError(t, err)

	success := mainProc(false)
	assert.True(t, success)

	// Read the generated configuration
	cfg := &config.Config{}
	cfgBytes, err := os.ReadFile(filepath.Join(testDir, "target_config", "config", "model-orch.yaml"))
	assert.NoError(t, err)
	err = yaml.Unmarshal(cfgBytes, cfg)
	assert.NoError(t, err)

	t.Run("tls_config_empty_when_not_configured", func(t *testing.T) {
		// Template has tls: section, so the object is not nil, but cert paths should be empty
		assert.NotNil(t, cfg.ServerConfig.TLSConfig,
			"tls config object exists from template but should have empty fields")
		assert.Empty(t, cfg.ServerConfig.TLSConfig.CertFile,
			"cert-file should be empty when --tls-cert-file is not provided")
		assert.Empty(t, cfg.ServerConfig.TLSConfig.KeyFile,
			"key-file should be empty when --tls-key-file is not provided")
		assert.False(t, cfg.ServerConfig.TLSConfig.EnableMTLS,
			"enable-mtls should be false by default")
		assert.False(t, cfg.ServerConfig.TLSConfig.SkipVerify,
			"skip-verify should be false by default")
	})

	t.Run("ops_tls_config_empty_when_not_configured", func(t *testing.T) {
		// Template has ops-tls: section, so the object is not nil, but cert paths should be empty
		assert.NotNil(t, cfg.ServerConfig.OpsTLSConfig,
			"ops-tls config object exists from template but should have empty fields")
		assert.Empty(t, cfg.ServerConfig.OpsTLSConfig.CertFile,
			"ops-tls cert-file should be empty when --ops-tls-cert-file is not provided")
		assert.Empty(t, cfg.ServerConfig.OpsTLSConfig.KeyFile,
			"ops-tls key-file should be empty when --ops-tls-key-file is not provided")
	})

	t.Run("http_port_set_correctly", func(t *testing.T) {
		assert.Equal(t, "1234", cfg.ServerConfig.HttpPort,
			"http-port should be set from --port arg")
	})

	t.Run("ops_port_empty_when_not_configured", func(t *testing.T) {
		assert.Empty(t, cfg.ServerConfig.OpsPort,
			"ops-port should be empty when --ops-port is not provided (route-server will default to http-port + 10)")
	})
}

func Test_mainProc_applyServerConfigFails(t *testing.T) {
	defer saveGlobals()()
	testDir := t.TempDir()
	setupBasicGlobals(testDir)
	err := os.MkdirAll(filepath.Join(testDir, "target_config", "config"), 0755)
	assert.NoError(t, err)
	initCfg := &config.Config{
		ServerConfig: config.ServerConfig{
			Addr:     "127.0.0.1",
			HttpPort: "1234",
		},
		InstanceManagerCfg: config.InstanceManagerCfg{
			HealthCheckConfig: &config.HealthCheckConfig{},
		},
	}
	cfgBytes, err := yaml.Marshal(initCfg)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(testDir, "config.yaml"), cfgBytes, 0o600)
	assert.NoError(t, err)
	// UseDomainForLeader is not a valid bool -> applyServerConfig fails
	UseDomainForLeader = "not-a-bool"
	success := mainProc(false)
	assert.False(t, success)
}

func Test_mainProc_modifyClusterConfigFails(t *testing.T) {
	defer saveGlobals()()
	testDir := t.TempDir()
	setupBasicGlobals(testDir)
	err := os.MkdirAll(filepath.Join(testDir, "target_config", "config"), 0755)
	assert.NoError(t, err)
	initCfg := &config.Config{
		ServerConfig: config.ServerConfig{
			Addr:     "127.0.0.1",
			HttpPort: "1234",
		},
		InstanceManagerCfg: config.InstanceManagerCfg{
			HealthCheckConfig: &config.HealthCheckConfig{},
		},
	}
	cfgBytes, err := yaml.Marshal(initCfg)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(testDir, "config.yaml"), cfgBytes, 0o600)
	assert.NoError(t, err)
	// IsCandidate is not a valid bool -> modifyClusterConfig fails
	IsCandidate = "not-a-bool"
	success := mainProc(false)
	assert.False(t, success)
}

func Test_mainProc_instancesFails(t *testing.T) {
	defer saveGlobals()()
	testDir := t.TempDir()
	setupBasicGlobals(testDir)
	err := os.MkdirAll(filepath.Join(testDir, "target_config", "config"), 0755)
	assert.NoError(t, err)
	initCfg := &config.Config{
		ServerConfig: config.ServerConfig{
			Addr:     "127.0.0.1",
			HttpPort: "1234",
		},
		InstanceManagerCfg: config.InstanceManagerCfg{
			HealthCheckConfig: &config.HealthCheckConfig{},
		},
	}
	cfgBytes, err := yaml.Marshal(initCfg)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(testDir, "config.yaml"), cfgBytes, 0o600)
	assert.NoError(t, err)
	// prefill address format is invalid -> instances fails
	PrefillAddrs = "invalid-addr"
	success := mainProc(false)
	assert.False(t, success)
}

func Test_mainProc_generateServerInferModeFails(t *testing.T) {
	defer saveGlobals()()
	testDir := t.TempDir()
	setupBasicGlobals(testDir)
	err := os.MkdirAll(filepath.Join(testDir, "target_config", "config"), 0755)
	assert.NoError(t, err)
	initCfg := &config.Config{
		ServerConfig: config.ServerConfig{
			Addr:     "127.0.0.1",
			HttpPort: "1234",
		},
		InstanceManagerCfg: config.InstanceManagerCfg{
			HealthCheckConfig: &config.HealthCheckConfig{},
		},
	}
	cfgBytes, err := yaml.Marshal(initCfg)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(testDir, "config.yaml"), cfgBytes, 0o600)
	assert.NoError(t, err)
	// Invalid infer mode -> generateServerInferMode fails
	InferMode = "UNKNOWN_MODE"
	success := mainProc(false)
	assert.False(t, success)
}

func Test_mainProc_generateRequiredRolesFails(t *testing.T) {
	defer saveGlobals()()
	testDir := t.TempDir()
	setupBasicGlobals(testDir)
	err := os.MkdirAll(filepath.Join(testDir, "target_config", "config"), 0755)
	assert.NoError(t, err)
	initCfg := &config.Config{
		ServerConfig: config.ServerConfig{
			Addr:     "127.0.0.1",
			HttpPort: "1234",
		},
		InstanceManagerCfg: config.InstanceManagerCfg{
			HealthCheckConfig: &config.HealthCheckConfig{},
		},
	}
	cfgBytes, err := yaml.Marshal(initCfg)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(testDir, "config.yaml"), cfgBytes, 0o600)
	assert.NoError(t, err)
	// Invalid infer mode -> generateRequiredRoles fails
	InferMode = "UNKNOWN_MODE"
	success := mainProc(false)
	assert.False(t, success)
}

func Test_mainProc_writeConfigFile_yamlMarshalFails(t *testing.T) {
	defer saveGlobals()()
	testDir := t.TempDir()
	setupBasicGlobals(testDir)
	err := os.MkdirAll(filepath.Join(testDir, "target_config", "config"), 0755)
	assert.NoError(t, err)
	initCfg := &config.Config{
		ServerConfig: config.ServerConfig{
			Addr:     "127.0.0.1",
			HttpPort: "1234",
		},
		InstanceManagerCfg: config.InstanceManagerCfg{
			HealthCheckConfig: &config.HealthCheckConfig{},
		},
	}
	cfgBytes, err := yaml.Marshal(initCfg)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(testDir, "config.yaml"), cfgBytes, 0o600)
	assert.NoError(t, err)

	// Stub yaml.Marshal using gomonkey to return an error
	patches := gomonkey.NewPatches()
	defer patches.Reset()
	patches.ApplyFunc(yaml.Marshal, func(_ interface{}) ([]byte, error) {
		return nil, fmt.Errorf("yaml marshal error")
	})
	success := mainProc(false)
	assert.False(t, success)
}

func Test_mainProc_writeConfigFile_writeFileFails(t *testing.T) {
	defer saveGlobals()()
	testDir := t.TempDir()
	setupBasicGlobals(testDir)
	err := os.MkdirAll(filepath.Join(testDir, "target_config", "config"), 0755)
	assert.NoError(t, err)
	initCfg := &config.Config{
		ServerConfig: config.ServerConfig{
			Addr:     "127.0.0.1",
			HttpPort: "1234",
		},
		InstanceManagerCfg: config.InstanceManagerCfg{
			HealthCheckConfig: &config.HealthCheckConfig{},
		},
	}
	cfgBytes, err := yaml.Marshal(initCfg)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(testDir, "config.yaml"), cfgBytes, 0o600)
	assert.NoError(t, err)

	// Stub os.WriteFile using gomonkey to return an error
	patches := gomonkey.NewPatches()
	defer patches.Reset()
	patches.ApplyFunc(os.WriteFile, func(_ string, _ []byte, _ os.FileMode) error {
		return fmt.Errorf("write file error")
	})
	success := mainProc(false)
	assert.False(t, success)
}
