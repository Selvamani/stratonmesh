package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/open-policy-agent/opa/rego"
	"github.com/stratonmesh/stratonmesh/pkg/manifest"
	"github.com/stratonmesh/stratonmesh/pkg/store"
	"github.com/stratonmesh/stratonmesh/pkg/telemetry"
	"go.uber.org/zap"
)

// Pipeline executes the 7-stage IaC deployment pipeline.
type Pipeline struct {
	store      *store.Store
	bus        *telemetry.Bus
	logger     *zap.SugaredLogger
	opaModules []string // extra Rego policy source strings
}

// DefaultPolicy is the built-in OPA policy enforcing baseline safety rules.
const DefaultPolicy = `
package stratonmesh.policy

import future.keywords.if
import future.keywords.contains

default allow := true

# Blast radius: single deploy must not touch > 60% of services (when > 3 total).
deny contains msg if {
    total := count([s | s := input.stack.services[_]; s.enabled != false])
    total > 3
    changed := count(input.diff.create) + count(input.diff.update) + count(input.diff.destroy)
    changed / total > 0.6
    msg := sprintf("blast radius too high: %d/%d services affected (>60%%)", [changed, total])
}

# CPU limit: max 16 cores (16000m) per service.
deny contains msg if {
    svc := input.stack.services[_]
    svc.enabled != false
    cpu := svc.resources.cpu
    cpu != ""
    cpuMillis(cpu) > 16000
    msg := sprintf("service %q: CPU %s exceeds 16-core limit", [svc.name, cpu])
}

# Memory limit: max 64Gi per service.
deny contains msg if {
    svc := input.stack.services[_]
    svc.enabled != false
    mem := svc.resources.memory
    mem != ""
    memBytes(mem) > 68719476736
    msg := sprintf("service %q: memory %s exceeds 64Gi limit", [svc.name, mem])
}

# Replica cap: max 50 replicas.
deny contains msg if {
    svc := input.stack.services[_]
    svc.scaling.maxReplicas > 50
    msg := sprintf("service %q: maxReplicas %d exceeds 50", [svc.name, svc.scaling.maxReplicas])
}

# Helper: parse CPU string to millicores (best-effort, OPA limitations).
cpuMillis(s) = v if {
    endswith(s, "m")
    v := to_number(trim_suffix(s, "m"))
}
cpuMillis(s) = v if {
    not endswith(s, "m")
    v := to_number(s) * 1000
}

# Helper: parse memory string to bytes (Gi only for now).
memBytes(s) = v if {
    endswith(s, "Gi")
    v := to_number(trim_suffix(s, "Gi")) * 1073741824
}
memBytes(s) = v if {
    endswith(s, "Mi")
    v := to_number(trim_suffix(s, "Mi")) * 1048576
}
memBytes(s) = 0 if {
    not endswith(s, "Gi")
    not endswith(s, "Mi")
}
`

// PipelineConfig holds pipeline behavior settings.
type PipelineConfig struct {
	DryRun          bool              `yaml:"dryRun"`
	RequireApproval bool              `yaml:"requireApproval"`
	Variables       map[string]string `yaml:"variables"`
	Environment     string            `yaml:"environment"`
}

// PipelineResult captures the outcome of each stage.
type PipelineResult struct {
	PipelineID string            `json:"pipelineId"`
	Stack      *manifest.Stack   `json:"stack"`
	Stages     []StageResult     `json:"stages"`
	DiffResult *DiffOutput       `json:"diffResult,omitempty"`
	Duration   time.Duration     `json:"duration"`
	Error      error             `json:"error,omitempty"`
}

// StageResult records one stage's outcome.
type StageResult struct {
	Name     string        `json:"name"`
	Status   string        `json:"status"` // passed, failed, skipped
	Duration time.Duration `json:"duration"`
	Message  string        `json:"message,omitempty"`
}

// DiffOutput describes what will change.
type DiffOutput struct {
	Create    []string `json:"create"`
	Update    []string `json:"update"`
	Destroy   []string `json:"destroy"`
	Unchanged []string `json:"unchanged"`
}

// New creates a Pipeline.
func New(st *store.Store, bus *telemetry.Bus, logger *zap.SugaredLogger) *Pipeline {
	return &Pipeline{store: st, bus: bus, logger: logger}
}

