package importer

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/selvamani/stratonmesh/pkg/manifest"
	"github.com/selvamani/stratonmesh/pkg/store"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// Importer handles Git-based stack onboarding.
type Importer struct {
	store    *store.Store
	logger   *zap.SugaredLogger
	ReposDir string // default clone directory; overridden per-request by ImportRequest.ReposDir
}

const (
	// ImportModeCatalog clones the repo, parses metadata into a blueprint, then deletes the clone.
	// Services that reference built images must have those images pre-built.
	ImportModeCatalog = "catalog"
	// ImportModeRepo clones the repo and keeps it on disk under ReposDir.
	// The compose adapter will run `docker compose build` from the local source.
	ImportModeRepo = "repo"
	// ImportModeAI clones the repo, sends its structure to Claude for analysis,
	// and generates a StratonMesh manifest automatically. Falls back gracefully
	// when no ANTHROPIC_API_KEY is set or Claude cannot produce a manifest.
	ImportModeAI = "ai"

	// DefaultReposDir is where repo-mode clones are stored.
	DefaultReposDir = "/var/lib/stratonmesh/repos"
)

// ImportRequest describes what to import from Git.
type ImportRequest struct {
	GitURL    string `json:"gitUrl"`
	Branch    string `json:"branch,omitempty"` // default: main/master
	Tag       string `json:"tag,omitempty"`
	Path      string `json:"path,omitempty"`     // subdirectory within the repo
	Name      string `json:"name,omitempty"`     // blueprint name (auto-detected if empty)
	SSHKey    string `json:"sshKey,omitempty"`
	AuthToken string `json:"authToken,omitempty"`
	// Mode controls whether the cloned repo is kept on disk (repo) or discarded (catalog).
	Mode      string `json:"mode,omitempty"`     // "catalog" (default) or "repo"
	// ReposDir overrides DefaultReposDir for repo-mode storage.
	ReposDir  string `json:"reposDir,omitempty"`
}

// ImportResult describes what was found and generated.
type ImportResult struct {
	Blueprint    store.Blueprint      `json:"blueprint"`
	Format       string               `json:"format"`    // docker-compose, helm, kubernetes, stratonmesh, dockerfile
	Services     int                  `json:"services"`
	Volumes      int                  `json:"volumes"`
	Parameters   int                  `json:"parameters"`
	Classifications map[string]string `json:"classifications"` // service -> archetype
}

// DetectedFormat describes a stack definition found in a repo.
type DetectedFormat struct {
	Format   string // docker-compose, helm, kubernetes, terraform, stratonmesh, dockerfile
	FilePath string
	Priority int // lower = higher priority
}

// New creates an Importer.
func New(st *store.Store, logger *zap.SugaredLogger) *Importer {
	return &Importer{store: st, logger: logger}
}

// Import clones a Git repo, scans for stack definitions, and generates a blueprint.
//
// Two modes:
//   - catalog (default): clone → parse → save metadata → delete clone.
//     Services with build: directives get a synthesised image name; images must be pre-built.
//   - repo: clone → parse → save metadata → keep clone at {ReposDir}/{name}.
//     The compose adapter will run `docker compose build` from local source on deploy.
// nameFromGitURL derives a blueprint name from a Git URL.
// "https://github.com/org/example-voting-app.git" → "example-voting-app"
func nameFromGitURL(u string) string {
	u = strings.TrimSuffix(u, ".git")
	parts := strings.Split(strings.Trim(u, "/"), "/")
	name := strings.ToLower(parts[len(parts)-1])
	// Replace non-alphanumeric chars (except hyphen) with hyphen
	var out []byte
	for _, c := range []byte(name) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			out = append(out, c)
		} else {
			out = append(out, '-')
		}
	}
	return strings.Trim(string(out), "-")
}

