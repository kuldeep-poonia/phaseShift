package telemetry

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// numShards is the number of independent lock domains.
// Must be a power of two so shardFor() can use bitwise AND.
// 64 shards → lock contention drops to ~1/64 of a single-mutex design.
const numShards = 64

type storeShard struct {
	mu       sync.RWMutex
	buffers  map[string]*RingBuffer
	lastSeen map[string]time.Time
}

// Store is a sharded, concurrent telemetry store.
// Each service is assigned to a shard by FNV-1a hash of its service ID.
// Writes to different services never contend. AllWindows() locks shards
// one at a time (never all at once) so ingest is only blocked for the
// duration of a single shard snapshot, not the entire window computation.
type Store struct {
	shards        [numShards]storeShard
	appliedScales sync.Map // map[string]float64 — no lock needed
	bufCap        int
	maxSvc        int
	svcCount      atomic.Int64 // fast path: admit check without locking all shards
	staleAge      time.Duration
}

// shardFor maps a service ID to a shard index using FNV-1a (no import needed).
func shardFor(serviceID string) int {
	h := uint32(2166136261)
	for i := 0; i < len(serviceID); i++ {
		h ^= uint32(serviceID[i])
		h *= 16777619
	}
	return int(h & (numShards - 1))
}

func NewStore(bufferCapacity, maxServices int, staleAge time.Duration) *Store {
	s := &Store{
		bufCap:   bufferCapacity,
		maxSvc:   maxServices,
		staleAge: staleAge,
	}
	for i := range s.shards {
		s.shards[i].buffers = make(map[string]*RingBuffer, maxServices/numShards+1)
		s.shards[i].lastSeen = make(map[string]time.Time, maxServices/numShards+1)
	}
	return s
}

func (s *Store) StaleAge() time.Duration { return s.staleAge }

//  sanitize 

func finiteOrZero(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

func sanitizePoint(p *MetricPoint) bool {
	if p.ServiceID == "" {
		return false
	}
	p.RequestRate = finiteOrZero(p.RequestRate)
	p.ErrorRate = finiteOrZero(p.ErrorRate)
	p.Latency.Mean = finiteOrZero(p.Latency.Mean)
	p.Latency.P50 = finiteOrZero(p.Latency.P50)
	p.Latency.P95 = finiteOrZero(p.Latency.P95)
	p.Latency.P99 = finiteOrZero(p.Latency.P99)
	p.CPUUsage = finiteOrZero(p.CPUUsage)
	p.MemUsage = finiteOrZero(p.MemUsage)
	if p.RequestRate < 0 {
		p.RequestRate = 0
	}
	const maxReasonableRPS = 10_000_000.0
	const maxReasonableLatencyMs = 3_600_000.0
	if p.RequestRate > maxReasonableRPS {
		p.RequestRate = maxReasonableRPS
	}
	if p.Latency.Mean > maxReasonableLatencyMs {
		p.Latency.Mean = maxReasonableLatencyMs
	}
	if p.Latency.P50 > maxReasonableLatencyMs {
		p.Latency.P50 = maxReasonableLatencyMs
	}
	if p.Latency.P95 > maxReasonableLatencyMs {
		p.Latency.P95 = maxReasonableLatencyMs
	}
	if p.Latency.P99 > maxReasonableLatencyMs {
		p.Latency.P99 = maxReasonableLatencyMs
	}
	if p.ErrorRate < 0 {
		p.ErrorRate = 0
	}
	if p.ErrorRate > 1 {
		p.ErrorRate = 1
	}
	if p.Latency.Mean < 0 {
		p.Latency.Mean = 0
	}
	if p.Latency.P50 < 0 {
		p.Latency.P50 = 0
	}
	if p.Latency.P95 < 0 {
		p.Latency.P95 = 0
	}
	if p.Latency.P99 < 0 {
		p.Latency.P99 = 0
	}
	const minLatencyMs = 0.1
	if p.Latency.Mean == 0 && p.Latency.P50 == 0 && p.Latency.P95 == 0 {
		p.Latency.Mean = minLatencyMs
	}
	if p.ActiveConns < 0 {
		p.ActiveConns = 0
	}
	if p.QueueDepth < 0 {
		p.QueueDepth = 0
	}
	return true
}

//  Ingest 

// Ingest appends a MetricPoint. New services are admitted up to maxSvc.
// Lock scope: one shard only. Concurrent writes to different services
// never serialise against each other.
func (s *Store) Ingest(p *MetricPoint) {
	if !sanitizePoint(p) {
		return
	}
	if p.Timestamp.IsZero() {
		p.Timestamp = time.Now()
	}

	sh := &s.shards[shardFor(p.ServiceID)]

	sh.mu.Lock()
	buf, ok := sh.buffers[p.ServiceID]
	if !ok {

	for {
		current := s.svcCount.Load()

		if current >= int64(s.maxSvc) {
			sh.mu.Unlock()
			return
		}

		if s.svcCount.CompareAndSwap(
			current,
			current+1,
		) {
			break
		}
	}

	buf = NewRingBuffer(s.bufCap)
	sh.buffers[p.ServiceID] = buf
}
	sh.lastSeen[p.ServiceID] = p.Timestamp
	sh.mu.Unlock()

	// RingBuffer.Push has its own per-buffer lock. No shard lock held here.
	buf.Append(p)
}

//  SetAppliedScale 

// SetAppliedScale records the last scale directive for a service.
// Uses sync.Map — zero mutex overhead on the hot read path.
func (s *Store) SetAppliedScale(serviceID string, scale float64) {
	s.appliedScales.Store(serviceID, scale)
}

//  Prune 

// Prune removes stale services shard by shard. Holds one shard lock at a time.
func (s *Store) Prune(now time.Time) []string {
	var pruned []string
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		for id, t := range sh.lastSeen {
			if now.Sub(t) > s.staleAge {
				delete(sh.buffers, id)
				delete(sh.lastSeen, id)
				s.svcCount.Add(-1)
				pruned = append(pruned, id)
			}
		}
		sh.mu.Unlock()
	}
	return pruned
}

