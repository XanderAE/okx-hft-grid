package strategy

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirement 4.3**

// TestProperty_GridProfitGuarantee verifies that for any counter SELL order placed after a BUY fill,
// the expected net profit (sell_price - buy_price) * quantity SHALL exceed the double-sided trading fees.
// If it doesn't, the counter order SHALL NOT be placed.
func TestProperty_GridProfitGuarantee(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate fee rate between 0.0001 (0.01%) and 0.05 (5%)
		feeRateBps := rapid.IntRange(1, 500).Draw(t, "feeRateBps")
		feeRate := decimal.NewFromInt(int64(feeRateBps)).Div(decimal.NewFromInt(10000))

		// Generate grid with at least 2 levels so we can have a buy and a counter sell level
		numLevels := rapid.IntRange(3, 20).Draw(t, "numLevels")

		// Generate a base price for the lowest level
		basePriceCents := rapid.IntRange(100, 100000).Draw(t, "basePriceCents")
		basePrice := decimal.NewFromInt(int64(basePriceCents)).Div(decimal.NewFromInt(100))

		// Generate level spacing in cents (can be small to test tight spreads that fail profit check)
		spacingCents := rapid.IntRange(1, 5000).Draw(t, "spacingCents")
		spacing := decimal.NewFromInt(int64(spacingCents)).Div(decimal.NewFromInt(100))

		// Build grid levels with strictly ascending prices
		levels := make([]models.GridLevel, numLevels)
		for i := 0; i < numLevels; i++ {
			levels[i] = models.GridLevel{
				Index: i,
				Price: basePrice.Add(spacing.Mul(decimal.NewFromInt(int64(i)))),
			}
		}

		// Pick a random buy fill level that has a next higher level for a counter sell
		buyLevel := rapid.IntRange(0, numLevels-2).Draw(t, "buyLevel")

		// Generate order quantity
		qtyCents := rapid.IntRange(1, 100000).Draw(t, "qtyCents")
		quantity := decimal.NewFromInt(int64(qtyCents)).Div(decimal.NewFromInt(1000))

		// Build config
		config := &models.GridConfig{
			Symbol:      "TEST-USDT",
			FeeRate:     feeRate,
			MaxPosition: decimal.NewFromInt(999999), // Large enough to not interfere
			OrderSize:   quantity,
		}

		// Build state
		state := &GridState{
			Levels:   levels,
			Position: decimal.Zero,
		}

		// Create fill event
		fill := models.FillEvent{
			Symbol:    "TEST-USDT",
			Side:      models.SideBuy,
			Price:     levels[buyLevel].Price,
			Quantity:  quantity,
			GridLevel: buyLevel,
		}

		// Execute
		result := HandleGridFill(fill, state, config)

		// Calculate expected profit and fees for this scenario
		sellPrice := levels[buyLevel+1].Price
		buyPrice := levels[buyLevel].Price
		expectedProfit := sellPrice.Sub(buyPrice).Mul(quantity)
		buyNotional := quantity.Mul(buyPrice)
		sellNotional := quantity.Mul(sellPrice)
		fees := buyNotional.Add(sellNotional).Mul(feeRate)

		// Property assertion:
		if result.CounterOrder != nil {
			// Counter SELL was placed -> expected profit MUST exceed fees
			if expectedProfit.LessThanOrEqual(fees) {
				t.Fatalf("counter SELL placed but profit %s <= fees %s "+
					"(buyPrice=%s, sellPrice=%s, qty=%s, feeRate=%s)",
					expectedProfit, fees,
					buyPrice, sellPrice, quantity, feeRate)
			}

			// Verify the counter order is a SELL at the next level
			if result.CounterOrder.Side != models.SideSell {
				t.Fatalf("expected counter order side SELL, got %s", result.CounterOrder.Side)
			}
			if !result.CounterOrder.Price.Equal(sellPrice) {
				t.Fatalf("expected counter order price %s, got %s",
					sellPrice, result.CounterOrder.Price)
			}
		} else {
			// Counter SELL was NOT placed (and no error) -> expected profit MUST be <= fees
			if result.Error == nil {
				if expectedProfit.GreaterThan(fees) {
					t.Fatalf("counter SELL not placed but profit %s > fees %s "+
						"(buyPrice=%s, sellPrice=%s, qty=%s, feeRate=%s)",
						expectedProfit, fees,
						buyPrice, sellPrice, quantity, feeRate)
				}
			}
		}
	})
}

