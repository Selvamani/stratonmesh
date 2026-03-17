package scheduler

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/stratonmesh/stratonmesh/pkg/manifest"
	"github.com/stratonmesh/stratonmesh/pkg/store"
	"go.uber.org/zap"
)

// Scheduler handles placement decisions for service instances across nodes.
type Scheduler struct {
	store   *store.Store
	logger  *zap.SugaredLogger
	weights ScoringWeights
}

// ScoringWeights controls the relative importance of each scorer (0-100, should sum to 100).
type ScoringWeights struct {
	BinPack  float64 `yaml:"binPack" json:"binPack"`   // consolidate workloads
	Spread   float64 `yaml:"spread" json:"spread"`      // distribute for HA
	Affinity float64 `yaml:"affinity" json:"affinity"`  // co-locate with dependencies
	Cost     float64 `yaml:"cost" json:"cost"`          // minimize $/hr
	Locality float64 `yaml:"locality" json:"locality"`  // prefer same region
}

// DefaultWeights returns the standard production scoring weights.
func DefaultWeights() ScoringWeights {
	return ScoringWeights{BinPack: 35, Spread: 25, Affinity: 20, Cost: 10, Locality: 10}
}

// PlacementRequest describes what needs to be scheduled.
type PlacementRequest struct {
	StackID string
	Service *manifest.Service
	Region  string // preferred region (from caller's location)
}

// PlacementDecision is the output of the scheduler.
type PlacementDecision struct {
	NodeID   string `json:"nodeId"`
	NodeName string `json:"nodeName"`
	Port     int    `json:"port,omitempty"` // allocated host port
}

// NodeScore holds a candidate node and its computed score.
type NodeScore struct {
	Node     store.NodeInfo
	Scores   map[string]float64 // per-scorer breakdown
	Total    float64
	Filtered bool
	Reason   string // why it was filtered
}

// New creates a Scheduler.
func New(st *store.Store, logger *zap.SugaredLogger, weights ScoringWeights) *Scheduler {
	return &Scheduler{store: st, logger: logger, weights: weights}
}

// Schedule runs the 4-phase pipeline and returns a placement decision.
func (s *Scheduler) Schedule(ctx context.Context, req PlacementRequest) (*PlacementDecision, error) {
	// Fetch all nodes
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no nodes available")
	}

	svc := req.Service

	// Phase 1: Filter — hard constraints
	candidates := s.filter(nodes, svc)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no nodes pass filter for service %q (checked %d nodes)", svc.Name, len(nodes))
	}

	// Phase 2: Score — soft preferences
	scored := s.score(candidates, svc, req.Region)

	// Sort by total score descending
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Total > scored[j].Total
	})

	best := scored[0]

	// Phase 3: Bind — reserve resources
	if err := s.bind(ctx, best.Node, svc); err != nil {
		// If bind fails (resource contention), try next candidate
		for i := 1; i < len(scored); i++ {
			if err := s.bind(ctx, scored[i].Node, svc); err == nil {
				best = scored[i]
				break
			}
		}
		if best.Total == scored[0].Total { // no fallback succeeded
			return nil, fmt.Errorf("bind failed on all candidates: %w", err)
		}
	}

	// Phase 4: Pre-flight verify — confirm node is reachable
	if err := s.verify(ctx, best.Node); err != nil {
		s.logger.Warnw("pre-flight verify failed, trying next candidate",
			"node", best.Node.Name, "error", err)
		// Try remaining candidates
		for i := 1; i < len(scored); i++ {
			if err := s.verify(ctx, scored[i].Node); err == nil {
				best = scored[i]
				break
			}
		}
	}

	s.logger.Infow("placement decided",
		"service", svc.Name,
		"node", best.Node.Name,
		"score", fmt.Sprintf("%.1f", best.Total),
		"breakdown", best.Scores,
	)

	return &PlacementDecision{
		NodeID:   best.Node.ID,
		NodeName: best.Node.Name,
	}, nil
}

// ScheduleMultiReplica places N replicas iteratively, re-scoring after each placement.
func (s *Scheduler) ScheduleMultiReplica(ctx context.Context, req PlacementRequest, replicas int) ([]PlacementDecision, error) {
	var decisions []PlacementDecision

	for i := 0; i < replicas; i++ {
		decision, err := s.Schedule(ctx, req)
		if err != nil {
			if len(decisions) > 0 {
				s.logger.Warnw("partial placement",
					"service", req.Service.Name,
					"placed", len(decisions),
					"requested", replicas,
				)
				return decisions, nil // partial success
			}
			return nil, err
		}
		decisions = append(decisions, *decision)
	}

	return decisions, nil
}

// --- Phase 1: Filter ---

