# CLAUDE.md — StratonMesh Project Context

This file provides Claude with full context about the StratonMesh project. Read this before making any changes.

## What is StratonMesh

StratonMesh is a universal platform orchestration engine written in Go. It deploys any stack to any platform — Docker, Docker Compose, Kubernetes, Terraform, or Pulumi — from a single declarative manifest. It includes a Catalog Onboarding Layer layer that auto-imports stacks from Git repos containing docker-compose files, Helm charts, or Kubernetes manifests.

The system manages the full lifecycle: onboarding (Git import → blueprint catalog), deployment (7-stage IaC pipeline with policy gates), orchestration (state machine with reconciliation loop), scheduling (4-phase filter/score/bind/verify placement), scaling (auto-scaler with metric-driven feedback), service discovery (DNS + registry hybrid mesh), and observability (NATS JetStream telemetry bus).

## Architecture — Seven Tiers

1. **Client interfaces** — CLI (`stratonmesh`), web dashboard (Next.js, not yet built), Code-Server IDE, Git webhooks, gRPC API
2. **Catalog Onboarding Layer** — Git importer, format parsers (Compose, Helm, K8s, Dockerfile), blueprint catalog in etcd, instantiation engine with size profiles
3. **IaC pipeline + GitOps** — 7 stages: parse → resolve env → interpolate vars → diff → policy gate → apply to intent store → reconcile
4. **Control plane** — Stack orchestrator (state machine), scheduler (placement engine), config manager (Vault + etcd), version ledger (rollback)
5. **Platform adapter layer** — Docker, Compose, Kubernetes, Terraform, Pulumi, Process/systemd — all implement the `PlatformAdapter` interface
6. **Hybrid service mesh** — CoreDNS (Layer 1), service registry in etcd (Layer 2), smart proxy (Layer 3), optional sidecar with mTLS (Layer 4). NATS is async messaging only, NOT service discovery.
7. **Observability** — NATS JetStream (telemetry bus), VictoriaMetrics (metrics), Loki (logs), Tempo (traces), Grafana (dashboards), auto-scaler (feedback loop)

## Project Structure

```
stratonmesh/
├── cmd/
│   ├── stratonmesh/main.go        # CLI binary — deploy, scale, destroy, catalog commands
│   ├── sm-controller/main.go      # Control plane — orchestrator + scheduler + API server
│   ├── sm-agent/main.go           # Node agent — heartbeat, metrics, provider detection
│   └── sm-proxy/                  # Smart proxy (stub — to be implemented)
├── pkg/
│   ├── manifest/
│   │   ├── types.go               # Core types: Stack, Service, WorkloadType, all specs
│   │   └── parser.go              # YAML parsing, validation, interpolation, topo sort
│   ├── orchestrator/
│   │   └── orchestrator.go        # State machine, reconciliation loop, PlatformAdapter interface
│   ├── scheduler/
│   │   └── scheduler.go           # 4-phase placement: filter → score → bind → verify
│   ├── pipeline/
│   │   └── pipeline.go            # 7-stage IaC pipeline with policy evaluation
│   ├── adapters/
│   │   ├── docker/adapter.go      # Docker Engine SDK — container lifecycle
│   │   ├── compose/adapter.go     # Generates docker-compose.yml, runs compose up
│   │   ├── kubernetes/adapter.go  # Generates K8s YAML, kubectl apply
│   │   ├── terraform/adapter.go   # Generates HCL (.tf), terraform apply
│   │   └── pulumi/                # (stub — to be implemented)
│   ├── importer/
│   │   └── git.go                 # Git clone, format detection, Compose/Helm/K8s parsing
│   ├── store/
│   │   └── store.go               # etcd client: state, registry, DNS, ledger, nodes, catalog
│   ├── telemetry/
│   │   └── bus.go                 # NATS JetStream: events, metrics, logs publishing + subscribing
│   ├── mesh/                      # Service mesh components (to be implemented)
│   │   ├── dns/                   # CoreDNS integration
│   │   ├── registry/              # Service registry queries
│   │   ├── proxy/                 # Smart proxy / gateway
│   │   └── sidecar/               # Optional mTLS sidecar
│   ├── catalog/                   # Blueprint catalog operations (to be expanded)
│   └── config/                    # Config manager (Vault integration, to be implemented)
├── api/proto/                     # gRPC + protobuf definitions (to be defined)
├── internal/
│   ├── version/version.go         # Build version info (set via ldflags)
│   ├── logger/logger.go           # Structured logging with zap
│   └── errors/errors.go           # Typed error codes: validation, not_found, policy_denied, etc.
├── web/                           # Dashboard (Next.js, to be built)
├── deploy/
│   ├── Dockerfile                 # Multi-stage build for controller image
│   └── docker-compose.dev.yml     # Local dev: etcd + NATS + Vault + VictoriaMetrics + Grafana
├── examples/
│   ├── mailu/stack.yaml           # Full Mailu email suite manifest (7 services)
│   └── nextcloud/stack.yaml       # Full Nextcloud manifest (7 services, all archetypes)
├── go.mod                         # Go module with all dependencies
├── Makefile                       # Build, test, cross-compile, dev helpers
└── README.md                      # Getting started guide
```

