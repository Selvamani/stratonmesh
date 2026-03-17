package sidecar

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Identity holds the SPIFFE-style workload identity and its TLS material.
// SPIFFE URI format: spiffe://{trust-domain}/stack/{stack}/service/{service}
type Identity struct {
	TrustDomain string
	Stack       string
	Service     string
	Instance    string

	Cert       *x509.Certificate
	PrivateKey *ecdsa.PrivateKey
	CACert     *x509.Certificate

	// PEM-encoded copies for serialisation / mounting
	CertPEM []byte
	KeyPEM  []byte
	CAPEM   []byte
}

// SPIFFEID returns the SPIFFE URI for this identity.
func (id *Identity) SPIFFEID() string {
	return fmt.Sprintf("spiffe://%s/stack/%s/service/%s", id.TrustDomain, id.Stack, id.Service)
}

// TLSConfig returns a *tls.Config suitable for mutual-TLS between sidecars.
func (id *Identity) TLSConfig(server bool) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("load key pair: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(id.CACert)

	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}
	if server {
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

// Manager is the CA and identity manager for the sidecar mesh.
// In production, delegate to Vault PKI or cert-manager; this implementation
// is self-contained for development and small deployments.
type Manager struct {
	mu          sync.RWMutex
	trustDomain string
	caCert      *x509.Certificate
	caKey       *ecdsa.PrivateKey
	caPEM       []byte
	identities  map[string]*Identity // key: stack/service
	logger      *zap.SugaredLogger
}

// Config holds sidecar manager settings.
type Config struct {
	TrustDomain string `yaml:"trustDomain" json:"trustDomain"` // default "stratonmesh.local"
	// CACertPEM / CAKeyPEM: supply to use an existing CA.
	// Leave empty to auto-generate a self-signed CA on first run.
	CACertPEM string `yaml:"caCertPem,omitempty" json:"caCertPem,omitempty"`
	CAKeyPEM  string `yaml:"caKeyPem,omitempty" json:"caKeyPem,omitempty"`
}

// NewManager creates a sidecar Manager, generating a CA if none is provided.
func NewManager(cfg Config, logger *zap.SugaredLogger) (*Manager, error) {
	if cfg.TrustDomain == "" {
		cfg.TrustDomain = "stratonmesh.local"
	}

	m := &Manager{
		trustDomain: cfg.TrustDomain,
		identities:  make(map[string]*Identity),
		logger:      logger,
	}

	if cfg.CACertPEM != "" && cfg.CAKeyPEM != "" {
		if err := m.loadCA(cfg.CACertPEM, cfg.CAKeyPEM); err != nil {
			return nil, fmt.Errorf("load CA: %w", err)
		}
		logger.Infow("sidecar manager: loaded existing CA", "trustDomain", cfg.TrustDomain)
	} else {
		if err := m.generateCA(); err != nil {
			return nil, fmt.Errorf("generate CA: %w", err)
		}
		logger.Infow("sidecar manager: generated self-signed CA", "trustDomain", cfg.TrustDomain)
	}

	return m, nil
}

// Issue creates (or renews) a workload certificate for the given stack/service/instance.
// TTL defaults to 24h if zero. Certificates are cached by stack+service key.
func (m *Manager) Issue(stack, service, instance string, ttl time.Duration) (*Identity, error) {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	id, err := m.issueInternal(stack, service, instance, ttl)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.identities[stack+"/"+service] = id
	m.mu.Unlock()

	m.logger.Infow("certificate issued",
		"spiffe", id.SPIFFEID(),
		"instance", instance,
		"expires", time.Now().Add(ttl).Format(time.RFC3339),
	)
	return id, nil
}

// Get returns a cached identity, or nil if not found.
func (m *Manager) Get(stack, service string) *Identity {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.identities[stack+"/"+service]
}

// Revoke removes a workload identity from the cache.
func (m *Manager) Revoke(stack, service string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := stack + "/" + service
	if _, ok := m.identities[key]; ok {
		delete(m.identities, key)
		m.logger.Infow("certificate revoked", "stack", stack, "service", service)
	}
}

// CACertPEM returns the PEM-encoded CA certificate for distribution to workloads.
func (m *Manager) CACertPEM() []byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.caPEM
}

// --- Internal ---

func (m *Manager) issueInternal(stack, service, instance string, ttl time.Duration) (*Identity, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	spiffeURL := &url.URL{
		Scheme: "spiffe",
		Host:   m.trustDomain,
		Path:   "/stack/" + stack + "/service/" + service,
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization:       []string{"StratonMesh"},
			OrganizationalUnit: []string{stack},
			CommonName:         fmt.Sprintf("%s.%s.%s", instance, service, stack),
		},
		NotBefore:             now.Add(-1 * time.Minute),
		NotAfter:              now.Add(ttl),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{spiffeURL},
		DNSNames: []string{
			fmt.Sprintf("%s.%s.stratonmesh.local", service, stack),
			fmt.Sprintf("%s.%s.%s.stratonmesh.local", instance, service, stack),
		},
		IPAddresses: []net.IP{},
	}

	m.mu.RLock()
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, m.caCert, &priv.PublicKey, m.caKey)
	m.mu.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("sign certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	m.mu.RLock()
	caPEM := m.caPEM
	caCert := m.caCert
	m.mu.RUnlock()

	return &Identity{
		TrustDomain: m.trustDomain,
		Stack:       stack,
		Service:     service,
		Instance:    instance,
		Cert:        cert,
		PrivateKey:  priv,
		CACert:      caCert,
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
		CAPEM:       caPEM,
	}, nil
}

func (m *Manager) generateCA() error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"StratonMesh"}, CommonName: "StratonMesh CA — " + m.trustDomain},
		NotBefore:             now.Add(-1 * time.Minute),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return err
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return err
	}

	m.caCert = cert
	m.caKey = priv
	m.caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return nil
}

func (m *Manager) loadCA(certPEM, keyPEM string) error {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return fmt.Errorf("invalid CA cert PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}

	keyBlock, _ := pem.Decode([]byte(keyPEM))
	if keyBlock == nil {
		return fmt.Errorf("invalid CA key PEM")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return err
	}

	m.caCert = cert
	m.caKey = key
	m.caPEM = []byte(certPEM)
	return nil
}
