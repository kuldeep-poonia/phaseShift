package modelling

import "math"

// sigmoid maps x → (0,1) with standard logistic curve.
func sigmoid(x float64) float64 { return 1.0 / (1.0 + math.Exp(-x)) }

// clamp01 constrains v to [0, 1]. Shorthand used throughout the modelling pipeline.
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// pos returns max(v, 0). Prevents negative values from reaching physics calculations.
func pos(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}

// safeDiv divides a by b, returning fallback when b ≤ 0 or result is non-finite.
func safeDiv(a, b, fallback float64) float64 {
	if b <= 0 {
		return fallback
	}
	r := a / b
	if math.IsNaN(r) || math.IsInf(r, 0) {
		return fallback
	}
	return r
}

// medianBiasedRate returns a burst-resistant arrival rate estimate.
// It Winsorises the last sample towards the mean when the last sample
// deviates more than 1.5 standard deviations, reducing burst-clustering bias.
// When std is near-zero (stable traffic) it behaves identically to the EWMA estimator.
func medianBiasedRate(last, mean, std float64) float64 {
	if std <= 0 {
		return 0.6*last + 0.4*mean
	}
	deviation := last - mean
	threshold := 1.5 * std
	if math.Abs(deviation) > threshold {
		clampedLast := mean + math.Copysign(threshold, deviation)
		return 0.5*clampedLast + 0.5*mean
	}
	return 0.6*last + 0.4*mean
}

// clamp constrains v to [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}