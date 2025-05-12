.PHONY: all build test lint helm-lint verify clean docker-build docker-push test-unit test-integration test-e2e help

# Project variables
BINARY_NAME := pte-collector
IMAGE_REPO := ghcr.io/deepaucksharma-nr/pte-collector
IMAGE_TAG := v0.1.0
BUILD_DIR := bin
GOPATH := $(shell go env GOPATH)

# Go build parameters
GO := go
GO_BUILD := $(GO) build
GO_TEST := $(GO) test
GO_LINT := golangci-lint
GO_CLEAN := $(GO) clean
GO_MOD := $(GO) mod
GO_BUILD_FLAGS := -v
GO_TEST_FLAGS := -v -race -cover

# Helm variables
HELM_CHART_DIR := helm/pte
HELM := helm
KUBEVAL := kubeval

# Build time versioning info
VERSION := $(IMAGE_TAG)
GIT_COMMIT := $(shell git rev-parse --short HEAD)
BUILD_TIME := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# Default target
all: protos build

# Help target
help:
	@echo "Phoenix Telemetry Edge (PTE) - Makefile targets:"
	@echo "  all             : Default target, builds the collector binary"
	@echo "  build           : Build the collector binary"
	@echo "  test            : Run all tests (unit, integration)"
	@echo "  test-unit       : Run unit tests"
	@echo "  test-integration: Run integration tests"
	@echo "  test-e2e        : Run end-to-end tests"
	@echo "  lint            : Run the Go linter"
	@echo "  helm-lint       : Validate the Helm chart"
	@echo "  verify          : Run all verification tasks (lint, tests, helm-lint)"
	@echo "  docker-build    : Build the Docker image"
	@echo "  docker-push     : Push the Docker image to registry"
	@echo "  clean           : Clean build artifacts"
	@echo "  bench           : Run all benchmarks"
	@echo "  bench-fallbackprocparser : Run fallback process parser benchmarks and generate report"
	@echo "  bench-[component] : Run benchmarks for a specific component"

# Build the collector binary
build:
	cd builder && $(GO_BUILD) $(GO_BUILD_FLAGS) \
		-ldflags "-X main.Version=$(VERSION) -X main.GitCommit=$(GIT_COMMIT) -X main.BuildTime=$(BUILD_TIME)" \
		-o ../$(BUILD_DIR)/$(BINARY_NAME) \
		../cmd/pte-collector

# Build the collector using OpenTelemetry Collector Builder
build-collector: 
	cd builder && \
		$(GOPATH)/bin/builder --config builder-config.yaml && \
		mv ./bin/pte-collector ../$(BUILD_DIR)/$(BINARY_NAME)

# Run all tests
test: test-unit test-integration

# Run unit tests
test-unit:
	$(GO_TEST) $(GO_TEST_FLAGS) ./internal/...

# Run integration tests
test-integration:
	$(GO_TEST) $(GO_TEST_FLAGS) ./tests/integration/... ./integration/...

# Run end-to-end tests
test-e2e:
	$(GO_TEST) $(GO_TEST_FLAGS) ./tests/e2e/...

# Run the Go linter
lint:
	$(GO_LINT) run ./...

# Validate the Helm chart
helm-lint:
	$(HELM) lint $(HELM_CHART_DIR)
	$(HELM) template $(HELM_CHART_DIR) | $(KUBEVAL) --strict

# Run basic statistical test for reservoir sampling
test-statistical:
	$(GO_TEST) $(GO_TEST_FLAGS) ./internal/processor/reservoirsampler/... -run=TestReservoirSampler_UnbiasedDistribution

# Run performance benchmarks
bench:
	$(GO_TEST) -benchmem -run=^$$ -bench . ./internal/processor/...

# Run specific component benchmarks
bench-%:
	$(GO_TEST) -benchmem -run=^$$ -bench . ./internal/processor/$*/...

# Run fallback process parser benchmarks
bench-fallbackprocparser:
	$(GO_TEST) -benchmem -run=^$$ -bench . ./internal/processor/fallbackprocparser/...
	mkdir -p benchmark_results
	$(GO_TEST) -benchmem -run=^$$ -bench . ./internal/processor/fallbackprocparser/... > benchmark_results/benchmark_output.txt
	python tests/benchmarks/analyze_fallbackprocparser.py --input benchmark_results/benchmark_output.txt --output benchmark_results/report
	@echo "Benchmark report generated at benchmark_results/report/benchmark_report.html"

# Run all verification tasks
verify: lint test helm-lint

# Build the Docker image
docker-build:
	docker build -t $(IMAGE_REPO):$(IMAGE_TAG) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		.

# Push the Docker image to registry
docker-push:
	docker push $(IMAGE_REPO):$(IMAGE_TAG)

# Generate protobuf files
protos:
	protoc -I=. --go_out=. --go_opt=paths=source_relative \
		internal/processor/reservoirsampler/spanprotos/span_summary.proto

# Generate and verify SBOM for the Docker image
sbom:
	syft packages $(IMAGE_REPO):$(IMAGE_TAG) -o json > sbom.json
	osv-scanner --sbom sbom.json

# Clean build artifacts
clean:
	$(GO_CLEAN)
	rm -rf $(BUILD_DIR)/*

# Install required dependencies
deps:
	$(GO_MOD) tidy
	$(GO_MOD) download
	$(GO_MOD) verify
	go install github.com/open-telemetry/opentelemetry-collector/cmd/builder@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# Install Helm
install-helm:
	curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
	$(HELM) repo add stable https://charts.helm.sh/stable

# Install kubeval for Helm validation
install-kubeval:
	curl -L https://github.com/instrumenta/kubeval/releases/latest/download/kubeval-darwin-amd64.tar.gz | tar xz
	chmod +x kubeval
	mv kubeval /usr/local/bin