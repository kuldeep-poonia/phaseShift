package telemetry

import "time"

type MetricPoint struct {
	ServiceID     string         `json:"service_id"`
	Timestamp     time.Time      `json:"timestamp"`
	RequestRate   float64        `json:"request_rate"`
	ErrorRate     float64        `json:"error_rate"`
	Latency       LatencyStats   `json:"latency"`
	CPUUsage      float64        `json:"cpu_usage"`
	MemUsage      float64        `json:"mem_usage"`
	ActiveConns   int64          `json:"active_conns"`
	QueueDepth    int64          `json:"queue_depth"`
	UpstreamCalls []UpstreamCall `json:"upstream_calls,omitempty"`
}

type LatencyStats struct {
	P50  float64 `json:"p50"`
	P95  float64 `json:"p95"`
	P99  float64 `json:"p99"`
	Mean float64 `json:"mean"`
}

type UpstreamCall struct {
	TargetServiceID string  `json:"target_service_id"`
	CallRate        float64 `json:"call_rate"`
	ErrorRate       float64 `json:"error_rate"`
	LatencyMean     float64 `json:"latency_mean"`
}

type ServiceWindow struct {
	ServiceID        string
	ComputedAt       time.Time
	LastObservedAt   time.Time
	SampleCount      int
	MeanRequestRate  float64
	StdRequestRate   float64
	LastRequestRate  float64
	MeanLatencyMs    float64
	MaxLatencyMs     float64
	LastLatencyMs    float64
	LastP99LatencyMs float64
	MeanErrorRate    float64
	LastErrorRate    float64
	MeanCPU          float64
	MeanMem          float64
	MeanQueueDepth   float64
	LastQueueDepth   float64
	MeanActiveConns  float64
	UpstreamEdges    map[string]EdgeWindow

	// AppliedScale is an optional control directive applied by the controller
	// indicating desired capacity scaling for the service (1.0 = no change).
	// Controllers should write this into the telemetry window to influence
	// how the physics engine computes effective service rate.
	AppliedScale float64
	// ConfidenceScore: composite signal quality [0,1] combining:
	//   - sample count adequacy
	//   - arrival rate stability (low CoV = high confidence)
	//   - data freshness (recency of last observation)
	// Downstream models use this to degrade predictions for noisy windows.
	ConfidenceScore float64

	// SignalQuality classifies the window: "good" | "degraded" | "sparse"
	SignalQuality string

	// Physics Engine States (Injected from simulation)
	Hazard    float64
	Reservoir float64
}

type EdgeWindow struct {
	TargetServiceID string
	MeanCallRate    float64
	MeanErrorRate   float64
	MeanLatencyMs   float64
}
