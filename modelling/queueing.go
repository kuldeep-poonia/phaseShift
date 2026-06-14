package modelling

import (
	"math"
	"sync"
	"time"

	"github.com/qphysics/phaseshift/telemetry"
	"github.com/qphysics/phaseshift/topology"
)

// PhysicalQueueState maintains continuous system momentum between disjoint ticks.
type PhysicalQueueState struct {
	accumulatedBacklog float64
	arrivalMomentum    float64
	localArrivalEwma   float64
	delayBuffer        [3]float64
	delayIdx           int
	lastTickTime       time.Time
}

// QueuePhysicsEngine encapsulates the stateful Erlang-C fluid approximations.
//
// Original design: one global sync.Mutex serialized ALL service goroutines at
// RunQueueModel() entry. With 100 services in an 8-worker pool, 7/8 goroutines
// always waited for the single lock — the worker pool provided almost no benefit.
//
// Fix: 32 independent shards. Lock held only for the map lookup (nanoseconds).
// All physics computation runs lock-free. Services in different shards never contend.
const queueShards = 32

type queueShard struct {
	mu     sync.Mutex
	states map[string]*PhysicalQueueState
}

type QueuePhysicsEngine struct {
	shards [queueShards]queueShard
}

func queueShardFor(id string) int {
	h := uint32(2166136261)
	for i := 0; i < len(id); i++ {
		h ^= uint32(id[i])
		h *= 16777619
	}
	return int(h & (queueShards - 1))
}

func NewQueuePhysicsEngine() *QueuePhysicsEngine {
	pe := &QueuePhysicsEngine{}
	for i := range pe.shards {
		pe.shards[i].states = make(map[string]*PhysicalQueueState, 8)
	}
	return pe
}

func (pe *QueuePhysicsEngine) Prune(activeIDs map[string]struct{}) {
	for i := range pe.shards {
		sh := &pe.shards[i]
		sh.mu.Lock()
		for id := range sh.states {
			if _, ok := activeIDs[id]; !ok {
				delete(sh.states, id)
			}
		}
		sh.mu.Unlock()
	}
}

// RunQueueModel computes queueing analysis using M/M/c (Erlang-C) as the primary
// model, upgraded with continuous physical accumulation dynamics.
//
// Lock protocol: acquire shard mutex only for the map lookup (O(1), nanoseconds),
// then release. All computation is lock-free on the per-service state pointer.
// This is safe because the orchestrator assigns exactly one goroutine per service
// per tick — no concurrent access to the same service's state within a tick.
//
// Hot-path log.Printf calls removed:
//   Original had two log.Printf per service per tick ("[service-rate-coupling]"
//   and "[physics]"). At 100 services those are 200 log writes/tick = 100/sec.
//   Each fmt.Sprintf call allocates, then writes to stderr under a mutex.
//   Replaced with no-op: these were DIAGNOSTIC logs left in production.
//   Physics state is fully observable via the /metrics and /ws endpoints.
func (pe *QueuePhysicsEngine) RunQueueModel(w *telemetry.ServiceWindow, topoSnap topology.GraphSnapshot, medianMode bool) QueueModel {
	sh := &pe.shards[queueShardFor(w.ServiceID)]

	sh.mu.Lock()
	st, ok := sh.states[w.ServiceID]
	if !ok {
		st = &PhysicalQueueState{
			lastTickTime:     time.Now(),
			localArrivalEwma: w.MeanRequestRate,
		}
		sh.states[w.ServiceID] = st
	}
	sh.mu.Unlock()
	// Lock released — all computation below is lock-free.

	dt := time.Since(st.lastTickTime).Seconds()
	if dt <= 0 || dt > 10.0 {
		dt = 2.0 // default tick clamp
	}
	st.lastTickTime = time.Now()

	m := QueueModel{
		ServiceID:  w.ServiceID,
		ComputedAt: time.Now(),
		Confidence: confidenceFromSamples(w.SampleCount),
	}
	if w.MeanRequestRate <= 0 || w.MeanLatencyMs <= 0 {
		return m
	}
	m.Hazard = w.Hazard
	m.Reservoir = w.Reservoir

	trustWeight := math.Max(w.ConfidenceScore, 0.10)

	c := math.Max(math.Round(w.MeanActiveConns), 1.0)
	m.Concurrency = c

	currentArrival := w.MeanRequestRate
	if currentArrival > st.arrivalMomentum {
		st.arrivalMomentum = 0.8*currentArrival + 0.2*st.arrivalMomentum
	} else {
		st.arrivalMomentum = 0.2*currentArrival + 0.8*st.arrivalMomentum
	}
	m.ArrivalRate = st.arrivalMomentum

	effectiveLatency := w.MeanLatencyMs
	if effectiveLatency <= 0 {
		effectiveLatency = 50.0
	}
	if st.accumulatedBacklog > 10000.0 && effectiveLatency > 500.0 {
		effectiveLatency = 500.0
	}

	hazardFactor := math.Exp(-w.Hazard * 0.1)

	baselineServicePerServer := 700.0
	latencyServicePerServer := (1000.0 / math.Max(effectiveLatency, 1e-3)) * hazardFactor

	hazardWeight := math.Min(w.Hazard, 1.0)
	if hazardWeight > 0.5 {
		hazardWeight = 0.5 + (hazardWeight-0.5)*0.2
	}
	serviceRatePerServer := hazardWeight*latencyServicePerServer + (1.0-hazardWeight)*baselineServicePerServer

	appliedScale := 1.0
	if w.AppliedScale > 0 {
		appliedScale = w.AppliedScale
	}
	m.ServiceRate = serviceRatePerServer * c * appliedScale

	// Removed: log.Printf("[service-rate-coupling] ...") — was 1 alloc+write per service per tick.

	netFlowNormalised := (m.ArrivalRate - m.ServiceRate) / c
	st.accumulatedBacklog += netFlowNormalised * c * dt
	if st.accumulatedBacklog < 0 {
		st.accumulatedBacklog = 0.0
	}
	m.MeanQueueLen = st.accumulatedBacklog

	m.Utilisation = m.ArrivalRate / math.Max(m.ServiceRate, 1e-3)
	if m.Utilisation > 1.0 && m.ServiceRate > 0 {
		m.MeanWaitMs = (m.MeanQueueLen / m.ServiceRate) * 1000.0
	} else {
		a := m.ArrivalRate / serviceRatePerServer
		erlangC := computeErlangC(c, a)
		denom := c * serviceRatePerServer * (1.0 - m.Utilisation)
		if denom > 0 {
			m.MeanWaitMs = (erlangC / denom) * 1000.0
		} else {
			m.MeanWaitMs = 0
		}
	}
	m.AdjustedWaitMs = m.MeanWaitMs
	m.MeanSojournMs = m.MeanWaitMs + effectiveLatency
	m.BurstFactor = 1.0

	rawTrend := utilTrendRegression(w, m.ServiceRate)
	m.UtilisationTrend = rawTrend * trustWeight

	if m.Utilisation < 1.0 && m.UtilisationTrend > 1e-6 {
		ttsSec := (1.0 - m.Utilisation) / m.UtilisationTrend
		m.SaturationHorizon = time.Duration(ttsSec * float64(time.Second))
		m.NetworkSaturationHorizon = m.SaturationHorizon
	}
	m.UpstreamPressure = computeInboundPressure(w) * trustWeight

	covPenalty := math.Exp(-w.StdRequestRate / math.Max(w.MeanRequestRate, 1) * 0.5)
	if math.IsNaN(covPenalty) || covPenalty <= 0 {
		covPenalty = 0.5
	}
	m.Confidence = m.Confidence * covPenalty * stalePenalty(w, 2.0)

	// Removed: log.Printf("[physics] ...") — was 1 alloc+write per service per tick.

	_ = trustWeight // used above
	_ = topoSnap    // kept for interface compatibility; upstream pressure already in window
	_ = medianMode  // arrival blend applied upstream

	return m
}

