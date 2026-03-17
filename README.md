# StratonMesh

Universal platform orchestration engine. Write one manifest, deploy to any platform.

> **Full architecture and design details:** [StratonMesh-HLD.md](StratonMesh-HLD.md)

---

## What it does

StratonMesh takes a single declarative manifest and compiles it to platform-native artifacts — Docker containers, Compose stacks, Kubernetes manifests, Terraform HCL, or Pulumi programs. It manages the full lifecycle: import from Git, policy-gated deployment, scheduling, auto-scaling, service discovery, and observability.

```yaml
# stack.yaml — deploy the same manifest to Docker, K8s, or Terraform
name: nextcloud
platform: kubernetes          # or: docker, compose, terraform, pulumi

services:
  - name: app
    image: nextcloud:28
    type: long-running
    replicas: 3
    port: 80

  - name: db
    image: postgres:16
    type: stateful             # → StatefulSet in K8s, RDS in Terraform
    env:
      POSTGRES_PASSWORD: "${DB_PASSWORD}"
```

---

## Quick start

### Prerequisites

- **Go 1.22+**
- **Docker** — for the Docker/Compose adapter and dev infrastructure

### 1. Start infrastructure

```bash
make dev-deps
```

Starts etcd and NATS in Docker containers (required by the controller and agent).

### 2. Build

```bash
make build
```

Produces four binaries in `bin/`:

| Binary | Role |
|--------|------|
| `stratonmesh` | CLI — deploy, scale, destroy, catalog, rollback |
| `sm-controller` | Control plane — orchestrator + scheduler + REST API |
| `sm-agent` | Node agent — heartbeat, metrics, provider detection |
| `sm-proxy` | Smart proxy — canary routing, load balancing |

### 3. Run the control plane

```bash
# Controller (REST API on :8080)
./bin/sm-controller --etcd localhost:2379 --nats nats://localhost:4222 &

# Node agent
./bin/sm-agent --node-name dev-local --region local --etcd localhost:2379 &
```

### 4. Deploy a stack

```bash
# Deploy the example Mailu email suite
./bin/stratonmesh deploy examples/mailu/stack.yaml

# Deploy Nextcloud
./bin/stratonmesh deploy examples/nextcloud/stack.yaml

# Check status
./bin/stratonmesh status

# Scale a service
./bin/stratonmesh scale mailu --service webmail --replicas 4

# Roll back to a previous version
./bin/stratonmesh rollback mailu --version 1700000000

# Destroy
./bin/stratonmesh destroy mailu
```

### 5. Import from Git (Catalog Onboarding Layer)

```bash
# Auto-detect format and import (docker-compose, Helm, K8s, or Dockerfile)
./bin/stratonmesh catalog add \
    --git https://github.com/Mailu/Mailu.git \
    --name mailu

# AI-assisted import — Claude analyses the repo and generates a manifest
./bin/stratonmesh catalog add \
    --git https://github.com/someorg/someapp.git \
    --name myapp \
    --mode ai

# List blueprints
./bin/stratonmesh catalog list

# Deploy from catalog with size profile and parameters
./bin/stratonmesh deploy --name mailu \
    --size M \
    --param domain=mail.example.com \
    --param tls=true
```

---

## Architecture

Seven tiers from client interfaces down to infrastructure:

