package autopilot

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"math/rand"
	"sort"
	"time"
)

// -------------------------------------------------------------------------
// TEST 1: NESTEROV ACCELERATED GRADIENT DESCENT STRICT CONVERGENCE
// -------------------------------------------------------------------------
func TestElite_NesterovConvergence_Strict(t *testing.T) {
	fmt.Println("\n=== [TEST 1] NESTEROV MPC STRICT CONVERGENCE ===")
	
	mpc := &MPCOptimiser{
		Horizon:      10,
		Dt:           1.0,
		BacklogCost:  10.0,
		LatencyCost:  5.0,
		SmoothCost:   20.0,
		MinCapacity:  1.0,
		MaxCapacity:  1000.0,
		Iters:        30,
		IterModifier: 1.0,
	}

	initial := MPCState{
		Backlog:        5000.0,
		Latency:        800.0,
		ArrivalMean:    200.0,
		ServiceRate:    10.0, 
		CapacityActive: 5.0,
	}

	prevSeq := make([]MPCControl, mpc.Horizon)
	seq, conf := mpc.Optimise(initial, prevSeq)

	// STRICT ASSERTION 1: Confidence must reflect mathematical improvement
	if conf < 0.8 {
		t.Fatalf("[FATAL] Optimizer failed to find a high-confidence trajectory. Conf: %.4f", conf)
	}

	simState := initial
	for _, u := range seq {
		simState = mpc.propagate(simState, u)
	}

	// STRICT ASSERTION 2: The optimal trajectory MUST drain the queue significantly
	if simState.Backlog >= initial.Backlog {
		t.Fatalf("[FATAL] Optimizer trajectory did not drain queue! Start: %.1f, End: %.1f", initial.Backlog, simState.Backlog)
	}
	
	// STRICT ASSERTION 3: Latency must mathematically drop as queue drains
	if simState.Latency >= initial.Latency {
		t.Fatalf("[FATAL] Latency invariant violated! Start: %.1f, End: %.1f", initial.Latency, simState.Latency)
	}

	fmt.Printf("✔ PASS: Queue strictly drained (5000 -> %.1f). Confidence: %.4f\n", simState.Backlog, conf)
}

// -------------------------------------------------------------------------
// TEST 2: LYAPUNOV DPP VARIANCE INVARIANT CHECKS
// -------------------------------------------------------------------------
func TestElite_LyapunovDPP_Strict(t *testing.T) {
	fmt.Println("\n=== [TEST 2] LYAPUNOV DPP INVARIANT CHECKS ===")

	ctrl := &Controller{Dt: 100.0, MPCWeight: 0.0} 
	mpcInput := ControlInput{CapacityTarget: 0, RetryFactor: 0, CacheRelief: 0}

	// Scenarios
	calm := PlantState{ArrivalMean: 100, ArrivalP95: 105, Backlog: 50, ServiceRate: 10, CapacityActive: 10}
	chaos := PlantState{ArrivalMean: 100, ArrivalP95: 800, Backlog: 50, ServiceRate: 10, CapacityActive: 10}
	swamped := PlantState{ArrivalMean: 100, ArrivalP95: 105, Backlog: 2000, ServiceRate: 10, CapacityActive: 10}

	outCalm := ctrl.Compute(calm, calm, mpcInput)
	outChaos := ctrl.Compute(chaos, chaos, mpcInput)
	outSwamped := ctrl.Compute(swamped, swamped, mpcInput)

	fmt.Printf("Targets -> Calm: %.1f | Chaos: %.1f | Swamped: %.1f\n", 
		outCalm.CapacityTarget, outChaos.CapacityTarget, outSwamped.CapacityTarget)

	// STRICT ASSERTION 1: Variance Awareness
	// If P95 variance is huge (Chaos), target MUST be strictly greater than Calm, even if backlog is identical.
	if outChaos.CapacityTarget <= outCalm.CapacityTarget {
		t.Fatalf("[FATAL] DPP failed variance check! Chaos target (%.1f) must be > Calm target (%.1f)", outChaos.CapacityTarget, outCalm.CapacityTarget)
	}

	// STRICT ASSERTION 2: Backlog Awareness
	// If queue is massive (Swamped), target MUST be strictly greater than Calm.
	if outSwamped.CapacityTarget <= outCalm.CapacityTarget {
		t.Fatalf("[FATAL] DPP failed queue check! Swamped target (%.1f) must be > Calm target (%.1f)", outSwamped.CapacityTarget, outCalm.CapacityTarget)
	}

	fmt.Println("✔ PASS: DPP strictly adapts to statistical variance and queue depth.")
}

