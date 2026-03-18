package autoscaler

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/selvamani/stratonmesh/pkg/manifest"
	"github.com/selvamani/stratonmesh/pkg/store"
	"github.com/selvamani/stratonmesh/pkg/telemetry"
	"go.uber.org/zap"
)

// ScaleAction is emitted when the auto-scaler decides to change replica count.
type ScaleAction struct {
	StackID  string
	Service  string
	From     int
	To       int
	Reason   string
	Metric   string
	Value    float64
	Target   float64
}

// Orchestrator is the interface the auto-scaler uses to scale services.
// Matches orchestrator.Orchestrator.Scale.
type Orchestrator interface {
	Scale(ctx context.Context, stackID, service string, replicas int) error
}

// AutoScaler subscribes to the telemetry bus, evaluates ScaleMetric thresholds
// defined in manifests, and adjusts replica counts via the orchestrator.
//
// Design:
//   - One goroutine per stack-service pair that has auto-scaling enabled.
//   - Cooldown prevents more than one scale event per window (default 2 min).
//   - Scale-up is immediate; scale-down requires 3 consecutive evaluations below threshold.
type AutoScaler struct {
	store  *store.Store
	bus    *telemetry.Bus
	orch   Orchestrator
	logger *zap.SugaredLogger

	mu       sync.Mutex
	states   map[string]*serviceState // key: stackID/service
	cooldown time.Duration
}

type serviceState struct {
	stackID     string
	service     string
	spec        manifest.ScalingSpec
	current     int // current replica count
	lastScaled  time.Time
	belowCount  int // consecutive evaluations below scale-down threshold
	latestCPU   float64
	latestMem   float64
	latestRate  float64
}

// Config holds auto-scaler settings.
type Config struct {
	// Cooldown is the minimum time between scale events (default 2m).
	Cooldown time.Duration `yaml:"cooldown" json:"cooldown"`
	// EvalInterval is how often metrics are polled (default 15s).
	EvalInterval time.Duration `yaml:"evalInterval" json:"evalInterval"`
	// ScaleDownThreshold is how many consecutive under-threshold evals before scale-down (default 3).
	ScaleDownThreshold int `yaml:"scaleDownThreshold" json:"scaleDownThreshold"`
}

// New creates an AutoScaler.
func New(st *store.Store, bus *telemetry.Bus, orch Orchestrator, cfg Config, logger *zap.SugaredLogger) *AutoScaler {
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 2 * time.Minute
	}
	if cfg.EvalInterval <= 0 {
		cfg.EvalInterval = 15 * time.Second
	}
	if cfg.ScaleDownThreshold <= 0 {
		cfg.ScaleDownThreshold = 3
	}
	return &AutoScaler{
		store:    st,
		bus:      bus,
		orch:     orch,
		logger:   logger,
		states:   make(map[string]*serviceState),
		cooldown: cfg.Cooldown,
	}
}

// Watch registers a service for auto-scaling and starts the evaluation loop.
func (a *AutoScaler) Watch(stackID string, svc manifest.Service) {
	if !svc.Scaling.Auto {
		return
	}
	key := stackID + "/" + svc.Name
	a.mu.Lock()
	if _, exists := a.states[key]; exists {
		a.mu.Unlock()
		return
	}
	a.states[key] = &serviceState{
		stackID: stackID,
		service: svc.Name,
		spec:    svc.Scaling,
		current: svc.DefaultReplicas(),
	}
	a.mu.Unlock()
	a.logger.Infow("auto-scaler watching", "stack", stackID, "service", svc.Name,
		"min", svc.Scaling.MinReplicas, "max", svc.Scaling.MaxReplicas)
}

// Unwatch stops auto-scaling a service.
func (a *AutoScaler) Unwatch(stackID, service string) {
	a.mu.Lock()
	delete(a.states, stackID+"/"+service)
	a.mu.Unlock()
}

// WatchStack registers all auto-scalable services from a stack.
func (a *AutoScaler) WatchStack(stackID string, services []manifest.Service) {
	for _, svc := range services {
		a.Watch(stackID, svc)
	}
}

