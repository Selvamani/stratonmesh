package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

// Manager syncs StratonMesh service registry records into etcd paths that
// the CoreDNS etcd plugin reads. It also generates the Corefile snippet
// that configures CoreDNS to serve the stratonmesh.local zone.
//
// CoreDNS etcd plugin expects records at:
//
//	/coredns/{reversed-zone}/{host}   →  {"host": "10.0.0.1", "ttl": 30}
//
// For a service "api" in stack "myapp" we write:
//
//	/coredns/local/stratonmesh/myapp/api  →  {"host": "10.0.0.5", "ttl": 30}
//
// which makes api.myapp.stratonmesh.local resolvable.
type Manager struct {
	client  *clientv3.Client
	smStore smStore
	prefix  string // CoreDNS etcd prefix, default "/coredns"
	zone    string // DNS zone, default "stratonmesh.local"
	logger  *zap.SugaredLogger
}

// smStore is the minimal interface the DNS manager needs from the StratonMesh store.
type smStore interface {
	GetServiceEndpoints(ctx context.Context, service, stack string) ([]serviceEndpoint, error)
	WatchServices(ctx context.Context) <-chan serviceChange
}

// serviceEndpoint mirrors store.ServiceEndpoint.
type serviceEndpoint struct {
	Service  string
	Stack    string
	Instance string
	Endpoint string // "ip:port"
	Health   string
}

// serviceChange is a notification that a service was registered or deregistered.
type serviceChange struct {
	Service  string
	Stack    string
	Action   string // "put" | "delete"
	Endpoint serviceEndpoint
}

// CoreDNSRecord is the JSON value the CoreDNS etcd plugin expects.
type CoreDNSRecord struct {
	Host string `json:"host"`
	TTL  int    `json:"ttl"`
}

// Config holds DNS manager settings.
type Config struct {
	EtcdEndpoints []string      `yaml:"etcdEndpoints" json:"etcdEndpoints"`
	DialTimeout   time.Duration `yaml:"dialTimeout" json:"dialTimeout"`
	Prefix        string        `yaml:"prefix" json:"prefix"` // default "/coredns"
	Zone          string        `yaml:"zone" json:"zone"`     // default "stratonmesh.local"
}

