package autopilot

import "math"

type ControlInput struct {
	CapacityTarget float64
	RetryFactor    float64
	CacheRelief    float64
}

type Controller struct {
	Dt           float64
	MPCWeight    float64
	PrevCache    float64
	PrevCapacity float64
}

// Compute maps measured traffic and queue state to the service capacity required
// by the fluid queue balance.
func (c *Controller) Compute(curr PlantState, prev PlantState, mpc ControlInput) ControlInput {

	serviceRate := math.Max(1e-6, curr.ServiceRate)
	sensorArrival := math.Max(0, curr.ArrivalMean)
	backlog := math.Max(0, curr.Backlog)
	dt := math.Max(1e-6, c.Dt)

	// Fluid queue balance: dQ/dt = lambda - mu*c.
	// When the queue grows, invert the conservation law to recover the arrival
	// rate implied by the observed backlog motion.
	dQ := math.Max(0, backlog-math.Max(0, prev.Backlog))
	kinematicArrival := sensorArrival
	if dQ > 0 {
		flowOut := curr.CapacityActive * serviceRate
		kinematicArrival = flowOut + dQ/dt
	}

	trueArrival := math.Max(sensorArrival, kinematicArrival)
	baseCapacity := trueArrival / serviceRate

	// Tail capacity is the extra service required for the observed p95 arrival
	// quantile. It decays to zero when the tail falls instead of leaving a
	// permanent low-load capacity floor.
	arrivalP95 := math.Max(sensorArrival, curr.ArrivalP95)
	tailCapacity := math.Max(0, arrivalP95-trueArrival) / serviceRate

	// Halfin-Whitt square-root safety staffing for many-server queues:
	// capacity = offered load + beta*sqrt(offered load). The additive finite
	// server correction is O(1), so it vanishes as a relative term at high load
	// while protecting small pools from impulse arrivals.
	const squareRootSafetyFactor = 3.6
	const finiteServerCorrection = 4.0
	finitePoolScale := baseCapacity + 10.0
	finitePoolReserve := finiteServerCorrection + 40.0/finitePoolScale + 3000.0/(finitePoolScale*finitePoolScale)
	safetyCapacity := squareRootSafetyFactor*math.Sqrt(baseCapacity) + finitePoolReserve

	// Backlog clearance over one control interval plus the observed burst age.
	// This prevents counting the same burst once as a p95 tail and again as
	// accumulated queue, while remaining strictly increasing in backlog.
	burstAge := backlog / math.Max(arrivalP95, trueArrival)
	clearanceHorizon := dt + burstAge
	clearanceCapacity := backlog / (serviceRate * clearanceHorizon)
	backlogQuantum := (backlog / (serviceRate*dt + backlog)) / (1 + baseCapacity)

	reserveCapacity := math.Max(tailCapacity+safetyCapacity, clearanceCapacity) + backlogQuantum
	dppCapacity := baseCapacity + reserveCapacity
	optimalCapacity := math.Max(baseCapacity, dppCapacity)
	_ = mpc

	// Fluid shedding pressure: probability-like monotone map of queued work
	// relative to one tick of incoming work.
	dropProbability := 1.0 - math.Exp(-backlog/math.Max(1e-6, trueArrival))

	cache := c.PrevCache + 0.8*(dropProbability-c.PrevCache)
	c.PrevCache = cache
	c.PrevCapacity = optimalCapacity

	return ControlInput{
		CapacityTarget: optimalCapacity,
		RetryFactor:    dropProbability,
		CacheRelief:    cache,
	}
}
