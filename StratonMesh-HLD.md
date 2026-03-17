# StratonMesh — High-Level Design Document

**Version:** 4.0
**Date:** March 17, 2026
**Status:** Current

---

## 1. Executive Summary

StratonMesh is a universal platform orchestration engine that automates the full lifecycle of microservice stacks across any infrastructure target. A single declarative manifest compiles to platform-native artifacts — Docker containers, Docker Compose stacks, Kubernetes manifests, Terraform HCL, or Pulumi programs — enabling write-once, deploy-anywhere infrastructure management.

The system is organised into seven architectural tiers: client interfaces, a **Catalog Onboarding Layer (COL)** for zero-friction stack import, a seven-stage IaC pipeline with GitOps, a control plane with distributed scheduling, a platform adapter layer, a hybrid service mesh, and a unified observability stack with auto-scaling feedback loops.

The COL supports three import modes: **catalog** (parse metadata, images pre-built), **repo** (clone kept on disk, images built from source at deploy time), and **AI** (Claude analyses any repository and generates a StratonMesh manifest automatically, falling back to format detection on failure).

---

## 2. Architecture Overview

### 2.1 Seven-tier architecture

```
+--------------------------------------------------------------------+
|  Tier 1 -- Client Interfaces                                       |
|  CLI (stratonmesh)  Web Dashboard (Next.js)  Code-Server IDE       |
|  Git webhooks  REST API (sm-api)  gRPC (proto-defined)             |
+--------------------------------------------------------------------+
|  Tier 2 -- Catalog Onboarding Layer (COL)                          |
|  Git importer  Format parsers  Blueprint catalog (etcd)            |
|  Instantiation engine  Size profiles  AI import (Claude)           |
+--------------------------------------------------------------------+
|  Tier 3 -- IaC Pipeline + GitOps                                   |
|  7-stage pipeline  OPA policy gate  GitOps sync loop               |
|  HMAC-SHA256 GitHub webhooks  GitLab token webhooks                |
+--------------------------------------------------------------------+
|  Tier 4 -- Control Plane                                           |
|  Stack orchestrator (state machine)  Scheduler (4-phase)           |
|  Config manager (Vault + etcd)  Version ledger  Auto-scaler        |
+--------------------------------------------------------------------+
|  Tier 5 -- Platform Adapter Layer                                  |
|  Docker  Compose  Kubernetes  Terraform  Pulumi  Process           |
+--------------------------------------------------------------------+
|  Tier 6 -- Hybrid Service Mesh                                     |
|  CoreDNS (Layer 1)  Service registry/etcd (Layer 2)               |
|  Smart proxy + canary (Layer 3)  mTLS sidecar (Layer 4, optional) |
+--------------------------------------------------------------------+
|  Tier 7 -- Observability                                           |
|  NATS JetStream (telemetry bus)  VictoriaMetrics  Promtail         |
|  Loki  Grafana Tempo  Grafana dashboards  Auto-scaler              |
+--------------------------------------------------------------------+
```

### 2.2 Infrastructure layer

Single-node etcd and NATS for development (docker-compose.dev.yml). Production target: 3-node etcd cluster, 3-node NATS cluster with JetStream, Harbor container registry, MinIO for snapshots, CoreDNS per node.

---

## 3. Implementation Status

### 3.1 Implemented (Phase 1–4)

