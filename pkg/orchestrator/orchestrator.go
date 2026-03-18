package orchestrator

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/selvamani/stratonmesh/pkg/manifest"
	"github.com/selvamani/stratonmesh/pkg/store"
	"go.uber.org/zap"
)

// State represents the lifecycle state of a stack.
type State string

const (
	StatePending      State = "pending"
	StateScheduling   State = "scheduling"
	StateProvisioning State = "provisioning"
	StateDeploying    State = "deploying"
	StateVerifying    State = "verifying"
	StateRunning      State = "running"
	StateStopping     State = "stopping"
	StateStarting     State = "starting"
	StateDraining     State = "draining"
	StateStopped      State = "stopped"
	StateDown         State = "down" // containers removed, volumes kept, stack entry kept
	StateFailed       State = "failed"
	StateRollingBack  State = "rolling_back"
)

// PlatformAdapter is the interface every deployment target must implement.
type PlatformAdapter interface {
	Name() string
	Generate(ctx context.Context, stack *manifest.Stack) ([]byte, error) // produce platform artifacts
	Apply(ctx context.Context, stack *manifest.Stack) error              // execute deployment
	Stop(ctx context.Context, stackID string) error                      // pause (containers stopped, volumes intact)
	Start(ctx context.Context, stackID string) error                     // resume stopped stack
	Restart(ctx context.Context, stackID string) error                   // stop + start without losing data
	Down(ctx context.Context, stackID string) error                      // remove containers, keep volumes + stack entry
	Status(ctx context.Context, stackID string) (*AdapterStatus, error)  // query running state
	Inspect(ctx context.Context, stackID, service string) (*ServiceDetail, error)               // per-service deep info
	Logs(ctx context.Context, stackID, service string, tail int) (string, error)               // recent log lines (snapshot)
	LogStream(ctx context.Context, stackID, service string) (io.ReadCloser, error)             // live-follow stream
	Destroy(ctx context.Context, stackID string) error                   // tear down + delete volumes
	Diff(ctx context.Context, desired, actual *manifest.Stack) (*DiffResult, error)
	Rollback(ctx context.Context, stackID string, version string) error
}

// ServiceDetail carries adapter-specific runtime information about a single service.
type ServiceDetail struct {
	Name      string         `json:"name"`
	Image     string         `json:"image"`
	Platform  string         `json:"platform"` // adapter name
	Instances []InstanceInfo `json:"instances"`
	Ports     []PortBinding  `json:"ports"`
	Env       []string       `json:"env"`
	Mounts    []MountInfo    `json:"mounts"`
	Labels    map[string]string `json:"labels,omitempty"`
	Command   string         `json:"command,omitempty"`
	Created   string         `json:"created,omitempty"`
}

// InstanceInfo describes a single replica / container / pod.
type InstanceInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Health  string `json:"health"`
	Node    string `json:"node,omitempty"`
	Started string `json:"started,omitempty"`
}

// PortBinding maps a host port to a container port.
type PortBinding struct {
	HostIP        string `json:"hostIp,omitempty"`
	HostPort      string `json:"hostPort"`
	ContainerPort string `json:"containerPort"`
	Protocol      string `json:"protocol"`
}

// MountInfo describes a volume or bind mount.
type MountInfo struct {
	Type        string `json:"type"`        // volume, bind, tmpfs
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Mode        string `json:"mode,omitempty"`
}

// AdapterStatus reports the current state from the platform.
type AdapterStatus struct {
	Services []ServiceStatus `json:"services"`
}

// ServiceStatus reports one service's running state.
type ServiceStatus struct {
	Name     string `json:"name"`
	Replicas int    `json:"replicas"`
	Ready    int    `json:"ready"`
	Health   string `json:"health"` // healthy, unhealthy, unknown
}

// DiffResult categorizes changes between desired and actual state.
type DiffResult struct {
	Create    []string       `json:"create"`
	Update    []UpdateAction `json:"update"`
	Destroy   []string       `json:"destroy"`
	Unchanged []string       `json:"unchanged"`
}

// UpdateAction describes what changed for a service.
type UpdateAction struct {
	Service    string `json:"service"`
	ChangeType string `json:"changeType"` // image, config, replicas, resources
	From       string `json:"from"`
	To         string `json:"to"`
}

