// Property 14: Risk Check Position Limit
// **Validates: Requirements 7.1, 7.2**
//
// For any order that passes risk checks, the post-order position exposure
// does not exceed maxPositionPerSymbol, and the total portfolio exposure
// does not exceed maxTotalPosition.
package risk

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// TestProperty_PositionLimit_ApprovedOrderWithinSymbolLimit verifies that for any
// order approved by the risk manager, the post-order per-symbol position exposure
// does not exceed maxPositionPerSymbol.
func TestProperty_PositionLimit_ApprovedOrderWithinSymbolLimit(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate risk limits with meaningful bounds
		maxPosSym := rapid.Float64Range(1000.0, 100000.0).Draw(t, "maxPosSym")
		maxTotalPos := rapid.Float64Range(maxPosSym, maxPosSym*10).Draw(t, "maxTotalPos")

		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(maxPosSym),
			MaxTotalPosition:     decimal.NewFromFloat(maxTotalPos),
			MaxDailyLoss:         decimal.NewFromFloat(999999), // large to avoid triggering
			MaxOrdersPerSecond:   1000,                         // large to avoid triggering
			MaxOpenOrders:        1000,                         // large to avoid triggering
			MinSpreadBps:         1,                            // small to avoid triggering
		}

		rm := NewRiskManager(limits)

		// Generate existing position for the symbol (could be zero)
		existingQty := rapid.Float64Range(0, maxPosSym/2).Draw(t, "existingQty")
		existingPrice := rapid.Float64Range(1.0, 1000.0).Draw(t, "existingPrice")
		symbol := "BTC-USDT"

		rm.UpdatePosition(symbol, &models.Position{
			Symbol:        symbol,
			Quantity:      decimal.NewFromFloat(existingQty),
			AvgEntryPrice: decimal.NewFromFloat(existingPrice),
		})

		// Generate an order
		orderPrice := rapid.Float64Range(1.0, 10000.0).Draw(t, "orderPrice")
		orderQty := rapid.Float64Range(0.001, maxPosSym/orderPrice).Draw(t, "orderQty")

		order := &OrderRequest{
			Symbol:    symbol,
			Side:      models.SideBuy,
			OrderType: models.OrderTypeLimit,
			Price:     decimal.NewFromFloat(orderPrice),
			Quantity:  decimal.NewFromFloat(orderQty),
			SpreadBps: 100, // well above any minSpreadBps
		}

		decision := rm.CheckOrder(order)

		if decision.Approved {
			// Verify the property: post-order notional <= maxPositionPerSymbol
			existingNotional := decimal.NewFromFloat(existingQty).Mul(decimal.NewFromFloat(existingPrice))
			orderNotional := decimal.NewFromFloat(orderPrice).Mul(decimal.NewFromFloat(orderQty))
			postOrderNotional := existingNotional.Add(orderNotional).Abs()

			maxPosDecimal := decimal.NewFromFloat(maxPosSym)
			if postOrderNotional.GreaterThan(maxPosDecimal) {
				t.Fatalf("approved order would exceed per-symbol limit: "+
					"postOrderNotional=%s > maxPositionPerSymbol=%s",
					postOrderNotional.String(), maxPosDecimal.String())
			}
		}
		// If rejected, the property is trivially satisfied
	})
}

