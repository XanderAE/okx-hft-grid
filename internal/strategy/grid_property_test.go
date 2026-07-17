package strategy

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirements 3.3, 3.4, 3.5**

// genGridConfig generates a valid GridConfig with randomized prices and grid count.
func genGridConfig(t *rapid.T) *models.GridConfig {
	// Generate lower price as cents to avoid zero/negative values
	lowerCents := rapid.IntRange(1, 999999).Draw(t, "lowerCents")
	lower := decimal.NewFromInt(int64(lowerCents)).Div(decimal.NewFromInt(100))

	// Generate upper price strictly greater than lower
	extraCents := rapid.IntRange(1, 999999).Draw(t, "extraCents")
	upper := lower.Add(decimal.NewFromInt(int64(extraCents)).Div(decimal.NewFromInt(100)))

	// Grid count between 1 and 500
	gridCount := rapid.IntRange(1, 500).Draw(t, "gridCount")

	// Randomly choose arithmetic or geometric
	gridTypeInt := rapid.IntRange(0, 1).Draw(t, "gridType")
	gridType := models.GridType(gridTypeInt)

	return &models.GridConfig{
		LowerPrice: lower,
		UpperPrice: upper,
		GridCount:  gridCount,
		GridType:   gridType,
	}
}

// TestPropertyGridLevelCount verifies that CalculateGridLevels returns exactly gridCount+1 levels
// for any valid GridConfig.
func TestPropertyGridLevelCount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		config := genGridConfig(t)

		levels, err := CalculateGridLevels(config)
		if err != nil {
			t.Fatalf("unexpected error for valid config: %v", err)
		}

		expected := config.GridCount + 1
		if len(levels) != expected {
			t.Fatalf("expected %d levels, got %d", expected, len(levels))
		}
	})
}

// TestPropertyGridLevelMonotonicity verifies that all levels are strictly ascending
// (level[i] < level[i+1] for all i).
func TestPropertyGridLevelMonotonicity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		config := genGridConfig(t)

		levels, err := CalculateGridLevels(config)
		if err != nil {
			t.Fatalf("unexpected error for valid config: %v", err)
		}

		for i := 1; i < len(levels); i++ {
			if levels[i].LessThanOrEqual(levels[i-1]) {
				t.Fatalf("levels not strictly ascending at index %d: %s <= %s",
					i, levels[i].String(), levels[i-1].String())
			}
		}
	})
}

// TestPropertyGridFirstLevelEqualsLower verifies that the first level always equals lowerPrice.
func TestPropertyGridFirstLevelEqualsLower(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		config := genGridConfig(t)

		levels, err := CalculateGridLevels(config)
		if err != nil {
			t.Fatalf("unexpected error for valid config: %v", err)
		}

		if !levels[0].Equal(config.LowerPrice) {
			t.Fatalf("first level %s != lowerPrice %s",
				levels[0].String(), config.LowerPrice.String())
		}
	})
}

// TestPropertyGridLastLevelApproxUpper verifies that the last level approximately equals upperPrice
// (within 1e-9 × priceRange tolerance).
func TestPropertyGridLastLevelApproxUpper(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		config := genGridConfig(t)

		levels, err := CalculateGridLevels(config)
		if err != nil {
			t.Fatalf("unexpected error for valid config: %v", err)
		}

		lastLevel := levels[len(levels)-1]
		priceRange := config.UpperPrice.Sub(config.LowerPrice)
		tolerance := priceRange.Mul(decimal.NewFromFloat(1e-9))
		diff := lastLevel.Sub(config.UpperPrice).Abs()

		if diff.GreaterThan(tolerance) {
			t.Fatalf("last level %s deviates from upperPrice %s by %s (tolerance %s)",
				lastLevel.String(), config.UpperPrice.String(), diff.String(), tolerance.String())
		}
	})
}
