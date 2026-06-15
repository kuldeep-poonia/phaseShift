// Package api provides the HTTP server, JSON API endpoints, and embedded
// dashboard for qphysics-agent.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/qphysics/phaseshift/internal/agent/control"
	"github.com/qphysics/phaseshift/internal/agent/simulation"
    "github.com/qphysics/phaseshift/modelling"
    "github.com/qphysics/phaseshift/telemetry"
    "github.com/qphysics/phaseshift/topology"
)


// State types


type SystemState struct {
	CollapseZone         string        `json:"collapse_zone"`
	CollapseProb         float64       `json:"collapse_prob"`
	SaturationHorizonSec float64       `json:"saturation_horizon_sec"`
	SaturationHorizonStr string        `json:"saturation_horizon_str"`
	HighestRiskService   string        `json:"highest_risk_service"`
	BottleneckService    string        `json:"bottleneck_service"`
	MostDangerousPath    []string      `json:"most_dangerous_path"`
	SystemFragility      float64       `json:"system_fragility"`
	NetworkSatRisk       float64       `json:"network_sat_risk"`
	IsConverging         bool          `json:"is_converging"`
	ControlPlane         control.Snapshot `json:"control_plane"`
	Services             []ServiceState `json:"services"`
	Edges                []EdgeState   `json:"edges"`
	DiscoveryStatus      string        `json:"discovery_status"`
	HasLiveData          bool          `json:"has_live_data"`
	ComputedAt           time.Time     `json:"computed_at"`
	TickCount            int64         `json:"tick_count"`
	ServiceCount         int           `json:"service_count"`
}

type ServiceState struct {
	ID                   string  `json:"id"`
	CollapseZone         string  `json:"collapse_zone"`
	CollapseRisk         float64 `json:"collapse_risk"`
	Utilisation          float64 `json:"utilisation"`
	EquilibriumRho       float64 `json:"equilibrium_rho"`
	SaturationHorizonSec float64 `json:"saturation_horizon_sec"`
	MeanQueueDepth       float64 `json:"mean_queue_depth"`
	MeanLatencyMs        float64 `json:"mean_latency_ms"`
	MeanRequestRate      float64 `json:"mean_request_rate"`
	BurstAmplification   float64 `json:"burst_amplification"`
	RiskPropagation      float64 `json:"risk_propagation"`
	OscillationRisk      float64 `json:"oscillation_risk"`
	FeedbackGain         float64 `json:"feedback_gain"`
	CascadeScore         float64 `json:"cascade_score"`
	IsKeystone           bool    `json:"is_keystone"`
	IsBottleneck         bool    `json:"is_bottleneck"`
	UpstreamPressure     float64 `json:"upstream_pressure"`
	StabilityMargin      float64 `json:"stability_margin"`
	ChangePoint          bool    `json:"change_point"`
	SpikeDetected        bool    `json:"spike_detected"`
	SignalQuality        string  `json:"signal_quality"`
	Hazard               float64 `json:"hazard"`
	Reservoir            float64 `json:"reservoir"`
	Control              *control.ServiceSnapshot `json:"control,omitempty"`
}

type EdgeState struct {
	Source    string  `json:"source"`
	Target    string  `json:"target"`
	Weight    float64 `json:"weight"`
	CallRate  float64 `json:"call_rate"`
	ErrorRate float64 `json:"error_rate"`
	LatencyMs float64 `json:"latency_ms"`
}


// Server


type Server struct {
	mu        sync.RWMutex
	state     *SystemState
	simEng    *simulation.Engine
	store     *telemetry.Store
	graph     *topology.Graph
	port      int
	qEngine   *modelling.QueuePhysicsEngine
	sigProc   *modelling.SignalProcessor
	coupler   *modelling.TelemetryCoupler
	controlPlane *control.Plane
	tickCount int64
}

func New(
	port int,
	store *telemetry.Store,
	graph *topology.Graph,
	qEngine *modelling.QueuePhysicsEngine,
	sigProc *modelling.SignalProcessor,
	coupler *modelling.TelemetryCoupler,
) *Server {
	return &Server{
		port:    port,
		store:   store,
		graph:   graph,
		simEng:  simulation.New(),
		qEngine: qEngine,
		sigProc: sigProc,
		coupler: coupler,
		state:   &SystemState{CollapseZone: "safe", ComputedAt: time.Now()},
	}
}

func (s *Server) AttachControlPlane(controlPlane *control.Plane) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.controlPlane = controlPlane
}

func (s *Server) UpdateState(discoveryStatus string, hasLiveData bool) {
	state := s.computeState(discoveryStatus, hasLiveData)
	s.mu.Lock()
	s.state = state
	s.tickCount++
	s.mu.Unlock()
}