// Start begins the metric subscription loop. Blocks until ctx is cancelled.
func (a *AutoScaler) Start(ctx context.Context) error {
	metricCh, err := a.bus.SubscribeMetrics(ctx, "")
	if err != nil {
		return fmt.Errorf("subscribe metrics: %w", err)
	}

	// Metric ingestion
	go func() {
		for {
			select {
			case m, ok := <-metricCh:
				if !ok {
					return
				}
				a.ingestMetric(m)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Evaluation loop
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				a.evaluateAll(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()

	a.logger.Info("auto-scaler started")
	return nil
}

// ingestMetric updates the latest observed value for a service.
func (a *AutoScaler) ingestMetric(m telemetry.MetricPoint) {
	if m.Stack == "" || m.Service == "" {
		return
	}
	key := m.Stack + "/" + m.Service
	a.mu.Lock()
	defer a.mu.Unlock()
	state, ok := a.states[key]
	if !ok {
		return
	}
	switch m.Name {
	case "cpu":
		state.latestCPU = m.Value
	case "memory":
		state.latestMem = m.Value
	case "request_rate", "requestRate":
		state.latestRate = m.Value
	}
}

// evaluateAll checks every watched service for scale actions.
func (a *AutoScaler) evaluateAll(ctx context.Context) {
	a.mu.Lock()
	snapshot := make([]*serviceState, 0, len(a.states))
	for _, s := range a.states {
		snapshot = append(snapshot, s)
	}
	a.mu.Unlock()

	for _, state := range snapshot {
		if action := a.evaluate(state); action != nil {
			a.applyAction(ctx, state, action)
		}
	}
}

// evaluate computes whether a scale action is needed for a single service.
func (a *AutoScaler) evaluate(s *serviceState) *ScaleAction {
	if time.Since(s.lastScaled) < a.cooldown {
		return nil
	}

	for _, metric := range s.spec.Metrics {
		current, target, ok := a.metricValue(s, metric)
		if !ok {
			continue
		}

		ratio := current / target

		if ratio > 1.2 && s.current < maxReplicas(s.spec) {
			// Scale up: current metric exceeds 120% of target
			desired := clamp(int(float64(s.current)*ratio+0.5), minReplicas(s.spec), maxReplicas(s.spec))
			if desired > s.current {
				s.belowCount = 0
				return &ScaleAction{
					StackID: s.stackID, Service: s.service,
					From: s.current, To: desired,
					Metric: metric.Type, Value: current, Target: target,
					Reason: fmt.Sprintf("%s at %.1f%% (target %.1f%%): scale up", metric.Type, current, target),
				}
			}
		} else if ratio < 0.5 && s.current > minReplicas(s.spec) {
			// Scale down: metric below 50% of target — require ScaleDownThreshold consecutive checks
			s.belowCount++
			if s.belowCount >= 3 {
				desired := clamp(int(float64(s.current)*ratio+0.5), minReplicas(s.spec), maxReplicas(s.spec))
				if desired < s.current {
					s.belowCount = 0
					return &ScaleAction{
						StackID: s.stackID, Service: s.service,
						From: s.current, To: desired,
						Metric: metric.Type, Value: current, Target: target,
						Reason: fmt.Sprintf("%s at %.1f%% (target %.1f%%): scale down", metric.Type, current, target),
					}
				}
			}
		} else {
			s.belowCount = 0
		}
	}
	return nil
}

// metricValue returns (current, target, ok) for a given ScaleMetric.
func (a *AutoScaler) metricValue(s *serviceState, m manifest.ScaleMetric) (current, target float64, ok bool) {
	switch strings.ToLower(m.Type) {
	case "cpu":
		current = s.latestCPU
	case "memory":
		current = s.latestMem
	case "requestrate", "request_rate":
		current = s.latestRate
	default:
		return 0, 0, false
	}
	if current == 0 {
		return 0, 0, false // no data yet
	}
	target = parseTarget(m.Target)
	if target <= 0 {
		return 0, 0, false
	}
	return current, target, true
}

// applyAction calls the orchestrator to change replica count and records timing.
func (a *AutoScaler) applyAction(ctx context.Context, state *serviceState, action *ScaleAction) {
	a.logger.Infow("scaling service",
		"stack", action.StackID, "service", action.Service,
		"from", action.From, "to", action.To,
		"reason", action.Reason,
	)
	if err := a.orch.Scale(ctx, action.StackID, action.Service, action.To); err != nil {
		a.logger.Errorw("scale failed", "stack", action.StackID, "service", action.Service, "error", err)
		return
	}
	a.mu.Lock()
	state.current = action.To
	state.lastScaled = time.Now()
	a.mu.Unlock()
}

// --- Helpers ---

// parseTarget parses "70%" → 70.0, "1000/s" → 1000.0.
func parseTarget(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "%")
	s = strings.TrimSuffix(s, "/s")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func minReplicas(spec manifest.ScalingSpec) int {
	if spec.MinReplicas <= 0 {
		return 1
	}
	return spec.MinReplicas
}

func maxReplicas(spec manifest.ScalingSpec) int {
	if spec.MaxReplicas <= 0 {
		return 10
	}
	return spec.MaxReplicas
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