```
┌──────────────────────────────────────────────────────────────────────┐
│  Tier 1 — Client Interfaces                                          │
│  CLI · Web Dashboard (Next.js) · Git webhooks · REST/gRPC API       │
├──────────────────────────────────────────────────────────────────────┤
│  Tier 2 — Catalog Onboarding Layer (COL)                            │
│  Git importer · Format parsers · Blueprint catalog · AI import      │
│  Size profiles (XS/S/M/L/XL) · Instantiation engine                │
├──────────────────────────────────────────────────────────────────────┤
│  Tier 3 — IaC Pipeline + GitOps                                     │
│  7-stage pipeline · OPA policy gate · GitHub/GitLab webhooks        │
├──────────────────────────────────────────────────────────────────────┤
│  Tier 4 — Control Plane                                             │
│  Orchestrator (state machine) · Scheduler (4-phase placement)       │
│  Vault config manager · Version ledger · Auto-scaler               │
├──────────────────────────────────────────────────────────────────────┤
│  Tier 5 — Platform Adapter Layer                                    │
│  Docker · Compose · Kubernetes · Terraform · Pulumi · Process       │
│  OpenShift · Mesos/Marathon · Nomad · Swarm  [planned]             │
├──────────────────────────────────────────────────────────────────────┤
│  Tier 6 — Hybrid Service Mesh                                       │
│  CoreDNS (L1) · Service registry/etcd (L2)                         │
│  Smart proxy + canary (L3) · mTLS sidecar (L4, optional)           │
├──────────────────────────────────────────────────────────────────────┤
│  Tier 7 — Observability                                             │
│  NATS JetStream · VictoriaMetrics · Loki · Grafana Tempo · Grafana  │
└──────────────────────────────────────────────────────────────────────┘
                Infrastructure: etcd · NATS · Harbor · MinIO · Vault
```

---

## Supported platforms

| Platform | Adapter | Status |
|----------|---------|--------|
| Docker Engine | Engine SDK | ✅ Complete |
| Docker Compose | compose-go | ✅ Complete |
| Kubernetes | client-go | ✅ Complete |
| Terraform (AWS) | hclwrite | ✅ Complete |
| systemd / Process | os/exec | ✅ Complete |
| Pulumi | auto SDK | 🔲 Planned |
| OpenShift | client-go + Routes | 🔲 Planned |
| Apache Mesos / Marathon | REST API | 🔲 Planned |
| HashiCorp Nomad | HCL job specs | 🔲 Planned |
| Docker Swarm | docker stack | 🔲 Planned |
| AWS ECS / Fargate | ECS API | 🔲 Planned |
| Azure Container Apps | ARM API | 🔲 Planned |
| Google Cloud Run | GCP API | 🔲 Planned |

---

## Workload archetypes

Six types drive lifecycle rules across all platform adapters:

| Archetype | Description | K8s resource | Terraform resource |
|-----------|-------------|--------------|-------------------|
| `long-running` | Persistent process, HTTP health checks, auto-scaling | Deployment | ECS Service / Cloud Run |
| `stateful` | Ordered startup, bound volumes, ordinal identity | StatefulSet | RDS / managed DB |
| `batch` | Run-to-completion, exit-code health, retries | Job | Lambda / AWS Batch |
| `scheduled` | Cron-triggered batch | CronJob | EventBridge |
| `daemon` | One instance per node, auto-spread | DaemonSet | ASG sidecar |
| `composite` | Implicit when mixing types in one manifest | Helm chart | Module bundle |

If `type` is omitted, `Service.InferType()` auto-classifies based on image name, volume mounts, and command.

---

## Project structure

```
stratonmesh/
├── cmd/
│   ├── stratonmesh/         # CLI binary
│   ├── sm-controller/       # Control plane server
│   ├── sm-agent/            # Node agent
│   └── sm-proxy/            # Smart proxy
├── pkg/
│   ├── manifest/            # Stack manifest types + parser
│   ├── orchestrator/        # State machine + reconciler
│   ├── scheduler/           # 4-phase placement engine
│   ├── pipeline/            # 7-stage IaC pipeline + OPA
│   ├── adapters/
│   │   ├── docker/          # Docker Engine adapter       ✅
│   │   ├── compose/         # Docker Compose adapter      ✅
│   │   ├── kubernetes/      # Kubernetes adapter          ✅
│   │   ├── terraform/       # Terraform HCL adapter       ✅
│   │   ├── pulumi/          # Pulumi SDK adapter          🔲
│   │   └── process/         # systemd / bare-metal        ✅
│   ├── catalog/             # Blueprint catalog + size profiles
│   ├── importer/            # Git scanner + format parsers + AI import
│   ├── autoscaler/          # Metric-driven auto-scaler
│   ├── gitops/              # GitHub/GitLab webhook + poll loop
│   ├── mesh/
│   │   ├── dns/             # CoreDNS integration
│   │   ├── registry/        # Service registry + load balancing
│   │   ├── proxy/           # Smart proxy + canary routing
│   │   └── sidecar/         # mTLS cert manager (SPIFFE)
│   ├── config/              # Vault secret injection
│   ├── telemetry/           # NATS JetStream telemetry bus
│   ├── store/               # etcd state store
│   └── api/                 # REST API server
├── api/proto/               # gRPC + protobuf definitions
├── internal/
│   ├── errors/              # Typed error codes
│   ├── logger/              # zap structured logger
│   └── version/             # Build version (ldflags)
├── web/                     # Dashboard (Next.js — in progress)
├── examples/
│   ├── mailu/stack.yaml     # Full Mailu email suite (7 services)
│   └── nextcloud/stack.yaml # Nextcloud + PostgreSQL + Redis (7 services)
├── deploy/
│   ├── Dockerfile           # Multi-stage controller image
│   └── docker-compose.dev.yml  # Full dev stack: etcd, NATS, Vault, Loki, Grafana
└── test/
    ├── integration/         # Integration tests (etcd + NATS required)
    └── e2e/                 # End-to-end tests
```