// Orchestrator manages the lifecycle of all stacks.
type Orchestrator struct {
	store    *store.Store
	adapters map[string]PlatformAdapter // keyed by platform name
	logger   *zap.SugaredLogger

	mu       sync.RWMutex
	stacks   map[string]*StackContext // active stack state machines
	stopCh   chan struct{}
}

// StackContext holds runtime state for a single stack's reconciliation.
type StackContext struct {
	ID        string
	State     State
	Manifest  *manifest.Stack
	Adapter   PlatformAdapter
	Retries   int
	LastError error
	LastCheck time.Time
}

// New creates an Orchestrator.
func New(st *store.Store, logger *zap.SugaredLogger) *Orchestrator {
	return &Orchestrator{
		store:    st,
		adapters: make(map[string]PlatformAdapter),
		logger:   logger,
		stacks:   make(map[string]*StackContext),
		stopCh:   make(chan struct{}),
	}
}

// RegisterAdapter adds a platform adapter (Docker, Compose, K8s, etc.).
func (o *Orchestrator) RegisterAdapter(adapter PlatformAdapter) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.adapters[adapter.Name()] = adapter
	o.logger.Infow("registered platform adapter", "name", adapter.Name())
}

// Deploy initiates a stack deployment through the state machine.
func (o *Orchestrator) Deploy(ctx context.Context, stack *manifest.Stack) error {
	// Validate
	if errs := manifest.Validate(stack); len(errs) > 0 {
		return fmt.Errorf("validation failed: %v", errs)
	}

	// Select adapter
	platform := stack.Platform
	if platform == "" {
		platform = "docker" // default
	}
	adapter, ok := o.adapters[platform]
	if !ok {
		return fmt.Errorf("no adapter registered for platform %q", platform)
	}

	// Topological sort services by dependency
	sorted, err := manifest.TopologicalSort(stack.Services)
	if err != nil {
		return fmt.Errorf("dependency resolution: %w", err)
	}
	stack.Services = sorted

	// Create stack context
	sc := &StackContext{
		ID:       stack.Name,
		State:    StatePending,
		Manifest: stack,
		Adapter:  adapter,
	}

	o.mu.Lock()
	o.stacks[stack.Name] = sc
	o.mu.Unlock()

	// Write desired state to etcd
	if err := o.store.SetDesired(ctx, stack.Name, stack); err != nil {
		return fmt.Errorf("store desired state: %w", err)
	}
	if err := o.store.SetStatus(ctx, stack.Name, string(StatePending)); err != nil {
		return fmt.Errorf("store status: %w", err)
	}

	// Record in version ledger
	o.store.AppendLedger(ctx, store.LedgerEntry{
		StackID:    stack.Name,
		Version:    stack.Version,
		Manifest:   stack,
		DeployedBy: stack.Metadata.DeployedBy,
		DeployedAt: time.Now(),
		GitSHA:     stack.Metadata.GitSHA,
	})

	deployStart := time.Now()
	// Start the state machine
	go func() {
		o.runStateMachine(ctx, sc)
		status := "success"
		errMsg := ""
		if sc.LastError != nil {
			status = "failed"
			errMsg = sc.LastError.Error()
		}
		o.emitEvent(context.Background(), stack.Name, "deploy", status, errMsg,
			fmt.Sprintf("version=%s platform=%s", stack.Version, platform), deployStart)
	}()

	o.logger.Infow("deploy initiated",
		"stack", stack.Name,
		"version", stack.Version,
		"platform", platform,
		"services", len(stack.Services),
	)
	return nil
}

