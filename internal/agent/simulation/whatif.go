// Package simulation wires FinalFluidPlant and NetworkField into a user-facing
// what-if engine. This is where the stochastic fluid model and PDE solver become
// observable — each what-if request runs a short forward simulation and returns
// predicted outcomes.
package simulation

import (
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/qphysics/phaseshift/modelling"
	"github.com/qphysics/phaseshift/telemetry"
	"github.com/qphysics/phaseshift/topology"
)

// WhatIfRequest specifies a failure scenario to simulate.
type WhatIfRequest struct {
	// TargetService is the service to inject the failure into.
	TargetService string `json:"target_service"`

	// LatencyInjectionMs adds artificial latency to the target service (ms).
	// 0 = no injection. Example: 200 = add 200ms.
	LatencyInjectionMs float64 `json:"latency_injection_ms"`

	// TrafficMultiplier scales the arrival rate. 1.0 = baseline.
	// 2.0 = 2× traffic, 0.5 = half traffic.
	TrafficMultiplier float64 `json:"traffic_multiplier"`

	// NodeFailure simulates complete removal of the target service.
	// When true, its traffic is redistributed to upstream callers.
	NodeFailure bool `json:"node_failure"`

	// SimulationSteps controls resolution. 0 = auto (200 steps).
	SimulationSteps int `json:"simulation_steps"`
}

// WhatIfResult is the predicted outcome of the scenario.
type WhatIfResult struct {
	// Scenario description for the UI.
	ScenarioDescription string `json:"scenario_description"`

	// Per-service predictions after the simulated scenario.
	Services map[string]ServicePrediction `json:"services"`

	// CollapseProb is the system-wide collapse probability post-scenario.
	CollapseProb float64 `json:"collapse_prob"`

	// SaturationHorizonSec: earliest saturation point across all services.
	// -1 = no saturation predicted within the horizon.
	SaturationHorizonSec float64 `json:"saturation_horizon_sec"`

	// DangerousPath: the service chain with the highest cascade risk
	// under this scenario.
	DangerousPath []string `json:"dangerous_path"`

	// PeakQueueDepth: maximum queue depth observed in the simulation.
	PeakQueueDepth float64 `json:"peak_queue_depth"`

	// HazardAccumulated: Z value (structural degradation) at end of simulation.
	HazardAccumulated float64 `json:"hazard_accumulated"`

	// NetworkMass: total traffic density in the PDE network at end of sim.
	NetworkMass float64 `json:"network_mass"`

	// SimulatedAt is when this result was computed.
	SimulatedAt time.Time `json:"simulated_at"`

	// BaselineCollapseProb for comparison.
	BaselineCollapseProb float64 `json:"baseline_collapse_prob"`
}

// ServicePrediction holds per-service outcome data.
type ServicePrediction struct {
	ServiceID            string  `json:"service_id"`
	PredictedRho         float64 `json:"predicted_rho"`
	PredictedQueueDepth  float64 `json:"predicted_queue_depth"`
	PredictedLatencyMs   float64 `json:"predicted_latency_ms"`
	CollapseRisk         float64 `json:"collapse_risk"`
	SaturationHorizonSec float64 `json:"saturation_horizon_sec"`
	HazardAccumulated    float64 `json:"hazard_accumulated"`
	IsBottleneck         bool    `json:"is_bottleneck"`
}

// Engine runs what-if simulations using FinalFluidPlant (per-service stochastic
// fluid model) and NetworkField (PDE congestion wave propagation).
type Engine struct {
	rng *rand.Rand
}

