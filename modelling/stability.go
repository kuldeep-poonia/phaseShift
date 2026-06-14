package modelling

import (
	"math"
	"time"

	"github.com/qphysics/phaseshift/topology"
)

// RunStabilityAssessment computes nonlinear stability characteristics,
// including oscillation risk, collapse zone classification, and cascade amplification.
func RunStabilityAssessment(
	q QueueModel,
	sig SignalState,
	topoSnap topology.GraphSnapshot,
	collapseThreshold float64,
) StabilityAssessment {
	sa := StabilityAssessment{
		ServiceID:  q.ServiceID,
		ComputedAt: time.Now(),
	}

	rho := q.Utilisation
	// Physics engine coupling: Hazard and Reservoir increase risk
	// Hazard (Z) represents structural degradation
	// Reservoir (R) represents hidden metabolic debt
	effectiveRho := rho + q.Hazard*0.2 + q.Reservoir*0.1

	// Stability margin: distance to saturation boundary.
	sa.StabilityMargin = 1.0 - effectiveRho

	// CollapseZone classification derived from collapseThreshold config.
	// Warning zone starts at 83% of threshold; collapse zone at threshold.
	warningBoundary := collapseThreshold * 0.83
	switch {
	case effectiveRho >= collapseThreshold:
		sa.CollapseZone = "collapse"
	case effectiveRho >= warningBoundary:
		sa.CollapseZone = "warning"
	default:
		sa.CollapseZone = "safe"
	}

	// Collapse risk: steep sigmoid centred on collapseThreshold.
	sa.CollapseRisk = sigmoid((effectiveRho - collapseThreshold*0.95) / 0.04)

	// Trend-adjusted stability margin: pessimistic estimate over the MPC horizon.
	// Uses a 10-tick prediction horizon at 2s per tick = 20 seconds.
	const predHorizonSec = 20.0
	projectedRho := rho + q.UtilisationTrend*predHorizonSec
	sa.TrendAdjustedMargin = 1.0 - math.Max(rho, projectedRho)

	// StabilityDerivative: d(CollapseRisk)/dt = d(CollapseRisk)/dρ × dρ/dt
	// d(sigmoid)/dρ = sigmoid × (1-sigmoid) / scale_factor
	const sigScale = 0.04
	dRiskDRho := sa.CollapseRisk * (1.0 - sa.CollapseRisk) / sigScale
	sa.StabilityDerivative = dRiskDRho * q.UtilisationTrend

	// Oscillation risk: two-timescale EWMA divergence.
	// We use both relative divergence (fast vs slow) and the absolute variance
	// to detect instability even when both EWMAs are moving together rapidly.
	oscillationSignal := 0.0
	if sig.SlowEWMA > 0 {
		relativeDivergence := math.Abs(sig.FastEWMA-sig.SlowEWMA) / sig.SlowEWMA
		// Also incorporate EWMA variance relative to mean (normalised noise level).
		varianceSignal := 0.0
		if sig.FastEWMA > 0 {
			varianceSignal = math.Sqrt(math.Max(sig.EWMAVariance, 0)) / math.Max(sig.FastEWMA, 1e-6)
		}
		// RMS combination of both signals.
		oscillationSignal = math.Sqrt((relativeDivergence*relativeDivergence + varianceSignal*varianceSignal) / 2.0)
	}
	sa.OscillationRisk = math.Min(oscillationSignal*4.0, 1.0) // scale to 0..1

	// Feedback gain from outbound topology.
	sa.FeedbackGain = outboundFeedbackGain(q.ServiceID, topoSnap)

	// Instability: either collapse threshold crossed or oscillating at high load.
	sa.IsUnstable = sa.CollapseRisk > 0.85 || (sa.OscillationRisk > 0.6 && rho > 0.7) || rho >= 1.0

	// Predicted collapse: extrapolate from utilisation trend.
	if rho < 1.0 && q.UtilisationTrend > 1e-6 {
		ttsMs := ((1.0 - rho) / q.UtilisationTrend) * 1000.0
		sa.PredictedCollapseMs = ttsMs
	} else if rho >= 1.0 {
		sa.PredictedCollapseMs = 0
	} else {
		sa.PredictedCollapseMs = -1
	}

	// Cascade amplification: high feedback gain at high utilisation magnifies risk.
	// Apply amplification factor to collapse risk.
	if sa.FeedbackGain > 0.5 && rho > collapseThreshold*0.75 {
		amplification := 1.0 + sa.FeedbackGain*0.4
		sa.CollapseRisk = math.Min(sa.CollapseRisk*amplification, 1.0)
	}

	// CascadeAmplificationScore: product of three risk dimensions.
	// Measures systemic risk propagation potential:
	// high feedback × high collapse risk × (1 + oscillation instability)
	sa.CascadeAmplificationScore = sa.FeedbackGain * sa.CollapseRisk * (1.0 + sa.OscillationRisk)

	// Account for network coupling: upstream pressure raises effective collapse risk.
	if q.UpstreamPressure > 0.3 {
		pressureBoost := sigmoid((q.UpstreamPressure - 0.3) / 0.2)
		sa.StabilityMargin = sa.StabilityMargin * (1.0 - pressureBoost*0.2)
		sa.StabilityMargin = math.Max(sa.StabilityMargin, 0)
	}

	return sa
}

func outboundFeedbackGain(serviceID string, snap topology.GraphSnapshot) float64 {
	totalWeight := 0.0
	count := 0
	for _, e := range snap.Edges {
		if e.Source == serviceID && e.Weight > 0 {
			totalWeight += e.Weight
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return math.Min(totalWeight/float64(count), 1.0)
}
