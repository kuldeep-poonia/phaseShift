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
// findMaxAmplificationPath finds the path through the service graph with the
// highest cumulative edge-weight product (maximum traffic amplification potential).
//
// Previous approach (Bellman-Ford on log-weights) had a fundamental flaw:
// since all edge weights ≤ 1.0, log(weight) ≤ 0, and longer paths always have
// a smaller (more negative) sum of log-weights than shorter paths. Bellman-Ford
// maximising this sum always preferred single-edge paths over multi-hop chains.
//
// Correct approach: DFS from each source node, tracking accumulated weight
// product. The path with the highest product is returned. For graphs up to
// 300 nodes (our cap) with bounded depth, this is efficient enough.
func findMaxAmplificationPath(snap topology.GraphSnapshot) (float64, []string) {
	if len(snap.Edges) == 0 {
		return 0, nil
	}

	// Build direct-weight adjacency (no log transform).
	type edge struct {
		target string
		weight float64
	}
	adj := make(map[string][]edge, len(snap.Nodes))
	hasIncoming := make(map[string]bool, len(snap.Nodes))
	for _, e := range snap.Edges {
		if e.Weight > 0 {
			adj[e.Source] = append(adj[e.Source], edge{e.Target, e.Weight})
			hasIncoming[e.Target] = true
		}
	}

	// Source nodes: no incoming edges → valid starting points.
	var sources []string
	for _, n := range snap.Nodes {
		if !hasIncoming[n.ServiceID] {
			sources = append(sources, n.ServiceID)
		}
	}
	// If every node has incoming edges (cycle) treat all as potential sources.
	if len(sources) == 0 {
		for _, n := range snap.Nodes {
			sources = append(sources, n.ServiceID)
		}
	}

	bestScore := 0.0
	var bestPath []string

	// DFS with visited set to avoid cycles.
	var dfs func(node string, product float64, path []string, visited map[string]bool)
	dfs = func(node string, product float64, path []string, visited map[string]bool) {
		path = append(path, node)
		// Score = product × path length. Longer paths that maintain high weight
		// are more dangerous than single high-weight edges.
		score := product * float64(len(path))
		if score > bestScore {
			bestScore = score
			bestPath = make([]string, len(path))
			copy(bestPath, path)
		}
		for _, e := range adj[node] {
			if !visited[e.target] {
				visited[e.target] = true
				dfs(e.target, product*e.weight, path, visited)
				visited[e.target] = false
			}
		}
	}

	for _, src := range sources {
		visited := map[string]bool{src: true}
		dfs(src, 1.0, nil, visited)
	}

	if len(bestPath) == 0 {
		return 0, nil
	}
	// Return the weight product (without the length multiplier) as the score.
	// Recompute pure product for the best path.
	pureProduct := 1.0
	for i := 0; i < len(bestPath)-1; i++ {
		for _, e := range adj[bestPath[i]] {
			if e.target == bestPath[i+1] {
				pureProduct *= e.weight
				break
			}
		}
	}
	return pureProduct, bestPath
}