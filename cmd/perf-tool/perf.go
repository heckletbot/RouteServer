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
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"infer-dp-scheduler/pkg/common/utils"
)

// Config parameters
type Config struct {
	URL           string
	Workers       int
	TotalRequests int
	PromptLength  int
	MaxTokens     int
	TargetQPS     int // New target QPS parameter
	mode          string
}

// Request metrics
type RequestMetrics struct {
	TTFT    time.Duration // First token latency
	TPOT    time.Duration // Average latency per token
	Latency time.Duration // Total latency
	Tokens  int           // Number of tokens received
	Success bool          // Whether the request succeeded
}

// Global state
type GlobalState struct {
	Config
	CompletedRequests  int64
	SuccessfulRequests int64
	FailedRequests     int64
	TotalTokens        int64
	StartTime          time.Time
	Metrics            []RequestMetrics
	MetricsMutex       sync.Mutex
	ProgressStop       chan struct{}
}

// Generate random prompt
func generateRandomPrompt(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 "
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

// HTTP client pool
var clientPool = sync.Pool{
	New: func() interface{} {
		return &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        1000,
				MaxIdleConnsPerHost: 1000,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  true,
			},
			Timeout: 3600 * time.Second,
		}
	},
}

// Send streaming request
func sendStreamRequest(config Config, state *GlobalState) RequestMetrics {
	client, ok := clientPool.Get().(*http.Client)
	if !ok {
		return RequestMetrics{
			Success: false,
		}
	}
	defer clientPool.Put(client)
	payload := map[string]interface{}{
		"prompt":      generateRandomPrompt(config.PromptLength),
		"max_tokens":  config.MaxTokens,
		"temperature": 0.1,
		"stream":      true,
	}
	jsonData, err := utils.SonicApi.Marshal(payload)
	if err != nil {
		log.Printf("JSON marshal error: %v", err)
		return RequestMetrics{Success: false}
	}
	metrics := RequestMetrics{}
	start := time.Now()
	req, err := http.NewRequest("POST", config.URL, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Request creation error: %v", err)
		return RequestMetrics{Success: false}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Request failed: %v", err)
		return RequestMetrics{Success: false}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Request failed with status: %d", resp.StatusCode)
		return RequestMetrics{Success: false}
	}
	totalTokenDelay, tokenCount := dealStreamResponse(resp, &metrics, start)
	endTime := time.Now()
	metrics.Latency = endTime.Sub(start)
	metrics.Tokens = tokenCount
	if tokenCount > 1 {
		metrics.TPOT = totalTokenDelay / time.Duration(tokenCount-1)
	} else if tokenCount == 1 {
		metrics.TPOT = metrics.TTFT
	} else {
		metrics.TPOT = 0
	}
	metrics.Success = true
	return metrics
}

func dealStreamResponse(resp *http.Response, metrics *RequestMetrics, start time.Time) (time.Duration, int) {
	reader := bufio.NewReader(resp.Body)
	firstToken := true
	var lastTokenTime time.Time
	totalTokenDelay := time.Duration(0)
	tokenCount := 0
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("Read error: %v", err)
			break
		}
		if bytes.HasPrefix(line, []byte("data: ")) {
			data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data: ")))
			if bytes.Equal(data, []byte("[DONE]")) {
				break
			}
			var response struct {
				Choices []struct {
					Text string `json:"text"`
				} `json:"choices"`
			}
			if err := utils.SonicApi.Unmarshal(data, &response); err != nil {
				log.Printf("JSON unmarshal error: %v", err)
				continue
			}
			if len(response.Choices) > 0 && response.Choices[0].Text != "" {
				currentTime := time.Now()
				tokenCount++

				if firstToken {
					// Record first token time
					metrics.TTFT = currentTime.Sub(start)
					firstToken = false
					lastTokenTime = currentTime
				} else {
					// Calculate inter-token delay
					tokenDelay := currentTime.Sub(lastTokenTime)
					totalTokenDelay += tokenDelay
					lastTokenTime = currentTime
				}
			}
		}
	}
	return totalTokenDelay, tokenCount
}

// Worker thread function
func worker(config Config, state *GlobalState, requests int, workerID int) {
	localMetrics := make([]RequestMetrics, 0, requests)
	var localTokens, localSuccess int64

	for i := 0; i < requests; i++ {
		metrics := sendStreamRequest(config, state)
		if metrics.Success {
			localMetrics = append(localMetrics, metrics)
			localTokens += int64(metrics.Tokens)
			localSuccess++
		}
		atomic.AddInt64(&state.CompletedRequests, 1)
	}

	// Batch submit results
	state.MetricsMutex.Lock()
	state.Metrics = append(state.Metrics, localMetrics...)
	state.MetricsMutex.Unlock()

	atomic.AddInt64(&state.TotalTokens, localTokens)
	atomic.AddInt64(&state.SuccessfulRequests, localSuccess)
}

