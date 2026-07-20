package strategy

import "math"

// ComputeLeverage returns a leverage multiplier in [1.0, 3.0] based on recent
// realized volatility and direction confidence. Pure and deterministic.
//
//   - base = 1.0
//   - higher confidence raises leverage (up to +2.0)
//   - higher volatility lowers leverage
//   - invalid inputs (NaN, Inf, negative) default to 1.0
//   - result is HARD clamped to [1.0, 3.0] and never exceeds 3.0
func ComputeLeverage(realizedVol, confidence float64) float64 {
	const (
		base       = 1.0
		maxLev     = 3.0
		minLev     = 1.0
		confGain   = 2.0  // confidence in [0,1] contributes up to +2.0
		volPenalty = 40.0 // realized vol (as fraction, e.g. 0.01 = 1%) penalizes
	)

	// Guard invalid inputs
	if math.IsNaN(realizedVol) || math.IsInf(realizedVol, 0) || realizedVol < 0 {
		return minLev
	}
	if math.IsNaN(confidence) || math.IsInf(confidence, 0) {
		return minLev
	}
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}

	lev := base + confidence*confGain - realizedVol*volPenalty

	// Hard clamp
	if lev < minLev {
		return minLev
	}
	if lev > maxLev {
		return maxLev
	}
	return lev
}