func (imp *Importer) Import(ctx context.Context, req ImportRequest) (*ImportResult, error) {
	if req.Mode == "" {
		req.Mode = ImportModeCatalog
	}

	// Determine the blueprint name up front so parsers can use it for image synthesis.
	// Priority: explicit req.Name > git URL basename
	if req.Name == "" && req.GitURL != "" {
		req.Name = nameFromGitURL(req.GitURL)
	}

	// Clone the repo to a temp directory
	tmpDir, err := os.MkdirTemp("", "sm-import-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	// cleanup is set to false for repo mode after we move the dir to its final location.
	cleanup := true
	defer func() {
		if cleanup {
			os.RemoveAll(tmpDir)
		}
	}()

	imp.logger.Infow("cloning repository", "url", req.GitURL, "branch", req.Branch)

	cloneOpts := &git.CloneOptions{
		URL:   req.GitURL,
		Depth: 1,
	}
	if req.Tag != "" {
		cloneOpts.ReferenceName = plumbing.NewTagReferenceName(req.Tag)
	} else if req.Branch != "" {
		cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(req.Branch)
	}

	repo, err := git.PlainCloneContext(ctx, tmpDir, false, cloneOpts)
	if err != nil && cloneOpts.ReferenceName != "" {
		// Branch not found — retry without a branch hint to get the default branch.
		imp.logger.Warnw("branch not found, retrying with default branch",
			"url", req.GitURL, "branch", req.Branch, "error", err)
		cloneOpts.ReferenceName = ""
		repo, err = git.PlainCloneContext(ctx, tmpDir, false, cloneOpts)
	}
	if err != nil {
		return nil, fmt.Errorf("git clone %s: %w", req.GitURL, err)
	}

	// Get the HEAD commit SHA
	head, _ := repo.Head()
	gitSHA := ""
	if head != nil {
		gitSHA = head.Hash().String()[:7]
	}

	imp.logger.Infow("cloned successfully", "sha", gitSHA)

	// Determine the scan directory
	scanDir := tmpDir
	if req.Path != "" {
		scanDir = filepath.Join(tmpDir, req.Path)
	}

	// Scan for recognizable formats — root first, then recurse into subdirs.
	formats := imp.detectFormats(scanDir)
	if len(formats) == 0 {
		formats = imp.detectFormatsDeep(scanDir, 3)
	}
	if len(formats) == 0 {
		return nil, fmt.Errorf("no recognizable stack definition found in %s (checked root and subdirectories up to depth 3); try specifying a path with the 'path' field", req.GitURL)
	}

	// Pick the highest-priority format
	best := formats[0]
	for _, f := range formats[1:] {
		if f.Priority < best.Priority {
			best = f
		}
	}

	// AI mode: use Claude to analyze the repo and generate a StratonMesh manifest.
	// On failure (no API key, quota, etc.) fall back to the best detected format.
	var stack *manifest.Stack
	if req.Mode == ImportModeAI {
		imp.logger.Infow("AI import: analyzing repo with Claude", "url", req.GitURL)
		aiStack, aiErr := analyzeRepoWithAI(ctx, scanDir, req.Name, imp.logger)
		if aiErr != nil {
			imp.logger.Warnw("AI analysis failed, falling back to format detection",
				"error", aiErr, "format", best.Format)
		} else {
			stack = aiStack
			imp.logger.Infow("AI analysis succeeded", "services", len(aiStack.Services))
		}
	}

	// Compute the deploy file path relative to the repo root.
	// If the file was found in a subdirectory (deep scan), this records exactly
	// which file to use at deploy time so the adapter doesn't have to re-search.
	bestRelPath, _ := filepath.Rel(tmpDir, best.FilePath)

	if stack == nil {
		imp.logger.Infow("detected format", "format", best.Format, "file", best.FilePath)
		switch best.Format {
		case "stratonmesh":
			stack, err = imp.parseStratonMesh(best.FilePath)
		case "docker-compose":
			stack, err = imp.parseDockerCompose(best.FilePath, req.Name)
		case "helm":
			stack, err = imp.parseHelmChart(filepath.Dir(best.FilePath))
		case "kubernetes":
			stack, err = imp.parseKubernetesManifests(filepath.Dir(best.FilePath))
		case "dockerfile":
			stack, err = imp.parseDockerfile(best.FilePath, req.Name)
		default:
			return nil, fmt.Errorf("unsupported format: %s", best.Format)
		}
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", best.Format, err)
		}
	}

	// Override name if provided; ensure name is never empty
	if req.Name != "" {
		stack.Name = req.Name
	}
	if stack.Name == "" {
		stack.Name = nameFromGitURL(req.GitURL)
	}

	// Store the detected deploy file path (relative to repo root) in metadata.
	// This is used by the compose adapter at deploy time to locate the right file
	// even when it lives in a subdirectory rather than the repo root.
	if bestRelPath != "" && bestRelPath != "." {
		stack.Metadata.DeployFile = bestRelPath
	}

	// Auto-classify workload types
	classifications := make(map[string]string)
	for i := range stack.Services {
		svc := &stack.Services[i]
		if svc.Type == "" {
			svc.Type = svc.InferType()
		}
		classifications[svc.Name] = string(svc.Type)
	}

	// Count volumes and extract parameters
	volCount := 0
	for _, svc := range stack.Services {
		volCount += len(svc.Volumes)
	}

	// For repo and AI modes: move the clone to a persistent directory so the compose
	// adapter can build images from source at deploy time.
	var localPath string
	if req.Mode == ImportModeRepo || req.Mode == ImportModeAI {
		reposDir := req.ReposDir
		if reposDir == "" {
			reposDir = imp.ReposDir
		}
		if reposDir == "" {
			reposDir = DefaultReposDir
		}
		localPath = filepath.Join(reposDir, stack.Name)
		if err := os.MkdirAll(reposDir, 0755); err != nil {
			return nil, fmt.Errorf("create repos dir: %w", err)
		}
		// Remove any previous clone for this name.
		os.RemoveAll(localPath)
		if err := os.Rename(tmpDir, localPath); err != nil {
			// os.Rename fails across filesystems (e.g. /tmp → /var). Fall back to copy.
			if err2 := copyDir(tmpDir, localPath); err2 != nil {
				return nil, fmt.Errorf("persist repo to %s: %w", localPath, err2)
			}
		}
		cleanup = false // dir was moved or copied; original tmpDir no longer exists
		stack.Metadata.RepoPath = localPath
		imp.logger.Infow("repo kept on disk", "path", localPath)
	}

	// Build the blueprint
	bp := store.Blueprint{
		Name:        stack.Name,
		Version:     stack.Version,
		Source:      best.Format,
		ImportMode:  req.Mode,
		LocalPath:   localPath,
		GitURL:      req.GitURL,
		GitBranch:   req.Branch,
		GitPath:     req.Path,
		GitSHA:      gitSHA,
		Manifest:    stack,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	// Save to catalog
	if err := imp.store.SaveBlueprint(ctx, bp); err != nil {
		return nil, fmt.Errorf("save blueprint: %w", err)
	}

	result := &ImportResult{
		Blueprint:       bp,
		Format:          best.Format,
		Services:        len(stack.Services),
		Volumes:         volCount,
		Parameters:      len(stack.Variables),
		Classifications: classifications,
	}

	imp.logger.Infow("import completed",
		"name", stack.Name,
		"format", best.Format,
		"services", len(stack.Services),
		"volumes", volCount,
	)

	return result, nil
}

// detectFormats scans a directory for recognizable stack definition files.
func (imp *Importer) detectFormats(dir string) []DetectedFormat {
	var found []DetectedFormat

	// Priority order (lower = higher priority)
	checks := []struct {
		pattern  string
		format   string
		priority int
	}{
		{"stratonmesh.yaml", "stratonmesh", 1},
		{"stratonmesh.yml", "stratonmesh", 1},
		{"docker-compose.yml", "docker-compose", 2},
		{"docker-compose.yaml", "docker-compose", 2},
		{"compose.yml", "docker-compose", 2},
		{"compose.yaml", "docker-compose", 2},
		{"Chart.yaml", "helm", 3},
		{"Dockerfile", "dockerfile", 6},
	}

	for _, check := range checks {
		path := filepath.Join(dir, check.pattern)
		if _, err := os.Stat(path); err == nil {
			found = append(found, DetectedFormat{
				Format:   check.format,
				FilePath: path,
				Priority: check.priority,
			})
		}
	}

	// Check for Kubernetes manifests (look for files with apiVersion)
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if (strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")) && name != "Chart.yaml" {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			if strings.Contains(string(data), "apiVersion:") && strings.Contains(string(data), "kind:") {
				found = append(found, DetectedFormat{
					Format:   "kubernetes",
					FilePath: filepath.Join(dir, name),
					Priority: 4,
				})
				break // one match is enough
			}
		}
	}

	return found
}

