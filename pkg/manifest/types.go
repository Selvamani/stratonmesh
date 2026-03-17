package manifest

import "time"

// Stack is the top-level manifest representing an entire deployable unit.
type Stack struct {
	Name        string            `yaml:"name" json:"name"`
	Version     string            `yaml:"version" json:"version"`
	Environment string            `yaml:"environment,omitempty" json:"environment,omitempty"`
	Platform    string            `yaml:"platform,omitempty" json:"platform,omitempty"` // docker, compose, kubernetes, terraform, pulumi
	Strategy    DeployStrategy    `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	Services    []Service         `yaml:"services" json:"services"`
	Variables   map[string]string `yaml:"variables,omitempty" json:"variables,omitempty"`
	Metadata    Metadata          `yaml:"metadata,omitempty" json:"metadata,omitempty"`
}

// WorkloadType classifies a service's lifecycle behavior.
type WorkloadType string

const (
	WorkloadLongRunning WorkloadType = "long-running"
	WorkloadStateful    WorkloadType = "stateful"
	WorkloadBatch       WorkloadType = "batch"
	WorkloadScheduled   WorkloadType = "scheduled"
	WorkloadDaemon      WorkloadType = "daemon"
)

// Service represents a single deployable unit within a stack.
type Service struct {
	Name         string            `yaml:"name" json:"name"`
	Type         WorkloadType      `yaml:"type,omitempty" json:"type,omitempty"`
	Image        string            `yaml:"image" json:"image"`
	Command      string            `yaml:"command,omitempty" json:"command,omitempty"`
	Replicas     int               `yaml:"replicas,omitempty" json:"replicas,omitempty"`
	Ports        []PortSpec        `yaml:"ports,omitempty" json:"ports,omitempty"`
	Env          map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Volumes      []VolumeSpec      `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Resources    ResourceSpec      `yaml:"resources,omitempty" json:"resources,omitempty"`
	HealthCheck  HealthCheckSpec   `yaml:"healthCheck,omitempty" json:"healthCheck,omitempty"`
	Scaling      ScalingSpec       `yaml:"scaling,omitempty" json:"scaling,omitempty"`
	Affinity     AffinitySpec      `yaml:"affinity,omitempty" json:"affinity,omitempty"`
	DependsOn    []string          `yaml:"dependsOn,omitempty" json:"dependsOn,omitempty"`
	Lifecycle    LifecycleSpec     `yaml:"lifecycle,omitempty" json:"lifecycle,omitempty"`
	Identity     string            `yaml:"identity,omitempty" json:"identity,omitempty"` // "ordinal" for stateful
	Schedule     string            `yaml:"schedule,omitempty" json:"schedule,omitempty"` // cron expression
	Timeout      string            `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Retries      int               `yaml:"retries,omitempty" json:"retries,omitempty"`
	Enabled      *bool             `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	NodeSelector map[string]string `yaml:"nodeSelector,omitempty" json:"nodeSelector,omitempty"`
	Backup       BackupSpec        `yaml:"backup,omitempty" json:"backup,omitempty"`
	Runtime      string            `yaml:"runtime,omitempty" json:"runtime,omitempty"` // docker, process, vm, wasm
	Mesh         MeshSpec          `yaml:"mesh,omitempty" json:"mesh,omitempty"`
}

// PortSpec defines a network port exposure.
type PortSpec struct {
	Expose   int    `yaml:"expose" json:"expose"`
	Protocol string `yaml:"protocol,omitempty" json:"protocol,omitempty"` // tcp, udp
	Host     int    `yaml:"host,omitempty" json:"host,omitempty"`         // explicit host port (0 = auto)
}

// VolumeSpec defines a persistent volume.
type VolumeSpec struct {
	Name     string `yaml:"name" json:"name"`
	Size     string `yaml:"size" json:"size"`
	MountAt  string `yaml:"mountAt,omitempty" json:"mountAt,omitempty"`
	BindTo   string `yaml:"bindTo,omitempty" json:"bindTo,omitempty"`     // "instance" for stateful
	Snapshot bool   `yaml:"snapshot,omitempty" json:"snapshot,omitempty"` // include in stack snapshots
	Shared   bool   `yaml:"shared,omitempty" json:"shared,omitempty"`     // NFS/shared storage
}

// ResourceSpec defines CPU, memory, and GPU requests.
type ResourceSpec struct {
	CPU    string `yaml:"cpu,omitempty" json:"cpu,omitempty"`       // e.g., "500m" or "500m-2000m"
	Memory string `yaml:"memory,omitempty" json:"memory,omitempty"` // e.g., "256Mi" or "256Mi-1Gi"
	GPU    int    `yaml:"gpu,omitempty" json:"gpu,omitempty"`
}