## Key Interfaces and Types

### PlatformAdapter (pkg/orchestrator/orchestrator.go)

Every deployment target must implement this interface:

```go
type PlatformAdapter interface {
    Name() string
    Generate(ctx context.Context, stack *manifest.Stack) ([]byte, error)
    Apply(ctx context.Context, stack *manifest.Stack) error
    Status(ctx context.Context, stackID string) (*AdapterStatus, error)
    Destroy(ctx context.Context, stackID string) error
    Diff(ctx context.Context, desired, actual *manifest.Stack) (*DiffResult, error)
    Rollback(ctx context.Context, stackID string, version string) error
}
```

When adding a new platform adapter, create a new package under `pkg/adapters/`, implement this interface, and register it in `cmd/sm-controller/main.go`.

### Stack Manifest (pkg/manifest/types.go)

The `Stack` type is the universal manifest. Key fields:
- `Name`, `Version`, `Environment`, `Platform` — identity and target
- `Services []Service` — the workloads, each with a `Type` (WorkloadType)
- `Strategy` — rolling, blue-green, or canary deployment
- `Variables` — interpolated at pipeline stage 3
- `Metadata` — git SHA, pipeline ID, timestamps

### WorkloadType (pkg/manifest/types.go)

Six archetypes that determine lifecycle behavior:
- `long-running` — persistent process, HTTP health checks, auto-scaling (→ Deployment in K8s, ECS Service in Terraform)
- `stateful` — ordinal identity, bound volumes, ordered startup (→ StatefulSet in K8s, RDS in Terraform)
- `batch` — run-to-completion, exit code health, retries (→ Job in K8s, AWS Batch in Terraform)
- `scheduled` — cron-triggered batch (→ CronJob in K8s, EventBridge in Terraform)
- `daemon` — one per node, auto-spread (→ DaemonSet in K8s)
- `composite` — implicit when mixing types in one manifest

Services auto-classify via `Service.InferType()` if `Type` is not explicitly set.

### Store (pkg/store/store.go)

All state lives in etcd under the `/stratonmesh` prefix:
- `/stratonmesh/stacks/{id}/desired` — desired state (written by pipeline)
- `/stratonmesh/stacks/{id}/actual` — actual state (written by reconciler)
- `/stratonmesh/stacks/{id}/status` — lifecycle state string
- `/stratonmesh/services/{svc}/{stack}/{instance}` — service registry endpoints
- `/stratonmesh/dns/{fqdn}` — DNS A records
- `/stratonmesh/ledger/{stack}/{timestamp}` — version history for rollback
- `/stratonmesh/nodes/{id}` — node info with TTL lease (30s heartbeat)
- `/stratonmesh/catalog/{name}` — blueprint catalog entries

The store uses etcd watches for real-time notifications and compare-and-swap for optimistic concurrency in the scheduler.

### Orchestrator State Machine (pkg/orchestrator/orchestrator.go)

States: `pending → scheduling → provisioning → deploying → verifying → running → draining → stopped`

Failure handling: `deploying` or `verifying` can transition to `failed`. From `failed`, three paths: retry (up to 3x), automatic rollback (if `rollbackOnFailure: true`), or park for operator intervention.

The reconciliation loop runs every 30 seconds for all active stacks AND immediately on etcd watch triggers when desired state changes.

### Scheduler (pkg/scheduler/scheduler.go)

Four-phase pipeline:
1. **Filter** — hard constraints: resource fit, provider availability, node selector, taints
2. **Score** — soft preferences with configurable weights: bin-packing (35%), spread (25%), affinity (20%), cost (10%), locality (10%)
3. **Bind** — reserve resources on the selected node via etcd CAS
4. **Verify** — pre-flight check that the node agent is responsive

Multi-replica placement uses iterative scoring — after placing replica N, scores recalculate for replica N+1 (spread scorer penalizes the node that just received a replica).

### IaC Pipeline (pkg/pipeline/pipeline.go)

Seven stages:
1. Parse + validate (schema, lint, cycle detection)
2. Resolve environment (merge base + overlay)
3. Interpolate variables (${var}, vault: references left opaque)
4. Diff + plan (compare desired vs current in etcd)
5. Policy gate (blast radius, resource limits, replica caps)
6. Apply to intent store (atomic etcd write + ledger entry)
7. Reconcile (orchestrator watches etcd, converges state)

### Catalog Importer (pkg/importer/git.go)

