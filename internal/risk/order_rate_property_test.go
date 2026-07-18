// Property 16: Risk Check Order Rate Limit
// **Validates: Requirement 7.4**
//
// In any 1-second sliding window, the number of approved orders does not
// exceed maxOrdersPerSecond.
package risk

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// TestProperty_OrderRate_ApprovedCountNeverExceedsLimit verifies that within
// any burst of orders submitted instantly, the risk manager never approves
// more than maxOrdersPerSecond orders in the same 1-second window.
func TestProperty_OrderRate_ApprovedCountNeverExceedsLimit(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxOrdersPerSec := rapid.IntRange(1, 20).Draw(t, "maxOrdersPerSec")

		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(999999),
			MaxTotalPosition:     decimal.NewFromFloat(999999),
			MaxDailyLoss:         decimal.NewFromFloat(999999),
			MaxOrdersPerSecond:   maxOrdersPerSec,
			MaxOpenOrders:        1000,
			MinSpreadBps:         1,
		}

		rm := NewRiskManager(limits)

		// Submit more orders than allowed in a burst
		totalOrders := rapid.IntRange(maxOrdersPerSec+1, maxOrdersPerSec*3).Draw(t, "totalOrders")
		approvedCount := 0

		for i := 0; i < totalOrders; i++ {
			order := &OrderRequest{
				Symbol:    "BTC-USDT",
				Side:      models.SideBuy,
				OrderType: models.OrderTypeLimit,
				Price:     decimal.NewFromFloat(100.0),
				Quantity:  decimal.NewFromFloat(0.001), // tiny to avoid position limits
				SpreadBps: 100,
			}

			decision := rm.CheckOrder(order)
			if decision.Approved {
				approvedCount++
			}
		}

		// The approved count should not exceed the rate limit
		if approvedCount > maxOrdersPerSec {
			t.Fatalf("approved %d orders in 1s window, exceeds maxOrdersPerSecond=%d",
				approvedCount, maxOrdersPerSec)
		}
	})
}

// TestProperty_OrderRate_AfterWindowExpiryNewOrdersAllowed verifies that after
// the 1-second window passes, new orders are approved again.
// Note: This test uses time.Sleep so we limit iterations to keep runtime reasonable.
func TestProperty_OrderRate_AfterWindowExpiryNewOrdersAllowed(t *testing.T) {
	// Use a single parameterized run to avoid 100 × 1.1s = 110s runtime
	maxOrdersPerSec := 3

	limits := &models.RiskLimits{
		MaxPositionPerSymbol: decimal.NewFromFloat(999999),
		MaxTotalPosition:     decimal.NewFromFloat(999999),
		MaxDailyLoss:         decimal.NewFromFloat(999999),
		MaxOrdersPerSecond:   maxOrdersPerSec,
		MaxOpenOrders:        1000,
		MinSpreadBps:         1,
	}

	rm := NewRiskManager(limits)

	// Fill the rate window
	for i := 0; i < maxOrdersPerSec; i++ {
		order := &OrderRequest{
			Symbol:    "BTC-USDT",
			Side:      models.SideBuy,
			OrderType: models.OrderTypeLimit,
			Price:     decimal.NewFromFloat(100.0),
			Quantity:  decimal.NewFromFloat(0.001),
			SpreadBps: 100,
		}
		rm.CheckOrder(order)
	}

	// Next order should be rejected
	order := &OrderRequest{
		Symbol:    "BTC-USDT",
		Side:      models.SideBuy,
		OrderType: models.OrderTypeLimit,
		Price:     decimal.NewFromFloat(100.0),
		Quantity:  decimal.NewFromFloat(0.001),
		SpreadBps: 100,
	}

	decision := rm.CheckOrder(order)
	if decision.Approved {
		t.Fatal("order should be rejected when rate limit is reached")
	}

	// Wait for the window to expire
	time.Sleep(1100 * time.Millisecond)

	// Now a new order should be approved
	decision = rm.CheckOrder(order)
	if !decision.Approved {
		t.Fatalf("order should be approved after 1s window expires, got: %v", decision.Reasons)
	}
}

// TestProperty_OrderRate_ExactlyAtLimitStillApproved verifies that exactly
// maxOrdersPerSecond orders can be approved (boundary condition).
func TestProperty_OrderRate_ExactlyAtLimitStillApproved(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxOrdersPerSec := rapid.IntRange(1, 15).Draw(t, "maxOrdersPerSec")

		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(999999),
			MaxTotalPosition:     decimal.NewFromFloat(999999),
			MaxDailyLoss:         decimal.NewFromFloat(999999),
			MaxOrdersPerSecond:   maxOrdersPerSec,
			MaxOpenOrders:        1000,
			MinSpreadBps:         1,
		}

		rm := NewRiskManager(limits)

		// Submit exactly maxOrdersPerSec orders — all should be approved
		approvedCount := 0
		for i := 0; i < maxOrdersPerSec; i++ {
			order := &OrderRequest{
				Symbol:    "BTC-USDT",
				Side:      models.SideBuy,
				OrderType: models.OrderTypeLimit,
				Price:     decimal.NewFromFloat(100.0),
				Quantity:  decimal.NewFromFloat(0.001),
				SpreadBps: 100,
			}

			decision := rm.CheckOrder(order)
			if decision.Approved {
				approvedCount++
			}
		}

		if approvedCount != maxOrdersPerSec {
			t.Fatalf("expected exactly %d approved orders at the limit, got %d",
				maxOrdersPerSec, approvedCount)
		}
	})
}
