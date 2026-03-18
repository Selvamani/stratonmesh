# StratonMesh Makefile
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS    := -ldflags "-X github.com/selvamani/stratonmesh/internal/version.Version=$(VERSION) \
	-X github.com/selvamani/stratonmesh/internal/version.GitCommit=$(GIT_COMMIT) \
	-X github.com/selvamani/stratonmesh/internal/version.BuildDate=$(BUILD_DATE)"

BINS := stratonmesh sm-controller sm-agent sm-proxy

# Local dev endpoint defaults (override on CLI if needed)
DEV_ETCD      ?= localhost:2379
DEV_NATS      ?= nats://localhost:4222
DEV_API_ADDR  ?= :8080
DEV_NODE_ID   ?= local-dev
DEV_REGION    ?= local
DEV_REPOS_DIR ?= $(HOME)/.stratonmesh/repos
ANTHROPIC_API_KEY ?=

.PHONY: all build clean test lint fmt help \
        tidy \
        stack-up stack-build-up stack-up-infra stack-up-obs \
        stack-down stack-destroy stack-logs stack-ps \
        dev dev-infra dev-controller dev-agent dev-web dev-stop \
        run-cli run-deploy-mailu run-deploy-nextcloud run-import-git \
        api-stacks api-nodes api-catalog api-health \
        web-install web-dev web-build web-docker \
        docker-build build-all proto

# ── Build ────────────────────────────────────────────────────────────────────

all: build web-build docker-build web-docker ## Build all binaries, web dashboard, and Docker images

build: $(BINS) ## Build all Go binaries

stratonmesh: ## Build the CLI
	@echo "  GO  bin/stratonmesh"
	@go build $(LDFLAGS) -o bin/stratonmesh ./cmd/stratonmesh/

sm-controller: ## Build the control plane (includes REST API)
	@echo "  GO  bin/sm-controller"
	@go build $(LDFLAGS) -o bin/sm-controller ./cmd/sm-controller/

sm-agent: ## Build the node agent
	@echo "  GO  bin/sm-agent"
	@go build $(LDFLAGS) -o bin/sm-agent ./cmd/sm-agent/

sm-proxy: ## Build the smart proxy
	@echo "  GO  bin/sm-proxy"
	@go build $(LDFLAGS) -o bin/sm-proxy ./cmd/sm-proxy/

clean: ## Remove build artifacts and PID files
	rm -rf bin/ .pid.*
	@echo "Cleaned."

# ── Code quality ─────────────────────────────────────────────────────────────

test: ## Run all unit tests with race detector
	go test -v -race -coverprofile=coverage.out ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

fmt: ## Format Go source
	gofmt -s -w .
	goimports -w .

# ── Dependency management ─────────────────────────────────────────────────────

tidy: ## Update all dev deps: go mod tidy + npm install
	@echo "→ go mod tidy"
	go mod tidy
	@echo "→ npm install (web/)"
	cd web && npm install
	@echo "Dependencies up to date."

# ── Cross-compilation ─────────────────────────────────────────────────────────

build-all: ## Cross-compile CLI + agent for linux/darwin/windows × amd64/arm64
	@for os in linux darwin windows; do \
		for arch in amd64 arm64; do \
			echo "  $$os/$$arch"; \
			GOOS=$$os GOARCH=$$arch go build $(LDFLAGS) -o bin/stratonmesh-$$os-$$arch ./cmd/stratonmesh/; \
			GOOS=$$os GOARCH=$$arch go build $(LDFLAGS) -o bin/sm-agent-$$os-$$arch ./cmd/sm-agent/; \
		done; \
	done

# ── Docker Compose stack ──────────────────────────────────────────────────────

stack-up: ## Start full stack (all services + observability) from pre-built images
	docker compose -f deploy/docker-compose.dev.yml up -d
	@$(MAKE) _stack-urls

stack-build-up: ## Rebuild images and start full stack
	docker compose -f deploy/docker-compose.dev.yml up -d --build
	@$(MAKE) _stack-urls

stack-up-infra: ## Start required infra only: etcd + NATS
	docker compose -f deploy/docker-compose.dev.yml up -d etcd nats