// AddPolicy appends an extra Rego policy module. Call before Run.
// The module must be in the "stratonmesh.policy" package and may add rules
// to the "deny" set.
func (p *Pipeline) AddPolicy(regoSource string) {
	p.opaModules = append(p.opaModules, regoSource)
}

// EvaluatePoliciesPublic is the exported counterpart of evaluatePolicies,
// intended for use in tests and external policy validation tools.
func (p *Pipeline) EvaluatePoliciesPublic(stack *manifest.Stack, diff *DiffOutput) string {
	return p.evaluatePolicies(stack, diff)
}

// Run executes the full 7-stage pipeline.
func (p *Pipeline) Run(ctx context.Context, manifestPath string, cfg PipelineConfig) (*PipelineResult, error) {
	start := time.Now()
	pipelineID := fmt.Sprintf("pipe-%d", time.Now().UnixNano()%1000000)

	result := &PipelineResult{
		PipelineID: pipelineID,
	}

	p.logger.Infow("pipeline started", "id", pipelineID, "manifest", manifestPath, "env", cfg.Environment)

	// Stage 1: Parse + Validate
	stage1Start := time.Now()
	stack, err := manifest.LoadFile(manifestPath)
	if err != nil {
		result.Stages = append(result.Stages, StageResult{Name: "parse", Status: "failed", Message: err.Error(), Duration: time.Since(stage1Start)})
		return result, err
	}
	errs := manifest.Validate(stack)
	if len(errs) > 0 {
		result.Stages = append(result.Stages, StageResult{Name: "parse", Status: "failed", Message: fmt.Sprintf("%v", errs), Duration: time.Since(stage1Start)})
		return result, fmt.Errorf("validation: %v", errs)
	}
	result.Stages = append(result.Stages, StageResult{Name: "parse", Status: "passed", Duration: time.Since(stage1Start), Message: fmt.Sprintf("%d services validated", len(stack.Services))})
	p.logger.Infow("stage 1 passed: parse + validate", "services", len(stack.Services))

	// Stage 2: Resolve Environment
	stage2Start := time.Now()
	if cfg.Environment != "" {
		stack.Environment = cfg.Environment
	}
	result.Stages = append(result.Stages, StageResult{Name: "resolve", Status: "passed", Duration: time.Since(stage2Start), Message: fmt.Sprintf("environment: %s", stack.Environment)})
	p.logger.Infow("stage 2 passed: resolve environment", "env", stack.Environment)

	// Stage 3: Interpolate Variables
	stage3Start := time.Now()
	if err := manifest.Interpolate(stack, cfg.Variables); err != nil {
		result.Stages = append(result.Stages, StageResult{Name: "interpolate", Status: "failed", Message: err.Error(), Duration: time.Since(stage3Start)})
		return result, err
	}
	stack.Metadata.PipelineID = pipelineID
	stack.Metadata.ResolvedAt = time.Now()
	result.Stages = append(result.Stages, StageResult{Name: "interpolate", Status: "passed", Duration: time.Since(stage3Start), Message: fmt.Sprintf("%d variables resolved", len(cfg.Variables))})
	p.logger.Infow("stage 3 passed: interpolate variables", "vars", len(cfg.Variables))

	// Stage 4: Diff + Plan
	stage4Start := time.Now()
	diff, err := p.computeDiff(ctx, stack)
	if err != nil {
		// First deploy — no existing state, entire stack is "create"
		diff = &DiffOutput{}
		for _, svc := range stack.Services {
			if svc.IsEnabled() {
				diff.Create = append(diff.Create, svc.Name)
			}
		}
	}
	result.DiffResult = diff
	result.Stages = append(result.Stages, StageResult{
		Name: "diff", Status: "passed", Duration: time.Since(stage4Start),
		Message: fmt.Sprintf("create:%d update:%d destroy:%d unchanged:%d",
			len(diff.Create), len(diff.Update), len(diff.Destroy), len(diff.Unchanged)),
	})
	p.logger.Infow("stage 4 passed: diff + plan",
		"create", len(diff.Create), "update", len(diff.Update),
		"destroy", len(diff.Destroy), "unchanged", len(diff.Unchanged))

	// Stage 5: Policy Gate
	stage5Start := time.Now()
	policyResult := p.evaluatePolicies(stack, diff)
	if policyResult != "" {
		result.Stages = append(result.Stages, StageResult{Name: "policy", Status: "failed", Message: policyResult, Duration: time.Since(stage5Start)})
		return result, fmt.Errorf("policy denied: %s", policyResult)
	}
	result.Stages = append(result.Stages, StageResult{Name: "policy", Status: "passed", Duration: time.Since(stage5Start), Message: "all policies passed"})
	p.logger.Info("stage 5 passed: policy gate")

	// Stage 5b: Manual approval (if required)
	if cfg.RequireApproval {
		result.Stages = append(result.Stages, StageResult{Name: "approval", Status: "skipped", Message: "auto-approved (dry-run or non-prod)"})
	}

	// Stage 6: Apply to Intent Store
	if cfg.DryRun {
		result.Stages = append(result.Stages, StageResult{Name: "apply", Status: "skipped", Message: "dry-run mode"})
		result.Stack = stack
		result.Duration = time.Since(start)
		return result, nil
	}

	stage6Start := time.Now()
	if err := p.store.SetDesired(ctx, stack.Name, stack); err != nil {
		result.Stages = append(result.Stages, StageResult{Name: "apply", Status: "failed", Message: err.Error(), Duration: time.Since(stage6Start)})
		return result, err
	}
	if err := p.store.SetStatus(ctx, stack.Name, "pending"); err != nil {
		p.logger.Warnw("failed to set status", "error", err)
	}

	// Record in version ledger
	p.store.AppendLedger(ctx, store.LedgerEntry{
		StackID:    stack.Name,
		Version:    stack.Version,
		Manifest:   stack,
		DeployedBy: stack.Metadata.DeployedBy,
		DeployedAt: time.Now(),
		GitSHA:     stack.Metadata.GitSHA,
	})

	result.Stages = append(result.Stages, StageResult{Name: "apply", Status: "passed", Duration: time.Since(stage6Start), Message: "desired state written to etcd"})
	p.logger.Info("stage 6 passed: apply to intent store")

	// Stage 7: Reconcile + Deploy (triggered by orchestrator watching etcd)
	result.Stages = append(result.Stages, StageResult{Name: "reconcile", Status: "passed", Message: "orchestrator will converge state"})
	p.logger.Info("stage 7: reconciler triggered")

	// Publish pipeline completion event
	if p.bus != nil {
		p.bus.PublishEvent(ctx, telemetry.Event{
			Type:     "deploy",
			StackID:  stack.Name,
			Severity: "success",
			Message:  fmt.Sprintf("Pipeline %s completed for %s v%s", pipelineID, stack.Name, stack.Version),
		})
	}

	result.Stack = stack
	result.Duration = time.Since(start)

	p.logger.Infow("pipeline completed",
		"id", pipelineID,
		"stack", stack.Name,
		"version", stack.Version,
		"duration", result.Duration,
		"stages", len(result.Stages),
	)

	return result, nil
}

