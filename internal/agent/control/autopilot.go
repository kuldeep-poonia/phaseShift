// Package control bridges live observability windows into the autopilot
// control plane.
package control

import (
	"math"
	"sync"
	"time"

	"github.com/qphysics/phaseshift/autopilot"
	"github.com/qphysics/phaseshift/telemetry"
	"github.com/qphysics/phaseshift/topology"
)

// ApplyFunc publishes a per-service scale directive.
type ApplyFunc func(serviceID string, scale float64)

// Snapshot is the externally visible control-plane state.
type Snapshot struct {
	Enabled      bool                       `json:"enabled"`
	UpdatedAt    time.Time                  `json:"updated_at"`
	ServiceCount int                        `json:"service_count"`
	Services     map[string]ServiceSnapshot `json:"services"`
}

// ServiceSnapshot captures the latest autopilot decision for one service.
type ServiceSnapshot struct {
	ServiceID       string    `json:"service_id"`
	AppliedScale    float64   `json:"applied_scale"`
	CapacityActive  float64   `json:"capacity_active"`
	CapacityTarget  float64   `json:"capacity_target"`
	Backlog         float64   `json:"backlog"`
	PhysicalBacklog float64   `json:"physical_backlog"`
	LatencyMs       float64   `json:"latency_ms"`
	Confidence      float64   `json:"confidence"`
	MPCConfidence   float64   `json:"mpc_confidence"`
	OverrideRate    float64   `json:"override_rate"`
	Mode            string    `json:"mode"`
	DecisionAction  string    `json:"decision_action"`
	DecisionDelta   float64   `json:"decision_delta"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type serviceLoop struct {
	orchestrator *autopilot.RuntimeOrchestrator
	state        autopilot.RuntimeState
}

// Plane owns one autopilot runtime per observed service.
type Plane struct {
	mu     sync.RWMutex
	dt     float64
	loops  map[string]*serviceLoop
	latest Snapshot
}

// NewPlane constructs an autopilot bridge with per-service runtime state.
func NewPlane(interval time.Duration) *Plane {
	dt := interval.Seconds()
	if dt <= 0 {
		dt = 2
	}
	return &Plane{
		dt:    dt,
		loops: make(map[string]*serviceLoop),
		latest: Snapshot{
			Enabled:  true,
			Services: make(map[string]ServiceSnapshot),
		},
	}
}

// Tick feeds live service windows into autopilot and publishes scale directives.
func (p *Plane) Tick(
	windows map[string]*telemetry.ServiceWindow,
	snap topology.GraphSnapshot,
	apply ApplyFunc,
) Snapshot {
	now := time.Now()
	updates := make(map[string]float64, len(windows))
	active := make(map[string]struct{}, len(windows))

	p.mu.Lock()
	if p.latest.Services == nil {
		p.latest.Services = make(map[string]ServiceSnapshot)
	}

	for id, w := range windows {
		if w == nil {
			continue
		}
		active[id] = struct{}{}

		loop := p.loops[id]
		if loop == nil {
			loop = p.newServiceLoop(w)
			p.loops[id] = loop
		}

		loop.state = p.hydrateState(loop.state, w, snap)
		next, tel := loop.orchestrator.Tick(loop.state, measuredArrival(w), infraLoad(w))
		loop.state = next

		scale := capacityScale(w, tel.Capacity)
		updates[id] = scale
		p.latest.Services[id] = serviceSnapshot(id, scale, next, tel, now)
	}

	p.pruneLocked(active)
	p.latest.Enabled = true
	p.latest.UpdatedAt = now
	p.latest.ServiceCount = len(p.latest.Services)
	snapshot := p.cloneLocked()
	p.mu.Unlock()

	if apply != nil {
		for id, scale := range updates {
			apply(id, scale)
		}
	}

	return snapshot
}

// Snapshot returns a copy of the latest control-plane state.
func (p *Plane) Snapshot() Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cloneLocked()
}

// PruneActive removes control state for services that are no longer live.
func (p *Plane) PruneActive(active map[string]struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(active)
	p.latest.ServiceCount = len(p.latest.Services)
	p.latest.UpdatedAt = time.Now()
}

func (p *Plane) newServiceLoop(w *telemetry.ServiceWindow) *serviceLoop {
	state := initialRuntimeState(w, p.dt)
	return &serviceLoop{
		orchestrator: newRuntimeOrchestrator(p.dt),
		state:        state,
	}
}

func (p *Plane) hydrateState(
	s autopilot.RuntimeState,
	w *telemetry.ServiceWindow,
	snap topology.GraphSnapshot,
) autopilot.RuntimeState {
	arrival := measuredArrival(w)
	variance := requestVariance(w)
	active := observedCapacity(w)
	serviceRate := serviceRatePerCapacity(w)
	backlog := observedBacklog(w)
	confidence := windowConfidence(w)
	pressure := topologyPressure(w.ServiceID, w, snap)

	if s.Rollout.CapacityActive <= 0 {
		s.Rollout.CapacityActive = active
	}
	if s.Plant.ServiceRate <= 0 {
		s.Plant.ServiceRate = serviceRate
	} else {
		s.Plant.ServiceRate = 0.85*s.Plant.ServiceRate + 0.15*serviceRate
	}
	if s.Plant.CapacityTarget <= 0 {
		s.Plant.CapacityTarget = active
	}
	if s.Plant.TopologyAmplification <= 0 {
		s.Plant.TopologyAmplification = 1 + pressure
	}
	if s.ID.ArrivalEstimate <= 0 {
		s.ID.ArrivalFast = arrival
		s.ID.ArrivalSlow = arrival
		s.ID.ArrivalEstimate = arrival
		s.ID.ArrivalUpper = arrival * 1.5
	}
	if s.ID.ModelConfidence <= 0 {
		s.ID.ModelConfidence = confidence
	}

	if backlog > 0 {
		s.PhysicalBacklog = backlog
		s.Plant.Backlog = backlog
	} else if s.PhysicalBacklog > 0 {
		s.Plant.Backlog = s.PhysicalBacklog
	}

	s.Plant.ArrivalMean = arrival
	s.Plant.ArrivalVar = variance
	s.Plant.ServiceEfficiency = 1
	s.Plant.ConcurrencyLimit = active
	s.Plant.CapacityActive = s.Rollout.CapacityActive
	s.Plant.CapacityTauUp = math.Max(1, p.dt*2)
	s.Plant.CapacityTauDown = math.Max(1, p.dt*4)
	s.Plant.Latency = finiteNonNegative(w.MeanLatencyMs)
	s.Plant.CPUPressure = clamp01(w.MeanCPU)
	s.Plant.NetworkJitter = latencyJitter(w)
	s.Plant.UpstreamPressure = pressure
	s.Plant.Disturbance = 0.85*s.Plant.Disturbance + 0.15*(finiteNonNegative(w.Hazard)+finiteNonNegative(w.Reservoir))
	s.Plant.DisturbanceEnergy = finiteNonNegative(w.Reservoir)

	return s
}

func initialRuntimeState(w *telemetry.ServiceWindow, dt float64) autopilot.RuntimeState {
	arrival := measuredArrival(w)
	variance := requestVariance(w)
	active := observedCapacity(w)
	backlog := observedBacklog(w)
	confidence := windowConfidence(w)

	return autopilot.RuntimeState{
		Plant: autopilot.CongestionState{
			Backlog:          backlog,
			ArrivalMean:      arrival,
			ArrivalVar:       variance,
			ServiceRate:      serviceRatePerCapacity(w),
			ServiceEfficiency: 1,
			ConcurrencyLimit: active,
			CapacityActive:   active,
			CapacityTarget:   active,
			CapacityTauUp:    math.Max(1, dt*2),
			CapacityTauDown:  math.Max(1, dt*4),
			Latency:          finiteNonNegative(w.MeanLatencyMs),
			CPUPressure:      clamp01(w.MeanCPU),
			NetworkJitter:    latencyJitter(w),
			Disturbance:      finiteNonNegative(w.Hazard),
			DisturbanceEnergy: finiteNonNegative(w.Reservoir),
			TopologyAmplification: 1,
		},
		Rollout: autopilot.RolloutState{
			CapacityActive: active,
			WarmupReadiness: 1,
			Mode: autopilot.ModeNormal,
		},
		ID: autopilot.IdentificationState{
			ArrivalFast:      arrival,
			ArrivalSlow:      arrival,
			ArrivalEstimate:  arrival,
			ArrivalVar:       variance,
			ModelConfidence:  confidence,
			ArrivalUpper:     arrival * 1.5,
			ReliabilityUp:    0.8,
			ReliabilityDown:  0.8,
		},
		PhysicalBacklog: backlog,
		Mode:            autopilot.ModeStable,
	}
}

func newRuntimeOrchestrator(dt float64) *autopilot.RuntimeOrchestrator {
	return &autopilot.RuntimeOrchestrator{
		Dt: dt,
		Predictor: &autopilot.Predictor{
			Dt:                       dt,
			MaxQueue:                 10000,
			BurstEntryRate:           0.04,
			BurstCollapseThreshold:   5,
			BurstIntensity:           0.15,
			ArrivalRiseGain:          0.35,
			ArrivalDropGain:          0.08,
			VarianceDecayRate:        0.20,
			RetryGain:                0.08,
			RetryDelayTau:            math.Max(1, 2*dt),
			DisturbanceSigma:         0.01,
			DisturbanceInjectionGain: 0.02,
			DisturbanceBound:         5,
			TopologyCouplingK:        0.20,
			TopologyAdaptTau:         math.Max(1, 4*dt),
			CacheAdaptTau:            math.Max(1, 3*dt),
			LatencyGain:              1,
			BarrierExpK:              0.001,
			BarrierCap:               1_000_000,
		},
		MPC: &autopilot.MPCOptimiser{
			Horizon:      6,
			Dt:           dt,
			BacklogCost:  8,
			LatencyCost:  2,
			ScalingCost:  1,
			SmoothCost:   6,
			MinCapacity:  1,
			MaxCapacity:  1000,
			Iters:        18,
			IterModifier: 1,
		},
		Safety: &autopilot.SafetyEngine{
			BaseMaxBacklog:    10000,
			BaseMaxLatency:    2000,
			Alpha:             0.4,
			Beta:              0.2,
			ArrivalGain:       0.0005,
			DisturbanceGain:   0.1,
			TopologyGain:      0.4,
			RetryGain:         0.2,
			TailRiskBase:      5,
			AccelBaseWindow:   3,
			AccelThreshold:    0.5,
			MaxCapacityRamp:   32,
			CapacityEffectTau: math.Max(1, dt),
			TopologyDelayTau:  math.Max(1, 2*dt),
			TerminalEnergyBase: 1_000_000_000,
			ContractionSlack:   1.0,
			HysteresisBand:     0.05,
		},
		Rollout: &autopilot.RolloutController{
			Dt:                    dt,
			CapRampUpNormal:       4,
			CapRampUpEmergency:    16,
			CapRampDown:           2,
			RetryEnableRamp:       0.05,
			RetryDisableRamp:      0.10,
			CacheEnableRamp:       0.05,
			CacheDisableRamp:      0.10,
			WarmupTau:             math.Max(1, 2*dt),
			ConfigLagTau:          math.Max(1, 2*dt),
			QueueMax:              8,
			QueuePressureRampGain: 0.4,
			EmergencyBacklog:      250,
			DegradedBacklog:       100,
			RolloutTimeout:        math.Max(10, 5*dt),
			MaxRetries:            3,
			SuccessProbBase:       0.99,
			InfraFailureGain:      0.25,
			PaceModifier:          1,
		},
		ID: &autopilot.IdentificationEngine{
			Dt:                  dt,
			FastGain:            0.35,
			SlowGain:            0.08,
			BlendGain:           0.20,
			VarGain:             0.20,
			BurstGain:           0.20,
			BurstDecay:          0.15,
			BurstCap:            10,
			NoiseGain:           0.15,
			DriftGain:           0.05,
			BaseConfidenceFloor: 0.10,
			ConfidenceGain:      0.20,
			ReliabilityGain:     0.10,
			InfraSensitivity:    0.50,
			SLAWeightQueue:      0.001,
			SLAWeightLatency:    0.001,
			EVTFactor:           2,
			SeasonalGain:        0.05,
			DampingGain:         0.10,
		},
		SLA_Backlog:       250,
		OverrideWindow:    20,
		DampingMin:        0.5,
		DampingMax:        1.0,
		FailureScaleProb:  0,
		FailureConfigProb: 0,
		TelemetryTau:      math.Max(1, 3*dt),
	}
}

func serviceSnapshot(
	id string,
	scale float64,
	state autopilot.RuntimeState,
	tel autopilot.RuntimeTelemetry,
	now time.Time,
) ServiceSnapshot {
	target := state.Plant.CapacityTarget
	if len(state.LastPlan) > 0 {
		target = state.LastPlan[0].CapacityTarget
	}

	return ServiceSnapshot{
		ServiceID:       id,
		AppliedScale:    scale,
		CapacityActive:  tel.Capacity,
		CapacityTarget:  target,
		Backlog:         tel.Backlog,
		PhysicalBacklog: tel.PhysicalBacklog,
		LatencyMs:       tel.Latency,
		Confidence:      tel.Confidence,
		MPCConfidence:   tel.MPCConfidence,
		OverrideRate:    tel.OverrideRate,
		Mode:            modeName(state.Mode),
		DecisionAction:  tel.DecisionAction,
		DecisionDelta:   tel.DecisionDelta,
		UpdatedAt:       now,
	}
}

func (p *Plane) pruneLocked(active map[string]struct{}) {
	for id := range p.loops {
		if _, ok := active[id]; !ok {
			delete(p.loops, id)
		}
	}
	for id := range p.latest.Services {
		if _, ok := active[id]; !ok {
			delete(p.latest.Services, id)
		}
	}
}

func (p *Plane) cloneLocked() Snapshot {
	out := Snapshot{
		Enabled:      p.latest.Enabled,
		UpdatedAt:    p.latest.UpdatedAt,
		ServiceCount: p.latest.ServiceCount,
		Services:     make(map[string]ServiceSnapshot, len(p.latest.Services)),
	}
	for id, svc := range p.latest.Services {
		out.Services[id] = svc
	}
	return out
}

func measuredArrival(w *telemetry.ServiceWindow) float64 {
	if v := finiteNonNegative(w.LastRequestRate); v > 0 {
		return v
	}
	return finiteNonNegative(w.MeanRequestRate)
}

func requestVariance(w *telemetry.ServiceWindow) float64 {
	std := finiteNonNegative(w.StdRequestRate)
	if std > 0 {
		return std * std
	}
	mean := measuredArrival(w)
	if mean <= 0 {
		return 0
	}
	return mean * 0.05
}

func observedCapacity(w *telemetry.ServiceWindow) float64 {
	return math.Max(1, finiteNonNegative(w.MeanActiveConns))
}

func observedBacklog(w *telemetry.ServiceWindow) float64 {
	return math.Max(finiteNonNegative(w.LastQueueDepth), finiteNonNegative(w.MeanQueueDepth))
}

func serviceRatePerCapacity(w *telemetry.ServiceWindow) float64 {
	latency := finiteNonNegative(w.MeanLatencyMs)
	if latency <= 0 {
		latency = 50
	}
	hazardFactor := math.Exp(-finiteNonNegative(w.Hazard) * 0.1)
	return math.Max(1e-3, (1000.0/latency)*hazardFactor)
}

func windowConfidence(w *telemetry.ServiceWindow) float64 {
	if w.ConfidenceScore <= 0 {
		return 0.5
	}
	return clamp(w.ConfidenceScore, 0.1, 1.0)
}

func infraLoad(w *telemetry.ServiceWindow) float64 {
	queuePressure := 0.0
	if w.MeanActiveConns > 0 {
		queuePressure = finiteNonNegative(w.LastQueueDepth) / w.MeanActiveConns
	}
	return clamp01(0.45*finiteNonNegative(w.MeanCPU) + 0.35*finiteNonNegative(w.MeanMem) + 0.20*clamp01(queuePressure))
}

func topologyPressure(id string, w *telemetry.ServiceWindow, snap topology.GraphSnapshot) float64 {
	nodeLoad := make(map[string]float64, len(snap.Nodes))
	for _, n := range snap.Nodes {
		nodeLoad[n.ServiceID] = clamp01(n.NormalisedLoad)
	}

	inbound := 0.0
	for _, e := range snap.Edges {
		if e.Target != id {
			continue
		}
		srcLoad := nodeLoad[e.Source]
		latencyPressure := clamp01(finiteNonNegative(e.LatencyMs) / 1000.0)
		errorPressure := clamp01(e.ErrorRate)
		inbound += clamp01(e.Weight) * (0.70*srcLoad + 0.20*latencyPressure + 0.10*errorPressure)
	}

	queuePressure := 0.0
	if w.MeanActiveConns > 0 {
		queuePressure = finiteNonNegative(w.LastQueueDepth) / w.MeanActiveConns
	}
	cov := 0.0
	if w.MeanRequestRate > 0 {
		cov = finiteNonNegative(w.StdRequestRate) / w.MeanRequestRate
	}
	local := 0.60*clamp01(queuePressure) + 0.40*clamp01(cov/2.0)

	return clamp01(local + inbound)
}

func latencyJitter(w *telemetry.ServiceWindow) float64 {
	mean := finiteNonNegative(w.MeanLatencyMs)
	maxLatency := finiteNonNegative(w.MaxLatencyMs)
	if mean <= 0 || maxLatency <= mean {
		return 0
	}
	return clamp01((maxLatency - mean) / mean)
}

func capacityScale(w *telemetry.ServiceWindow, capacity float64) float64 {
	if capacity <= 0 || math.IsNaN(capacity) || math.IsInf(capacity, 0) {
		return 1
	}
	scale := capacity / observedCapacity(w)
	return clamp(scale, 0.25, 10)
}

func modeName(mode autopilot.AutonomyMode) string {
	switch mode {
	case autopilot.ModeGuarded:
		return "guarded"
	case autopilot.ModeCritical:
		return "critical"
	case autopilot.ModeRecovery:
		return "recovery"
	default:
		return "stable"
	}
}

func finiteNonNegative(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

func clamp01(v float64) float64 {
	return clamp(v, 0, 1)
}

func clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
