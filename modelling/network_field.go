package modelling

import "math"

/*constant*/

const (
	RhoCritical = 0.5
	CFLMax      = 0.4
	WaveFloor   = 0.05
)

/******** RNG ********/

type RNG struct {
	state uint64
}

func (r *RNG) Next() float64 {
	r.state = 6364136223846793005*r.state + 1
	return float64((r.state>>33)%1000000) / 1000000.0
}

func (r *RNG) Sign() float64 {
	if r.Next() > 0.5 {
		return 1
	}
	return -1
}

/******** STATE ********/

type Cell struct {
	Rho float64
}

type EdgeField struct {
	Cells []Cell
	Dx    float64

	InFlux  float64
	OutFlux float64

	ServiceRate    float64
	NoiseAmp       float64
	SourceGain     float64
	QueueLoadRatio float64
}

type Junction struct {
	In  []string
	Out []string
	R   [][]float64
}

type NetworkField struct {
	Edges     map[string]*EdgeField
	Junctions []*Junction
	CFL       float64
	Time      float64

	RNG RNG
}

func NewNetworkField() *NetworkField {
	return &NetworkField{
		Edges:     make(map[string]*EdgeField),
		Junctions: make([]*Junction, 0),
		CFL:       0.4,
	}
}

/******** PHYSICS ********/

func Flux(r float64) float64      { return r * (1 - r) }
func FluxPrime(r float64) float64 { return 1 - 2*r }

func Demand(r float64) float64 {
	if r <= RhoCritical {
		return Flux(r)
	}
	return Flux(RhoCritical)
}

func Supply(r float64) float64 {
	if r <= RhoCritical {
		return Flux(RhoCritical)
	}
	return Flux(r)
}

/******** HALF RIEMANN ********/

func boundaryDensity(interior float64, F float64) float64 {

	d := 1 - 4*F
	if d < 0 {
		d = 0
	}

	free := 0.5 * (1 - math.Sqrt(d))
	cong := 0.5 * (1 + math.Sqrt(d))

	if FluxPrime(interior) > 0 {
		return free
	}
	return cong
}

/******** GODUNOV ********/

func GodunovFlux(l, r float64) float64 {

	if l <= r {
		if l <= RhoCritical && r >= RhoCritical {
			return Flux(RhoCritical)
		}
		return math.Min(Flux(l), Flux(r))
	}
	return math.Max(Flux(l), Flux(r))
}

/******** LIMITER ********/

func minmod(a, b float64) float64 {
	if a*b <= 0 {
		return 0
	}
	if math.Abs(a) < math.Abs(b) {
		return a
	}
	return b
}

func MUSCL(uL, uC, uR float64) (float64, float64) {
	s := minmod(uC-uL, uR-uC)
	return uC - 0.5*s, uC + 0.5*s
}

/******** GLOBAL DT ********/

func (nf *NetworkField) dt() float64 {

	cfl := nf.CFL
	if cfl > CFLMax {
		cfl = CFLMax
	}

	vmax := WaveFloor
	dx := math.MaxFloat64
	nodeFactor := 1.0

	for _, e := range nf.Edges {

		if e.Dx < dx {
			dx = e.Dx
		}

		for _, c := range e.Cells {
			v := math.Abs(FluxPrime(c.Rho))
			if v > vmax {
				vmax = v
			}
		}
	}

	// routing amplification estimate
	for _, j := range nf.Junctions {
		sum := 0.0
		for i := range j.R {
			for k := range j.R[i] {
				sum += j.R[i][k]
			}
		}
		if sum > nodeFactor {
			nodeFactor = sum
		}
	}

	return cfl * dx / (vmax * nodeFactor)
}

/******** FLUX STEP ********/

