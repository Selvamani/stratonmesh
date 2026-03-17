package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/stratonmesh/stratonmesh/pkg/manifest"
	"github.com/stratonmesh/stratonmesh/pkg/store"
	"go.uber.org/zap"
)

// SizeProfile maps a T-shirt size to concrete resource allocations.
// Services with no explicit resources get these defaults when instantiated.
type SizeProfile struct {
	Name        string `yaml:"name" json:"name"`               // S, M, L, XL
	CPU         string `yaml:"cpu" json:"cpu"`                 // e.g. "250m"
	Memory      string `yaml:"memory" json:"memory"`           // e.g. "256Mi"
	Replicas    int    `yaml:"replicas" json:"replicas"`       // default replica count
	MaxReplicas int    `yaml:"maxReplicas" json:"maxReplicas"` // max for auto-scaling
}

// DefaultProfiles are the built-in size profiles.
var DefaultProfiles = map[string]SizeProfile{
	"XS": {Name: "XS", CPU: "100m", Memory: "128Mi", Replicas: 1, MaxReplicas: 2},
	"S":  {Name: "S", CPU: "250m", Memory: "256Mi", Replicas: 1, MaxReplicas: 4},
	"M":  {Name: "M", CPU: "500m", Memory: "512Mi", Replicas: 2, MaxReplicas: 8},
	"L":  {Name: "L", CPU: "1000m", Memory: "1Gi", Replicas: 3, MaxReplicas: 16},
	"XL": {Name: "XL", CPU: "2000m", Memory: "2Gi", Replicas: 5, MaxReplicas: 32},
}

// InstantiateRequest describes how to create a concrete Stack from a Blueprint.
type InstantiateRequest struct {
	// BlueprintName is the catalog entry to use.
	BlueprintName string `json:"blueprintName"`
	// InstanceName overrides the stack name (default: blueprint name).
	InstanceName string `json:"instanceName,omitempty"`
	// Size applies a SizeProfile to all services that have no explicit resources.
	Size string `json:"size,omitempty"` // XS, S, M, L, XL
	// Parameters replaces {{param}} tokens in the manifest template.
	Parameters map[string]string `json:"parameters,omitempty"`
	// Environment sets the target environment.
	Environment string `json:"environment,omitempty"`
	// Platform overrides the platform in the blueprint.
	Platform string `json:"platform,omitempty"`
	// DeployFile is a path relative to the repo root for a specific deploy file.
	// Empty = auto-detect. Only relevant for repo-mode blueprints.
	DeployFile string `json:"deployFile,omitempty"`
}

// InstantiateResult is the output of Instantiate.
type InstantiateResult struct {
	Stack          *manifest.Stack   `json:"stack"`
	BlueprintName  string            `json:"blueprintName"`
	Size           string            `json:"size"`
	AppliedProfile *SizeProfile      `json:"appliedProfile,omitempty"`
	ParameterCount int               `json:"parameterCount"`
}

// Engine orchestrates blueprint retrieval and stack instantiation.
type Engine struct {
	store   *store.Store
	logger  *zap.SugaredLogger
	profiles map[string]SizeProfile
}

// New creates a catalog Engine with the default size profiles.
func New(st *store.Store, logger *zap.SugaredLogger) *Engine {
	profiles := make(map[string]SizeProfile, len(DefaultProfiles))
	for k, v := range DefaultProfiles {
		profiles[k] = v
	}
	return &Engine{store: st, logger: logger, profiles: profiles}
}

// AddProfile registers a custom size profile (overrides built-ins with the same name).
func (e *Engine) AddProfile(p SizeProfile) {
	e.profiles[strings.ToUpper(p.Name)] = p
}

// GetProfile returns a size profile by name (case-insensitive).
func (e *Engine) GetProfile(name string) (SizeProfile, bool) {
	p, ok := e.profiles[strings.ToUpper(name)]
	return p, ok
}

// ListProfiles returns all registered size profiles.
func (e *Engine) ListProfiles() []SizeProfile {
	out := make([]SizeProfile, 0, len(e.profiles))
	for _, p := range e.profiles {
		out = append(out, p)
	}
	return out
}

