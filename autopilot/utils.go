package autopilot

import "math"

// clamp01 strictly enforces the invariant x ∈ [0, 1].
func clamp01(x float64) float64 {
	if x < 0 { return 0 }
	if x > 1 { return 1 }
	return x
}

// pos enforces the physical non-negativity constraint x ∈ [0, ∞).
func pos(x float64) float64 {
	if x < 0 { return 0 }
	return x
}

// norm rigorously projects an unbounded signal x ∈ [0, ∞) onto the compact set [0, 1).
// Replaces the heuristic log-compression with a parameter-free, continuously 
// differentiable algebraic sigmoid mapping.
func norm(x float64) float64 {
	if x <= 0 { return 0 }
	// Algebraic Sigmoid: f(x) = x / (1 + x)
	return x / (1.0 + x)
}

// boundedAgg computes a mathematically rigorous Smooth Maximum (Log-Sum-Exp) 
// over a set of variables, replacing the flawed heuristic linear blend.
//
// THEOREM:
// By defining SmoothMax(x) = (1/beta) * ln( (1/N) * \sum exp(beta * x_i) ),
// we guarantee an exact 0 output when all inputs are 0 (since ln(N/N) = ln(1) = 0).
func boundedAgg(vals ...float64) float64 {
	n := len(vals)
	if n == 0 { return 0 }

	beta := 5.0 // Smoothness hardness parameter
	sumExp := 0.0

	for _, v := range vals {
		sumExp += math.Exp(beta * v)
	}

	// Formal formulation incorporating the 1/N scaling factor to precisely shift the origin
	smoothMax := (1.0 / beta) * math.Log(sumExp / float64(n))

	// Project bounded output through algebraic sigmoid
	return smoothMax / (1.0 + smoothMax)
}