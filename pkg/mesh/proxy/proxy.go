package proxy

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Route defines how traffic is directed for a given host/path.
type Route struct {
	// Host matches the incoming Host header (e.g., "api.myapp.stratonmesh.local").
	Host string `yaml:"host" json:"host"`
	// Prefix matches the URL path prefix (e.g., "/api").
	Prefix string `yaml:"prefix" json:"prefix"`
	// Backends is the stable backend pool.
	Backends []Backend `yaml:"backends" json:"backends"`
	// Canary, if set, receives CanaryWeight% of requests.
	Canary *CanaryConfig `yaml:"canary,omitempty" json:"canary,omitempty"`
}

// Backend is an upstream target.
type Backend struct {
	Address string `yaml:"address" json:"address"` // "host:port"
	Weight  int    `yaml:"weight" json:"weight"`   // 0-100, defaults to 100
	Healthy bool   `yaml:"healthy" json:"healthy"`
}

// CanaryConfig defines a traffic split for canary deployments.
type CanaryConfig struct {
	Backends []Backend `yaml:"backends" json:"backends"`
	// Weight is the percentage of requests to send to canary (0-100).
	Weight int `yaml:"weight" json:"weight"`
	// Header, if non-empty, forces canary routing when present (regardless of Weight).
	Header string `yaml:"header,omitempty" json:"header,omitempty"`
}

// Proxy is an HTTP reverse proxy with canary routing and health-based load balancing.
type Proxy struct {
	mu     sync.RWMutex
	routes map[string]*Route // key: host or host+prefix

	healthChecker *healthChecker
	logger        *zap.SugaredLogger
}

// New creates a Proxy.
func New(logger *zap.SugaredLogger) *Proxy {
	p := &Proxy{
		routes: make(map[string]*Route),
		logger: logger,
	}
	p.healthChecker = newHealthChecker(p, logger)
	return p
}

// AddRoute registers or replaces a routing rule.
func (p *Proxy) AddRoute(r Route) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := routeKey(r.Host, r.Prefix)
	p.routes[key] = &r
	p.logger.Infow("route added", "host", r.Host, "prefix", r.Prefix, "backends", len(r.Backends))
}

// RemoveRoute deletes a routing rule.
func (p *Proxy) RemoveRoute(host, prefix string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.routes, routeKey(host, prefix))
}

// ListRoutes returns a snapshot of all current routes.
func (p *Proxy) ListRoutes() []Route {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Route, 0, len(p.routes))
	for _, r := range p.routes {
		out = append(out, *r)
	}
	return out
}

// StartHealthChecks begins background health polling for all backends.
// Call this once after all initial routes are added.
func (p *Proxy) StartHealthChecks(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	p.healthChecker.start(ctx, interval)
}

// Handler returns the http.Handler for the proxy.
func (p *Proxy) Handler() http.Handler {
	return http.HandlerFunc(p.serveHTTP)
}

func (p *Proxy) serveHTTP(w http.ResponseWriter, r *http.Request) {
	route := p.matchRoute(r)
	if route == nil {
		http.Error(w, "no route found for "+r.Host+r.URL.Path, http.StatusBadGateway)
		return
	}

	backend, isCanary := p.selectBackend(route, r)
	if backend == nil {
		http.Error(w, "no healthy backend available", http.StatusServiceUnavailable)
		return
	}

	if isCanary {
		w.Header().Set("X-Straton-Canary", "true")
	}

	target := &url.URL{
		Scheme: "http",
		Host:   backend.Address,
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		p.logger.Warnw("upstream error", "backend", backend.Address, "error", err)
		// Mark backend unhealthy and retry with next
		p.markUnhealthy(route, backend.Address)
		http.Error(w, "upstream error", http.StatusBadGateway)
	}

	p.logger.Debugw("proxying request",
		"host", r.Host,
		"path", r.URL.Path,
		"backend", backend.Address,
		"canary", isCanary,
	)

	proxy.ServeHTTP(w, r)
}

