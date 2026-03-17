# StratonMesh

Universal platform orchestration engine. Write one manifest, deploy to any platform.

## Quick start

### Prerequisites

- **Go 1.22+** — [install](https://go.dev/dl/)
- **Docker** — for the Docker adapter and dev dependencies
- **etcd** — state store (started via Docker below)
- **NATS** — telemetry bus (started via Docker below)

### 1. Start infrastructure

```bash
make dev-deps
```

This starts etcd and NATS in Docker containers.

### 2. Build

```bash
make build
```

Produces four binaries in `bin/`:
- `stratonmesh` — CLI
- `sm-controller` — control plane
- `sm-agent` — node agent
- `sm-proxy` — smart proxy

### 3. Deploy a stack

```bash
# Deploy the example Mailu email suite
./bin/stratonmesh deploy examples/mailu/stack.yaml

# Or deploy Nextcloud
./bin/stratonmesh deploy examples/nextcloud/stack.yaml

# Check status
./bin/stratonmesh status

# Scale a service
./bin/stratonmesh scale mailu --service webmail --replicas 4

# Destroy
./bin/stratonmesh destroy mailu
```

### 4. Import from Git

```bash
# Import a stack from any Git repo with a docker-compose.yml
./bin/stratonmesh catalog add \
    --git https://github.com/Mailu/Mailu.git \
    --name mailu

# List imported blueprints
./bin/stratonmesh catalog list

# Deploy from the catalog
./bin/stratonmesh deploy --name mailu --param domain=mail.example.com
```

## Architecture

```
┌─────────────────────────────────────────────────────┐
│  CLI / Dashboard / Git Webhooks / API Gateway       │  Client interfaces
├─────────────────────────────────────────────────────┤
│  Catalog Onboarding Layer    │  IaC Pipeline  │  Policy Gates    │  Onboarding + GitOps
├─────────────────────────────────────────────────────┤
│  Orchestrator   │  Scheduler     │  Config Manager  │  Control plane
├─────────────────────────────────────────────────────┤
│  Docker │ Compose │ K8s │ Terraform │ Pulumi │ Proc │  Platform adapters
├─────────────────────────────────────────────────────┤
│  CoreDNS │ Service Registry │ Proxy │ Sidecar(opt)  │  Hybrid service mesh
├─────────────────────────────────────────────────────┤
│  NATS JetStream │ VictoriaMetrics │ Loki │ Grafana  │  Observability
├─────────────────────────────────────────────────────┤
│  Node Agents │ etcd │ NATS │ Harbor │ MinIO         │  Infrastructure
└─────────────────────────────────────────────────────┘
```

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
│   ├── scheduler/           # Placement engine
│   ├── adapters/            # Platform adapters
│   │   ├── docker/          # Docker Engine adapter
│   │   ├── compose/         # Docker Compose adapter
│   │   ├── kubernetes/      # Kubernetes adapter
│   │   ├── terraform/       # Terraform HCL adapter
│   │   ├── pulumi/          # Pulumi SDK adapter
│   │   └── process/         # systemd/launchd adapter
│   ├── catalog/             # Blueprint catalog
│   ├── importer/            # Git scanner + format parsers
│   ├── mesh/                # Service mesh components
│   │   ├── dns/             # CoreDNS integration
│   │   ├── registry/        # Service registry
│   │   ├── proxy/           # Smart proxy
│   │   └── sidecar/         # Optional mTLS sidecar
│   ├── telemetry/           # NATS JetStream telemetry
│   └── store/               # etcd state store
├── api/proto/               # gRPC + protobuf definitions
├── internal/                # Internal packages
├── web/                     # Dashboard (Next.js)
├── examples/                # Example stack manifests
│   ├── mailu/
│   └── nextcloud/
├── deploy/                  # Self-hosting manifests
└── docs/                    # Documentation
```

## Supported platforms

| Platform   | Adapter    | Status      |
|-----------|------------|-------------|
| Docker    | Engine API | ✅ Complete  |
| Compose   | compose-go | 🔨 Phase 3  |
| Kubernetes| client-go  | 🔨 Phase 3  |
| Terraform | hclwrite   | 🔨 Phase 3  |
| Pulumi    | auto SDK   | 🔨 Phase 4  |
| Process   | systemd    | 🔨 Phase 4  |

## Workload types

| Archetype    | Description                    | Example              |
|-------------|--------------------------------|----------------------|
| long-running | Persistent process + scaling   | API gateway, web app |
| stateful     | Ordered deploy + bound volumes | PostgreSQL, Redis    |
| batch        | Run-to-completion              | ML training, ETL     |
| scheduled    | Cron-triggered batch           | Backups, reports     |
| daemon       | One per node                   | Log collector, agent |
| composite    | Mix of above in one manifest   | Full-stack apps      |

## License

Apache 2.0
