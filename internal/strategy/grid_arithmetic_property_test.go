package strategy

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirement 3.1**

// TestProperty_GridArithmeticEqualIntervals verifies that for any arithmetic grid,
// the difference between adjacent levels is constant (within decimal precision tolerance of 1e-10).
func TestProperty_GridArithmeticEqualIntervals(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate random valid arithmetic GridConfig:
		// - positive prices, upper > lower
		// - gridCount in [3, 500]
		lowerCents := rapid.IntRange(1, 999999).Draw(t, "lowerCents")
		spreadCents := rapid.IntRange(1, 999999).Draw(t, "spreadCents")
		upperCents := lowerCents + spreadCents

		lower := decimal.NewFromInt(int64(lowerCents)).Div(decimal.NewFromInt(100))
		upper := decimal.NewFromInt(int64(upperCents)).Div(decimal.NewFromInt(100))

		gridCount := rapid.IntRange(3, 500).Draw(t, "gridCount")

		config := &models.GridConfig{
			LowerPrice: lower,
			UpperPrice: upper,
			GridCount:  gridCount,
			GridType:   models.GridTypeArithmetic,
		}

		// Compute levels
		levels, err := CalculateGridLevels(config)
		if err != nil {
			t.Fatalf("CalculateGridLevels returned error: %v", err)
		}

		// Verify we have the expected number of levels
		if len(levels) != gridCount+1 {
			t.Fatalf("expected %d levels, got %d", gridCount+1, len(levels))
		}

		// Verify all intervals level[i+1] - level[i] are equal (tolerance 1e-10)
		tolerance := decimal.NewFromFloat(1e-10)
		expectedStep := upper.Sub(lower).Div(decimal.NewFromInt(int64(gridCount)))

		for i := 0; i < len(levels)-1; i++ {
			interval := levels[i+1].Sub(levels[i])
			diff := interval.Sub(expectedStep).Abs()
			if diff.GreaterThan(tolerance) {
				t.Fatalf("interval[%d] = %s, expected step = %s, diff = %s exceeds tolerance 1e-10",
					i, interval, expectedStep, diff)
			}
		}
	})
}
