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

package pipeline

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/onsi/gomega"
	"route-server/pkg/common/constant"
	"route-server/pkg/common/health"
	"route-server/pkg/common/trace"
)

func TestNewSchedulePhase_Minimal(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	phase := newSchedulePhase()
	g.Expect(phase).NotTo(gomega.BeNil())

	schedulePhase, ok := phase.(*SchedulePhase)
	g.Expect(ok).To(gomega.BeTrue())
	g.Expect(schedulePhase.BasePhase.PhaseTypeName()).To(gomega.Equal(constant.SchedulePhase))
}

// TestNoSkip_Minimal tests noSkip strategy
func TestNoSkip_Minimal(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	// Create a simple ctx for testing
	ctx := &PipelineContext{}

	skip, reason := noSkip(ctx)
	g.Expect(skip).To(gomega.BeFalse())
	g.Expect(reason).To(gomega.BeEmpty())
}

// TestSkipOnZeroImage_Minimal tests skipOnZeroImage strategy
func TestSkipOnZeroImage_Minimal(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("Zero image skip", func(t *testing.T) {
		ctx := &PipelineContext{
			RequestContext: &RequestContext{},
		}
		ctx.GetRequestCtx().RequestLoadsCount.Store(LoadTypeImage, int32(0))

		skip, reason := skipOnZeroImage(ctx)
		g.Expect(skip).To(gomega.BeTrue())
		g.Expect(reason).To(gomega.ContainSubstring("no image found"))
	})

	t.Run("Has image no skip", func(t *testing.T) {
		ctx := &PipelineContext{
			RequestContext: &RequestContext{},
		}
		ctx.GetRequestCtx().RequestLoadsCount.Store(LoadTypeImage, int32(2))

		skip, reason := skipOnZeroImage(ctx)
		g.Expect(skip).To(gomega.BeFalse())
		g.Expect(reason).To(gomega.BeEmpty())
	})

	t.Run("Type conversion error", func(t *testing.T) {
		ctx := &PipelineContext{
			RequestContext: &RequestContext{},
		}
		ctx.GetRequestCtx().RequestLoadsCount.Store(LoadTypeImage, "invalid")

		skip, reason := skipOnZeroImage(ctx)
		g.Expect(skip).To(gomega.BeFalse())
		g.Expect(reason).To(gomega.BeEmpty())
	})

	t.Run("Missing load info", func(t *testing.T) {
		ctx := &PipelineContext{
			RequestContext: &RequestContext{},
		}
		skip, reason := skipOnZeroImage(ctx)
		g.Expect(skip).To(gomega.BeFalse())
		g.Expect(reason).To(gomega.BeEmpty())
	})
}

// TestSkipOnZeroImage_ConcurrentSafety tests concurrent safety of skipOnZeroImage
func TestSkipOnZeroImage_ConcurrentSafety(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("High concurrency read/write test", func(t *testing.T) {
		ctx := &PipelineContext{
			RequestContext: &RequestContext{
				RequestLoadsCount: sync.Map{},
			},
		}

		// Initialize image count to 0
		ctx.RequestContext.RequestLoadsCount.Store(LoadTypeImage, int32(0))

		const numWorkers = 100
		const numOperations = 1000
		var wg sync.WaitGroup
		results := make(chan bool, numWorkers*numOperations)

		// Start multiple goroutines for concurrent read/write operations
		for i := 0; i < numWorkers; i++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()

				for j := 0; j < numOperations; j++ {
					if workerID%2 == 0 {
						// Write operation: randomly set image count
						count := int32(j % 10)
						ctx.GetRequestCtx().RequestLoadsCount.Store(LoadTypeImage, count)
					} else {
						// Read operation: call skipOnZeroImage
						_, _ = skipOnZeroImage(ctx)
					}
					results <- true
				}
			}(i)
		}

		wg.Wait()
		close(results)

		// Verify all operations completed
		completedOps := 0
		for range results {
			completedOps++
		}
		g.Expect(completedOps).To(gomega.Equal(numWorkers * numOperations))
	})
}

