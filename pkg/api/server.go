package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/selvamani/stratonmesh/pkg/catalog"
	"github.com/selvamani/stratonmesh/pkg/importer"
	"github.com/selvamani/stratonmesh/pkg/manifest"
	"github.com/selvamani/stratonmesh/pkg/orchestrator"
	"github.com/selvamani/stratonmesh/pkg/pipeline"
	"github.com/selvamani/stratonmesh/pkg/snapshot"
	"github.com/selvamani/stratonmesh/pkg/store"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// Orchestrator is the minimal interface the API server uses.
type Orchestrator interface {
	Scale(ctx context.Context, stackID, service string, replicas int) error
	Stop(ctx context.Context, stackID string) error
	Start(ctx context.Context, stackID string) error
	Restart(ctx context.Context, stackID string) error
	Down(ctx context.Context, stackID string) error
	Destroy(ctx context.Context, stackID string) error
	Rollback(ctx context.Context, stackID string) error
	Redeploy(ctx context.Context, stackID string) error
	Status(ctx context.Context, stackID string) (*orchestrator.StackContext, error)
	ServiceInspect(ctx context.Context, stackID, service string) (*orchestrator.ServiceDetail, error)
	ServiceLogs(ctx context.Context, stackID, service string, tail int) (string, error)
	ServiceLogStream(ctx context.Context, stackID, service string) (io.ReadCloser, error)
}

// SnapshotEngine is the minimal snapshot interface the API server uses.
type SnapshotEngine interface {
	Create(ctx context.Context, stackID, label string, tags map[string]string) (*snapshot.Snapshot, error)
	List(ctx context.Context, stackID string) ([]snapshot.Snapshot, error)
	Get(ctx context.Context, stackID, snapshotID string) (*snapshot.Snapshot, error)
	Restore(ctx context.Context, stackID, snapshotID string) error
	Clone(ctx context.Context, sourceStackID, snapshotID, newStackID string) (*snapshot.Snapshot, error)
	Delete(ctx context.Context, stackID, snapshotID string) error
}

// Server exposes the StratonMesh control plane over a REST/JSON HTTP API.
//
// Snapshot routes:
//
//	POST   /v1/stacks/{id}/snapshots              create snapshot
//	GET    /v1/stacks/{id}/snapshots              list snapshots
//	GET    /v1/stacks/{id}/snapshots/{sid}        get snapshot
//	POST   /v1/stacks/{id}/snapshots/{sid}/restore restore snapshot
//	POST   /v1/stacks/{id}/snapshots/{sid}/clone  clone to new stack
//	DELETE /v1/stacks/{id}/snapshots/{sid}        delete snapshot
//
// Routes:
//
//	GET  /v1/stacks                           list all stack IDs and statuses
//	GET  /v1/stacks/{id}                      get stack status + services
//	POST /v1/stacks                           deploy a new stack (JSON body = manifest)
//	DELETE /v1/stacks/{id}                    destroy a stack
//	POST /v1/stacks/{id}/scale               scale a service
//	POST /v1/stacks/{id}/rollback            rollback to previous version
//	GET  /v1/stacks/{id}/ledger              deployment history
//
//	GET  /v1/nodes                            list cluster nodes
//
//	GET  /v1/catalog                          list blueprints
//	POST /v1/catalog/import                   import a blueprint from a Git repo
//	GET  /v1/catalog/{name}                   get a blueprint
//	POST /v1/catalog/{name}/instantiate       instantiate a blueprint with a size profile
//
//	GET  /healthz                             liveness probe
//	GET  /readyz                              readiness probe
//	GET  /v1/version                          build version info
type Server struct {
	store    *store.Store
	pipeline *pipeline.Pipeline
	orch     Orchestrator
	imp      *importer.Importer
	cat      *catalog.Engine
	snaps    SnapshotEngine // nil if MinIO not configured
	logger   *zap.SugaredLogger
	version  string
	mux      *http.ServeMux
}