Auto-detection priority: stratonmesh.yaml (1) > docker-compose.yml (2) > Chart.yaml (3) > K8s YAML (4) > Terraform .tf (5) > Dockerfile (6)

The importer clones the repo, scans the file tree, parses the detected format into a `manifest.Stack`, auto-classifies workload types, and saves a `store.Blueprint` to the catalog.

## Build and Run

```bash
# Prerequisites: Go 1.22+, Docker

# Start infrastructure
docker compose -f deploy/docker-compose.dev.yml up -d etcd nats

# Build all binaries
make build

# Run the controller (background)
./bin/sm-controller --etcd localhost:2379 --nats nats://localhost:4222 &

# Run the agent (background)
./bin/sm-agent --node-name dev-local --region local --etcd localhost:2379 &

# Deploy a stack
./bin/stratonmesh deploy examples/mailu/stack.yaml --platform docker

# Import from Git
./bin/stratonmesh catalog add --git https://github.com/Mailu/Mailu.git --name mailu

# Check status
./bin/stratonmesh status
```

## Code Conventions

- **Error handling**: Return errors, don't panic. Use `internal/errors` for typed errors with codes.
- **Logging**: Use `go.uber.org/zap` SugaredLogger everywhere. Pass logger via constructor injection.
- **Context**: Every public function that does I/O takes `context.Context` as the first parameter.
- **etcd keys**: Always go through `store.Store` methods — never construct keys manually in other packages.
- **YAML tags**: Every struct field that appears in manifests needs `yaml` and `json` tags.
- **Adapters**: Each adapter is self-contained in its own package. Never import one adapter from another.
- **Testing**: Use table-driven tests. Mock the store and adapters for unit tests. Integration tests go in `test/integration/`.

## What's Implemented vs TODO

### Implemented (Phase 1-2)
- [x] Manifest types, parsing, validation, interpolation, topological sort
- [x] etcd store with all operations (state, registry, DNS, ledger, nodes, catalog)
- [x] Orchestrator state machine with reconciliation loop
- [x] Scheduler (filter/score/bind/verify) with configurable weights
- [x] Docker adapter (full — create, start, stop, status, destroy, diff)
- [x] Compose adapter (generate YAML, apply via docker compose)
- [x] Kubernetes adapter (generate typed K8s resources, kubectl apply)
- [x] Terraform adapter (generate HCL with AWS provider, managed DB mapping)
- [x] Catalog Git importer with format auto-detection
- [x] NATS JetStream telemetry bus (events, metrics, logs)
- [x] Node agent with heartbeat, metrics, provider detection
- [x] Control plane server with adapter registration
- [x] IaC 7-stage pipeline with policy evaluation
- [x] CLI with deploy, scale, destroy, status, rollback, catalog commands
- [x] Error types, structured logging, version injection
- [x] Example manifests (Mailu, Nextcloud)
- [x] Makefile, Dockerfile, dev compose stack

### Implemented (Phase 3-4)
- [x] gRPC API proto definitions (`api/proto/stratonmesh.proto`) — StackService, CatalogService, NodeService with REST gateway annotations
- [x] REST API server (`pkg/api/server.go`, `cmd/sm-api/main.go`) — full stack/catalog/node CRUD over HTTP/JSON; integrated into controller
- [x] CoreDNS integration (`pkg/mesh/dns/coredns.go`) — etcd path sync, Corefile generation, watch-and-sync loop
- [x] Service registry HTTP API (`pkg/mesh/registry/registry.go`) — weighted random load balancing, health filtering, /resolve endpoint
- [x] Smart proxy with canary routing (`pkg/mesh/proxy/proxy.go`) — weight-based + header-forced canary splits, background health checks
- [x] mTLS sidecar cert manager (`pkg/mesh/sidecar/sidecar.go`) — self-signed ECDSA CA, SPIFFE URI SANs, TLS 1.3 config generation
- [x] Vault secret injection (`pkg/config/vault.go`) — AppRole + token auth, `vault:path#field` env resolution, TTL cache, token renewal loop
- [x] Blueprint size profiles + instantiation engine (`pkg/catalog/catalog.go`) — XS/S/M/L/XL profiles, `{{param}}` + `${param}` substitution, Publish()
- [x] Auto-scaler (`pkg/autoscaler/autoscaler.go`) — cpu/memory/requestRate metrics, cooldown, 3-tick scale-down hysteresis, wired into controller
- [x] GitOps continuous sync (`pkg/gitops/gitops.go`) — GitHub (HMAC-SHA256) + GitLab (token) webhook receivers, configurable poll loop
- [x] OPA policy evaluation (`pkg/pipeline/pipeline.go`) — full Rego policy replaces hardcoded checks; AddPolicy() for custom rules; fallback on OPA error
- [x] CI/CD pipeline (`.github/workflows/ci.yml`) — lint → test (with etcd+nats services) → multi-platform build → Docker push → GitHub Release
- [x] Test suite — manifest/parser_test, scheduler_test, pipeline_test (incl. OPA), catalog_test, autoscaler_test
- [x] Dev compose stack expanded (`deploy/docker-compose.dev.yml`) — added Loki, Grafana Tempo, MinIO, CoreDNS, container registry with healthchecks

