package modelling

import (
	"math"
	"sync"
	"time"

	"github.com/qphysics/phaseshift/telemetry"
)

const (
	cusumSlack     = 0.5
	cusumThreshold = 5.0
)

// SignalProcessor maintains per-service EWMA and CUSUM signal state.
//
// Original double-lock bug:
//   sp.mu.Lock()            // lock 1: map lookup
//   sp.mu.Unlock()
//   ss := SignalState{...}
//   sp.mu.Lock()            // lock 2: HELD FOR ENTIRE COMPUTATION ← all goroutines serialize here
//   defer sp.mu.Unlock()
//   ... all EWMA + CUSUM math ...
//
// With 8 worker goroutines and 100 services, 7/8 goroutines always waited at
// lock 2 while one ran the EWMA computation. Worker parallelism was negated.
//
// Fix: 32 shards. Lock held only for the map lookup+insert (nanoseconds).
// Computation runs lock-free. Two goroutines never access the same service's
// state concurrently because the orchestrator assigns one goroutine per service.

const signalShards = 32

type signalShard struct {
	mu     sync.Mutex
	states map[string]*signalPerService
}

type SignalProcessor struct {
	shards    [signalShards]signalShard
	fastAlpha float64
	slowAlpha float64
	spikeK    float64
}

type signalPerService struct {
	fastEWMA     float64
	fastVariance float64
	slowEWMA     float64
	cusumPos     float64
	cusumNeg     float64
	initialized  bool
	lastUpdate   time.Time
}

func signalShardFor(id string) int {
	h := uint32(2166136261)
	for i := 0; i < len(id); i++ {
		h ^= uint32(id[i])
		h *= 16777619
	}
	return int(h & (signalShards - 1))
}

func NewSignalProcessor(fastAlpha, slowAlpha, spikeK float64) *SignalProcessor {
	sp := &SignalProcessor{
		fastAlpha: fastAlpha,
		slowAlpha: slowAlpha,
		spikeK:    spikeK,
	}
	for i := range sp.shards {
		sp.shards[i].states = make(map[string]*signalPerService, 8)
	}
	return sp
}

// Update ingests a new window observation and returns the updated SignalState.
//
// Lock protocol:
//   1. Acquire shard mutex — map lookup/insert only (nanoseconds)
//   2. Release immediately after getting the state pointer
//   3. All computation (EWMA, variance, CUSUM) is lock-free
//
// Safe because the orchestrator's worker pool assigns one goroutine per service
// per tick. No two goroutines ever call Update() for the same service ID within
// a single tick interval.
func (sp *SignalProcessor) Update(w *telemetry.ServiceWindow) SignalState {
	x := w.LastRequestRate
	id := w.ServiceID

	// Phase 1: get-or-create state pointer — lock held briefly.
	sh := &sp.shards[signalShardFor(id)]
	sh.mu.Lock()
	st, ok := sh.states[id]
	if !ok {
		st = &signalPerService{}
		sh.states[id] = st
	}
	sh.mu.Unlock()
	// Lock released — ALL computation below is lock-free.

	ss := SignalState{ServiceID: id, ComputedAt: time.Now()}

	if !st.initialized {
		st.fastEWMA = x
		st.slowEWMA = x
		st.fastVariance = math.Max(x*0.01, 1.0)
		st.initialized = true
		st.lastUpdate = w.LastObservedAt
		ss.FastEWMA = x
		ss.SlowEWMA = x
		return ss
	}

	// Noise pre-filter: spike rejection via Winsorisation.
	stdDev := math.Sqrt(math.Max(st.fastVariance, 1e-12))
	filteredX := x
	deviation := x - st.fastEWMA
	if math.Abs(deviation) > sp.spikeK*stdDev {
		filteredX = st.fastEWMA + math.Copysign(sp.spikeK*stdDev, deviation)
	}

	prevFast := st.fastEWMA
	st.fastEWMA = sp.fastAlpha*filteredX + (1-sp.fastAlpha)*st.fastEWMA
	st.slowEWMA = sp.slowAlpha*filteredX + (1-sp.slowAlpha)*st.slowEWMA

	diff := filteredX - prevFast
	st.fastVariance = (1 - sp.fastAlpha) * (st.fastVariance + sp.fastAlpha*diff*diff)

	ss.SpikeDetected = math.Abs(x-prevFast) > sp.spikeK*stdDev

	normalisedDiff := diff / math.Max(stdDev, 1e-12)
	st.cusumPos = math.Max(0, st.cusumPos+normalisedDiff-cusumSlack)
	st.cusumNeg = math.Max(0, st.cusumNeg-normalisedDiff-cusumSlack)
	ss.ChangePointDetected = st.cusumPos > cusumThreshold || st.cusumNeg > cusumThreshold
	if ss.ChangePointDetected {
		st.cusumPos = 0
		st.cusumNeg = 0
	}

	ss.FastEWMA = st.fastEWMA
	ss.SlowEWMA = st.slowEWMA
	ss.EWMAVariance = st.fastVariance
	ss.CUSUMPos = st.cusumPos
	ss.CUSUMNeg = st.cusumNeg
	st.lastUpdate = w.LastObservedAt

	return ss
}

// Prune removes signal state for services that are no longer active.
func (sp *SignalProcessor) Prune(activeIDs map[string]struct{}) {
	for i := range sp.shards {
		sh := &sp.shards[i]
		sh.mu.Lock()
		for id := range sh.states {
			if _, ok := activeIDs[id]; !ok {
				delete(sh.states, id)
			}
		}
		sh.mu.Unlock()
	}
}