// New creates a REST API Server.
func New(st *store.Store, pl *pipeline.Pipeline, orch Orchestrator, imp *importer.Importer, cat *catalog.Engine, snaps SnapshotEngine, version string, logger *zap.SugaredLogger) *Server {
	s := &Server{
		store:    st,
		pipeline: pl,
		orch:     orch,
		imp:      imp,
		cat:      cat,
		snaps:    snaps,
		version:  version,
		logger:   logger,
		mux:      http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	return withLogging(s.mux, s.logger)
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/readyz", s.handleReadyz)
	s.mux.HandleFunc("/v1/version", s.handleVersion)

	s.mux.HandleFunc("/v1/stacks", s.handleStacks)
	s.mux.HandleFunc("/v1/stacks/", s.handleStackPath)
	s.mux.HandleFunc("/v1/nodes", s.handleNodes)
	s.mux.HandleFunc("/v1/events", s.handleEvents)
	s.mux.HandleFunc("/v1/catalog", s.handleCatalog)
	s.mux.HandleFunc("/v1/catalog/import", s.handleCatalogImport)
	s.mux.HandleFunc("/v1/catalog/instantiate", s.handleCatalogInstantiateByBody)
	s.mux.HandleFunc("/v1/catalog/", s.handleCatalogItem)
}

// --- Health / meta ---

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	_, err := s.store.ListStacks(ctx)
	if err != nil {
		http.Error(w, "etcd unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ready"))
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]string{"version": s.version})
}

// --- Stacks collection ---

