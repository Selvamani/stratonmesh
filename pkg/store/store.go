package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

// Store provides typed access to StratonMesh state in etcd.
type Store struct {
	client *clientv3.Client
	logger *zap.SugaredLogger
	prefix string // "/stratonmesh" by default
}

// Config holds etcd connection settings.
type Config struct {
	Endpoints   []string      `yaml:"endpoints" json:"endpoints"`     // e.g., ["localhost:2379"]
	DialTimeout time.Duration `yaml:"dialTimeout" json:"dialTimeout"` // default 5s
	Prefix      string        `yaml:"prefix" json:"prefix"`           // default "/stratonmesh"
}

// New creates a connected Store.
func New(cfg Config, logger *zap.SugaredLogger) (*Store, error) {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "/stratonmesh"
	}
	if len(cfg.Endpoints) == 0 {
		cfg.Endpoints = []string{"localhost:2379"}
	}

	client, err := clientv3.New(clientv3.Config{
		Endpoints:   cfg.Endpoints,
		DialTimeout: cfg.DialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to etcd: %w", err)
	}

	logger.Infow("connected to etcd", "endpoints", cfg.Endpoints, "prefix", cfg.Prefix)
	return &Store{client: client, logger: logger, prefix: cfg.Prefix}, nil
}

// Close shuts down the etcd connection.
func (s *Store) Close() error {
	return s.client.Close()
}

// --- Key helpers ---

func (s *Store) key(parts ...string) string {
	return s.prefix + "/" + strings.Join(parts, "/")
}

// --- Stack state operations ---

// StackState holds both desired and actual state for a stack.
type StackState struct {
	StackID  string      `json:"stackId"`
	Desired  interface{} `json:"desired"`
	Actual   interface{} `json:"actual"`
	Status   string      `json:"status"` // pending, scheduling, deploying, running, failed, stopped
	Version  int64       `json:"version"`
}

// SetDesired writes the desired state for a stack (atomic).
func (s *Store) SetDesired(ctx context.Context, stackID string, desired interface{}) error {
	data, err := json.Marshal(desired)
	if err != nil {
		return fmt.Errorf("marshal desired state: %w", err)
	}
	key := s.key("stacks", stackID, "desired")
	_, err = s.client.Put(ctx, key, string(data))
	if err != nil {
		return fmt.Errorf("put desired state: %w", err)
	}
	s.logger.Debugw("set desired state", "stack", stackID, "bytes", len(data))
	return nil
}

// GetDesired reads the desired state for a stack.
func (s *Store) GetDesired(ctx context.Context, stackID string, dest interface{}) error {
	key := s.key("stacks", stackID, "desired")
	resp, err := s.client.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("get desired state: %w", err)
	}
	if len(resp.Kvs) == 0 {
		return fmt.Errorf("stack %q: desired state not found", stackID)
	}
	return json.Unmarshal(resp.Kvs[0].Value, dest)
}

// SetActual writes the actual (observed) state for a stack.
func (s *Store) SetActual(ctx context.Context, stackID string, actual interface{}) error {
	data, err := json.Marshal(actual)
	if err != nil {
		return fmt.Errorf("marshal actual state: %w", err)
	}
	key := s.key("stacks", stackID, "actual")
	_, err = s.client.Put(ctx, key, string(data))
	return err
}

// GetActual reads the actual state for a stack.
func (s *Store) GetActual(ctx context.Context, stackID string, dest interface{}) error {
	key := s.key("stacks", stackID, "actual")
	resp, err := s.client.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("get actual state: %w", err)
	}
	if len(resp.Kvs) == 0 {
		return fmt.Errorf("stack %q: actual state not found", stackID)
	}
	return json.Unmarshal(resp.Kvs[0].Value, dest)
}

// SetStatus atomically updates the stack status.
func (s *Store) SetStatus(ctx context.Context, stackID, status string) error {
	key := s.key("stacks", stackID, "status")
	_, err := s.client.Put(ctx, key, status)
	return err
}

// GetStatus reads the current stack status.
func (s *Store) GetStatus(ctx context.Context, stackID string) (string, error) {
	key := s.key("stacks", stackID, "status")
	resp, err := s.client.Get(ctx, key)
	if err != nil {
		return "", err
	}
	if len(resp.Kvs) == 0 {
		return "", nil
	}
	return string(resp.Kvs[0].Value), nil
}

// ListStacks returns all stack IDs.
func (s *Store) ListStacks(ctx context.Context) ([]string, error) {
	prefix := s.key("stacks") + "/"
	resp, err := s.client.Get(ctx, prefix, clientv3.WithPrefix(), clientv3.WithKeysOnly())
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	for _, kv := range resp.Kvs {
		// Key format: /stratonmesh/stacks/{stackID}/...
		parts := strings.Split(strings.TrimPrefix(string(kv.Key), prefix), "/")
		if len(parts) > 0 && !seen[parts[0]] {
			seen[parts[0]] = true
		}
	}
	var ids []string
	for id := range seen {
		ids = append(ids, id)
	}
	return ids, nil
}

