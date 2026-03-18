package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/selvamani/stratonmesh/internal/logger"
	"github.com/selvamani/stratonmesh/internal/version"
	smapi "github.com/selvamani/stratonmesh/pkg/api"
	"github.com/selvamani/stratonmesh/pkg/adapters/docker"
	"github.com/selvamani/stratonmesh/pkg/adapters/compose"
	k8sadapter "github.com/selvamani/stratonmesh/pkg/adapters/kubernetes"
	"github.com/selvamani/stratonmesh/pkg/adapters/terraform"
	"github.com/selvamani/stratonmesh/pkg/adapters/pulumi"
	"github.com/selvamani/stratonmesh/pkg/adapters/process"
	"github.com/selvamani/stratonmesh/pkg/catalog"
	"github.com/selvamani/stratonmesh/pkg/importer"
	"github.com/selvamani/stratonmesh/pkg/orchestrator"
	"github.com/selvamani/stratonmesh/pkg/pipeline"
	"github.com/selvamani/stratonmesh/pkg/store"
	"github.com/selvamani/stratonmesh/pkg/telemetry"
)

func main() {
	log := logger.New("production")
	log.Infow("StratonMesh API server starting", "version", version.Version)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	st, err := store.New(store.Config{
		Endpoints: []string{envOr("SM_ETCD", "localhost:2379")},
	}, log)
	if err != nil {
		log.Fatalw("etcd connect failed", "error", err)
	}
	defer st.Close()

	bus, _ := telemetry.New(telemetry.Config{URL: envOr("SM_NATS", "nats://localhost:4222")}, log)
	if bus != nil {
		defer bus.Close()
	}

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

	pl := pipeline.New(st, bus, log)
	imp := importer.New(st, log)
	cat := catalog.New(st, log)

	ver := version.Version + " (commit: " + version.GitCommit + ")"
	srv := smapi.New(st, pl, orch, imp, cat, nil, ver, log)

	addr := envOr("SM_API_ADDR", ":8080")
	httpSrv := &http.Server{
		Addr:    addr,
		Handler: srv.Handler(),
	}

	go func() {
		log.Infow("API server listening", "addr", addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalw("API server failed", "error", err)
		}
	}()

	<-ctx.Done()
	log.Info("API server shutting down")
	httpSrv.Shutdown(context.Background())
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