// New creates a simulation Engine.
func New() *Engine {
	return &Engine{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Run executes the what-if scenario and returns predicted outcomes.
// It modifies a copy of the windows — the original data is never mutated.
func (e *Engine) Run(
	req WhatIfRequest,
	windows map[string]*telemetry.ServiceWindow,
	snap topology.GraphSnapshot,
) WhatIfResult {
	if req.TrafficMultiplier <= 0 {
		req.TrafficMultiplier = 1.0
	}
	steps := req.SimulationSteps
	if steps <= 0 {
		steps = 200 // default: 200 × dt=0.1s = 20 seconds of sim time
	}

	result := WhatIfResult{
		SimulatedAt: time.Now(),
		Services:    make(map[string]ServicePrediction, len(windows)),
	}
	result.ScenarioDescription = buildDescription(req)

	// ── Baseline collapse prob (without perturbation) ──────────────────────
	baselineFP := modelling.ComputeFixedPointEquilibrium(windows, snap)
	result.BaselineCollapseProb = baselineFP.SystemicCollapseProb

	// ── Build perturbed windows ────────────────────────────────────────────
	perturbed := perturbWindows(windows, req)

	// ── Run FinalFluidPlant for each service ──────────────────────────────
	// Each service gets its own plant parameterised from its window data.
	// We run all plants forward simultaneously (coupled via NetworkField).
	plants := make(map[string]*modelling.FinalFluidPlant, len(perturbed))
	for id, w := range perturbed {
		plants[id] = windowToPlant(w)
	}

	// ── Build NetworkField from topology ──────────────────────────────────
	// NetworkField models congestion as a fluid PDE across the dependency graph.
	// Each service becomes an EdgeField; topology edges become Junctions.
	nf := modelling.NewNetworkField()
	modelling.PopulateNetworkField(nf, snap)

	// Seed edge initial densities from perturbed window data.
	for id, w := range perturbed {
		if ef, ok := nf.Edges[id]; ok {
			// ρ_initial = min(λ × E[S] / c, 1.0) — Little's Law approximation.
			rho := 0.0
			if w.MeanActiveConns > 0 && w.MeanLatencyMs > 0 {
				rho = math.Min(w.MeanRequestRate*(w.MeanLatencyMs/1000.0)/w.MeanActiveConns, 1.0)
			}
			// Set uniform initial density across all cells.
			for i := range ef.Cells {
				ef.Cells[i].Rho = rho
			}
			// Wire QueueLoadRatio into EdgeField (previously unused field).
			ef.QueueLoadRatio = math.Min(w.LastQueueDepth/math.Max(w.MeanActiveConns, 1), 1.0)
			// Adjust service rate from window latency.
			if w.MeanLatencyMs > 0 {
				ef.ServiceRate = math.Min(1.0/w.MeanLatencyMs*50, 0.5)
			}
		}
	}

	// ── Forward simulation ─────────────────────────────────────────────────
	dt := 0.1 // seconds per step
	peakQueue := 0.0
	maxHazard := 0.0

	for step := 0; step < steps; step++ {
		// Step each fluid plant with fractional Brownian noise.
		for id, plant := range plants {
			dBH := modelling.ComputeDBH(e.rng, dt)
			q, _, z := plant.Step(1.0, dBH)
			if q > peakQueue {
				peakQueue = q
			}
			if z > maxHazard {
				maxHazard = z
			}
			// Feed plant state back into network field density.
			if ef, ok := nf.Edges[id]; ok {
				// Map queue depth to density: clamp to [0,1].
				density := math.Min(q/100.0, 1.0)
				// Update the source cell (index 0) with current queue pressure.
				if len(ef.Cells) > 0 {
					ef.Cells[0].Rho = density
				}
				// Wire hazard into service degradation via NoiseAmp.
				ef.NoiseAmp = math.Min(z*0.001, 0.1)
			}
		}

		// Step the network PDE — propagates congestion waves between services.
		nf.Step()
	}

	// ── Extract per-service predictions from plant final states ───────────
	for id, plant := range plants {
		w := perturbed[id]
		sp := ServicePrediction{
			ServiceID:           id,
			PredictedQueueDepth: plant.Q,
			PredictedLatencyMs:  w.MeanLatencyMs + plant.Q*0.5, // queue penalty
			HazardAccumulated:   plant.Z,
		}
		// Predicted ρ from plant state: A / (Mu × service degradation).
		if plant.Mu > 0 {
			congestion := 1.0 / (1.0 + 0.01*math.Pow(plant.Q, 1.5))
			hazardF := 1.0 / (1.0 + 0.1*plant.Z)
			effectiveMu := plant.Mu * congestion * hazardF
			sp.PredictedRho = plant.A / math.Max(effectiveMu, 1e-3)
		}
		sp.CollapseRisk = sigmoid((sp.PredictedRho - 0.90) / 0.04)

		// Saturation horizon from trend.
		if sp.PredictedRho < 1.0 && sp.PredictedRho > 0 {
			trend := (sp.PredictedRho - (w.MeanRequestRate / 700.0)) / (float64(steps) * dt)
			if trend > 1e-6 {
				sp.SaturationHorizonSec = (1.0 - sp.PredictedRho) / trend
			} else {
				sp.SaturationHorizonSec = -1
			}
		} else if sp.PredictedRho >= 1.0 {
			sp.SaturationHorizonSec = 0
		}

		result.Services[id] = sp
	}

	// ── System-level predictions from fixed-point on perturbed windows ────
	perturbedFP := modelling.ComputeFixedPointEquilibrium(perturbed, snap)
	result.CollapseProb = perturbedFP.SystemicCollapseProb

	// ── Network mass from PDE solver ──────────────────────────────────────
	result.NetworkMass = nf.TotalMass()

	// ── Dangerous path from topology sensitivity ──────────────────────────
	ts := modelling.ComputeTopologySensitivity(snap)
	result.DangerousPath = ts.MaxAmplificationPath
	if len(result.DangerousPath) == 0 {
		// Fallback: use critical path from topology graph.
		result.DangerousPath = snap.CriticalPath.Nodes
	}

	// ── Saturation horizon (earliest across all services) ─────────────────
	result.SaturationHorizonSec = -1
	for _, sp := range result.Services {
		if sp.SaturationHorizonSec == 0 {
			result.SaturationHorizonSec = 0
			break
		}
		if sp.SaturationHorizonSec > 0 {
			if result.SaturationHorizonSec < 0 || sp.SaturationHorizonSec < result.SaturationHorizonSec {
				result.SaturationHorizonSec = sp.SaturationHorizonSec
			}
		}
	}

	// ── Bottleneck: highest predicted ρ ───────────────────────────────────
	maxRho := 0.0
	for id, sp := range result.Services {
		if sp.PredictedRho > maxRho {
			maxRho = sp.PredictedRho
			updated := result.Services[id]
			updated.IsBottleneck = true
			result.Services[id] = updated
		}
	}

	result.PeakQueueDepth = peakQueue
	result.HazardAccumulated = maxHazard

	return result
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// perturbWindows creates a modified copy of windows with the scenario applied.
func perturbWindows(
	windows map[string]*telemetry.ServiceWindow,
	req WhatIfRequest,
) map[string]*telemetry.ServiceWindow {
	out := make(map[string]*telemetry.ServiceWindow, len(windows))
	for id, w := range windows {
		cp := *w
		out[id] = &cp
	}
	target, ok := out[req.TargetService]
	if !ok {
		// Target not found — apply traffic multiplier globally.
		for _, w := range out {
			w.MeanRequestRate *= req.TrafficMultiplier
			w.LastRequestRate *= req.TrafficMultiplier
		}
		return out
	}

	// Latency injection.
	if req.LatencyInjectionMs > 0 {
		target.MeanLatencyMs += req.LatencyInjectionMs
		target.LastLatencyMs += req.LatencyInjectionMs
		target.LastP99LatencyMs = target.MeanLatencyMs * 3.0
	}

	// Traffic multiplier on the target service.
	if req.TrafficMultiplier != 1.0 {
		target.MeanRequestRate *= req.TrafficMultiplier
		target.LastRequestRate *= req.TrafficMultiplier
		target.StdRequestRate *= req.TrafficMultiplier
	}

	// Node failure: zero the target's capacity (simulate removal).
	if req.NodeFailure {
		target.MeanActiveConns = 0
		target.MeanRequestRate = 0
		target.LastRequestRate = 0
	}

	return out
}

// windowToPlant converts a ServiceWindow into a FinalFluidPlant
// with parameters derived from the window's observed statistics.
// This is the wiring that makes FinalFluidPlant observable.
func windowToPlant(w *telemetry.ServiceWindow) *modelling.FinalFluidPlant {
	// Mu: service capacity in req/s. Use latency-based estimate.
	mu := 100.0
	if w.MeanLatencyMs > 0 {
		mu = 1000.0 / w.MeanLatencyMs * math.Max(w.MeanActiveConns, 1)
	}

	// Rho: traffic intensity.
	rho := 0.5
	if mu > 0 {
		rho = math.Min(w.MeanRequestRate/mu, 1.5)
	}

	// Arrival rate A: current observed rate.
	a := math.Max(w.MeanRequestRate, 0.01)

	// Queue depth Q: current observation.
	q := math.Max(w.LastQueueDepth, 0)

	// Hazard Z and Reservoir R from injected physics state.
	z := math.Max(w.Hazard, 0)
	r := math.Max(w.Reservoir, 0)

	// CoV from signal — used to set noise amplitude Nu.
	cov := 0.0
	if w.MeanRequestRate > 0 {
		cov = w.StdRequestRate / w.MeanRequestRate
	}
	nu := math.Min(cov*0.3, 0.5)
	if nu < 0.05 {
		nu = 0.05
	}

	// Amax: cap arrival at 3× current rate (reasonable burst ceiling).
	amax := math.Max(a*3, mu*rho*3)

	return &modelling.FinalFluidPlant{
		Mu:  mu,
		Rho: rho,

		// Arrival dynamics.
		KappaA: 0.5,
		Nu:     nu,
		ChiA:   0.001,
		Psi0:   0.5,
		Qsat:   math.Max(w.MeanQueueDepth*2, 20),
		Amax:   amax,

		// Congestion service degradation.
		Alpha: 0.01,
		Beta:  1.5,
		Eta:   0.1,

		// Stochastic volatility.
		Theta: 0.5,
		Zeta:  0.3,
		Pexp:  1.5,

		// Slow hazard physics.
		Eps:   0.05,
		Gamma: 1.2,

		// Reservoir coupling.
		Omega:   0.2,
		LambdaR: 0.1,

		// Soft boundary.
		KL:    50,
		Delta: 10,

		Dt: 0.1,

		// Initial states from window.
		A: a,
		Q: q,
		Z: z,
		R: r,
	}
}

func buildDescription(req WhatIfRequest) string {
	parts := []string{}
	if req.NodeFailure {
		parts = append(parts, req.TargetService+" node failure")
	} else {
		if req.LatencyInjectionMs > 0 {
			parts = append(parts, fmt.Sprintf("+%.0fms latency on %s", req.LatencyInjectionMs, req.TargetService))
		}
		if req.TrafficMultiplier != 1.0 {
			parts = append(parts, fmt.Sprintf("%.1f× traffic on %s", req.TrafficMultiplier, req.TargetService))
		}
	}
	if len(parts) == 0 {
		return "Baseline (no changes)"
	}
	return joinStrings(parts, " + ")
}

func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}

// joinStrings joins a slice with a separator.
func joinStrings(parts []string, sep string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += sep
		}
		result += p
	}
	return result
}