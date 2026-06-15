# qphysics-agent

**Physics-based saturation prediction for distributed systems.**

Prometheus, Grafana, and Datadog show what *happened*. qphysics predicts what *will happen* — mathematically, before it occurs.

> "Your payments database will saturate in 47 seconds. Cascade risk to auth-service: 0.73."

---

## What this is not

This is **not** a monitoring dashboard. It does not show CPU charts, memory graphs, or request counts. Those tools already exist.

This is a **prediction engine**. It runs queueing theory, stochastic fluid dynamics, and graph topology mathematics against your live telemetry and produces forward-looking signals that no observability tool currently provides.

---

## Signals produced (none of these exist in Prometheus/Datadog)

| Signal | What it means |
|---|---|
| **Saturation Horizon** | Exact time until a service hits ρ=1 (queue diverges) |
| **Collapse Probability** | P(system-wide cascade) from fixed-point Gauss-Seidel solver |
| **Equilibrium ρ** | Steady-state utilisation under mutual service coupling — not current snapshot |
| **Cascade Amplification Score** | How much risk propagates downstream if this node degrades |
| **Burst Amplification** | M/G/1 factor: how arrival variance multiplies queue length |
| **Hazard Z** | Structural degradation accumulation from stochastic fluid plant |
| **Reservoir R** | Hidden metabolic debt — system stress that survives load removal |
| **Change Point Detected** | CUSUM statistical regime shift (not a threshold breach) |
| **System Fragility** | Topology-structural brittleness — how many single points of amplification exist |
| **Most Dangerous Path** | Service chain with maximum cascade potential |
| **What-If Simulation** | Forward simulation using FinalFluidPlant + Godunov PDE solver |

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     qphysics-agent                          │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐   │
│  │              Discovery Layer                        │   │
│  │  detect.go                                          │   │
│  │  • Probes Prometheus on known ports                 │   │
│  │  • Probes OTel Collector :4317/:4318                │   │
│  │  • Reads /var/run/docker.sock                       │   │
│  │  • Reads KUBERNETES_SERVICE_HOST env                │   │
│  └──────────────────────┬──────────────────────────────┘   │
│                         │                                   │
│  ┌──────────────────────▼──────────────────────────────┐   │
│  │              Ingestion Layer                        │   │
│  │  prom.go                                            │   │
│  │  • PromScraper: /api/v1/query every tick            │   │
│  │    10 PromQL queries: rate, latency histograms,     │   │
│  │    P95/P99, CPU, memory, goroutines, queue depth    │   │
│  │  • OTelReceiver: :4318/v1/traces (OTLP/HTTP)        │   │
│  │    Parses span parent/child → topology edges        │   │
│  └──────────────────────┬──────────────────────────────┘   │
│                         │                                   │
│  ┌──────────────────────▼──────────────────────────────┐   │
│  │              Telemetry Layer                        │   │
│  │  telemetry/store.go + ringbuffer.go                 │   │
│  │  • 64-shard concurrent store (FNV-1a hash)          │   │
│  │  • Per-service RingBuffer (120 samples default)     │   │
│  │  • sanitizePoint: NaN/Inf/bounds guard on ingest    │   │
│  │  • computeWindow: sliding mean, std, confidence     │   │
│  │    score, signal quality classification             │   │
│  └──────────────────────┬──────────────────────────────┘   │
│                         │                                   │
│  ┌──────────────────────▼──────────────────────────────┐   │
│  │              Topology Layer                         │   │
│  │  topology/graph.go                                  │   │
│  │  • Edge decay (factor 0.82 per unseen tick)         │   │
│  │  • Load propagation (8-iteration convergence)       │   │
│  │  • Critical path via Bellman-Ford (max weight)      │   │
│  │  • Node staleness pruning (5 min)                   │   │
│  └──────────────────────┬──────────────────────────────┘   │
│                         │                                   │
│  ┌──────────────────────▼──────────────────────────────┐   │
│  │              Prediction Engine (per tick)           │   │
│  │                                                     │   │
│  │  Stage 1: TelemetryCoupler (coupler.go)             │   │
│  │  ├─ Queue baseline persistence across ticks         │   │
│  │  ├─ Latency feedback: rapid assault / slow decay    │   │
│  │  ├─ Arrival demand memory (0.7/0.3 EWMA)            │   │
│  │  ├─ 3-stage propagation delay buffer                │   │
│  │  └─ Downstream injection from upstream queues       │   │
│  │                                                     │   │
│  │  Stage 2: FinalFluidPlant (final_fluid_plant.go)    │   │
│  │  ├─ Per-service stochastic fluid model              │   │
│  │  ├─ Tamed SDE: arrival drift + fBm noise            │   │
│  │  ├─ Congestion service degradation                  │   │
│  │  ├─ Hazard Z: slow structural degradation           │   │
│  │  ├─ Reservoir R: hidden metabolic debt              │   │
│  │  └─ Outputs → window.Hazard, window.Reservoir       │   │
│  │                                                     │   │
│  │  Stage 3: SignalProcessor (signal.go)               │   │
│  │  ├─ 32-shard concurrent state                       │   │
│  │  ├─ Fast EWMA (α=0.3) + Slow EWMA (α=0.05)         │   │
│  │  ├─ Winsorisation: spike rejection at k×σ          │   │
│  │  └─ CUSUM: change-point detection                   │   │
│  │                                                     │   │
│  │  Stage 4: QueuePhysicsEngine (queueing.go)          │   │
│  │  ├─ 32-shard concurrent state                       │   │
│  │  ├─ M/M/c Erlang-C: exact blocking probability      │   │
│  │  ├─ Physical backlog accumulation (dt-integrated)   │   │
│  │  ├─ Arrival momentum (asymmetric EWMA)              │   │
│  │  ├─ Utilisation trend regression                    │   │
│  │  └─ Saturation horizon extrapolation                │   │
│  │                                                     │   │
│  │  Stage 5: StochasticModel (stochastic.go)           │   │
│  │  ├─ Arrival CoV (coefficient of variation)          │   │
│  │  ├─ M/G/1 burst amplification factor                │   │
│  │  └─ Risk propagation probability                    │   │
│  │                                                     │   │
│  │  Stage 6: NetworkCoupling (network.go)              │   │
│  │  ├─ SOR pressure propagation across graph           │   │
│  │  ├─ Coupled arrival rate per service                │   │
│  │  ├─ M/M/c steady-state P0 and E[Lq]                │   │
│  │  └─ Path saturation horizon                         │   │
│  │                                                     │   │
│  │  Stage 7: FixedPointEquilibrium (network.go)        │   │
│  │  ├─ Gauss-Seidel solver (max 50 iterations)         │   │
│  │  ├─ Per-service equilibrium ρ under coupling        │   │
│  │  ├─ Spectral radius convergence rate                │   │
│  │  └─ Systemic collapse probability                   │   │
│  │                                                     │   │
│  │  Stage 8: TopologySensitivity (topology_sens..go)   │   │
│  │  ├─ Perturbation score per node                     │   │
│  │  ├─ Downstream reach (4-hop BFS)                    │   │
│  │  ├─ Keystone detection                              │   │
│  │  └─ System fragility (weighted mean)                │   │
│  │                                                     │   │
│  │  Stage 9: PerturbationSensitivity (network.go)      │   │
│  │  ├─ 30% capacity reduction per service              │   │
│  │  ├─ Re-run fixed-point after each perturbation      │   │
│  │  └─ ΔCollapseProb per service                       │   │
│  │                                                     │   │
│  │  Stage 10: StabilityAssessment (stability.go)       │   │
│  │  ├─ Collapse zone: safe / warning / collapse        │   │
│  │  ├─ Sigmoid collapse risk from effective ρ          │   │
│  │  ├─ Oscillation risk from EWMA divergence           │   │
│  │  ├─ Feedback gain from outbound topology            │   │
│  │  ├─ Cascade amplification score                     │   │
│  │  ├─ Trend-adjusted margin (20s horizon)             │   │
│  │  └─ Stability derivative (dRisk/dt)                 │   │
│  └──────────────────────┬──────────────────────────────┘   │
│                         │                                   │
│  ┌──────────────────────▼──────────────────────────────┐   │
│  │              What-If Simulator                      │   │
│  │  simulation/whatif.go                               │   │
│  │  • Perturbs window copies (latency/traffic/failure) │   │
│  │  • Builds FinalFluidPlant per service from windows  │   │
│  │  • Runs NetworkField PDE (Godunov scheme, MUSCL)    │   │
│  │  • 200 forward steps (20s sim time at dt=0.1s)      │   │
│  │  • Re-runs FixedPoint on perturbed state            │   │
│  │  └─ Returns: ΔCollapseProb, ΔSatHorizon,           │   │
│  │              PeakQueue, HazardZ, NetworkMass        │   │
│  └──────────────────────┬──────────────────────────────┘   │
│                         │                                   │
│  ┌──────────────────────▼──────────────────────────────┐   │
│  │              API + Dashboard                        │   │
│  │  api/server.go + api/dashboard.go                   │   │
│  │  GET  /            → dashboard HTML                 │   │
│  │  GET  /api/state   → full SystemState JSON          │   │
│  │  POST /api/simulate → WhatIfResult JSON             │   │
│  │  GET  /api/services → []ServiceState JSON           │   │
│  │  GET  /health      → {"status":"ok"}                │   │
│  └─────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