| Component | Package | Status |
|-----------|---------|--------|
| Manifest types, parsing, validation, topo sort | `pkg/manifest` | ✅ |
| etcd store (state, registry, DNS, ledger, nodes, catalog) | `pkg/store` | ✅ |
| Orchestrator state machine + reconciliation loop | `pkg/orchestrator` | ✅ |
| Scheduler — filter / score / bind / verify | `pkg/scheduler` | ✅ |
| Docker adapter — full container lifecycle | `pkg/adapters/docker` | ✅ |
| Compose adapter — generate YAML + repo build | `pkg/adapters/compose` | ✅ |
| Kubernetes adapter — typed K8s resources, kubectl | `pkg/adapters/kubernetes` | ✅ |
| Terraform adapter — HCL generation, AWS provider | `pkg/adapters/terraform` | ✅ |
| COL Git importer — clone, detect, parse | `pkg/importer` | ✅ |
| COL AI import mode — Claude manifest generation | `pkg/importer/ai.go` | ✅ |
| NATS JetStream telemetry bus | `pkg/telemetry` | ✅ |
| Node agent — heartbeat, real CPU/memory metrics | `cmd/sm-agent` | ✅ |
| Control plane server — adapter registration | `cmd/sm-controller` | ✅ |
| REST API server — full CRUD over HTTP/JSON | `pkg/api`, `cmd/sm-api` | ✅ |
| IaC 7-stage pipeline with OPA policy gate | `pkg/pipeline` | ✅ |
| CoreDNS integration — etcd sync, watch loop | `pkg/mesh/dns` | ✅ |
| Service registry — weighted random LB, /resolve | `pkg/mesh/registry` | ✅ |
| Smart proxy — weight-based + header canary splits | `pkg/mesh/proxy` | ✅ |
| mTLS sidecar — ECDSA CA, SPIFFE SANs, TLS 1.3 | `pkg/mesh/sidecar` | ✅ |
| Vault secret injection — AppRole + token + cache | `pkg/config` | ✅ |
| Blueprint size profiles + instantiation engine | `pkg/catalog` | ✅ |
| Auto-scaler — cpu/mem/rps metrics, hysteresis | `pkg/autoscaler` | ✅ |
| GitOps sync — GitHub/GitLab webhooks, poll loop | `pkg/gitops` | ✅ |
| gRPC proto definitions | `api/proto` | ✅ |
| Web dashboard (Next.js) | `web/` | ✅ |
| CI/CD pipeline | `.github/workflows/ci.yml` | ✅ |
| Dev compose stack — etcd, NATS, Loki, Tempo, MinIO, CoreDNS | `deploy/` | ✅ |
| Code-Server IDE integration | `deploy/docker-compose.dev.yml` | ✅ |
| Prometheus `/metrics` endpoint on sm-agent (`:9091`) | `cmd/sm-agent` | ✅ |
| VictoriaMetrics scrape config (all agents + controller + NATS) | `deploy/victoriametrics/` | ✅ |
| Promtail Docker SD log shipping to Loki | `deploy/promtail/` | ✅ |
| Grafana auto-provisioned datasources (VM, Loki, Tempo) | `deploy/grafana/provisioning/` | ✅ |
| Grafana dashboards — Overview + Nodes | `deploy/grafana/provisioning/dashboards/` | ✅ |

### 3.2 Remaining / TODO

| Item | Priority | Notes |
|------|----------|-------|
| Snapshot and backup engine | High | `pkg/snapshot/` — needed for stateful cloning |
| Pulumi adapter | Medium | Stub only; needs SDK integration |
| sm-proxy binary | Medium | Stub entry point; logic lives in `pkg/mesh/proxy` |
| gRPC codegen | Medium | Run `make proto` to generate Go stubs from `.proto` |
| Integration test suite | Medium | `test/integration/` — currently only unit tests |
| go.sum (full tidy) | Done | Generated; keep in sync after adding dependencies |
| Multi-node etcd/NATS | Low | Dev uses single-node; production needs 3-node HA |

---

## 4. Catalog Onboarding Layer (COL)

### 4.1 Purpose

The COL eliminates the need to write StratonMesh manifests from scratch. It provides three onboarding paths:

1. **Catalog mode** — clones the repo, parses the best-matched format into a blueprint, then discards the clone. Images must be pre-built and pushed to a registry.
2. **Repo mode** — clones the repo and keeps it on disk under `/var/lib/stratonmesh/repos/{name}/`. At deploy time the Compose adapter runs `docker compose up --build -d` from local source.
3. **AI mode** — clones the repo, sends the file tree and key file contents to Claude (Haiku 4.5), and uses the generated YAML manifest as the blueprint. Falls back to format detection on API error, quota exhaustion, or parse failure. Also keeps the repo on disk for source builds.

### 4.2 Format auto-detection priority

Detection priority (lower number wins):

| Priority | Format | Detection signal |
|----------|--------|-----------------|
| 1 | stratonmesh | `stratonmesh.yaml` / `stack.yaml` |
| 2 | docker-compose | `docker-compose.yml` / `docker-compose.yaml` |
| 3 | helm | `Chart.yaml` present |
| 4 | kubernetes | `*.yaml` with `kind:` field |
| 5 | terraform | `*.tf` files present |
| 6 | dockerfile | `Dockerfile` present |

### 4.3 AI import flow

```
Git clone
    ↓
Build repo snapshot (file tree + README + Dockerfiles + compose files, max 3k chars/file)
    ↓
POST to Claude claude-haiku-4-5 with system prompt + snapshot
    ↓
Claude returns StratonMesh YAML manifest
    ↓
Parse + validate + auto-classify workload types
    ↓              ↓ on failure
Save blueprint   Fall back to format detection
```

The system prompt instructs Claude to: identify services from directory structure and Dockerfiles, assign workload archetypes, infer health check endpoints, leave `image` empty for source-built services, and default `platform: compose` for repos with Dockerfiles.

### 4.4 Blueprint catalog

