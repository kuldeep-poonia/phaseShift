package modelling

import (
	"math"

	"github.com/qphysics/phaseshift/telemetry"
	"github.com/qphysics/phaseshift/topology"
)

type couplerState struct {
	queueBaseline    float64
	latencyPersisted float64
	demandMemory     float64
	varianceAcc      float64
	propBuffer       [3]float64
	propIdx          int
}

// TelemetryCoupler enforces persistent physical state continuity across disjoint ticks.
type TelemetryCoupler struct {
	states map[string]*couplerState
}

func NewTelemetryCoupler() *TelemetryCoupler {
	return &TelemetryCoupler{
		states: make(map[string]*couplerState),
	}
}

// ApplyCoupling injects physical momentum directly into the telemetry snapshot.
func (c *TelemetryCoupler) ApplyCoupling(windows map[string]*telemetry.ServiceWindow, topo topology.GraphSnapshot) {
	injectedLoad := make(map[string]float64, len(windows))

	// D. Downstream Demand Propagation
	// Upstream overload increases effective arrival signals in dependent services.
	for _, edge := range topo.Edges {
		if st, ok := c.states[edge.Source]; ok {
			// If upstream is queuing, it propagates pressure directly onto the wire
			if st.queueBaseline > 0 {
				injectedLoad[edge.Target] += st.queueBaseline * edge.Weight * 0.15
			}
		}
	}

	for id, w := range windows {
		st := c.states[id]
		if st == nil {
			st = &couplerState{
				demandMemory:     w.MeanRequestRate,
				latencyPersisted: math.Max(w.MeanLatencyMs, 10.0),
			}
			c.states[id] = st
		}

		// F. Capacity-Normalised Window Metrics
		// Evaluate behaviour relative to logical core footprint
		capScale := math.Max(1.0, w.MeanActiveConns)

		// C. Arrival Demand Memory
		// Scenario-scaled arrival rates smoothed into window buffers for tracking burst inertia
		rawDemand := w.MeanRequestRate
		st.demandMemory = 0.7*rawDemand + 0.3*st.demandMemory

		// Deterministic delay stages for propagated load
		st.propBuffer[st.propIdx] = injectedLoad[id]
		st.propIdx = (st.propIdx + 1) % 3
		delayedInjection := st.propBuffer[st.propIdx]

		smoothedDemand := st.demandMemory + delayedInjection
		w.MeanRequestRate = smoothedDemand

		// A. Persistent Queue State Injection
		// computed queue_next becomes the next telemetry window baseline
		svcRate := capScale * 1000.0 / math.Max(st.latencyPersisted, 1.0)
		netDemand := smoothedDemand - svcRate

		// Accumulate backlog unconditionally tracking net differentials (dt ~ 2.0s)
		st.queueBaseline += netDemand * 2.0
		if st.queueBaseline < 0 {
			st.queueBaseline = 0
		}

		normalizedQueue := st.queueBaseline / capScale
		w.MeanQueueDepth = st.queueBaseline
		w.LastQueueDepth = st.queueBaseline

		// B. Latency Feedback Persistence
		// Latency growth caused by backlog must be written and decay gradually
		latencyPenalty := normalizedQueue * 60.0 // ms of injected wait-time
		rawExpectedLatency := w.MeanLatencyMs + latencyPenalty

		if rawExpectedLatency > st.latencyPersisted {
			st.latencyPersisted = 0.8*rawExpectedLatency + 0.2*st.latencyPersisted // rapid assault
		} else {
			st.latencyPersisted = 0.1*rawExpectedLatency + 0.9*st.latencyPersisted // slow draining decay
		}

		w.MeanLatencyMs = st.latencyPersisted
		w.LastLatencyMs = st.latencyPersisted

		// E. Variance Accumulation
		// Telemetry accumulates variance over disturbance periods instead of rapid resetting
		// Simulates structural vibration inside the queue
		if smoothedDemand > svcRate {
			st.varianceAcc += math.Abs(netDemand) * 0.25
		} else {
			st.varianceAcc *= 0.85 // relax structurally once equilibrium bounds
		}
		w.StdRequestRate += st.varianceAcc
		// G. Coupling state is fully observable via /api/state and /api/services endpoints.
		// Hot-path log.Printf removed: was firing per-service per-tick (~100 writes/sec at
		// 100 services), each allocating fmt.Sprintf buffer under the log mutex.
	}

	// Prune inactive metrics bounds
	for id := range c.states {
		if _, ok := windows[id]; !ok {
			delete(c.states, id)
		}
	}
}