func (s *Scheduler) filter(nodes []store.NodeInfo, svc *manifest.Service) []store.NodeInfo {
	var candidates []store.NodeInfo

	for _, node := range nodes {
		// Check node is healthy
		if node.Status != "healthy" {
			continue
		}

		// Check resource fit
		cpuReq := parseCPU(svc.Resources.CPU)
		memReq := parseMemory(svc.Resources.Memory)
		if node.CPUFree < cpuReq || node.MemFree < memReq {
			continue
		}

		// Check GPU requirement
		if svc.Resources.GPU > 0 && node.GPUFree < svc.Resources.GPU {
			continue
		}

		// Check runtime provider availability
		if svc.Runtime != "" {
			hasProvider := false
			for _, p := range node.Providers {
				if p == svc.Runtime {
					hasProvider = true
					break
				}
			}
			if !hasProvider {
				continue
			}
		}

		// Check node selector (labels must match)
		if len(svc.NodeSelector) > 0 {
			match := true
			for k, v := range svc.NodeSelector {
				if node.Labels[k] != v {
					// Special case: os selector can match directly
					if k == "os" && node.OS != v {
						match = false
						break
					}
				}
			}
			if !match {
				continue
			}
		}

		candidates = append(candidates, node)
	}

	return candidates
}

// --- Phase 2: Score ---

func (s *Scheduler) score(nodes []store.NodeInfo, svc *manifest.Service, preferredRegion string) []NodeScore {
	scored := make([]NodeScore, len(nodes))

	for i, node := range nodes {
		scores := make(map[string]float64)

		// Bin-packing: prefer nodes that are already loaded (consolidate)
		cpuUsage := 1.0 - float64(node.CPUFree)/float64(max(node.CPUTotal, 1))
		memUsage := 1.0 - float64(node.MemFree)/float64(max(node.MemTotal, 1))
		scores["binpack"] = (cpuUsage*0.5 + memUsage*0.5) * 100

		// Spread: prefer nodes with fewer stacks (distribute)
		// Inverse of bin-packing — fewer existing stacks = higher spread score
		stackCount, _ := strconv.ParseFloat(node.Labels["stackCount"], 64)
		scores["spread"] = math.Max(0, 100-stackCount*20) // rough heuristic

		// Affinity: prefer nodes that have colocated services
		affinityScore := 30.0 // base
		for _, colocName := range svc.Affinity.Colocate {
			if node.Labels["has_"+colocName] == "true" {
				affinityScore = 90.0
				break
			}
		}
		scores["affinity"] = affinityScore

		// Cost: prefer cheaper nodes
		maxCost := 10.0 // normalize against a reasonable max
		scores["cost"] = math.Max(0, (1-node.CostPerHr/maxCost)*100)

		// Locality: prefer same region
		if preferredRegion != "" && node.Region == preferredRegion {
			scores["locality"] = 85
		} else if node.Region != "" {
			scores["locality"] = 40
		} else {
			scores["locality"] = 50
		}

		// Weighted total
		total := (scores["binpack"]*s.weights.BinPack +
			scores["spread"]*s.weights.Spread +
			scores["affinity"]*s.weights.Affinity +
			scores["cost"]*s.weights.Cost +
			scores["locality"]*s.weights.Locality) / 100.0

		scored[i] = NodeScore{
			Node:   node,
			Scores: scores,
			Total:  total,
		}
	}

	return scored
}

// --- Phase 3: Bind ---

func (s *Scheduler) bind(ctx context.Context, node store.NodeInfo, svc *manifest.Service) error {
	cpuReq := parseCPU(svc.Resources.CPU)
	memReq := parseMemory(svc.Resources.Memory)

	// Optimistic concurrency: try to deduct resources
	newCPUFree := node.CPUFree - cpuReq
	newMemFree := node.MemFree - memReq

	if newCPUFree < 0 || newMemFree < 0 {
		return fmt.Errorf("insufficient resources on node %s after concurrent claim", node.Name)
	}

	node.CPUFree = newCPUFree
	node.MemFree = newMemFree
	node.LastSeen = time.Now()

	return s.store.RegisterNode(ctx, node)
}

// --- Phase 4: Verify ---

func (s *Scheduler) verify(ctx context.Context, node store.NodeInfo) error {
	// In production this would ping the node agent
	// For now, verify node was seen recently
	if time.Since(node.LastSeen) > 60*time.Second {
		return fmt.Errorf("node %s last seen %v ago", node.Name, time.Since(node.LastSeen))
	}
	return nil
}

// --- Helpers ---

func parseCPU(s string) int64 {
	// Parse "500m" -> 500 millicores, "2" -> 2000 millicores
	if s == "" {
		return 100 // default 100m
	}
	var val int64
	if _, err := fmt.Sscanf(s, "%dm", &val); err == nil {
		return val
	}
	if _, err := fmt.Sscanf(s, "%d", &val); err == nil {
		return val * 1000
	}
	return 100
}

func parseMemory(s string) int64 {
	// Parse "256Mi" -> bytes, "1Gi" -> bytes
	if s == "" {
		return 128 * 1024 * 1024 // default 128Mi
	}
	var val int64
	if _, err := fmt.Sscanf(s, "%dGi", &val); err == nil {
		return val * 1024 * 1024 * 1024
	}
	if _, err := fmt.Sscanf(s, "%dMi", &val); err == nil {
		return val * 1024 * 1024
	}
	return 128 * 1024 * 1024
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
