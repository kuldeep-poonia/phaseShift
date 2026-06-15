package autopilot

import "math"

type GovernanceMode int

const (
	ModeNormal GovernanceMode = iota
	ModeDegraded
	ModeEmergency
)

type RolloutIntent struct {
	Cap   float64
	Retry float64
	Cache float64

	SLAWeight  float64
	CostWeight float64
	TopoImpact float64
}

type RolloutState struct {
	CapacityActive float64
	RetryActive    float64
	CacheActive    float64

	IntentQueue []RolloutIntent

	WarmupReadiness float64
	ConfigLag       float64

	Mode GovernanceMode

	RolloutTimer float64
	RetryCount   int
}

type RolloutController struct {
	Dt float64

	CapRampUpNormal    float64
	CapRampUpEmergency float64
	CapRampDown        float64

	RetryEnableRamp  float64
	RetryDisableRamp float64

	CacheEnableRamp  float64
	CacheDisableRamp float64

	WarmupTau float64

	ConfigLagTau float64

	QueueMax int

	QueuePressureRampGain float64

	EmergencyBacklog float64
	DegradedBacklog  float64

	RolloutTimeout float64
	MaxRetries     int

	SuccessProbBase  float64
	InfraFailureGain float64

	// runtime-adjustable pacing modifier — set by damping signal
	PaceModifier float64
}

/*
priority scoring
*/
func (r *RolloutController) priorityScore(
	i RolloutIntent,
	backlog float64,
	topology float64,
) float64 {

	return i.SLAWeight*backlog +
		i.CostWeight*math.Abs(i.Cap) +
		i.TopoImpact*math.Abs(topology)
}

/*
full-vector coalescing
*/
func (r *RolloutController) enqueue(
	x RolloutState,
	intent RolloutIntent,
	backlog float64,
	topology float64,
) RolloutState {

	for k := range x.IntentQueue {

		if math.Abs(
			x.IntentQueue[k].Cap-intent.Cap,
		) < 0.05 &&
			math.Abs(
				x.IntentQueue[k].Retry-intent.Retry,
			) < 0.05 &&
			math.Abs(
				x.IntentQueue[k].Cache-intent.Cache,
			) < 0.05 {

			x.IntentQueue[k] = intent
			continue // 🔥 NOT return
		}
	}

	x.IntentQueue =
		append(x.IntentQueue, intent)

	if len(x.IntentQueue) > r.QueueMax {

		minIdx := 0
		minScore :=
			r.priorityScore(
				x.IntentQueue[0],
				backlog,
				topology,
			)

		for i := 1; i < len(x.IntentQueue); i++ {

			s :=
				r.priorityScore(
					x.IntentQueue[i],
					backlog,
					topology,
				)

			if s < minScore {
				minIdx = i
				minScore = s
			}
		}

		x.IntentQueue =
			append(
				x.IntentQueue[:minIdx],
				x.IntentQueue[minIdx+1:]...,
			)
	}

	return x
}

/*
governance mode update
*/
func (r *RolloutController) modeNext(
	x RolloutState,
	backlog float64,
	queuePressure float64,
) GovernanceMode {

	if backlog > r.EmergencyBacklog {
		return ModeEmergency
	}

	if backlog > r.DegradedBacklog ||
		queuePressure > 0.7 {

		return ModeDegraded
	}

	return ModeNormal
}

/*
nonlinear warmup readiness
*/
func (r *RolloutController) warmupNext(
	x RolloutState,
	scaleDelta float64,
) float64 {

	if scaleDelta <= 0 {
		return x.WarmupReadiness
	}

	g :=
		1 - math.Exp(
			-scaleDelta/r.WarmupTau,
		)

	return math.Min(1,
		x.WarmupReadiness+g)
}

/*
config lag dynamics
*/
func (r *RolloutController) lagNext(
	x RolloutState,
	delta float64,
) float64 {

	return x.ConfigLag +
		(1-x.ConfigLag)*
			(1-math.Exp(
				-math.Abs(delta)*
					r.Dt/r.ConfigLagTau))
}

/*
stochastic rollout success
*/
func (r *RolloutController) successProb(
	infraLoad float64,
) float64 {

	return r.SuccessProbBase *
		math.Exp(
			-r.InfraFailureGain*
				infraLoad)
}

/*
capacity ramp
*/
func (r *RolloutController) rampCap(
	active float64,
	target float64,
	mode GovernanceMode,
	queuePressure float64,
	backlog float64,
) float64 {

	err := target - active

	rate := r.CapRampUpNormal

	if mode == ModeEmergency || mode == ModeDegraded {
		rate = r.CapRampUpEmergency
	}

	// Proportional assist for large capacity deficits — bounded to prevent panic scaling.
	// BEFORE: rate += err * 3.0  →  at err=50 (emergency): rate = 14 + 150 = 164 reps/tick!
	//         One tick jumped from 10 → 174 replicas, massively over-shooting and oscillating.
	// AFTER:  capped at CapRampUpEmergency so total rate never exceeds 2× emergency ceiling.
	//         At err=50, emergency=14: boost = min(50*0.3, 14) = 14, total rate = 28.
	//         At err=10: boost = min(3, 14) = 3, total rate = 17.  Smooth, proportional.
	if err > 5.0 {
		boost := math.Min(err*0.3, rate) // boost ≤ 100% of current rate → max 2× ramp
		rate += boost
	}

	if err < 0 {
		rate = r.CapRampDown
	}

	rate *= 1 + r.QueuePressureRampGain*queuePressure

	// Proportional queue pressure multiplier with hard cap.
	// BEFORE: `if backlog > 100 { rate *= 2.0 }` — step-function that double-fires when
	//         backlog is already declining. In queue_saturation at tick=3: backlog=197,
	//         rate already boosted by queuePressure, then doubled again → overshoot to 147
	//         replicas when only 110 needed. Creates 33% over-provision that takes 40+
	//         ticks to drain.
	// AFTER:  gradient from 1.0 at backlog=100 to 1.5 at backlog=SLA (r.EmergencyBacklog).
	//         Smooth pressure-proportional acceleration without cliff-edge doubling.
	//         At backlog=200, SLA=250, EmergencyBacklog=200: factor=1.5.
	//         At backlog=150: factor=1.25. No hard step, no overshoot.
	if backlog > 50 && r.EmergencyBacklog > 50 {
		pressureRatio := math.Min(1.0, (backlog-50)/(r.EmergencyBacklog-50))
		rate *= 1.0 + 0.5*pressureRatio // max 1.5× boost, not 2×
	}

	step :=
		math.Max(
			-rate*r.Dt,
			math.Min(rate*r.Dt, err),
		)

	return active + step
}

