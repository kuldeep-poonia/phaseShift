package autopilot

import "math"

// ConfidenceState carries the single persistent value needed for temporal smoothing.
type ConfidenceState struct {
	PrevConfidence float64
}

type ConfidenceInput struct {
	TrendConsistency     float64
	SignalAgreement      float64
	ControlEffectiveness float64
	Oscillation          float64
}

// ComputeConfidence derives a bounded confidence score from control signals.

func ComputeConfidence(prev ConfidenceState, in ConfidenceInput) (float64, ConfidenceState) {

	c := clamp01(in.TrendConsistency)
	a := clamp01(in.SignalAgreement)
	e := clamp01(in.ControlEffectiveness)
	osc := clamp01(in.Oscillation)

	// Coherence: rewards both magnitude and agreement between trend and signal.
	agreement := 1.0 - math.Abs(c-a)
	magnitude := (c + a) * 0.5
	coherence := 0.5*magnitude + 0.5*agreement

	// Control effectiveness: nonlinear gain with discrimination at the low end.
	controlGain := (0.2 + 0.8*e*e) / (1.0 + 0.2*(1.0-e))

	// Instability: dominant (max) over additive dilution
	mismatch := math.Abs(c - a)
	instability := max3(mismatch, 1.0-c, 1.0-e)
	instability = clamp01(instability)

	// Short-term stability (incorporating oscillation)
	// Coefficients reduced from 5.0/3.0 to 3.0/2.0 — mild oscillation
	// should reduce confidence moderately, not crush it.
	shortTermRisk := 0.6*instability + 0.4*osc
	stabilityFactor := 1.0 / (1.0 + 3.0*shortTermRisk + 2.0*shortTermRisk*shortTermRisk)

	raw := coherence * controlGain * stabilityFactor

	// Fast collapse under clearly unsafe conditions.
	// Fast collapse: instability must be present AND corroborated.
	// e<0.2 alone (cold start, new memory) must NOT trigger collapse.
	if instability > 0.8 && (osc > 0.5 || e < 0.15) {
		raw *= 0.15
	} else if osc > 0.8 {
		raw *= 0.3
	}

	// Controlled saturation — softer curve allows higher raw values through
	conf := raw / (0.40 + 0.60*raw)

	// Temporal smoothing: alpha accelerates when confidence is low (fast recovery
	// from collapse) and decelerates when confidence is high (resist noise).
	// Wider range for faster recovery from collapse states.
	alpha := 0.25 + 0.30*(1.0-prev.PrevConfidence)
	if alpha > 0.85 {
		alpha = 0.85
	}

	conf = (1-alpha)*prev.PrevConfidence + alpha*conf
	conf = clamp01(conf)

	// Recovery: when the system has been calm for a sustained period,
	// grow confidence toward 1.0 regardless of history.
	// calmness = (absence of oscillation) × (trend consistency) — purely signal-based.
	// Threshold lowered to 0.35 so moderate oscillation doesn't block recovery.
	calmness := (1.0 - in.Oscillation) * in.TrendConsistency
	if calmness > 0.35 && in.ControlEffectiveness > 0.3 && conf < 0.6 {
		recovery := 0.08 * (1.0 - conf)
		conf = clamp01(conf + recovery)
	}

	return conf, ConfidenceState{PrevConfidence: conf}
}

// max3 returns the largest of three float64 values.
// Local to this file — used only in the instability computation above.
func max3(a, b, c float64) float64 {
	if a > b {
		if a > c {
			return a
		}
		return c
	}
	if b > c {
		return b
	}
	return c
}