Blueprints are stored in etcd under `/stratonmesh/catalog/{name}`. Each blueprint carries:
- `name`, `version`, `source` (format), `importMode` (`catalog` / `repo` / `ai`)
- `localPath` — on-disk repo location for repo/ai modes
- `gitUrl`, `gitBranch`, `gitSHA`, `gitPath`
- `manifest` — the full parsed `manifest.Stack` (JSON-encoded)
- `parameters`, `classifications`, `volumes` metadata
- `createdAt` timestamp

### 4.5 Instantiation engine

`catalog.Engine.Instantiate()` deserialises the stored manifest, applies `{{param}}` and `${param}` substitutions from the request's `parameters` map, merges the selected size profile (XS/S/M/L/XL), and returns a fully resolved `manifest.Stack` that enters the standard IaC pipeline. For repo/ai blueprints, `doInstantiate` in the API server explicitly re-stamps `platform: compose` and `metadata.repoPath` onto the stack before writing to etcd, ensuring the compose adapter is selected at deploy time regardless of serialisation round-trips.

---

## 5. IaC Pipeline and GitOps

### 5.1 Seven-stage pipeline (`pkg/pipeline`)

| Stage | What happens |
|-------|-------------|
| 1 — Parse & validate | Schema validation, lint, cycle detection, workload classification |
| 2 — Resolve environment | Deep merge: base manifest → shared vars → env overlay → service overrides |
| 3 — Interpolate variables | `${var}` substitution; `vault:` refs left opaque for runtime resolution |
| 4 — Diff & plan | Compare desired vs current etcd state; classify as create/update/destroy/unchanged |
| 5 — Policy gate | OPA Rego evaluation; built-in rules for blast radius, resource limits, replica caps; `AddPolicy()` for custom rules; graceful fallback on OPA error |
| 6 — Apply to intent store | Atomic etcd write + ledger entry |
| 7 — Reconcile | Orchestrator watches etcd, converges actual state |

### 5.2 GitOps sync (`pkg/gitops`)

Two trigger paths:
- **Push path** — GitHub (HMAC-SHA256) and GitLab (token) webhook receivers; fires immediately on push
- **Poll path** — configurable interval (default 60s); compares branch HEAD against last-applied SHA

---

## 6. Control Plane

### 6.1 Orchestrator state machine (`pkg/orchestrator`)

```
pending → scheduling → provisioning → deploying → verifying → running
                                          ↓               ↓
                                       failed          failed
                                       ↓ (retry ≤3)
                                       pending (retry)
                                       ↓ (rollbackOnFailure)
                                       rolling-back
                                       ↓ (give up)
                                       failed (parked)
```

The reconciliation loop runs every **30 seconds** for all active stacks AND immediately on etcd watch triggers when desired state changes. For stacks in etcd that are not yet in memory (e.g. written by CLI before controller started), `reconcileAll` calls `Deploy()` to bootstrap them.

### 6.2 Scheduler (`pkg/scheduler`)

Four-phase pipeline:

| Phase | Action |
|-------|--------|
| Filter | Hard constraints: resource fit, provider availability, node selector, taints |
| Score | Weighted soft preferences: bin-packing 35%, spread 25%, affinity 20%, cost 10%, locality 10% |
| Bind | Reserve resources on selected node via etcd CAS (optimistic concurrency) |
| Verify | Pre-flight check that node agent is responsive |

Multi-replica placement is iterative: after placing replica N, scores recalculate for replica N+1 (spread scorer penalises the node that just received a replica).

### 6.3 Config manager (`pkg/config`)

Vault AppRole and token auth. `vault:path#field` env references resolved at deploy time and injected directly — never stored in etcd or git. TTL-based cache with automatic token renewal loop.

### 6.4 Auto-scaler (`pkg/autoscaler`)

Watches cpu/memory/requestRate metrics per service. Configurable cooldown period. 3-tick scale-down hysteresis (must be below threshold for 3 consecutive ticks before scaling down). Scale decisions write updated replica counts to etcd; orchestrator executes with full health checking and rollback guarantees.

---

## 7. Platform Adapter Layer

### 7.1 Platform Adapter Interface

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

### 7.2 Adapter capabilities