// NewManager creates a Manager connected to etcd.
func NewManager(cfg Config, logger *zap.SugaredLogger) (*Manager, error) {
	if cfg.Prefix == "" {
		cfg.Prefix = "/coredns"
	}
	if cfg.Zone == "" {
		cfg.Zone = "stratonmesh.local"
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if len(cfg.EtcdEndpoints) == 0 {
		cfg.EtcdEndpoints = []string{"localhost:2379"}
	}

	client, err := clientv3.New(clientv3.Config{
		Endpoints:   cfg.EtcdEndpoints,
		DialTimeout: cfg.DialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to etcd: %w", err)
	}

	return &Manager{
		client: client,
		prefix: cfg.Prefix,
		zone:   cfg.Zone,
		logger: logger,
	}, nil
}

// Close shuts down the etcd connection.
func (m *Manager) Close() error {
	return m.client.Close()
}

// WriteRecord writes a single DNS A record so CoreDNS can resolve the FQDN.
// fqdn must be relative to the zone (e.g., "api.myapp" → api.myapp.stratonmesh.local).
func (m *Manager) WriteRecord(ctx context.Context, fqdn, ip string, ttl int) error {
	if ttl <= 0 {
		ttl = 30
	}
	rec := CoreDNSRecord{Host: ip, TTL: ttl}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	key := m.recordKey(fqdn)
	_, err = m.client.Put(ctx, key, string(data))
	if err != nil {
		return fmt.Errorf("write DNS record %s: %w", fqdn, err)
	}
	m.logger.Debugw("DNS record written", "fqdn", fqdn+"."+m.zone, "ip", ip, "key", key)
	return nil
}

// DeleteRecord removes a DNS record.
func (m *Manager) DeleteRecord(ctx context.Context, fqdn string) error {
	key := m.recordKey(fqdn)
	_, err := m.client.Delete(ctx, key)
	return err
}

// WriteServiceRecord derives the FQDN from service/stack names and writes the record.
// Produces: {service}.{stack}.stratonmesh.local → ip
func (m *Manager) WriteServiceRecord(ctx context.Context, service, stack, ip string) error {
	fqdn := fmt.Sprintf("%s.%s", service, stack)
	return m.WriteRecord(ctx, fqdn, ip, 30)
}

// DeleteServiceRecord removes the DNS record for a service instance.
func (m *Manager) DeleteServiceRecord(ctx context.Context, service, stack string) error {
	fqdn := fmt.Sprintf("%s.%s", service, stack)
	return m.DeleteRecord(ctx, fqdn)
}

// ListRecords returns all DNS records currently in the CoreDNS etcd namespace.
func (m *Manager) ListRecords(ctx context.Context) (map[string]CoreDNSRecord, error) {
	resp, err := m.client.Get(ctx, m.prefix+"/", clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	records := make(map[string]CoreDNSRecord)
	for _, kv := range resp.Kvs {
		var rec CoreDNSRecord
		if err := json.Unmarshal(kv.Value, &rec); err != nil {
			continue
		}
		records[string(kv.Key)] = rec
	}
	return records, nil
}

// GenerateCorefile returns a minimal Corefile snippet for the stratonmesh.local zone.
// Embed this in your CoreDNS configuration.
func (m *Manager) GenerateCorefile() string {
	return fmt.Sprintf(`%s {
    etcd {
        stubzones
        path %s
        endpoint %s
    }
    cache 30
    loadbalance
    loop
    reload
}

. {
    forward . 8.8.8.8 1.1.1.1
    cache 300
}
`, m.zone, m.prefix, "http://localhost:2379")
}

// SyncFromRegistry copies all currently registered service endpoints from the
// StratonMesh etcd service registry into the CoreDNS etcd namespace.
// Call this once on startup to populate DNS before the watch loop catches up.
func (m *Manager) SyncFromRegistry(ctx context.Context, etcdPrefix string) error {
	srcPrefix := etcdPrefix + "/services/"
	resp, err := m.client.Get(ctx, srcPrefix, clientv3.WithPrefix())
	if err != nil {
		return fmt.Errorf("scan service registry: %w", err)
	}

	type epRecord struct {
		Endpoint string `json:"endpoint"`
		Health   string `json:"health"`
	}

	synced := 0
	for _, kv := range resp.Kvs {
		// Key: /stratonmesh/services/{service}/{stack}/{instance}
		rel := strings.TrimPrefix(string(kv.Key), srcPrefix)
		parts := strings.Split(rel, "/")
		if len(parts) < 2 {
			continue
		}
		svcName, stackName := parts[0], parts[1]

		var ep epRecord
		if err := json.Unmarshal(kv.Value, &ep); err != nil {
			continue
		}
		if ep.Health == "unhealthy" {
			continue
		}
		// Extract just the IP from "ip:port"
		ip := ep.Endpoint
		if idx := strings.LastIndex(ip, ":"); idx != -1 {
			ip = ip[:idx]
		}
		if ip == "" {
			continue
		}

		if err := m.WriteServiceRecord(ctx, svcName, stackName, ip); err != nil {
			m.logger.Warnw("failed to sync DNS record", "service", svcName, "stack", stackName, "error", err)
			continue
		}
		synced++
	}

	m.logger.Infow("DNS sync from registry complete", "records", synced)
	return nil
}

// WatchAndSync watches the StratonMesh service registry for changes and keeps
// CoreDNS records up to date. Blocks until ctx is cancelled.
func (m *Manager) WatchAndSync(ctx context.Context, etcdPrefix string) {
	watchPrefix := etcdPrefix + "/services/"
	watcher := m.client.Watch(ctx, watchPrefix, clientv3.WithPrefix())

	m.logger.Info("DNS watch loop started")
	for {
		select {
		case resp, ok := <-watcher:
			if !ok {
				return
			}
			for _, ev := range resp.Events {
				key := strings.TrimPrefix(string(ev.Kv.Key), watchPrefix)
				parts := strings.Split(key, "/")
				if len(parts) < 2 {
					continue
				}
				svcName, stackName := parts[0], parts[1]

				switch ev.Type {
				case clientv3.EventTypePut:
					type epRecord struct {
						Endpoint string `json:"endpoint"`
						Health   string `json:"health"`
					}
					var ep epRecord
					if err := json.Unmarshal(ev.Kv.Value, &ep); err != nil {
						continue
					}
					ip := ep.Endpoint
					if idx := strings.LastIndex(ip, ":"); idx != -1 {
						ip = ip[:idx]
					}
					if ip != "" {
						m.WriteServiceRecord(ctx, svcName, stackName, ip)
					}
				case clientv3.EventTypeDelete:
					m.DeleteServiceRecord(ctx, svcName, stackName)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// recordKey converts a relative FQDN to a CoreDNS etcd key.
// "api.myapp" in zone "stratonmesh.local" →
//
//	/coredns/local/stratonmesh/myapp/api
func (m *Manager) recordKey(fqdn string) string {
	// Reverse the full FQDN: api.myapp.stratonmesh.local → local.stratonmesh.myapp.api
	full := fqdn + "." + m.zone
	labels := strings.Split(full, ".")
	for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
		labels[i], labels[j] = labels[j], labels[i]
	}
	return m.prefix + "/" + strings.Join(labels, "/")
}