// matchRoute finds the most specific route for the request.
func (p *Proxy) matchRoute(r *http.Request) *Route {
	p.mu.RLock()
	defer p.mu.RUnlock()

	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	// Try host+prefix first (most specific)
	if route, ok := p.routes[routeKey(host, r.URL.Path)]; ok {
		return route
	}

	// Try host+prefix prefix match
	for _, route := range p.routes {
		if route.Host == host && route.Prefix != "" && strings.HasPrefix(r.URL.Path, route.Prefix) {
			return route
		}
	}

	// Try host only
	if route, ok := p.routes[routeKey(host, "")]; ok {
		return route
	}

	// Wildcard
	if route, ok := p.routes[routeKey("*", "")]; ok {
		return route
	}

	return nil
}

// selectBackend picks a backend using weighted random, with canary splitting.
func (p *Proxy) selectBackend(route *Route, r *http.Request) (*Backend, bool) {
	// Forced canary via header
	if route.Canary != nil && route.Canary.Header != "" && r.Header.Get(route.Canary.Header) != "" {
		if b := pickHealthy(route.Canary.Backends); b != nil {
			return b, true
		}
	}

	// Weighted canary split
	if route.Canary != nil && route.Canary.Weight > 0 {
		if rand.Intn(100) < route.Canary.Weight {
			if b := pickHealthy(route.Canary.Backends); b != nil {
				return b, true
			}
		}
	}

	return pickHealthy(route.Backends), false
}

// pickHealthy selects a healthy backend from a pool using weighted random.
func pickHealthy(backends []Backend) *Backend {
	var healthy []Backend
	for _, b := range backends {
		if b.Healthy {
			healthy = append(healthy, b)
		}
	}
	if len(healthy) == 0 {
		// Fallback: use all if none are marked healthy (startup race)
		healthy = backends
	}
	if len(healthy) == 0 {
		return nil
	}

	total := 0
	for _, b := range healthy {
		w := b.Weight
		if w <= 0 {
			w = 100
		}
		total += w
	}
	n := rand.Intn(max(total, 1))
	for i := range healthy {
		w := healthy[i].Weight
		if w <= 0 {
			w = 100
		}
		n -= w
		if n < 0 {
			return &healthy[i]
		}
	}
	return &healthy[0]
}

func (p *Proxy) markUnhealthy(route *Route, address string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range route.Backends {
		if route.Backends[i].Address == address {
			route.Backends[i].Healthy = false
		}
	}
	if route.Canary != nil {
		for i := range route.Canary.Backends {
			if route.Canary.Backends[i].Address == address {
				route.Canary.Backends[i].Healthy = false
			}
		}
	}
}

func routeKey(host, prefix string) string {
	return host + "|" + prefix
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// --- Background health checker ---

type healthChecker struct {
	proxy  *Proxy
	logger *zap.SugaredLogger
}

func newHealthChecker(p *Proxy, logger *zap.SugaredLogger) *healthChecker {
	return &healthChecker{proxy: p, logger: logger}
}

func (hc *healthChecker) start(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				hc.checkAll(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
	hc.logger.Infow("health checker started", "interval", interval)
}

func (hc *healthChecker) checkAll(ctx context.Context) {
	hc.proxy.mu.Lock()
	defer hc.proxy.mu.Unlock()

	client := &http.Client{Timeout: 2 * time.Second}
	for _, route := range hc.proxy.routes {
		checkPool(ctx, client, route.Backends, hc.logger)
		if route.Canary != nil {
			checkPool(ctx, client, route.Canary.Backends, hc.logger)
		}
	}
}

func checkPool(ctx context.Context, client *http.Client, backends []Backend, logger *zap.SugaredLogger) {
	for i := range backends {
		addr := backends[i].Address
		healthy := probe(ctx, client, addr)
		if backends[i].Healthy != healthy {
			logger.Infow("backend health changed",
				"address", addr,
				"healthy", healthy,
			)
		}
		backends[i].Healthy = healthy
	}
}

func probe(ctx context.Context, client *http.Client, address string) bool {
	// Try TCP connect first (fast)
	d := net.Dialer{Timeout: 1 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return false
	}
	conn.Close()

	// Then try HTTP /healthz or /
	u := fmt.Sprintf("http://%s/healthz", address)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return true // TCP OK, just can't form request
	}
	resp, err := client.Do(req)
	if err != nil {
		return true // TCP OK, endpoint may not have /healthz
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}