// TestProperty_GridProfitGuarantee_NeverNegativeProfit is a secondary property verifying that
// if a counter SELL order IS placed, the gross profit always exceeds fees (no money-losing trades).
func TestProperty_GridProfitGuarantee_NeverNegativeProfit(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate fee rate between 0.0001 (0.01%) and 0.05 (5%)
		feeRateBps := rapid.IntRange(1, 500).Draw(t, "feeRateBps")
		feeRate := decimal.NewFromInt(int64(feeRateBps)).Div(decimal.NewFromInt(10000))

		// Generate grid with moderate number of levels
		numLevels := rapid.IntRange(3, 50).Draw(t, "numLevels")

		// Generate base price
		basePriceCents := rapid.IntRange(100, 500000).Draw(t, "basePriceCents")
		basePrice := decimal.NewFromInt(int64(basePriceCents)).Div(decimal.NewFromInt(100))

		// Generate spacing using a wider range to ensure some scenarios pass profit check
		spacingCents := rapid.IntRange(1, 10000).Draw(t, "spacingCents")
		spacing := decimal.NewFromInt(int64(spacingCents)).Div(decimal.NewFromInt(100))

		// Build grid levels
		levels := make([]models.GridLevel, numLevels)
		for i := 0; i < numLevels; i++ {
			levels[i] = models.GridLevel{
				Index: i,
				Price: basePrice.Add(spacing.Mul(decimal.NewFromInt(int64(i)))),
			}
		}

		// Pick a buy fill level
		buyLevel := rapid.IntRange(0, numLevels-2).Draw(t, "buyLevel")

		// Generate quantity
		qtyCents := rapid.IntRange(1, 50000).Draw(t, "qtyCents")
		quantity := decimal.NewFromInt(int64(qtyCents)).Div(decimal.NewFromInt(1000))

		config := &models.GridConfig{
			Symbol:      "TEST-USDT",
			FeeRate:     feeRate,
			MaxPosition: decimal.NewFromInt(999999),
			OrderSize:   quantity,
		}

		state := &GridState{
			Levels:   levels,
			Position: decimal.Zero,
		}

		fill := models.FillEvent{
			Symbol:    "TEST-USDT",
			Side:      models.SideBuy,
			Price:     levels[buyLevel].Price,
			Quantity:  quantity,
			GridLevel: buyLevel,
		}

		result := HandleGridFill(fill, state, config)

		// Only verify if a counter order was actually placed
		if result.CounterOrder != nil && result.Error == nil {
			sellPrice := result.CounterOrder.Price
			buyPrice := fill.Price
			grossProfit := sellPrice.Sub(buyPrice).Mul(quantity)
			buyNotional := quantity.Mul(buyPrice)
			sellNotional := quantity.Mul(sellPrice)
			totalFees := buyNotional.Add(sellNotional).Mul(feeRate)

			netProfit := grossProfit.Sub(totalFees)
			if netProfit.LessThanOrEqual(decimal.Zero) {
				t.Fatalf("counter SELL placed with non-positive net profit: "+
					"grossProfit=%s, fees=%s, net=%s (buy=%s, sell=%s, qty=%s, feeRate=%s)",
					grossProfit, totalFees, netProfit,
					buyPrice, sellPrice, quantity, feeRate)
			}
		}
	})
}