func computeInboundPressure(w *telemetry.ServiceWindow) float64 {
	if w.MeanRequestRate <= 0 {
		return 0
	}
	queueRatio := 0.0
	if w.MeanActiveConns > 0 {
		queueRatio = math.Min(w.LastQueueDepth/w.MeanActiveConns, 1.0)
	}
	arrivalVariance := 0.0
	if w.MeanRequestRate > 0 {
		cov := w.StdRequestRate / w.MeanRequestRate
		arrivalVariance = math.Min(cov/2.0, 1.0)
	}
	return math.Min(0.60*queueRatio+0.40*arrivalVariance, 1.0)
}

func computeErlangC(c, a float64) float64 {
	ci := int(math.Round(c))
	if ci < 1 {
		ci = 1
	}
	rho := a / c
	if rho >= 1.0 {
		return 1.0
	}
	logA := math.Log(a)
	terms := make([]float64, ci)
	for k := 0; k < ci; k++ {
		terms[k] = float64(k)*logA - logFactorial(k)
	}
	maxTerm := terms[0]
	for _, t := range terms {
		if t > maxTerm {
			maxTerm = t
		}
	}
	sumExp := 0.0
	for _, t := range terms {
		sumExp += math.Exp(t - maxTerm)
	}
	logSumK := maxTerm + math.Log(sumExp)
	logLastTerm := float64(ci)*logA - logFactorial(ci) - math.Log(1.0-rho)
	d := logLastTerm - logSumK
	if d > 700 {
		return 1.0
	}
	ratio := math.Exp(d)
	return ratio / (1.0 + ratio)
}

func logFactorial(n int) float64 {
	if n <= 1 {
		return 0
	}
	sum := 0.0
	for i := 2; i <= n; i++ {
		sum += math.Log(float64(i))
	}
	return sum
}

func estimateServiceCoV(w *telemetry.ServiceWindow) float64 {
	if w.LastP99LatencyMs <= 0 || w.MeanLatencyMs <= 0 {
		return 1.0
	}
	actualRatio := w.LastP99LatencyMs / w.MeanLatencyMs
	cov := math.Sqrt(math.Max((actualRatio / 4.6), 0.1))
	return math.Min(cov, 5.0)
}

func utilTrendRegression(w *telemetry.ServiceWindow, serviceRate float64) float64 {
	if w.SampleCount < 3 || serviceRate <= 0 {
		return 0
	}
	halfWindowSec := float64(w.SampleCount) * 2.0 / 2.0
	if halfWindowSec <= 0 {
		return 0
	}
	lastUtil := w.LastRequestRate / serviceRate
	meanUtil := w.MeanRequestRate / serviceRate
	slope := (lastUtil - meanUtil) / halfWindowSec
	conf := confidenceFromSamples(w.SampleCount)
	slope *= conf
	return math.Max(-0.5, math.Min(slope, 0.5))
}

func stalePenalty(w *telemetry.ServiceWindow, tickInterval float64) float64 {
	if w.LastObservedAt.IsZero() {
		return 1.0
	}
	ageSec := time.Since(w.LastObservedAt).Seconds()
	if ageSec <= 0 {
		return 1.0
	}
	return math.Exp(-ageSec / (3.0 * tickInterval))
}

func confidenceFromSamples(n int) float64 {
	if n <= 0 {
		return 0
	}
	return 1.0 - math.Exp(-float64(n)/15.0)
}