| Adapter | Status | Key behaviour |
|---------|--------|--------------|
| Docker | ✅ | Docker Engine SDK; containers on shared network; sequential replica names (`svc-0`, `svc-1`) |
| Compose | ✅ | Generates `docker-compose.yml`; repo/ai mode runs `docker compose up --build -d` from local clone |
| Kubernetes | ✅ | Generates typed K8s resources per workload archetype; `kubectl apply` |
| Terraform | ✅ | Generates HCL targeting AWS; stateful services → managed RDS/ElastiCache |
| Pulumi | 🔲 | Stub — TypeScript/Python programs via Pulumi SDK |
| Process / systemd | ✅ | systemd units for bare-metal without Docker |
| OpenShift | 🔲 | Red Hat K8s; adds Routes, DeploymentConfigs, ImageStreams, SCCs, OAuthClient |
| Apache Mesos / Marathon | 🔲 | Marathon REST API; maps archetypes to Marathon app definitions; ZooKeeper state |
| Docker Swarm | 🔲 | Swarm services with placement constraints; `docker stack deploy` |
| HashiCorp Nomad | 🔲 | HCL job specs; supports Docker, exec, Java, WASM task drivers |
| Fly.io | 🔲 | `flyctl` CLI wrapper; machines API; regions as placement zones |
| AWS ECS / Fargate | 🔲 | Direct ECS task definitions (not via Terraform); Fargate serverless tasks |
| Azure Container Apps | 🔲 | Managed K8s-based platform; KEDA-driven scaling; Dapr sidecar support |
| Google Cloud Run | 🔲 | Serverless containers; min/max instance scaling; VPC connector for mesh |
| Podman / Podman Compose | 🔲 | Rootless containers; drop-in Docker adapter variant using Podman socket |
| LXC / LXD | 🔲 | System containers; useful for VM-like isolation without full virtualisation |

### 7.3 Workload archetype → platform resource mapping

| Archetype | Docker | Compose | Kubernetes | OpenShift | Mesos/Marathon | Nomad |
|-----------|--------|---------|------------|-----------|----------------|-------|
| long-running | Container | service | Deployment | DeploymentConfig | Marathon app | service job |
| stateful | Container + volume | service + volume | StatefulSet | StatefulSet | persistent app | stateful job |
| batch | run --rm | run one-off | Job | Job | batch job | batch job |
| scheduled | cron | cron (ext) | CronJob | CronJob | cron app | periodic job |
| daemon | 1 per host | global mode | DaemonSet | DaemonSet | constraints:unique | system job |
| composite | Multi-container | Full stack | Helm chart | Template | group | task group |

---

## 8. Hybrid Service Mesh

### 8.1 Layered design

NATS is dedicated to async telemetry only — not service discovery. Synchronous RPC uses DNS + service registry, requiring zero SDK changes in application code.

| Layer | Component | Always on? |
|-------|-----------|-----------|
| 1 — DNS | CoreDNS; `{svc}.{stack}.{env}.mesh.local` | Yes |
| 2 — Registry | etcd-backed; version, weight, health metadata | Yes |
| 3 — Proxy | Smart proxy; canary splits; TLS termination | Yes |
| 4 — Sidecar | mTLS; circuit breaking; retries (opt-in per stack) | No |

### 8.2 Smart proxy canary routing

Weight-based and header-forced (`X-Canary: true`) traffic splits. Background health checks remove unhealthy upstreams within one TTL window (5 seconds).

### 8.3 mTLS sidecar

Self-signed ECDSA CA per cluster. SPIFFE URI SANs (`spiffe://stratonmesh/{stack}/{service}`). TLS 1.3 minimum. Certificate lifecycle managed by sidecar — services never handle their own certificates.

---

## 9. Observability

### 9.1 Telemetry bus (NATS JetStream)

Three durable streams: `METRICS` (4h retention), `LOGS` (24h), `TRACES` (1h with head-based sampling). All published by `pkg/telemetry` bus. Node agents publish real CPU/memory samples every 15 seconds. NATS is used for **internal** event fan-out (auto-scaler, GitOps, pipeline events) — it is **not** the metrics source for VictoriaMetrics.

### 9.2 Metrics pipeline

```
sm-agent (gopsutil)
    │
    ├── NATS JetStream (telemetry.metrics.*)   ← internal: auto-scaler feedback
    │
    └── HTTP GET /metrics :9091                ← Prometheus exposition format
              │
              └── VictoriaMetrics scrape        ← promscrape.config prometheus.yml
                        │
                        └── Grafana dashboards  ← datasource: VictoriaMetrics
```

`sm-agent` exposes a Prometheus `/metrics` endpoint on `:9091` using `prometheus/client_golang`. VictoriaMetrics is configured with `--promscrape.config` pointing at `deploy/victoriametrics/prometheus.yml`, which scrapes all three agent instances, the controller, and NATS.

