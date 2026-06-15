package autopilot

import "math"

type SafetyState struct {
	Backlog float64
	Latency float64

	CapacityActive float64
	CapacityTarget float64
	ServiceRate    float64

	ArrivalMean float64
	ArrivalVar  float64

	Disturbance      float64
	TopologyPressure float64

	RetryPressure float64
}

type SafetyEngine struct {
	BaseMaxBacklog float64
	BaseMaxLatency float64

	Alpha float64
	Beta  float64

	ArrivalGain     float64
	DisturbanceGain float64
	TopologyGain    float64
	RetryGain       float64

	TailRiskBase float64

	AccelBaseWindow int
	AccelThreshold  float64

	MaxCapacityRamp   float64
	CapacityEffectTau float64
	TopologyDelayTau  float64

	TerminalEnergyBase float64

	ContractionSlack float64

	HysteresisBand float64
	LastUnsafe     bool

	// AdaptiveTightness ∈ [0,1] — raised by RuntimeOrchestrator under stress;
	// tightens the effective backlog limit proportionally.
	AdaptiveTightness float64
}

/*
Lyapunov congestion energy
*/
func (s *SafetyEngine) energy(x SafetyState) float64 {

	util :=
		x.ArrivalMean -
			x.ServiceRate*x.CapacityActive

	return x.Backlog*x.Backlog +
		s.Alpha*util*util +
		s.Beta*x.Disturbance*x.Disturbance
}

/*
Higher-dimensional invariant projection
*/
func (s *SafetyEngine) backlogLimit(x SafetyState) float64 {

	scale :=
		1 +
			s.ArrivalGain*math.Abs(x.ArrivalMean) +
			s.DisturbanceGain*math.Abs(x.Disturbance) +
			s.TopologyGain*math.Abs(x.TopologyPressure) +
			s.RetryGain*math.Abs(x.RetryPressure)

	return (s.BaseMaxBacklog / scale) * (1.0 - 0.5*s.AdaptiveTightness)
}

/*
Heavy-tail risk margin
*/
func (s *SafetyEngine) riskMargin(x SafetyState) float64 {

	return s.TailRiskBase *
		math.Pow(x.ArrivalVar+1, 0.75)
}

/*
Disturbance-tube adaptive energy bound
*/
func (s *SafetyEngine) adaptiveEnergyBound(x SafetyState) float64 {

	tube :=
		1 +
			0.3*math.Abs(x.Disturbance) +
			0.2*math.Abs(x.TopologyPressure)

	return s.energy(x) * tube
}

/*
Adaptive instability horizon
*/
func (s *SafetyEngine) accelWindow(x SafetyState) int {

	loadFactor :=
		math.Abs(x.ArrivalMean) /
			(x.ServiceRate*x.CapacityActive + 1)

	w :=
		int(
			float64(s.AccelBaseWindow) *
				(1 + loadFactor),
		)

	if w < 3 {
		return 3
	}

	return w
}

/*
Normalized growth detection
*/
func (s *SafetyEngine) InstabilityGrowth(
	traj []SafetyState,
) bool {

	w :=
		s.accelWindow(traj[0])

	if len(traj) < w {
		return false
	}

	e0 := s.energy(traj[0])

	var rate float64

	for i := 1; i < w; i++ {

		rate +=
			(s.energy(traj[i]) -
				s.energy(traj[i-1])) /
				(e0 + 1)
	}

	return rate > 0.5
}

/*
Topology-aware actuator feasibility
*/
func (s *SafetyEngine) ActuationFeasible(
	current SafetyState,
	target SafetyState,
) bool {

	required :=
		math.Abs(
			target.CapacityActive -
				current.CapacityActive,
		)

	delay :=
		1 +
			s.CapacityEffectTau +
			s.TopologyDelayTau

	return required <=
		s.MaxCapacityRamp/delay
}

/*
State-dependent terminal safe region
*/
func (s *SafetyEngine) terminalSafe(
	x SafetyState,
) bool {

	limit :=
		s.backlogLimit(x) * 0.7

	return s.energy(x) <
		s.TerminalEnergyBase*
			(1+0.5*math.Abs(x.Disturbance)) &&
		x.Backlog < limit
}

/*
Recursive feasibility
*/
func (s *SafetyEngine) recursivelySafe(
	traj []SafetyState,
) bool {

	for i := 0; i < len(traj)-1; i++ {

		if !s.ActuationFeasible(
			traj[i],
			traj[i+1],
		) {
			return false
		}
	}

	return s.terminalSafe(
		traj[len(traj)-1],
	)
}