---

## Data Flow (one tick, ~2 seconds)

```
Prometheus /api/v1/query ──► sanitizePoint ──► store.Ingest
OTel /v1/traces          ──► parseOTLP     ──► graph.Update

                    Every 2s tick:
                         │
                    store.AllWindows(60 samples, 30s freshness)
                         │
                    graph.Update(windows)        ← edge decay + load propagation
                         │
                    updateFluidStates(windows)   ← FinalFluidPlant.Step per service
                         │                          writes Z → window.Hazard
                         │                          writes tanh(R) → window.Reservoir
                         │
                    computeState():
                    ├── TelemetryCoupler.ApplyCoupling  ← mutates window rates/latency
                    ├── ComputeNetworkCoupling           ← SOR → pressure, eqRho, P0
                    ├── ComputeFixedPointEquilibrium     ← Gauss-Seidel → collapseProb
                    ├── ComputeTopologySensitivity       ← fragility, keystone
                    ├── ComputePerturbationSensitivity   ← ΔcollapseProbPerService
                    └── per service (8 goroutines parallel):
                        ├── SignalProcessor.Update      ← EWMA, CUSUM
                        ├── RunQueueModel               ← Erlang-C, backlog, horizon
                        ├── RunStochasticModel          ← CoV, burstAmp, riskProp
                        └── RunStabilityAssessment      ← zone, cascadeScore, margin
                                   │
                    SystemState → srv.UpdateState()
                                   │
                    GET /api/state ← dashboard polls every 2s
```