### TODO (remaining)
- [ ] Web dashboard (Next.js in `web/`)
- [ ] Snapshot and backup engine (`pkg/snapshot/`)
- [ ] go.sum file (run `go mod tidy` after setting up deps)
- [ ] Integration test suite (`test/integration/`)
- [ ] protoc codegen — run `make proto` to generate Go stubs from `api/proto/stratonmesh.proto`

## Infrastructure Dependencies

Required:
- **etcd v3.5+** — state store (start with `docker compose -f deploy/docker-compose.dev.yml up -d etcd`)
- **NATS v2.10+** with JetStream — telemetry bus (start with `docker compose -f deploy/docker-compose.dev.yml up -d nats`)

Optional (add incrementally):
- **Vault v1.15+** — secrets management
- **VictoriaMetrics** — metrics storage
- **Grafana Loki** — log aggregation
- **Grafana Tempo** — distributed traces
- **Grafana** — dashboards and alerting
- **Harbor** — container image registry
- **MinIO** — object storage for snapshots
- **CoreDNS** — internal service discovery DNS

## Common Tasks

### Adding a new platform adapter

1. Create `pkg/adapters/yourplatform/adapter.go`
2. Implement the `orchestrator.PlatformAdapter` interface
3. Register in `cmd/sm-controller/main.go`: `orch.RegisterAdapter(yourAdapter)`
4. Add to CLI switch in `cmd/stratonmesh/main.go` under `runDeploy`
5. Add workload-to-resource mapping logic per archetype

### Adding a new manifest field

1. Add the field to the appropriate struct in `pkg/manifest/types.go` with yaml/json tags
2. If it affects validation, update `manifest.Validate()` in `parser.go`
3. If it needs interpolation, ensure it's covered in `manifest.Interpolate()`
4. Update each adapter's `Generate()` / `Apply()` to handle the new field
5. Update example manifests if relevant

### Adding a new importer format

1. Add detection logic in `importer.detectFormats()` in `pkg/importer/git.go`
2. Add a `parseNewFormat()` method that returns `*manifest.Stack`
3. Add the case to the switch in `importer.Import()`
4. The parser should map the foreign format's concepts to StratonMesh archetypes

### Debugging the reconciliation loop

The reconciler watches `/stratonmesh/stacks/*/desired` in etcd. When it detects a change:
1. Reads desired state from etcd
2. Calls the adapter's `Diff()` to compare against current
3. If changes exist, calls `Apply()` to converge

Set `SM_LOG_LEVEL=debug` to see all reconciler ticks and diff results.

### Testing with etcd

```bash
# Check what's in etcd
docker exec sm-etcd etcdctl get /stratonmesh --prefix --keys-only

# Read a specific stack's desired state
docker exec sm-etcd etcdctl get /stratonmesh/stacks/mailu/desired

# Watch for changes in real-time
docker exec sm-etcd etcdctl watch /stratonmesh --prefix
```

## Design Decisions and Rationale

**Why Go over Python**: Every dependency (Docker SDK, K8s client-go, etcd client, NATS client, Helm SDK, Terraform HCL writer, Compose parser) is a native Go library. Python would require CLI subprocess wrappers for Helm, Terraform, and Compose. Go cross-compiles to static binaries for the node agent. Goroutines handle thousands of concurrent reconciliation loops.

**Why etcd for everything**: One consistent state store for desired state, actual state, service registry, DNS, version ledger, node registration, and blueprint catalog. etcd's watch API drives the reconciliation loop. CAS transactions handle scheduling concurrency. TTL leases handle node health.

**Why NATS for telemetry but not service discovery**: NATS excels at async pub/sub (fire-and-forget events, fan-out notifications, stream processing). But it requires every caller to use the NATS SDK, which locks out standard protocols (HTTP, gRPC, database drivers). DNS + service registry handles synchronous RPC with zero SDK requirement.

**Why the PlatformAdapter interface**: The orchestrator never knows which platform it's deploying to. It calls `adapter.Apply()` and `adapter.Status()`. This means adding a new platform (Nomad, Fly.io, Railway) is one new package — zero changes to the orchestrator, scheduler, or pipeline.

**Why 6 workload archetypes**: They determine lifecycle rules. A batch job's "healthy" means exit code 0. A stateful service needs ordered startup. A daemon needs one-per-node placement. Without archetypes, the orchestrator would need per-service special cases. With them, the rules compose cleanly across any platform adapter.