**Exposed metrics:**

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `stratonmesh_node_cpu_percent` | Gauge | `node_id`, `node_name` | Host CPU usage % |
| `stratonmesh_node_memory_percent` | Gauge | `node_id`, `node_name` | Host memory usage % |
| `stratonmesh_node_info` | Gauge (=1) | `node_id`, `node_name`, `os`, `region` | Node registration info |
| `stratonmesh_stack_state` | Gauge | `stack_name`, `state` | Stack lifecycle state |
| `stratonmesh_pipeline_runs_total` | Counter | — | Total pipeline executions |
| `stratonmesh_pipeline_stage_duration_ms` | Histogram | `stage` | Per-stage latency |
| `stratonmesh_autoscaler_scale_up_total` | Counter | `service` | Scale-up events |
| `stratonmesh_autoscaler_scale_down_total` | Counter | `service` | Scale-down events |
| `stratonmesh_catalog_blueprints_total` | Gauge | — | Blueprints in catalog |

### 9.3 Log pipeline

```
Docker container stdout/stderr
    │
    └── Promtail (Docker SD via /var/run/docker.sock)
              │  relabel: service, compose_service, level, caller
              │  pipeline: parse zap JSON, drop debug
              │
              └── Loki push API :3100
                        │
                        └── Grafana log panel  ← datasource: Loki
```

Promtail uses Docker service discovery (`docker_sd_configs`) to automatically tail all running containers. It parses structured zap JSON logs and promotes `level` and `caller` as indexed Loki labels. Debug-level logs are dropped at the pipeline stage to reduce storage.

**Useful Loki label selectors:**

| Selector | Returns |
|----------|---------|
| `{compose_service="sm-controller"}` | Controller logs |
| `{compose_service=~"sm-agent.*"}` | All agent logs |
| `{level="error"}` | Errors across all services |
| `{compose_service="sm-controller", level="warn"}` | Controller warnings |

### 9.4 Trace pipeline

Grafana Tempo receives OTLP traces on `:4317` (gRPC) and `:4318` (HTTP). Tempo is linked to Loki in the Grafana datasource config (`tracesToLogsV2`) so trace spans can jump directly to correlated log lines by trace ID.

### 9.5 Storage backends

| System | Port | Role |
|--------|------|------|
| VictoriaMetrics | 8428 | Time-series metrics storage; Prometheus-compatible query API |
| Grafana Loki | 3100 | Structured log aggregation; LogQL query language |
| Grafana Tempo | 3200 | Distributed trace storage; OTLP ingest on 4317/4318 |
| Grafana | 4000 | Unified dashboards, alerting, data source management |
| Promtail | — | Log shipper; Docker SD → Loki push (no exposed port) |

### 9.6 Grafana provisioning

All Grafana configuration is provisioned automatically from `deploy/grafana/provisioning/` — no manual setup required after `docker compose up`:

```
deploy/grafana/provisioning/
├── datasources/
│   └── datasources.yaml       # VictoriaMetrics (default), Loki, Tempo — wired together
└── dashboards/
    ├── dashboards.yaml        # Provider: load from this directory, hot-reload every 30s
    ├── stratonmesh-overview.json   # Stack health, node CPU/mem, pipeline latency, log tail
    └── stratonmesh-nodes.json      # Per-node gauges, trends, heartbeat table; node variable
```

Grafana is configured with `GF_SERVER_HTTP_PORT=4000` (default 3000 would conflict with Next.js dashboard). Anonymous viewer access is enabled for the dev stack.

### 9.7 Node metrics

The `sm-agent` binary collects real host metrics via `gopsutil/v3`: CPU utilisation sampled over 200ms, available memory from `VirtualMemory().Available`. Two parallel publish paths run every 15 seconds:
1. **NATS** — `telemetry.metrics.{nodeID}.` subjects consumed by the auto-scaler
2. **Prometheus gauge** — updated in-process, scraped by VictoriaMetrics on `/metrics`

---

## 10. Infrastructure and Deployment

### 10.1 Binaries

| Binary | Role |
|--------|------|
| `sm-controller` | Orchestrator + scheduler + API server + auto-scaler |
| `sm-agent` | Node heartbeat, real CPU/memory metrics, provider detection |
| `sm-api` | Standalone REST API (also embedded in controller) |
| `sm-proxy` | Smart proxy entry point (stub; logic in `pkg/mesh/proxy`) |
| `stratonmesh` | CLI — deploy, scale, destroy, status, rollback, catalog |

### 10.2 Development stack (`deploy/docker-compose.dev.yml`)

Services: etcd, NATS, Vault, VictoriaMetrics, Grafana, Loki, **Promtail**, Tempo, MinIO, CoreDNS, Harbor (registry), Code-Server IDE, sm-controller, sm-agent (× 3 nodes), Next.js dashboard.

Config files auto-mounted at startup:

| File | Mounted into | Purpose |
|------|-------------|---------|
| `deploy/victoriametrics/prometheus.yml` | VictoriaMetrics | Scrape targets for all sm-agent instances + controller + NATS |
| `deploy/promtail/config.yml` | Promtail | Docker SD log collection → Loki |
| `deploy/grafana/provisioning/datasources/` | Grafana | Auto-provision VictoriaMetrics, Loki, Tempo data sources |
| `deploy/grafana/provisioning/dashboards/` | Grafana | Auto-provision Overview and Nodes dashboards |
| `deploy/coredns/Corefile` | CoreDNS | etcd-backed DNS for service mesh |

The Dockerfile uses a multi-stage build: Go 1.25 Alpine builder compiles all five binaries; the runtime image is Alpine 3.19 with `docker-cli`, `docker-cli-compose`, `git`, `ca-certificates`, and `curl`. `CMD` (not `ENTRYPOINT`) allows compose `command:` to select which binary runs.

### 10.3 Key etcd paths

```
/stratonmesh/stacks/{id}/desired     desired manifest (written by pipeline)
/stratonmesh/stacks/{id}/actual      actual manifest (written by reconciler)
/stratonmesh/stacks/{id}/status      lifecycle state string
/stratonmesh/services/{svc}/{stack}/{instance}   service registry endpoints
/stratonmesh/dns/{fqdn}              DNS A records
/stratonmesh/ledger/{stack}/{ts}     version history for rollback
/stratonmesh/nodes/{id}              node info with 30s TTL lease
/stratonmesh/catalog/{name}          blueprint catalog entries
```

---

## 11. Security Model

| Concern | Mechanism |
|---------|-----------|
| Secrets | Vault AppRole/token; `vault:path#field` refs resolved at runtime; never in etcd or git |
| Service-to-service | Optional mTLS via sidecar; ECDSA certs managed by COL sidecar lifecycle |
| Policy enforcement | OPA Rego; policies version-controlled alongside manifests; custom rules via `AddPolicy()` |
| Webhook integrity | HMAC-SHA256 (GitHub), secret token (GitLab) |
| Container registry | Harbor (self-hosted); eliminates external registry dependency |

---

## 12. Web Dashboard

### 12.1 Pages implemented

| Page | Path | Features |
|------|------|---------|
| Overview | `/` | Stack count, healthy nodes, recent activity |
| Stacks | `/stacks` | List with status badges, deploy button |
| Stack detail | `/stacks/{id}` | Services, replica bars, deployment history, rollback, destroy, manifest editor |
| Catalog | `/catalog` | Blueprint cards, import modal (Catalog / Repo / AI modes), deploy modal with platform + file picker, size profiles, open in Code-Server |
| Nodes | `/nodes` | Per-node CPU/memory bars, provider badges, last-seen |

### 12.2 API surface (Next.js route handlers)

All dashboard API calls proxy to `http://sm-controller:8080` (internal Docker network). Environment variable `SM_API_URL` controls the upstream.

---

## 13. Next Steps

### Priority 1 — Core stability

1. **Snapshot and backup engine** (`pkg/snapshot/`)
   Stateful volume snapshots stored in MinIO. Enables: stack cloning, disaster recovery, preview environment branching. Required for production readiness of stateful workloads.

2. **gRPC codegen**
   Run `make proto` to generate Go stubs from `api/proto/stratonmesh.proto`. Wire StackService, CatalogService, and NodeService handlers. Enables SDK consumers and CLI to use gRPC instead of REST.

3. **Integration test suite** (`test/integration/`)
   Tests that spin up etcd + NATS in CI and exercise the full pipeline → orchestrator → adapter chain. Currently only unit tests exist.

### Priority 2 — New Platform Adapters

4. **OpenShift adapter** (`pkg/adapters/openshift/`)
   Red Hat OpenShift extends Kubernetes with Routes (ingress), DeploymentConfigs, ImageStreams, BuildConfigs, and Security Context Constraints (SCCs). The adapter generates OpenShift-native YAML, creates Routes instead of Ingresses, handles the stricter SCC defaults (no root containers), and authenticates via `oc login` token or service account kubeconfig.

5. **Apache Mesos / Marathon adapter** (`pkg/adapters/mesos/`)
   Translates StratonMesh archetypes to Marathon application definitions (JSON). Long-running services become persistent Marathon apps with health check URLs. Batch jobs become one-off tasks. Stateful services use Marathon storage persistent volumes. Placement uses constraint expressions (`[["hostname", "UNIQUE"]]` for daemons). State discovery via the Marathon REST API; ZooKeeper used for leader election detection.

6. **Docker Swarm adapter** (`pkg/adapters/swarm/`)
   Generates a `docker-stack.yml` and applies it via `docker stack deploy`. Swarm services with `replicas:`, placement constraints, and named overlay networks. Secrets managed via `docker secret`. Useful for existing Swarm clusters before migration to Compose or K8s.