// Scale updates the replica count for a service and immediately applies the change.
func (o *Orchestrator) Scale(ctx context.Context, stackID, service string, replicas int) error {
	o.mu.RLock()
	sc, ok := o.stacks[stackID]
	o.mu.RUnlock()
	if !ok {
		return fmt.Errorf("stack %q not found", stackID)
	}

	// Update the desired manifest
	found := false
	for i := range sc.Manifest.Services {
		if sc.Manifest.Services[i].Name == service {
			sc.Manifest.Services[i].Replicas = replicas
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("service %q not found in stack %q", service, stackID)
	}

	// Write updated desired state to etcd
	if err := o.store.SetDesired(ctx, stackID, sc.Manifest); err != nil {
		return err
	}

	o.logger.Infow("scale requested", "stack", stackID, "service", service, "replicas", replicas)

	scaleStart := time.Now()
	// Apply immediately — don't wait for the 30-second reconcile tick.
	// Run in background so the API call returns promptly.
	go func() {
		bgCtx := context.Background()
		if err := sc.Adapter.Apply(bgCtx, sc.Manifest); err != nil {
			o.logger.Errorw("scale apply failed", "stack", stackID, "service", service, "error", err)
			o.emitEvent(bgCtx, stackID, "scale", "failed", err.Error(),
				fmt.Sprintf("service=%s replicas=%d", service, replicas), scaleStart)
		} else {
			o.logger.Infow("scale applied", "stack", stackID, "service", service, "replicas", replicas)
			o.emitEvent(bgCtx, stackID, "scale", "success", "",
				fmt.Sprintf("service=%s replicas=%d", service, replicas), scaleStart)
		}
	}()

	return nil
}

// Stop pauses a running stack — containers are stopped but volumes are preserved.
// The stopped state is persisted in etcd so it survives controller restarts.
func (o *Orchestrator) Stop(ctx context.Context, stackID string) error {
	o.mu.RLock()
	sc, ok := o.stacks[stackID]
	o.mu.RUnlock()

	var adapter PlatformAdapter
	if ok {
		adapter = sc.Adapter
	} else {
		// Load from etcd
		var desired manifest.Stack
		if err := o.store.GetDesired(ctx, stackID, &desired); err != nil {
			return fmt.Errorf("stack %q not found", stackID)
		}
		platform := desired.Platform
		if platform == "" {
			platform = "docker"
		}
		o.mu.RLock()
		a, has := o.adapters[platform]
		o.mu.RUnlock()
		if !has {
			return fmt.Errorf("no adapter for platform %q", platform)
		}
		adapter = a
	}

	stopStart := time.Now()
	o.store.SetStatus(ctx, stackID, string(StateStopping))
	if err := adapter.Stop(ctx, stackID); err != nil {
		o.store.SetStatus(ctx, stackID, string(StateFailed))
		o.emitEvent(ctx, stackID, "stop", "failed", err.Error(), "", stopStart)
		return fmt.Errorf("stop stack: %w", err)
	}
	o.store.SetStatus(ctx, stackID, string(StateStopped))
	if ok {
		sc.State = StateStopped
	}
	o.emitEvent(ctx, stackID, "stop", "success", "", "", stopStart)
	o.logger.Infow("stack stopped", "stack", stackID)
	return nil
}

// Down removes containers for a stack but preserves volumes and the stack entry in etcd.
// The stack can be brought back up via Redeploy or Start without losing data.
func (o *Orchestrator) Down(ctx context.Context, stackID string) error {
	adapter, err := o.adapterForStack(ctx, stackID)
	if err != nil {
		return err
	}

	downStart := time.Now()
	o.store.SetStatus(ctx, stackID, string(StateDraining))
	if err := adapter.Down(ctx, stackID); err != nil {
		o.store.SetStatus(ctx, stackID, string(StateFailed))
		o.emitEvent(ctx, stackID, "down", "failed", err.Error(), "", downStart)
		return fmt.Errorf("down stack: %w", err)
	}
	o.store.SetStatus(ctx, stackID, string(StateDown))

	o.mu.Lock()
	if sc, ok := o.stacks[stackID]; ok {
		sc.State = StateDown
	}
	o.mu.Unlock()

	o.emitEvent(ctx, stackID, "down", "success", "", "", downStart)
	o.logger.Infow("stack down (containers removed, volumes kept)", "stack", stackID)
	return nil
}

// Restart stops then starts a stack without losing data or re-pulling images.
func (o *Orchestrator) Restart(ctx context.Context, stackID string) error {
	adapter, err := o.adapterForStack(ctx, stackID)
	if err != nil {
		return err
	}

	restartStart := time.Now()
	o.store.SetStatus(ctx, stackID, string(StateStopping))
	if err := adapter.Restart(ctx, stackID); err != nil {
		o.store.SetStatus(ctx, stackID, string(StateFailed))
		o.emitEvent(ctx, stackID, "restart", "failed", err.Error(), "", restartStart)
		return fmt.Errorf("restart stack: %w", err)
	}
	o.store.SetStatus(ctx, stackID, string(StateRunning))

	o.mu.Lock()
	if sc, ok := o.stacks[stackID]; ok {
		sc.State = StateRunning
	}
	o.mu.Unlock()

	o.emitEvent(ctx, stackID, "restart", "success", "", "", restartStart)
	o.logger.Infow("stack restarted", "stack", stackID)
	return nil
}

// Start resumes a stopped stack without re-deploying (no rebuild, no image pull).
func (o *Orchestrator) Start(ctx context.Context, stackID string) error {
	var desired manifest.Stack
	if err := o.store.GetDesired(ctx, stackID, &desired); err != nil {
		return fmt.Errorf("stack %q not found", stackID)
	}

	platform := desired.Platform
	if platform == "" {
		platform = "docker"
	}
	o.mu.RLock()
	adapter, ok := o.adapters[platform]
	o.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no adapter for platform %q", platform)
	}

	startStart := time.Now()
	o.store.SetStatus(ctx, stackID, string(StateStarting))
	if err := adapter.Start(ctx, stackID); err != nil {
		o.store.SetStatus(ctx, stackID, string(StateFailed))
		o.emitEvent(ctx, stackID, "start", "failed", err.Error(), "", startStart)
		return fmt.Errorf("start stack: %w", err)
	}
	o.store.SetStatus(ctx, stackID, string(StateRunning))
	o.emitEvent(ctx, stackID, "start", "success", "", "", startStart)

	// Update or create in-memory context
	o.mu.Lock()
	sc, exists := o.stacks[stackID]
	if exists {
		sc.State = StateRunning
	} else {
		o.stacks[stackID] = &StackContext{
			ID:      stackID,
			State:   StateRunning,
			Manifest: &desired,
			Adapter: adapter,
		}
	}
	o.mu.Unlock()

	o.logger.Infow("stack started", "stack", stackID)
	return nil
}

