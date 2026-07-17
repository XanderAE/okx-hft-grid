package strategy

import (
	"math"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirement 3.2**
// Property 4: Grid Geometric Equal Ratios
// For any geometric grid, the ratio between adjacent levels SHALL be equal (within floating-point tolerance).

func TestPropertyGridGeometricEqualRatios(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate random valid geometric GridConfig:
		// positive prices, upper > lower, gridCount in [3, 100]
		gridCount := rapid.IntRange(3, 100).Draw(t, "gridCount")

		// Generate lower price in a reasonable range (avoid extremely small values that cause precision issues)
		lowerCents := rapid.IntRange(1, 1000000).Draw(t, "lowerCents") // 0.01 to 10000.00
		lowerPrice := decimal.NewFromInt(int64(lowerCents)).Div(decimal.NewFromInt(100))

		// Generate a ratio factor > 1 to ensure upper > lower
		// Use a multiplier between 1.01 and 100.0 (upper/lower ratio)
		multiplierCents := rapid.IntRange(101, 10000).Draw(t, "multiplierCents") // 1.01x to 100.0x
		multiplier := decimal.NewFromInt(int64(multiplierCents)).Div(decimal.NewFromInt(100))

		upperPrice := lowerPrice.Mul(multiplier)

		config := &models.GridConfig{
			LowerPrice: lowerPrice,
			UpperPrice: upperPrice,
			GridCount:  gridCount,
			GridType:   models.GridTypeGeometric,
		}

		levels, err := CalculateGridLevels(config)
		if err != nil {
			t.Fatalf("unexpected error for valid config: %v", err)
		}

		// Must have gridCount+1 levels
		if len(levels) != gridCount+1 {
			t.Fatalf("expected %d levels, got %d", gridCount+1, len(levels))
		}

		// For geometric grid: all ratios level[i+1]/level[i] must be approximately equal
		// The expected ratio is (upperPrice / lowerPrice) ^ (1 / gridCount)
		lowerF := lowerPrice.InexactFloat64()
		upperF := upperPrice.InexactFloat64()
		expectedRatio := math.Pow(upperF/lowerF, 1.0/float64(gridCount))

		tolerance := 1e-8

		for i := 0; i < len(levels)-1; i++ {
			levelI := levels[i].InexactFloat64()
			levelNext := levels[i+1].InexactFloat64()

			if levelI <= 0 {
				t.Fatalf("level[%d] is non-positive: %v", i, levels[i])
			}

			actualRatio := levelNext / levelI
			diff := math.Abs(actualRatio - expectedRatio)

			if diff > tolerance {
				t.Fatalf("ratio level[%d+1]/level[%d] = %v, expected ≈ %v (diff=%v, tolerance=%v)",
					i, i, actualRatio, expectedRatio, diff, tolerance)
			}
		}
	})
}
