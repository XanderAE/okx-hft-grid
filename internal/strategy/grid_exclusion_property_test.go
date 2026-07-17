package strategy

import (
	"sort"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirements 3.8**

// TestProperty_GridLevelMutualExclusion verifies that for any grid state, no grid level
// simultaneously has both a BUY order and a SELL order. Each price level in the output
// of PlaceGridOrders must appear exclusively in one side (buy OR sell), never both.
func TestProperty_GridLevelMutualExclusion(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate random grid levels (sorted ascending, unique)
		numLevels := rapid.IntRange(3, 50).Draw(t, "numLevels")

		levelSet := make(map[string]bool)
		levels := make([]decimal.Decimal, 0, numLevels)
		for len(levels) < numLevels {
			cents := rapid.IntRange(1, 1000000).Draw(t, "levelCents")
			price := decimal.NewFromInt(int64(cents)).Div(decimal.NewFromInt(100))
			if levelSet[price.String()] {
				continue
			}
			levelSet[price.String()] = true
			levels = append(levels, price)
		}

		// Sort levels ascending
		sort.Slice(levels, func(i, j int) bool {
			return levels[i].LessThan(levels[j])
		})

		// Generate current price within the grid range
		minLevel := levels[0]
		maxLevel := levels[len(levels)-1]
		priceRange := maxLevel.Sub(minLevel)

		// Random position within the range (0-10000 basis points)
		bps := rapid.IntRange(0, 10000).Draw(t, "priceBps")
		currentPrice := minLevel.Add(priceRange.Mul(decimal.NewFromInt(int64(bps))).Div(decimal.NewFromInt(10000)))

		// Create grid config
		config := &models.GridConfig{
			Symbol:    "TEST-USDT",
			OrderSize: decimal.NewFromFloat(1.0),
		}

		// Call PlaceGridOrders
		orders := PlaceGridOrders(levels, currentPrice, config)

		// Track which levels have buy orders and which have sell orders
		buyLevels := make(map[string]bool)
		sellLevels := make(map[string]bool)

		for _, order := range orders {
			priceKey := order.Price.String()
			if order.Side == models.SideBuy {
				buyLevels[priceKey] = true
			} else if order.Side == models.SideSell {
				sellLevels[priceKey] = true
			}
		}

		// Verify no level appears in both sets
		for priceKey := range buyLevels {
			if sellLevels[priceKey] {
				t.Fatalf("grid level at price %s has BOTH a BUY and a SELL order (mutual exclusion violated)", priceKey)
			}
		}

		for priceKey := range sellLevels {
			if buyLevels[priceKey] {
				t.Fatalf("grid level at price %s has BOTH a BUY and a SELL order (mutual exclusion violated)", priceKey)
			}
		}
	})
}