// HealthCheckSpec defines liveness, readiness, and startup probes.
type HealthCheckSpec struct {
	Liveness  *Probe `yaml:"liveness,omitempty" json:"liveness,omitempty"`
	Readiness *Probe `yaml:"readiness,omitempty" json:"readiness,omitempty"`
	Startup   *Probe `yaml:"startup,omitempty" json:"startup,omitempty"`
	Custom    *Probe `yaml:"custom,omitempty" json:"custom,omitempty"` // exec-based for stateful
}

// Probe is a single health check configuration.
type Probe struct {
	HTTP             string `yaml:"http,omitempty" json:"http,omitempty"`
	TCP              int    `yaml:"tcp,omitempty" json:"tcp,omitempty"`
	Exec             string `yaml:"exec,omitempty" json:"exec,omitempty"`
	Interval         string `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout          string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	FailureThreshold int    `yaml:"failureThreshold,omitempty" json:"failureThreshold,omitempty"`
}

// ScalingSpec defines auto-scaling behavior.
type ScalingSpec struct {
	Auto        bool           `yaml:"auto,omitempty" json:"auto,omitempty"`
	MinReplicas int            `yaml:"minReplicas,omitempty" json:"minReplicas,omitempty"`
	MaxReplicas int            `yaml:"maxReplicas,omitempty" json:"maxReplicas,omitempty"`
	Metrics     []ScaleMetric  `yaml:"metrics,omitempty" json:"metrics,omitempty"`
}

// ScaleMetric defines a metric target for auto-scaling.
type ScaleMetric struct {
	Type   string `yaml:"type" json:"type"`     // cpu, memory, requestRate, custom
	Target string `yaml:"target" json:"target"` // "70%" or "1000/s"
}

// AffinitySpec defines placement preferences.
type AffinitySpec struct {
	Spread   string   `yaml:"spread,omitempty" json:"spread,omitempty"`     // "node", "zone"
	Colocate []string `yaml:"colocate,omitempty" json:"colocate,omitempty"` // service names
}

// LifecycleSpec defines hooks for stateful services.
type LifecycleSpec struct {
	PostStart string `yaml:"postStart,omitempty" json:"postStart,omitempty"`
	PreStop   string `yaml:"preStop,omitempty" json:"preStop,omitempty"`
}

// BackupSpec defines backup schedule for stateful services.
type BackupSpec struct {
	Schedule  string `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	Retention string `yaml:"retention,omitempty" json:"retention,omitempty"`
}

// MeshSpec configures service mesh behavior.
type MeshSpec struct {
	Sidecar bool `yaml:"sidecar,omitempty" json:"sidecar,omitempty"` // inject sidecar proxy
}

// DeployStrategy defines how updates are rolled out.
type DeployStrategy struct {
	Type               string `yaml:"type,omitempty" json:"type,omitempty"` // rolling, blue-green, canary
	MaxUnavailable     int    `yaml:"maxUnavailable,omitempty" json:"maxUnavailable,omitempty"`
	HealthCheckTimeout string `yaml:"healthCheckTimeout,omitempty" json:"healthCheckTimeout,omitempty"`
	RollbackOnFailure  bool   `yaml:"rollbackOnFailure,omitempty" json:"rollbackOnFailure,omitempty"`
}

// Metadata carries pipeline and provenance information.
type Metadata struct {
	GitSHA     string    `yaml:"gitSha,omitempty" json:"gitSha,omitempty"`
	PipelineID string    `yaml:"pipelineId,omitempty" json:"pipelineId,omitempty"`
	ResolvedAt time.Time `yaml:"resolvedAt,omitempty" json:"resolvedAt,omitempty"`
	DeployedBy string    `yaml:"deployedBy,omitempty" json:"deployedBy,omitempty"`
	RepoPath   string    `yaml:"repoPath,omitempty" json:"repoPath,omitempty"`
	// DeployFile is a path relative to RepoPath pointing to a specific deploy file
	// (docker-compose.yml, main.tf, Chart.yaml, etc.). Empty = auto-detect.
	DeployFile string `yaml:"deployFile,omitempty" json:"deployFile,omitempty"`
}

// IsEnabled checks if a service is enabled (defaults to true).
func (s *Service) IsEnabled() bool {
	if s.Enabled == nil {
		return true
	}
	return *s.Enabled
}

// DefaultReplicas returns replicas with a default of 1.
func (s *Service) DefaultReplicas() int {
	if s.Replicas <= 0 {
		return 1
	}
	return s.Replicas
}

// InferType auto-classifies the workload type from service properties.
func (s *Service) InferType() WorkloadType {
	if s.Type != "" {
		return s.Type
	}
	if s.Schedule != "" {
		return WorkloadScheduled
	}
	if s.Identity == "ordinal" || len(s.Volumes) > 0 {
		return WorkloadStateful
	}
	if s.Timeout != "" && s.Replicas == 0 {
		return WorkloadBatch
	}
	if s.NodeSelector != nil && s.Replicas == 0 {
		return WorkloadDaemon
	}
	return WorkloadLongRunning
}