stack-up-obs: ## Start observability stack: VictoriaMetrics + Loki + Tempo + Grafana + Promtail
	docker compose -f deploy/docker-compose.dev.yml up -d victoriametrics loki tempo grafana promtail

stack-down: ## Stop all services, keep volumes
	docker compose -f deploy/docker-compose.dev.yml down

stack-destroy: ## Stop all services and delete all volumes (data loss!)
	docker compose -f deploy/docker-compose.dev.yml down -v

stack-logs: ## Tail logs from all stack services (Ctrl-C to stop)
	docker compose -f deploy/docker-compose.dev.yml logs -f

stack-ps: ## Show health and status of all stack services
	docker compose -f deploy/docker-compose.dev.yml ps

_stack-urls:
	@echo ""
	@echo "  sm-controller (API)  →  http://localhost:8080"
	@echo "  dashboard            →  http://localhost:3001"
	@echo "  grafana              →  http://localhost:4000  (admin / admin)"
	@echo "  vault                →  http://localhost:8200  (token: dev-token)"
	@echo "  victoriametrics      →  http://localhost:8428"
	@echo "  minio                →  http://localhost:9001  (stratonmesh / stratonmesh123)"
	@echo "  registry             →  localhost:5000"
	@echo "  nats monitor         →  http://localhost:8222"
	@echo ""

# ── Local dev (binaries on localhost, infra in Docker) ────────────────────────
#
#  Workflow:
#    Terminal 0:  make dev-infra       # start etcd + NATS in Docker
#    Terminal 1:  make dev-controller  # sm-controller on :8080
#    Terminal 2:  make dev-agent       # sm-agent with metrics on :9091
#    Terminal 3:  make dev-web         # Next.js dev server on :3000
#
#  Or run everything with background processes:
#    make dev          # infra up + build + controller + agent in bg + web in fg
#    make dev-stop     # kill background controller + agent

dev-infra: ## Start infra services in Docker (etcd, NATS, Vault, observability)
	docker compose -f deploy/docker-compose.dev.yml up -d \
		etcd nats vault victoriametrics loki grafana promtail tempo
	@echo "Waiting for etcd and NATS..."
	@sleep 4
	@echo "Infra ready.  etcd=localhost:2379  nats=localhost:4222  vault=localhost:8200"

dev-controller: sm-controller ## Build + run sm-controller locally (blocks — use a dedicated terminal)
	@echo "→ sm-controller  api=http://localhost$(DEV_API_ADDR)  repos=$(DEV_REPOS_DIR)"
	@mkdir -p $(DEV_REPOS_DIR)
	SM_ETCD=$(DEV_ETCD) \
	SM_NATS=$(DEV_NATS) \
	SM_API_ADDR=$(DEV_API_ADDR) \
	SM_REPOS_DIR=$(DEV_REPOS_DIR) \
	VAULT_ADDR=http://localhost:8200 \
	VAULT_TOKEN=dev-token \
	ANTHROPIC_API_KEY=$(ANTHROPIC_API_KEY) \
	./bin/sm-controller

dev-agent: sm-agent ## Build + run sm-agent locally (blocks — use a dedicated terminal)
	@echo "→ sm-agent  node=$(DEV_NODE_ID)  metrics=http://localhost:9091/metrics"
	SM_ETCD=$(DEV_ETCD) \
	SM_NATS=$(DEV_NATS) \
	SM_NODE_ID=$(DEV_NODE_ID) \
	SM_NODE_NAME=$(DEV_NODE_ID) \
	SM_REGION=$(DEV_REGION) \
	SM_METRICS_ADDR=:9091 \
	./bin/sm-agent

dev-web: ## Start Next.js dev server on :3000 (proxies API to localhost:8080)
	@echo "→ next dev  http://localhost:3000"
	cd web && SM_API_URL=http://localhost:8080 npm run dev

