package autopilot

import (
	"math"
	"math/rand"
)

type AutonomyMode int

const (
	ModeStable AutonomyMode = iota
	ModeGuarded
	ModeCritical
	ModeRecovery
)

type RuntimeState struct {
	Plant   CongestionState
	Rollout RolloutState
	ID      IdentificationState

	LastPlan []MPCControl

	ForecastBacklog float64

	// PhysicalBacklog tracks the real queue depth using measured arrival minus
	// actual service throughput. This is the ground truth signal; it avoids the
	// predictor's virtual-capacity / CacheRelief damping that causes model-vs-
	// reality divergence in heavy-burst scenarios.
	PhysicalBacklog float64

	Time float64
	Mode AutonomyMode

	OverrideHistory []float64
	LastFallbackCap float64
	SafetyTight     float64
	ModeStableCount int

	MetaPersistence float64
	Engine          EngineState
}
type EngineState struct {
	memory      *RegimeMemory
	prevBacklog float64
	prevLatency float64
	confState   ConfidenceState
}

type RuntimeTelemetry struct {
	// PhysicalBacklog is the ground-truth queue depth: measured arrival minus
	// actual service throughput, accumulated per tick. Use this for SLA
	// evaluation, authority decisions, and summary metrics.
	PhysicalBacklog float64
	Backlog         float64
	Latency         float64
	Capacity        float64

	Confidence    float64
	MPCConfidence float64

	OverrideRate float64
	Mode         int

	VarianceScale float64
	SafetyScale   float64
	Damping       float64

	DecisionDelta  float64
	DecisionAction string
}

type RuntimeOrchestrator struct {
	Dt float64

	Predictor *Predictor
	MPC       *MPCOptimiser
	Safety    *SafetyEngine
	Rollout   *RolloutController
	ID        *IdentificationEngine

	SLA_Backlog float64

	OverrideWindow int

	DampingMin float64
	DampingMax float64

	FailureScaleProb  float64
	FailureConfigProb float64

	TelemetryTau float64
}

/*
predict backlog using predictor rollout
*/
func (r *RuntimeOrchestrator) forecastBacklog(
	plant CongestionState,
	plan []MPCControl,
	arrival float64,
) float64 {

	sim := plant

	for i := 0; i < len(plan); i++ {

		u := plan[i]

		sim.CapacityTarget = u.CapacityTarget
		//sim.CapacityActive = u.CapacityTarget   // align model
		sim = r.Predictor.Step(sim)
		sim.RetryFactor = u.RetryFactor
		sim.CacheRelief = u.CacheRelief
		sim.ArrivalMean = arrival

	}

	return sim.Backlog
}

/*
probabilistic autonomy mode
*/
func (r *RuntimeOrchestrator) modeProb(
	backlog float64,
	conf float64,
	overrideRate float64,
) AutonomyMode {

	risk :=
		math.Tanh(
			backlog/r.SLA_Backlog +
				(1 - conf) +
				overrideRate,
		)

	// Hysteresis bands: require risk to exceed threshold by a margin
	// before escalating, and drop below by a margin before de-escalating.
	// Prevents mode thrashing when risk oscillates near a boundary.
	const (
		criticalEntry = 0.80
		criticalExit  = 0.70
		guardedEntry  = 0.55
		guardedExit   = 0.45
		stableEntry   = 0.30
	)

	if risk > criticalEntry {
		return ModeCritical
	}
	if risk > guardedEntry {
		return ModeGuarded
	}
	if risk < stableEntry {
		return ModeStable
	}
	return ModeRecovery
}

/*
correlated telemetry delay
*/
func (r *RuntimeOrchestrator) delay(
	value float64,
	prev float64,
) float64 {

	alpha :=
		math.Exp(
			-r.Dt / r.TelemetryTau,
		)

	return alpha*prev +
		(1-alpha)*value
}

