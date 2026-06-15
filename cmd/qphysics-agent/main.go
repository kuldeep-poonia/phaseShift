// qphysics-agent — physics-based saturation prediction for distributed systems.
//
// Usage:
//   qphysics-agent [--port 8080] [--tick 2s]
//
// Auto-discovers Prometheus and OpenTelemetry sources. Zero manual configuration.
// When no sources are found the dashboard shows a clear status screen with
// instructions — no synthetic data is ever generated.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	
    "github.com/qphysics/phaseshift/internal/agent/api"
    "github.com/qphysics/phaseshift/internal/agent/discovery"
    "github.com/qphysics/phaseshift/internal/agent/ingestion"
    "github.com/qphysics/phaseshift/modelling"
    "github.com/qphysics/phaseshift/telemetry"
    "github.com/qphysics/phaseshift/topology"
)


func main() {
	port    := flag.Int("port", 8080, "Dashboard port")
	tickStr := flag.String("tick", "2s", "Engine tick interval (e.g. 2s, 500ms)")
	bufCap  := flag.Int("buf", 120, "Ring buffer capacity per service")
	maxSvc  := flag.Int("max-services", 500, "Maximum services to track")
	flag.Parse()

	tickInterval, err := time.ParseDuration(*tickStr)
	if err != nil || tickInterval < 100*time.Millisecond {
		tickInterval = 2 * time.Second
	}

	log.SetFlags(log.Ltime | log.Lshortfile)
	log.Printf("[qphysics] port=%d tick=%s", *port, tickInterval)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		log.Println("[qphysics] shutting down")
		cancel()
	}()

	// ── Core engine ───────────────────────────────────────────────────────
	store   := telemetry.NewStore(*bufCap, *maxSvc, 5*time.Minute)
	graph   := topology.New()
	qEngine := modelling.NewQueuePhysicsEngine()
	sigProc := modelling.NewSignalProcessor(0.3, 0.05, 3.0)
	coupler := modelling.NewTelemetryCoupler()

	// ── API server ────────────────────────────────────────────────────────
	srv := api.New(*port, store, graph, qEngine, sigProc, coupler)

	// ── Discovery ─────────────────────────────────────────────────────────
	discCtx, discCancel := context.WithTimeout(ctx, 10*time.Second)
	env := discovery.Discover(discCtx)
	discCancel()
	log.Printf("[discovery] %s", env.Summary())

	// ── Prometheus scrapers ───────────────────────────────────────────────
	for _, u := range env.PrometheusURLs {
		sc := ingestion.NewPromScraper(u, store)
		go sc.Run(ctx, tickInterval)
		log.Printf("[prom] scraper → %s", u)
	}

	// ── OTel trace receiver ───────────────────────────────────────────────
	otelRx := ingestion.NewOTelReceiver()
	go runOTelEdgeConsumer(ctx, otelRx.EdgeChannel(), graph, store)
	go runOTelHTTPServer(ctx, otelRx, 4318)

	// ── Topology maintainer ───────────────────────────────────────────────
	go runTopologyMaintainer(ctx, store, graph, qEngine, sigProc, tickInterval*10)

	// ── Orchestrator tick loop ────────────────────────────────────────────
	go runOrchestrator(ctx, store, graph, srv, env, tickInterval)

	// ── HTTP server (blocks) ──────────────────────────────────────────────
	if err := srv.Start(ctx); err != nil {
		log.Printf("[api] %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Orchestrator
// ─────────────────────────────────────────────────────────────────────────────

func runOrchestrator(
	ctx      context.Context,
	store    *telemetry.Store,
	graph    *topology.Graph,
	srv      *api.Server,
	env      *discovery.Environment,
	interval time.Duration,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// First tick immediately.
	doTick(store, graph, srv, env)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			doTick(store, graph, srv, env)
		}
	}
}

func doTick(
	store *telemetry.Store,
	graph *topology.Graph,
	srv   *api.Server,
	env   *discovery.Environment,
) {
	hasData := store.HasServices()
	if hasData {
		// Update topology graph with current window data.
		windows := store.AllWindows(60, 30*time.Second)
		if len(windows) > 0 {
			graph.Update(windows)
			// Wire FinalFluidPlant outputs (Hazard Z, Reservoir R) into windows.
			updateFluidStates(windows, graph.Snapshot())
		}
		store.Prune(time.Now())
	}
	srv.UpdateState(env.Summary(), hasData)
}

// ─────────────────────────────────────────────────────────────────────────────
// FinalFluidPlant wiring
//
// Every ServiceWindow carries Hazard (Z) and Reservoir (R) fields that were
// previously always zero. We run one FinalFluidPlant step per service per tick
// and write Z → window.Hazard, normalised(R) → window.Reservoir.
// These values then flow through RunStabilityAssessment and RunQueueModel
// (via q.Hazard, q.Reservoir) making the fluid plant state observable in the
// dashboard and in what-if simulations.
// ─────────────────────────────────────────────────────────────────────────────

var (
	plantMu  sync.Mutex
	plants   = make(map[string]*modelling.FinalFluidPlant)
	plantRNG = rand.New(rand.NewSource(time.Now().UnixNano()))
)

