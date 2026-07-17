package strategy

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirements 4.1, 4.2, 4.6**
//
// Property 7: Grid Fill Counter-Order Placement
// "For any BUY fill at grid level[i], if i < len(levels)-1, a SELL counter order SHALL be placed at level[i+1].
//  For any SELL fill at grid level[i], if i > 0, a BUY counter order SHALL be placed at level[i-1]."

// genGridStateAndConfig generates a random valid grid state (5-20 levels) and matching config.
func genGridStateAndConfig(t *rapid.T) (*GridState, *models.GridConfig) {
	numLevels := rapid.IntRange(5, 20).Draw(t, "numLevels")

	// Generate ascending price levels
	baseCents := rapid.IntRange(100, 10000).Draw(t, "baseCents") // base price in cents
	stepCents := rapid.IntRange(10, 500).Draw(t, "stepCents")    // step between levels in cents

	levels := make([]models.GridLevel, numLevels)
	for i := 0; i < numLevels; i++ {
		price := decimal.NewFromInt(int64(baseCents + i*stepCents)).Div(decimal.NewFromInt(100))
		levels[i] = models.GridLevel{
			Index: i,
			Price: price,
		}
	}

	// Fee rate: 0 to keep profit check from interfering with counter order placement logic
	// We use zero fee to ensure the property about counter order placement is tested cleanly
	feeRate := decimal.Zero

	// Max position: large enough to never hit the limit
	maxPosition := decimal.NewFromInt(100000)

	// Order size
	orderSize := decimal.NewFromInt(int64(rapid.IntRange(1, 100).Draw(t, "orderSize")))

	config := &models.GridConfig{
		Symbol:      "TEST-USDT",
		LowerPrice:  levels[0].Price,
		UpperPrice:  levels[numLevels-1].Price,
		GridCount:   numLevels - 1,
		GridType:    models.GridTypeArithmetic,
		OrderSize:   orderSize,
		MaxPosition: maxPosition,
		FeeRate:     feeRate,
	}

	state := &GridState{
		Levels:        levels,
		Position:      decimal.Zero,
		AvgEntryPrice: decimal.Zero,
		RealizedPnL:   decimal.Zero,
		TotalBuys:     0,
		TotalSells:    0,
	}

	return state, config
}

// TestPropertyBuyFillCounterSellAtNextLevel verifies that a BUY fill at a non-highest level
// produces a counter SELL order at level[i+1].
func TestPropertyBuyFillCounterSellAtNextLevel(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		state, config := genGridStateAndConfig(t)

		// Pick a random level that is NOT the highest (so counter is possible)
		maxIdx := len(state.Levels) - 2 // highest valid index for counter order
		fillLevel := rapid.IntRange(0, maxIdx).Draw(t, "fillLevel")

		// Generate a fill quantity
		qtyCents := rapid.IntRange(1, 1000).Draw(t, "qtyCents")
		qty := decimal.NewFromInt(int64(qtyCents)).Div(decimal.NewFromInt(100))

		fill := models.FillEvent{
			Symbol:    config.Symbol,
			Side:      models.SideBuy,
			Price:     state.Levels[fillLevel].Price,
			Quantity:  qty,
			GridLevel: fillLevel,
		}

		result := HandleGridFill(fill, state, config)

		// Should have no error
		if result.Error != nil {
			t.Fatalf("unexpected error: %v", result.Error)
		}

		// Counter order should exist
		if result.CounterOrder == nil {
			t.Fatalf("expected counter SELL order at level %d+1, but got nil (reason: %s)",
				fillLevel, result.Reason)
		}

		// Counter order must be SELL
		if result.CounterOrder.Side != models.SideSell {
			t.Fatalf("counter order side = %s, want SELL", result.CounterOrder.Side)
		}

		// Counter order price must be at level[i+1]
		expectedPrice := state.Levels[fillLevel+1].Price
		if !result.CounterOrder.Price.Equal(expectedPrice) {
			t.Fatalf("counter order price = %s, want %s (level[%d])",
				result.CounterOrder.Price, expectedPrice, fillLevel+1)
		}

		// Counter order quantity should match fill quantity
		if !result.CounterOrder.Quantity.Equal(qty) {
			t.Fatalf("counter order quantity = %s, want %s",
				result.CounterOrder.Quantity, qty)
		}
	})
}

