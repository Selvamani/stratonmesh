# StratonMesh Makefile
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -ldflags "-X github.com/stratonmesh/stratonmesh/internal/version.Version=$(VERSION) \
	-X github.com/stratonmesh/stratonmesh/internal/version.GitCommit=$(GIT_COMMIT) \
	-X github.com/stratonmesh/stratonmesh/internal/version.BuildDate=$(BUILD_DATE)"

BINS := stratonmesh sm-controller sm-agent sm-proxy sm-api

.PHONY: all build clean test lint fmt run help stack-up stack-up-infra stack-down stack-destroy stack-logs stack-ps

all: build web-build docker-build web-docker ## Build all binaries, web dashboard, and Docker images

build: $(BINS) ## Build all binaries

stratonmesh: ## Build the CLI
	@echo "Building stratonmesh CLI..."
	go build $(LDFLAGS) -o bin/stratonmesh ./cmd/stratonmesh/

sm-controller: ## Build the control plane
	@echo "Building sm-controller..."
	go build $(LDFLAGS) -o bin/sm-controller ./cmd/sm-controller/

sm-agent: ## Build the node agent
	@echo "Building sm-agent..."
	go build $(LDFLAGS) -o bin/sm-agent ./cmd/sm-agent/

sm-proxy: ## Build the smart proxy
	@echo "Building sm-proxy..."
	go build $(LDFLAGS) -o bin/sm-proxy ./cmd/sm-proxy/

sm-api: ## Build the REST API server
	@echo "Building sm-api..."
	go build $(LDFLAGS) -o bin/sm-api ./cmd/sm-api/

clean: ## Remove build artifacts
	rm -rf bin/

test: ## Run all tests
	go test -v -race -coverprofile=coverage.out ./...

lint: ## Run linter
	golangci-lint run ./...

fmt: ## Format code
	gofmt -s -w .
	goimports -w .

# --- Cross-compilation ---

build-all: ## Build for all platforms
	@for os in linux darwin windows; do \
		for arch in amd64 arm64; do \
			echo "Building for $$os/$$arch..."; \
			GOOS=$$os GOARCH=$$arch go build $(LDFLAGS) -o bin/stratonmesh-$$os-$$arch ./cmd/stratonmesh/; \
			GOOS=$$os GOARCH=$$arch go build $(LDFLAGS) -o bin/sm-agent-$$os-$$arch ./cmd/sm-agent/; \
		done; \
	done

# --- Development ---

dev-deps: ## Start development dependencies (etcd + NATS)
	docker run -d --name sm-etcd -p 2379:2379 \
		quay.io/coreos/etcd:v3.5.12 \
		/usr/local/bin/etcd --advertise-client-urls http://0.0.0.0:2379 --listen-client-urls http://0.0.0.0:2379
	docker run -d --name sm-nats -p 4222:4222 -p 8222:8222 \
		nats:latest -js

dev-deps-stop: ## Stop development dependencies
	docker rm -f sm-etcd sm-nats 2>/dev/null || true

stack-up: ## Bring up the full stack (infra + all services + observability)
	docker compose -f deploy/docker-compose.dev.yml up -d
	@echo ""
	@echo "  sm-controller (API) → http://localhost:8080"
	@echo "  dashboard   → http://localhost:3001"
	@echo "  grafana     → http://localhost:4000  (admin/admin)"
	@echo "  vault       → http://localhost:8200  (token: dev-token)"
	@echo "  victoriametrics → http://localhost:8428"
	@echo "  minio       → http://localhost:9001  (stratonmesh/stratonmesh123)"
	@echo "  registry    → localhost:5000"
	@echo "  nats-mon    → http://localhost:8222"
	@echo ""

stack-build-up: ## Bring up the full stack (infra + all services + observability)
	docker compose -f deploy/docker-compose.dev.yml up -d --build
	@echo ""
	@echo "  sm-controller (API) → http://localhost:8080"
	@echo "  dashboard   → http://localhost:3001"
	@echo "  grafana     → http://localhost:4000  (admin/admin)"
	@echo "  vault       → http://localhost:8200  (token: dev-token)"
	@echo "  victoriametrics → http://localhost:8428"
	@echo "  minio       → http://localhost:9001  (stratonmesh/stratonmesh123)"
	@echo "  registry    → localhost:5000"
	@echo "  nats-mon    → http://localhost:8222"
	@echo ""

stack-up-infra: ## Bring up required infra only (etcd + NATS)
	docker compose -f deploy/docker-compose.dev.yml up -d etcd nats

stack-down: ## Tear down the full stack (keeps volumes)
	docker compose -f deploy/docker-compose.dev.yml down

stack-destroy: ## Tear down the full stack and remove all volumes
	docker compose -f deploy/docker-compose.dev.yml down -v

stack-logs: ## Tail logs from all services (CTRL+C to stop)
	docker compose -f deploy/docker-compose.dev.yml logs -f

stack-ps: ## Show status of all stack services
	docker compose -f deploy/docker-compose.dev.yml ps

run-cli: build ## Run the CLI (example: make run-cli ARGS="status")
	./bin/stratonmesh $(ARGS)

run-deploy-mailu: build ## Deploy the Mailu example stack
	./bin/stratonmesh deploy examples/mailu/stack.yaml --platform docker

run-deploy-nextcloud: build ## Deploy the Nextcloud example stack
	./bin/stratonmesh deploy examples/nextcloud/stack.yaml --platform docker

run-import-git: build ## Import a stack from Git (example: make run-import-git URL=https://github.com/Mailu/Mailu.git NAME=mailu)
	./bin/stratonmesh catalog add --git $(URL) --name $(NAME)

run-api: build ## Run the REST API server (SM_API_ADDR=:8080)
	./bin/sm-api

api-stacks: ## List all stacks via REST API
	curl -s http://localhost:8080/v1/stacks | jq .

api-nodes: ## List all nodes via REST API
	curl -s http://localhost:8080/v1/nodes | jq .

# --- Web dashboard ---

web-install: ## Install web dashboard dependencies
	cd web && npm install

web-dev: ## Start web dashboard dev server (requires sm-api on :8080)
	cd web && npm run dev

web-build: ## Build web dashboard for production
	cd web && NEXT_PUBLIC_VERSION=$(VERSION) npm run build

web-docker: ## Build web dashboard Docker image
	docker build -t stratonmesh/dashboard:$(VERSION) --build-arg NEXT_PUBLIC_VERSION=$(VERSION) ./web

# --- Docker image ---

docker-build: ## Build Docker image for controller
	docker build -t stratonmesh/controller:$(VERSION) -f deploy/Dockerfile .

# --- Proto generation ---

proto: ## Generate gRPC code from proto files
	protoc --go_out=. --go-grpc_out=. --grpc-gateway_out=. api/proto/*.proto

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