// Destroy tears down a stack.
func (o *Orchestrator) Destroy(ctx context.Context, stackID string) error {
	o.mu.RLock()
	sc, ok := o.stacks[stackID]
	o.mu.RUnlock()

	destroyStart := time.Now()
	if !ok {
		// Stack not in memory (deployed via CLI before controller started).
		// Best-effort: load desired state to find the adapter and call Destroy,
		// then clean up etcd regardless.
		var desired manifest.Stack
		if err := o.store.GetDesired(ctx, stackID, &desired); err == nil {
			platform := desired.Platform
			if platform == "" {
				platform = "docker"
			}
			o.mu.RLock()
			adapter, hasAdapter := o.adapters[platform]
			o.mu.RUnlock()
			if hasAdapter {
				if err := adapter.Destroy(ctx, stackID); err != nil {
					o.logger.Warnw("destroy adapter error (cleaning up anyway)", "stack", stackID, "error", err)
				}
			}
		}
		o.store.SetStatus(ctx, stackID, string(StateStopped))
		o.store.DeleteStack(ctx, stackID)
		o.emitEvent(ctx, stackID, "destroy", "success", "", "", destroyStart)
		o.logger.Infow("stack destroyed (etcd-only)", "stack", stackID)
		return nil
	}

	o.transition(ctx, sc, StateDraining)

	if err := sc.Adapter.Destroy(ctx, stackID); err != nil {
		o.logger.Errorw("destroy failed", "stack", stackID, "error", err)
		o.emitEvent(ctx, stackID, "destroy", "failed", err.Error(), "", destroyStart)
		return err
	}

	o.transition(ctx, sc, StateStopped)

	o.mu.Lock()
	delete(o.stacks, stackID)
	o.mu.Unlock()

	o.store.DeleteStack(ctx, stackID)
	o.emitEvent(ctx, stackID, "destroy", "success", "", "", destroyStart)
	o.logger.Infow("stack destroyed", "stack", stackID)
	return nil
}

