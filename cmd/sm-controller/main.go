package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stratonmesh/stratonmesh/internal/logger"
	"github.com/stratonmesh/stratonmesh/internal/version"
	smapi "github.com/stratonmesh/stratonmesh/pkg/api"
	"github.com/stratonmesh/stratonmesh/pkg/adapters/compose"
	"github.com/stratonmesh/stratonmesh/pkg/adapters/docker"
	k8sadapter "github.com/stratonmesh/stratonmesh/pkg/adapters/kubernetes"
	"github.com/stratonmesh/stratonmesh/pkg/adapters/process"
	"github.com/stratonmesh/stratonmesh/pkg/adapters/pulumi"
	"github.com/stratonmesh/stratonmesh/pkg/adapters/terraform"
	"github.com/stratonmesh/stratonmesh/pkg/autoscaler"
	"github.com/stratonmesh/stratonmesh/pkg/catalog"
	"github.com/stratonmesh/stratonmesh/pkg/importer"
	"github.com/stratonmesh/stratonmesh/pkg/orchestrator"
	"github.com/stratonmesh/stratonmesh/pkg/pipeline"
	"github.com/stratonmesh/stratonmesh/pkg/store"
	"github.com/stratonmesh/stratonmesh/pkg/telemetry"
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
	orch.RegisterAdapter(k8sadapter.New(log, ""))
	orch.RegisterAdapter(terraform.New(log, "aws"))
	orch.RegisterAdapter(pulumi.New(log))
	orch.RegisterAdapter(process.New(log))

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
	cat := catalog.New(st, log)
	ver := version.Version + " (commit: " + version.GitCommit + ")"
	apiSrv := smapi.New(st, pl, orch, imp, cat, ver, log)

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