// TestPropertySellFillCounterBuyAtPreviousLevel verifies that a SELL fill at a non-lowest level
// produces a counter BUY order at level[i-1].
func TestPropertySellFillCounterBuyAtPreviousLevel(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		state, config := genGridStateAndConfig(t)

		// Pick a random level that is NOT the lowest (so counter is possible)
		fillLevel := rapid.IntRange(1, len(state.Levels)-1).Draw(t, "fillLevel")

		// Generate a fill quantity
		qtyCents := rapid.IntRange(1, 1000).Draw(t, "qtyCents")
		qty := decimal.NewFromInt(int64(qtyCents)).Div(decimal.NewFromInt(100))

		// Set up state so that position limit won't be hit and avg entry is set
		// (for PnL calculation to work, we need a positive avg entry price)
		state.Position = qty.Mul(decimal.NewFromInt(2))
		state.AvgEntryPrice = state.Levels[fillLevel].Price

		fill := models.FillEvent{
			Symbol:    config.Symbol,
			Side:      models.SideSell,
			Price:     state.Levels[fillLevel].Price,
			Quantity:  qty,
			GridLevel: fillLevel,
		}

		result := HandleGridFill(fill, state, config)

		// Should have no error
		if result.Error != nil {
			t.Fatalf("unexpected error: %v", result.Error)
		}

		// Counter order should exist
		if result.CounterOrder == nil {
			t.Fatalf("expected counter BUY order at level %d-1, but got nil (reason: %s)",
				fillLevel, result.Reason)
		}

		// Counter order must be BUY
		if result.CounterOrder.Side != models.SideBuy {
			t.Fatalf("counter order side = %s, want BUY", result.CounterOrder.Side)
		}

		// Counter order price must be at level[i-1]
		expectedPrice := state.Levels[fillLevel-1].Price
		if !result.CounterOrder.Price.Equal(expectedPrice) {
			t.Fatalf("counter order price = %s, want %s (level[%d])",
				result.CounterOrder.Price, expectedPrice, fillLevel-1)
		}

		// Counter order quantity should match fill quantity
		if !result.CounterOrder.Quantity.Equal(qty) {
			t.Fatalf("counter order quantity = %s, want %s",
				result.CounterOrder.Quantity, qty)
		}
	})
}

// TestPropertyBuyFillAtHighestLevelNoCounter verifies that a BUY fill at the highest grid level
// does NOT produce a counter order.
func TestPropertyBuyFillAtHighestLevelNoCounter(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		state, config := genGridStateAndConfig(t)

		// Fill at the highest level
		highestLevel := len(state.Levels) - 1

		qtyCents := rapid.IntRange(1, 1000).Draw(t, "qtyCents")
		qty := decimal.NewFromInt(int64(qtyCents)).Div(decimal.NewFromInt(100))

		fill := models.FillEvent{
			Symbol:    config.Symbol,
			Side:      models.SideBuy,
			Price:     state.Levels[highestLevel].Price,
			Quantity:  qty,
			GridLevel: highestLevel,
		}

		result := HandleGridFill(fill, state, config)

		// Should have no error
		if result.Error != nil {
			t.Fatalf("unexpected error: %v", result.Error)
		}

		// No counter order should be placed
		if result.CounterOrder != nil {
			t.Fatalf("expected no counter order for BUY at highest level, but got %+v", result.CounterOrder)
		}
	})
}

// TestPropertySellFillAtLowestLevelNoCounter verifies that a SELL fill at the lowest grid level
// does NOT produce a counter order.
func TestPropertySellFillAtLowestLevelNoCounter(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		state, config := genGridStateAndConfig(t)

		// Fill at the lowest level (index 0)
		lowestLevel := 0

		qtyCents := rapid.IntRange(1, 1000).Draw(t, "qtyCents")
		qty := decimal.NewFromInt(int64(qtyCents)).Div(decimal.NewFromInt(100))

		// Set up state so we have position to sell
		state.Position = qty.Mul(decimal.NewFromInt(2))
		state.AvgEntryPrice = state.Levels[lowestLevel].Price

		fill := models.FillEvent{
			Symbol:    config.Symbol,
			Side:      models.SideSell,
			Price:     state.Levels[lowestLevel].Price,
			Quantity:  qty,
			GridLevel: lowestLevel,
		}

		result := HandleGridFill(fill, state, config)

		// Should have no error
		if result.Error != nil {
			t.Fatalf("unexpected error: %v", result.Error)
		}

		// No counter order should be placed
		if result.CounterOrder != nil {
			t.Fatalf("expected no counter order for SELL at lowest level, but got %+v", result.CounterOrder)
		}
	})
}
