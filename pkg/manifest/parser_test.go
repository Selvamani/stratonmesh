package manifest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stratonmesh/stratonmesh/pkg/manifest"
)

func TestParse_ValidManifest(t *testing.T) {
	src := `
name: myapp
version: "1.0"
platform: docker
services:
  - name: api
    image: myapp/api:latest
    replicas: 2
    ports:
      - expose: 8080
    resources:
      cpu: "500m"
      memory: "512Mi"
`
	stack, err := manifest.Parse([]byte(src))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if stack.Name != "myapp" {
		t.Errorf("name = %q, want %q", stack.Name, "myapp")
	}
	if len(stack.Services) != 1 {
		t.Fatalf("services count = %d, want 1", len(stack.Services))
	}
	if stack.Services[0].Replicas != 2 {
		t.Errorf("replicas = %d, want 2", stack.Services[0].Replicas)
	}
}

func TestParse_EmptyYAML(t *testing.T) {
	_, err := manifest.Parse([]byte(""))
	// An empty YAML yields an empty struct — Validate will catch it
	if err != nil {
		t.Fatalf("unexpected error on empty YAML: %v", err)
	}
}

func TestValidate_MissingName(t *testing.T) {
	stack := &manifest.Stack{
		Services: []manifest.Service{{Name: "svc", Image: "img"}},
	}
	errs := manifest.Validate(stack)
	if len(errs) == 0 {
		t.Fatal("expected validation error for missing name, got none")
	}
}

func TestValidate_MissingServices(t *testing.T) {
	stack := &manifest.Stack{Name: "app", Version: "1.0"}
	errs := manifest.Validate(stack)
	if len(errs) == 0 {
		t.Fatal("expected validation error for empty services, got none")
	}
}

func TestValidate_DuplicateServiceNames(t *testing.T) {
	stack := &manifest.Stack{
		Name:    "app",
		Version: "1.0",
		Services: []manifest.Service{
			{Name: "svc", Image: "img"},
			{Name: "svc", Image: "img2"},
		},
	}
	errs := manifest.Validate(stack)
	found := false
	for _, e := range errs {
		if contains(e.Error(), "duplicate") || contains(e.Error(), "svc") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected duplicate-service error, got: %v", errs)
	}
}

func TestInterpolate(t *testing.T) {
	stack := &manifest.Stack{
		Name:    "app",
		Version: "1.0",
		Services: []manifest.Service{
			{Name: "api", Image: "myapp:${TAG}"},
		},
	}
	if err := manifest.Interpolate(stack, map[string]string{"TAG": "v2"}); err != nil {
		t.Fatalf("interpolate: %v", err)
	}
	if stack.Services[0].Image != "myapp:v2" {
		t.Errorf("image = %q, want %q", stack.Services[0].Image, "myapp:v2")
	}
}

func TestTopologicalSort_NoCycle(t *testing.T) {
	services := []manifest.Service{
		{Name: "c", Image: "c", DependsOn: []string{"b"}},
		{Name: "a", Image: "a"},
		{Name: "b", Image: "b", DependsOn: []string{"a"}},
	}
	sorted, err := manifest.TopologicalSort(services)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sorted) != 3 {
		t.Fatalf("want 3 services, got %d", len(sorted))
	}
	// a must precede b, b must precede c
	pos := make(map[string]int)
	for i, s := range sorted {
		pos[s.Name] = i
	}
	if pos["a"] > pos["b"] {
		t.Errorf("a should come before b in sorted order, got pos a=%d b=%d", pos["a"], pos["b"])
	}
	if pos["b"] > pos["c"] {
		t.Errorf("b should come before c in sorted order, got pos b=%d c=%d", pos["b"], pos["c"])
	}
}

func TestTopologicalSort_Cycle(t *testing.T) {
	services := []manifest.Service{
		{Name: "a", Image: "a", DependsOn: []string{"b"}},
		{Name: "b", Image: "b", DependsOn: []string{"a"}},
	}
	_, err := manifest.TopologicalSort(services)
	if err == nil {
		t.Fatal("expected cycle detection error, got nil")
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	content := `
name: testapp
version: "0.1"
services:
  - name: web
    image: nginx:latest
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	stack, err := manifest.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if stack.Name != "testapp" {
		t.Errorf("name = %q, want testapp", stack.Name)
	}
}

func TestInferType(t *testing.T) {
	tests := []struct {
		name string
		svc  manifest.Service
		want manifest.WorkloadType
	}{
		{"explicit long-running", manifest.Service{Type: manifest.WorkloadLongRunning}, manifest.WorkloadLongRunning},
		{"scheduled via cron", manifest.Service{Schedule: "0 * * * *"}, manifest.WorkloadScheduled},
		{"stateful via volume", manifest.Service{Volumes: []manifest.VolumeSpec{{Name: "data"}}}, manifest.WorkloadStateful},
		{"default inferred", manifest.Service{Image: "nginx"}, manifest.WorkloadLongRunning},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.svc.InferType()
			if got != tc.want {
				t.Errorf("InferType() = %q, want %q", got, tc.want)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