// TestProperty_PositionLimit_ApprovedOrderWithinTotalExposure verifies that for any
// order approved by the risk manager, the post-order total portfolio exposure
// does not exceed maxTotalPosition.
func TestProperty_PositionLimit_ApprovedOrderWithinTotalExposure(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate risk limits
		maxTotalPos := rapid.Float64Range(5000.0, 200000.0).Draw(t, "maxTotalPos")
		maxPosSym := rapid.Float64Range(1000.0, maxTotalPos).Draw(t, "maxPosSym")

		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(maxPosSym),
			MaxTotalPosition:     decimal.NewFromFloat(maxTotalPos),
			MaxDailyLoss:         decimal.NewFromFloat(999999),
			MaxOrdersPerSecond:   1000,
			MaxOpenOrders:        1000,
			MinSpreadBps:         1,
		}

		rm := NewRiskManager(limits)

		// Generate existing positions across multiple symbols
		numSymbols := rapid.IntRange(0, 3).Draw(t, "numSymbols")
		symbols := []string{"ETH-USDT", "SOL-USDT", "DOGE-USDT"}
		totalExistingExposure := decimal.Zero

		for i := 0; i < numSymbols; i++ {
			qty := rapid.Float64Range(0.1, maxTotalPos/10).Draw(t, "posQty")
			price := rapid.Float64Range(1.0, 500.0).Draw(t, "posPrice")
			rm.UpdatePosition(symbols[i], &models.Position{
				Symbol:        symbols[i],
				Quantity:      decimal.NewFromFloat(qty),
				AvgEntryPrice: decimal.NewFromFloat(price),
			})
			totalExistingExposure = totalExistingExposure.Add(
				decimal.NewFromFloat(qty).Mul(decimal.NewFromFloat(price)).Abs())
		}

		// Generate order for a new symbol
		orderPrice := rapid.Float64Range(1.0, 5000.0).Draw(t, "orderPrice")
		orderQty := rapid.Float64Range(0.001, 100.0).Draw(t, "orderQty")

		order := &OrderRequest{
			Symbol:    "BTC-USDT",
			Side:      models.SideBuy,
			OrderType: models.OrderTypeLimit,
			Price:     decimal.NewFromFloat(orderPrice),
			Quantity:  decimal.NewFromFloat(orderQty),
			SpreadBps: 100,
		}

		decision := rm.CheckOrder(order)

		if decision.Approved {
			// Verify the property: total exposure + order notional <= maxTotalPosition
			orderNotional := decimal.NewFromFloat(orderPrice).Mul(decimal.NewFromFloat(orderQty)).Abs()
			postTotalExposure := totalExistingExposure.Add(orderNotional)

			maxTotalDecimal := decimal.NewFromFloat(maxTotalPos)
			if postTotalExposure.GreaterThan(maxTotalDecimal) {
				t.Fatalf("approved order would exceed total exposure limit: "+
					"postTotalExposure=%s > maxTotalPosition=%s",
					postTotalExposure.String(), maxTotalDecimal.String())
			}
		}
	})
}

// TestProperty_PositionLimit_ExceedingLimitAlwaysRejected verifies that an order whose
// post-order position clearly exceeds the limit is always rejected.
func TestProperty_PositionLimit_ExceedingLimitAlwaysRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxPosSym := rapid.Float64Range(1000.0, 50000.0).Draw(t, "maxPosSym")

		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(maxPosSym),
			MaxTotalPosition:     decimal.NewFromFloat(maxPosSym * 100), // very large, won't trigger
			MaxDailyLoss:         decimal.NewFromFloat(999999),
			MaxOrdersPerSecond:   1000,
			MaxOpenOrders:        1000,
			MinSpreadBps:         1,
		}

		rm := NewRiskManager(limits)

		// Order that clearly exceeds the per-symbol limit
		orderPrice := rapid.Float64Range(10.0, 1000.0).Draw(t, "orderPrice")
		// Ensure order notional > maxPosSym
		minQty := maxPosSym/orderPrice + 1
		orderQty := rapid.Float64Range(minQty, minQty*2).Draw(t, "orderQty")

		order := &OrderRequest{
			Symbol:    "BTC-USDT",
			Side:      models.SideBuy,
			OrderType: models.OrderTypeLimit,
			Price:     decimal.NewFromFloat(orderPrice),
			Quantity:  decimal.NewFromFloat(orderQty),
			SpreadBps: 100,
		}

		decision := rm.CheckOrder(order)

		if decision.Approved {
			orderNotional := decimal.NewFromFloat(orderPrice).Mul(decimal.NewFromFloat(orderQty))
			t.Fatalf("order with notional %s should be rejected (limit=%s)",
				orderNotional.String(), decimal.NewFromFloat(maxPosSym).String())
		}
	})
}