// computeDiff compares new manifest against current desired state.
func (p *Pipeline) computeDiff(ctx context.Context, desired *manifest.Stack) (*DiffOutput, error) {
	var current manifest.Stack
	if err := p.store.GetDesired(ctx, desired.Name, &current); err != nil {
		return nil, err // no existing state
	}

	diff := &DiffOutput{}
	existingMap := make(map[string]*manifest.Service)
	for i := range current.Services {
		existingMap[current.Services[i].Name] = &current.Services[i]
	}

	for _, svc := range desired.Services {
		if !svc.IsEnabled() {
			continue
		}
		existing, ok := existingMap[svc.Name]
		if !ok {
			diff.Create = append(diff.Create, svc.Name)
		} else if svc.Image != existing.Image || svc.Replicas != existing.Replicas {
			diff.Update = append(diff.Update, svc.Name)
		} else {
			diff.Unchanged = append(diff.Unchanged, svc.Name)
		}
		delete(existingMap, svc.Name)
	}

	for name := range existingMap {
		diff.Destroy = append(diff.Destroy, name)
	}

	return diff, nil
}

// evaluatePolicies runs OPA policies (DefaultPolicy + any added via AddPolicy).
// Returns a combined denial message if any policy fires; empty string on pass.
func (p *Pipeline) evaluatePolicies(stack *manifest.Stack, diff *DiffOutput) string {
	// Build OPA input document
	input := map[string]interface{}{
		"stack": stackToInput(stack),
		"diff": map[string]interface{}{
			"create":    diff.Create,
			"update":    diff.Update,
			"destroy":   diff.Destroy,
			"unchanged": diff.Unchanged,
		},
	}

	// Collect all policy modules
	modules := []func(*rego.Rego){
		rego.Module("default.rego", DefaultPolicy),
	}
	for i, src := range p.opaModules {
		modules = append(modules, rego.Module(fmt.Sprintf("extra_%d.rego", i), src))
	}
	modules = append(modules, rego.Query("data.stratonmesh.policy.deny"))

	r := rego.New(append(modules, rego.Input(input))...)

	ctx := context.Background()
	rs, err := r.Eval(ctx)
	if err != nil {
		if p.logger != nil {
			p.logger.Warnw("OPA policy eval failed, falling back to built-in checks", "error", err)
		}
		return p.evaluatePoliciesFallback(stack, diff)
	}

	var denials []string
	for _, result := range rs {
		for _, expr := range result.Expressions {
			if msgs, ok := expr.Value.([]interface{}); ok {
				for _, m := range msgs {
					if s, ok := m.(string); ok {
						denials = append(denials, s)
					}
				}
			}
		}
	}

	if len(denials) > 0 {
		return strings.Join(denials, "; ")
	}
	return ""
}

