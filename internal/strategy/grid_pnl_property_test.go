package strategy

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirement 4.4**
// Property 33: Grid Realized PnL Calculation
//
// For any SELL fill, realizedPnL SHALL be updated as:
//   realizedPnL += (fill_price - avgEntryPrice) * quantity
// The cumulative realizedPnL SHALL equal the sum of all individual PnL contributions.

// TestPropertyGridRealizedPnL generates a sequence of BUY fills (building position)
// followed by SELL fills, verifying that each SELL correctly updates RealizedPnL
// and the cumulative PnL equals the sum of individual contributions.
func TestPropertyGridRealizedPnL(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate grid config with enough levels
		numLevels := rapid.IntRange(5, 20).Draw(t, "numLevels")

		levels := make([]models.GridLevel, numLevels)
		basePrice := decimal.NewFromInt(100)
		for i := 0; i < numLevels; i++ {
			levels[i] = models.GridLevel{
				Index: i,
				Price: basePrice.Add(decimal.NewFromInt(int64(i * 10))),
			}
		}

		config := &models.GridConfig{
			Symbol:      "ETH-USDT",
			LowerPrice:  levels[0].Price,
			UpperPrice:  levels[numLevels-1].Price,
			GridCount:   numLevels - 1,
			GridType:    models.GridTypeArithmetic,
			OrderSize:   decimal.NewFromInt(1),
			MaxPosition: decimal.NewFromInt(10000),
			FeeRate:     decimal.Zero, // Zero fee to isolate PnL logic
		}

		state := &GridState{
			Levels:        levels,
			Position:      decimal.Zero,
			AvgEntryPrice: decimal.Zero,
			RealizedPnL:   decimal.Zero,
		}

		// Generate 1-10 BUY fills to build position
		numBuys := rapid.IntRange(1, 10).Draw(t, "numBuys")

		for i := 0; i < numBuys; i++ {
			// Buy at a level in the lower half of the grid
			buyLevel := rapid.IntRange(0, numLevels-2).Draw(t, "buyLevel")
			// Generate a random quantity in cents to avoid zero
			qtyCents := rapid.IntRange(1, 1000).Draw(t, "buyQtyCents")
			qty := decimal.NewFromInt(int64(qtyCents)).Div(decimal.NewFromInt(100))

			// Generate a random buy price near the level price
			priceCents := rapid.IntRange(50, 500).Draw(t, "buyPriceCents")
			buyPrice := decimal.NewFromInt(int64(priceCents)).Div(decimal.NewFromInt(10))

			fill := models.FillEvent{
				Symbol:    "ETH-USDT",
				Side:      models.SideBuy,
				Price:     buyPrice,
				Quantity:  qty,
				GridLevel: buyLevel,
			}

			HandleGridFill(fill, state, config)
		}

		// Record state after buys
		positionAfterBuys := state.Position

		// If no position was built, skip the sell phase
		if positionAfterBuys.IsZero() {
			return
		}

		// Generate SELL fills - ensure total sell quantity doesn't exceed position
		numSells := rapid.IntRange(1, 5).Draw(t, "numSells")

		// Track individual PnL contributions
		var pnlContributions []decimal.Decimal
		cumulativePnL := decimal.Zero
		remainingPosition := positionAfterBuys

		for i := 0; i < numSells; i++ {
			if remainingPosition.IsZero() {
				break
			}

			// Sell at a level in the upper half of the grid (must be > 0 for boundary check)
			sellLevel := rapid.IntRange(1, numLevels-1).Draw(t, "sellLevel")

			// Quantity: a portion of remaining position
			maxQtyCents := remainingPosition.Mul(decimal.NewFromInt(100)).IntPart()
			if maxQtyCents <= 0 {
				break
			}
			if maxQtyCents > 1000 {
				maxQtyCents = 1000
			}
			qtyCents := rapid.IntRange(1, int(maxQtyCents)).Draw(t, "sellQtyCents")
			qty := decimal.NewFromInt(int64(qtyCents)).Div(decimal.NewFromInt(100))

			// Generate sell price
			priceCents := rapid.IntRange(50, 500).Draw(t, "sellPriceCents")
			sellPrice := decimal.NewFromInt(int64(priceCents)).Div(decimal.NewFromInt(10))

			// Record the expected PnL for this sell
			currentAvgEntry := state.AvgEntryPrice
			expectedPnL := sellPrice.Sub(currentAvgEntry).Mul(qty)
			pnlContributions = append(pnlContributions, expectedPnL)

			// Record PnL before this fill
			pnlBefore := state.RealizedPnL

			fill := models.FillEvent{
				Symbol:    "ETH-USDT",
				Side:      models.SideSell,
				Price:     sellPrice,
				Quantity:  qty,
				GridLevel: sellLevel,
			}

			result := HandleGridFill(fill, state, config)

			// Verify: RealizedPnL increased by exactly (fill_price - avgEntryPrice) * quantity
			actualIncrease := result.RealizedPnL.Sub(pnlBefore)
			if !actualIncrease.Equal(expectedPnL) {
				t.Fatalf("sell %d: PnL increase mismatch: got %s, expected %s (sellPrice=%s, avgEntry=%s, qty=%s)",
					i, actualIncrease.String(), expectedPnL.String(),
					sellPrice.String(), currentAvgEntry.String(), qty.String())
			}

			cumulativePnL = cumulativePnL.Add(expectedPnL)
			remainingPosition = remainingPosition.Sub(qty)

			// After position goes to zero, avgEntryPrice resets so we stop
			if state.Position.IsZero() {
				break
			}
		}

		// Verify cumulative PnL equals sum of all individual contributions
		expectedCumulative := decimal.Zero
		for _, c := range pnlContributions {
			expectedCumulative = expectedCumulative.Add(c)
		}

		// The state's RealizedPnL should equal the cumulative sum (since we started from zero)
		if !state.RealizedPnL.Equal(expectedCumulative) {
			t.Fatalf("cumulative PnL mismatch: state.RealizedPnL=%s, sum of contributions=%s",
				state.RealizedPnL.String(), expectedCumulative.String())
		}
	})
}