// -------------------------------------------------------------------------
// TEST 3: CONTROL BARRIER FUNCTION (CBF) MATHEMATICAL PROOFS
// -------------------------------------------------------------------------
func TestElite_ControlBarrierSafety_Strict(t *testing.T) {
	fmt.Println("\n=== [TEST 3] CONTROL BARRIER FUNCTION (CBF) PROOFS ===")

	safeBacklogLimitFunc := func(workers float64) float64 { return workers * 5.0 }

	scenarios := []DecisionInput{
		{Backlog: 10, Workers: 20, TargetCapacity: 5, Confidence: 0.9},   // Safe
		{Backlog: 150, Workers: 20, TargetCapacity: 5, Confidence: 0.9},  // Critical Violation
	}

	for i, s := range scenarios {
		decision := Decide(s)
		hx := safeBacklogLimitFunc(s.Workers) - s.Backlog

		if hx < 0 {
			// STRICT ASSERTION 1: If barrier is breached, system MUST override to scale_up.
			if decision.Action != "scale_up" {
				t.Fatalf("[FATAL] CBF Violation! Backlog (%.1f) breached limit (%.1f), but action was %s", s.Backlog, safeBacklogLimitFunc(s.Workers), decision.Action)
			}
			// STRICT ASSERTION 2: ScaleDelta MUST reflect the severity of the breach.
			if decision.ScaleDelta < 0.1 {
				t.Fatalf("[FATAL] CBF Violation! Action is scale_up but delta is dangerously low (%.3f)", decision.ScaleDelta)
			}
			fmt.Printf("✔ PASS (Scenario %d): CBF successfully blocked unsafe MPC downscale and forced emergency up-scale.\n", i+1)
		} else {
			// STRICT ASSERTION 3: Normal operation allowed.
			if decision.Action == "scale_up" && s.TargetCapacity < s.Workers {
				t.Fatalf("[FATAL] False positive CBF trigger. Safe state overriden incorrectly.")
			}
			fmt.Printf("✔ PASS (Scenario %d): CBF correctly allowed safe operation.\n", i+1)
		}
	}
}

