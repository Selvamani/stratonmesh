package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/stratonmesh/stratonmesh/pkg/manifest"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

const systemPrompt = `You are a DevOps expert that converts any Git repository into a StratonMesh stack manifest.

Given a repository's file structure and key file contents, output ONLY a valid StratonMesh stack.yaml in YAML format. No explanation, no markdown fences.

StratonMesh manifest schema:
name: string (slug, lowercase, hyphens only)
version: string (e.g. "1.0.0")
platform: string (one of: docker, compose, kubernetes, terraform)
services:
  - name: string
    image: string (Docker image ref, or leave empty if built from source)
    type: string (long-running | stateful | batch | scheduled | daemon)
    replicas: int (default 1)
    ports:
      - expose: int
        protocol: tcp|udp
    env:
      KEY: value
    volumes:
      - name: string
        mountAt: string
        size: string (e.g. "10Gi")
    healthCheck:
      liveness:
        http: /health
        port: 8080
      readiness:
        http: /ready
        port: 8080
    resources:
      cpuRequest: "100m"
      cpuLimit: "500m"
      memRequest: "128Mi"
      memLimit: "512Mi"
    dependsOn: [other-service-name]

Rules:
- Use platform: compose when the repo has docker-compose files or Dockerfiles to build
- Use platform: kubernetes for K8s-native apps
- Infer services from Dockerfiles, docker-compose files, or directory structure
- Assign workload types: web servers → long-running, databases → stateful, cron jobs → scheduled, scripts → batch
- For services built from source (Dockerfile present), leave image empty
- Set reasonable resource defaults
- Include health checks when you can infer the HTTP port
- Output ONLY the YAML. No prose.`

// analyzeRepoWithAI sends the repository structure to Claude and returns a parsed Stack manifest.
func analyzeRepoWithAI(ctx context.Context, repoDir, nameHint string, log *zap.SugaredLogger) (*manifest.Stack, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY not set")
	}

	client := anthropic.NewClient() // reads ANTHROPIC_API_KEY from env

	// Build a snapshot of the repo for Claude.
	snapshot, err := buildRepoSnapshot(repoDir)
	if err != nil {
		return nil, fmt.Errorf("snapshot repo: %w", err)
	}

	userMsg := fmt.Sprintf("Repository name hint: %q\n\n%s\n\nGenerate the StratonMesh stack.yaml for this repository.", nameHint, snapshot)

	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeHaiku4_5,
		MaxTokens: 2048,
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userMsg)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("claude API: %w", err)
	}
	if len(resp.Content) == 0 {
		return nil, fmt.Errorf("claude returned empty response")
	}

	raw := resp.Content[0].Text
	raw = strings.TrimSpace(raw)
	// Strip markdown fences if Claude adds them despite instructions.
	raw = strings.TrimPrefix(raw, "```yaml")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var stack manifest.Stack
	if err := yaml.Unmarshal([]byte(raw), &stack); err != nil {
		return nil, fmt.Errorf("parse Claude output as YAML: %w\nRaw:\n%s", err, raw)
	}
	if len(stack.Services) == 0 {
		return nil, fmt.Errorf("Claude generated no services")
	}

	// Sanitise the name.
	if nameHint != "" {
		stack.Name = nameHint
	}
	if stack.Name == "" {
		stack.Name = "ai-imported"
	}
	if stack.Version == "" {
		stack.Version = "1.0.0"
	}
	// AI imports always keep the repo on disk for `docker compose build`.
	if stack.Platform == "" || stack.Platform == "compose" {
		stack.Platform = "compose"
	}

	return &stack, nil
}

// buildRepoSnapshot returns a text summary of the repo: file tree + content of key files.
func buildRepoSnapshot(repoDir string) (string, error) {
	var sb strings.Builder

	// File tree (max 200 entries, skip hidden / vendor dirs).
	sb.WriteString("=== File Tree ===\n")
	count := 0
	err := filepath.Walk(repoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || count > 200 {
			return nil
		}
		rel, _ := filepath.Rel(repoDir, path)
		base := filepath.Base(rel)
		// Skip hidden dirs and common vendor paths.
		if strings.HasPrefix(base, ".") || base == "node_modules" || base == "vendor" || base == "__pycache__" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			sb.WriteString(rel + "/\n")
		} else {
			sb.WriteString(rel + "\n")
			count++
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	// Key file contents.
	keyFiles := []string{
		"README.md", "readme.md",
		"docker-compose.yml", "docker-compose.yaml",
		"docker-compose.prod.yml", "docker-compose.production.yml",
		"Dockerfile",
	}
	// Also collect Dockerfiles in subdirectories (one level deep).
	entries, _ := os.ReadDir(repoDir)
	for _, e := range entries {
		if e.IsDir() {
			df := filepath.Join(repoDir, e.Name(), "Dockerfile")
			if _, err := os.Stat(df); err == nil {
				keyFiles = append(keyFiles, filepath.Join(e.Name(), "Dockerfile"))
			}
		}
	}

	for _, rel := range keyFiles {
		full := filepath.Join(repoDir, rel)
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > 3000 {
			content = content[:3000] + "\n... (truncated)"
		}
		sb.WriteString(fmt.Sprintf("\n=== %s ===\n%s\n", rel, content))
	}

	return sb.String(), nil
}
