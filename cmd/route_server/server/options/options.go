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

package options

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-playground/validator/v10"
	"github.com/goccy/go-yaml"
	commoncfg "infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
)

const (
	RuntimeConfigFileName = "route-server.yaml"
)

var config commoncfg.Config

func InitConfig() error {
	var err error
	configPath := filepath.Join(os.Getenv(constant.EnvServerConfigPath), RuntimeConfigFileName)
	err = loadFromFile(&config, configPath)
	if err != nil {
		fmt.Printf("load config file failed %s]\n", os.Getenv(constant.EnvServerConfigPath))
		return err
	}
	validate := validator.New()
	if err := validate.Struct(config); err != nil {
		var val validator.ValidationErrors
		if errors.As(err, &val) {
			for _, err := range err.(validator.ValidationErrors) {
				fmt.Printf("config is invalid: field %s violate role '%s', expect [%s], got [%s]\n", err.Namespace(),
					err.Tag(), err.Param(), err.Value())
			}
		} else {
			fmt.Printf("config is invalid, err = %v\n", err)
		}
		return err
	}
	return err
}

func loadFromFile(obj interface{}, configPath string) error {
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("load config file failed, err:%v", err)
	}
	if err := yaml.Unmarshal(configBytes, obj); err != nil {
		return fmt.Errorf("unmarshal config file failed, err:%v", err)
	}
	return nil
}

// LoadFromStruct for testing
func LoadFromStruct(cfg commoncfg.Config) {
	config = cfg
}

func GetConfig() *commoncfg.Config {
	return &config
}