func (s *Server) handleStacks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		ids, err := s.store.ListStacks(ctx)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err)
			return
		}
		type entry struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		var stacks []entry
		for _, id := range ids {
			status, _ := s.store.GetStatus(ctx, id)
			stacks = append(stacks, entry{ID: id, Status: status})
		}
		if stacks == nil {
			stacks = []entry{}
		}
		jsonOK(w, map[string]interface{}{"stacks": stacks, "count": len(stacks)})

	case http.MethodPost:
		// Deploy from manifest body — accepts JSON or YAML.
		// Detect by Content-Type header; default to YAML when ambiguous.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			jsonErr(w, http.StatusBadRequest, fmt.Errorf("read body: %w", err))
			return
		}
		var stack manifest.Stack
		ct := r.Header.Get("Content-Type")
		if ct == "application/json" {
			if err := json.Unmarshal(body, &stack); err != nil {
				jsonErr(w, http.StatusBadRequest, fmt.Errorf("invalid manifest JSON: %w", err))
				return
			}
		} else {
			// YAML (or unknown content-type — try YAML first, fall back to JSON)
			if err := yaml.Unmarshal(body, &stack); err != nil {
				// Try JSON as fallback
				if err2 := json.Unmarshal(body, &stack); err2 != nil {
					jsonErr(w, http.StatusBadRequest, fmt.Errorf("invalid manifest (expected YAML or JSON): %w", err))
					return
				}
			}
		}
		if errs := manifest.Validate(&stack); len(errs) > 0 {
			jsonErr(w, http.StatusBadRequest, fmt.Errorf("validation: %v", errs))
			return
		}
		if err := s.store.SetDesired(ctx, stack.Name, &stack); err != nil {
			jsonErr(w, http.StatusInternalServerError, err)
			return
		}
		s.logger.Infow("stack deploy requested via API", "stack", stack.Name)
		if err := s.orch.Redeploy(ctx, stack.Name); err != nil {
			jsonErr(w, http.StatusInternalServerError, fmt.Errorf("deploy: %w", err))
			return
		}
		jsonOK(w, map[string]interface{}{"id": stack.Name, "status": "deploying"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Individual stack ---

func (s *Server) handleStackPath(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	path := strings.TrimPrefix(r.URL.Path, "/v1/stacks/")
	parts := strings.SplitN(strings.Trim(path, "/"), "/", 2)
	stackID := parts[0]

	if stackID == "" {
		http.NotFound(w, r)
		return
	}

	// Sub-resource?
	if len(parts) == 2 {
		sub := parts[1]
		// Handle deeper paths: services/{svc}/inspect and services/{svc}/logs
		if strings.HasPrefix(sub, "services/") {
			s.handleServiceSub(w, r, ctx, stackID, strings.TrimPrefix(sub, "services/"))
			return
		}
		if strings.HasPrefix(sub, "snapshots") {
			s.handleSnapshotPath(w, r, ctx, stackID, strings.TrimPrefix(sub, "snapshots"))
			return
		}
		switch sub {
		case "scale":
			s.handleScale(w, r, ctx, stackID)
		case "redeploy":
			s.handleRedeploy(w, r, ctx, stackID)
		case "stop":
			s.handleStop(w, r, ctx, stackID)
		case "start":
			s.handleStart(w, r, ctx, stackID)
		case "down":
			s.handleDown(w, r, ctx, stackID)
		case "restart":
			s.handleRestart(w, r, ctx, stackID)
		case "rollback":
			s.handleRollback(w, r, ctx, stackID)
		case "ledger":
			s.handleLedger(w, r, ctx, stackID)
		case "manifest":
			s.handleManifest(w, r, ctx, stackID)
		case "events":
			s.handleStackEvents(w, r, ctx, stackID)
		default:
			http.NotFound(w, r)
		}
		return
	}

	switch r.Method {
	case http.MethodGet:
		status, err := s.store.GetStatus(ctx, stackID)
		if err != nil || status == "" {
			jsonErr(w, http.StatusNotFound, fmt.Errorf("stack %q not found", stackID))
			return
		}
		var desired manifest.Stack
		s.store.GetDesired(ctx, stackID, &desired)

		// Try to get live per-service ready/health from the adapter.
		type svcStatus struct {
			Name     string `json:"name"`
			Replicas int    `json:"replicas"`
			Ready    int    `json:"ready"`
			Health   string `json:"health"`
		}
		liveByName := map[string]orchestrator.ServiceStatus{}
		if sc, err := s.orch.Status(ctx, stackID); err == nil && sc.Adapter != nil {
			if as, err := sc.Adapter.Status(ctx, stackID); err == nil {
				for _, svc := range as.Services {
					liveByName[svc.Name] = svc
				}
			}
		}
		services := make([]svcStatus, 0, len(desired.Services))
		for _, svc := range desired.Services {
			ss := svcStatus{
				Name:     svc.Name,
				Replicas: svc.Replicas,
				Ready:    0,
				Health:   "unknown",
			}
			if live, ok := liveByName[svc.Name]; ok {
				ss.Ready = live.Ready
				ss.Health = live.Health
			}
			services = append(services, ss)
		}
		jsonOK(w, map[string]interface{}{
			"id":       stackID,
			"status":   status,
			"services": services,
			"version":  desired.Version,
		})

	case http.MethodDelete:
		if err := s.orch.Destroy(ctx, stackID); err != nil {
			jsonErr(w, http.StatusInternalServerError, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleScale(w http.ResponseWriter, r *http.Request, ctx context.Context, stackID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Service  string `json:"service"`
		Replicas int    `json:"replicas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Service == "" || req.Replicas <= 0 {
		jsonErr(w, http.StatusBadRequest, fmt.Errorf("service and replicas>0 required"))
		return
	}
	if err := s.orch.Scale(ctx, stackID, req.Service, req.Replicas); err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	jsonOK(w, map[string]interface{}{"stack": stackID, "service": req.Service, "replicas": req.Replicas})
}

func (s *Server) handleRedeploy(w http.ResponseWriter, r *http.Request, ctx context.Context, stackID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.orch.Redeploy(ctx, stackID); err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	jsonOK(w, map[string]string{"stack": stackID, "status": "deploying"})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request, ctx context.Context, stackID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.orch.Stop(ctx, stackID); err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	jsonOK(w, map[string]string{"stack": stackID, "status": "stopped"})
}

func (s *Server) handleDown(w http.ResponseWriter, r *http.Request, ctx context.Context, stackID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.orch.Down(ctx, stackID); err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	jsonOK(w, map[string]string{"stack": stackID, "status": "down"})
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request, ctx context.Context, stackID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.orch.Restart(ctx, stackID); err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	jsonOK(w, map[string]string{"stack": stackID, "status": "running"})
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request, ctx context.Context, stackID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.orch.Start(ctx, stackID); err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	jsonOK(w, map[string]string{"stack": stackID, "status": "running"})
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request, ctx context.Context, stackID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.orch.Rollback(ctx, stackID); err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	jsonOK(w, map[string]string{"stack": stackID, "status": "rolling_back"})
}

func (s *Server) handleLedger(w http.ResponseWriter, r *http.Request, ctx context.Context, stackID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	entries, err := s.store.GetLedger(ctx, stackID, 20)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	jsonOK(w, map[string]interface{}{"stack": stackID, "entries": entries})
}

// handleServiceSub routes GET /v1/stacks/{id}/services/{svc}/inspect and /logs.
func (s *Server) handleServiceSub(w http.ResponseWriter, r *http.Request, ctx context.Context, stackID, sub string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// sub is like "web/inspect" or "web/logs"
	parts := strings.SplitN(sub, "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	service, action := parts[0], parts[1]
	switch action {
	case "inspect":
		detail, err := s.orch.ServiceInspect(ctx, stackID, service)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err)
			return
		}
		jsonOK(w, detail)
	case "logs":
		tail := 100
		if t := r.URL.Query().Get("tail"); t != "" {
			fmt.Sscanf(t, "%d", &tail)
		}
		logs, err := s.orch.ServiceLogs(ctx, stackID, service, tail)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(logs))
	case "logs/stream":
		s.handleServiceLogStream(w, r, ctx, stackID, service)
	default:
		http.NotFound(w, r)
	}
}

// handleServiceLogStream serves GET /v1/stacks/{id}/services/{svc}/logs/stream as SSE.
// Each log line is emitted as a Server-Sent Event: "data: <line>\n\n".
// The connection stays open until the client disconnects or the container exits.
func (s *Server) handleServiceLogStream(w http.ResponseWriter, r *http.Request, ctx context.Context, stackID, service string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported by this server", http.StatusInternalServerError)
		return
	}
	rc, err := s.orch.ServiceLogStream(ctx, stackID, service)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx/proxy buffering

	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 64*1024), 64*1024) // handle long lines
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := scanner.Text()
		// Escape newlines within a line so SSE framing is not broken.
		line = strings.ReplaceAll(line, "\n", "↵")
		fmt.Fprintf(w, "data: %s\n\n", line)
		flusher.Flush()
	}
	// Signal stream end to the client.
	fmt.Fprintf(w, "event: end\ndata: stream closed\n\n")
	flusher.Flush()
}

// handleStackEvents serves GET /v1/stacks/{id}/events.
func (s *Server) handleStackEvents(w http.ResponseWriter, r *http.Request, ctx context.Context, stackID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	events, err := s.store.ListEvents(ctx, stackID, limit)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	if events == nil {
		events = []store.OperationEvent{}
	}
	jsonOK(w, map[string]interface{}{"stack": stackID, "events": events})
}

// handleSnapshotPath routes /v1/stacks/{id}/snapshots[/{sid}[/{action}]].
// sub is everything after "snapshots" (may be "", "/{sid}", "/{sid}/restore", etc.)
func (s *Server) handleSnapshotPath(w http.ResponseWriter, r *http.Request, ctx context.Context, stackID, sub string) {
	if s.snaps == nil {
		jsonErr(w, http.StatusServiceUnavailable, fmt.Errorf("snapshot engine not configured (set SM_MINIO_ENDPOINT)"))
		return
	}
	// Trim leading slash.
	sub = strings.TrimPrefix(sub, "/")

	// No snapshot ID — list or create.
	if sub == "" {
		switch r.Method {
		case http.MethodGet:
			snaps, err := s.snaps.List(ctx, stackID)
			if err != nil {
				jsonErr(w, http.StatusInternalServerError, err)
				return
			}
			if snaps == nil {
				snaps = []snapshot.Snapshot{}
			}
			jsonOK(w, map[string]interface{}{"stack": stackID, "snapshots": snaps})
		case http.MethodPost:
			var req struct {
				Label string            `json:"label"`
				Tags  map[string]string `json:"tags"`
			}
			json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
			snap, err := s.snaps.Create(ctx, stackID, req.Label, req.Tags)
			if err != nil {
				jsonErr(w, http.StatusInternalServerError, err)
				return
			}
			jsonOK(w, snap)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// Have a snapshot ID — maybe also an action.
	parts := strings.SplitN(sub, "/", 2)
	snapshotID := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	switch action {
	case "":
		switch r.Method {
		case http.MethodGet:
			snap, err := s.snaps.Get(ctx, stackID, snapshotID)
			if err != nil {
				jsonErr(w, http.StatusNotFound, err)
				return
			}
			jsonOK(w, snap)
		case http.MethodDelete:
			if err := s.snaps.Delete(ctx, stackID, snapshotID); err != nil {
				jsonErr(w, http.StatusInternalServerError, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "restore":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := s.snaps.Restore(ctx, stackID, snapshotID); err != nil {
			jsonErr(w, http.StatusInternalServerError, err)
			return
		}
		jsonOK(w, map[string]string{"stack": stackID, "snapshot": snapshotID, "status": "restored"})
	case "clone":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			NewStackID string `json:"newStackId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NewStackID == "" {
			jsonErr(w, http.StatusBadRequest, fmt.Errorf("newStackId is required"))
			return
		}
		cloned, err := s.snaps.Clone(ctx, stackID, snapshotID, req.NewStackID)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err)
			return
		}
		jsonOK(w, cloned)
	default:
		http.NotFound(w, r)
	}
}

// handleEvents serves GET /v1/events (all stacks).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	events, err := s.store.ListAllEvents(ctx, limit)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	if events == nil {
		events = []store.OperationEvent{}
	}
	jsonOK(w, map[string]interface{}{"events": events, "count": len(events)})
}

