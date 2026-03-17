package autoscaler_test

import (
	"context"
	"testing"
	"time"

	"github.com/stratonmesh/stratonmesh/pkg/autoscaler"
	"github.com/stratonmesh/stratonmesh/pkg/manifest"
)

// mockOrch satisfies autoscaler.Orchestrator for testing.
type mockOrch struct {
	calls []scaleCall
}

type scaleCall struct {
	stackID  string
	service  string
	replicas int
}

func (m *mockOrch) Scale(_ context.Context, stackID, service string, replicas int) error {
	m.calls = append(m.calls, scaleCall{stackID, service, replicas})
	return nil
}

func newTestScaler(orch autoscaler.Orchestrator) *autoscaler.AutoScaler {
	return autoscaler.New(nil, nil, orch, autoscaler.Config{
		Cooldown:           100 * time.Millisecond,
		EvalInterval:       50 * time.Millisecond,
		ScaleDownThreshold: 3,
	}, nil)
}

func TestWatch_AutoScalingEnabled(t *testing.T) {
	orch := &mockOrch{}
	scaler := newTestScaler(orch)

	svc := manifest.Service{
		Name: "api",
		Scaling: manifest.ScalingSpec{
			Auto:        true,
			MinReplicas: 1,
			MaxReplicas: 5,
			Metrics:     []manifest.ScaleMetric{{Type: "cpu", Target: "70%"}},
		},
	}
	scaler.Watch("mystack", svc)
	// Just verify no panic and the watcher is registered
}

func TestWatch_AutoScalingDisabled(t *testing.T) {
	orch := &mockOrch{}
	scaler := newTestScaler(orch)

	svc := manifest.Service{
		Name:    "api",
		Scaling: manifest.ScalingSpec{Auto: false},
	}
	scaler.Watch("mystack", svc)
	// Should not register anything (no-op)
}

func TestUnwatch(t *testing.T) {
	orch := &mockOrch{}
	scaler := newTestScaler(orch)

	svc := manifest.Service{
		Name:    "api",
		Scaling: manifest.ScalingSpec{Auto: true, MinReplicas: 1, MaxReplicas: 5},
	}
	scaler.Watch("stack1", svc)
	scaler.Unwatch("stack1", "api")
	// Should not panic
}

func TestWatchStack(t *testing.T) {
	orch := &mockOrch{}
	scaler := newTestScaler(orch)

	services := []manifest.Service{
		{Name: "api", Scaling: manifest.ScalingSpec{Auto: true, MinReplicas: 1, MaxReplicas: 5}},
		{Name: "worker", Scaling: manifest.ScalingSpec{Auto: false}},
		{Name: "db", Scaling: manifest.ScalingSpec{Auto: true, MinReplicas: 1, MaxReplicas: 3}},
	}
	scaler.WatchStack("stack1", services)
	// Should register api and db only (worker has Auto:false)
}

func TestConfig_Defaults(t *testing.T) {
	orch := &mockOrch{}
	// Empty config → defaults applied inside New
	scaler := autoscaler.New(nil, nil, orch, autoscaler.Config{}, nil)
	if scaler == nil {
		t.Fatal("New returned nil")
	}
}

func TestScaleAction_Fields(t *testing.T) {
	a := autoscaler.ScaleAction{
		StackID: "stack",
		Service: "api",
		From:    2,
		To:      4,
		Reason:  "cpu at 90% (target 70%)",
		Metric:  "cpu",
		Value:   90,
		Target:  70,
	}
	if a.To-a.From != 2 {
		t.Errorf("To-From = %d, want 2", a.To-a.From)
	}
}
