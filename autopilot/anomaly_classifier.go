package autopilot

// AnomalyType classifies the current failure regime.
type AnomalyType string

const (
	Stable   AnomalyType = "stable"
	Local    AnomalyType = "local"
	Systemic AnomalyType = "systemic"
	Cascade  AnomalyType = "cascade"
)

// AnomalyInput carries the pre-computed signal vector for classification.
type AnomalyInput struct {
	Instability   float64
	Confidence    float64
	BacklogGrowth float64
	LatencyTrend  float64
	RetryPressure float64
	Oscillation   float64
}

// Classify returns the dominant failure regime from the current signal vector.
//

func Classify(in AnomalyInput) AnomalyType {

	// --- normalization ---
	// FIX W3: normPos removed — norm() from utils.go is identical.
	bg := norm(in.BacklogGrowth)
	lt := norm(in.LatencyTrend)
	rp := norm(in.RetryPressure)
	osc := clamp01(in.Oscillation)

	inst := clamp01(in.Instability)
	conf := clamp01(in.Confidence)

	// --- signal activity (presence of deviation) ---
	s1 := activity(bg)
	s2 := activity(lt)
	s3 := activity(rp)
	s4 := activity(osc)

	activitySum := s1 + s2 + s3 + s4

	// --- correlation structure ---
	corrBL := bg * lt
	corrLR := lt * rp
	corrLoop := bg * lt * rp
	corrOsc := osc * (bg + lt + rp) / (1.0 + bg + lt + rp)

	// FIX W1: was softAgg — log-sum-exp produced ln(4) ≈ 1.386 floor at zero load.
	// boundedAgg returns 0 when all inputs are 0.
	correlation := boundedAgg(corrBL, corrLR, corrLoop, corrOsc)

	// --- propagation strength ---
	propagation := (corrBL + corrLR + corrLoop) / (1.0 + corrBL + corrLR + corrLoop)

	// --- amplification (feedback loop) ---
	amplification := (lt * rp) / (1.0 + lt + rp)

	// --- confidence adjustment (low confidence → safer / more conservative classification) ---
	safetyBias := (1.0 - conf) * (0.5 + 0.5*inst)

	// --- decision energy per regime ---
	energyCascade :=
		(inst * (propagation + amplification + correlation)) +
			(0.2 * safetyBias * inst) // decays when inst is 0

	energySystemic :=
		(inst * (correlation + 0.5*propagation)) +
			(0.2 * safetyBias * inst)

	energyLocal :=
		activitySum / (1.0 + activitySum) *
			(1.0 - correlation) *
			(1.0 - propagation)

	// --- smooth dominance selection (all outputs in [0,1)) ---
	eCascade := energyCascade / (1.0 + energyCascade)
	eSystemic := energySystemic / (1.0 + energySystemic)
	eLocal := energyLocal / (1.0 + energyLocal)

	eStable := 1.0 - (eLocal + eSystemic + eCascade)
	if eStable < 0 {
		eStable = 0
	}

	// --- soft winner-take-all decision ---
	maxVal := eStable
	result := Stable

	if eLocal > maxVal {
		maxVal = eLocal
		result = Local
	}
	if eSystemic > maxVal {
		maxVal = eSystemic
		result = Systemic
	}
	if eCascade > maxVal {
		// maxVal update intentionally omitted — eCascade is the last check.
		result = Cascade
	}

	// FIX 3: Hard Signal override
	if bg > 0.0 && lt > 0.0 {
		if rp > 0.4 {
			return Cascade
		}
		if result == Stable {
			return Systemic
		}
	} else if result == Stable && (bg > 0.2 || lt > 0.2) {
		// Real backlog or latency growth → at minimum it's local
		return Local
	} else if result == Stable && rp > 0.35 && (bg > 0.05 || lt > 0.05) {
		// Retry only qualifies when a supporting load signal is present
		return Local
	}

	return result
}

// activity maps a normalized signal x → [0,1) presence score.
// Local to this file; not shared because semantic is classifier-specific.
func activity(x float64) float64 {
	return x / (1.0 + x)
}