// --- Nodes ---

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	jsonOK(w, map[string]interface{}{"nodes": nodes, "count": len(nodes)})
}

// handleManifest serves GET/PUT /v1/stacks/{id}/manifest as YAML.
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request, ctx context.Context, stackID string) {
	switch r.Method {
	case http.MethodGet:
		var stack manifest.Stack
		if err := s.store.GetDesired(ctx, stackID, &stack); err != nil {
			jsonErr(w, http.StatusNotFound, fmt.Errorf("stack %q not found", stackID))
			return
		}
		data, err := yaml.Marshal(&stack)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		w.Write(data)

	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			jsonErr(w, http.StatusBadRequest, fmt.Errorf("read body: %w", err))
			return
		}
		var stack manifest.Stack
		if err := yaml.Unmarshal(body, &stack); err != nil {
			jsonErr(w, http.StatusBadRequest, fmt.Errorf("invalid YAML: %w", err))
			return
		}
		if errs := manifest.Validate(&stack); len(errs) > 0 {
			jsonErr(w, http.StatusBadRequest, fmt.Errorf("validation: %v", errs))
			return
		}
		stack.Name = stackID // prevent rename via body
		if err := s.store.SetDesired(ctx, stackID, &stack); err != nil {
			jsonErr(w, http.StatusInternalServerError, err)
			return
		}
		s.logger.Infow("manifest updated via API, triggering redeploy", "stack", stackID)
		if err := s.orch.Redeploy(ctx, stackID); err != nil {
			jsonErr(w, http.StatusInternalServerError, fmt.Errorf("redeploy: %w", err))
			return
		}
		jsonOK(w, map[string]string{"status": "deploying"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- Catalog ---

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	blueprints, err := s.store.ListBlueprints(r.Context())
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	jsonOK(w, map[string]interface{}{"blueprints": blueprints, "count": len(blueprints)})
}

func (s *Server) handleCatalogItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/catalog/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.SplitN(path, "/", 2)
	name := parts[0]

	if len(parts) == 2 {
		switch parts[1] {
		case "instantiate":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			s.handleCatalogInstantiate(w, r, name)
		case "files":
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			s.handleCatalogFiles(w, r, name)
		case "manifest":
			s.handleCatalogManifest(w, r, name)
		default:
			http.NotFound(w, r)
		}
		return
	}

	switch r.Method {
	case http.MethodGet:
		bp, err := s.store.GetBlueprint(r.Context(), name)
		if err != nil {
			jsonErr(w, http.StatusNotFound, err)
			return
		}
		jsonOK(w, bp)

	case http.MethodDelete:
		bp, err := s.store.GetBlueprint(r.Context(), name)
		if err != nil {
			jsonErr(w, http.StatusNotFound, err)
			return
		}
		// Clean up the local repo clone if this was a repo-mode import.
		if bp.LocalPath != "" {
			if err := os.RemoveAll(bp.LocalPath); err != nil {
				s.logger.Warnw("failed to remove local repo", "path", bp.LocalPath, "error", err)
			}
		}
		if err := s.store.DeleteBlueprint(r.Context(), name); err != nil {
			jsonErr(w, http.StatusInternalServerError, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleCatalogImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req importer.ImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, fmt.Errorf("invalid request: %w", err))
		return
	}
	if req.GitURL == "" {
		jsonErr(w, http.StatusBadRequest, fmt.Errorf("gitUrl is required"))
		return
	}
	if req.Mode != importer.ImportModeRepo {
		req.Mode = importer.ImportModeCatalog
	}
	s.logger.Infow("catalog import request", "url", req.GitURL, "branch", req.Branch, "mode", req.Mode, "name", req.Name)
	result, err := s.imp.Import(r.Context(), req)
	if err != nil {
		s.logger.Errorw("catalog import failed", "url", req.GitURL, "error", err)
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	s.logger.Infow("blueprint imported via API", "name", result.Blueprint.Name, "format", result.Format)
	jsonOK(w, result)
}

// handleCatalogFiles lists deployable files in a repo-mode blueprint's local clone.
func (s *Server) handleCatalogFiles(w http.ResponseWriter, r *http.Request, name string) {
	bp, err := s.store.GetBlueprint(r.Context(), name)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err)
		return
	}
	if bp.LocalPath == "" {
		jsonOK(w, map[string]interface{}{"files": []string{}})
		return
	}

	// Walk the repo and collect recognisable deploy files.
	var files []string
	deployPatterns := []string{
		"docker-compose*.yml", "docker-compose*.yaml",
		"compose*.yml", "compose*.yaml",
		"*.tf",
		"Chart.yaml",
		"Dockerfile", "Dockerfile.*",
	}
	filepath.WalkDir(bp.LocalPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			// Skip hidden dirs and node_modules
			if d != nil && d.IsDir() && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(bp.LocalPath, path)
		base := filepath.Base(path)
		for _, pat := range deployPatterns {
			if matched, _ := filepath.Match(pat, base); matched {
				files = append(files, rel)
				break
			}
		}
		// Also include *.yaml in known k8s dirs
		if (strings.Contains(rel, "k8s") || strings.Contains(rel, "kubernetes") || strings.Contains(rel, "manifests")) &&
			(strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml")) {
			// avoid duplicates
			found := false
			for _, f := range files {
				if f == rel {
					found = true
					break
				}
			}
			if !found {
				files = append(files, rel)
			}
		}
		return nil
	})
	sort.Strings(files)
	jsonOK(w, map[string]interface{}{"files": files, "repoPath": bp.LocalPath})
}