dev: dev-infra sm-controller sm-agent ## Start infra + controller + agent in bg + web dev server in fg
	@echo "→ starting sm-controller (background)..."
	@mkdir -p $(DEV_REPOS_DIR)
	@SM_ETCD=$(DEV_ETCD) SM_NATS=$(DEV_NATS) SM_API_ADDR=$(DEV_API_ADDR) \
	  SM_REPOS_DIR=$(DEV_REPOS_DIR) \
	  VAULT_ADDR=http://localhost:8200 VAULT_TOKEN=dev-token \
	  ANTHROPIC_API_KEY=$(ANTHROPIC_API_KEY) \
	  ./bin/sm-controller > /tmp/sm-controller.log 2>&1 & echo $$! > .pid.controller
	@sleep 2
	@echo "→ starting sm-agent (background)..."
	@SM_ETCD=$(DEV_ETCD) SM_NATS=$(DEV_NATS) \
	  SM_NODE_ID=$(DEV_NODE_ID) SM_NODE_NAME=$(DEV_NODE_ID) \
	  SM_REGION=$(DEV_REGION) SM_METRICS_ADDR=:9091 \
	  ./bin/sm-agent > /tmp/sm-agent.log 2>&1 & echo $$! > .pid.agent
	@sleep 1
	@echo ""
	@echo "  Controller  →  http://localhost:8080   (logs: tail -f /tmp/sm-controller.log)"
	@echo "  Agent       →  http://localhost:9091   (logs: tail -f /tmp/sm-agent.log)"
	@echo "  Dashboard   →  http://localhost:3000   (starting now...)"
	@echo "  Stop bg     →  make dev-stop"
	@echo ""
	cd web && SM_API_URL=http://localhost:8080 npm run dev

dev-stop: ## Kill background sm-controller and sm-agent processes
	@if [ -f .pid.controller ]; then \
		kill $$(cat .pid.controller) 2>/dev/null && echo "Stopped controller"; \
		rm -f .pid.controller; fi
	@if [ -f .pid.agent ]; then \
		kill $$(cat .pid.agent) 2>/dev/null && echo "Stopped agent"; \
		rm -f .pid.agent; fi

dev-logs: ## Tail background process logs
	@echo "=== sm-controller ===" && tail -20 /tmp/sm-controller.log || true
	@echo "=== sm-agent ===" && tail -20 /tmp/sm-agent.log || true

# ── API smoke tests (requires sm-controller on :8080) ─────────────────────────

api-health: ## Check controller health endpoint
	@curl -sf http://localhost:8080/v1/nodes > /dev/null && echo "API OK" || echo "API unreachable"

api-stacks: ## List all stacks
	curl -s http://localhost:8080/v1/stacks | jq .

api-nodes: ## List registered nodes
	curl -s http://localhost:8080/v1/nodes | jq .

api-catalog: ## List blueprint catalog
	curl -s http://localhost:8080/v1/catalog | jq .

# ── Quick deploy examples ─────────────────────────────────────────────────────

run-cli: build ## Run the CLI (example: make run-cli ARGS="status")
	./bin/stratonmesh $(ARGS)

run-deploy-mailu: build ## Deploy the Mailu example stack
	./bin/stratonmesh deploy examples/mailu/stack.yaml --platform docker

run-deploy-nextcloud: build ## Deploy the Nextcloud example stack
	./bin/stratonmesh deploy examples/nextcloud/stack.yaml --platform docker

run-import-git: build ## Import from Git (make run-import-git URL=https://... NAME=foo)
	./bin/stratonmesh catalog add --git $(URL) --name $(NAME)

# ── Web dashboard ─────────────────────────────────────────────────────────────

web-install: ## Install web dashboard npm dependencies
	cd web && npm install

web-dev: dev-web ## Alias for dev-web

web-build: ## Build web dashboard for production
	cd web && NEXT_PUBLIC_VERSION=$(VERSION) npm run build

web-docker: ## Build web dashboard Docker image
	docker build -t stratonmesh/dashboard:$(VERSION) \
		--build-arg NEXT_PUBLIC_VERSION=$(VERSION) ./web

# ── Docker image ──────────────────────────────────────────────────────────────

docker-build: ## Build controller Docker image
	docker build -t stratonmesh/controller:$(VERSION) -f deploy/Dockerfile .

# ── Proto codegen ─────────────────────────────────────────────────────────────

proto: ## Generate gRPC stubs from api/proto/stratonmesh.proto
	protoc --go_out=. --go-grpc_out=. --grpc-gateway_out=. api/proto/*.proto

# ── Help ──────────────────────────────────────────────────────────────────────

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