// TestPropertyGridRealizedPnLAdditivity verifies that the cumulative realized PnL
// is always exactly the sum of individual (fill_price - avgEntryPrice) * qty for each sell.
// This uses a simpler approach with a fixed buy price to make avgEntryPrice predictable.
func TestPropertyGridRealizedPnLAdditivity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		numLevels := 10
		levels := make([]models.GridLevel, numLevels)
		for i := 0; i < numLevels; i++ {
			levels[i] = models.GridLevel{
				Index: i,
				Price: decimal.NewFromInt(int64(100 + i*10)),
			}
		}

		config := &models.GridConfig{
			Symbol:      "ETH-USDT",
			LowerPrice:  levels[0].Price,
			UpperPrice:  levels[numLevels-1].Price,
			GridCount:   numLevels - 1,
			GridType:    models.GridTypeArithmetic,
			OrderSize:   decimal.NewFromInt(1),
			MaxPosition: decimal.NewFromInt(10000),
			FeeRate:     decimal.Zero,
		}

		state := &GridState{
			Levels:        levels,
			Position:      decimal.Zero,
			AvgEntryPrice: decimal.Zero,
			RealizedPnL:   decimal.Zero,
		}

		// Single BUY to establish a known avgEntryPrice
		buyPriceCents := rapid.IntRange(100, 5000).Draw(t, "buyPriceCents")
		buyPrice := decimal.NewFromInt(int64(buyPriceCents)).Div(decimal.NewFromInt(100))
		// Large enough quantity to allow multiple sells
		totalQtyCents := rapid.IntRange(100, 1000).Draw(t, "totalQtyCents")
		totalQty := decimal.NewFromInt(int64(totalQtyCents)).Div(decimal.NewFromInt(100))

		buyFill := models.FillEvent{
			Symbol:    "ETH-USDT",
			Side:      models.SideBuy,
			Price:     buyPrice,
			Quantity:  totalQty,
			GridLevel: 0,
		}
		HandleGridFill(buyFill, state, config)

		// avgEntryPrice should now equal buyPrice exactly
		if !state.AvgEntryPrice.Equal(buyPrice) {
			t.Fatalf("avgEntryPrice not set correctly: got %s, want %s",
				state.AvgEntryPrice.String(), buyPrice.String())
		}

		// Generate multiple SELL fills, each taking a portion
		numSells := rapid.IntRange(1, 5).Draw(t, "numSells")
		remainingQty := totalQty
		individualPnLs := make([]decimal.Decimal, 0, numSells)

		for i := 0; i < numSells; i++ {
			if remainingQty.IsZero() || state.Position.IsZero() {
				break
			}

			// Take a portion of remaining
			maxCents := remainingQty.Mul(decimal.NewFromInt(100)).IntPart()
			if maxCents <= 0 {
				break
			}
			if maxCents > 500 {
				maxCents = 500
			}
			sellQtyCents := rapid.IntRange(1, int(maxCents)).Draw(t, "sellQtyCents")
			sellQty := decimal.NewFromInt(int64(sellQtyCents)).Div(decimal.NewFromInt(100))

			sellPriceCents := rapid.IntRange(100, 5000).Draw(t, "sellPriceCents")
			sellPrice := decimal.NewFromInt(int64(sellPriceCents)).Div(decimal.NewFromInt(100))

			// Expected PnL for this sell
			expectedPnL := sellPrice.Sub(state.AvgEntryPrice).Mul(sellQty)

			pnlBefore := state.RealizedPnL

			sellFill := models.FillEvent{
				Symbol:    "ETH-USDT",
				Side:      models.SideSell,
				Price:     sellPrice,
				Quantity:  sellQty,
				GridLevel: 1, // Any valid level > 0
			}
			HandleGridFill(sellFill, state, config)

			// Verify individual increase
			actualIncrease := state.RealizedPnL.Sub(pnlBefore)
			if !actualIncrease.Equal(expectedPnL) {
				t.Fatalf("sell %d: expected PnL increase %s, got %s",
					i, expectedPnL.String(), actualIncrease.String())
			}

			individualPnLs = append(individualPnLs, expectedPnL)
			remainingQty = remainingQty.Sub(sellQty)
		}

		// Verify cumulative equals sum of individual
		sum := decimal.Zero
		for _, p := range individualPnLs {
			sum = sum.Add(p)
		}
		if !state.RealizedPnL.Equal(sum) {
			t.Fatalf("cumulative PnL %s != sum of individual PnLs %s",
				state.RealizedPnL.String(), sum.String())
		}
	})
}
