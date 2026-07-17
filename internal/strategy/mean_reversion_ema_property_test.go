package strategy

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// **Validates: Requirement 5.2**

// TestPropertyEMABoundedness verifies that for any sequence of prices,
// the EMA SHALL always be within [MIN(prices), MAX(prices)].
func TestPropertyEMABoundedness(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate random price sequence of length 5-100
		n := rapid.IntRange(5, 100).Draw(t, "numPrices")
		prices := make([]decimal.Decimal, n)
		for i := 0; i < n; i++ {
			// Generate prices as positive integers in cents (1..9999999) to avoid zero/negative
			cents := rapid.IntRange(1, 9999999).Draw(t, "priceCents")
			prices[i] = decimal.NewFromInt(int64(cents)).Div(decimal.NewFromInt(100))
		}

		// Generate random lookbackPeriod in [2, 50]
		lookbackPeriod := rapid.IntRange(2, 50).Draw(t, "lookbackPeriod")

		// Compute EMA
		ema := CalculateEMA(prices, lookbackPeriod)

		// Find min and max of prices
		minPrice := prices[0]
		maxPrice := prices[0]
		for _, p := range prices[1:] {
			if p.LessThan(minPrice) {
				minPrice = p
			}
			if p.GreaterThan(maxPrice) {
				maxPrice = p
			}
		}

		// Verify EMA is within [min, max]
		if ema.LessThan(minPrice) {
			t.Fatalf("EMA %v is less than min price %v (lookback=%d, len=%d)",
				ema, minPrice, lookbackPeriod, n)
		}
		if ema.GreaterThan(maxPrice) {
			t.Fatalf("EMA %v is greater than max price %v (lookback=%d, len=%d)",
				ema, maxPrice, lookbackPeriod, n)
		}
	})
}

// TestPropertyEMAEqualPrices verifies that when all prices are equal,
// the EMA equals that constant price.
func TestPropertyEMAEqualPrices(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a random constant price
		cents := rapid.IntRange(1, 9999999).Draw(t, "priceCents")
		constPrice := decimal.NewFromInt(int64(cents)).Div(decimal.NewFromInt(100))

		// Generate a random sequence length in [5, 100]
		n := rapid.IntRange(5, 100).Draw(t, "numPrices")
		prices := make([]decimal.Decimal, n)
		for i := 0; i < n; i++ {
			prices[i] = constPrice
		}

		// Generate random lookbackPeriod in [2, 50]
		lookbackPeriod := rapid.IntRange(2, 50).Draw(t, "lookbackPeriod")

		// Compute EMA
		ema := CalculateEMA(prices, lookbackPeriod)

		// EMA of equal prices must equal that price
		if !ema.Equal(constPrice) {
			t.Fatalf("EMA of equal prices: expected %v, got %v (lookback=%d, len=%d)",
				constPrice, ema, lookbackPeriod, n)
		}
	})
}
