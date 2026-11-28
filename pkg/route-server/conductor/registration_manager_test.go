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

package conductor

import (
	"context"
	"sync"
	"testing"
	"time"

	"route-server/pkg/common/config"
)

type fakeConductorClient struct {
	mu          sync.Mutex
	registers   []RegisterRequest
	unregisters []UnregisterRequest
}

func (c *fakeConductorClient) Register(_ context.Context, req RegisterRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.registers = append(c.registers, req)
	return nil
}

func (c *fakeConductorClient) Unregister(_ context.Context, req UnregisterRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unregisters = append(c.unregisters, req)
	return nil
}

func (c *fakeConductorClient) registerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.registers)
}

func (c *fakeConductorClient) unregisterCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.unregisters)
}

func (m *RegistrationManager) desiredEntriesHaveConflict() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.desired) == 0 {
		return false
	}
	for _, entry := range m.desired {
		if !entry.conflict {
			return false
		}
	}
	return true
}

type fakeModelResolver struct {
	models map[string]string
}

func (r fakeModelResolver) ResolveModelName(instanceID string) (string, error) {
	if model, ok := r.models[instanceID]; ok {
		return model, nil
	}
	return "fallback-model", nil
}

func newTestRegistrationManager(t *testing.T) (*RegistrationManager, *fakeConductorClient) {
	t.Helper()
	client := &fakeConductorClient{}
	manager, err := NewRegistrationManager(Config{
		Enabled:        true,
		BaseURL:        DefaultBaseURL,
		RequestTimeout: time.Second,
		RetryInterval:  10 * time.Millisecond,
		BlockSize:      DefaultBlockSize,
		RegisterRoles:  map[string]struct{}{RolePrefill: {}},
	}, client, fakeModelResolver{models: map[string]string{"p0": "qwen3-0.6b"}})
	if err != nil {
		t.Fatalf("NewRegistrationManager() error = %v", err)
	}
	manager.Start()
	t.Cleanup(manager.Close)
	return manager, client
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", message)
}

func TestRegistrationManagerOnlyWritesOnLeader(t *testing.T) {
	manager, client := newTestRegistrationManager(t)

	manager.OnAddInstance(map[string]*config.Route{
		"p0": {Ip: "10.0.0.1", Port: 8010, Role: RolePrefill},
	}, "test")

	time.Sleep(30 * time.Millisecond)
	if got := client.registerCount(); got != 0 {
		t.Fatalf("register count before leadership = %d, want 0", got)
	}

	manager.OnLeaderChanged(nil, true, nil)
	waitFor(t, 2*time.Second, func() bool { return client.registerCount() == 1 }, "register")

	client.mu.Lock()
	req := client.registers[0]
	client.mu.Unlock()
	if req.InstanceID != "p0" {
		t.Errorf("InstanceID = %q, want p0", req.InstanceID)
	}
	if req.ModelName != "qwen3-0.6b" {
		t.Errorf("ModelName = %q, want qwen3-0.6b", req.ModelName)
	}
	if req.Endpoint != "tcp://10.0.0.1:5557" {
		t.Errorf("Endpoint = %q, want tcp://10.0.0.1:5557", req.Endpoint)
	}
	if req.BlockSize != DefaultBlockSize {
		t.Errorf("BlockSize = %d, want %d", req.BlockSize, DefaultBlockSize)
	}
}

func TestRegistrationManagerUnregistersRemovedInstance(t *testing.T) {
	manager, client := newTestRegistrationManager(t)
	manager.OnLeaderChanged(nil, true, nil)
	manager.OnAddInstance(map[string]*config.Route{
		"p0": {Ip: "10.0.0.1", Port: 8010, Role: RolePrefill},
	}, "test")
	waitFor(t, 2*time.Second, func() bool { return client.registerCount() == 1 }, "register")

	manager.OnRemoveInstance([]string{"p0"}, "test")
	waitFor(t, 2*time.Second, func() bool { return client.unregisterCount() == 1 }, "unregister")

	client.mu.Lock()
	req := client.unregisters[0]
	client.mu.Unlock()
	if req.InstanceID != "p0" {
		t.Errorf("InstanceID = %q, want p0", req.InstanceID)
	}
	if req.TenantID != DefaultTenantID || req.DpRank != DefaultDpRank {
		t.Errorf("UnregisterRequest = %+v, want default tenant and rank", req)
	}
}

func TestRegistrationManagerIgnoresUnmanagedRole(t *testing.T) {
	manager, client := newTestRegistrationManager(t)
	manager.OnLeaderChanged(nil, true, nil)

	manager.OnAddInstance(map[string]*config.Route{
		"d0": {Ip: "10.0.0.2", Port: 8011, Role: RoleDecode},
	}, "test")

	time.Sleep(50 * time.Millisecond)
	if got := client.registerCount(); got != 0 {
		t.Fatalf("register count for unmanaged role = %d, want 0", got)
	}
}

func TestRegistrationManagerRoleChangeUnregisters(t *testing.T) {
	manager, client := newTestRegistrationManager(t)
	manager.OnLeaderChanged(nil, true, nil)
	manager.OnAddInstance(map[string]*config.Route{
		"p0": {Ip: "10.0.0.1", Port: 8010, Role: RolePrefill},
	}, "test")
	waitFor(t, 2*time.Second, func() bool { return client.registerCount() == 1 }, "register")

	manager.OnAddInstance(map[string]*config.Route{
		"p0": {Ip: "10.0.0.1", Port: 8010, Role: RoleDecode},
	}, "test")
	waitFor(t, 2*time.Second, func() bool { return client.unregisterCount() == 1 }, "unregister")
}

func TestRegistrationManagerSkipsSharedKVEndpoint(t *testing.T) {
	manager, client := newTestRegistrationManager(t)
	manager.OnLeaderChanged(nil, true, nil)
	manager.OnAddInstance(map[string]*config.Route{
		"p0": {Ip: "10.0.0.1", Port: 8010, Role: RolePrefill},
		"p1": {Ip: "10.0.0.1", Port: 8011, Role: RolePrefill},
	}, "test")

	time.Sleep(50 * time.Millisecond)
	if got := client.registerCount(); got != 0 {
		t.Fatalf("register count for conflicting endpoint = %d, want 0", got)
	}
	if !manager.desiredEntriesHaveConflict() {
		t.Fatalf("desired entries are not marked as endpoint conflicts")
	}
}
