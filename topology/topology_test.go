package topology

import (
	"fmt"
	"testing"
	"time"

	"github.com/qphysics/phaseshift/telemetry"
)

func makeWindow(
	id string,
	rate float64,
) *telemetry.ServiceWindow {

	return &telemetry.ServiceWindow{
		ServiceID:       id,
		MeanRequestRate: rate,
	}
}

func TestGraphEmpty(t *testing.T) {
	g := New()

	if g.NodeCount() != 0 {
		t.Fatalf(
			"expected empty graph got=%d",
			g.NodeCount(),
		)
	}

	snap := g.Snapshot()

	if len(snap.Nodes) != 0 {
		t.Fatal("expected no nodes")
	}

	if len(snap.Edges) != 0 {
		t.Fatal("expected no edges")
	}
}

func TestGraphCreateSingleNode(t *testing.T) {
	g := New()

	g.Update(
		map[string]*telemetry.ServiceWindow{
			"checkout": makeWindow(
				"checkout",
				100,
			),
		},
	)

	if g.NodeCount() != 1 {
		t.Fatalf(
			"expected 1 node got=%d",
			g.NodeCount(),
		)
	}

	snap := g.Snapshot()

	if len(snap.Nodes) != 1 {
		t.Fatal("snapshot node count incorrect")
	}

	if snap.Nodes[0].ServiceID != "checkout" {
		t.Fatal("wrong node id")
	}
}

func TestGraphCreateMultipleNodes(t *testing.T) {
	g := New()

	windows := map[string]*telemetry.ServiceWindow{
		"api":      makeWindow("api", 100),
		"auth":     makeWindow("auth", 50),
		"checkout": makeWindow("checkout", 25),
	}

	g.Update(windows)

	if g.NodeCount() != 3 {
		t.Fatalf(
			"expected 3 nodes got=%d",
			g.NodeCount(),
		)
	}
}

func TestGraphCreateEdge(t *testing.T) {

	g := New()

	windows := map[string]*telemetry.ServiceWindow{
		"api": {
			ServiceID:       "api",
			MeanRequestRate: 100,
			UpstreamEdges: map[string]telemetry.EdgeWindow{
				"db": {
					TargetServiceID: "db",
					MeanCallRate:    50,
					MeanLatencyMs:   10,
				},
			},
		},
	}

	g.Update(windows)

	snap := g.Snapshot()

	if len(snap.Edges) != 1 {
		t.Fatalf(
			"expected 1 edge got=%d",
			len(snap.Edges),
		)
	}

	e := snap.Edges[0]

	if e.Source != "api" {
		t.Fatalf(
			"expected source api got=%s",
			e.Source,
		)
	}

	if e.Target != "db" {
		t.Fatalf(
			"expected target db got=%s",
			e.Target,
		)
	}
}

func TestEdgeWeightCalculation(t *testing.T) {

	g := New()

	windows := map[string]*telemetry.ServiceWindow{
		"api": {
			ServiceID:       "api",
			MeanRequestRate: 100,
			UpstreamEdges: map[string]telemetry.EdgeWindow{
				"db": {
					TargetServiceID: "db",
					MeanCallRate:    50,
				},
			},
		},
	}

	g.Update(windows)

	snap := g.Snapshot()

	if len(snap.Edges) != 1 {
		t.Fatal("edge missing")
	}

	if snap.Edges[0].Weight != 0.5 {
		t.Fatalf(
			"expected weight=0.5 got=%v",
			snap.Edges[0].Weight,
		)
	}
}

func TestEdgeWeightCapAtOne(t *testing.T) {

	g := New()

	windows := map[string]*telemetry.ServiceWindow{
		"api": {
			ServiceID:       "api",
			MeanRequestRate: 100,
			UpstreamEdges: map[string]telemetry.EdgeWindow{
				"db": {
					TargetServiceID: "db",
					MeanCallRate:    1000,
				},
			},
		},
	}

	g.Update(windows)

	snap := g.Snapshot()

	if snap.Edges[0].Weight > 1.0 {
		t.Fatalf(
			"weight exceeded cap got=%v",
			snap.Edges[0].Weight,
		)
	}
}

func TestSnapshotCompaction(t *testing.T) {

	g := New()

	windows := map[string]*telemetry.ServiceWindow{
		"api": {
			ServiceID:       "api",
			MeanRequestRate: 100,
			UpstreamEdges: map[string]telemetry.EdgeWindow{
				"db": {
					TargetServiceID: "db",
					MeanCallRate:    1,
				},
			},
		},
	}

	g.Update(windows)

	snap := g.Snapshot()

	if len(snap.Edges) != 0 {
		t.Fatal(
			"low weight edge should be compacted out",
		)
	}
}

