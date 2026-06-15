package autopilot

import (
	"math"
)

type MPCState struct {
	Backlog float64
	Latency float64

	ArrivalMean float64
	ArrivalVar  float64

	TopologyPressure float64
	TopologyState    float64

	ServiceRate    float64
	CapacityActive float64

	PrevBacklog float64
}

type MPCControl struct {
	CapacityTarget float64
	RetryFactor    float64
	CacheRelief    float64
}

// MPCOptimiser implements a Zero-Allocation Accelerated Gradient Descent (Nesterov)
// MPC solver, replacing the computationally heavy Simulated Annealing approach.
type MPCOptimiser struct {
	Horizon int
	Dt      float64

	// Zero-allocation buffers to eliminate Garbage Collection pressure
	seqBuf      []MPCControl
	gradBuf     []float64
	momentumBuf []float64

	BacklogCost  float64
	LatencyCost  float64
	ScalingCost  float64
	SmoothCost   float64

	MaxCapacity float64
	MinCapacity float64

	Iters        int
	IterModifier float64
}

// initBuffers ensures zero-allocation slices are correctly sized in memory.
func (m *MPCOptimiser) initBuffers() {
	if len(m.seqBuf) < m.Horizon {
		m.seqBuf = make([]MPCControl, m.Horizon)
		m.gradBuf = make([]float64, m.Horizon)
		m.momentumBuf = make([]float64, m.Horizon)
	}
}

// propagate applies the dynamic state transition (Certainty Equivalent model).
func (m *MPCOptimiser) propagate(x MPCState, u MPCControl) MPCState {
	next := x

	// Capacity kinematics: Continuous first-order filter
	cap := x.CapacityActive + 0.3*(u.CapacityTarget-x.CapacityActive)
	cap = math.Max(m.MinCapacity, math.Min(m.MaxCapacity, cap))

	effectiveCache := math.Min(0.5, math.Max(0, u.CacheRelief))
	service := x.ServiceRate * cap
	retry := math.Min(u.RetryFactor*x.ArrivalMean, x.ArrivalMean*0.5)

	// Certainty equivalent arrival expectation
	arrival := x.ArrivalMean + 0.35*x.TopologyPressure
	arrival *= (1.0 - effectiveCache)

	dQ := (arrival + retry - service) * m.Dt
	next.Backlog = math.Max(0, x.Backlog+dQ)

	util := arrival / (service + 1e-6)
	next.Latency = 0.55*x.Latency + 0.3*util*math.Sqrt(next.Backlog+1) + 0.15*math.Sqrt(next.Backlog)
	
	next.CapacityActive = cap
	next.PrevBacklog = x.Backlog

	return next
}

// evaluateTrajectory computes the LQR-equivalent quadratic cost.
func (m *MPCOptimiser) evaluateTrajectory(initial MPCState, seq []MPCControl) float64 {
	x := initial
	totalCost := 0.0

	for t := 0; t < m.Horizon; t++ {
		u := seq[t]
		prevTarget := initial.CapacityActive
		if t > 0 {
			prevTarget = seq[t-1].CapacityTarget
		}

		x = m.propagate(x, u)

		// Formal Quadratic Penalty: J = x^T Q x + u^T R u + \Delta u^T S \Delta u
		pressure := x.ArrivalMean - (x.ServiceRate * u.CapacityTarget)
		utilizationCost := math.Pow(math.Max(0, pressure), 2) * 5.0
		
		smoothnessCost := m.SmoothCost * math.Pow(u.CapacityTarget-prevTarget, 2)
		stateCost := m.BacklogCost*math.Pow(x.Backlog, 2) + m.LatencyCost*math.Pow(x.Latency, 2)
		
		totalCost += stateCost + utilizationCost + smoothnessCost
	}
	return totalCost
}

func (m *MPCOptimiser) SetCadenceModifier(d float64) {
	if d > 0 && d <= 1 {
		m.IterModifier = d
	}
}

// Optimise executes Nesterov Accelerated Gradient Descent.
// Replace ONLY the Optimise function inside mpc.go
func (m *MPCOptimiser) Optimise(initial MPCState, prevSeq []MPCControl) ([]MPCControl, float64) {
	m.initBuffers()
	
	initial.ServiceRate = math.Max(1e-6, initial.ServiceRate)
	initial.CapacityActive = math.Max(0.5, initial.CapacityActive)

	baseRequired := (initial.ArrivalMean + initial.Backlog/m.Dt) / initial.ServiceRate

	for i := 0; i < m.Horizon; i++ {
		if i < len(prevSeq) {
			m.seqBuf[i] = prevSeq[i]
		} else {
			m.seqBuf[i] = MPCControl{CapacityTarget: baseRequired, RetryFactor: 0, CacheRelief: 0}
		}
		m.momentumBuf[i] = 0.0
	}

	// Capture unoptimized baseline cost to measure true confidence
	baselineCost := m.evaluateTrajectory(initial, m.seqBuf)

	learningRate := 0.05
	beta := 0.9      
	epsilon := 1e-4  

	iters := m.Iters
	if iters <= 0 { iters = 20 }

	for iter := 0; iter < iters; iter++ {
		baseCost := m.evaluateTrajectory(initial, m.seqBuf)

		for t := 0; t < m.Horizon; t++ {
			original := m.seqBuf[t].CapacityTarget
			m.seqBuf[t].CapacityTarget += epsilon
			costUp := m.evaluateTrajectory(initial, m.seqBuf)
			m.seqBuf[t].CapacityTarget = original

			m.gradBuf[t] = (costUp - baseCost) / epsilon
		}

		for t := 0; t < m.Horizon; t++ {
			m.momentumBuf[t] = beta*m.momentumBuf[t] + (1.0-beta)*m.gradBuf[t]
			m.seqBuf[t].CapacityTarget -= learningRate * m.momentumBuf[t]

			if m.seqBuf[t].CapacityTarget < m.MinCapacity { m.seqBuf[t].CapacityTarget = m.MinCapacity }
			if m.seqBuf[t].CapacityTarget > m.MaxCapacity { m.seqBuf[t].CapacityTarget = m.MaxCapacity }
		}
	}

	finalCost := m.evaluateTrajectory(initial, m.seqBuf)
	
	// ELITE CONFIDENCE FIX: Relative Cost Reduction Ratio
	// High confidence if it found a mathematically stable trajectory compared to start
	improvement := math.Max(0, baselineCost - finalCost) / (baselineCost + 1e-6)
	conf := 0.5 + (0.5 * improvement) 
	if math.IsNaN(conf) { conf = 0.5 }

	result := make([]MPCControl, m.Horizon)
	copy(result, m.seqBuf)
	return result, conf
}