func (s *Server) GetState() *SystemState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/control", s.handleControl)
	mux.HandleFunc("/api/simulate", s.handleSimulate)
	mux.HandleFunc("/api/services", s.handleServices)
	mux.HandleFunc("/health", s.handleHealth)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()
	log.Printf("[api] dashboard → http://localhost:%d", s.port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}


// Handlers


func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(s.GetState())
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(s.GetState().Services)
}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(s.controlSnapshot())
}

func (s *Server) handleSimulate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req simulation.WhatIfRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	snap := s.graph.Snapshot()
	windows := s.store.AllWindows(60, 30*time.Second)
	if len(windows) == 0 {
		http.Error(w, `{"error":"no live telemetry data available — connect Prometheus or send OTel traces"}`, http.StatusServiceUnavailable)
		return
	}
	result := s.simEng.Run(req, windows, snap)
	json.NewEncoder(w).Encode(result)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","tick":%d}`, s.tickCount)
}

func (s *Server) controlSnapshot() control.Snapshot {
	s.mu.RLock()
	controlPlane := s.controlPlane
	s.mu.RUnlock()
	if controlPlane == nil {
		return control.Snapshot{
			Enabled:  false,
			Services: map[string]control.ServiceSnapshot{},
		}
	}
	return controlPlane.Snapshot()
}


// State computation — full prediction pipeline