func TestMaxNodeLimit(t *testing.T) {

	g := New()

	windows := make(
		map[string]*telemetry.ServiceWindow,
	)

	for i := 0; i < 1000; i++ {

		id := fmt.Sprintf(
			"svc-%d",
			i,
		)

		windows[id] = makeWindow(
			id,
			1,
		)
	}

	g.Update(windows)

	if g.NodeCount() > maxNodes {

		t.Fatalf(
			"node limit violated expected<=%d got=%d",
			maxNodes,
			g.NodeCount(),
		)
	}
}

func TestSnapshotIsolation(t *testing.T) {

	g := New()

	g.Update(
		map[string]*telemetry.ServiceWindow{
			"svc": makeWindow(
				"svc",
				100,
			),
		},
	)

	snap := g.Snapshot()

	snap.Nodes[0].ServiceID = "hacked"

	snap2 := g.Snapshot()

	if snap2.Nodes[0].ServiceID == "hacked" {

		t.Fatal(
			"snapshot mutated internal graph",
		)
	}
}

func TestNodeLastSeenUpdate(t *testing.T) {

	g := New()

	g.Update(
		map[string]*telemetry.ServiceWindow{
			"svc": makeWindow(
				"svc",
				100,
			),
		},
	)

	snap1 := g.Snapshot()

	time.Sleep(
		10 * time.Millisecond,
	)

	g.Update(
		map[string]*telemetry.ServiceWindow{
			"svc": makeWindow(
				"svc",
				100,
			),
		},
	)

	snap2 := g.Snapshot()

	if !snap2.Nodes[0].LastSeen.After(
		snap1.Nodes[0].LastSeen,
	) {
		t.Fatal(
			"LastSeen did not advance",
		)
	}
}


func TestEdgeDecay(t *testing.T) {
	g := New()

	g.Update(map[string]*telemetry.ServiceWindow{
		"api": {
			ServiceID:       "api",
			MeanRequestRate: 100,
			UpstreamEdges: map[string]telemetry.EdgeWindow{
				"db": {
					TargetServiceID: "db",
					MeanCallRate:    50,
				},
			},
		},
	})

	s1 := g.Snapshot()

	if len(s1.Edges) != 1 {
		t.Fatal("expected edge")
	}

	w1 := s1.Edges[0].Weight

	g.Update(map[string]*telemetry.ServiceWindow{
		"api": {
			ServiceID:       "api",
			MeanRequestRate: 100,
		},
	})

	s2 := g.Snapshot()

	if len(s2.Edges) != 1 {
		t.Fatal("edge disappeared too early")
	}

	if s2.Edges[0].Weight >= w1 {
		t.Fatal("edge did not decay")
	}
}

func TestEdgeRemovalAfterDecay(t *testing.T) {
	g := New()

	g.Update(map[string]*telemetry.ServiceWindow{
		"api": {
			ServiceID:       "api",
			MeanRequestRate: 100,
			UpstreamEdges: map[string]telemetry.EdgeWindow{
				"db": {
					TargetServiceID: "db",
					MeanCallRate:    100,
				},
			},
		},
	})

	for i := 0; i < 40; i++ {
		g.Update(map[string]*telemetry.ServiceWindow{
			"api": {
				ServiceID:       "api",
				MeanRequestRate: 100,
			},
		})
	}

	snap := g.Snapshot()

	if len(snap.Edges) != 0 {
		t.Fatalf(
			"expected decayed edge removal got=%d",
			len(snap.Edges),
		)
	}
}

func TestLoadPropagationSimple(t *testing.T) {
	g := New()

	g.Update(map[string]*telemetry.ServiceWindow{
		"a": {
			ServiceID:       "a",
			MeanRequestRate: 100,
			UpstreamEdges: map[string]telemetry.EdgeWindow{
				"b": {
					TargetServiceID: "b",
					MeanCallRate:    100,
				},
			},
		},
		"b": {
			ServiceID:       "b",
			MeanRequestRate: 10,
		},
	})

	snap := g.Snapshot()

	var aLoad float64
	var bLoad float64

	for _, n := range snap.Nodes {
		switch n.ServiceID {
		case "a":
			aLoad = n.NormalisedLoad
		case "b":
			bLoad = n.NormalisedLoad
		}
	}

	if bLoad <= 0.1 {
		t.Fatalf(
			"expected propagated load got=%v",
			bLoad,
		)
	}

	if aLoad <= 0 {
		t.Fatal("source load missing")
	}
}