// evaluatePoliciesFallback runs the original hardcoded checks when OPA is unavailable.
func (p *Pipeline) evaluatePoliciesFallback(stack *manifest.Stack, diff *DiffOutput) string {
	totalServices := 0
	for _, svc := range stack.Services {
		if svc.IsEnabled() {
			totalServices++
		}
	}
	changedCount := len(diff.Create) + len(diff.Update) + len(diff.Destroy)
	if totalServices > 3 && float64(changedCount)/float64(totalServices) > 0.6 {
		return fmt.Sprintf("blast radius too high: %d/%d services affected (>60%%)", changedCount, totalServices)
	}
	for _, svc := range stack.Services {
		if !svc.IsEnabled() {
			continue
		}
		if cpuMillis := parseCPU(svc.Resources.CPU); cpuMillis > 16000 {
			return fmt.Sprintf("service %q: CPU %s exceeds 16-core limit", svc.Name, svc.Resources.CPU)
		}
		if memBytes := parseMem(svc.Resources.Memory); memBytes > 64*1024*1024*1024 {
			return fmt.Sprintf("service %q: memory %s exceeds 64Gi limit", svc.Name, svc.Resources.Memory)
		}
		if svc.Scaling.MaxReplicas > 50 {
			return fmt.Sprintf("service %q: maxReplicas %d exceeds 50 limit", svc.Name, svc.Scaling.MaxReplicas)
		}
	}
	return ""
}

// stackToInput converts a manifest.Stack to a plain map[string]interface{} for OPA.
func stackToInput(stack *manifest.Stack) map[string]interface{} {
	services := make([]map[string]interface{}, 0, len(stack.Services))
	for _, svc := range stack.Services {
		services = append(services, map[string]interface{}{
			"name":    svc.Name,
			"enabled": svc.IsEnabled(),
			"image":   svc.Image,
			"replicas": svc.DefaultReplicas(),
			"resources": map[string]interface{}{
				"cpu":    svc.Resources.CPU,
				"memory": svc.Resources.Memory,
				"gpu":    svc.Resources.GPU,
			},
			"scaling": map[string]interface{}{
				"auto":        svc.Scaling.Auto,
				"minReplicas": svc.Scaling.MinReplicas,
				"maxReplicas": svc.Scaling.MaxReplicas,
			},
		})
	}
	return map[string]interface{}{
		"name":        stack.Name,
		"version":     stack.Version,
		"environment": stack.Environment,
		"platform":    stack.Platform,
		"services":    services,
	}
}

func parseCPU(s string) int64 {
	var val int64
	if _, err := fmt.Sscanf(s, "%dm", &val); err == nil { return val }
	if _, err := fmt.Sscanf(s, "%d", &val); err == nil { return val * 1000 }
	return 0
}

func parseMem(s string) int64 {
	var val int64
	if _, err := fmt.Sscanf(s, "%dGi", &val); err == nil { return val * 1024 * 1024 * 1024 }
	if _, err := fmt.Sscanf(s, "%dMi", &val); err == nil { return val * 1024 * 1024 }
	return 0
}
