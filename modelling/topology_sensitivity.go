package modelling

import (
	"math"

	"github.com/qphysics/phaseshift/topology"
)

// TopologySensitivity quantifies how sensitive the overall system is to
// the failure or degradation of each service.
type TopologySensitivity struct {
	// ByService: per-service sensitivity scores.
	ByService map[string]ServiceSensitivity

	// SystemFragility: weighted mean of all per-service perturbation scores.
	// High fragility means the system is brittle — small degradations propagate widely.
	SystemFragility float64

	// MaxAmplificationPath: the service chain with the highest cumulative
	// amplification potential (product of edge weights along the chain).
	MaxAmplificationPath []string

	// MaxAmplificationScore: product of edge weights along MaxAmplificationPath.
	// > 1 is theoretically impossible (weights capped at 1), but near-1 chains
	// are extremely dangerous because they transfer nearly all load end-to-end.
	MaxAmplificationScore float64
}

// ServiceSensitivity quantifies the impact of degrading one service on the rest.
type ServiceSensitivity struct {
	ServiceID string

	// PerturbationScore: fraction of the system's total normalised load that
	// passes through this service. Computed as:
	//   inbound_weight_sum + outbound_weight_sum (normalised to [0,1]).
	// Services with high PerturbationScore are single points of amplification.
	PerturbationScore float64

	// DownstreamReach: number of distinct services reachable via outbound edges
	// within 4 hops. High reach = wide blast radius on failure.
	DownstreamReach int

	// UpstreamExposure: number of distinct callers within 4 hops upstream.
	// High exposure = many services are affected if this one slows down.
	UpstreamExposure int

	// IsKeystone: true when PerturbationScore > 0.6 or DownstreamReach > len(services)/2.
	// A keystone service is critical to system stability.
	IsKeystone bool
}

// ComputeTopologySensitivity computes perturbation sensitivity for every node
// in the topology snapshot. This analysis is topology-structural — it does not
// require live telemetry (telemetry-driven risk is in stability.go).
func ComputeTopologySensitivity(snap topology.GraphSnapshot) TopologySensitivity {
	result := TopologySensitivity{
		ByService: make(map[string]ServiceSensitivity, len(snap.Nodes)),
	}
	if len(snap.Nodes) == 0 {
		return result
	}

	n := len(snap.Nodes)

	// Build adjacency: outbound and inbound edge weight sums per node.
	outWeight := make(map[string]float64, n)
	inWeight := make(map[string]float64, n)
	outEdges := make(map[string][]string, n)
	inEdges := make(map[string][]string, n)

	for _, e := range snap.Edges {
		if e.Weight <= 0 {
			continue
		}
		outWeight[e.Source] += e.Weight
		inWeight[e.Target] += e.Weight
		outEdges[e.Source] = append(outEdges[e.Source], e.Target)
		inEdges[e.Target] = append(inEdges[e.Target], e.Source)
	}

	// Normalise weight sums by maximum observed (prevents scale sensitivity).
	maxWeight := 1.0
	for id := range outWeight {
		if v := outWeight[id] + inWeight[id]; v > maxWeight {
			maxWeight = v
		}
	}

	var sumPerturbation float64
	for _, node := range snap.Nodes {
		id := node.ServiceID
		perturbScore := (outWeight[id] + inWeight[id]) / maxWeight

		downReach := reachCount(id, outEdges, 4)
		upExposure := reachCount(id, inEdges, 4)

		isKeystone := perturbScore > 0.6 || downReach > n/2

		result.ByService[id] = ServiceSensitivity{
			ServiceID:         id,
			PerturbationScore: math.Min(perturbScore, 1.0),
			DownstreamReach:   downReach,
			UpstreamExposure:  upExposure,
			IsKeystone:        isKeystone,
		}
		sumPerturbation += perturbScore
	}

	if n > 0 {
		result.SystemFragility = math.Min(sumPerturbation/float64(n), 1.0)
	}

	// Find max amplification path: longest path by product of weights.
	// Use log-space to avoid underflow: sum of log(weights).
	bestScore, bestPath := findMaxAmplificationPath(snap)
	result.MaxAmplificationPath = bestPath
	result.MaxAmplificationScore = bestScore

	return result
}

// reachCount returns the number of distinct nodes reachable from start
// via the given adjacency map within maxDepth hops, excluding start itself.
func reachCount(start string, adj map[string][]string, maxDepth int) int {
	visited := make(map[string]bool)
	visited[start] = true
	queue := []string{start}
	depth := 0
	for len(queue) > 0 && depth < maxDepth {
		next := []string{}
		for _, cur := range queue {
			for _, nb := range adj[cur] {
				if !visited[nb] {
					visited[nb] = true
					next = append(next, nb)
				}
			}
		}
		queue = next
		depth++
	}
	return len(visited) - 1 // exclude start
}

// findMaxAmplificationPath finds the path with the highest sum of log(weights),
// equivalent to the maximum weight-product path.
// Returns (amplification_score, path_nodes).
func findMaxAmplificationPath(snap topology.GraphSnapshot) (float64, []string) {
	if len(snap.Edges) == 0 {
		return 0, nil
	}

	// Build log-weight adjacency.
	type edge struct {
		target string
		logW   float64
	}
	adj := make(map[string][]edge, len(snap.Nodes))
	for _, e := range snap.Edges {
		if e.Weight > 0 {
			adj[e.Source] = append(adj[e.Source], edge{e.Target, math.Log(e.Weight)})
		}
	}

	// Bellman-Ford longest path (negate weights for max).
	dist := make(map[string]float64, len(snap.Nodes))
	prev := make(map[string]string, len(snap.Nodes))

	for iter := 0; iter < len(snap.Nodes); iter++ {
		for src, edges := range adj {
			for _, e := range edges {
				if dist[src]+e.logW > dist[e.target] {
					dist[e.target] = dist[src] + e.logW
					prev[e.target] = src
				}
			}
		}
	}

	// Find endpoint of best path.
	bestEnd := ""
	bestDist := -math.MaxFloat64
	for id, d := range dist {
		if d > bestDist {
			bestDist = d
			bestEnd = id
		}
	}
	if bestEnd == "" {
		return 0, nil
	}

	// Reconstruct path.
	var path []string
	cur := bestEnd
	visited := make(map[string]bool)
	for cur != "" && !visited[cur] {
		path = append([]string{cur}, path...)
		visited[cur] = true
		cur = prev[cur]
	}

	return math.Exp(bestDist), path
}