func (s *Server) computeState(discoveryStatus string, hasLiveData bool) *SystemState {
	state := &SystemState{
		ComputedAt:      time.Now(),
		DiscoveryStatus: discoveryStatus,
		HasLiveData:     hasLiveData,
	}
	controlState := s.controlSnapshot()
	state.ControlPlane = controlState

	snap := s.graph.Snapshot()
	windows := s.store.AllWindows(60, 30*time.Second)
	if len(windows) == 0 {
		state.CollapseZone = "safe"
		state.SaturationHorizonStr = "∞"
		state.SaturationHorizonSec = -1
		return state
	}
	state.ServiceCount = len(windows)

	// Stage 1: TelemetryCoupler — queue persistence, latency decay, demand memory
	s.coupler.ApplyCoupling(windows, snap)

	// Stage 2: NetworkCoupling — SOR propagation across dependency graph
	coupling := modelling.ComputeNetworkCoupling(windows, snap)

	// Stage 3: FixedPointEquilibrium — Gauss-Seidel convergence solver
	fp := modelling.ComputeFixedPointEquilibrium(windows, snap)

	// Stage 4: TopologySensitivity — keystone detection, system fragility
	ts := modelling.ComputeTopologySensitivity(snap)

	// Stage 5: PerturbationSensitivity — which service removal hurts most
	perturbSens := modelling.ComputePerturbationSensitivity(windows, snap, fp.SystemicCollapseProb)

	// Stage 6: per-service parallel pipeline
	type svcResult struct {
		id string
		ss ServiceState
	}
	results := make([]svcResult, 0, len(windows))
	var resultMu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

	for id, w := range windows {
		wg.Add(1)
		sem <- struct{}{}
		go func(svcID string, win *telemetry.ServiceWindow) {
			defer func() { <-sem; wg.Done() }()

			sig := s.sigProc.Update(win)
			// medianMode=true wires medianBiasedRate for burst-resistant arrival
			qm := s.qEngine.RunQueueModel(win, snap, true)
			sm := modelling.RunStochasticModel(win)
			sa := modelling.RunStabilityAssessment(qm, sig, snap, 0.90)

			nc := coupling[svcID]
			tsSvc := ts.ByService[svcID]

			ss := ServiceState{
				ID:                 svcID,
				CollapseZone:       sa.CollapseZone,
				CollapseRisk:       sa.CollapseRisk,
				Utilisation:        qm.Utilisation,
				EquilibriumRho:     fp.EquilibriumRho[svcID],
				MeanQueueDepth:     qm.MeanQueueLen,
				MeanLatencyMs:      win.MeanLatencyMs,
				MeanRequestRate:    win.MeanRequestRate,
				BurstAmplification: sm.BurstAmplification,
				RiskPropagation:    sm.RiskPropagation,
				OscillationRisk:    sa.OscillationRisk,
				FeedbackGain:       sa.FeedbackGain,
				CascadeScore:       sa.CascadeAmplificationScore,
				IsKeystone:         tsSvc.IsKeystone,
				UpstreamPressure:   nc.EffectivePressure,
				StabilityMargin:    sa.StabilityMargin,
				ChangePoint:        sig.ChangePointDetected,
				SpikeDetected:      sig.SpikeDetected,
				SignalQuality:      win.SignalQuality,
				Hazard:             win.Hazard,
				Reservoir:          win.Reservoir,
			}
			if nc.PathSaturationHorizonSec >= 0 {
				ss.SaturationHorizonSec = nc.PathSaturationHorizonSec
			} else if qm.SaturationHorizon > 0 {
				ss.SaturationHorizonSec = qm.SaturationHorizon.Seconds()
			} else {
				ss.SaturationHorizonSec = -1
			}
			if controlSvc, ok := controlState.Services[svcID]; ok {
				controlCopy := controlSvc
				ss.Control = &controlCopy
			}

			resultMu.Lock()
			results = append(results, svcResult{id: svcID, ss: ss})
			resultMu.Unlock()
		}(id, w)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool { return results[i].id < results[j].id })

	// System-level aggregation
	systemZone := "safe"
	systemProb := fp.SystemicCollapseProb
	earliestSat := math.MaxFloat64
	highestRiskSvc, maxRisk := "", 0.0
	bottleneckSvc, maxRho := "", 0.0
	maxPerturbSvc, maxPerturbDelta := "", 0.0

	for i := range results {
		ss := &results[i].ss
		if ss.EquilibriumRho > maxRho {
			maxRho = ss.EquilibriumRho
			bottleneckSvc = ss.ID
		}
		if ss.CollapseRisk > maxRisk {
			maxRisk = ss.CollapseRisk
			highestRiskSvc = ss.ID
		}
		if ss.SaturationHorizonSec == 0 {
			earliestSat = 0
		} else if ss.SaturationHorizonSec > 0 && ss.SaturationHorizonSec < earliestSat {
			earliestSat = ss.SaturationHorizonSec
		}
		if zoneRank(ss.CollapseZone) > zoneRank(systemZone) {
			systemZone = ss.CollapseZone
		}
		if perturbSens != nil {
			if d := perturbSens[ss.ID]; d > maxPerturbDelta {
				maxPerturbDelta = d
				maxPerturbSvc = ss.ID
			}
		}
	}
	for i := range results {
		if results[i].id == bottleneckSvc {
			results[i].ss.IsBottleneck = true
		}
	}

	svcStates := make([]ServiceState, len(results))
	for i, r := range results {
		svcStates[i] = r.ss
	}
	state.Services = svcStates

	// NetworkEquilibrium
	netEq := modelling.ComputeNetworkEquilibrium(coupling, windows)

	// Edges
	edgeStates := make([]EdgeState, 0, len(snap.Edges))
	for _, e := range snap.Edges {
		edgeStates = append(edgeStates, EdgeState{
			Source: e.Source, Target: e.Target, Weight: e.Weight,
			CallRate: e.CallRate, ErrorRate: e.ErrorRate, LatencyMs: e.LatencyMs,
		})
	}
	state.Edges = edgeStates

	// Dangerous path: topology sensitivity MaxAmplificationPath → CriticalPath → perturbation
	state.MostDangerousPath = ts.MaxAmplificationPath
	if len(state.MostDangerousPath) == 0 {
		state.MostDangerousPath = snap.CriticalPath.Nodes
	}
	if len(state.MostDangerousPath) == 0 && maxPerturbSvc != "" {
		state.MostDangerousPath = []string{maxPerturbSvc}
	}

	state.CollapseZone = systemZone
	state.CollapseProb = math.Min(systemProb, 1.0)
	state.HighestRiskService = highestRiskSvc
	state.BottleneckService = bottleneckSvc
	state.SystemFragility = ts.SystemFragility
	state.NetworkSatRisk = netEq.NetworkSaturationRisk
	state.IsConverging = netEq.IsConverging
	state.TickCount = s.tickCount

	switch {
	case earliestSat == 0:
		state.SaturationHorizonSec = 0
		state.SaturationHorizonStr = "NOW"
	case earliestSat == math.MaxFloat64:
		state.SaturationHorizonSec = -1
		state.SaturationHorizonStr = "∞"
	default:
		state.SaturationHorizonSec = earliestSat
		state.SaturationHorizonStr = fmtDuration(earliestSat)
	}

	_ = maxPerturbSvc
	return state
}

func zoneRank(z string) int {
	switch z {
	case "collapse":
		return 2
	case "warning":
		return 1
	}
	return 0
}

func fmtDuration(sec float64) string {
	if sec <= 0 {
		return "NOW"
	}
	if sec < 60 {
		return fmt.Sprintf("%.0fs", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%.1fm", sec/60)
	}
	return fmt.Sprintf("%.1fh", sec/3600)
}


// Dashboard


func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buildDashboardHTML())
}

func buildDashboardHTML() []byte {
	return []byte(dashCSS + dashHTML + dashJS)
}


// ensure strings import used

var _ = strings.Contains
