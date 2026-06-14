package modelling

import (
	"math"
	"time"

	"github.com/qphysics/phaseshift/telemetry"
)

// RunStochasticModel computes arrival variance, burst amplification, and
// risk propagation probability based on observed traffic statistics.
func RunStochasticModel(w *telemetry.ServiceWindow) StochasticModel {
	sm := StochasticModel{
		ServiceID:  w.ServiceID,
		ComputedAt: time.Now(),
	}
	if w.MeanRequestRate <= 0 || w.SampleCount < 2 {
		return sm
	}

	sm.ArrivalCoV = w.StdRequestRate / w.MeanRequestRate

	// M/G/1 burst amplification from arrival-side PKC interpretation.
	sm.BurstAmplification = (1.0 + sm.ArrivalCoV*sm.ArrivalCoV) / 2.0

	// Risk propagation: probability that a burst reaches downstream services.
	// Modelled as P(burst) × P(downstream is within 1σ of saturation).
	// P(burst) ≈ tail probability of Pareto-distributed inter-arrivals.
	// Simplified: sigmoid of CoV above threshold.
	sm.RiskPropagation = sigmoid((sm.ArrivalCoV - 1.0) / 0.3)

	sampleConf := confidenceFromSamples(w.SampleCount)
	covPenalty := math.Exp(-sm.ArrivalCoV * 0.3)
	sm.Confidence = sampleConf * covPenalty

	return sm
}
