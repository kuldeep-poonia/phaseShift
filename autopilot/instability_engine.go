package autopilot

// InstabilityInput carries the raw operating metrics for instability scoring.
type InstabilityInput struct {
	Backlog     float64
	BacklogRate float64
	Latency     float64
	LatencyRate float64
	RetryRate   float64
	Oscillation float64
	Utilization float64
}

// Replaced with boundedAgg which returns 0 when all inputs are 0.
func ComputeInstability(in InstabilityInput) (float64, string) {

	// ---------- normalization (scale-robust, resolution-preserving) ----------
	b := pos(in.Backlog)
	br := pos(in.BacklogRate)

	l := pos(in.Latency)
	lr := pos(in.LatencyRate)
	r := clamp01(in.RetryRate)
	o := clamp01(in.Oscillation)
	u := clamp01(in.Utilization)

	bs := norm(b)
	bm := norm(br)
	ls := norm(l)
	lm := norm(lr)
	rr := norm(r)

	//  pressure field (load)
	pressure := bs * (1.0 + 0.5*ls) / (1.0 + 0.5*bs*ls)

	//  momentum field (acceleration)
	momentum := bm * (1.0 + lm) / (1.0 + bm*lm)

	//  failure field (system weakness)
	utilStress := u / (1.0 + (1.0 - u))
	failure := rr * (1.0 + utilStress) / (1.0 + rr*utilStress)

	loadContext := pressure + momentum
	oscScaled := o * (loadContext / (1.0 + loadContext)) // load-scaled: zero when load=0
	// FIX: Removed oscFloor (was: 0.25 * o).
	// The floor created phantom energy at idle: after any scaling event, OscillationScore
	// stays elevated for many ticks. With oscFloor=0.25*o, energy += 0.5 * 0.125 = 0.0625
	// even at zero backlog, contributing to false instability_high events.
	// Oscillation contribution is only real when load context (pressure+momentum) is non-zero.
	oscEffect := oscScaled

	// ---------- cascade physics ----------
	// Products of normalized [0,1] signals — represent co-occurrence of failure modes.
	cascadeBL := bs * ls
	cascadeLR := ls * rr
	cascadeRU := rr * utilStress
	cascadeFull := bs * ls * rr

	cascade := boundedAgg(cascadeBL, cascadeLR, cascadeRU, cascadeFull)

	// ---------- nonlinear coupling ----------
	pm := pressure * momentum
	pf := pressure * failure
	mf := momentum * failure

	coupling := (pm + pf + mf) / (1.0 + pm + pf + mf)

	// ---------- persistence (temporal simulation) ----------
	persistence := pressure * momentum

	// ---------- energy accumulation ----------
	energy :=
		pressure +
			0.8*momentum +
			0.7*failure +
			0.9*cascade +
			0.6*coupling +
			0.5*persistence +
			0.5*oscEffect

	// ---------- energy shaping (multi-stage response) ----------
	shape := energy / (1.0 + 0.5*energy)
	energy = energy * (1.0 + shape)

	// ---------- final mapping ----------
	score := energy / (1.0 + energy)
	score = clamp01(score)

	// ---------- severity ----------
	var level string
	switch {
	case score < 0.3:
		level = "stable"
	case score < 0.7:
		level = "warning"
	default:
		level = "critical"
	}

	return score, level
}