// Worker thread function - refactored for steady-rate sending
func avgWorker(config Config, state *GlobalState, workerID int, tokenChan <-chan time.Time) {
	for range tokenChan {
		metrics := sendStreamRequest(config, state)
		atomic.AddInt64(&state.CompletedRequests, 1)

		if metrics.Success {
			atomic.AddInt64(&state.SuccessfulRequests, 1)
			atomic.AddInt64(&state.TotalTokens, int64(metrics.Tokens))

			state.MetricsMutex.Lock()
			state.Metrics = append(state.Metrics, metrics)
			state.MetricsMutex.Unlock()
		}
	}
}

// Print progress
func printProgress(state *GlobalState) {
	startTime := state.StartTime
	lastReportTime := time.Now()
	lastCompleted := int64(0)
	for {
		select {
		case <-state.ProgressStop:
			clearProgressBar()
			return
		default:
			completed := atomic.LoadInt64(&state.CompletedRequests)
			currentTime := time.Now()
			elapsed := currentTime.Sub(startTime).Seconds()

			// Calculate QPS
			qps := 0.0
			if elapsed > 0 {
				qps = float64(completed) / elapsed
			}

			// Calculate instantaneous QPS
			instantQps := 0.0
			timeSinceLast := currentTime.Sub(lastReportTime).Seconds()
			completedSinceLast := completed - lastCompleted
			if timeSinceLast > 0 {
				instantQps = float64(completedSinceLast) / timeSinceLast
			}
			progress := float64(completed) / float64(state.TotalRequests) * 100
			eta := 0.0
			if qps > 0 {
				eta = float64(int64(state.TotalRequests)-completed) / qps
			}
			progressLine := fmt.Sprintf(
				"\r[Progress] %d/%d (%.1f%%) | QPS: %.1f (Inst: %.1f) | Elapsed: %.0fs | ETA: %.0fs",
				completed, state.TotalRequests, progress, qps, instantQps, elapsed, eta,
			)
			if len(progressLine) < 100 {
				progressLine += strings.Repeat(" ", 100-len(progressLine))
			}
			fmt.Print(progressLine)
			if runtime.GOOS == "windows" {
				err := os.Stdout.Sync()
				if err != nil {
					fmt.Printf("failed to flush windows std")
				}
			}
			lastReportTime = currentTime
			lastCompleted = completed
			time.Sleep(2 * time.Second)
		}
	}
}

func clearProgressBar() {
	fmt.Print("\r")
	if runtime.GOOS == "windows" {
		fmt.Print(strings.Repeat(" ", 100))
		fmt.Print("\r")
	}
}

// Calculate percentile
func percentile(durations []float64, p float64) float64 {
	if len(durations) == 0 {
		return 0
	}
	sort.Float64s(durations)
	index := int(float64(len(durations)) * p)
	if index >= len(durations) {
		index = len(durations) - 1
	}
	return durations[index]
}

func printReport(state *GlobalState) {
	var ttfts []float64
	var tpots []float64
	var latencies []float64
	var tokens []int
	state.MetricsMutex.Lock()
	defer state.MetricsMutex.Unlock()
	for _, m := range state.Metrics {
		if m.Success {
			ttfts = append(ttfts, m.TTFT.Seconds()*1000)
			tpots = append(tpots, m.TPOT.Seconds()*1000)
			latencies = append(latencies, m.Latency.Seconds()*1000)
			tokens = append(tokens, m.Tokens)
		}
	}
	avgTokens := 0
	for _, v := range tokens {
		avgTokens += v
	}
	if len(tokens) > 0 {
		avgTokens /= len(tokens)
	}
	report := Report{
		successful:    atomic.LoadInt64(&state.SuccessfulRequests),
		totalDuration: time.Since(state.StartTime).Seconds(),
		avgTTFT:       avg(ttfts),
		ttfts:         ttfts,
		avgTPOT:       avg(tpots),
		tpots:         tpots,
		avgLatency:    avg(latencies),
		latencies:     latencies,
		avgTokens:     avgTokens,
	}
	qps := 0.0
	if report.totalDuration > 0 {
		qps = float64(report.successful) / report.totalDuration
	}
	report.qps = qps
	printResult(state, &report)
}

type Report struct {
	successful    int64
	totalDuration float64
	qps           float64
	avgTTFT       float64
	ttfts         []float64
	avgTPOT       float64
	tpots         []float64
	avgLatency    float64
	latencies     []float64
	avgTokens     int
}

func avg(arr []float64) float64 {
	avgMetric := 0.0
	for _, v := range arr {
		avgMetric += v
	}
	if len(arr) > 0 {
		avgMetric /= float64(len(arr))
	}
	return avgMetric
}