// handleCatalogManifest handles GET and POST /v1/catalog/{name}/manifest.
//
//	GET  ?file=<relative-path>  — parse the file and return the manifest preview (JSON + YAML).
//	POST {"file":"<path>"}       — re-generate the blueprint's stored manifest from the file and
//	                              save it back to etcd so future instantiations use the new manifest.
func (s *Server) handleCatalogManifest(w http.ResponseWriter, r *http.Request, name string) {
	bp, err := s.store.GetBlueprint(r.Context(), name)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err)
		return
	}
	if bp.LocalPath == "" {
		jsonErr(w, http.StatusBadRequest, fmt.Errorf("blueprint %q has no local repo clone; re-import with mode=repo", name))
		return
	}

	var filePath string
	switch r.Method {
	case http.MethodGet:
		filePath = r.URL.Query().Get("file")
		if filePath == "" {
			jsonErr(w, http.StatusBadRequest, fmt.Errorf("file query parameter is required"))
			return
		}
	case http.MethodPost:
		var body struct {
			File string `json:"file"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.File == "" {
			jsonErr(w, http.StatusBadRequest, fmt.Errorf("body must be {\"file\":\"<relative-path>\"}"))
			return
		}
		filePath = body.File
	case http.MethodPut:
		// Save user-edited YAML directly (no re-parse from disk).
		rawYAML, err := io.ReadAll(r.Body)
		if err != nil || len(rawYAML) == 0 {
			jsonErr(w, http.StatusBadRequest, fmt.Errorf("request body must be non-empty YAML"))
			return
		}
		var stack manifest.Stack
		if err := yaml.Unmarshal(rawYAML, &stack); err != nil {
			jsonErr(w, http.StatusUnprocessableEntity, fmt.Errorf("invalid YAML: %w", err))
			return
		}
		if stack.Name == "" {
			stack.Name = bp.Name
		}
		manifestJSON, err := json.Marshal(stack)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err)
			return
		}
		bp.Manifest = string(manifestJSON)
		if err := s.store.SaveBlueprint(r.Context(), *bp); err != nil {
			jsonErr(w, http.StatusInternalServerError, fmt.Errorf("save blueprint: %w", err))
			return
		}
		s.logger.Infow("blueprint manifest saved from user edit", "blueprint", name)
		jsonOK(w, map[string]interface{}{
			"blueprint": name,
			"manifest":  stack,
			"yaml":      string(rawYAML),
		})
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	stack, err := s.imp.ParseRepoFile(bp.Name, bp.LocalPath, filePath)
	if err != nil {
		jsonErr(w, http.StatusUnprocessableEntity, fmt.Errorf("parse %s: %w", filePath, err))
		return
	}
	stack.Name = bp.Name

	yamlBytes, err := yaml.Marshal(stack)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, fmt.Errorf("marshal manifest: %w", err))
		return
	}

	if r.Method == http.MethodPost {
		// Re-encode the new stack into the blueprint and persist to etcd.
		manifestJSON, err := json.Marshal(stack)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err)
			return
		}
		bp.Manifest = string(manifestJSON)
		bp.GitPath = filePath
		if err := s.store.SaveBlueprint(r.Context(), *bp); err != nil {
			jsonErr(w, http.StatusInternalServerError, fmt.Errorf("save blueprint: %w", err))
			return
		}
		s.logger.Infow("blueprint manifest regenerated", "blueprint", name, "file", filePath)
	}

	jsonOK(w, map[string]interface{}{
		"blueprint": name,
		"file":      filePath,
		"manifest":  stack,
		"yaml":      string(yamlBytes),
	})
}

// handleCatalogInstantiateByBody handles POST /v1/catalog/instantiate with name in the JSON body.
func (s *Server) handleCatalogInstantiateByBody(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Accept both the dashboard shape { name, sizeProfile, parameters }
	// and the canonical shape { blueprintName, size, parameters }.
	var body struct {
		Name          string            `json:"name"`
		BlueprintName string            `json:"blueprintName"`
		SizeProfile   string            `json:"sizeProfile"`
		Size          string            `json:"size"`
		Platform      string            `json:"platform"`
		DeployFile    string            `json:"deployFile"`
		Parameters    map[string]string `json:"parameters"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, http.StatusBadRequest, fmt.Errorf("invalid request: %w", err))
		return
	}
	req := catalog.InstantiateRequest{
		BlueprintName: firstNonEmpty(body.BlueprintName, body.Name),
		Size:          firstNonEmpty(body.Size, body.SizeProfile),
		Platform:      body.Platform,
		DeployFile:    body.DeployFile,
		Parameters:    body.Parameters,
	}
	if req.BlueprintName == "" {
		jsonErr(w, http.StatusBadRequest, fmt.Errorf("name is required"))
		return
	}
	s.doInstantiate(w, r, req)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (s *Server) handleCatalogInstantiate(w http.ResponseWriter, r *http.Request, name string) {
	var req catalog.InstantiateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, fmt.Errorf("invalid request: %w", err))
		return
	}
	req.BlueprintName = name
	s.doInstantiate(w, r, req)
}

