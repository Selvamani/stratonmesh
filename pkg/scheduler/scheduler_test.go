package scheduler_test

import (
	"testing"

	"github.com/selvamani/stratonmesh/pkg/manifest"
	"github.com/selvamani/stratonmesh/pkg/scheduler"
	"github.com/selvamani/stratonmesh/pkg/store"
)

func TestDefaultWeights(t *testing.T) {
	w := scheduler.DefaultWeights()
	total := w.BinPack + w.Spread + w.Affinity + w.Cost + w.Locality
	if total != 100 {
		t.Errorf("default weights sum = %.1f, want 100", total)
	}
}

func TestPlacementRequest_Fields(t *testing.T) {
	svc := &manifest.Service{Name: "api", Image: "api:latest"}
	req := scheduler.PlacementRequest{
		StackID: "mystack",
		Service: svc,
		Region:  "us-east-1",
	}
	if req.StackID != "mystack" {
		t.Errorf("StackID = %q, want mystack", req.StackID)
	}
	if req.Service.Name != "api" {
		t.Errorf("Service.Name = %q, want api", req.Service.Name)
	}
}

func TestNodeScore_Struct(t *testing.T) {
	ns := scheduler.NodeScore{
		Node:  store.NodeInfo{ID: "n1", Name: "node-1"},
		Total: 75.5,
	}
	if ns.Total != 75.5 {
		t.Errorf("Total = %.1f, want 75.5", ns.Total)
	}
}

func TestNew_WithCustomWeights(t *testing.T) {
	// Just verify New accepts custom weights without panic
	w := scheduler.ScoringWeights{
		BinPack:  50,
		Spread:   50,
	}
	_ = scheduler.New(nil, nil, w)
}
