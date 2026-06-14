package modelling

import (
	"math"
	"math/rand"
)

// ComputeDBH generates a deterministic fractional noise increment.
// dBH ~ Normal(0, sqrt(dt)) scaled for Hurst-like long-memory (H=0.75 proxy).
func ComputeDBH(rng *rand.Rand, dt float64) float64 {
	// Hurst exponent proxy H = 0.75
	// sigma ~ dt^H. For standard Brownian motion H = 0.5.
	// Here we scale by sqrt(dt) and a slight H-bias.
	hBias := math.Pow(dt, 0.25) // adjustment factor for H=0.75
	return rng.NormFloat64() * math.Sqrt(dt) * hBias
}