// DeleteStack removes all state for a stack.
func (s *Store) DeleteStack(ctx context.Context, stackID string) error {
	prefix := s.key("stacks", stackID) + "/"
	_, err := s.client.Delete(ctx, prefix, clientv3.WithPrefix())
	return err
}

// --- Watch ---

// WatchDesired watches for changes to any stack's desired state.
// Sends the stack ID on the channel when desired state changes.
func (s *Store) WatchDesired(ctx context.Context) <-chan string {
	ch := make(chan string, 64)
	prefix := s.key("stacks")

	go func() {
		defer close(ch)
		watcher := s.client.Watch(ctx, prefix, clientv3.WithPrefix())
		for resp := range watcher {
			for _, ev := range resp.Events {
				key := string(ev.Kv.Key)
				if strings.HasSuffix(key, "/desired") {
					parts := strings.Split(strings.TrimPrefix(key, prefix+"/"), "/")
					if len(parts) >= 1 {
						select {
						case ch <- parts[0]:
						default:
							s.logger.Warnw("watch channel full, dropping event", "stack", parts[0])
						}
					}
				}
			}
		}
	}()

	return ch
}

// --- Service Registry ---

// ServiceEndpoint represents a running service instance in the registry.
type ServiceEndpoint struct {
	Service      string            `json:"service"`
	Stack        string            `json:"stack"`
	Instance     string            `json:"instance"` // e.g., "api-gateway-0"
	Endpoint     string            `json:"endpoint"` // "10.0.3.47:8080"
	Node         string            `json:"node"`
	Version      string            `json:"version"`
	Health       string            `json:"health"` // healthy, unhealthy, unknown
	Weight       int               `json:"weight"` // traffic weight (0-100)
	Labels       map[string]string `json:"labels,omitempty"`
	RegisteredAt time.Time         `json:"registeredAt"`
}

// RegisterService adds a service endpoint to the registry.
func (s *Store) RegisterService(ctx context.Context, ep ServiceEndpoint) error {
	data, err := json.Marshal(ep)
	if err != nil {
		return err
	}
	key := s.key("services", ep.Service, ep.Stack, ep.Instance)
	_, err = s.client.Put(ctx, key, string(data))
	s.logger.Debugw("registered service", "service", ep.Service, "instance", ep.Instance, "endpoint", ep.Endpoint)
	return err
}

// DeregisterService removes a service endpoint from the registry.
func (s *Store) DeregisterService(ctx context.Context, service, stack, instance string) error {
	key := s.key("services", service, stack, instance)
	_, err := s.client.Delete(ctx, key)
	return err
}

// GetServiceEndpoints returns all healthy endpoints for a service.
func (s *Store) GetServiceEndpoints(ctx context.Context, service, stack string) ([]ServiceEndpoint, error) {
	prefix := s.key("services", service, stack) + "/"
	resp, err := s.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	var endpoints []ServiceEndpoint
	for _, kv := range resp.Kvs {
		var ep ServiceEndpoint
		if err := json.Unmarshal(kv.Value, &ep); err != nil {
			continue
		}
		endpoints = append(endpoints, ep)
	}
	return endpoints, nil
}

// --- DNS Records ---

// SetDNSRecord writes a DNS A record for service discovery.
func (s *Store) SetDNSRecord(ctx context.Context, fqdn string, addresses []string) error {
	data, err := json.Marshal(addresses)
	if err != nil {
		return err
	}
	key := s.key("dns", fqdn)
	_, err = s.client.Put(ctx, key, string(data))
	return err
}

// --- Version Ledger ---

// LedgerEntry records a deployment event.
type LedgerEntry struct {
	StackID    string      `json:"stackId"`
	Version    string      `json:"version"`
	Manifest   interface{} `json:"manifest"` // full resolved manifest
	DeployedBy string      `json:"deployedBy"`
	DeployedAt time.Time   `json:"deployedAt"`
	GitSHA     string      `json:"gitSha,omitempty"`
}

// AppendLedger adds an entry to the version ledger for a stack.
func (s *Store) AppendLedger(ctx context.Context, entry LedgerEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	key := s.key("ledger", entry.StackID, fmt.Sprintf("%d", time.Now().UnixNano()))
	_, err = s.client.Put(ctx, key, string(data))
	return err
}