7. **HashiCorp Nomad adapter** (`pkg/adapters/nomad/`)
   Generates HCL job specifications. Nomad's multi-driver support (Docker, exec, Java, WASM) maps cleanly to StratonMesh archetypes. Service jobs → long-running; batch jobs → batch/scheduled; system jobs → daemon. Nomad's built-in service discovery integrates with the COL service registry via Consul.

8. **Pulumi adapter** (`pkg/adapters/pulumi/`)
   Generate TypeScript or Python programs using Pulumi SDKs. Implement the full `PlatformAdapter` interface. Useful for cloud-native stacks targeting AWS/GCP/Azure with programmatic conditionals and loops.

9. **Cloud-native serverless adapters**
   - **AWS ECS/Fargate** — direct ECS task definition generation (not via Terraform); faster iteration than Terraform for container-only workloads
   - **Azure Container Apps** — KEDA-driven scaling rules, Dapr sidecar annotations, managed certificates
   - **Google Cloud Run** — min/max instance bounds, VPC connector for private mesh, IAM invoker bindings

### Priority 3 — Capabilities

10. **sm-proxy binary** (`cmd/sm-proxy/`)
    Wire `pkg/mesh/proxy` into a standalone process. Enables running the smart proxy independently of the controller (e.g. on edge nodes or as a sidecar injection target).

11. **Live discovery import**
    Connect to a running Docker host or Kubernetes cluster via Docker socket / kubeconfig, enumerate running containers/pods, reverse-engineer service topology from network connections and environment variables, and generate a blueprint. Enables brownfield migration — bring unmanaged applications under StratonMesh with zero rewriting.

12. **Deploy file selector per instantiation**
    Allow users to pick a specific compose/terraform/k8s file from within a cloned repo at instantiate time (UI + API). `DeployFile` field stored in `manifest.Metadata`; compose adapter uses it instead of auto-detection.

13. **Environment promotion pipeline**
    First-class `dev → staging → production` promotion: copy desired state from one environment to another with a single command, gate on policy approval, preserve secrets references. Dashboard shows promotion status across environments per stack.

14. **Blue-green and canary deployment UI**
    Visual wizard in the dashboard to configure traffic split percentages, header-forced canary routing, automated rollback triggers (error rate threshold), and step-up schedules. Currently the proxy supports the mechanics; the UX is missing.

15. **Multi-tenancy and RBAC**
    Namespace isolation per team/project. Role-based access control: admin, deployer, viewer roles per namespace. etcd prefix isolation (`/stratonmesh/{tenant}/...`). API token or OIDC/SSO authentication. Audit log per tenant.

16. **Real-time log streaming and terminal**
    Dashboard: live log tail per service (SSE from sm-api → Docker logs / kubectl logs). In-browser terminal (`xterm.js` + WebSocket → `docker exec` / `kubectl exec`). Required for debugging deployed services without leaving the UI.

### Priority 4 — Scale and Operations

17. **Multi-node etcd and NATS**
    Production HA setup: 3-node etcd cluster, 3-node NATS with JetStream replication (R=3). Add cluster bootstrap scripts and health checks to `deploy/`. Etcd learner nodes for read-scale.

18. **Drift detection and auto-revert**
    Reconciler currently converges on desired state but does not detect drift (actual state diverging without a desired state change change). Add: compare actual vs desired on each tick; emit `drift.detected` NATS event; configurable policy per environment (`warn` for dev, `revert` for production).

19. **Horizontal orchestrator scaling**
    Multiple controller instances with etcd-based leader election. Each instance owns a hash-partitioned slice of the stack namespace. Required when managing 1,000+ concurrent stacks. Scheduler uses consistent hashing to avoid double-scheduling.

20. **Cost projection and budget enforcement**
    Per-stack cost tracking: node `cost_per_hour` × resource allocation fraction. Real-time cost display in dashboard. Pipeline stage 5 OPA rule blocks deploys that exceed per-namespace budget thresholds. Monthly cost reports via email/Slack.

21. **Snapshot and backup engine** (`pkg/snapshot/`)
    Stateful volume snapshots stored in MinIO via S3 API. Scheduled backups per service (cron expression in manifest). Point-in-time restore. Stack cloning (duplicate a running stack to a new name). Cross-region replication for disaster recovery.

22. **Multi-cluster federation**
    Deploy a single stack across multiple clusters/regions. Global scheduler selects the target cluster per service based on latency, cost, and compliance zone. Cross-cluster service mesh: services in cluster A resolve cluster B endpoints via DNS. Federated state in a global etcd tier.

23. **GPU workload scheduling**
    Node agent reports GPU availability (NVIDIA/AMD) via nvml. Scheduler filter phase: reject nodes without requested GPU type. Score phase: prefer nodes with fractional GPU availability. Manifest field: `resources.gpu: "1"`. Maps to Docker `--gpus`, K8s `nvidia.com/gpu` resource limits, and Nomad device fingerprinter.