// -------------------------------------------------------------------------
// TEST 4: BLACK SWAN PHYSICS SIMULATION (RECOVERY & OVERSHOOT BOUNDS)
// -------------------------------------------------------------------------
func TestElite_BlackSwanSimulation_Strict(t *testing.T) {
	fmt.Println("\n=== [TEST 4] BLACK SWAN STRICT SIMULATION ===")
	
	ctrl := &Controller{Dt: 1.0, MPCWeight: 0.8}
	mpc := &MPCOptimiser{
		Horizon: 5, Dt: 1.0, BacklogCost: 10.0, SmoothCost: 10.0, 
		MinCapacity: 1.0, MaxCapacity: 500.0, Iters: 20, IterModifier: 1.0,
	}

	realBacklog := 0.0
	realWorkers := 10.0
	serviceRate := 10.0
	prevPlant := PlantState{Backlog: 0, CapacityActive: 10}
	prevSeq := make([]MPCControl, mpc.Horizon)

	maxWorkersObserved := 0.0
	peakBacklogObserved := 0.0

	for tick := 1; tick <= 40; tick++ {
		arrival := 100.0
		if tick >= 10 && tick <= 15 {
			arrival = 800.0 // Black Swan Spike
		} else if tick > 15 {
			arrival = 50.0  // Post-spike drop
		}

		processed := realWorkers * serviceRate
		realBacklog = math.Max(0, realBacklog + arrival - processed)

		if realBacklog > peakBacklogObserved { peakBacklogObserved = realBacklog }
		if realWorkers > maxWorkersObserved { maxWorkersObserved = realWorkers }

		currPlant := PlantState{
			ArrivalMean: arrival, ArrivalP95: arrival * 1.5,
			Backlog: realBacklog, ServiceRate: serviceRate, CapacityActive: realWorkers,
		}
		mpcState := MPCState{
			Backlog: realBacklog, ArrivalMean: arrival, ArrivalVar: arrival*0.5,
			ServiceRate: serviceRate, CapacityActive: realWorkers, PrevBacklog: prevPlant.Backlog,
		}

		prevSeq, _ = mpc.Optimise(mpcState, prevSeq)
		mpcInput := ControlInput{CapacityTarget: prevSeq[0].CapacityTarget, RetryFactor: prevSeq[0].RetryFactor, CacheRelief: prevSeq[0].CacheRelief}
		dppOut := ctrl.Compute(currPlant, prevPlant, mpcInput)
		
		decision := Decide(DecisionInput{
			Instability: 0.5, Confidence: 0.9, Backlog: realBacklog, 
			Workers: realWorkers, TargetCapacity: dppOut.CapacityTarget,
		})

		if decision.Action == "scale_up" { realWorkers += decision.ScaleDelta * realWorkers }
		if decision.Action == "scale_down" { realWorkers -= decision.ScaleDelta * realWorkers }
		if realWorkers < 1.0 { realWorkers = 1.0 }

		prevPlant = currPlant
	}

	fmt.Printf("Peak Backlog: %.1f | Peak Workers: %.1f | Final Backlog: %.1f\n", peakBacklogObserved, maxWorkersObserved, realBacklog)

	// STRICT ASSERTION 1: Did the system actually recover?
	if realBacklog > 50.0 {
		t.Fatalf("[FATAL] System failed to recover from Black Swan! Final backlog: %.1f", realBacklog)
	}

	// STRICT ASSERTION 2: Did the system OVERSHOOT massively? (Cloud Bill check)
	// For an 800 RPS spike at 10 serviceRate, theoretical min required is 80 workers.
	// Anything above 160 is a dangerous overshoot (cost waste).
	if maxWorkersObserved > 160.0 {
		t.Fatalf("[FATAL] Massive Overshoot Detected! Cloud Bill Waste. Peak Workers: %.1f (Expected < 160)", maxWorkersObserved)
	}

	// STRICT ASSERTION 3: Did the system scale down after recovery?
	if realWorkers > 40.0 {
		t.Fatalf("[FATAL] Scale-down failure! System is holding excess capacity after recovery. Final Workers: %.1f", realWorkers)
	}

	fmt.Println("✔ PASS: Black swan handled without infinite overshoot, and gracefully drained.")
}