// GetLedger returns the last N deployment entries for a stack.
func (s *Store) GetLedger(ctx context.Context, stackID string, limit int) ([]LedgerEntry, error) {
	prefix := s.key("ledger", stackID) + "/"
	resp, err := s.client.Get(ctx, prefix,
		clientv3.WithPrefix(),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortDescend),
		clientv3.WithLimit(int64(limit)),
	)
	if err != nil {
		return nil, err
	}
	var entries []LedgerEntry
	for _, kv := range resp.Kvs {
		var entry LedgerEntry
		if err := json.Unmarshal(kv.Value, &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// --- Node Registration ---

// NodeInfo represents a cluster node reported by its agent.
type NodeInfo struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	OS         string            `json:"os"` // linux, darwin, windows
	Providers  []string          `json:"providers"` // docker, process, vm, wasm
	CPUTotal   int64             `json:"cpuTotal"`  // millicores
	MemTotal   int64             `json:"memTotal"`  // bytes
	GPUTotal   int               `json:"gpuTotal"`
	CPUFree    int64             `json:"cpuFree"`
	MemFree    int64             `json:"memFree"`
	GPUFree    int               `json:"gpuFree"`
	Labels     map[string]string `json:"labels,omitempty"`
	Status     string            `json:"status"` // healthy, unhealthy
	Region     string            `json:"region,omitempty"`
	CostPerHr  float64           `json:"costPerHr"`
	LastSeen   time.Time         `json:"lastSeen"`
}

// RegisterNode upserts a node's info with a TTL lease.
func (s *Store) RegisterNode(ctx context.Context, node NodeInfo) error {
	data, err := json.Marshal(node)
	if err != nil {
		return err
	}
	// Create a lease that expires if the agent stops heartbeating
	lease, err := s.client.Grant(ctx, 30) // 30-second TTL
	if err != nil {
		return err
	}
	key := s.key("nodes", node.ID)
	_, err = s.client.Put(ctx, key, string(data), clientv3.WithLease(lease.ID))
	return err
}

// ListNodes returns all registered nodes.
func (s *Store) ListNodes(ctx context.Context) ([]NodeInfo, error) {
	prefix := s.key("nodes") + "/"
	resp, err := s.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	var nodes []NodeInfo
	for _, kv := range resp.Kvs {
		var node NodeInfo
		if err := json.Unmarshal(kv.Value, &node); err != nil {
			continue
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// --- Blueprint Catalog ---

// Blueprint stores a catalog entry.
type Blueprint struct {
	Name        string                 `json:"name"`
	Version     string                 `json:"version"`
	Source      string                 `json:"source"` // docker-compose, helm, k8s, custom
	ImportMode  string                 `json:"importMode"` // "catalog" (metadata only) or "repo" (full clone kept on disk)
	LocalPath   string                 `json:"localPath,omitempty"` // absolute path to cloned repo (repo mode only)
	GitURL      string                 `json:"gitUrl,omitempty"`
	GitBranch   string                 `json:"gitBranch,omitempty"`
	GitPath     string                 `json:"gitPath,omitempty"`
	GitSHA      string                 `json:"gitSha,omitempty"`
	Category    string                 `json:"category,omitempty"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
	Manifest    interface{}            `json:"manifest"` // the StratonMesh manifest template
	CreatedAt   time.Time              `json:"createdAt"`
	UpdatedAt   time.Time              `json:"updatedAt"`
}

// SaveBlueprint stores a blueprint in the catalog.
func (s *Store) SaveBlueprint(ctx context.Context, bp Blueprint) error {
	data, err := json.Marshal(bp)
	if err != nil {
		return err
	}
	key := s.key("catalog", bp.Name)
	_, err = s.client.Put(ctx, key, string(data))
	s.logger.Infow("saved blueprint", "name", bp.Name, "version", bp.Version, "source", bp.Source)
	return err
}

// GetBlueprint retrieves a blueprint by name.
func (s *Store) GetBlueprint(ctx context.Context, name string) (*Blueprint, error) {
	key := s.key("catalog", name)
	resp, err := s.client.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(resp.Kvs) == 0 {
		return nil, fmt.Errorf("blueprint %q not found", name)
	}
	var bp Blueprint
	if err := json.Unmarshal(resp.Kvs[0].Value, &bp); err != nil {
		return nil, err
	}
	return &bp, nil
}

// ListBlueprints returns all blueprints in the catalog.
func (s *Store) ListBlueprints(ctx context.Context) ([]Blueprint, error) {
	prefix := s.key("catalog") + "/"
	resp, err := s.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	var blueprints []Blueprint
	for _, kv := range resp.Kvs {
		var bp Blueprint
		if err := json.Unmarshal(kv.Value, &bp); err != nil {
			continue
		}
		blueprints = append(blueprints, bp)
	}
	return blueprints, nil
}

// DeleteBlueprint removes a blueprint from the catalog.
func (s *Store) DeleteBlueprint(ctx context.Context, name string) error {
	key := s.key("catalog", name)
	_, err := s.client.Delete(ctx, key)
	if err != nil {
		return err
	}
	s.logger.Infow("deleted blueprint", "name", name)
	return nil
}

// --- Generic helpers ---

// CompareAndSwap performs an atomic compare-and-swap for optimistic concurrency.
func (s *Store) CompareAndSwap(ctx context.Context, key string, expected, newValue string) (bool, error) {
	fullKey := s.prefix + "/" + key
	txn := s.client.Txn(ctx).
		If(clientv3.Compare(clientv3.Value(fullKey), "=", expected)).
		Then(clientv3.OpPut(fullKey, newValue)).
		Else(clientv3.OpGet(fullKey))

	resp, err := txn.Commit()
	if err != nil {
		return false, err
	}
	return resp.Succeeded, nil
}