// Rollback reverts a stack to a previous version from the ledger.
func (o *Orchestrator) Rollback(ctx context.Context, stackID string) error {
	entries, err := o.store.GetLedger(ctx, stackID, 2)
	if err != nil || len(entries) < 2 {
		return fmt.Errorf("no previous version to rollback to")
	}

	prev := entries[1] // index 0 is current, 1 is previous
	prevManifest, ok := prev.Manifest.(*manifest.Stack)
	if !ok {
		return fmt.Errorf("cannot deserialize previous manifest")
	}

	o.logger.Infow("rolling back", "stack", stackID, "to_version", prev.Version)
	return o.Deploy(ctx, prevManifest)
}

// Status returns the current state of a stack.
func (o *Orchestrator) Status(ctx context.Context, stackID string) (*StackContext, error) {
	o.mu.RLock()
	sc, ok := o.stacks[stackID]
	o.mu.RUnlock()
	if ok {
		return sc, nil
	}
	// Not in memory — read status from etcd and return a minimal context.
	status, err := o.store.GetStatus(ctx, stackID)
	if err != nil || status == "" {
		return nil, fmt.Errorf("stack %q not found", stackID)
	}
	var desired manifest.Stack
	o.store.GetDesired(ctx, stackID, &desired)
	return &StackContext{
		ID:       stackID,
		State:    State(status),
		Manifest: &desired,
	}, nil
}

// StartReconcileLoop begins watching etcd for desired state changes
// and runs the reconciliation loop for all active stacks.
func (o *Orchestrator) StartReconcileLoop(ctx context.Context) {
	// Watch for external desired state changes (GitOps, API, auto-scaler)
	watchCh := o.store.WatchDesired(ctx)

	go func() {
		for {
			select {
			case stackID, ok := <-watchCh:
				if !ok {
					return
				}
				o.logger.Debugw("desired state changed", "stack", stackID)
				o.reconcile(ctx, stackID)

			case <-ctx.Done():
				return
			}
		}
	}()

	// Periodic reconciliation (catch drift + pick up stacks written before controller started)
	go func() {
		o.reconcileAll(ctx) // immediate pass on startup
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				o.reconcileAll(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()

	o.logger.Info("reconciliation loop started")
}

// --- Internal state machine ---

func (o *Orchestrator) runStateMachine(ctx context.Context, sc *StackContext) {
	maxRetries := 3

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		switch sc.State {
		case StatePending:
			o.transition(ctx, sc, StateDeploying)

		case StateDeploying:
			err := sc.Adapter.Apply(ctx, sc.Manifest)
			if err != nil {
				sc.LastError = err
				sc.Retries++
				o.logger.Errorw("deploy failed", "stack", sc.ID, "attempt", sc.Retries, "error", err)

				if sc.Retries >= maxRetries {
					if sc.Manifest.Strategy.RollbackOnFailure {
						o.transition(ctx, sc, StateRollingBack)
					} else {
						o.transition(ctx, sc, StateFailed)
						return
					}
				} else {
					time.Sleep(time.Duration(sc.Retries) * 5 * time.Second) // exponential-ish backoff
					continue
				}
			} else {
				o.transition(ctx, sc, StateVerifying)
			}

		case StateVerifying:
			healthy := o.waitForHealthy(ctx, sc, 120*time.Second)
			if healthy {
				// Register all services in the service registry + DNS
				o.registerServices(ctx, sc)
				o.transition(ctx, sc, StateRunning)
				return
			} else {
				sc.LastError = fmt.Errorf("health check timeout")
				if sc.Manifest.Strategy.RollbackOnFailure {
					o.transition(ctx, sc, StateRollingBack)
				} else {
					o.transition(ctx, sc, StateFailed)
					return
				}
			}

		case StateRollingBack:
			o.logger.Warnw("initiating rollback", "stack", sc.ID)
			err := o.Rollback(ctx, sc.ID)
			if err != nil {
				o.logger.Errorw("rollback failed", "stack", sc.ID, "error", err)
				o.transition(ctx, sc, StateFailed)
			}
			return

		case StateRunning, StateFailed, StateStopped, StateDown:
			return
		}
	}
}

func (o *Orchestrator) transition(ctx context.Context, sc *StackContext, to State) {
	from := sc.State
	sc.State = to
	o.store.SetStatus(ctx, sc.ID, string(to))
	o.logger.Infow("state transition", "stack", sc.ID, "from", from, "to", to)
}

func (o *Orchestrator) waitForHealthy(ctx context.Context, sc *StackContext, timeout time.Duration) bool {
	deadline := time.After(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			return false
		case <-ticker.C:
			status, err := sc.Adapter.Status(ctx, sc.ID)
			if err != nil {
				continue
			}
			allHealthy := true
			for _, svc := range status.Services {
				if svc.Ready < svc.Replicas {
					allHealthy = false
					break
				}
			}
			if allHealthy {
				return true
			}
		case <-ctx.Done():
			return false
		}
	}
}