// Instantiate retrieves a blueprint from the catalog, applies size profiles and
// parameters, and returns a concrete ready-to-deploy Stack.
func (e *Engine) Instantiate(ctx context.Context, req InstantiateRequest) (*InstantiateResult, error) {
	// 1. Fetch blueprint
	bp, err := e.store.GetBlueprint(ctx, req.BlueprintName)
	if err != nil {
		return nil, fmt.Errorf("blueprint %q not found: %w", req.BlueprintName, err)
	}

	// 2. Deserialise the manifest template
	stack, err := blueprintToStack(bp)
	if err != nil {
		return nil, fmt.Errorf("deserialise blueprint manifest: %w", err)
	}

	// 3. Apply instance name
	if req.InstanceName != "" {
		stack.Name = req.InstanceName
	}

	// 4. Apply environment / platform overrides
	if req.Environment != "" {
		stack.Environment = req.Environment
	}
	if req.Platform != "" {
		stack.Platform = req.Platform
	}

	// 5. Apply size profile to services without explicit resources
	var appliedProfile *SizeProfile
	if req.Size != "" {
		profile, ok := e.GetProfile(req.Size)
		if !ok {
			return nil, fmt.Errorf("unknown size profile %q (valid: XS S M L XL)", req.Size)
		}
		appliedProfile = &profile
		for i := range stack.Services {
			svc := &stack.Services[i]
			if svc.Resources.CPU == "" {
				svc.Resources.CPU = profile.CPU
			}
			if svc.Resources.Memory == "" {
				svc.Resources.Memory = profile.Memory
			}
			if svc.Replicas == 0 {
				svc.Replicas = profile.Replicas
			}
			if svc.Scaling.Auto && svc.Scaling.MaxReplicas == 0 {
				svc.Scaling.MaxReplicas = profile.MaxReplicas
			}
			if svc.Scaling.Auto && svc.Scaling.MinReplicas == 0 {
				svc.Scaling.MinReplicas = profile.Replicas
			}
		}
	}

	// 6. Interpolate parameters (both {{key}} and ${key} styles)
	if len(req.Parameters) > 0 {
		if err := manifest.Interpolate(stack, req.Parameters); err != nil {
			return nil, fmt.Errorf("interpolate parameters: %w", err)
		}
		// Also replace {{key}} style tokens
		applyHandlebars(stack, req.Parameters)
	}

	// 7. Merge blueprint parameters as defaults for anything not yet resolved
	if bp.Parameters != nil && len(req.Parameters) < len(bp.Parameters) {
		defaults := make(map[string]string)
		for k, v := range bp.Parameters {
			if _, set := req.Parameters[k]; !set {
				defaults[k] = fmt.Sprint(v)
			}
		}
		manifest.Interpolate(stack, defaults)
		applyHandlebars(stack, defaults)
	}

	// 8. Stamp metadata
	stack.Metadata.ResolvedAt = time.Now()
	if stack.Version == "" {
		stack.Version = bp.Version
	}

	e.logger.Infow("blueprint instantiated",
		"blueprint", req.BlueprintName,
		"instance", stack.Name,
		"size", req.Size,
		"services", len(stack.Services),
	)

	return &InstantiateResult{
		Stack:          stack,
		BlueprintName:  req.BlueprintName,
		Size:           req.Size,
		AppliedProfile: appliedProfile,
		ParameterCount: len(req.Parameters),
	}, nil
}

// Publish saves a Stack as a new blueprint version in the catalog.
func (e *Engine) Publish(ctx context.Context, stack *manifest.Stack, source, gitURL string) error {
	bp := store.Blueprint{
		Name:        stack.Name,
		Version:     stack.Version,
		Source:      source,
		GitURL:      gitURL,
		Description: fmt.Sprintf("Published from manifest %s v%s", stack.Name, stack.Version),
		Manifest:    stack,
		Parameters:  extractParameters(stack),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := e.store.SaveBlueprint(ctx, bp); err != nil {
		return fmt.Errorf("save blueprint: %w", err)
	}
	e.logger.Infow("blueprint published", "name", bp.Name, "version", bp.Version)
	return nil
}

// --- helpers ---

func blueprintToStack(bp *store.Blueprint) (*manifest.Stack, error) {
	data, err := json.Marshal(bp.Manifest)
	if err != nil {
		return nil, err
	}
	return manifest.Parse(data)
}

// applyHandlebars replaces {{key}} style tokens in service image, command, and env.
func applyHandlebars(stack *manifest.Stack, params map[string]string) {
	for i := range stack.Services {
		svc := &stack.Services[i]
		svc.Image = replaceTokens(svc.Image, params)
		svc.Command = replaceTokens(svc.Command, params)
		for k, v := range svc.Env {
			svc.Env[k] = replaceTokens(v, params)
		}
	}
}

func replaceTokens(s string, params map[string]string) string {
	for k, v := range params {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}

// extractParameters scans a stack for ${var} and {{var}} tokens and returns
// a map of parameter name → empty string (documenting required inputs).
func extractParameters(stack *manifest.Stack) map[string]interface{} {
	params := make(map[string]interface{})
	for _, svc := range stack.Services {
		scanTokens(svc.Image, params)
		scanTokens(svc.Command, params)
		for _, v := range svc.Env {
			scanTokens(v, params)
		}
	}
	return params
}

func scanTokens(s string, out map[string]interface{}) {
	for _, style := range []struct{ open, close string }{{"${", "}"}, {"{{", "}}"}} {
		for {
			start := strings.Index(s, style.open)
			if start == -1 {
				break
			}
			end := strings.Index(s[start:], style.close)
			if end == -1 {
				break
			}
			token := s[start+len(style.open) : start+end]
			if token != "" && !strings.HasPrefix(token, "vault:") {
				out[token] = ""
			}
			s = s[start+end+len(style.close):]
		}
	}
}
