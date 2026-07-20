package strategy

import (
	"math"
	"testing"

	"pgregory.net/rapid"
)

// **Validates: Requirements 3.1, 3.4**
//
// Property: For ANY realizedVol and confidence input, ComputeLeverage always
// returns a value in the closed range [1.0, 3.0] and never exceeds 3.0.
func TestProperty_ComputeLeverage_AlwaysInRange(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		realizedVol := rapid.Float64Range(-1, 100).Draw(t, "realizedVol")
		confidence := rapid.Float64Range(-5, 5).Draw(t, "confidence")

		lev := ComputeLeverage(realizedVol, confidence)

		if lev < 1.0 || lev > 3.0 {
			t.Fatalf("ComputeLeverage(%v, %v) = %v, want in [1.0, 3.0]", realizedVol, confidence, lev)
		}
	})
}

// **Validates: Requirements 3.1, 3.4**
//
// Explicit NaN/Inf cases: ComputeLeverage must never produce out-of-range or
// non-finite output for special float values, and invalid inputs default to 1.0.
func TestComputeLeverage_SpecialValues(t *testing.T) {
	specials := []float64{
		math.NaN(),
		math.Inf(1),
		math.Inf(-1),
		-1.0,
		0.0,
		0.5,
		1.0,
		1e18,
		-1e18,
	}

	for _, rv := range specials {
		for _, c := range specials {
			lev := ComputeLeverage(rv, c)
			if math.IsNaN(lev) || math.IsInf(lev, 0) {
				t.Fatalf("ComputeLeverage(%v, %v) = %v, want finite", rv, c, lev)
			}
			if lev < 1.0 || lev > 3.0 {
				t.Fatalf("ComputeLeverage(%v, %v) = %v, want in [1.0, 3.0]", rv, c, lev)
			}
		}
	}
}

// Unit test: high volatility drives leverage down to the 1.0 floor.
func TestComputeLeverage_HighVolatilityFloorsAtOne(t *testing.T) {
	lev := ComputeLeverage(0.1, 1.0)
	if lev != 1.0 {
		t.Fatalf("ComputeLeverage(0.1, 1.0) = %v, want 1.0 (high vol should floor leverage)", lev)
	}
}

// Unit test: high confidence with zero volatility drives leverage toward the
// 3.0 ceiling.
func TestComputeLeverage_HighConfidenceLowVolCeilingsAtThree(t *testing.T) {
	lev := ComputeLeverage(0.0, 1.0)
	if lev != 3.0 {
		t.Fatalf("ComputeLeverage(0.0, 1.0) = %v, want 3.0 (high confidence + low vol)", lev)
	}
}

// Unit test: NaN input defaults to 1.0.
func TestComputeLeverage_NaNDefaultsToOne(t *testing.T) {
	if lev := ComputeLeverage(math.NaN(), 1.0); lev != 1.0 {
		t.Fatalf("ComputeLeverage(NaN, 1.0) = %v, want 1.0", lev)
	}
	if lev := ComputeLeverage(0.0, math.NaN()); lev != 1.0 {
		t.Fatalf("ComputeLeverage(0.0, NaN) = %v, want 1.0", lev)
	}
}

// Unit test: negative volatility defaults to 1.0.
func TestComputeLeverage_NegativeVolDefaultsToOne(t *testing.T) {
	if lev := ComputeLeverage(-0.5, 1.0); lev != 1.0 {
		t.Fatalf("ComputeLeverage(-0.5, 1.0) = %v, want 1.0", lev)
	}
}

// Unit test: zero confidence and zero volatility yields the base leverage 1.0.
func TestComputeLeverage_ZeroConfidenceZeroVolIsBase(t *testing.T) {
	if lev := ComputeLeverage(0.0, 0.0); lev != 1.0 {
		t.Fatalf("ComputeLeverage(0.0, 0.0) = %v, want 1.0 (base)", lev)
	}
}