func (o *Orchestrator) registerServices(ctx context.Context, sc *StackContext) {
	status, err := sc.Adapter.Status(ctx, sc.ID)
	if err != nil {
		o.logger.Warnw("cannot register services, status unavailable", "stack", sc.ID)
		return
	}

	for _, svc := range status.Services {
		ep := store.ServiceEndpoint{
			Service:      svc.Name,
			Stack:        sc.ID,
			Instance:     fmt.Sprintf("%s-0", svc.Name),
			Health:       svc.Health,
			Version:      sc.Manifest.Version,
			Weight:       100,
			RegisteredAt: time.Now(),
		}
		o.store.RegisterService(ctx, ep)
	}
}

// ServiceInspect returns detailed runtime info for one service in a stack.
func (o *Orchestrator) ServiceInspect(ctx context.Context, stackID, service string) (*ServiceDetail, error) {
	adapter, err := o.adapterForStack(ctx, stackID)
	if err != nil {
		return nil, err
	}
	detail, err := adapter.Inspect(ctx, stackID, service)
	if err != nil {
		return nil, err
	}
	if detail != nil {
		detail.Platform = adapter.Name()
	}
	return detail, nil
}

// ServiceLogs returns the last N log lines for one service in a stack.
func (o *Orchestrator) ServiceLogs(ctx context.Context, stackID, service string, tail int) (string, error) {
	adapter, err := o.adapterForStack(ctx, stackID)
	if err != nil {
		return "", err
	}
	return adapter.Logs(ctx, stackID, service, tail)
}

// ServiceLogStream opens a live-follow log stream for one service.
// The caller is responsible for closing the returned ReadCloser.
func (o *Orchestrator) ServiceLogStream(ctx context.Context, stackID, service string) (io.ReadCloser, error) {
	adapter, err := o.adapterForStack(ctx, stackID)
	if err != nil {
		return nil, err
	}
	return adapter.LogStream(ctx, stackID, service)
}

// adapterForStack resolves the platform adapter for a stack (from memory or etcd).
func (o *Orchestrator) adapterForStack(ctx context.Context, stackID string) (PlatformAdapter, error) {
	o.mu.RLock()
	sc, ok := o.stacks[stackID]
	o.mu.RUnlock()
	if ok && sc.Adapter != nil {
		return sc.Adapter, nil
	}
	var desired manifest.Stack
	if err := o.store.GetDesired(ctx, stackID, &desired); err != nil {
		return nil, fmt.Errorf("stack %q not found", stackID)
	}
	platform := desired.Platform
	if platform == "" {
		platform = "docker"
	}
	o.mu.RLock()
	adapter, has := o.adapters[platform]
	o.mu.RUnlock()
	if !has {
		return nil, fmt.Errorf("no adapter for platform %q", platform)
	}
	return adapter, nil
}

// Redeploy immediately applies the current desired state for a stack.
// If the stack is already in memory (previously deployed this session), it updates
// the manifest in place and calls Apply without going through the full state machine.
// If not in memory, it falls back to Deploy (full state machine from pending).
func (o *Orchestrator) Redeploy(ctx context.Context, stackID string) error {
	var desired manifest.Stack
	if err := o.store.GetDesired(ctx, stackID, &desired); err != nil {
		return fmt.Errorf("stack %q desired state not found: %w", stackID, err)
	}

	o.mu.RLock()
	sc, ok := o.stacks[stackID]
	o.mu.RUnlock()

	if !ok {
		return o.Deploy(ctx, &desired)
	}

	// Stack already exists — update manifest and apply immediately.
	sc.Manifest = &desired
	sc.Retries = 0
	o.transition(ctx, sc, StateDeploying)

	redeployStart := time.Now()
	go func() {
		bgCtx := context.Background()
		if err := sc.Adapter.Apply(bgCtx, &desired); err != nil {
			o.logger.Errorw("redeploy failed", "stack", stackID, "error", err)
			o.transition(bgCtx, sc, StateFailed)
			o.emitEvent(bgCtx, stackID, "redeploy", "failed", err.Error(), "", redeployStart)
			return
		}
		o.registerServices(bgCtx, sc)
		o.transition(bgCtx, sc, StateRunning)
		o.emitEvent(bgCtx, stackID, "redeploy", "success", "", "", redeployStart)
		o.logger.Infow("redeploy complete", "stack", stackID)
	}()

	return nil
}

