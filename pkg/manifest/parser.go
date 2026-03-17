package manifest

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var varPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// LoadFile reads and parses a StratonMesh manifest from disk.
func LoadFile(path string) (*Stack, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	return Parse(data)
}

// Parse decodes YAML bytes into a Stack.
func Parse(data []byte) (*Stack, error) {
	// The YAML may have a top-level "stack:" key or be flat
	var wrapper struct {
		Stack Stack `yaml:"stack"`
	}
	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		// Try flat format (no wrapper key)
		var flat Stack
		if err2 := yaml.Unmarshal(data, &flat); err2 != nil {
			return nil, fmt.Errorf("parse manifest: %w", err)
		}
		return &flat, nil
	}
	if wrapper.Stack.Name == "" {
		// Was flat after all
		var flat Stack
		if err := yaml.Unmarshal(data, &flat); err != nil {
			return nil, fmt.Errorf("parse manifest: %w", err)
		}
		return &flat, nil
	}
	return &wrapper.Stack, nil
}

// Validate checks a manifest for structural correctness.
func Validate(s *Stack) []string {
	var errs []string

	if s.Name == "" {
		errs = append(errs, "stack name is required")
	}
	if len(s.Services) == 0 {
		errs = append(errs, "at least one service is required")
	}

	names := make(map[string]bool)
	for i, svc := range s.Services {
		if svc.Name == "" {
			errs = append(errs, fmt.Sprintf("service[%d]: name is required", i))
			continue
		}
		if names[svc.Name] {
			errs = append(errs, fmt.Sprintf("service %q: duplicate name", svc.Name))
		}
		names[svc.Name] = true

		if svc.Image == "" && svc.Runtime != "process" {
			errs = append(errs, fmt.Sprintf("service %q: image is required", svc.Name))
		}

		// Validate dependsOn references
		for _, dep := range svc.DependsOn {
			if dep == svc.Name {
				errs = append(errs, fmt.Sprintf("service %q: cannot depend on itself", svc.Name))
			}
		}

		// Validate scaling bounds
		if svc.Scaling.Auto {
			if svc.Scaling.MaxReplicas > 0 && svc.Scaling.MinReplicas > svc.Scaling.MaxReplicas {
				errs = append(errs, fmt.Sprintf("service %q: minReplicas > maxReplicas", svc.Name))
			}
		}
	}

	// Validate dependency graph (no cycles)
	if cycle := detectCycle(s.Services); cycle != "" {
		errs = append(errs, fmt.Sprintf("circular dependency detected: %s", cycle))
	}

	// Validate dependsOn targets exist
	for _, svc := range s.Services {
		for _, dep := range svc.DependsOn {
			if !names[dep] {
				errs = append(errs, fmt.Sprintf("service %q depends on %q which does not exist", svc.Name, dep))
			}
		}
	}

	return errs
}

// Interpolate replaces ${var} references with values from the variables map.
func Interpolate(s *Stack, vars map[string]string) error {
	// Merge stack-level variables with provided vars (provided take precedence)
	merged := make(map[string]string)
	for k, v := range s.Variables {
		merged[k] = v
	}
	for k, v := range vars {
		merged[k] = v
	}

	for i := range s.Services {
		svc := &s.Services[i]
		svc.Image = interpolateStr(svc.Image, merged)
		svc.Command = interpolateStr(svc.Command, merged)

		for k, v := range svc.Env {
			svc.Env[k] = interpolateStr(v, merged)
		}
	}

	return nil
}

// TopologicalSort returns services ordered by dependency (dependencies first).
func TopologicalSort(services []Service) ([]Service, error) {
	graph := make(map[string][]string)
	byName := make(map[string]Service)
	inDegree := make(map[string]int)

	for _, svc := range services {
		if !svc.IsEnabled() {
			continue
		}
		byName[svc.Name] = svc
		inDegree[svc.Name] = 0
		graph[svc.Name] = nil
	}

	for _, svc := range services {
		if !svc.IsEnabled() {
			continue
		}
		for _, dep := range svc.DependsOn {
			if _, ok := byName[dep]; ok {
				graph[dep] = append(graph[dep], svc.Name)
				inDegree[svc.Name]++
			}
		}
	}

	var queue []string
	for name, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}

	var sorted []Service
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		sorted = append(sorted, byName[name])

		for _, dependent := range graph[name] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	if len(sorted) != len(byName) {
		return nil, fmt.Errorf("dependency cycle detected")
	}

	return sorted, nil
}

func interpolateStr(s string, vars map[string]string) string {
	return varPattern.ReplaceAllStringFunc(s, func(match string) string {
		key := strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}")
		// Support default values: ${var ?? default}
		parts := strings.SplitN(key, "??", 2)
		key = strings.TrimSpace(parts[0])
		if val, ok := vars[key]; ok {
			return val
		}
		if len(parts) == 2 {
			return strings.TrimSpace(parts[1])
		}
		return match // Leave unresolved
	})
}

func detectCycle(services []Service) string {
	visited := make(map[string]int) // 0=unvisited, 1=visiting, 2=done
	var path []string

	var visit func(name string) bool
	byName := make(map[string]*Service)
	for i := range services {
		byName[services[i].Name] = &services[i]
	}

	visit = func(name string) bool {
		if visited[name] == 2 {
			return false
		}
		if visited[name] == 1 {
			return true
		}
		visited[name] = 1
		path = append(path, name)

		svc := byName[name]
		if svc != nil {
			for _, dep := range svc.DependsOn {
				if visit(dep) {
					return true
				}
			}
		}

		path = path[:len(path)-1]
		visited[name] = 2
		return false
	}

	for _, svc := range services {
		if visit(svc.Name) {
			return strings.Join(path, " → ")
		}
	}
	return ""
}