// -------------------------------------------------------------------------
// TEST 5: PROPERTY-BASED FUZZING (100,000 EXTREME RANDOM STATES)
// -------------------------------------------------------------------------
// Objective: Prove that no matter what garbage data the sensors send,
// the math NEVER panics, NEVER returns NaN/Inf, and bounds outputs strictly.
func TestElite_PropertyBased_Fuzzing(t *testing.T) {
	fmt.Println("\n=== [TEST 5] PROPERTY-BASED FUZZING (100k STATES) ===")
	
	rng := rand.New(rand.NewSource(42))
	iterations := 100000

	for i := 0; i < iterations; i++ {
		// Generate absolute chaos (negative values, extreme spikes, zeroes)
		in := DecisionInput{
			Instability:    rng.NormFloat64() * 5.0,  // Can be negative or huge
			Confidence:     rng.NormFloat64() * 2.0,  // Values outside [0,1]
			Backlog:        rng.ExpFloat64() * 50000, // Massive queues
			Workers:        rng.Float64() * 1000,     // Floating point workers
			TargetCapacity: rng.Float64() * 5000,
			Oscillation:    rng.NormFloat64(),
		}

		// Force some zeroes to test division-by-zero panics
		if i%10 == 0 { in.Workers = 0.0 }
		if i%11 == 0 { in.Backlog = 0.0 }

		decision := Decide(in)

		// STRICT PROPERTIES (INVARIANTS)
		if math.IsNaN(decision.ScaleDelta) || math.IsInf(decision.ScaleDelta, 0) {
			t.Fatalf("[FATAL] Math engine leaked NaN/Inf! Input: %+v", in)
		}
		if decision.ScaleDelta < 0.0 || decision.ScaleDelta > 1.0 {
			t.Fatalf("[FATAL] ScaleDelta out of strict [0,1] bounds: %f", decision.ScaleDelta)
		}
		if decision.Action == "" {
			t.Fatalf("[FATAL] Decision action is empty!")
		}
	}
	fmt.Printf("✔ PASS: Survived %d extreme chaos states with 0 panics, 0 NaNs, and strict mathematical bounds.\n", iterations)
}

// -------------------------------------------------------------------------
// TEST 6: MONTE CARLO + SENSOR NOISE + LATENCY SLO
// -------------------------------------------------------------------------
// Objective: Run thousands of stochastic realities where actual traffic
// differs from forecasted traffic (sensor noise). Ensure P95 Latency SLO is met.
func TestElite_MonteCarlo_SLO(t *testing.T) {
	fmt.Println("\n=== [TEST 6] MONTE CARLO & SENSOR NOISE (1000 REALITIES) ===")

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	simulations := 1000
	sloFailures := 0
	targetSLO_P95 := 500.0 // 500ms max latency

	for sim := 0; sim < simulations; sim++ {
		ctrl := &Controller{Dt: 1.0, MPCWeight: 0.5}
		realWorkers := 10.0
		realBacklog := 0.0
		serviceRate := 10.0

		var latencies []float64

		for tick := 1; tick <= 50; tick++ {
			// 1. Stochastic Traffic (Random walk with spikes)
			actualArrival := 100.0 + rng.NormFloat64()*30.0
			if tick == 20 { actualArrival += rng.ExpFloat64() * 500.0 } // Random burst

			// 2. SENSOR NOISE: Controller sees a distorted reality (+/- 20% error)
			sensorNoise := 0.8 + (0.4 * rng.Float64())
			forecastArrival := actualArrival * sensorNoise

			// Physical Reality
			processed := realWorkers * serviceRate
			realBacklog = math.Max(0, realBacklog + actualArrival - processed)

			// Calculate real simulated latency (Queue / Process Rate)
			currentLatency := 0.0
			if processed > 0 { currentLatency = (realBacklog / processed) * 1000.0 } // in ms
			latencies = append(latencies, currentLatency)

			// Controller acts on NOISY sensor data
			currPlant := PlantState{ArrivalMean: forecastArrival, ArrivalP95: forecastArrival*1.5, Backlog: realBacklog, ServiceRate: serviceRate, CapacityActive: realWorkers}
			prevPlant := PlantState{Backlog: realBacklog*0.9, CapacityActive: realWorkers} // Fake history
			
			dppOut := ctrl.Compute(currPlant, prevPlant, ControlInput{CapacityTarget: forecastArrival/serviceRate, RetryFactor: 0, CacheRelief: 0})
			decision := Decide(DecisionInput{Confidence: 0.8, Backlog: realBacklog, Workers: realWorkers, TargetCapacity: dppOut.CapacityTarget})

			if decision.Action == "scale_up" { realWorkers += decision.ScaleDelta * realWorkers }
			if decision.Action == "scale_down" { realWorkers -= decision.ScaleDelta * realWorkers }
			if realWorkers < 1.0 { realWorkers = 1.0 }
		}

		// Calculate P95 Latency for this specific Monte Carlo run
		sort.Float64s(latencies)
		p95Index := int(float64(len(latencies)) * 0.95)
		p95Latency := latencies[p95Index]

		if p95Latency > targetSLO_P95 {
			sloFailures++
		}
	}

	failureRate := (float64(sloFailures) / float64(simulations)) * 100.0
	fmt.Printf("Total Monte Carlo Runs: %d\n", simulations)
	fmt.Printf("SLO Violations (>%.0fms): %d (%.2f%%)\n", targetSLO_P95, sloFailures, failureRate)

	// STRICT ASSERTION: 99% of realities MUST meet the SLO despite sensor noise
	if failureRate > 1.0 {
		t.Fatalf("[FATAL] Monte Carlo SLO Failure! %f%% of realities breached the P95 latency SLO.", failureRate)
	}
	fmt.Println("✔ PASS: Met strict P95 Latency SLO across heavily noisy stochastic realities.")
}

