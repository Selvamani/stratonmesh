package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/selvamani/stratonmesh/internal/logger"
)

func main() {
	log := logger.New("production")
	log.Info("StratonMesh proxy starting")
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	log.Info("proxy running — TODO: reverse proxy with service registry routing")
	<-ctx.Done()
}