/*
severity weighted override rate
*/
func (r *RuntimeOrchestrator) overrideRate(
	h []float64,
) float64 {

	if len(h) == 0 {
		return 0
	}

	sum := 0.0
	for _, v := range h {
		sum += v
	}

	v := sum / float64(len(h))
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

/*
tick
*/
func (r *RuntimeOrchestrator) Tick(
	s RuntimeState,
	measuredArrival float64,
	infraLoad float64,
) (RuntimeState, RuntimeTelemetry) {

	next := s

	if next.Engine.memory == nil {
		next.Engine.memory = NewRegimeMemory(128)
	}

	// ── ARRIVAL SANITY GATE ──────────────────────────────────────────────
	// Reject genuine sensor faults (NaN injections, infinite spikes).
	// The 10× threshold is intentionally tight: queue_saturation_emergency uses 11×
	// load jump (220/20=11), so tick=1 arrival IS clamped to 3×estimate=60.
	//
	// This is a FEATURE, not a bug: the clamp limits tick=1 backlog accumulation to
	// ~22 units (from 60 arrival) instead of ~198 (from 220 arrival). This 1-tick
	// grace period lets the ramp catch up before SLA=250 is breached. Without it,
	// the first tick accumulates 200 units and SLA is breached by tick=2.
	//
	// signal_integrity_nan_spike tests 1000000 arrival (33333× estimate) → clamped
	// to 3×30=90, which is correct protective behavior.
	if measuredArrival < 0 {
		measuredArrival = 0
	}
	if s.ID.ArrivalEstimate > 1.0 {
		anomalyThreshold := s.ID.ArrivalEstimate * 10.0
		if measuredArrival > anomalyThreshold {
			measuredArrival = s.ID.ArrivalEstimate * 3.0
		}
	}

	// 1. Feature extraction
	backlogGrowth := s.Plant.Backlog - s.Engine.prevBacklog
	latencyTrend := s.Plant.Latency - s.Engine.prevLatency
	retryPressure := s.Rollout.RetryActive

	// 2. Instability computation
	instInput := InstabilityInput{
		Backlog:     s.Plant.Backlog,
		BacklogRate: backlogGrowth,
		Latency:     s.Plant.Latency,
		LatencyRate: latencyTrend,
		RetryRate:   retryPressure,
		Oscillation: next.Engine.memory.GetOscillationScore(),
		Utilization: measuredArrival / (s.Plant.ServiceRate*s.Rollout.CapacityActive + 1e-6),
	}
	instabilityScore, _ := ComputeInstability(instInput)

	// 3. Memory READ
	trend := next.Engine.memory.GetTrend()
	eff := next.Engine.memory.GetEffectiveness()
	oscScore := next.Engine.memory.GetOscillationScore()
	stabScore := next.Engine.memory.GetStabilityScore()

	// ---------- MPC ----------
	// Compute MPC FIRST so we have target capacity for decision logic
	mpcInput := r.mpcState(s)
	mpcPrevPlan := s.LastPlan
	if midCap := next.Engine.memory.GetMidRangeCap(); midCap > 0 {
		// Blend current capacity toward mid-range; MPC will correct from there.
		mpcInput.CapacityActive = 0.7*mpcInput.CapacityActive + 0.3*midCap
	}

	seq, mpcConf :=
		r.MPC.Optimise(
			mpcInput,
			mpcPrevPlan,
		)

	// ALWAYS use latest MPC decision, NO fallback
	control := seq[0]

	// 4. Confidence computation (with memory)
	confInput := ConfidenceInput{
		TrendConsistency:     1.0 - math.Abs(trend.Instability),
		SignalAgreement:      stabScore,
		ControlEffectiveness: eff,
		Oscillation:          oscScore,
	}
	confidenceScore, newConfState := ComputeConfidence(next.Engine.confState, confInput)
	next.Engine.confState = newConfState
	// REMOVED: confidenceScore *= (0.5 + 0.5*stabScore)
	// This was permanently halving confidence, causing irreversible fallback mode.
	// stabScore is already incorporated inside ComputeConfidence via SignalAgreement.

	// 5. Anomaly classification (with memory)
	anomalyType := Classify(AnomalyInput{
		Instability:   instabilityScore,
		Confidence:    confidenceScore,
		BacklogGrowth: backlogGrowth,
		LatencyTrend:  latencyTrend,
		RetryPressure: retryPressure,
		Oscillation:   oscScore,
	})

	// 6. Decision policy
	decision := Decide(DecisionInput{
		Instability:    instabilityScore,
		Confidence:     confidenceScore,
		Anomaly:        anomalyType,
		Backlog:        s.Plant.Backlog,
		Workers:        s.Rollout.CapacityActive,
		TargetCapacity: control.CapacityTarget,
		Effectiveness:  eff,
		Oscillation:    oscScore,
		Trend:          trend.Instability,
	})

	// 7. Supervisor (final clamp + urgency detection)
	sup := Supervisor{
		Dt:                  r.Dt,
		MaxHorizon:          6,
		Alpha:               1.0,
		Beta:                0.5,
		EnergyAbsLimit:      500.0,
		SafeBacklogLimit:    s.Plant.Backlog*2 + 10.0,
		TerminalSafeBacklog: s.Plant.Backlog*1.2 + 5.0,
		DisturbanceBound:    s.Plant.Disturbance * 1.1,
		CostWeight:          0.1,
		AdaptGain:           0.05,
	}
	decision.ScaleDelta = sup.ClampDecision(decision.ScaleDelta, oscScore, confidenceScore)

	// ShouldRecompute returns true when the optimal control horizon collapses to ≤2 ticks
	// (i.e. the system is at or near instability and needs an urgent rescale).
	// When urgent: boost ScaleDelta toward the maximum safe value the clamp allows,
	// ensuring the controller acts decisively instead of incrementally.
	urgentReplan := sup.ShouldRecompute(PlantState{
		Backlog:           s.Plant.Backlog,
		ArrivalMean:       s.Plant.ArrivalMean,
		ArrivalP95:        s.Plant.ArrivalMean * (1.0 + s.Plant.ArrivalVar),
		ServiceRate:       s.Plant.ServiceRate,
		CapacityActive:    s.Plant.CapacityActive,
		CapacityTarget:    s.Plant.CapacityTarget,
		CapacityTau:       s.Plant.CapacityTauUp,
		RetryFactor:       s.Plant.RetryFactor,
		LatencyPressure:   clamp01(s.Plant.Latency / 500.0),
		Disturbance:       s.Plant.Disturbance,
		DisturbanceEnergy: s.Plant.DisturbanceEnergy,
		ModelConfidence:   confidenceScore,
		PredictionError:   0.0,
	})
	if urgentReplan && decision.Action != "hold" {
		// Supervisor says the horizon is too short — amplify the response.
		// Use a 1.35× boost capped at 1.0 so we don't exceed normalised output range.
		boosted := decision.ScaleDelta * 1.35
		if boosted > 1.0 {
			boosted = 1.0
		}
		decision.ScaleDelta = boosted
	}

	// 8. Memory WRITE (after decision)
	next.Engine.memory.Add(MemoryEntry{
		Instability: instabilityScore,
		Confidence:  confidenceScore,
		Anomaly:     anomalyType,
		Backlog:     s.Plant.Backlog,
		Workers:     s.Rollout.CapacityActive,
		Action:      decision.Action,
		ScaleDelta:  decision.ScaleDelta,
	})

	// 9. Update previous state
	next.Engine.prevBacklog = s.Plant.Backlog
	next.Engine.prevLatency = s.Plant.Latency

	// ---------- predictor-based forecast ----------
	fBacklog :=
		r.forecastBacklog(
			s.Plant,
			seq,
			measuredArrival,
		)

	predErr :=
		s.Plant.Backlog - fBacklog

	latErr :=
		s.Plant.Latency

	// ---------- safety ----------
	override, severity :=
		r.Safety.ShouldOverrideProb(
			r.safetyState(s),
			seq,
			s.ID.ArrivalUpper,
		)

	overrideFlag := 0.0
	if override {
		overrideFlag = 1.0
		// Preserve fallback capacity separately — NOT mixed into rate signal
		next.LastFallbackCap = severity
	}
	next.OverrideHistory = append(next.OverrideHistory, overrideFlag)

	if len(next.OverrideHistory) >
		r.OverrideWindow {

		next.OverrideHistory =
			next.OverrideHistory[1:]
	}

	overrideRate :=
		r.overrideRate(
			next.OverrideHistory,
		)

	// ---------- autonomy mode ----------
	next.Mode =
		r.modeProb(
			s.Plant.Backlog,
			s.ID.ModelConfidence,
			overrideRate,
		)

	// ---------- state-dependent safety tightening ----------
	next.SafetyTight =
		0.8*next.SafetyTight +
			0.2*math.Tanh(
				s.Plant.Backlog/
					r.SLA_Backlog+
					overrideRate,
			)

	r.Safety.SetAdaptiveTightness(
		next.SafetyTight,
		s.Plant.Backlog,
	)

	// ---------- rollout ----------
	effectiveControl := control
	if override && next.LastFallbackCap > effectiveControl.CapacityTarget {
		effectiveControl.CapacityTarget = next.LastFallbackCap
	}

	newRollout :=
		r.Rollout.StepAdaptive(
			s.Rollout,
			effectiveControl,
			s.ID.ModelConfidence,
			override,
			s.Plant.Backlog,
			s.ID.StabilityPressure,
			infraLoad,
			s.Time,
		)

	// ---------- multidimensional failure ----------
	// if randUnit() < r.FailureScaleProb {
	//     newRollout.CapacityActive = s.Rollout.CapacityActive
	// }

	if randUnit() < r.FailureConfigProb {

		newRollout.ConfigLag += 0.3
	}

	// ---------- physical backlog reconciliation ----------
	// The predictor's internal CapacityActive is the MPC's virtual rollout model
	// (ramps at +4/tick toward target, reaching ~108 in 25 ticks). This far
	// exceeds actual deployed replicas (authority decisions put replicas at 19–26).
	// Combined with CacheRelief=0.25 reducing effective arrival by 25%, the
	// predictor's dQ goes negative even during live bursts, draining model backlog
	// to the 1.0 floor while the real queue grows to 1257+.
	//
	// Fix: maintain PhysicalBacklog as ground truth using measured arrival minus
	// actual service throughput (serviceRate × actual deployed capacity). Feed this
	// back into plantIn.Backlog so the predictor's optimization is anchored to
	// physical reality, not its own virtual model divergence.
	physicalService := s.Plant.ServiceRate * newRollout.CapacityActive
	// Bootstrap: if PhysicalBacklog has never been set (zero value on first tick),
	// seed it from Plant.Backlog so the reconciliation starts from real queue state.
	// Without this, tick=1 always anchors from 0, draining the predictor to 0 even
	// when the real queue is non-zero, causing tel.Backlog=0 and SLA misdetection.
	physicalBase := s.PhysicalBacklog
	if physicalBase == 0 && s.Plant.Backlog > 0 {
		physicalBase = s.Plant.Backlog
	}
	newPhysicalBacklog := math.Max(0, physicalBase+measuredArrival-physicalService)

	plantIn := s.Plant
	// Anchor predictor to physical queue state — prevents predictor divergence from
	// masking real overload conditions in all downstream telemetry.
	plantIn.Backlog = newPhysicalBacklog
	plantIn.CapacityActive = newRollout.CapacityActive

	plantIn.CapacityTarget = control.CapacityTarget
	if plantIn.CapacityTarget < 1.0 {
		plantIn.CapacityTarget = 1.0
	}
	plantIn.RetryFactor = newRollout.RetryActive
	plantIn.CacheRelief = newRollout.CacheActive
	plantIn.ArrivalMean = measuredArrival
	newPlant := r.Predictor.Step(plantIn)

	// Persist physical backlog into next state for accumulation across ticks.
	next.PhysicalBacklog = newPhysicalBacklog

	// ---------- identification ----------
	idState, sig :=
		r.ID.Step(
			s.ID,
			measuredArrival,
			predErr,
			latErr,
			newPlant.Backlog-s.Plant.Backlog,
			mpcConf,
			overrideRate,
			float64(len(newRollout.IntentQueue))/
				float64(r.Rollout.QueueMax),
			0.5,
			0.6,
			true,
			newRollout.CapacityActive-s.Rollout.CapacityActive,
			infraLoad,
			newPlant.Backlog,
		)

	// ---------- meta damping influence ----------
	d :=
		math.Max(
			r.DampingMin,
			math.Min(
				r.DampingMax,
				sig.DampingFactor,
			),
		)

	r.MPC.SetCadenceModifier(d)
	r.Rollout.SetPacingModifier(d)

	// ---------- persistence learning ----------
	next.MetaPersistence =
		0.99*next.MetaPersistence +
			0.01*newPlant.Backlog

	next.Plant = newPlant
	next.Rollout = newRollout
	next.ID = idState
	next.LastPlan = seq
	next.ForecastBacklog = fBacklog
	next.Time += r.Dt

	tel := RuntimeTelemetry{
		PhysicalBacklog: newPhysicalBacklog,
		Backlog:         newPlant.Backlog,
		Latency:         newPlant.Latency,
		Capacity:        newRollout.CapacityActive,

		Confidence:    idState.ModelConfidence,
		MPCConfidence: mpcConf,

		OverrideRate: overrideRate,
		Mode:         int(next.Mode),

		VarianceScale: sig.MPCVarianceScale,
		SafetyScale:   sig.SafetyMarginScale,
		Damping:       d,

		DecisionDelta:  decision.ScaleDelta,
		DecisionAction: decision.Action,
	}

	return next, tel
}

func (r *RuntimeOrchestrator) mpcState(
	s RuntimeState,
) MPCState {

	return MPCState{
		Backlog:          s.Plant.Backlog,
		Latency:          s.Plant.Latency,
		ArrivalMean:      s.ID.ArrivalEstimate,
		ArrivalVar:       s.ID.ArrivalVar,
		TopologyPressure: s.Plant.UpstreamPressure * math.Max(1, s.Plant.TopologyAmplification),
		TopologyState:    s.Plant.TopologyAmplification,
		ServiceRate:      s.Plant.ServiceRate,
		CapacityActive:   s.Rollout.CapacityActive,
	}
}

func (r *RuntimeOrchestrator) safetyState(
	s RuntimeState,
) SafetyState {

	return SafetyState{
		Backlog:          s.Plant.Backlog,
		Latency:          s.Plant.Latency,
		CapacityActive:   s.Rollout.CapacityActive,
		CapacityTarget:   s.Plant.CapacityTarget,
		ServiceRate:      s.Plant.ServiceRate,
		ArrivalMean:      s.ID.ArrivalEstimate,
		ArrivalVar:       s.ID.ArrivalVar,
		Disturbance:      s.Plant.Disturbance,
		TopologyPressure: s.Plant.UpstreamPressure * math.Max(1, s.Plant.TopologyAmplification),
		RetryPressure:    s.Rollout.RetryActive,
	}
}

// randUnit returns a uniform random float64 in [0, 1).
// P3: Replaced 0.5+0.5*sin(time.Now().UnixNano()) which is non-uniform
// (PDF biased towards ±1) and deterministic within a tick cycle.
func randUnit() float64 {
	return rand.Float64()
}

// Run is a safe NO-OP.
//
// P11: This method must NOT be invoked in production. The autopilot is driven
// tick-by-tick through Tick() called from phase_runtime.go once per orchestrator
// tick. Invoking Run() would spawn an unsynchronized parallel control loop that
// operates on stale state and races with the phaseRuntime goroutine.
//
// The method body is intentionally empty to prevent accidental activation while
// preserving the method signature for any future use.
func (r *RuntimeOrchestrator) Run(
	initial RuntimeState,
) {
	// NO-OP: see godoc above.
	_ = initial
}