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
	if dt < 0.5 || dt > 10.0 {
		// Sub-500ms: rapid successive calls (warm-up, tests, or same-tick re-reads).
		// Use default tick interval. Production orchestrator always runs at ≥ 2s.
		dt = 2.0
	}
	st.lastTickTime = time.Now()

	m := QueueModel{
		ServiceID:  w.ServiceID,
		ComputedAt: time.Now(),
		Confidence: confidenceFromSamples(w.SampleCount),
	}
	// Guard: sanitize inputs. The store.sanitizePoint handles this for Prometheus-ingested
	// windows, but windows constructed directly (OTel edge consumer, what-if simulator)
	// can carry NaN/Inf. One NaN here would corrupt all downstream calculations.
	if math.IsNaN(w.MeanRequestRate) || math.IsInf(w.MeanRequestRate, 0) {
		w.MeanRequestRate = 0
	}
	if math.IsNaN(w.MeanLatencyMs) || math.IsInf(w.MeanLatencyMs, 0) || w.MeanLatencyMs < 0 {
		w.MeanLatencyMs = 0
	}
	if math.IsNaN(w.MeanActiveConns) || math.IsInf(w.MeanActiveConns, 0) {
		w.MeanActiveConns = 1
	}
	if w.MeanRequestRate <= 0 || w.MeanLatencyMs <= 0 {
		return m
	}
	m.Hazard = w.Hazard
	m.Reservoir = w.Reservoir

	trustWeight := math.Max(w.ConfidenceScore, 0.10)

	c := math.Max(math.Round(w.MeanActiveConns), 1.0)
	m.Concurrency = c

	// medianMode=true: use medianBiasedRate for burst-resistant arrival estimation.
	// This Winsorises the last sample toward the mean when it deviates > 1.5σ,
	// preventing a single traffic spike from dominating the arrival estimate.
	var currentArrival float64
	if medianMode {
		currentArrival = medianBiasedRate(w.LastRequestRate, w.MeanRequestRate, w.StdRequestRate)
	} else {
		currentArrival = w.MeanRequestRate
	}
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

	// Previously: hazardWeight=0 at hazard=0, so latency was completely ignored.
	// Fix: always blend latency into service rate (minimum 40% weight),
	// scaled further by hazard. This means latency changes are always visible.
	hazardWeight := math.Min(w.Hazard, 1.0)
	if hazardWeight > 0.5 {
		hazardWeight = 0.5 + (hazardWeight-0.5)*0.2
	}
	// Minimum latency blend: 0.4 at hazard=0, increases to 1.0 at high hazard.
	latencyBlend := 0.4 + 0.6*hazardWeight
	serviceRatePerServer := latencyBlend*latencyServicePerServer + (1.0-latencyBlend)*baselineServicePerServer

	appliedScale := 1.0
	if w.AppliedScale > 0 {
		appliedScale = w.AppliedScale
	}
	m.ServiceRate = serviceRatePerServer * c * appliedScale

	// Removed: log.Printf("[service-rate-coupling] ...") — was 1 alloc+write per service per tick.

	netFlowNormalised := (m.ArrivalRate - m.ServiceRate) / c
	st.accumulatedBacklog += netFlowNormalised * c * dt
	st.accumulatedBacklog = pos(st.accumulatedBacklog)
	// MeanQueueLen is now set in the utilisation block below:
	// - rho >= 1: physical accumulated backlog
	// - rho <  1: Erlang-C / Little's Law formula

	m.Utilisation = m.ArrivalRate / math.Max(m.ServiceRate, 1e-3)
	if m.Utilisation > 1.0 && m.ServiceRate > 0 {
		// Overload: use accumulated physical backlog (grows with time).
		m.MeanQueueLen = st.accumulatedBacklog
		m.MeanWaitMs = (m.MeanQueueLen / m.ServiceRate) * 1000.0
	} else {
		// Sub-saturation: use M/M/c Erlang-C queue length formula.
		// E[Lq] = C(c,a) * rho / (c*(1-rho))
		// where a = arrival/serviceRatePerServer, rho = a/c
		// Previously MeanQueueLen was always 0 below saturation — wrong.
		a := m.ArrivalRate / serviceRatePerServer
		erlangC := computeErlangC(c, a)
		denom := c * serviceRatePerServer * (1.0 - m.Utilisation)
		m.MeanWaitMs = safeDiv(erlangC, denom, 0) * 1000.0
		// E[Lq] = λ × E[Wq] / c  — corrected M/M/c Little's Law.
		// Without /c this is factor-of-c too large for multi-server queues.
		// Derivation: E[Lq] = C(c,a) × ρ / (c × (1-ρ))
		//             E[Wq] = C(c,a) / (c × μ × (1-ρ))
		//             E[Lq] = λ × E[Wq] = ρ × c × μ × E[Wq] / c = ρ × μ × E[Wq]
		// Since μ = svcRatePerServer and arrival = ρ × c × μ:
		//   arrival × E[Wq] / c = ρ × μ × E[Wq] = ρ × C(c,a)/(c×μ×(1-ρ)) × μ = correct
		m.MeanQueueLen = m.ArrivalRate * safeDiv(m.MeanWaitMs, 1000.0, 0) / m.Concurrency
	}
	m.AdjustedWaitMs = m.MeanWaitMs
	m.MeanSojournMs = m.MeanWaitMs + effectiveLatency
	// BurstFactor from M/G/1: (1 + CoV²) / 2 using service time CoV from P99/mean ratio.
	// Previously hardcoded to 1.0 (M/M/1 assumption). Now uses actual latency distribution.
	serviceCoV := estimateServiceCoV(w)
	m.BurstFactor = (1.0 + serviceCoV*serviceCoV) / 2.0

	rawTrend := utilTrendRegression(w, m.ServiceRate, dt)
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

// utilTrendRegression computes utilisation trend in ρ/second.
//
// Previous bug: slope was divided by halfWindowSec = SampleCount (60s),
// but (lastUtil - meanUtil) represents change over ONE tick interval (dt),
// not over the entire window. This caused 30-60× overestimate of time-to-saturation.
//
// Fix: divide by dt (actual elapsed seconds this tick). The trend is:
//   slope = (lastUtil - meanUtil) / dt   [ρ per second]
// Then SaturationHorizon = (1 - currentRho) / slope  [seconds to ρ=1]
func utilTrendRegression(w *telemetry.ServiceWindow, serviceRate, dt float64) float64 {
	if w.SampleCount < 3 || serviceRate <= 0 {
		return 0
	}
	if dt <= 0 {
		dt = 2.0
	}
	lastUtil := w.LastRequestRate / serviceRate
	meanUtil := w.MeanRequestRate / serviceRate
	// slope = change in ρ per second (lastRate is most recent, meanRate is window average)
	slope := (lastUtil - meanUtil) / dt
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
	return clamp01(math.Exp(-ageSec / (3.0 * tickInterval)))
}

func confidenceFromSamples(n int) float64 {
	if n <= 0 {
		return 0
	}
	return clamp01(1.0 - math.Exp(-float64(n)/15.0))
}