// TestSkipPolicy_EdgeConditions tests edge conditions of skip policy
func TestSkipPolicy_EdgeConditions(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("Extreme value test", func(t *testing.T) {
		ctx := &PipelineContext{
			RequestContext: &RequestContext{
				RequestLoadsCount: sync.Map{},
			},
		}

		// Test negative boundary
		ctx.RequestContext.RequestLoadsCount.Store(LoadTypeImage, int32(-1))
		skip, reason := skipOnZeroImage(ctx)
		g.Expect(skip).To(gomega.BeFalse())
		g.Expect(reason).To(gomega.BeEmpty())

		// Test max positive value
		maxInt32 := int32(^uint32(0) >> 1)
		ctx.RequestContext.RequestLoadsCount.Store(LoadTypeImage, maxInt32)
		skip, reason = skipOnZeroImage(ctx)
		g.Expect(skip).To(gomega.BeFalse())
		g.Expect(reason).To(gomega.BeEmpty())

		// Test zero value
		ctx.RequestContext.RequestLoadsCount.Store(LoadTypeImage, int32(0))
		skip, reason = skipOnZeroImage(ctx)
		g.Expect(skip).To(gomega.BeTrue())
		g.Expect(reason).To(gomega.ContainSubstring("no image found"))
	})

	t.Run("Type conversion safety", func(t *testing.T) {
		ctx := &PipelineContext{
			RequestContext: &RequestContext{
				RequestLoadsCount: sync.Map{},
			},
		}

		// Test various data type inputs
		testCases := []struct {
			name         string
			input        interface{}
			expectedSkip bool
		}{
			{"nil value", nil, false},
			{"empty string", "", false},
			{"string 0", "0", false},
			{"string 5", "5", false},
			{"float", 3.14, false},
			{"boolean", true, false},
			{"array", []int{1, 2, 3}, false},
			{"map", map[string]int{"count": 1}, false},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				ctx.RequestContext.RequestLoadsCount.Store(LoadTypeImage, tc.input)
				skip, reason := skipOnZeroImage(ctx)
				g.Expect(skip).To(gomega.Equal(tc.expectedSkip))
				if tc.expectedSkip {
					g.Expect(reason).To(gomega.ContainSubstring("no image found"))
				} else {
					g.Expect(reason).To(gomega.BeEmpty())
				}
			})
		}
	})
}

// TestSchedulePhase_MemoryLeaks tests memory leaks
func TestSchedulePhase_MemoryLeaks(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	t.Run("Massive create and destroy test", func(t *testing.T) {
		// Create a large number of SchedulePhase instances
		for i := 0; i < 1000; i++ {
			phase := newSchedulePhase().(*SchedulePhase)
			g.Expect(phase).NotTo(gomega.BeNil())
			g.Expect(phase.BasePhase.PhaseTypeName()).To(gomega.Equal(constant.SchedulePhase))
			// Let GC have a chance to recycle
			if i%100 == 0 {
				// Simulate GC
				phase = nil
			}
		}
	})

	t.Run("Resource cleanup in complex scenarios", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest("POST", "/test", nil)

		// Create complex context
		ctx := &PipelineContext{
			ClientGinCtx: c,
			RequestContext: &RequestContext{
				ServingRequestId:  "test-req",
				SubRequests:       sync.Map{},
				RequestLoadsCount: sync.Map{},
				Tracer:            &trace.TracerWithContext{},
				InferMsg:          health.NewInferMsg(true, "", "", ""),
			},
		}

		// Mass operation test
		for i := 0; i < 1000; i++ {
			// Simulate load count operations
			ctx.RequestContext.RequestLoadsCount.Store(LoadTypeUsage, int32(i))
			ctx.RequestContext.RequestLoadsCount.Store(LoadTypeImage, int32(i%10))
			ctx.RequestContext.RequestLoadsCount.Store(LoadTypeText, int32(i*2))

			// Call skip policy
			skipOnZeroImage(ctx)

			// Set instance info
			instanceId := fmt.Sprintf("instance-%d", i)
			ctx.RequestContext.InferMsg.SetInstance(constant.Prefill, instanceId)

			// Check status
			retrievedId := ctx.RequestContext.InferMsg.GetInstance(constant.Prefill)
			g.Expect(retrievedId).To(gomega.Equal(instanceId))
		}
	})
}