---

## Setup

**Requirements:** Go 1.22+. Zero external dependencies.

```bash
git clone https://github.com/kuldeep/phaseshift
cd phaseshift
go build -o qphysics-agent ./cmd/qphysics-agent/
```

**Run:**
```bash
export QPHYSICS_PROMETHEUS_URL=http://localhost:9090
./qphysics-agent
```

Open `http://localhost:8080`.

**Flags:**
```
--port  8080    Dashboard port
--tick  2s      Engine tick interval
--buf   120     Ring buffer depth per service (samples)
--max-services 500  Max services to track
```

**OTel traces** (optional, for topology auto-discovery):
```bash
# Point your SDK at the agent's built-in receiver:
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
```

---

## What the dashboard shows

**Section 1 — System Prediction**
The only section that matters. SAFE / WARNING / COLLAPSE state, saturation horizon, collapse probability, bottleneck service, most dangerous path.

**Section 2 — Service Physics**
Per-service table: utilisation, equilibrium ρ, queue depth, saturation horizon, collapse risk, burst amplification, cascade score, hazard Z.

**Section 3 — What-If Simulator**
Inject latency, multiply traffic, or remove a service entirely. See predicted collapse probability delta, new saturation horizon, peak queue depth, and hazard accumulation across a 20-second forward simulation.