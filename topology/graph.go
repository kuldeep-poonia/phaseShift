package topology

import (
	"math"
	"sync"
	"time"

	"github.com/qphysics/phaseshift/telemetry"
)

const (
	edgeDecayFactor = 0.82
	nodeStaleAge    = 5 * time.Minute
	maxNodes        = 300
)

type Graph struct {
	mu    sync.RWMutex
	nodes map[string]*Node
	edges map[string]map[string]*Edge
}

func New() *Graph {
	return &Graph{
		nodes: make(map[string]*Node),
		edges: make(map[string]map[string]*Edge),
	}
}

func (g *Graph) Update(windows map[string]*telemetry.ServiceWindow) {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()

	// Upsert nodes (respect cardinality cap).
	for id := range windows {
		n, ok := g.nodes[id]
		if !ok {
			if len(g.nodes) >= maxNodes {
				continue
			}
			n = &Node{ServiceID: id}
			g.nodes[id] = n
		}
		n.LastSeen = now
	}

	seen := make(map[string]map[string]bool)
	for srcID, w := range windows {
		if seen[srcID] == nil {
			seen[srcID] = make(map[string]bool)
		}
		for _, ue := range w.UpstreamEdges {
			tgtID := ue.TargetServiceID
			seen[srcID][tgtID] = true
			if _, ok := g.nodes[tgtID]; !ok {
				if len(g.nodes) < maxNodes {
					g.nodes[tgtID] = &Node{ServiceID: tgtID, LastSeen: now}
				}
			}
			if g.edges[srcID] == nil {
				g.edges[srcID] = make(map[string]*Edge)
			}
			e, ok := g.edges[srcID][tgtID]
			if !ok {
				e = &Edge{Source: srcID, Target: tgtID}
				g.edges[srcID][tgtID] = e
			}
			e.CallRate = ue.MeanCallRate
			e.ErrorRate = ue.MeanErrorRate
			e.LatencyMs = ue.MeanLatencyMs
			if w.MeanRequestRate > 0 {
				e.Weight = math.Min(ue.MeanCallRate/w.MeanRequestRate, 1.0)
			}
			e.LastUpdated = now
		}
	}

	// Decay unseen edges.
	for src, targets := range g.edges {
		for tgt, e := range targets {
			if seen[src] == nil || !seen[src][tgt] {
				e.Weight *= edgeDecayFactor
				if e.Weight < 0.005 {
					delete(targets, tgt)
				}
			}
		}
		if len(g.edges[src]) == 0 {
			delete(g.edges, src)
		}
	}

	// Prune stale nodes.
	for id, n := range g.nodes {
		if now.Sub(n.LastSeen) > nodeStaleAge {
			delete(g.nodes, id)
			delete(g.edges, id)
		}
	}

	g.propagateLoad(windows)
}

func (g *Graph) propagateLoad(windows map[string]*telemetry.ServiceWindow) {
	maxRate := 1.0
	for _, w := range windows {
		if w.MeanRequestRate > maxRate {
			maxRate = w.MeanRequestRate
		}
	}
	for id, n := range g.nodes {
		if w, ok := windows[id]; ok {
			n.NormalisedLoad = w.MeanRequestRate / maxRate
		}
	}
	for iter := 0; iter < 8; iter++ {
		delta := 0.0
		for src, targets := range g.edges {
			sn := g.nodes[src]
			if sn == nil {
				continue
			}
			for tgt, e := range targets {
				tn := g.nodes[tgt]
				if tn == nil {
					continue
				}
				contrib := sn.NormalisedLoad * e.Weight * 0.25
				prev := tn.NormalisedLoad
				tn.NormalisedLoad = math.Min(tn.NormalisedLoad+contrib, 1.0)
				delta += math.Abs(tn.NormalisedLoad - prev)
			}
		}
		if delta < 1e-6 {
			break
		}
	}
}

// edgeWeightFloor is the minimum edge weight retained in topology snapshots.
// Edges below this threshold represent negligible call rates and are pruned
// to bound graph cardinality and keep topology modelling O(N+E_significant).
const edgeWeightFloor = 0.02

func (g *Graph) Snapshot() GraphSnapshot {
	g.mu.RLock()
	defer g.mu.RUnlock()

	nodes := make([]Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		nodes = append(nodes, *n)
	}
	// Topology compaction: only include edges above the weight floor.
	// This prevents long-tail zero-weight edges from polluting the propagation graph.
	edges := make([]Edge, 0, len(g.edges))
	for _, targets := range g.edges {
		for _, e := range targets {
			if e.Weight >= edgeWeightFloor {
				edges = append(edges, *e)
			}
		}
	}
	return GraphSnapshot{
		CapturedAt:   time.Now(),
		Nodes:        nodes,
		Edges:        edges,
		CriticalPath: g.criticalPath(),
	}
}

func (g *Graph) criticalPath() CriticalPath {
	if len(g.nodes) == 0 {
		return CriticalPath{}
	}
	dist := make(map[string]float64, len(g.nodes))
	prev := make(map[string]string, len(g.nodes))
	for id := range g.nodes {
		dist[id] = 0
	}
	for i := 0; i < len(g.nodes); i++ {
		for src, targets := range g.edges {
			for tgt, e := range targets {
				if dist[src]+e.Weight > dist[tgt] {
					dist[tgt] = dist[src] + e.Weight
					prev[tgt] = src
				}
			}
		}
	}
	best, bestD := "", 0.0
	for id, d := range dist {
		if d > bestD {
			bestD = d
			best = id
		}
	}
	if best == "" {
		for _, n := range g.nodes {
			if n.NormalisedLoad > bestD {
				bestD = n.NormalisedLoad
				best = n.ServiceID
			}
		}
		if best == "" {
			return CriticalPath{}
		}
		return CriticalPath{Nodes: []string{best}, TotalWeight: bestD}
	}
	var path []string
	cur, visited := best, make(map[string]bool)
	for cur != "" && !visited[cur] {
		path = append([]string{cur}, path...)
		visited[cur] = true
		cur = prev[cur]
	}
	cascadeRisk := 0.0
	for _, id := range path {
		if n, ok := g.nodes[id]; ok {
			r := sigmoid((n.NormalisedLoad - 0.78) / 0.05)
			cascadeRisk = 1 - (1-cascadeRisk)*(1-r)
		}
	}
	return CriticalPath{Nodes: path, TotalWeight: bestD, CascadeRisk: cascadeRisk}
}

func (g *Graph) NodeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.nodes)
}

func sigmoid(x float64) float64 { return 1.0 / (1.0 + math.Exp(-x)) }