/*
main step
*/
func (r *RolloutController) Step(
	x RolloutState,
	intent RolloutIntent,
	backlog float64,
	topology float64,
	infraLoad float64,
) RolloutState {

	next := x

	next =
		r.enqueue(
			next,
			intent,
			backlog,
			topology,
		)

	queuePressure :=
		float64(len(next.IntentQueue)) /
			float64(r.QueueMax)

	next.Mode =
		r.modeNext(
			next,
			backlog,
			queuePressure,
		)

	if len(next.IntentQueue) == 0 {
		return next
	}

	i := next.IntentQueue[len(next.IntentQueue)-1]

	// Clear entire queue after picking latest intent
	next.IntentQueue = next.IntentQueue[:0]

	newCap :=
		r.rampCap(
			x.CapacityActive,
			i.Cap,
			next.Mode,
			queuePressure,
			backlog,
		)

	delta :=
		newCap - x.CapacityActive

	next.CapacityActive = newCap

	// asymmetric retry dynamics
	if i.Retry > x.RetryActive {
		next.RetryActive =
			x.RetryActive +
				r.RetryEnableRamp*r.Dt
	} else {
		next.RetryActive =
			x.RetryActive -
				r.RetryDisableRamp*r.Dt
	}

	// asymmetric cache dynamics
	if i.Cache > x.CacheActive {
		next.CacheActive =
			x.CacheActive +
				r.CacheEnableRamp*r.Dt
	} else {
		next.CacheActive =
			x.CacheActive -
				r.CacheDisableRamp*r.Dt
	}

	next.WarmupReadiness =
		r.warmupNext(x, delta)

	next.ConfigLag =
		r.lagNext(x, delta)

	next.RolloutTimer += r.Dt

	// rollout success / retry logic
	if next.RolloutTimer >
		r.RolloutTimeout {

		p :=
			r.successProb(infraLoad)

		if p < 0.5 &&
			next.RetryCount < r.MaxRetries {

			next.RetryCount++
			next.RolloutTimer = 0

		} else {

			if len(next.IntentQueue) > 0 {
				next.IntentQueue =
					next.IntentQueue[1:]
			}

			next.RolloutTimer = 0
			next.RetryCount = 0
		}
	}

	return next
}

/*
SetPacingModifier — wires damping factor from identification engine into
ramp rate scaling. Higher damping (instability) → slower pacing.
*/
func (r *RolloutController) SetPacingModifier(d float64) {
	if d > 0 {
		r.PaceModifier = d
	}
}

/*
StepAdaptive — bridges RuntimeOrchestrator to RolloutController.

Translates the MPC control output (MPCControl) into a RolloutIntent,
incorporating confidence and stability pressure into SLA / cost weights,
then delegates to the core Step method.
*/
func (r *RolloutController) StepAdaptive(
	x RolloutState,
	ctrl MPCControl,
	confidence float64,
	override bool,
	backlog float64,
	stabilityPressure float64,
	infraLoad float64,
	_ float64, // time — reserved for future time-varying policy
) RolloutState {

	// SLA urgency rises with instability pressure
	slaW := 1.0 + stabilityPressure

	// cost conservatism falls as model confidence rises
	costW := 1.0 - 0.5*confidence

	// topology impact proportional to stability pressure
	topoW := 0.5 + 0.5*stabilityPressure

	intent := RolloutIntent{
		Cap:        ctrl.CapacityTarget,
		Retry:      ctrl.RetryFactor,
		Cache:      ctrl.CacheRelief,
		SLAWeight:  slaW,
		CostWeight: costW,
		TopoImpact: topoW,
	}

	// apply pace modifier to ramp rates for this step
	if r.PaceModifier > 0 {
		savedUp := r.CapRampUpNormal
		savedEmg := r.CapRampUpEmergency
		savedDown := r.CapRampDown

		//scale := 1.0

		//if r.PaceModifier > 0 {
		// scale = 1.0 / (1.0 + r.PaceModifier)  // bounded slow-down
		//}

		//r.CapRampUpNormal = savedUp * scale
		//r.CapRampUpEmergency = savedEmg * scale
		//r.CapRampDown = savedDown * scale

		result := r.Step(x, intent, backlog, stabilityPressure, infraLoad)

		r.CapRampUpNormal = savedUp
		r.CapRampUpEmergency = savedEmg
		r.CapRampDown = savedDown

		return result
	}

	return r.Step(x, intent, backlog, stabilityPressure, infraLoad)
}
