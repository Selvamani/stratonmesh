package config

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	vault "github.com/hashicorp/vault/api"
	"go.uber.org/zap"
)

// Manager handles Vault-backed secret injection and dynamic credential renewal.
//
// In manifest env maps, values prefixed with "vault:" are resolved at deploy time:
//
//	env:
//	  DB_PASSWORD: "vault:secret/data/myapp#password"
//	  API_KEY:     "vault:secret/data/myapp#api_key"
//
// The format is:  vault:{mount/path}#{field}
// If the field is omitted the entire secret data map is returned as JSON.
type Manager struct {
	client  *vault.Client
	token   string
	mu      sync.RWMutex
	cache   map[string]cacheEntry
	logger  *zap.SugaredLogger
	renewCh chan struct{}
}

type cacheEntry struct {
	data      map[string]interface{}
	expiresAt time.Time
}

// Config holds Vault connection settings.
type Config struct {
	Address string `yaml:"address" json:"address"` // e.g. "http://localhost:8200"
	Token   string `yaml:"token" json:"token"`     // root/app-role token
	// RoleID / SecretID for AppRole auth (preferred over static token in prod).
	RoleID   string `yaml:"roleId,omitempty" json:"roleId,omitempty"`
	SecretID string `yaml:"secretId,omitempty" json:"secretId,omitempty"`
	// Namespace for Vault Enterprise (leave empty for OSS).
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`
	// CacheTTL controls how long secret values are cached locally (default 5m).
	CacheTTL time.Duration `yaml:"cacheTtl,omitempty" json:"cacheTtl,omitempty"`
}

// New creates a Manager and authenticates to Vault.
func New(cfg Config, logger *zap.SugaredLogger) (*Manager, error) {
	vcfg := vault.DefaultConfig()
	if cfg.Address != "" {
		vcfg.Address = cfg.Address
	}

	client, err := vault.NewClient(vcfg)
	if err != nil {
		return nil, fmt.Errorf("create vault client: %w", err)
	}

	if cfg.Namespace != "" {
		client.SetNamespace(cfg.Namespace)
	}

	m := &Manager{
		client:  client,
		cache:   make(map[string]cacheEntry),
		logger:  logger,
		renewCh: make(chan struct{}, 1),
	}

	if cfg.RoleID != "" && cfg.SecretID != "" {
		if err := m.appRoleLogin(cfg.RoleID, cfg.SecretID); err != nil {
			return nil, fmt.Errorf("vault AppRole login: %w", err)
		}
		logger.Infow("vault: authenticated via AppRole", "address", vcfg.Address)
	} else if cfg.Token != "" {
		client.SetToken(cfg.Token)
		m.token = cfg.Token
		logger.Infow("vault: authenticated via token", "address", vcfg.Address)
	} else {
		return nil, fmt.Errorf("vault: no auth credentials provided (set token or roleId+secretId)")
	}

	return m, nil
}

// Resolve reads a secret value from Vault.
// path is the KV path (e.g. "secret/data/myapp").
// field is the key within the secret data (e.g. "password"). Empty returns all data as JSON.
func (m *Manager) Resolve(ctx context.Context, path, field string) (string, error) {
	data, err := m.readSecret(ctx, path)
	if err != nil {
		return "", err
	}
	if field == "" {
		// Return JSON of entire data map
		var sb strings.Builder
		sb.WriteString("{")
		first := true
		for k, v := range data {
			if !first {
				sb.WriteString(",")
			}
			sb.WriteString(fmt.Sprintf("%q:%q", k, fmt.Sprint(v)))
			first = false
		}
		sb.WriteString("}")
		return sb.String(), nil
	}
	v, ok := data[field]
	if !ok {
		return "", fmt.Errorf("vault: field %q not found at path %q", field, path)
	}
	return fmt.Sprint(v), nil
}

// InjectSecrets replaces vault: references in an env map with the actual secret values.
// Returns a new map with resolved values; does not mutate the input.
func (m *Manager) InjectSecrets(ctx context.Context, env map[string]string) (map[string]string, error) {
	if len(env) == 0 {
		return env, nil
	}
	resolved := make(map[string]string, len(env))
	for k, v := range env {
		if !strings.HasPrefix(v, "vault:") {
			resolved[k] = v
			continue
		}
		ref := strings.TrimPrefix(v, "vault:")
		path, field, _ := strings.Cut(ref, "#")
		secret, err := m.Resolve(ctx, path, field)
		if err != nil {
			return nil, fmt.Errorf("inject secret for env key %q: %w", k, err)
		}
		resolved[k] = secret
		m.logger.Debugw("secret injected", "key", k, "vault_path", path)
	}
	return resolved, nil
}

// InjectAll resolves vault: references in all service env maps in the provided
// stack-like structure. envMaps is a mapping of service name → env map.
func (m *Manager) InjectAll(ctx context.Context, envMaps map[string]map[string]string) (map[string]map[string]string, error) {
	result := make(map[string]map[string]string, len(envMaps))
	for svc, env := range envMaps {
		resolved, err := m.InjectSecrets(ctx, env)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", svc, err)
		}
		result[svc] = resolved
	}
	return result, nil
}

// HasVaultRefs returns true if any value in env has a "vault:" prefix.
func HasVaultRefs(env map[string]string) bool {
	for _, v := range env {
		if strings.HasPrefix(v, "vault:") {
			return true
		}
	}
	return false
}

// WatchAndRenew starts a goroutine that periodically renews the Vault token
// and refreshes cached secrets. Blocks until ctx is cancelled.
func (m *Manager) WatchAndRenew(ctx context.Context, refreshInterval time.Duration) {
	if refreshInterval <= 0 {
		refreshInterval = 4 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := m.renewToken(); err != nil {
					m.logger.Warnw("vault token renewal failed", "error", err)
				}
				m.evictExpired()
			case <-ctx.Done():
				return
			}
		}
	}()
	m.logger.Infow("vault token renewal loop started", "interval", refreshInterval)
}

// --- Internal ---

func (m *Manager) readSecret(ctx context.Context, path string) (map[string]interface{}, error) {
	m.mu.RLock()
	entry, ok := m.cache[path]
	m.mu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.data, nil
	}

	secret, err := m.client.KVv2(kvMount(path)).Get(ctx, kvPath(path))
	if err != nil {
		return nil, fmt.Errorf("vault read %q: %w", path, err)
	}
	if secret == nil || secret.Data == nil {
		return nil, fmt.Errorf("vault: empty secret at %q", path)
	}

	m.mu.Lock()
	m.cache[path] = cacheEntry{
		data:      secret.Data,
		expiresAt: time.Now().Add(5 * time.Minute),
	}
	m.mu.Unlock()

	return secret.Data, nil
}

// kvMount extracts the mount point from a KV path.
// "secret/data/myapp/db" → "secret"
func kvMount(path string) string {
	parts := strings.SplitN(path, "/", 2)
	return parts[0]
}

// kvPath extracts the key path from a KV path (strips mount and "data/" prefix).
// "secret/data/myapp/db" → "myapp/db"
func kvPath(path string) string {
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 3 {
		return path
	}
	// Strip the "data/" segment that KVv2 adds
	sub := parts[2]
	if strings.HasPrefix(sub, "data/") {
		sub = strings.TrimPrefix(sub, "data/")
	}
	return sub
}

func (m *Manager) appRoleLogin(roleID, secretID string) error {
	data := map[string]interface{}{
		"role_id":   roleID,
		"secret_id": secretID,
	}
	resp, err := m.client.Logical().Write("auth/approle/login", data)
	if err != nil {
		return fmt.Errorf("approle login failed: %w", err)
	}
	if resp.Auth == nil {
		return fmt.Errorf("approle login: no auth in response")
	}
	m.client.SetToken(resp.Auth.ClientToken)
	m.token = resp.Auth.ClientToken
	return nil
}

func (m *Manager) renewToken() error {
	_, err := m.client.Auth().Token().RenewSelf(0)
	if err != nil {
		return err
	}
	m.logger.Debug("vault token renewed")
	return nil
}

func (m *Manager) evictExpired() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for k, entry := range m.cache {
		if now.After(entry.expiresAt) {
			delete(m.cache, k)
		}
	}
}
