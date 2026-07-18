// Property 32: Risk Check Open Orders and Spread Limits
// **Validates: Requirements 7.5, 7.6**
//
// When a symbol's open order count reaches maxOpenOrders or the spread is
// less than minSpreadBps, the order is rejected.
package risk

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// TestProperty_OpenOrders_AtLimitRejectsNewOrders verifies that when a symbol's
// open order count equals or exceeds maxOpenOrders, new orders for that symbol
// are rejected.
func TestProperty_OpenOrders_AtLimitRejectsNewOrders(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxOpenOrders := rapid.IntRange(1, 50).Draw(t, "maxOpenOrders")

		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(999999),
			MaxTotalPosition:     decimal.NewFromFloat(999999),
			MaxDailyLoss:         decimal.NewFromFloat(999999),
			MaxOrdersPerSecond:   1000,
			MaxOpenOrders:        maxOpenOrders,
			MinSpreadBps:         1, // small to isolate the open orders check
		}

		rm := NewRiskManager(limits)

		symbol := rapid.SampledFrom([]string{"BTC-USDT", "ETH-USDT", "SOL-USDT"}).Draw(t, "symbol")

		// Set the open order count to exactly maxOpenOrders
		currentCount := rapid.IntRange(maxOpenOrders, maxOpenOrders+10).Draw(t, "currentCount")
		rm.UpdateOpenOrderCount(symbol, currentCount)

		order := &OrderRequest{
			Symbol:    symbol,
			Side:      models.SideBuy,
			OrderType: models.OrderTypeLimit,
			Price:     decimal.NewFromFloat(100.0),
			Quantity:  decimal.NewFromFloat(0.01),
			SpreadBps: 100, // well above minSpreadBps
		}

		decision := rm.CheckOrder(order)

		if decision.Approved {
			t.Fatalf("order should be rejected when open orders (%d) >= maxOpenOrders (%d)",
				currentCount, maxOpenOrders)
		}

		found := false
		for _, reason := range decision.Reasons {
			if containsStr(reason, "open order limit") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("rejection should mention open order limit, got: %v", decision.Reasons)
		}
	})
}

// TestProperty_OpenOrders_BelowLimitAllowsOrders verifies that when a symbol's
// open order count is below maxOpenOrders, orders are not rejected for this reason.
func TestProperty_OpenOrders_BelowLimitAllowsOrders(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxOpenOrders := rapid.IntRange(2, 50).Draw(t, "maxOpenOrders")

		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(999999),
			MaxTotalPosition:     decimal.NewFromFloat(999999),
			MaxDailyLoss:         decimal.NewFromFloat(999999),
			MaxOrdersPerSecond:   1000,
			MaxOpenOrders:        maxOpenOrders,
			MinSpreadBps:         1,
		}

		rm := NewRiskManager(limits)

		symbol := "BTC-USDT"
		currentCount := rapid.IntRange(0, maxOpenOrders-1).Draw(t, "currentCount")
		rm.UpdateOpenOrderCount(symbol, currentCount)

		order := &OrderRequest{
			Symbol:    symbol,
			Side:      models.SideBuy,
			OrderType: models.OrderTypeLimit,
			Price:     decimal.NewFromFloat(100.0),
			Quantity:  decimal.NewFromFloat(0.01),
			SpreadBps: 100,
		}

		decision := rm.CheckOrder(order)

		// Should not be rejected for open order limit
		for _, reason := range decision.Reasons {
			if containsStr(reason, "open order limit") {
				t.Fatalf("order should NOT be rejected for open orders when count (%d) < max (%d), reasons: %v",
					currentCount, maxOpenOrders, decision.Reasons)
			}
		}
	})
}

// TestProperty_Spread_BelowMinimumRejectsOrder verifies that when the order's
// spread is below minSpreadBps, the order is rejected.
func TestProperty_Spread_BelowMinimumRejectsOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		minSpreadBps := rapid.IntRange(2, 100).Draw(t, "minSpreadBps")

		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(999999),
			MaxTotalPosition:     decimal.NewFromFloat(999999),
			MaxDailyLoss:         decimal.NewFromFloat(999999),
			MaxOrdersPerSecond:   1000,
			MaxOpenOrders:        1000,
			MinSpreadBps:         minSpreadBps,
		}

		rm := NewRiskManager(limits)

		// Generate a spread that's below the minimum
		spreadBps := rapid.IntRange(0, minSpreadBps-1).Draw(t, "spreadBps")

		order := &OrderRequest{
			Symbol:    "BTC-USDT",
			Side:      models.SideBuy,
			OrderType: models.OrderTypeLimit,
			Price:     decimal.NewFromFloat(100.0),
			Quantity:  decimal.NewFromFloat(0.01),
			SpreadBps: spreadBps,
		}

		decision := rm.CheckOrder(order)

		if decision.Approved {
			t.Fatalf("order should be rejected when spread (%d bps) < minSpreadBps (%d)",
				spreadBps, minSpreadBps)
		}

		found := false
		for _, reason := range decision.Reasons {
			if containsStr(reason, "spread too narrow") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("rejection should mention spread too narrow, got: %v", decision.Reasons)
		}
	})
}

// TestProperty_Spread_AtOrAboveMinimumAllowsOrder verifies that when the order's
// spread is at or above minSpreadBps, the order is not rejected for this reason.
func TestProperty_Spread_AtOrAboveMinimumAllowsOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		minSpreadBps := rapid.IntRange(1, 50).Draw(t, "minSpreadBps")

		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(999999),
			MaxTotalPosition:     decimal.NewFromFloat(999999),
			MaxDailyLoss:         decimal.NewFromFloat(999999),
			MaxOrdersPerSecond:   1000,
			MaxOpenOrders:        1000,
			MinSpreadBps:         minSpreadBps,
		}

		rm := NewRiskManager(limits)

		// Generate spread at or above minimum
		spreadBps := rapid.IntRange(minSpreadBps, minSpreadBps+100).Draw(t, "spreadBps")

		order := &OrderRequest{
			Symbol:    "BTC-USDT",
			Side:      models.SideBuy,
			OrderType: models.OrderTypeLimit,
			Price:     decimal.NewFromFloat(100.0),
			Quantity:  decimal.NewFromFloat(0.01),
			SpreadBps: spreadBps,
		}

		decision := rm.CheckOrder(order)

		// Should not be rejected for spread
		for _, reason := range decision.Reasons {
			if containsStr(reason, "spread too narrow") {
				t.Fatalf("order should NOT be rejected for spread when bps (%d) >= min (%d), reasons: %v",
					spreadBps, minSpreadBps, decision.Reasons)
			}
		}
	})
}