func printResult(state *GlobalState, report *Report) {
	totalTokens := atomic.LoadInt64(&state.TotalTokens)
	fmt.Println("\n\nBenchmark Results:")
	fmt.Printf("  Successful requests: %d/%d\n", report.successful, state.TotalRequests)
	fmt.Printf("  Total tokens generated: %d\n", totalTokens)
	fmt.Printf("  Total time: %.2f seconds\n", report.totalDuration)
	fmt.Printf("  QPS: %.2f requests/second\n", report.qps)
	fmt.Printf("  TTFT (avg): %.2f ms\n", report.avgTTFT)
	if len(report.ttfts) > 0 {
		fmt.Printf("  TTFT (p95): %.2f ms\n", percentile(report.ttfts, 0.95))
		fmt.Printf("  TTFT (p99): %.2f ms\n", percentile(report.ttfts, 0.99))
	}
	fmt.Printf("  TPOT (avg): %.2f ms/token\n", report.avgTPOT)
	if len(report.tpots) > 0 {
		fmt.Printf("  TPOT (p95): %.2f ms/token\n", percentile(report.tpots, 0.95))
		fmt.Printf("  TPOT (p99): %.2f ms/token\n", percentile(report.tpots, 0.99))
	}
	fmt.Printf("  Latency (avg): %.2f ms\n", report.avgLatency)
	if len(report.latencies) > 0 {
		fmt.Printf("  Latency (p95): %.2f ms\n", percentile(report.latencies, 0.95))
		fmt.Printf("  Latency (p99): %.2f ms\n", percentile(report.latencies, 0.99))
	}
	fmt.Printf("  Tokens per request (avg): %d\n", report.avgTokens)
}

func createTokenGenerator(qps, totalRequests int) (<-chan time.Time, error) {
	if qps <= 0 || totalRequests <= 0 {
		return nil, errors.New("invalid qps or total requests")
	}

	tokenChan := make(chan time.Time, qps*10)
	interval := time.Second / time.Duration(qps)

	go func() {
		defer close(tokenChan)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for i := 0; i < totalRequests; i++ {
			tokenChan <- <-ticker.C
		}
	}()
	return tokenChan, nil
}

func main() {
	// Parse command line arguments
	url := flag.String("url", "", "Target API endpoint URL (required)")
	workers := flag.Int("workers", 50, "Number of concurrent workers")
	totalRequests := flag.Int("total-requests", 1000, "Total number of requests")
	promptLength := flag.Int("prompt-length", 512, "Length of input prompt")
	maxTokens := flag.Int("max-tokens", 256, "Number of tokens to generate")
	targetQPS := flag.Int("target-qps", 50, "Target QPS (requests per second)")
	mode := flag.String("mode", "burst", "Execution mode: 'avg' for steady rate, 'burst' for instantaneous request sending")
	flag.Parse()

	// Validate parameters
	if *url == "" {
		fmt.Println("Error: URL is required")
		flag.Usage()
		os.Exit(1)
	}

	// Ensure URL format is correct
	if !strings.HasPrefix(*url, "http") {
		*url = "http://" + *url
	}

	// Initialize config
	config := Config{
		URL:           *url,
		Workers:       *workers,
		TotalRequests: *totalRequests,
		PromptLength:  *promptLength,
		MaxTokens:     *maxTokens,
		TargetQPS:     *targetQPS,
		mode:          *mode,
	}

	// Initialize global state
	state := &GlobalState{
		Config:       config,
		StartTime:    time.Now(),
		ProgressStop: make(chan struct{}),
	}

	// Print configuration information
	printStarter(config)

	// Start progress monitoring
	go printProgress(state)

	// Create worker pool
	var wg sync.WaitGroup

	if config.mode == "burst" {
		requestsPerWorker := config.TotalRequests / config.Workers
		remainder := config.TotalRequests % config.Workers
		for i := 0; i < config.Workers; i++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()
				requests := requestsPerWorker
				if workerID < remainder {
					requests++
				}
				worker(config, state, requests, workerID)
			}(i)
		}
	} else {
		tokenChan, err := createTokenGenerator(config.TargetQPS, config.TotalRequests)
		if err != nil {
			fmt.Printf("Create token generator failed: %v\n", err)
			os.Exit(1)
		}
		wg.Add(config.Workers)
		for i := 0; i < config.Workers; i++ {
			go func(workerID int) {
				defer wg.Done()
				avgWorker(config, state, workerID, tokenChan)
			}(i)
		}
	}

	// Wait for all workers to complete
	wg.Wait()

	// Stop progress monitoring
	close(state.ProgressStop)
	time.Sleep(200 * time.Millisecond) // Ensure progress printing is complete

	// Print final report
	printReport(state)
}

func printStarter(config Config) {
	fmt.Println("Starting vLLM streaming benchmark with configuration:")
	fmt.Printf("  URL: %s\n", config.URL)
	fmt.Printf("  Workers: %d\n", config.Workers)
	fmt.Printf("  Total Requests: %d\n", config.TotalRequests)
	fmt.Printf("  Prompt Length: %d\n", config.PromptLength)
	fmt.Printf("  Max Tokens: %d\n", config.MaxTokens)
	fmt.Printf("  Mode: %s\n", config.mode)
	if config.mode == "avg" {
		fmt.Printf("  Target QPS: %d\n", config.TargetQPS)
	}

	fmt.Println()
}