24. **Windows and ARM64 node support**
    sm-agent cross-compiles to `windows/amd64` and `linux/arm64`. Windows nodes use Windows containers or WSL2-backed Docker. ARM64 nodes use native Docker (Raspberry Pi, AWS Graviton, Apple M-series in Linux VMs). Multi-arch image builds via `docker buildx` in the Compose adapter.

25. **SSO / OAuth2 / OIDC dashboard authentication**
    Replace the current open dashboard with configurable OIDC provider (Keycloak, Google, GitHub, Okta). Token issued at login, passed as `Authorization: Bearer` to the API. Roles mapped from OIDC claims. Required for any multi-user or production deployment.

26. **Compliance and policy profiles**
    Pre-built OPA policy bundles for common compliance frameworks: SOC 2 (audit logging, encryption at rest), HIPAA (no PHI in env vars, volume encryption), PCI-DSS (network isolation, no public ports). Selectable per namespace. Dashboard shows compliance score per stack.

27. **Webhook and notification integrations**
    NATS event → webhook fan-out for: deploy success/failure, drift detected, auto-scale events, cost threshold breach, policy denial. Built-in targets: Slack, Microsoft Teams, PagerDuty, generic HTTP. Configurable per namespace in dashboard.

### Priority 5 — AI and Intelligence

28. **AI-assisted manifest refinement**
    After AI import, offer an interactive chat panel in the dashboard: users type "add a Redis cache service" or "set app to 3 replicas with 512Mi memory" and Claude rewrites the manifest. Changes are diffed and previewed before applying.

29. **Anomaly detection and predictive scaling**
    Claude analyses rolling metric windows from VictoriaMetrics: flag unusual CPU/memory patterns, predict traffic spikes from historical cycles, suggest adjusted auto-scaler thresholds. Recommendations surfaced as actionable cards in the dashboard with one-click apply.

30. **Natural language deploy**
    CLI: `stratonmesh deploy --from "deploy nextcloud with PostgreSQL and S3-compatible storage, sized for 500 users"` — Claude selects or generates the blueprint and parameters, shows a plan for approval, then deploys. Enables non-expert users to deploy complex stacks.

31. **AI ops assistant**
    Conversational interface in the dashboard for operational queries: "Why is the voting-app in failed state?", "Which service is consuming the most memory?", "Show me the deployment history for nextcloud". Claude is given read access to etcd state, NATS events, and metric aggregates to answer in natural language.

32. **Smart blueprint marketplace**
    Community-contributed blueprints with AI-generated tags, descriptions, and compatibility scores. Claude reviews incoming blueprints for security issues (hardcoded secrets, privileged containers, exposed admin ports) before publication. Semantic search: "find a blueprint similar to my docker-compose" using embedding similarity.

---

## 14. Glossary

**COL:** Catalog Onboarding Layer — the import, catalog, and instantiation subsystem.
**Blueprint:** A versioned, parameterized template in the catalog that defines a complete stack.
**PAI / PlatformAdapter:** The interface every deployment target implements (`Generate`, `Apply`, `Status`, `Destroy`, `Diff`, `Rollback`).
**Intent store:** The etcd-backed storage for desired and actual state per stack.
**Reconciler:** The loop that continuously converges actual state toward desired state (30s periodic + etcd watch trigger).
**Workload archetype:** One of six service types (long-running, stateful, batch, scheduled, daemon, composite) that determine lifecycle rules and platform resource mapping.
**Size profile:** A pre-computed resource allocation tier (XS/S/M/L/XL) mapping user count to concrete CPU, memory, storage, and replica numbers.
**AI import:** Import mode where Claude analyzes a Git repository and generates a StratonMesh manifest; falls back to format detection on failure.
**Repo mode:** Import mode that keeps the cloned repository on disk so the Compose adapter can build images from source at deploy time.
**OpenShift:** Red Hat's enterprise Kubernetes distribution; adds Routes, BuildConfigs, ImageStreams, SCCs, and OAuth on top of standard K8s.
**Marathon:** Apache Mesos's long-running service framework; accepts JSON app definitions via REST API; ZooKeeper for leader election.
**Nomad:** HashiCorp's workload orchestrator; multi-driver (Docker, exec, Java, WASM); HCL job specs; integrates with Consul for service discovery.
**Drift:** Divergence between desired state (etcd) and actual state (running platform) without a corresponding desired-state change — typically caused by manual intervention or platform-side failures.
**Tenant:** An isolated namespace within StratonMesh with its own etcd prefix, RBAC roles, budget, and policy set.