func (s *Server) doInstantiate(w http.ResponseWriter, r *http.Request, req catalog.InstantiateRequest) {
	ctx := r.Context()
	result, err := s.cat.Instantiate(ctx, req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}

	// For repo-mode blueprints, ensure the stack carries the local repo path and
	// uses the compose adapter — these may be lost through catalog serialisation.
	if bp, err := s.store.GetBlueprint(ctx, req.BlueprintName); err == nil {
		if bp.ImportMode == "repo" && bp.LocalPath != "" {
			if result.Stack.Platform == "" {
				result.Stack.Platform = "compose"
			}
			result.Stack.Metadata.RepoPath = bp.LocalPath
		}
	}
	if req.DeployFile != "" {
		result.Stack.Metadata.DeployFile = req.DeployFile
	}

	// Write the instantiated stack to etcd so the controller picks it up.
	if err := s.store.SetDesired(ctx, result.Stack.Name, result.Stack); err != nil {
		jsonErr(w, http.StatusInternalServerError, err)
		return
	}
	s.store.SetStatus(ctx, result.Stack.Name, "pending")
	s.logger.Infow("blueprint instantiated via API", "blueprint", req.BlueprintName, "stack", result.Stack.Name)
	jsonOK(w, map[string]interface{}{"stack": result.Stack.Name, "status": "pending"})
}

// --- Middleware ---

func withLogging(next http.Handler, logger *zap.SugaredLogger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rw, r)
		logger.Debugw("api request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.code,
			"duration", time.Since(start),
		)
	})
}

type responseWriter struct {
	http.ResponseWriter
	code int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.code = code
	rw.ResponseWriter.WriteHeader(code)
}

// --- Helpers ---

func jsonOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