func TestLoadPropagationChain(t *testing.T) {
	g := New()

	g.Update(map[string]*telemetry.ServiceWindow{
		"a": {
			ServiceID:       "a",
			MeanRequestRate: 100,
			UpstreamEdges: map[string]telemetry.EdgeWindow{
				"b": {
					TargetServiceID: "b",
					MeanCallRate:    100,
				},
			},
		},
		"b": {
			ServiceID:       "b",
			MeanRequestRate: 10,
			UpstreamEdges: map[string]telemetry.EdgeWindow{
				"c": {
					TargetServiceID: "c",
					MeanCallRate:    100,
				},
			},
		},
		"c": {
			ServiceID:       "c",
			MeanRequestRate: 1,
		},
	})

	snap := g.Snapshot()

	var cLoad float64

	for _, n := range snap.Nodes {
		if n.ServiceID == "c" {
			cLoad = n.NormalisedLoad
		}
	}

	if cLoad <= 0.01 {
		t.Fatalf(
			"expected chain propagation got=%v",
			cLoad,
		)
	}
}

func TestLoadPropagationSaturation(t *testing.T) {
	g := New()

	g.Update(map[string]*telemetry.ServiceWindow{
		"a": {
			ServiceID:       "a",
			MeanRequestRate: 10000,
			UpstreamEdges: map[string]telemetry.EdgeWindow{
				"b": {
					TargetServiceID: "b",
					MeanCallRate:    10000,
				},
			},
		},
		"b": {
			ServiceID:       "b",
			MeanRequestRate: 1,
		},
	})

	snap := g.Snapshot()

	for _, n := range snap.Nodes {

		if n.NormalisedLoad > 1.0 {
			t.Fatalf(
				"load overflow got=%v",
				n.NormalisedLoad,
			)
		}
	}
}

func TestCriticalPathSingleNode(t *testing.T) {
	g := New()

	g.Update(map[string]*telemetry.ServiceWindow{
		"svc": {
			ServiceID:       "svc",
			MeanRequestRate: 100,
		},
	})

	cp := g.Snapshot().CriticalPath

	if len(cp.Nodes) != 1 {
		t.Fatalf(
			"expected single node path got=%d",
			len(cp.Nodes),
		)
	}
}

func TestCriticalPathLinearChain(t *testing.T) {
	g := New()

	g.Update(map[string]*telemetry.ServiceWindow{
		"a": {
			ServiceID:       "a",
			MeanRequestRate: 100,
			UpstreamEdges: map[string]telemetry.EdgeWindow{
				"b": {
					TargetServiceID: "b",
					MeanCallRate:    80,
				},
			},
		},
		"b": {
			ServiceID:       "b",
			MeanRequestRate: 80,
			UpstreamEdges: map[string]telemetry.EdgeWindow{
				"c": {
					TargetServiceID: "c",
					MeanCallRate:    60,
				},
			},
		},
		"c": {
			ServiceID:       "c",
			MeanRequestRate: 60,
		},
	})

	cp := g.Snapshot().CriticalPath

	if len(cp.Nodes) < 2 {
		t.Fatalf(
			"critical path too short %#v",
			cp.Nodes,
		)
	}
}

func TestCriticalPathCycle(t *testing.T) {
	g := New()

	g.Update(map[string]*telemetry.ServiceWindow{
		"a": {
			ServiceID:       "a",
			MeanRequestRate: 100,
			UpstreamEdges: map[string]telemetry.EdgeWindow{
				"b": {
					TargetServiceID: "b",
					MeanCallRate:    100,
				},
			},
		},
		"b": {
			ServiceID:       "b",
			MeanRequestRate: 100,
			UpstreamEdges: map[string]telemetry.EdgeWindow{
				"c": {
					TargetServiceID: "c",
					MeanCallRate:    100,
				},
			},
		},
		"c": {
			ServiceID:       "c",
			MeanRequestRate: 100,
			UpstreamEdges: map[string]telemetry.EdgeWindow{
				"a": {
					TargetServiceID: "a",
					MeanCallRate:    100,
				},
			},
		},
	})

	cp := g.Snapshot().CriticalPath

	if len(cp.Nodes) == 0 {
		t.Fatal("cycle broke critical path")
	}
}