// Property 15: Risk Check Daily Loss Limit
// **Validates: Requirement 7.3**
//
// When dailyPnL reaches or exceeds -maxDailyLoss, all subsequent orders are
// rejected until the next day (PnL reset).
package risk

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// TestProperty_DailyLoss_ExceedingLossRejectsAllOrders verifies that when the
// cumulative daily PnL drops below -maxDailyLoss, all subsequent orders are rejected.
func TestProperty_DailyLoss_ExceedingLossRejectsAllOrders(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxDailyLoss := rapid.Float64Range(100.0, 10000.0).Draw(t, "maxDailyLoss")

		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(999999),
			MaxTotalPosition:     decimal.NewFromFloat(999999),
			MaxDailyLoss:         decimal.NewFromFloat(maxDailyLoss),
			MaxOrdersPerSecond:   1000,
			MaxOpenOrders:        1000,
			MinSpreadBps:         1,
		}

		rm := NewRiskManager(limits)

		// Set PnL that exceeds the loss limit (total PnL < -maxDailyLoss)
		numStrategies := rapid.IntRange(1, 5).Draw(t, "numStrategies")
		totalPnL := 0.0
		for i := 0; i < numStrategies; i++ {
			loss := rapid.Float64Range(maxDailyLoss/float64(numStrategies), maxDailyLoss).Draw(t, "loss")
			strategyPnL := -loss
			rm.UpdatePnL("strategy-"+string(rune('A'+i)), decimal.NewFromFloat(strategyPnL))
			totalPnL += strategyPnL
		}

		// Ensure total PnL is worse than the limit
		if totalPnL >= -maxDailyLoss {
			// Push it over the limit
			extraLoss := maxDailyLoss + 1.0
			rm.UpdatePnL("strategy-extra", decimal.NewFromFloat(-extraLoss))
		}

		// Any subsequent order should be rejected
		orderPrice := rapid.Float64Range(1.0, 1000.0).Draw(t, "orderPrice")
		orderQty := rapid.Float64Range(0.001, 10.0).Draw(t, "orderQty")
		symbol := rapid.SampledFrom([]string{"BTC-USDT", "ETH-USDT", "SOL-USDT"}).Draw(t, "symbol")

		order := &OrderRequest{
			Symbol:    symbol,
			Side:      models.SideBuy,
			OrderType: models.OrderTypeLimit,
			Price:     decimal.NewFromFloat(orderPrice),
			Quantity:  decimal.NewFromFloat(orderQty),
			SpreadBps: 100,
		}

		decision := rm.CheckOrder(order)

		if decision.Approved {
			t.Fatalf("order should be rejected when daily PnL exceeds -maxDailyLoss (%f)", maxDailyLoss)
		}

		// Verify the rejection mentions daily loss
		found := false
		for _, reason := range decision.Reasons {
			if containsStr(reason, "daily loss") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("rejection should mention daily loss limit, got: %v", decision.Reasons)
		}
	})
}

// TestProperty_DailyLoss_WithinLimitAllowsOrders verifies that when the cumulative
// daily PnL is above -maxDailyLoss, orders are not rejected due to loss limit.
func TestProperty_DailyLoss_WithinLimitAllowsOrders(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxDailyLoss := rapid.Float64Range(100.0, 10000.0).Draw(t, "maxDailyLoss")

		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(999999),
			MaxTotalPosition:     decimal.NewFromFloat(999999),
			MaxDailyLoss:         decimal.NewFromFloat(maxDailyLoss),
			MaxOrdersPerSecond:   1000,
			MaxOpenOrders:        1000,
			MinSpreadBps:         1,
		}

		rm := NewRiskManager(limits)

		// Set PnL within acceptable range (> -maxDailyLoss)
		pnl := rapid.Float64Range(-maxDailyLoss+0.01, maxDailyLoss).Draw(t, "pnl")
		rm.UpdatePnL("strategy-A", decimal.NewFromFloat(pnl))

		// Order should be allowed (no daily loss rejection)
		order := &OrderRequest{
			Symbol:    "BTC-USDT",
			Side:      models.SideBuy,
			OrderType: models.OrderTypeLimit,
			Price:     decimal.NewFromFloat(100.0),
			Quantity:  decimal.NewFromFloat(0.01),
			SpreadBps: 100,
		}

		decision := rm.CheckOrder(order)

		// Check that daily loss is not a reason for rejection
		for _, reason := range decision.Reasons {
			if containsStr(reason, "daily loss") {
				t.Fatalf("order should NOT be rejected for daily loss when PnL=%f > -%f, reasons: %v",
					pnl, maxDailyLoss, decision.Reasons)
			}
		}
	})
}

// TestProperty_DailyLoss_ResetReenablesOrders verifies that after resetting
// daily PnL (simulating next day), orders that were rejected are now approved.
func TestProperty_DailyLoss_ResetReenablesOrders(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxDailyLoss := rapid.Float64Range(100.0, 5000.0).Draw(t, "maxDailyLoss")

		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(999999),
			MaxTotalPosition:     decimal.NewFromFloat(999999),
			MaxDailyLoss:         decimal.NewFromFloat(maxDailyLoss),
			MaxOrdersPerSecond:   1000,
			MaxOpenOrders:        1000,
			MinSpreadBps:         1,
		}

		rm := NewRiskManager(limits)

		// Exceed the loss limit
		rm.UpdatePnL("strategy-A", decimal.NewFromFloat(-(maxDailyLoss + 1)))

		order := &OrderRequest{
			Symbol:    "BTC-USDT",
			Side:      models.SideBuy,
			OrderType: models.OrderTypeLimit,
			Price:     decimal.NewFromFloat(100.0),
			Quantity:  decimal.NewFromFloat(0.01),
			SpreadBps: 100,
		}

		// Should be rejected
		decision := rm.CheckOrder(order)
		if decision.Approved {
			t.Fatal("order should be rejected before PnL reset")
		}

		// Reset daily PnL (simulating next day)
		rm.ResetDailyPnL()

		// Should be approved now
		decision = rm.CheckOrder(order)
		if !decision.Approved {
			t.Fatalf("order should be approved after PnL reset, got: %v", decision.Reasons)
		}
	})
}