//  HasServices 

// HasServices is O(1), zero-allocation. Safe in hot paths.
func (s *Store) HasServices() bool {
	return s.svcCount.Load() > 0
}

//  ServiceIDs 

func (s *Store) ServiceIDs() []string {
	ids := make([]string, 0, int(s.svcCount.Load()))
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()
		for id := range sh.buffers {
			ids = append(ids, id)
		}
		sh.mu.RUnlock()
	}
	return ids
}

//  Window 

// Window computes a ServiceWindow over the most recent n points.
func (s *Store) Window(serviceID string, n int, freshnessCutoff time.Duration) *ServiceWindow {
	sh := &s.shards[shardFor(serviceID)]

	sh.mu.RLock()
	buf, ok := sh.buffers[serviceID]
	sh.mu.RUnlock()

	if !ok {
		return nil
	}
	last := buf.Last()
	if last.Timestamp.IsZero() {
		return nil
	}
	if freshnessCutoff > 0 && time.Since(last.Timestamp) > freshnessCutoff {
		return nil
	}
	points := buf.Snapshot(n)
	if len(points) == 0 {
		return nil
	}
	return computeWindow(serviceID, points)
}

//  AllWindows 

// AllWindows computes windows for every known service.
//
// Optimization over the original: shard locks are held only to snapshot
// buffer pointers (nanoseconds), not during computeWindow (microseconds).
// Ingest() is blocked per shard only for pointer-copy duration, not for the
// entire window computation loop. At 64 shards and 2000 services this
// reduces mean ingest blocking time from ~O(N×computeWindow) to ~O(N/64×pointer).
func (s *Store) AllWindows(n int, freshnessCutoff time.Duration) map[string]*ServiceWindow {
	type entry struct {
		id  string
		buf *RingBuffer
	}

	total := int(s.svcCount.Load())
	if total == 0 {
		return map[string]*ServiceWindow{}
	}

	entries := make([]entry, 0, total)
	now := time.Now()

	// Phase 1: snapshot buffer pointers — one shard at a time, lock held briefly.
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()
		for id, buf := range sh.buffers {
			if freshnessCutoff > 0 {
				t, seen := sh.lastSeen[id]
				if !seen || now.Sub(t) > freshnessCutoff {
					continue
				}
			}
			entries = append(entries, entry{id, buf})
		}
		sh.mu.RUnlock()
	}

	// Phase 2: compute windows — no lock held at all.
	out := make(map[string]*ServiceWindow, len(entries))
	for _, e := range entries {
		pts := e.buf.Snapshot(n) // RingBuffer has its own lock
		if len(pts) == 0 {
			continue
		}
		w := computeWindow(e.id, pts)
		if w == nil {
			continue
		}
		if v, ok := s.appliedScales.Load(e.id); ok {
			w.AppliedScale = v.(float64)
		}
		out[e.id] = w
	}
	return out
}

//  computeWindow 

