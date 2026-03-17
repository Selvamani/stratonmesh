package pipeline_test

import (
	"fmt"
	"testing"

	"github.com/stratonmesh/stratonmesh/pkg/manifest"
	"github.com/stratonmesh/stratonmesh/pkg/pipeline"
)

// newTestPipeline creates a Pipeline without a real store or bus (nil-safe for unit tests).
func newTestPipeline() *pipeline.Pipeline {
	return pipeline.New(nil, nil, nil)
}

func TestDefaultPolicy_BlastRadius(t *testing.T) {
	pl := newTestPipeline()

	// 5 services total, 4 changed → 80% > 60% → should be denied
	stack := makeStack(5)
	diff := &pipeline.DiffOutput{
		Create: []string{"svc1", "svc2", "svc3", "svc4"},
	}
	result := pl.EvaluatePoliciesPublic(stack, diff)
	if result == "" {
		t.Error("expected blast-radius denial, got empty string")
	}
}

func TestDefaultPolicy_BlastRadius_OK(t *testing.T) {
	pl := newTestPipeline()

	// 5 services, 2 changed → 40% < 60% → should pass
	stack := makeStack(5)
	diff := &pipeline.DiffOutput{
		Update: []string{"svc1", "svc2"},
	}
	result := pl.EvaluatePoliciesPublic(stack, diff)
	if result != "" {
		t.Errorf("unexpected policy denial: %s", result)
	}
}

func TestDefaultPolicy_SmallStack_NoBlastRadiusCheck(t *testing.T) {
	pl := newTestPipeline()

	// 3 services total — blast radius check is skipped (threshold is >3)
	stack := makeStack(3)
	diff := &pipeline.DiffOutput{
		Create: []string{"svc1", "svc2", "svc3"},
	}
	result := pl.EvaluatePoliciesPublic(stack, diff)
	if result != "" {
		t.Errorf("unexpected denial for small stack: %s", result)
	}
}

func TestDefaultPolicy_CPULimit(t *testing.T) {
	pl := newTestPipeline()

	stack := &manifest.Stack{
		Name:    "app",
		Version: "1.0",
		Services: []manifest.Service{
			{Name: "big", Image: "big:latest", Resources: manifest.ResourceSpec{CPU: "20000m"}},
		},
	}
	diff := &pipeline.DiffOutput{Create: []string{"big"}}
	result := pl.EvaluatePoliciesPublic(stack, diff)
	if result == "" {
		t.Error("expected CPU-limit denial, got empty string")
	}
}

func TestDefaultPolicy_ReplicaLimit(t *testing.T) {
	pl := newTestPipeline()

	stack := &manifest.Stack{
		Name:    "app",
		Version: "1.0",
		Services: []manifest.Service{
			{Name: "worker", Image: "w:latest", Scaling: manifest.ScalingSpec{MaxReplicas: 100}},
		},
	}
	diff := &pipeline.DiffOutput{Create: []string{"worker"}}
	result := pl.EvaluatePoliciesPublic(stack, diff)
	if result == "" {
		t.Error("expected replica-limit denial, got empty string")
	}
}

func TestCustomOPAPolicy(t *testing.T) {
	pl := newTestPipeline()
	pl.AddPolicy(`
package stratonmesh.policy
import future.keywords.contains
deny contains msg if {
    input.stack.environment == "production"
    input.stack.platform == "docker"
    msg := "production deployments to docker are not allowed"
}
`)
	stack := &manifest.Stack{
		Name:        "app",
		Version:     "1.0",
		Environment: "production",
		Platform:    "docker",
		Services:    []manifest.Service{{Name: "svc", Image: "img"}},
	}
	diff := &pipeline.DiffOutput{Create: []string{"svc"}}
	result := pl.EvaluatePoliciesPublic(stack, diff)
	if result == "" {
		t.Error("expected custom policy denial, got empty string")
	}
}

// --- helpers ---

func makeStack(n int) *manifest.Stack {
	svcs := make([]manifest.Service, n)
	for i := range svcs {
		svcs[i] = manifest.Service{
			Name:  fmt.Sprintf("svc%d", i+1),
			Image: "img:latest",
		}
	}
	return &manifest.Stack{Name: "app", Version: "1.0", Services: svcs}
}
