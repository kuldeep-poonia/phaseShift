package autopilot

import "math"

type PlantState struct {
	Backlog float64

	ArrivalMean        float64
	ArrivalP95         float64
	ArrivalRegimeShift float64

	ServiceRate float64

	Disturbance       float64
	DisturbanceEnergy float64

	CapacityActive float64
	CapacityTarget float64
	CapacityTau    float64

	RetryFactor     float64
	LatencyPressure float64

	ScalingCost float64

	ModelConfidence float64
	PredictionError float64
}

type Supervisor struct {
	Dt         float64
	MaxHorizon int

	Alpha float64
	Beta  float64

	EnergyAbsLimit      float64
	SafeBacklogLimit    float64
	TerminalSafeBacklog float64

	DisturbanceBound float64

	CostWeight float64

	AdaptGain float64
}

/*
Quantile-burst arrival envelope
*/
func (s *Supervisor) arrivalEnvelope(x PlantState, step int) float64 {

	regimeBoost :=
		x.ArrivalRegimeShift *
			math.Min(1, float64(step)/5.0)

	return math.Max(
		x.ArrivalMean+regimeBoost,
		x.ArrivalP95,
	)
}

/*
Retry load
*/
func (s *Supervisor) retryLoad(x PlantState) float64 {

	return x.RetryFactor *
		(1 + x.LatencyPressure) *
		math.Sqrt(x.Backlog+1)
}

/*
Capacity evolution
*/
func (s *Supervisor) capacityNext(x PlantState) float64 {

	return x.CapacityActive +
		(x.CapacityTarget-x.CapacityActive)*
			(1-math.Exp(-s.Dt/x.CapacityTau))
}

/*
Service
*/
func (s *Supervisor) service(x PlantState) float64 {
	return x.ServiceRate * x.CapacityActive
}

/*
Lyapunov congestion energy
*/
func (s *Supervisor) energy(x PlantState) float64 {

	util :=
		x.ArrivalMean +
			s.retryLoad(x) -
			s.service(x)

	return x.Backlog*x.Backlog +
		s.Alpha*util*util +
		s.Beta*x.Disturbance*x.Disturbance
}

/*
Piecewise analytic energy bound
*/
func (s *Supervisor) energyBound(x PlantState, h int) float64 {

	e := s.energy(x)

	cap := x.CapacityActive

	for i := 0; i < h; i++ {

		cap = cap +
			(x.CapacityTarget-cap)*
				(1-math.Exp(-s.Dt/x.CapacityTau))

		service := x.ServiceRate * cap

		gamma :=
			s.arrivalEnvelope(x, i) +
				s.retryLoad(x) -
				service +
				s.DisturbanceBound

		// contraction region
		if gamma < 0 {
			e *= 0.9
		} else {
			e += gamma * gamma * s.Dt
		}

		if e > s.EnergyAbsLimit {
			return e
		}
	}

	return e
}

/*
Terminal safe congestion region
*/
func (s *Supervisor) terminalSafe(x PlantState, h int) bool {

	bound := s.energyBound(x, h)

	return bound < s.EnergyAbsLimit &&
		x.Backlog < s.TerminalSafeBacklog
}

/*
Economic-stability score
*/
func (s *Supervisor) score(x PlantState, h int) float64 {

	stability := s.energyBound(x, h)

	economic :=
		s.CostWeight *
			x.ScalingCost *
			math.Pow(float64(h), 1.2)

	return stability + economic
}

/*
Bidirectional adaptation
*/
func (s *Supervisor) adapt(x PlantState) {

	if x.ModelConfidence > 0.7 {
		s.Alpha *= (1 - s.AdaptGain)
		s.Beta *= (1 - s.AdaptGain)
	}

	if x.PredictionError > 0.3 {
		s.Alpha *= (1 + s.AdaptGain)
		s.Beta *= (1 + s.AdaptGain)
	}
}

/*
Optimal trigger horizon
*/
func (s *Supervisor) optimalHorizon(x PlantState) int {

	best := 1
	bestScore := math.Inf(1)

	for h := 1; h <= s.MaxHorizon; h++ {

		if !s.terminalSafe(x, h) {
			break
		}

		sc := s.score(x, h)

		if sc < bestScore {
			bestScore = sc
			best = h
		}
	}

	return best
}

/*
Final trigger decision
*/
func (s *Supervisor) ShouldRecompute(x PlantState) bool {

	s.adapt(x)

	h := s.optimalHorizon(x)

	return h <= 2
}

// Clamp final scaling decision
//
// Smooth damping without thresholds. Oscillation and confidence modulate
// the delta but never crush it to near-zero.
//   - Oscillation: reduced coefficient (0.8, was 2.0) — prevents over-damping
//   - Confidence:  raised floor (0.5, was 0.3) — low confidence slows but doesn't kill
func (s *Supervisor) ClampDecision(delta float64, osc float64, conf float64) float64 {
	d := delta
	d *= 1.0 / (1.0 + 0.8*osc) // min at osc=1: 0.556 (was 0.333)
	d *= (0.5 + 0.5*conf)      // min at conf=0: 0.500 (was 0.300)
	return d
}
