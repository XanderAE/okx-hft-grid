package strategy

import (
	"sort"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirements 3.6, 3.7**

// TestProperty_GridOrderDirectionConsistency verifies that for any grid layout with a given
// current price, all orders below the current price are BUY orders, all orders above are
// SELL orders, and no order is placed at the current price level.
func TestProperty_GridOrderDirectionConsistency(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate at least 4 random grid levels (sorted ascending, unique)
		numLevels := rapid.IntRange(4, 30).Draw(t, "numLevels")

		// Generate levels as unique sorted prices
		levelSet := make(map[string]bool)
		levels := make([]decimal.Decimal, 0, numLevels)
		for len(levels) < numLevels {
			// Generate price in cents to avoid duplicates easily
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

		// Generate current price within the range [levels[0], levels[last]]
		// Pick a random value between min and max level
		minLevel := levels[0]
		maxLevel := levels[len(levels)-1]
		priceRange := maxLevel.Sub(minLevel)

		// Generate a random position within the range (0-10000 basis points)
		bps := rapid.IntRange(0, 10000).Draw(t, "priceBps")
		currentPrice := minLevel.Add(priceRange.Mul(decimal.NewFromInt(int64(bps))).Div(decimal.NewFromInt(10000)))

		// Create a minimal grid config
		config := &models.GridConfig{
			Symbol:    "TEST-USDT",
			OrderSize: decimal.NewFromFloat(1.0),
		}

		// Call PlaceGridOrders
		orders := PlaceGridOrders(levels, currentPrice, config)

		// Verify Property 5: Grid Order Direction Consistency
		for _, order := range orders {
			// 1. All buy orders have price < currentPrice
			if order.Side == models.SideBuy {
				if !order.Price.LessThan(currentPrice) {
					t.Fatalf("BUY order at price %s is NOT below currentPrice %s",
						order.Price, currentPrice)
				}
			}

			// 2. All sell orders have price > currentPrice
			if order.Side == models.SideSell {
				if !order.Price.GreaterThan(currentPrice) {
					t.Fatalf("SELL order at price %s is NOT above currentPrice %s",
						order.Price, currentPrice)
				}
			}

			// 3. No order at currentPrice level
			if order.Price.Equal(currentPrice) {
				t.Fatalf("order placed at currentPrice %s, which should not happen", currentPrice)
			}

			// 4. All orders use POST_ONLY type
			if order.OrderType != models.OrderTypePostOnly {
				t.Fatalf("order at price %s uses type %s, expected POST_ONLY",
					order.Price, order.OrderType)
			}
		}
	})
}
