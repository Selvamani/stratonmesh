package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/selvamani/stratonmesh/internal/logger"
	"github.com/selvamani/stratonmesh/pkg/store"
	"github.com/selvamani/stratonmesh/pkg/telemetry"
)

func main() {
	log := logger.New("production")
	log.Info("StratonMesh agent starting")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	st, err := store.New(store.Config{Endpoints: []string{envOr("SM_ETCD", "localhost:2379")}}, log)
	if err != nil { log.Fatalw("etcd connect failed", "error", err) }
	defer st.Close()

	bus, _ := telemetry.New(telemetry.Config{URL: envOr("SM_NATS", "nats://localhost:4222")}, log)
	if bus != nil { defer bus.Close() }

	nodeID   := envOr("SM_NODE_ID", hostname())
	nodeName := envOr("SM_NODE_NAME", hostname())

	// Prometheus metrics — scraped by VictoriaMetrics
	labels := prometheus.Labels{"node_id": nodeID, "node_name": nodeName}
	cpuGauge := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratonmesh_node_cpu_percent",
		Help: "Node CPU usage percentage",
	}, []string{"node_id", "node_name"}).With(labels)
	memGauge := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratonmesh_node_memory_percent",
		Help: "Node memory usage percentage",
	}, []string{"node_id", "node_name"}).With(labels)
	nodeInfo := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "stratonmesh_node_info",
		Help: "Node registration info (value always 1)",
	}, []string{"node_id", "node_name", "os", "region"}).With(prometheus.Labels{
		"node_id": nodeID, "node_name": nodeName,
		"os": runtime.GOOS, "region": envOr("SM_REGION", "local"),
	})
	nodeInfo.Set(1)

	metricsAddr := envOr("SM_METRICS_ADDR", ":9091")
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsSrv := &http.Server{Addr: metricsAddr, Handler: metricsMux}
	go func() {
		log.Infow("metrics endpoint listening", "addr", metricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Warnw("metrics server error", "error", err)
		}
	}()

	register := func() {
		cpuTotal, cpuFree := cpuMillicores()
		memTotal, memFree := memBytes()
		st.RegisterNode(ctx, store.NodeInfo{
			ID:        nodeID,
			Name:      nodeName,
			OS:        runtime.GOOS,
			Providers: detectProviders(),
			CPUTotal:  cpuTotal,
			CPUFree:   cpuFree,
			MemTotal:  memTotal,
			MemFree:   memFree,
			Status:    "healthy",
			Region:    envOr("SM_REGION", "local"),
			LastSeen:  time.Now(),
		})
	}

	// Register immediately, then every 10 s.
	register()
	go func() {
		for { select {
		case <-time.After(10 * time.Second):
			register()
		case <-ctx.Done(): return
		}}
	}()

	// Collect and publish metrics every 15 s — to both NATS and Prometheus gauges.
	go func() {
		for { select {
		case <-time.After(15 * time.Second):
			cpuTotal, cpuFree := cpuMillicores()
			memTotal, memFree := memBytes()
			cpuUsedPct := 0.0
			if cpuTotal > 0 {
				cpuUsedPct = float64(cpuTotal-cpuFree) / float64(cpuTotal) * 100
			}
			memUsedPct := 0.0
			if memTotal > 0 {
				memUsedPct = float64(memTotal-memFree) / float64(memTotal) * 100
			}
			cpuGauge.Set(cpuUsedPct)
			memGauge.Set(memUsedPct)
			if bus != nil {
				bus.PublishMetric(ctx, telemetry.MetricPoint{Node: nodeID, Name: "cpu",    Value: cpuUsedPct, Unit: "percent"})
				bus.PublishMetric(ctx, telemetry.MetricPoint{Node: nodeID, Name: "memory", Value: memUsedPct, Unit: "percent"})
			}
		case <-ctx.Done(): return
		}}
	}()

	log.Infow("agent running", "node", nodeID, "os", runtime.GOOS)
	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	metricsSrv.Shutdown(shutdownCtx)
}

// cpuMillicores returns total and free CPU in millicores using real host metrics.
func cpuMillicores() (total, free int64) {
	numCPU := int64(runtime.NumCPU())
	total = numCPU * 1000
	pcts, err := cpu.Percent(200*time.Millisecond, false)
	if err != nil || len(pcts) == 0 {
		free = total * 6 / 10 // fallback: assume 40% used
		return
	}
	usedPct := pcts[0] / 100.0
	free = total - int64(float64(total)*usedPct)
	return
}

// memBytes returns total and free memory in bytes using real host metrics.
func memBytes() (total, free int64) {
	v, err := mem.VirtualMemory()
	if err != nil {
		total = 8 << 30
		free = 4 << 30
		return
	}
	total = int64(v.Total)
	free = int64(v.Available)
	return
}

func detectProviders() []string {
	p := []string{"process"}
	if _, err := os.Stat("/var/run/docker.sock"); err == nil { p = append(p, "docker") }
	return p
}
func hostname() string { h, _ := os.Hostname(); return h }
func envOr(k, d string) string { if v := os.Getenv(k); v != "" { return v }; return d }
