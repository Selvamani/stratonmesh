package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/stratonmesh/stratonmesh/pkg/manifest"
	"github.com/stratonmesh/stratonmesh/pkg/store"
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
	StateDraining     State = "draining"
	StateStopped      State = "stopped"
	StateFailed       State = "failed"
	StateRollingBack  State = "rolling_back"
)

// PlatformAdapter is the interface every deployment target must implement.
type PlatformAdapter interface {
	Name() string
	Generate(ctx context.Context, stack *manifest.Stack) ([]byte, error) // produce platform artifacts
	Apply(ctx context.Context, stack *manifest.Stack) error              // execute deployment
	Status(ctx context.Context, stackID string) (*AdapterStatus, error)  // query running state
	Destroy(ctx context.Context, stackID string) error                   // tear down
	Diff(ctx context.Context, desired, actual *manifest.Stack) (*DiffResult, error)
	Rollback(ctx context.Context, stackID string, version string) error
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

	// Start the state machine
	go o.runStateMachine(ctx, sc)

	o.logger.Infow("deploy initiated",
		"stack", stack.Name,
		"version", stack.Version,
		"platform", platform,
		"services", len(stack.Services),
	)
	return nil
}

// Scale updates the replica count for a service and triggers reconciliation.
func (o *Orchestrator) Scale(ctx context.Context, stackID, service string, replicas int) error {
	o.mu.RLock()
	sc, ok := o.stacks[stackID]
	o.mu.RUnlock()
	if !ok {
		return fmt.Errorf("stack %q not found", stackID)
	}

	// Update the desired manifest
	for i := range sc.Manifest.Services {
		if sc.Manifest.Services[i].Name == service {
			sc.Manifest.Services[i].Replicas = replicas
			break
		}
	}

	// Write updated desired state
	if err := o.store.SetDesired(ctx, stackID, sc.Manifest); err != nil {
		return err
	}

	o.logger.Infow("scale requested", "stack", stackID, "service", service, "replicas", replicas)
	return nil
}

// Destroy tears down a stack.
func (o *Orchestrator) Destroy(ctx context.Context, stackID string) error {
	o.mu.RLock()
	sc, ok := o.stacks[stackID]
	o.mu.RUnlock()

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
		o.logger.Infow("stack destroyed (etcd-only)", "stack", stackID)
		return nil
	}

	o.transition(ctx, sc, StateDraining)

	if err := sc.Adapter.Destroy(ctx, stackID); err != nil {
		o.logger.Errorw("destroy failed", "stack", stackID, "error", err)
		return err
	}

	o.transition(ctx, sc, StateStopped)

	o.mu.Lock()
	delete(o.stacks, stackID)
	o.mu.Unlock()

	o.store.DeleteStack(ctx, stackID)
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

		case StateRunning, StateFailed, StateStopped:
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

	if sc.State != StateRunning {
		return
	}

	// Diff desired vs actual
	var desired manifest.Stack
	if err := o.store.GetDesired(ctx, stackID, &desired); err != nil {
		return
	}

	diff, err := sc.Adapter.Diff(ctx, &desired, sc.Manifest)
	if err != nil {
		o.logger.Warnw("diff failed", "stack", stackID, "error", err)
		return
	}

	if len(diff.Create) == 0 && len(diff.Update) == 0 && len(diff.Destroy) == 0 {
		return // no changes
	}

	o.logger.Infow("drift detected, reconciling",
		"stack", stackID,
		"create", len(diff.Create),
		"update", len(diff.Update),
		"destroy", len(diff.Destroy),
	)

	// Apply the desired state
	sc.Manifest = &desired
	if err := sc.Adapter.Apply(ctx, &desired); err != nil {
		o.logger.Errorw("reconcile apply failed", "stack", stackID, "error", err)
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