// -------------------------------------------------------------------------
// TEST 7: INFRASTRUCTURE CATASTROPHE (WORKER CRASH SURVIVABILITY)
// -------------------------------------------------------------------------
// Objective: Midway through a steady state, 50% of instances suddenly die
// (OOM kill, Node crash). Prove the controller detects and recovers.
func TestElite_WorkerFailure(t *testing.T) {
	fmt.Println("\n=== [TEST 7] CATASTROPHIC POD CRASH SURVIVABILITY ===")

	ctrl := &Controller{Dt: 1.0, MPCWeight: 0.8}
	
	realWorkers := 50.0
	realBacklog := 0.0
	serviceRate := 10.0
	arrival := 450.0 // Steady load

	fmt.Printf("%-5s | %-12s | %-10s | %-15s\n", "Tick", "Real Workers", "Queue", "Event")
	fmt.Println(strings.Repeat("-", 60))

	for tick := 1; tick <= 30; tick++ {
		event := ""

		// THE CATASTROPHE: At tick 12, AWS loses a zone. 50% pods drop instantly.
		if tick == 12 {
			realWorkers = math.Floor(realWorkers * 0.5)
			event = "⚠️ 50% POD CRASH"
		}

		processed := realWorkers * serviceRate
		realBacklog = math.Max(0, realBacklog + arrival - processed)

		currPlant := PlantState{ArrivalMean: arrival, Backlog: realBacklog, ServiceRate: serviceRate, CapacityActive: realWorkers}
		dppOut := ctrl.Compute(currPlant, currPlant, ControlInput{CapacityTarget: arrival/serviceRate})
		
		decision := Decide(DecisionInput{Confidence: 0.9, Backlog: realBacklog, Workers: realWorkers, TargetCapacity: dppOut.CapacityTarget})

		if decision.Action == "scale_up" { realWorkers += decision.ScaleDelta * realWorkers }
		if realWorkers < 1.0 { realWorkers = 1.0 }

		if tick >= 12 && tick <= 18 || tick == 30 {
			fmt.Printf("%-5d | %-12.1f | %-10.1f | %-15s\n", tick, realWorkers, realBacklog, event)
		}
	}

	// STRICT ASSERTION 1: System MUST have recovered capacity back to handle 450 arrival (needs 45 workers)
	if realWorkers < 45.0 {
		t.Fatalf("[FATAL] Controller failed to recover capacity after hardware crash. Final workers: %.1f", realWorkers)
	}

	// STRICT ASSERTION 2: Backlog must be under control/draining
	if realBacklog > 500.0 {
		t.Fatalf("[FATAL] Queue spiraled out of control after hardware crash. Final backlog: %.1f", realBacklog)
	}

	fmt.Println("✔ PASS: Detected infrastructure catastrophe and fully self-healed capacity.")
}