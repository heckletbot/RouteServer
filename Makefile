.PHONY: build test clean all run lint fmt vet help package

GO := go
GOFLAGS := -v
BINARY_NAME := route-server
CONFIG_TOOL := config_generator
SRC_DIR := cmd/route_server
CONFIG_TOOL_DIR := cmd/config-tool
BUILD_DIR := build
OUTPUT_DIR := output
PKG_NAME := route-server
DEPLOY_DIR := deploy

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "unknown")
BRANCH := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u '+%Y%m%d%H%M%S' 2>/dev/null || date -f "%Y%m%d%H%M%S" 2>/dev/null || echo "unknown")

LDFLAGS := -X 'route-server/cmd/route_server/server.version=$(VERSION)' \
           -X 'route-server/cmd/route_server/server.tagOrBranch=$(BRANCH)' \
           -X 'route-server/cmd/route_server/server.commitId=$(COMMIT)' \
           -X 'route-server/cmd/route_server/server.buildTime=$(BUILD_TIME)'

$(OUTPUT_DIR)/$(PKG_NAME)/bin:
	@mkdir -p $@

$(OUTPUT_DIR)/$(PKG_NAME)/bin/$(BINARY_NAME): | $(OUTPUT_DIR)/$(PKG_NAME)/bin
	@cd $(SRC_DIR) && $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o ../../$@ .

$(OUTPUT_DIR)/$(CONFIG_TOOL): | $(OUTPUT_DIR)/$(PKG_NAME)/bin
	@cd $(CONFIG_TOOL_DIR) && $(GO) build $(GOFLAGS) -o ../../$@ .

build-binary: $(OUTPUT_DIR)/$(PKG_NAME)/bin/$(BINARY_NAME)
	@echo "Binary built: $@"

build-config-tool: $(OUTPUT_DIR)/$(CONFIG_TOOL)
	@echo "Config tool built: $@"

build: build-binary build-config-tool
	@echo "Build complete: $(OUTPUT_DIR)/"

copy-deploy:
	@mkdir -p $(OUTPUT_DIR)
	@cp $(DEPLOY_DIR)/deploy.sh $(OUTPUT_DIR)/
	@cp $(DEPLOY_DIR)/*.yaml $(OUTPUT_DIR)/ 2>/dev/null || true
	@cp -r $(DEPLOY_DIR)/k8s-configmap $(OUTPUT_DIR)/ 2>/dev/null || true
	@cp -r $(DEPLOY_DIR)/scale $(OUTPUT_DIR)/ 2>/dev/null || true
	@cp -r $(DEPLOY_DIR)/pipelines $(OUTPUT_DIR)/ 2>/dev/null || true
	@cp Dockerfile $(OUTPUT_DIR)/ 2>/dev/null || true

version-info:
	@mkdir -p $(OUTPUT_DIR)/$(PKG_NAME)
	@printf 'Build Tag/Branch: %s\nBuild Commit ID: %s\nBuild Time: %s\n' \
		"$(BRANCH)" "$(COMMIT)" "$(BUILD_TIME)" > $(OUTPUT_DIR)/$(PKG_NAME)/version.info

package: build copy-deploy version-info
	@cd $(OUTPUT_DIR) && zip -q -r $(PKG_NAME)-$(VERSION)-$(BUILD_TIME).zip ./
	@echo "Package created: $(OUTPUT_DIR)/$(PKG_NAME)-$(VERSION)-$(BUILD_TIME).zip"

# Test pipelines need Bash pipefail so neither go test nor tee failures are hidden.
# Keep the shell change local to these targets.
test test-coverage test-unit: SHELL := /bin/bash

test:
	@mkdir -p $(BUILD_DIR)
	@echo "Running tests at $$(date)" | tee $(BUILD_DIR)/test.log
	@set -o pipefail; $(GO) test ./... -v -failfast=false 2>&1 | tee -a $(BUILD_DIR)/test.log

test-coverage:
	@mkdir -p $(BUILD_DIR)
	@echo "Running tests with coverage at $$(date)" | tee $(BUILD_DIR)/coverage.log
	@set -o pipefail; $(GO) test ./... -v -failfast=false -coverprofile=$(BUILD_DIR)/coverage.out 2>&1 | tee -a $(BUILD_DIR)/coverage.log
	@echo "Coverage report generated: $(BUILD_DIR)/coverage.out"

test-unit:
	@mkdir -p $(BUILD_DIR)
	@echo "Running unit tests at $$(date)" | tee $(BUILD_DIR)/unit-test.log
	@set -o pipefail; $(GO) test ./... -v -failfast=false -short 2>&1 | tee -a $(BUILD_DIR)/unit-test.log

clean:
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR) $(OUTPUT_DIR)
	@rm -rf $(CONFIG_TOOL_DIR)/output $(SRC_DIR)/output
	@rm -f $(PKG_NAME)-*.zip

all: clean build

fmt:
	@echo "Formatting code..."
	$(GO) fmt ./...

vet:
	@echo "Running vet..."
	$(GO) vet ./...

lint: fmt vet

help:
	@echo "Available targets:"
	@echo "  build          - Build $(BINARY_NAME) and $(CONFIG_TOOL)"
	@echo "  build-binary   - Build $(BINARY_NAME) only"
	@echo "  build-config-tool - Build $(CONFIG_TOOL) only"
	@echo "  package        - Build and create zip package"
	@echo "  test           - Run all tests (log: $(BUILD_DIR)/test.log)"
	@echo "  test-coverage  - Run tests with coverage"
	@echo "  test-unit      - Run unit tests only"
	@echo "  clean          - Clean $(BUILD_DIR)/ and $(OUTPUT_DIR)/"
	@echo "  all            - Clean and build"
	@echo "  lint           - Run fmt and vet"
	@echo "  help           - Show this help"