// ParseRepoFile parses a specific deploy file from an already-cloned repo and
// returns the resulting Stack. repoName is used as the blueprint name hint;
// filePath is relative to repoPath.
//
// Supported file types: stratonmesh.yaml, docker-compose*.yml/yaml,
// Chart.yaml (Helm), *.yaml with apiVersion/kind (Kubernetes), Dockerfile.
//
// Call this to preview or re-generate a manifest from any file in the repo
// without re-cloning (the repo must already be on disk at repoPath).
func (imp *Importer) ParseRepoFile(repoName, repoPath, filePath string) (*manifest.Stack, error) {
	absPath := filepath.Join(repoPath, filePath)
	if _, err := os.Stat(absPath); err != nil {
		return nil, fmt.Errorf("file not found in repo: %s", filePath)
	}
	base := filepath.Base(filePath)
	dir := filepath.Dir(absPath)

	switch {
	case base == "stratonmesh.yaml" || base == "stratonmesh.yml":
		return imp.parseStratonMesh(absPath)
	case strings.HasPrefix(base, "docker-compose") || strings.HasPrefix(base, "compose"):
		if strings.HasSuffix(base, ".yml") || strings.HasSuffix(base, ".yaml") {
			return imp.parseDockerCompose(absPath, repoName)
		}
	case base == "Chart.yaml":
		return imp.parseHelmChart(dir)
	case base == "Dockerfile" || strings.HasPrefix(base, "Dockerfile."):
		return imp.parseDockerfile(absPath, repoName)
	case strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml"):
		data, err := os.ReadFile(absPath)
		if err != nil {
			return nil, fmt.Errorf("read file: %w", err)
		}
		if strings.Contains(string(data), "apiVersion:") && strings.Contains(string(data), "kind:") {
			return imp.parseKubernetesManifests(dir)
		}
		return nil, fmt.Errorf("unrecognised YAML: no apiVersion/kind found in %s", filePath)
	}
	return nil, fmt.Errorf("unsupported deploy file format: %s", filePath)
}