---

## REST API

The controller exposes a REST API on `:8080`:

```bash
# List stacks
curl http://localhost:8080/v1/stacks

# Deploy a stack
curl -X POST http://localhost:8080/v1/stacks \
  -H "Content-Type: application/json" \
  -d @examples/mailu/stack.yaml

# Get stack status
curl http://localhost:8080/v1/stacks/mailu

# List catalog blueprints
curl http://localhost:8080/v1/catalog

# List registered nodes
curl http://localhost:8080/v1/nodes
```

Full gRPC definitions: [`api/proto/stratonmesh.proto`](api/proto/stratonmesh.proto)

---

## Configuration

Key environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `SM_ETCD_ENDPOINTS` | `localhost:2379` | etcd cluster endpoints (comma-separated) |
| `SM_NATS_URL` | `nats://localhost:4222` | NATS server URL |
| `SM_VAULT_ADDR` | — | Vault server address for secret injection |
| `SM_VAULT_ROLE_ID` | — | Vault AppRole role ID |
| `SM_VAULT_SECRET_ID` | — | Vault AppRole secret ID |
| `SM_API_PORT` | `8080` | REST API listen port |
| `SM_NODE_NAME` | hostname | Node identity in the scheduler |
| `SM_LOG_LEVEL` | `info` | Log level: debug / info / warn / error |

---

## Development

```bash
# Run all unit tests
make test

# Run linter
make lint

# Cross-compile for Linux/macOS/Windows
make build-all

# Start full dev stack (etcd, NATS, Vault, VictoriaMetrics, Loki, Grafana)
make dev-up

# Generate gRPC stubs from proto
make proto

# Build controller Docker image
make docker
```

---

## Design decisions

| Decision | Why |
|----------|-----|
| Go over Python | Native SDKs for Docker, K8s, etcd, NATS, Helm, Terraform HCL; static binaries for the node agent; goroutines for concurrent reconciliation |
| etcd for all state | One store for desired/actual state, service registry, DNS, version ledger, node registration, and catalog; watch API drives the reconciler |
| NATS for telemetry only | NATS is async pub/sub; service discovery uses DNS + registry so apps need zero SDK changes |
| `PlatformAdapter` interface | Adding a new platform (Nomad, OpenShift, Fly.io) is one new package — zero changes to orchestrator, scheduler, or pipeline |
| 6 workload archetypes | Each archetype compiles to a different platform primitive; without archetypes the orchestrator needs per-service special cases |
| OPA for policy | Rego policies are hot-reloadable, versionable in Git, and testable; no code changes needed to tighten deployment rules |

---

## Documentation

- **[StratonMesh-HLD.md](StratonMesh-HLD.md)** — full high-level design: architecture deep-dive, state machine, scheduler algorithm, pipeline stages, service mesh layers, all adapter capabilities, and roadmap

---

## License

Copyright 2026 StratonMesh Contributors

Licensed under the [Apache License, Version 2.0](LICENSE).

You may not use this project except in compliance with the License. A copy is included in the [`LICENSE`](LICENSE) file and is also available at http://www.apache.org/licenses/LICENSE-2.0.