func (o *Orchestrator) reconcile(ctx context.Context, stackID string) {
	o.mu.RLock()
	sc, ok := o.stacks[stackID]
	o.mu.RUnlock()

	// Stack not in memory — written directly to etcd by CLI or API.
	// Load the desired manifest and kick off the state machine.
	if !ok {
		var desired manifest.Stack
		if err := o.store.GetDesired(ctx, stackID, &desired); err != nil {
			o.logger.Warnw("reconcile: desired state not found", "stack", stackID, "error", err)
			return
		}
		if err := o.Deploy(ctx, &desired); err != nil {
			o.logger.Errorw("reconcile: failed to start deployment", "stack", stackID, "error", err)
		}
		return
	}

	// If stack is pending (set by API but not yet started), deploy it now.
	if sc.State == StatePending {
		go o.runStateMachine(ctx, sc)
		return
	}

	if sc.State != StateRunning {
		return
	}

	// Diff desired vs actual — apply only when changes are detected.
	var desired manifest.Stack
	if err := o.store.GetDesired(ctx, stackID, &desired); err != nil {
		return
	}

	diff, err := sc.Adapter.Diff(ctx, &desired, sc.Manifest)
	if err != nil {
		o.logger.Warnw("diff failed", "stack", stackID, "error", err)
		return
	}

	hasChanges := len(diff.Create) > 0 || len(diff.Update) > 0 || len(diff.Destroy) > 0
	// Also detect replica count changes that Diff() might not catch (e.g. compose stub).
	if !hasChanges {
		for i, svc := range desired.Services {
			if i < len(sc.Manifest.Services) && sc.Manifest.Services[i].Replicas != svc.Replicas {
				hasChanges = true
				break
			}
		}
	}
	if !hasChanges {
		return
	}

	o.logger.Infow("drift detected, reconciling",
		"stack", stackID,
		"create", len(diff.Create),
		"update", len(diff.Update),
		"destroy", len(diff.Destroy),
	)

	sc.Manifest = &desired
	if err := sc.Adapter.Apply(ctx, &desired); err != nil {
		o.logger.Errorw("reconcile apply failed", "stack", stackID, "error", err)
	}
}

// emitEvent records an operation outcome (success/failed) in the event store.
func (o *Orchestrator) emitEvent(ctx context.Context, stackID, op, status, errMsg, details string, started time.Time) {
	ev := store.OperationEvent{
		ID:         fmt.Sprintf("%s-%s-%d", stackID, op, started.UnixNano()),
		StackID:    stackID,
		Operation:  op,
		Status:     status,
		StartedAt:  started,
		FinishedAt: time.Now(),
		Error:      errMsg,
		Details:    details,
	}
	if err := o.store.AppendEvent(ctx, ev); err != nil {
		o.logger.Warnw("failed to record event", "stack", stackID, "op", op, "error", err)
	}
}

func (o *Orchestrator) reconcileAll(ctx context.Context) {
	// Collect in-memory stacks
	o.mu.RLock()
	ids := make(map[string]struct{}, len(o.stacks))
	for id := range o.stacks {
		ids[id] = struct{}{}
	}
	o.mu.RUnlock()

	// Also pick up any stacks written to etcd that we don't know about yet
	// (e.g. deployed via CLI before the controller started)
	if etcdIDs, err := o.store.ListStacks(ctx); err == nil {
		for _, id := range etcdIDs {
			ids[id] = struct{}{}
		}
	}

	for id := range ids {
		o.reconcile(ctx, id)
	}
}
