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
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"route-server/pkg/metrics-collector"
	"route-server/pkg/metrics-collector/logger"
)

// Regex to validate IP:port,IP:port format
func isValidIPPortList(s string) bool {
	// Regex explanation:
	// 1. IP address part: supports IPv4 or localhost
	//    - IPv4: four groups of 0-255 digits, no leading zeros
	//    - localhost: matches string "localhost" directly
	// 2. Port part: integer between 1-65535
	// 3. Overall: starts with IP:port, followed by comma+IP:port, 0 or more
	const pattern = `^(localhost|(25[0-5]|2[0-4]\d|1\d\d|[1-9][0-9]?|0)\.(25[0-5]|2[0-4]\d|1\d\d|[1-9][0-9]?|0)\.(25[0-5]|2[0-4]\d|1\d\d|[1-9][0-9]?|0)\.(25[0-5]|2[0-4]\d|1\d\d|[1-9][0-9]?|0)):([1-9]\d{0,3}|[1-5]\d{4}|6[0-4]\d{3}|65[0-4]\d{2}|655[0-2]\d|6553[0-5])(,(localhost|(25[0-5]|2[0-4]\d|1\d\d|[1-9][0-9]?|0)\.(25[0-5]|2[0-4]\d|1\d\d|[1-9][0-9]?|0)\.(25[0-5]|2[0-4]\d|1\d\d|[1-9][0-9]?|0)\.(25[0-5]|2[0-4]\d|1\d\d|[1-9][0-9]?|0)):([1-9]\d{0,3}|[1-5]\d{4}|6[0-4]\d{3}|65[0-4]\d{2}|655[0-2]\d|6553[0-5]))*$`

	// Compile regex
	re := regexp.MustCompile(pattern)
	return re.MatchString(s)
}

func main() {

	// Define parameter variables
	var (
		metricsServerIpAndPort = flag.String("metrics_collector_server", "",
			"metric collector server ip and port")
		schedulerServerIpAndPort = flag.String("scheduler_server", "",
			"scheduler server ip and port")
		prefillServersList = flag.String("prefill_servers_list", "",
			"prefill servers ip and port list, eg: ip1:port1,ip2:port2")
		decodeServersList = flag.String("decode_servers_list", "",
			"decode servers ip and port list, eg: ip1:port1,ip2:port2")
		metricsConfigYamlPath = flag.String("metrics_config_yaml_path", "",
			"metrics config yaml path")
	)

	// Parse parameters
	flag.Parse()
	// Check validity
	if !isValidIPPortList(*metricsServerIpAndPort) {
		logger.Logger().Errorf("metrics_collector_server configuration format error")
		os.Exit(1)
	}
	if !isValidIPPortList(*schedulerServerIpAndPort) {
		logger.Logger().Errorf("scheduler_server configuration format error")
		os.Exit(1)
	}
	if !isValidIPPortList(*prefillServersList) {
		logger.Logger().Errorf("prefill_servers_list configuration format error")
		os.Exit(1)
	}
	if !isValidIPPortList(*decodeServersList) {
		logger.Logger().Errorf("decode_servers_list configuration format error")
		os.Exit(1)
	}

	instances, err := initInstance(*schedulerServerIpAndPort, *prefillServersList, *decodeServersList)
	if err != nil {
		os.Exit(1)
	}
	collectInterval := 5 * time.Second
	collector, err := metrics_collector.NewMetricsCollector(instances, *metricsConfigYamlPath)
	if err != nil {
		os.Exit(1)
	}
	engine := gin.New()
	engine.GET("/metrics", collector.HandleMetricsRequest)
	metricsServerPort := strings.Split(*metricsServerIpAndPort, ":")[1]
	server := &http.Server{
		Addr:         ":" + metricsServerPort,
		Handler:      engine,
		ReadTimeout:  time.Second * 3600,
		WriteTimeout: time.Second * 3600,
	}

	go func() {
		logger.Logger().Info("start http server: http://localhost:" + metricsServerPort)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Logger().Errorf("http server error: %v", err.Error())
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), collectInterval)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}

	logger.Logger().Info("server shutdown successfully")
}

func initInstance(schedulerServerIpAndPort string, prefillServersList string, decodeServersList string) ([]metrics_collector.Instance, error) {
	instances := make([]metrics_collector.Instance, 0)

	schedulerServerIp := strings.Split(schedulerServerIpAndPort, ":")[0]
	schedulerServerPortString := strings.Split(schedulerServerIpAndPort, ":")[1]
	schedulerServerPort, err := strconv.Atoi(schedulerServerPortString)
	if err != nil {
		logger.Logger().Errorf("scheduler server port to integer conversion failed (%s): %v\n", schedulerServerPortString, err.Error())
		return nil, err
	}
	instances = append(instances, metrics_collector.Instance{
		Role: "Scheduler",
		IP:   schedulerServerIp,
		Port: schedulerServerPort,
	})

	prefillServers := strings.Split(prefillServersList, ",")
	for _, prefillNodeInfo := range prefillServers {
		prefillIpPort := strings.Split(prefillNodeInfo, ":")
		if len(prefillNodeInfo) < 2 {
			return nil, fmt.Errorf("unknwon prefillNodeInfo %v", prefillNodeInfo)
		}
		prefillPort, err := strconv.Atoi(prefillIpPort[1])
		if err != nil {
			logger.Logger().Errorf("prefill port to integer conversion failed (%s): %v\n", prefillIpPort[1], err.Error())
			return nil, err
		}
		instances = append(instances, metrics_collector.Instance{
			Role: "Prefill",
			IP:   prefillIpPort[0],
			Port: prefillPort,
		})

	}

	decodeServers := strings.Split(decodeServersList, ",")
	for _, decodeNodeInfo := range decodeServers {
		decodeIpPort := strings.Split(decodeNodeInfo, ":")
		if len(decodeIpPort) < 2 {
			return nil, fmt.Errorf("unknwon decodeNodeInfo %v", decodeNodeInfo)
		}
		decodePort, err := strconv.Atoi(decodeIpPort[1])
		if err != nil {
			logger.Logger().Errorf("decode port to integer conversion failed (%s): %v\n", decodeIpPort[1], err.Error())
			return nil, err
		}
		instances = append(instances, metrics_collector.Instance{
			Role: "decode",
			IP:   decodeIpPort[0],
			Port: decodePort,
		})
	}

	logger.Logger().Infof("%v", instances)
	return instances, nil
}