// detectFormatsDeep walks the repo tree up to maxDepth levels and returns all
// recognizable stack definitions found in any subdirectory. Directories named
// node_modules, vendor, .git, or starting with '.' are skipped.
// Results are sorted by priority then by path depth (shallower = better).
func (imp *Importer) detectFormatsDeep(root string, maxDepth int) []DetectedFormat {
	skipDirs := map[string]bool{
		"node_modules": true, "vendor": true, ".git": true,
		"__pycache__": true, "dist": true, "build": true, "target": true,
	}
	var found []DetectedFormat
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		// Skip hidden and non-relevant dirs.
		base := d.Name()
		if strings.HasPrefix(base, ".") || skipDirs[base] {
			return filepath.SkipDir
		}
		// Enforce depth limit.
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		if depth > maxDepth {
			return filepath.SkipDir
		}
		// Run the flat scan on this subdirectory.
		for _, f := range imp.detectFormats(path) {
			found = append(found, f)
		}
		return nil
	})
	return found
}

// parseStratonMesh reads a native StratonMesh manifest.
func (imp *Importer) parseStratonMesh(path string) (*manifest.Stack, error) {
	return manifest.LoadFile(path)
}

// parseDockerCompose converts a docker-compose.yml to a StratonMesh manifest.
// nameHint is used as the stack name (e.g. from req.Name or git URL) before falling
// back to the compose file's name: field or the parent directory name.
func (imp *Importer) parseDockerCompose(path, nameHint string) (*manifest.Stack, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Parse the compose file into a generic structure
	var compose struct {
		Name     string                    `yaml:"name"` // Compose v2+ top-level name
		Version  string                    `yaml:"version"`
		Services map[string]ComposeService `yaml:"services"`
		Volumes  map[string]interface{}    `yaml:"volumes"`
	}
	if err := yaml.Unmarshal(data, &compose); err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}

	// Derive stack name: explicit hint → compose name field → parent directory name
	// The hint is always preferred because the directory may be a temp clone path.
	stackName := strings.ToLower(nameHint)
	if stackName == "" {
		stackName = strings.ToLower(compose.Name)
	}
	if stackName == "" {
		stackName = strings.ToLower(filepath.Base(filepath.Dir(path)))
	}
	// Sanitise: replace anything not alphanumeric or hyphen with a hyphen
	var sanitised []byte
	for _, c := range []byte(stackName) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			sanitised = append(sanitised, c)
		} else {
			sanitised = append(sanitised, '-')
		}
	}
	stackName = strings.Trim(string(sanitised), "-")
	if stackName == "" {
		stackName = "app"
	}

	stack := &manifest.Stack{
		Name:     stackName,
		Version:  "1.0.0",
		Platform: "compose", // docker-compose files deploy via the compose adapter
	}

	for name, cs := range compose.Services {
		image := cs.Image
		if image == "" && cs.Build.Context != "" {
			// Service is built from source — synthesize a valid image name.
			image = stackName + "/" + name + ":latest"
		}
		svc := manifest.Service{
			Name:  name,
			Image: image,
			Env:   make(map[string]string),
		}

		// Parse ports
		for _, p := range cs.Ports {
			port := parseComposePort(p)
			if port > 0 {
				svc.Ports = append(svc.Ports, manifest.PortSpec{Expose: port})
			}
		}

		// Parse environment (composeEnv is already a map)
		for k, v := range cs.Environment {
			svc.Env[k] = v
		}

		// Parse volumes
		for _, v := range cs.Volumes {
			parts := strings.SplitN(v, ":", 2)
			if len(parts) == 2 {
				svc.Volumes = append(svc.Volumes, manifest.VolumeSpec{
					Name:    strings.ReplaceAll(parts[0], "/", "-"),
					MountAt: parts[1],
					Size:    "10Gi", // default, user can override
				})
			}
		}

		// Parse depends_on
		svc.DependsOn = cs.DependsOn

		// Parse deploy config
		if cs.Deploy.Replicas > 0 {
			svc.Replicas = cs.Deploy.Replicas
		}

		// Parse healthcheck
		if cs.Healthcheck.Test != nil {
			svc.HealthCheck.Liveness = &manifest.Probe{
				Exec: strings.Join(cs.Healthcheck.Test, " "),
			}
		}

		// Parse restart policy
		if cs.Restart == "unless-stopped" || cs.Restart == "always" {
			// long-running service (default)
		}

		stack.Services = append(stack.Services, svc)
	}

	return stack, nil
}

