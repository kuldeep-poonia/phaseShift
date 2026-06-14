package modelling

import "time"

type QueueModel struct {
	ServiceID         string
	ComputedAt        time.Time
	ArrivalRate       float64
	ServiceRate       float64
	Concurrency       float64
	Utilisation       float64
	MeanQueueLen      float64
	MeanWaitMs        float64
	MeanSojournMs     float64
	BurstFactor       float64
	AdjustedWaitMs    float64
	UtilisationTrend  float64
	SaturationHorizon time.Duration
	Confidence        float64
	// Network-coupled metrics
	UpstreamPressure         float64       // normalised upstream load pressure [0..1]
	NetworkSaturationHorizon time.Duration // saturation horizon accounting for upstream pressure
	// Physics Engine States
	Hazard    float64
	Reservoir float64
}

type StochasticModel struct {
	ServiceID          string
	ComputedAt         time.Time
	ArrivalCoV         float64
	BurstAmplification float64
	RiskPropagation    float64 // probability that burst reaches downstream
	Confidence         float64
}

type SignalState struct {
	ServiceID           string
	ComputedAt          time.Time
	FastEWMA            float64
	SlowEWMA            float64
	EWMAVariance        float64
	SpikeDetected       bool
	ChangePointDetected bool
	CUSUMPos            float64
	CUSUMNeg            float64
}

type StabilityAssessment struct {
	ServiceID           string
	ComputedAt          time.Time
	StabilityMargin     float64
	CollapseRisk        float64
	OscillationRisk     float64
	FeedbackGain        float64
	IsUnstable          bool
	PredictedCollapseMs float64
	// Enhanced fields
	CascadeAmplificationScore float64 // FeedbackGain × CollapseRisk × (1+OscillationRisk)
	CollapseZone              string  // "safe" | "warning" | "collapse"
	// Trend-adjusted margin: pessimistic distance to saturation accounting for load trend.
	// TrendAdjustedMargin = StabilityMargin - UtilisationTrend × horizonSec
	// Negative when the service is projected to cross ρ=1 within the prediction horizon.
	TrendAdjustedMargin float64
	// StabilityDerivative is the rate of change of CollapseRisk (dRisk/dρ × dρ/dt).
	// Positive = risk accelerating; negative = risk decelerating.
	// This measures how quickly the system is moving toward or away from collapse.
	StabilityDerivative float64
}

type ServiceModelBundle struct {
	Queue      QueueModel
	Stochastic StochasticModel
	Signal     SignalState
	Stability  StabilityAssessment
}
