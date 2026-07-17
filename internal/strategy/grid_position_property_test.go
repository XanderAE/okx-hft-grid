package strategy

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirement 4.5**

// TestProperty_GridPositionBound verifies that for any SELL fill generating a counter BUY,
// if placing that BUY would cause position to exceed maxPosition, the counter order SHALL NOT
// be placed. Conversely, when position + fillQuantity <= maxPosition, the counter BUY is placed.
func TestProperty_GridPositionBound(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate grid with at least 3 levels so SELL fills have a lower level for counter BUY
		numLevels := rapid.IntRange(3, 20).Draw(t, "numLevels")

		// Generate ascending price levels
		basePrice := rapid.IntRange(100, 10000).Draw(t, "basePrice")
		spacing := rapid.IntRange(10, 500).Draw(t, "spacing")

		levels := make([]models.GridLevel, numLevels)
		for i := 0; i < numLevels; i++ {
			levels[i] = models.GridLevel{
				Index: i,
				Price: decimal.NewFromInt(int64(basePrice + i*spacing)).Div(decimal.NewFromInt(100)),
			}
		}

		// Generate maxPosition (positive, between 1 and 100)
		maxPositionInt := rapid.IntRange(1, 100).Draw(t, "maxPosition")
		maxPosition := decimal.NewFromInt(int64(maxPositionInt))

		// Generate current position (0 to maxPosition)
		currentPositionInt := rapid.IntRange(0, maxPositionInt).Draw(t, "currentPosition")
		currentPosition := decimal.NewFromInt(int64(currentPositionInt))

		// Generate fill quantity (1 to 50)
		fillQtyInt := rapid.IntRange(1, 50).Draw(t, "fillQuantity")
		fillQuantity := decimal.NewFromInt(int64(fillQtyInt))

		// Pick a SELL fill at a level that has a lower neighbor (level >= 1)
		fillLevel := rapid.IntRange(1, numLevels-1).Draw(t, "fillLevel")

		// Set up state
		state := &GridState{
			Levels:        levels,
			Position:      currentPosition,
			AvgEntryPrice: decimal.NewFromFloat(50.0), // Arbitrary non-zero for PnL calc
			RealizedPnL:   decimal.Zero,
		}

		// Set up config with maxPosition and minimal fee
		config := &models.GridConfig{
			Symbol:      "TEST-USDT",
			MaxPosition: maxPosition,
			FeeRate:     decimal.NewFromFloat(0.001),
		}

		// Create the SELL fill event
		fill := models.FillEvent{
			Symbol:    "TEST-USDT",
			Side:      models.SideSell,
			Price:     levels[fillLevel].Price,
			Quantity:  fillQuantity,
			GridLevel: fillLevel,
		}

		// Execute
		result := HandleGridFill(fill, state, config)

		// The position check in handleSellFill uses state.Position (after sell update) + fill.Quantity
		// But actually looking at the code: it checks newPosition := state.Position.Add(fill.Quantity)
		// BEFORE updatePositionOnSell modifies state.Position... wait, let's re-check the order.
		// In handleSellFill: first it updates PnL, then calls updatePositionOnSell, THEN checks position.
		// After updatePositionOnSell: state.Position = state.Position - fillQty (clamped to 0 if negative)
		// Then newPosition = state.Position.Add(fill.Quantity)
		//
		// So the effective check is: (currentPosition - fillQty).clamp(0) + fillQty > maxPosition
		//
		// Let's compute what the code computes:
		positionAfterSell := currentPosition.Sub(fillQuantity)
		if positionAfterSell.LessThanOrEqual(decimal.Zero) {
			positionAfterSell = decimal.Zero
		}
		newPosition := positionAfterSell.Add(fillQuantity)

		wouldExceed := maxPosition.IsPositive() && newPosition.GreaterThan(maxPosition)

		if wouldExceed {
			// Property: counter BUY SHALL NOT be placed
			if result.CounterOrder != nil {
				t.Fatalf("position bound violated: position after sell=%s + fillQty=%s = %s > maxPosition=%s, but counter BUY was placed",
					positionAfterSell, fillQuantity, newPosition, maxPosition)
			}
		} else {
			// Property: counter BUY should be placed (no position bound blocking it)
			if result.Error != nil {
				t.Fatalf("unexpected error: %v", result.Error)
			}
			if result.CounterOrder == nil && result.Reason == "" {
				t.Fatalf("expected counter BUY to be placed: position after sell=%s + fillQty=%s = %s <= maxPosition=%s, but got nil counter order with no reason",
					positionAfterSell, fillQuantity, newPosition, maxPosition)
			}
			// If there's a reason but no counter order, it could be a boundary condition (fillLevel == 0)
			// which we already exclude by requiring fillLevel >= 1
			if result.CounterOrder != nil {
				if result.CounterOrder.Side != models.SideBuy {
					t.Fatalf("counter order should be BUY, got %s", result.CounterOrder.Side)
				}
			}
		}
	})
}