// parseHelmChart reads a Helm chart and extracts the service topology.
func (imp *Importer) parseHelmChart(chartDir string) (*manifest.Stack, error) {
	// Read Chart.yaml for metadata
	chartPath := filepath.Join(chartDir, "Chart.yaml")
	data, err := os.ReadFile(chartPath)
	if err != nil {
		return nil, err
	}

	var chart struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
	}
	yaml.Unmarshal(data, &chart)

	// Read values.yaml for defaults
	valuesPath := filepath.Join(chartDir, "values.yaml")
	valuesData, _ := os.ReadFile(valuesPath)
	var values map[string]interface{}
	yaml.Unmarshal(valuesData, &values)

	stack := &manifest.Stack{
		Name:    chart.Name,
		Version: chart.Version,
	}

	// Scan templates for Deployments, StatefulSets, etc.
	templatesDir := filepath.Join(chartDir, "templates")
	entries, _ := os.ReadDir(templatesDir)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		tplData, err := os.ReadFile(filepath.Join(templatesDir, entry.Name()))
		if err != nil {
			continue
		}
		content := string(tplData)

		// Extract service from template (simplified — production would use helm template rendering)
		if strings.Contains(content, "kind: Deployment") || strings.Contains(content, "kind: StatefulSet") {
			svcName := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			svcName = strings.TrimSuffix(svcName, "-deployment")
			svcName = strings.TrimSuffix(svcName, "-statefulset")

			svc := manifest.Service{
				Name:     svcName,
				Image:    "{{ .Values.image.repository }}:{{ .Values.image.tag }}",
				Replicas: 1,
			}

			if strings.Contains(content, "kind: StatefulSet") {
				svc.Type = manifest.WorkloadStateful
			}

			stack.Services = append(stack.Services, svc)
		}
	}

	if len(stack.Services) == 0 {
		// Fallback: create a single service from the chart name
		stack.Services = []manifest.Service{{
			Name:     chart.Name,
			Image:    chart.Name + ":latest",
			Replicas: 1,
		}}
	}

	return stack, nil
}