func updateFluidStates(windows map[string]*telemetry.ServiceWindow, snap topology.GraphSnapshot) {
	plantMu.Lock()
	defer plantMu.Unlock()

	for id, w := range windows {
		p, exists := plants[id]
		if !exists {
			p = plantFromWindow(w)
			plants[id] = p
		} else {
			// Slowly track window parameters.
			if w.MeanLatencyMs > 0 {
				newMu := 1000.0 / w.MeanLatencyMs * math.Max(w.MeanActiveConns, 1)
				p.Mu = 0.9*p.Mu + 0.1*newMu
			}
			if p.Mu > 0 {
				p.Rho = 0.8*p.Rho + 0.2*math.Min(w.MeanRequestRate/p.Mu, 1.5)
			}
		}

		dBH := modelling.ComputeDBH(plantRNG, p.Dt)
		_, _, z := p.Step(1.0, dBH)

		// Write results back into window — these are now consumed by the
		// stability and queue engines every tick.
		w.Hazard    = z
		w.Reservoir = math.Tanh(math.Abs(p.R)) // normalise R ∈ (-∞,∞) → [0,1]
	}

	// Prune plants for services that have been removed.
	for id := range plants {
		if _, ok := windows[id]; !ok {
			delete(plants, id)
		}
	}
	_ = snap
}

func plantFromWindow(w *telemetry.ServiceWindow) *modelling.FinalFluidPlant {
	mu := 100.0
	if w.MeanLatencyMs > 0 {
		mu = 1000.0 / w.MeanLatencyMs * math.Max(w.MeanActiveConns, 1)
	}
	rho := 0.5
	if mu > 0 {
		rho = math.Min(w.MeanRequestRate/mu, 1.5)
	}
	a := math.Max(w.MeanRequestRate, 0.01)
	return &modelling.FinalFluidPlant{
		Mu: mu, Rho: rho,
		KappaA: 0.5, Nu: 0.1, ChiA: 0.001, Psi0: 0.5,
		Qsat: math.Max(w.MeanQueueDepth*2, 20),
		Amax: math.Max(a*3, mu*rho*3),
		Alpha: 0.01, Beta: 1.5, Eta: 0.1,
		Theta: 0.5, Zeta: 0.3, Pexp: 1.5,
		Eps: 0.05, Gamma: 1.2,
		Omega: 0.2, LambdaR: 0.1,
		KL: 50, Delta: 10, Dt: 0.1,
		A: a,
		Q: math.Max(w.LastQueueDepth, 0),
		Z: math.Max(w.Hazard, 0),
		R: math.Max(w.Reservoir, 0),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// OTel edge consumer — builds topology from received trace spans
// ─────────────────────────────────────────────────────────────────────────────

func runOTelEdgeConsumer(
	ctx   context.Context,
	edges <-chan ingestion.SpanEdge,
	graph *topology.Graph,
	store *telemetry.Store,
) {
	batch := make(map[string]ingestion.SpanEdge)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-edges:
			if !ok {
				return
			}
			batch[e.Source+"→"+e.Target] = e
			// Seed store so both services are known.
			now := time.Now()
			for _, id := range []string{e.Source, e.Target} {
				if store.Window(id, 1, time.Minute) == nil {
					store.Ingest(&telemetry.MetricPoint{
						ServiceID:   id,
						Timestamp:   now,
						RequestRate: 1,
						Latency:     telemetry.LatencyStats{Mean: 10},
						ActiveConns: 1,
					})
				}
			}
		case <-ticker.C:
			if len(batch) == 0 {
				continue
			}
			wmap := make(map[string]*telemetry.ServiceWindow, len(batch))
			for _, e := range batch {
				lat := e.LatencyMs
				if lat <= 0 {
					lat = 10
				}
				errRate := 0.0
				if e.IsError {
					errRate = 0.05
				}
				wmap[e.Source] = &telemetry.ServiceWindow{
					ServiceID:       e.Source,
					MeanRequestRate: 10,
					MeanLatencyMs:   lat,
					MeanErrorRate:   errRate,
					MeanActiveConns: 1,
					ConfidenceScore: 0.5,
					SampleCount:     1,
					UpstreamEdges: map[string]telemetry.EdgeWindow{
						e.Target: {
							TargetServiceID: e.Target,
							MeanCallRate:    1,
							MeanErrorRate:   errRate,
							MeanLatencyMs:   lat,
						},
					},
				}
			}
			graph.Update(wmap)
			batch = make(map[string]ingestion.SpanEdge)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// OTel HTTP receiver on :4318
// ─────────────────────────────────────────────────────────────────────────────

func runOTelHTTPServer(ctx context.Context, rx *ingestion.OTelReceiver, port int) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", rx.HandleTraces)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()
	log.Printf("[otel] OTLP/HTTP receiver on :%d/v1/traces", port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("[otel] non-fatal: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Topology maintainer — prunes stale engine state, runs periodic cleanups
// ─────────────────────────────────────────────────────────────────────────────

func runTopologyMaintainer(
	ctx     context.Context,
	store   *telemetry.Store,
	graph   *topology.Graph,
	qEngine *modelling.QueuePhysicsEngine,
	sigProc *modelling.SignalProcessor,
	interval time.Duration,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ids := store.ServiceIDs()
			activeSet := make(map[string]struct{}, len(ids))
			for _, id := range ids {
				activeSet[id] = struct{}{}
			}
			qEngine.Prune(activeSet)
			sigProc.Prune(activeSet)
			store.Prune(time.Now())
			// Also prune fluid plants.
			plantMu.Lock()
			for id := range plants {
				if _, ok := activeSet[id]; !ok {
					delete(plants, id)
				}
			}
			plantMu.Unlock()
			_ = graph
		}
	}
}

// topology.GraphSnapshot is used in updateFluidStates — keep import live.
var _ topology.GraphSnapshot