func computeWindow(serviceID string, pts []MetricPoint) *ServiceWindow {
	n := float64(len(pts))
	var sumReq, sumErr, sumLat, maxLat, sumCPU, sumMem, sumQueue, sumConns float64
	var edgeSums map[string]*edgeAccum

	for _, p := range pts {
		sumReq += p.RequestRate
		sumErr += p.ErrorRate
		sumLat += p.Latency.Mean
		if p.Latency.Mean > maxLat {
			maxLat = p.Latency.Mean
		}
		sumCPU += p.CPUUsage
		sumMem += p.MemUsage
		sumQueue += float64(p.QueueDepth)
		sumConns += float64(p.ActiveConns)
		for _, uc := range p.UpstreamCalls {
			uc.CallRate = finiteOrZero(uc.CallRate)
			uc.ErrorRate = finiteOrZero(uc.ErrorRate)
			uc.LatencyMean = finiteOrZero(uc.LatencyMean)
			if edgeSums == nil {
				edgeSums = make(map[string]*edgeAccum)
			}
			acc, exists := edgeSums[uc.TargetServiceID]
			if !exists {
				acc = &edgeAccum{target: uc.TargetServiceID}
				edgeSums[uc.TargetServiceID] = acc
			}
			acc.sumCallRate += uc.CallRate
			acc.sumErrRate += uc.ErrorRate
			acc.sumLatency += uc.LatencyMean
			acc.count++
		}
	}

	last := pts[len(pts)-1]

	meanReq := sumReq / n
	var sumSqDiff float64
	for _, p := range pts {
		d := p.RequestRate - meanReq
		sumSqDiff += d * d
	}
	stdReq := 0.0
	if n > 1 {
		stdReq = math.Sqrt(sumSqDiff / (n - 1))
	}

	lastP99 := last.Latency.P99
	if lastP99 <= 0 && last.Latency.P95 > 0 {
		lastP99 = last.Latency.P95 * 1.20
	}
	if lastP99 <= 0 && last.Latency.Mean > 0 {
		lastP99 = last.Latency.Mean * 2.5
	}

	meanConns := sumConns / n
	if meanConns < 1.0 && meanReq > 0 && sumLat > 0 {
		inferredConns := meanReq * (sumLat / n / 1000.0)
		if inferredConns >= 1.0 {
			meanConns = inferredConns
		} else {
			meanConns = 1.0
		}
	}

	lastQueueDepth := float64(last.QueueDepth)
	meanQueueDepth := sumQueue / n
	if lastQueueDepth == 0 && last.Latency.P50 > 0 && last.Latency.Mean > last.Latency.P50*1.5 {
		excessFrac := (last.Latency.Mean - last.Latency.P50) / last.Latency.Mean
		lastQueueDepth = excessFrac * meanConns
		if meanQueueDepth == 0 {
			meanQueueDepth = lastQueueDepth / n
		}
	}

	sampleConf := 1.0 - math.Exp(-float64(len(pts))/15.0)
	cov := 0.0
	if meanReq > 0 {
		cov = stdReq / meanReq
	}
	stabilityConf := math.Exp(-cov * 0.5)
	ageSec := time.Since(last.Timestamp).Seconds()
	freshnessConf := math.Exp(-ageSec / 6.0)
	confidence := sampleConf * stabilityConf * freshnessConf

	quality := "good"
	switch {
	case len(pts) < 3 || confidence < 0.3:
		quality = "sparse"
	case confidence < 0.65:
		quality = "degraded"
	}

	edges := make(map[string]EdgeWindow, len(edgeSums))
	for tid, acc := range edgeSums {
		c := float64(acc.count)
		edges[tid] = EdgeWindow{
			TargetServiceID: tid,
			MeanCallRate:    acc.sumCallRate / c,
			MeanErrorRate:   acc.sumErrRate / c,
			MeanLatencyMs:   acc.sumLatency / c,
		}
	}

	return &ServiceWindow{
		ServiceID:        serviceID,
		ComputedAt:       time.Now(),
		SampleCount:      len(pts),
		MeanRequestRate:  meanReq,
		StdRequestRate:   stdReq,
		LastRequestRate:  last.RequestRate,
		MeanLatencyMs:    sumLat / n,
		MaxLatencyMs:     maxLat,
		LastLatencyMs:    last.Latency.Mean,
		LastP99LatencyMs: lastP99,
		MeanErrorRate:    sumErr / n,
		LastErrorRate:    last.ErrorRate,
		MeanCPU:          sumCPU / n,
		MeanMem:          sumMem / n,
		MeanQueueDepth:   meanQueueDepth,
		LastQueueDepth:   lastQueueDepth,
		MeanActiveConns:  meanConns,
		UpstreamEdges:    edges,
		LastObservedAt:   last.Timestamp,
		ConfidenceScore:  confidence,
		SignalQuality:    quality,
	}
}

type edgeAccum struct {
	target      string
	sumCallRate float64
	sumErrRate  float64
	sumLatency  float64
	count       int
}