// parseKubernetesManifests reads K8s YAML files and extracts services.
func (imp *Importer) parseKubernetesManifests(dir string) (*manifest.Stack, error) {
	stack := &manifest.Stack{
		Version: "1.0.0",
	}

	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}

		var k8sResource struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				Replicas int `yaml:"replicas"`
				Template struct {
					Spec struct {
						Containers []struct {
							Name  string `yaml:"name"`
							Image string `yaml:"image"`
							Ports []struct {
								ContainerPort int `yaml:"containerPort"`
							} `yaml:"ports"`
						} `yaml:"containers"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}

		if err := yaml.Unmarshal(data, &k8sResource); err != nil {
			continue
		}

		switch k8sResource.Kind {
		case "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob":
			svc := manifest.Service{
				Name:     k8sResource.Metadata.Name,
				Replicas: k8sResource.Spec.Replicas,
			}

			switch k8sResource.Kind {
			case "StatefulSet":
				svc.Type = manifest.WorkloadStateful
			case "DaemonSet":
				svc.Type = manifest.WorkloadDaemon
			case "Job":
				svc.Type = manifest.WorkloadBatch
			case "CronJob":
				svc.Type = manifest.WorkloadScheduled
			}

			if len(k8sResource.Spec.Template.Spec.Containers) > 0 {
				c := k8sResource.Spec.Template.Spec.Containers[0]
				svc.Image = c.Image
				for _, p := range c.Ports {
					svc.Ports = append(svc.Ports, manifest.PortSpec{Expose: p.ContainerPort})
				}
			}

			stack.Services = append(stack.Services, svc)
		}
	}

	return stack, nil
}

// parseDockerfile creates a single-service blueprint from a Dockerfile.
func (imp *Importer) parseDockerfile(path string, name string) (*manifest.Stack, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content := string(data)

	svc := manifest.Service{
		Name:     name,
		Image:    name + ":latest",
		Replicas: 1,
		Env:      make(map[string]string),
	}

	// Extract EXPOSE ports
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "EXPOSE ") {
			var port int
			fmt.Sscanf(strings.TrimPrefix(line, "EXPOSE "), "%d", &port)
			if port > 0 {
				svc.Ports = append(svc.Ports, manifest.PortSpec{Expose: port})
			}
		}
		if strings.HasPrefix(line, "ENV ") {
			parts := strings.SplitN(strings.TrimPrefix(line, "ENV "), "=", 2)
			if len(parts) == 2 {
				svc.Env[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}
		}
		if strings.HasPrefix(line, "VOLUME ") {
			vol := strings.Trim(strings.TrimPrefix(line, "VOLUME "), "[] \"")
			svc.Volumes = append(svc.Volumes, manifest.VolumeSpec{
				Name: strings.ReplaceAll(vol, "/", "-"), MountAt: vol, Size: "10Gi",
			})
		}
	}

	return &manifest.Stack{
		Name:     name,
		Version:  "1.0.0",
		Services: []manifest.Service{svc},
	}, nil
}

// --- Compose types ---

// composeEnv unmarshals environment as either a list ("KEY=VAL") or a map (KEY: val).
type composeEnv map[string]string

func (e *composeEnv) UnmarshalYAML(value *yaml.Node) error {
	*e = make(composeEnv)
	switch value.Kind {
	case yaml.MappingNode:
		var m map[string]string
		if err := value.Decode(&m); err != nil {
			// values may be null (no default), decode permissively
			var raw map[string]interface{}
			if err2 := value.Decode(&raw); err2 != nil {
				return err2
			}
			for k, v := range raw {
				if v != nil {
					(*e)[k] = fmt.Sprintf("%v", v)
				}
			}
			return nil
		}
		for k, v := range m {
			(*e)[k] = v
		}
	case yaml.SequenceNode:
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		for _, item := range list {
			parts := strings.SplitN(item, "=", 2)
			if len(parts) == 2 {
				(*e)[parts[0]] = parts[1]
			} else {
				(*e)[parts[0]] = ""
			}
		}
	}
	return nil
}

// composeStringList unmarshals a field that can be either a list of strings or
// a map (e.g. depends_on with condition objects — we just keep the keys).
type composeStringList []string

func (l *composeStringList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.SequenceNode:
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*l = list
	case yaml.MappingNode:
		// depends_on: {svc: {condition: service_healthy}} — extract keys
		var raw map[string]interface{}
		if err := value.Decode(&raw); err != nil {
			return err
		}
		for k := range raw {
			*l = append(*l, k)
		}
	case yaml.ScalarNode:
		*l = []string{value.Value}
	}
	return nil
}

// composeTestCmd handles healthcheck.test which can be a string or a list.
type composeTestCmd []string

func (t *composeTestCmd) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*t = []string{value.Value}
	case yaml.SequenceNode:
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*t = list
	}
	return nil
}

// composeBuild handles build: which can be a string path or a map {context, dockerfile}.
type composeBuild struct {
	Context string
}

func (b *composeBuild) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		b.Context = value.Value
	case yaml.MappingNode:
		var m struct {
			Context string `yaml:"context"`
		}
		if err := value.Decode(&m); err != nil {
			return err
		}
		b.Context = m.Context
	}
	return nil
}

type ComposeService struct {
	Image       string            `yaml:"image"`
	Build       composeBuild      `yaml:"build"`
	Ports       composeStringList `yaml:"ports"`
	Environment composeEnv        `yaml:"environment"`
	Volumes     composeStringList `yaml:"volumes"`
	DependsOn   composeStringList `yaml:"depends_on"`
	Restart     string            `yaml:"restart"`
	Healthcheck struct {
		Test composeTestCmd `yaml:"test"`
	} `yaml:"healthcheck"`
	Deploy struct {
		Replicas int `yaml:"replicas"`
	} `yaml:"deploy"`
}

func parseComposePort(s string) int {
	// Parse "8080:80" -> 80, "8080" -> 8080, "80:80/tcp" -> 80
	s = strings.Split(s, "/")[0] // strip protocol
	parts := strings.Split(s, ":")
	var port int
	if len(parts) >= 2 {
		fmt.Sscanf(parts[len(parts)-1], "%d", &port)
	} else {
		fmt.Sscanf(parts[0], "%d", &port)
	}
	return port
}

// copyDir recursively copies src to dst, used as a fallback when os.Rename fails across filesystems.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}
