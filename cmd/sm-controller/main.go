package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/selvamani/stratonmesh/internal/logger"
	"github.com/selvamani/stratonmesh/internal/version"
	smapi "github.com/selvamani/stratonmesh/pkg/api"
	"github.com/selvamani/stratonmesh/pkg/adapters/compose"
	"github.com/selvamani/stratonmesh/pkg/adapters/docker"
	k8sadapter "github.com/selvamani/stratonmesh/pkg/adapters/kubernetes"
	"github.com/selvamani/stratonmesh/pkg/adapters/mesos"
	"github.com/selvamani/stratonmesh/pkg/adapters/nomad"
	"github.com/selvamani/stratonmesh/pkg/adapters/process"
	"github.com/selvamani/stratonmesh/pkg/adapters/pulumi"
	"github.com/selvamani/stratonmesh/pkg/adapters/swarm"
	"github.com/selvamani/stratonmesh/pkg/adapters/terraform"
	"github.com/selvamani/stratonmesh/pkg/autoscaler"
	"github.com/selvamani/stratonmesh/pkg/catalog"
	"github.com/selvamani/stratonmesh/pkg/importer"
	"github.com/selvamani/stratonmesh/pkg/orchestrator"
	"github.com/selvamani/stratonmesh/pkg/pipeline"
	"github.com/selvamani/stratonmesh/pkg/snapshot"
	"github.com/selvamani/stratonmesh/pkg/store"
	"github.com/selvamani/stratonmesh/pkg/telemetry"
)

func main() {
	log := logger.New("production")
	log.Infow("StratonMesh controller starting", "version", version.Version)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// --- Store (etcd) ---
	st, err := store.New(store.Config{
		Endpoints: []string{envOr("SM_ETCD", "localhost:2379")},
	}, log)
	if err != nil {
		log.Fatalw("etcd connect failed", "error", err)
	}
	defer st.Close()

	// --- Telemetry (NATS) ---
	bus, _ := telemetry.New(telemetry.Config{URL: envOr("SM_NATS", "nats://localhost:4222")}, log)
	if bus != nil {
		defer bus.Close()
	}

	// --- Orchestrator ---
	orch := orchestrator.New(st, log)

	// Register all platform adapters
	if da, err := docker.New(log); err == nil {
		orch.RegisterAdapter(da)
	}
	orch.RegisterAdapter(compose.New(log, ""))
	orch.RegisterAdapter(swarm.New(log, ""))
	orch.RegisterAdapter(k8sadapter.New(log, ""))
	orch.RegisterAdapter(terraform.New(log, "aws"))
	orch.RegisterAdapter(pulumi.New(log))
	orch.RegisterAdapter(process.New(log))
	orch.RegisterAdapter(nomad.New(log, envOr("SM_NOMAD_ADDR", "")))
	orch.RegisterAdapter(mesos.New(log, envOr("SM_MARATHON_URL", "")))

	// Start the reconciliation loop (watches etcd + 30s periodic tick)
	orch.StartReconcileLoop(ctx)

	// --- Auto-scaler ---
	if bus != nil {
		scaler := autoscaler.New(st, bus, orch, autoscaler.Config{
			Cooldown:           2 * time.Minute,
			EvalInterval:       15 * time.Second,
			ScaleDownThreshold: 3,
		}, log)
		if err := scaler.Start(ctx); err != nil {
			log.Warnw("auto-scaler failed to start", "error", err)
		} else {
			log.Info("auto-scaler started")
		}
	}

	// --- REST API server ---
	pl := pipeline.New(st, bus, log)
	imp := importer.New(st, log)
	if reposDir := envOr("SM_REPOS_DIR", ""); reposDir != "" {
		imp.ReposDir = reposDir
	}
	cat := catalog.New(st, log)

	// --- Snapshot engine (optional — requires MinIO) ---
	var snaps smapi.SnapshotEngine
	if minioEndpoint := envOr("SM_MINIO_ENDPOINT", ""); minioEndpoint != "" {
		engine, err := snapshot.New(snapshot.Config{
			Endpoint:        minioEndpoint,
			AccessKeyID:     envOr("SM_MINIO_ACCESS_KEY", "stratonmesh"),
			SecretAccessKey: envOr("SM_MINIO_SECRET_KEY", "stratonmesh123"),
			UseSSL:          envOr("SM_MINIO_SSL", "") == "true",
		}, st, log)
		if err != nil {
			log.Warnw("snapshot engine disabled (MinIO connect failed)", "error", err)
		} else {
			snaps = engine
			log.Infow("snapshot engine enabled", "endpoint", minioEndpoint)
		}
	}

	ver := version.Version + " (commit: " + version.GitCommit + ")"
	apiSrv := smapi.New(st, pl, orch, imp, cat, snaps, ver, log)

	apiAddr := envOr("SM_API_ADDR", ":8080")
	httpSrv := &http.Server{
		Addr:    apiAddr,
		Handler: apiSrv.Handler(),
	}
	go func() {
		log.Infow("REST API server listening", "addr", apiAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Warnw("REST API server error", "error", err)
		}
	}()

	log.Info("controller running — watching etcd for desired state changes")
	<-ctx.Done()

	log.Info("controller shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	httpSrv.Shutdown(shutdownCtx)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