/*
Nonlinear reaction capability estimate
*/
func (s *SafetyEngine) reactionPossible(
	x SafetyState,
	horizon int,
) bool {

	util :=
		x.ArrivalMean -
			x.ServiceRate*x.CapacityActive

	recovery :=
		math.Log(
			1 +
				x.ServiceRate*
					float64(horizon),
		)

	return util < recovery
}

/*
Emergency fallback control synthesis

returns safe target capacity
*/
func (s *SafetyEngine) fallbackCapacity(
	x SafetyState,
) float64 {

	required :=
		x.ArrivalMean /
			(x.ServiceRate + 1e-6)

	base := math.Max(x.CapacityActive, x.CapacityTarget)

	return math.Min(
		required*1.3,
		base+s.MaxCapacityRamp,
	)
}

/*
Predictive safety check
*/
func (s *SafetyEngine) PredictiveSafe(
	traj []SafetyState,
) bool {

	e0 := s.energy(traj[0])

	for i := 0; i < len(traj); i++ {

		x := traj[i]

		limit :=
			s.backlogLimit(x) -
				s.riskMargin(x)

		if x.Backlog > limit {
			return false
		}

		if x.Latency > s.BaseMaxLatency {
			return false
		}

		if s.energy(x) >
			s.adaptiveEnergyBound(traj[0]) {
			return false
		}

		if i == len(traj)-1 &&
			s.energy(x) >
				e0*(1+s.ContractionSlack) {
			return false
		}
	}

	return true
}

/*
Emergency decision with hysteresis
*/
func (s *SafetyEngine) EmergencyOverride(
	traj []SafetyState,
) (bool, float64) {

	unsafe :=
		!s.PredictiveSafe(traj) ||
			s.InstabilityGrowth(traj) ||
			!s.recursivelySafe(traj) ||
			!s.reactionPossible(
				traj[0],
				len(traj),
			)

	// hysteresis gating
	if unsafe && !s.LastUnsafe {
		s.LastUnsafe = true
		return true,
			s.fallbackCapacity(traj[0])
	}

	if !unsafe {
		s.LastUnsafe = false
	}

	return false, 0
}

/*
SetAdaptiveTightness — called by RuntimeOrchestrator each tick to
tighten safety margins under sustained stress. tightness ∈ [0,1].
backlog is accepted for future non-linear tightening extensions.
*/
func (s *SafetyEngine) SetAdaptiveTightness(tightness, _ float64) {
	if tightness < 0 {
		tightness = 0
	}
	if tightness > 1 {
		tightness = 1
	}
	s.AdaptiveTightness = tightness
}

/*
ShouldOverrideProb — probabilistic safety gate used by RuntimeOrchestrator.

Builds a worst-case SafetyState trajectory from the MPC plan, evaluating
each horizon step at the heavy-tail arrival upper-bound. Delegates the
final override decision to EmergencyOverride (which carries hysteresis).

Returns (override bool, severity float64) where severity is the fallback
capacity target when override is true, or 0 otherwise.
*/
func (s *SafetyEngine) ShouldOverrideProb(
	x SafetyState,
	plan []MPCControl,
	arrivalUpper float64,
) (bool, float64) {

	if len(plan) == 0 {
		return false, 0
	}

	// Pessimistic: use the heavy-tail upper bound for arrival
	worst := x
	if arrivalUpper > worst.ArrivalMean {
		worst.ArrivalMean = arrivalUpper
	}

	// Build a trajectory by propagating the plan through a simple
	// first-order capacity model, consistent with SafetyState semantics.
	traj := make([]SafetyState, len(plan))
	cur := worst

	for i, u := range plan {
		// first-order capacity lag toward MPC target
		delta := u.CapacityTarget - cur.CapacityActive

		// 🚀 fast path when system stressed
		if cur.Backlog > 80 {
			cur.CapacityActive = u.CapacityTarget
		} else if cur.Backlog > 40 {
			cur.CapacityActive += delta * 0.6
		} else {
			cur.CapacityActive += delta * 0.3
		}

		// retain retry pressure from MPC retry knob
		cur.RetryPressure = u.RetryFactor

		// propagate backlog with worst-case arrival
		netFlow := cur.ArrivalMean -
			cur.ServiceRate*cur.CapacityActive
		if netFlow > 0 {
			cur.Backlog += netFlow
		} else {
			// controlled drain — cannot go negative
			cur.Backlog = math.Max(0, cur.Backlog+netFlow)
		}

		traj[i] = cur
	}

	return s.EmergencyOverride(traj)
}
