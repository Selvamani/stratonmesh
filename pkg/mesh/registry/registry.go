package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// Endpoint represents a live service instance returned by the registry API.
type Endpoint struct {
	Service      string            `json:"service"`
	Stack        string            `json:"stack"`
	Instance     string            `json:"instance"`
	Address      string            `json:"address"`  // "ip:port"
	Health       string            `json:"health"`   // healthy, unhealthy, unknown
	Version      string            `json:"version"`
	Weight       int               `json:"weight"`
	Labels       map[string]string `json:"labels,omitempty"`
	RegisteredAt time.Time         `json:"registeredAt"`
}

// EndpointStore is the minimal interface the registry needs from the StratonMesh store.
type EndpointStore interface {
	GetServiceEndpoints(ctx context.Context, service, stack string) ([]RawEndpoint, error)
	ListAllEndpoints(ctx context.Context) ([]RawEndpoint, error)
	RegisterService(ctx context.Context, ep RawEndpoint) error
	DeregisterService(ctx context.Context, service, stack, instance string) error
}

// RawEndpoint mirrors store.ServiceEndpoint to avoid an import cycle.
type RawEndpoint struct {
	Service      string            `json:"service"`
	Stack        string            `json:"stack"`
	Instance     string            `json:"instance"`
	Endpoint     string            `json:"endpoint"`
	Node         string            `json:"node"`
	Version      string            `json:"version"`
	Health       string            `json:"health"`
	Weight       int               `json:"weight"`
	Labels       map[string]string `json:"labels,omitempty"`
	RegisteredAt time.Time         `json:"registeredAt"`
}

// Server exposes the service registry over HTTP.
//
// Routes:
//
//	GET  /v1/services                        list all service names
//	GET  /v1/services/{service}              list endpoints across all stacks
//	GET  /v1/services/{service}/{stack}      list endpoints for a specific stack
//	GET  /v1/services/{service}/{stack}/resolve  pick one healthy endpoint (load-balanced)
//	POST /v1/services                        register an endpoint
//	DELETE /v1/services/{service}/{stack}/{instance}  deregister
type Server struct {
	store  EndpointStore
	logger *zap.SugaredLogger
	mux    *http.ServeMux
}

// New creates a registry Server.
func New(store EndpointStore, logger *zap.SugaredLogger) *Server {
	s := &Server{
		store:  store,
		logger: logger,
		mux:    http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

// Handler returns the HTTP handler (suitable for http.ListenAndServe).
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("/v1/services", s.handleServices)
	s.mux.HandleFunc("/v1/services/", s.handleServicePath)
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
}

// handleServices: GET /v1/services (list all) or POST /v1/services (register)
func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		eps, err := s.store.ListAllEndpoints(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Deduplicate service names
		seen := make(map[string]bool)
		var names []string
		for _, ep := range eps {
			if !seen[ep.Service] {
				seen[ep.Service] = true
				names = append(names, ep.Service)
			}
		}
		jsonResponse(w, http.StatusOK, map[string]interface{}{"services": names, "count": len(names)})

	case http.MethodPost:
		var ep RawEndpoint
		if err := json.NewDecoder(r.Body).Decode(&ep); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if ep.Service == "" || ep.Stack == "" || ep.Instance == "" {
			http.Error(w, "service, stack, and instance are required", http.StatusBadRequest)
			return
		}
		ep.RegisteredAt = time.Now()
		if err := s.store.RegisterService(ctx, ep); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.logger.Infow("endpoint registered", "service", ep.Service, "stack", ep.Stack, "instance", ep.Instance)
		jsonResponse(w, http.StatusCreated, ep)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleServicePath routes /v1/services/{service}[/{stack}[/{instance}[/resolve]]]
func (s *Server) handleServicePath(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Trim prefix and split
	path := strings.TrimPrefix(r.URL.Path, "/v1/services/")
	parts := strings.Split(strings.Trim(path, "/"), "/")

	switch len(parts) {
	case 1:
		// GET /v1/services/{service}
		s.getServiceEndpoints(w, r, ctx, parts[0], "")

	case 2:
		// GET /v1/services/{service}/{stack}
		s.getServiceEndpoints(w, r, ctx, parts[0], parts[1])

	case 3:
		if parts[2] == "resolve" {
			// GET /v1/services/{service}/{stack}/resolve
			s.resolveEndpoint(w, r, ctx, parts[0], parts[1])
		} else if r.Method == http.MethodDelete {
			// DELETE /v1/services/{service}/{stack}/{instance}
			s.deleteEndpoint(w, r, ctx, parts[0], parts[1], parts[2])
		} else {
			http.NotFound(w, r)
		}

	default:
		http.NotFound(w, r)
	}
}

func (s *Server) getServiceEndpoints(w http.ResponseWriter, r *http.Request, ctx context.Context, service, stack string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	eps, err := s.store.GetServiceEndpoints(ctx, service, stack)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	healthOnly := r.URL.Query().Get("healthy") == "true"
	var result []Endpoint
	for _, ep := range eps {
		if healthOnly && ep.Health != "healthy" {
			continue
		}
		result = append(result, toEndpoint(ep))
	}
	if result == nil {
		result = []Endpoint{}
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{"endpoints": result, "count": len(result)})
}

func (s *Server) resolveEndpoint(w http.ResponseWriter, r *http.Request, ctx context.Context, service, stack string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	eps, err := s.store.GetServiceEndpoints(ctx, service, stack)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Filter to healthy only
	var healthy []RawEndpoint
	for _, ep := range eps {
		if ep.Health == "healthy" {
			healthy = append(healthy, ep)
		}
	}
	if len(healthy) == 0 {
		http.Error(w, fmt.Sprintf("no healthy endpoints for %s/%s", service, stack), http.StatusServiceUnavailable)
		return
	}

	// Weighted random selection
	chosen := weightedRandom(healthy)
	jsonResponse(w, http.StatusOK, toEndpoint(chosen))
}

func (s *Server) deleteEndpoint(w http.ResponseWriter, r *http.Request, ctx context.Context, service, stack, instance string) {
	if err := s.store.DeregisterService(ctx, service, stack, instance); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.logger.Infow("endpoint deregistered", "service", service, "stack", stack, "instance", instance)
	w.WriteHeader(http.StatusNoContent)
}

// weightedRandom selects an endpoint proportional to its Weight field.
func weightedRandom(eps []RawEndpoint) RawEndpoint {
	total := 0
	for _, ep := range eps {
		w := ep.Weight
		if w <= 0 {
			w = 100
		}
		total += w
	}
	n := rand.Intn(total)
	for _, ep := range eps {
		w := ep.Weight
		if w <= 0 {
			w = 100
		}
		n -= w
		if n < 0 {
			return ep
		}
	}
	return eps[0]
}

func toEndpoint(r RawEndpoint) Endpoint {
	return Endpoint{
		Service:      r.Service,
		Stack:        r.Stack,
		Instance:     r.Instance,
		Address:      r.Endpoint,
		Health:       r.Health,
		Version:      r.Version,
		Weight:       r.Weight,
		Labels:       r.Labels,
		RegisteredAt: r.RegisteredAt,
	}
}

func jsonResponse(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
