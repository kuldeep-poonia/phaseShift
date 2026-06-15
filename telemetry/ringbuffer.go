package telemetry

import (
	"math"
	"sync"
	"time"
)

type RingBuffer struct {
	mu   sync.RWMutex
	buf  []MetricPoint
	head int // index of the next write slot
	size int // number of valid entries currently stored
	capacity  int
}

// NewRingBuffer allocates a RingBuffer of the given capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		buf: make([]MetricPoint, capacity),
		capacity: capacity,
	}
}

// Append appends a MetricPoint, evicting the oldest if the buffer is full.
func (r *RingBuffer) Append(p *MetricPoint) {
	r.mu.Lock()
	defer r.mu.Unlock()

	dst := &r.buf[r.head]

	// 1. CRITICAL: Release the previous slice reference so GC can claim it
	dst.UpstreamCalls = nil

	// 2. Copy scalars
	dst.ServiceID = p.ServiceID
	dst.Timestamp = p.Timestamp
	dst.RequestRate = p.RequestRate
	dst.ErrorRate = p.ErrorRate
	dst.Latency = p.Latency
	dst.ActiveConns = p.ActiveConns
	dst.QueueDepth = p.QueueDepth
	dst.CPUUsage = p.CPUUsage
	dst.MemUsage = p.MemUsage

	// 3. Deep copy the new slice
	if len(p.UpstreamCalls) > 0 {
		dst.UpstreamCalls = make([]UpstreamCall, len(p.UpstreamCalls))
		copy(dst.UpstreamCalls, p.UpstreamCalls)
	}

	r.head = (r.head + 1) % r.capacity
	if r.size < r.capacity {
		r.size++
	}
}

func (r *RingBuffer) Snapshot(n ...int) []MetricPoint {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.size == 0 {
		return nil
	}

	count := r.size
	if len(n) > 0 && n[0] > 0 && n[0] < count {
		count = n[0]
	}

	out := make([]MetricPoint, count)
	start := (r.head - count + r.capacity) % r.capacity
	for i := 0; i < count; i++ {
		src := r.buf[(start+i)%r.capacity]

out[i] = src

if len(src.UpstreamCalls) > 0 {
	out[i].UpstreamCalls =
		make([]UpstreamCall, len(src.UpstreamCalls))

	copy(
		out[i].UpstreamCalls,
		src.UpstreamCalls,
	)
}
	}
	return out
}

// Last returns the most recently pushed point, or nil if empty.
func (r *RingBuffer) Last() MetricPoint {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.size == 0 {
		return MetricPoint{}
	}
	idx := (r.head - 1 + r.capacity) % r.capacity
	return r.buf[idx]
}

// Size returns the current number of stored entries.
func (r *RingBuffer) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.size
}

type RingSummary struct {
	Count         int
	MeanReqRate   float64
	StdReqRate    float64
	MeanLatencyMs float64
	MaxLatencyMs  float64
	MeanErrorRate float64
	OldestAt      time.Time
	NewestAt      time.Time
}

// full point slice.
func (r *RingBuffer) SummaryStats() RingSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.size == 0 {
		return RingSummary{}
	}

	var sumReq, sumReqSq, sumLat, maxLat, sumErr float64
	start := (r.head - r.size + r.capacity) % r.capacity
	oldest := r.buf[start]
	newest := r.buf[(r.head-1+r.capacity)%r.capacity]

	for i := 0; i < r.size; i++ {
		p := r.buf[(start+i)%r.capacity]
		sumReq += p.RequestRate
		sumReqSq += p.RequestRate * p.RequestRate
		sumLat += p.Latency.Mean
		if p.Latency.Mean > maxLat {
			maxLat = p.Latency.Mean
		}
		sumErr += p.ErrorRate
	}

	n := float64(r.size)
	mean := sumReq / n
	variance := sumReqSq/n - mean*mean
	if variance < 0 {
		variance = 0
	}

	oldestAt := oldest.Timestamp
	newestAt := newest.Timestamp

	return RingSummary{
		Count:         r.size,
		MeanReqRate:   mean,
		StdReqRate:    math.Sqrt(variance),
		MeanLatencyMs: sumLat / n,
		MaxLatencyMs:  maxLat,
		MeanErrorRate: sumErr / n,
		OldestAt:      oldestAt,
		NewestAt:      newestAt,
	}
}
