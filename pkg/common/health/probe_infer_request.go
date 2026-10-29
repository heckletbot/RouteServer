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

package health

import (
	"bytes"
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
	"infer-dp-scheduler/pkg/common/client"
	"infer-dp-scheduler/pkg/common/config"
	"infer-dp-scheduler/pkg/common/constant"
	"infer-dp-scheduler/pkg/common/errs"
	"infer-dp-scheduler/pkg/common/logger"
	"infer-dp-scheduler/pkg/common/utils"

	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

type ProbeInferRequest struct {
	cfg          *config.HealthCheckConfig
	testInstance func(ctx *gin.Context, path string, isChat bool, instance *client.Instance) (
		successToCheck bool, testSuccess bool, errDetails *errs.RSError)
	modelName   string
	modelNameMu sync.RWMutex
}

type RequestBody struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens"`
	Stream    bool      `json:"stream"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func NewProbeInferRequest(cfg *config.HealthCheckConfig,
	testInstance func(ctx *gin.Context, path string, isChat bool, instance *client.Instance) (
		successToCheck bool, testSuccess bool, errDetails *errs.RSError)) *ProbeInferRequest {
	pir := &ProbeInferRequest{}
	pir.cfg = cfg
	pir.testInstance = testInstance
	return pir
}

func (pir *ProbeInferRequest) sendProbeInferRequestWithTimeout(instance *client.Instance, fromHealthModel string) (
	bool, bool, *errs.RSError) {
	req, err := pir.createProbeInferReq(instance, fromHealthModel)
	if err != nil {
		logger.Log().Warnf("[%s] create probe infer request error: %v", fromHealthModel, err)
		rsErr := errs.NewRSMaError(errs.BadReqErr.BuildArgs(fmt.Sprintf(
			"fail to create probe infer request: %v", err)), constant.StagePrepare)
		return false, false, rsErr
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(pir.cfg.ProbeInferRequestTimeoutMs)*time.Millisecond)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	reqId := req.Header.Get(constant.XRequestId)

	// probe infer request support timeout configuration, once it fails, count it as check fail instead of instance fail.
	successToCheck, testSuccess, errDetails := pir.testInstance(c, "", true, instance)
	if successToCheck && testSuccess {
		logger.Log().Infof("[%s] send probe infer request %s to instance %s success", fromHealthModel, reqId,
			instance.ID)
	} else {
		logger.Log().Warnf("[%s] send probe infer request %s to instance %s error, successToCheck: %t, "+
			"testSuccess: %t", fromHealthModel, reqId, instance.ID, successToCheck, testSuccess)
		// if request timeout, then abort
		if errDetails != nil && errDetails.MaErr != nil && strings.Contains(
			errDetails.MaErr.Error(), context.DeadlineExceeded.Error()) {
			logger.Log().Warnf("[%s] send probe infer request %s timeout, abort request", fromHealthModel, reqId)
			c.Abort()
		}
	}
	return successToCheck, testSuccess, errDetails
}

func (pir *ProbeInferRequest) createProbeInferReq(instance *client.Instance, fromHealthModel string) (*http.Request, error) {
	modelName := pir.getModelName()
	if modelName == "" {
		return nil, fmt.Errorf("model name is empty")
	}
	requestBody := RequestBody{
		Model: modelName,
		Messages: []Message{
			{
				Role:    "user",
				Content: "Hello",
			},
		},
		MaxTokens: 2,
		Stream:    true,
	}

	jsonData, err := utils.SonicApi.MarshalIndent(requestBody, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("json serialize error: %v", err)
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		bytes.NewBuffer(jsonData),
	)
	reqId := fmt.Sprintf("probe-infer-req-%s-%s-%d", fromHealthModel, instance.ID, time.Now().Unix())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Health-Check", "true")
	req.Header.Set(constant.XRequestId, reqId)
	return req, nil
}

func (pir *ProbeInferRequest) getModelName() string {
	pir.modelNameMu.RLock()
	defer pir.modelNameMu.RUnlock()
	return pir.modelName
}

func (pir *ProbeInferRequest) setModelName(modelName string) {
	pir.modelNameMu.Lock()
	defer pir.modelNameMu.Unlock()
	pir.modelName = modelName
}
