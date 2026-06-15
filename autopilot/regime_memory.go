package autopilot

type MemoryEntry struct {
	Instability float64
	Confidence  float64
	Anomaly     AnomalyType

	Backlog float64
	Workers float64

	Action     string
	ScaleDelta float64
}

type RegimeMemory struct {
	buffer []MemoryEntry
	size   int
	index  int
	count  int

	// cycle detection
	cycleScore  float64 // EWMA of action-flip frequency
	midRangeCap float64 // cached mid-range capacity for oscillating regimes
}

// -----------------------------
// INIT
// -----------------------------

func NewRegimeMemory(size int) *RegimeMemory {
	return &RegimeMemory{
		buffer: make([]MemoryEntry, size),
		size:   size,
	}
}

// -----------------------------
// ADD
// -----------------------------

func (m *RegimeMemory) Add(e MemoryEntry) {
	// update cycle score before overwriting slot
	if m.count >= 2 {
		prev := m.get(0) // most recent before this
		flip := 0.0
		if prev.Action != e.Action && e.Action != "hold" && prev.Action != "hold" {
			flip = 1.0
		}
		m.cycleScore = 0.85*m.cycleScore + 0.15*flip
	}

	// maintain mid-range capacity hint when oscillating
	if m.cycleScore > 0.45 && e.Workers > 0 {
		if m.midRangeCap == 0 {
			m.midRangeCap = e.Workers
		} else {
			m.midRangeCap = 0.90*m.midRangeCap + 0.10*e.Workers
		}
	}

	m.buffer[m.index] = e
	m.index = (m.index + 1) % m.size

	if m.count < m.size {
		m.count++
	}
}

// -----------------------------
// INTERNAL
// -----------------------------

func (m *RegimeMemory) get(i int) MemoryEntry {
	idx := (m.index - 1 - i + m.size) % m.size
	return m.buffer[idx]
}

func decay(i, n int) float64 {
	x := float64(i) / float64(n)
	return 1.0 / (1.0 + 2.5*x)
}

// -----------------------------
// TREND (NORMALIZED SLOPE)
// -----------------------------

type Trend struct {
	Instability float64
	Backlog     float64
	Confidence  float64
}

func (m *RegimeMemory) GetTrend() Trend {
	if m.count < 4 {
		return Trend{}
	}

	n := m.count

	var tInst, tBack, tConf float64
	var wSum float64

	for i := 0; i < n-1; i++ {
		curr := m.get(i)
		prev := m.get(i + 1)

		w := decay(i, n)

		// normalized differences (FIX)
		dInst := curr.Instability - prev.Instability

		dBack := norm(curr.Backlog) - norm(prev.Backlog)

		dConf := curr.Confidence - prev.Confidence

		tInst += w * dInst
		tBack += w * dBack
		tConf += w * dConf

		wSum += w
	}

	if wSum == 0 {
		return Trend{}
	}

	return Trend{
		Instability: tInst / wSum,
		Backlog:     tBack / wSum,
		Confidence:  tConf / wSum,
	}
}

// -----------------------------
// EFFECTIVENESS (DELAY-AWARE)
// -----------------------------

func (m *RegimeMemory) GetEffectiveness() float64 {
	if m.count < 6 {
		return 0.5 // neutral on cold start — not "broken", just unknown
	}

	n := m.count
	var score float64
	var wSum float64

	// check response over future horizon (FIX)
	maxDelay := 4

	for i := maxDelay; i < n-1; i++ {

		actionEntry := m.get(i)
		if actionEntry.Action == "hold" {
			continue
		}

		// expected direction
		var expected float64
		switch actionEntry.Action {
		case "scale_up":
			expected = -1
		case "scale_down":
			expected = 1
		default:
			continue
		}

		// aggregate response over delay window
		var respInst float64
		var respBack float64

		for d := 1; d <= maxDelay; d++ {
			curr := m.get(i - d)
			prev := m.get(i - d + 1)

			respInst += curr.Instability - prev.Instability

			respBack +=
				norm(curr.Backlog) - norm(prev.Backlog)
		}

		respInst /= float64(maxDelay)
		respBack /= float64(maxDelay)

		instEffect := -expected * respInst
		backEffect := -expected * respBack

		effect := 0.7*instEffect + 0.3*backEffect

		effect = clamp01((effect + 1) / 2)

		w := decay(i, n)

		score += w * effect
		wSum += w
	}

	if wSum == 0 {
		return 1.0
	}

	return clamp01(score / wSum)
}

// -----------------------------
// OSCILLATION (MULTI-SIGNAL)
// -----------------------------

func (m *RegimeMemory) GetOscillationScore() float64 {
	if m.count < 6 {
		return 0
	}

	n := m.count

	var zeroCross float64
	var variance float64
	var actionFlip float64
	var wSum float64

	var mean float64

	// mean instability
	for i := 0; i < n; i++ {
		mean += m.get(i).Instability
	}
	mean /= float64(n)

	for i := 2; i < n; i++ {
		a := m.get(i)
		b := m.get(i - 1)
		c := m.get(i - 2)

		w := decay(i, n)

		// zero-crossing (trend flip)
		d1 := b.Instability - a.Instability
		d2 := c.Instability - b.Instability

		if d1*d2 < 0 {
			zeroCross += w
		}

		// variance component
		dev := b.Instability - mean
		variance += w * dev * dev

		// action flipping
		if b.Action != a.Action {
			actionFlip += w
		}

		wSum += w
	}

	if wSum == 0 {
		return 0
	}

	// normalized components
	z := zeroCross / wSum
	v := variance / (variance + 1)
	a := actionFlip / wSum

	score :=
		0.4*z +
			0.4*v +
			0.2*a

	return clamp01(score)
}

// -----------------------------
// STABILITY (ADAPTIVE WEIGHT)
// -----------------------------

func (m *RegimeMemory) GetStabilityScore() float64 {
	if m.count < 4 {
		return 0.5
	}

	tr := m.GetTrend()
	osc := m.GetOscillationScore()
	eff := m.GetEffectiveness()

	// normalize trend
	inst := clamp01((tr.Instability + 1) / 2)
	conf := clamp01((tr.Confidence + 1) / 2)

	// adaptive weighting (FIX)
	// if oscillation high → trust trend less
	trustTrend := 1.0 - osc

	stability :=
		trustTrend*(1-inst)*0.4 +
			conf*0.3 +
			eff*0.3

	return clamp01(stability)
}

// ── Add exported accessors for the new fields ─────────────────────────────
// ADD these two methods after the existing GetStabilityScore():

// GetCycleScore returns the oscillation cycle frequency EWMA [0,1].
// Values > 0.45 indicate a persistent alternating load pattern.
func (m *RegimeMemory) GetCycleScore() float64 {
	return m.cycleScore
}

// GetMidRangeCap returns the capacity hint for oscillating regimes.
// Returns 0 when no oscillation has been detected.
func (m *RegimeMemory) GetMidRangeCap() float64 {
	if m.cycleScore < 0.45 {
		return 0
	}
	return m.midRangeCap
}