func fluxStep(e *EdgeField, dt float64, rng *RNG) {

	n := len(e.Cells)
	flux := make([]float64, n+1)

	leftGhost := boundaryDensity(e.Cells[0].Rho, e.InFlux)
	rightGhost := boundaryDensity(e.Cells[n-1].Rho, e.OutFlux)

	noiseScale := e.NoiseAmp * math.Sqrt(e.Dx)

	for i := 0; i <= n; i++ {

		var UL, UR float64

		if i == 0 {
			UL = leftGhost
			UR = e.Cells[0].Rho
		} else if i == n {
			UL = e.Cells[n-1].Rho
			UR = rightGhost
		} else {

			uL := e.Cells[i-1].Rho
			uC := e.Cells[i].Rho
			uR := e.Cells[int(math.Min(float64(n-1), float64(i+1)))].Rho

			_, UR = MUSCL(uL, uC, uR)

			uLL := e.Cells[int(math.Max(0, float64(i-2)))].Rho
			uLm := e.Cells[i-1].Rho
			uCm := e.Cells[i].Rho

			UL, _ = MUSCL(uLL, uLm, uCm)
		}

		base := GodunovFlux(UL, UR)

		flux[i] = base + noiseScale*rng.Sign()
	}

	for i := 0; i < n; i++ {

		e.Cells[i].Rho -= dt / e.Dx * (flux[i+1] - flux[i])

		if e.Cells[i].Rho < 0 {
			e.Cells[i].Rho = 0
		}
		if e.Cells[i].Rho > 1 {
			e.Cells[i].Rho = 1
		}
	}
}

/******** SOURCE STEP ********/

func sourceStep(e *EdgeField, dt float64) {

	n := len(e.Cells)
	k := int(math.Max(0, float64(n-2)))

	r := e.Cells[k].Rho

	e.Cells[k].Rho -= e.ServiceRate * r * dt

	if e.Cells[k].Rho < 0 {
		e.Cells[k].Rho = 0
	}
}

/******** ACTIVE-SET NODE ********/

func (nf *NetworkField) solveNode(j *Junction) {

	D := make([]float64, len(j.In))
	S := make([]float64, len(j.Out))

	for i, name := range j.In {
		r := nf.Edges[name].Cells[len(nf.Edges[name].Cells)-1].Rho
		D[i] = Demand(r)
	}

	for k, name := range j.Out {
		r := nf.Edges[name].Cells[0].Rho
		S[k] = Supply(r)
	}

	active := make([]bool, len(j.Out))
	for k := range active {
		active[k] = true
	}

	for {

		totalD := 0.0
		for _, d := range D {
			totalD += d
		}
		if totalD == 0 {
			break
		}

		progress := false

		for k := range j.Out {
			if !active[k] {
				continue
			}

			required := 0.0
			for i := range j.In {
				required += j.R[i][k] * D[i]
			}

			if required <= S[k] {
				for i := range j.In {
					f := j.R[i][k] * D[i]
					nf.Edges[j.In[i]].OutFlux += f
					nf.Edges[j.Out[k]].InFlux += f
				}
				active[k] = false
				progress = true
			} else {

				scale := S[k] / required
				for i := range j.In {
					f := j.R[i][k] * D[i] * scale
					nf.Edges[j.In[i]].OutFlux += f
					nf.Edges[j.Out[k]].InFlux += f
				}
				active[k] = false
				progress = true
			}
		}

		if !progress {
			break
		}
	}
}

/******** STRANG STEP ********/

func (nf *NetworkField) Step() {

	dt := nf.dt()

	for _, j := range nf.Junctions {
		nf.solveNode(j)
	}

	for _, e := range nf.Edges {
		sourceStep(e, 0.5*dt)
	}

	for _, e := range nf.Edges {
		fluxStep(e, dt, &nf.RNG)
	}

	for _, e := range nf.Edges {
		sourceStep(e, 0.5*dt)
	}

	nf.Time += dt
}

/******** DIAGNOSTICS ********/

func (nf *NetworkField) TotalMass() float64 {

	M := 0.0
	for _, e := range nf.Edges {
		for _, c := range e.Cells {
			M += c.Rho * e.Dx
		}
	}
	return M
}

func (nf *NetworkField) TotalVariation() float64 {

	tv := 0.0
	for _, e := range nf.Edges {
		for i := 1; i < len(e.Cells); i++ {
			tv += math.Abs(e.Cells[i].Rho - e.Cells[i-1].Rho)
		}
	}
	return tv
}
