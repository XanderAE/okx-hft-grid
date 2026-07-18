// Property 17: Emergency Stop Irreversibility
// **Validates: Requirements 7.7, 7.8, 7.9**
//
// After Emergency_Stop is triggered, all trading operations are rejected until
// manual confirmation (ResumeFromEmergencyStop). Triggering invokes callbacks
// to cancel all orders, stop strategies, and send critical alert.
package risk

import (
	"sync"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// propertyMockEmergencyCallback tracks callback invocations for property test verification.
type propertyMockEmergencyCallback struct {
	mu              sync.Mutex
	cancelCalled    int
	stopCalled      int
	alertCalled     int
	lastAlertReason string
}

func (m *propertyMockEmergencyCallback) CancelAllOrders() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelCalled++
	return nil
}

func (m *propertyMockEmergencyCallback) StopAllStrategies() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopCalled++
	return nil
}

func (m *propertyMockEmergencyCallback) SendCriticalAlert(reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alertCalled++
	m.lastAlertReason = reason
	return nil
}

// TestProperty_EmergencyStop_AllOrdersRejectedWhileActive verifies that once
// EmergencyStop is triggered, all trading operations are rejected regardless
// of order parameters.
func TestProperty_EmergencyStop_AllOrdersRejectedWhileActive(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(999999),
			MaxTotalPosition:     decimal.NewFromFloat(999999),
			MaxDailyLoss:         decimal.NewFromFloat(999999),
			MaxOrdersPerSecond:   1000,
			MaxOpenOrders:        1000,
			MinSpreadBps:         1,
		}

		rm := NewRiskManager(limits)

		// Trigger emergency stop with an arbitrary reason
		reason := rapid.StringMatching(`[a-zA-Z ]{5,50}`).Draw(t, "reason")
		rm.EmergencyStop(reason)

		// Verify it's active
		if !rm.IsEmergencyStopActive() {
			t.Fatal("emergency stop should be active after triggering")
		}

		// Generate arbitrary orders - all should be rejected
		numOrders := rapid.IntRange(1, 10).Draw(t, "numOrders")
		for i := 0; i < numOrders; i++ {
			symbol := rapid.SampledFrom([]string{"BTC-USDT", "ETH-USDT", "SOL-USDT"}).Draw(t, "symbol")
			price := rapid.Float64Range(0.01, 100000.0).Draw(t, "price")
			qty := rapid.Float64Range(0.001, 1000.0).Draw(t, "qty")
			side := rapid.SampledFrom([]models.Side{models.SideBuy, models.SideSell}).Draw(t, "side")

			order := &OrderRequest{
				Symbol:    symbol,
				Side:      side,
				OrderType: models.OrderTypeLimit,
				Price:     decimal.NewFromFloat(price),
				Quantity:  decimal.NewFromFloat(qty),
				SpreadBps: 1000,
			}

			decision := rm.CheckOrder(order)
			if decision.Approved {
				t.Fatalf("order should be rejected during emergency stop: symbol=%s, price=%f", symbol, price)
			}

			// Verify reason mentions emergency stop
			found := false
			for _, r := range decision.Reasons {
				if containsStr(r, "emergency stop") {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("rejection should mention emergency stop, got: %v", decision.Reasons)
			}
		}
	})
}

// TestProperty_EmergencyStop_ResumeReenablesTrading verifies that after
// ResumeFromEmergencyStop is called, orders are no longer rejected due
// to emergency stop.
func TestProperty_EmergencyStop_ResumeReenablesTrading(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(999999),
			MaxTotalPosition:     decimal.NewFromFloat(999999),
			MaxDailyLoss:         decimal.NewFromFloat(999999),
			MaxOrdersPerSecond:   1000,
			MaxOpenOrders:        1000,
			MinSpreadBps:         1,
		}

		rm := NewRiskManager(limits)

		// Trigger and then resume
		reason := rapid.StringMatching(`[a-zA-Z ]{5,30}`).Draw(t, "reason")
		rm.EmergencyStop(reason)

		if !rm.IsEmergencyStopActive() {
			t.Fatal("expected emergency stop to be active")
		}

		err := rm.ResumeFromEmergencyStop()
		if err != nil {
			t.Fatalf("unexpected error resuming: %v", err)
		}

		if rm.IsEmergencyStopActive() {
			t.Fatal("expected emergency stop to be inactive after resume")
		}

		// Order should be approved (no emergency stop rejection)
		order := &OrderRequest{
			Symbol:    "BTC-USDT",
			Side:      models.SideBuy,
			OrderType: models.OrderTypeLimit,
			Price:     decimal.NewFromFloat(100.0),
			Quantity:  decimal.NewFromFloat(0.01),
			SpreadBps: 100,
		}

		decision := rm.CheckOrder(order)
		if !decision.Approved {
			// Check that emergency stop is NOT the reason
			for _, r := range decision.Reasons {
				if containsStr(r, "emergency stop") {
					t.Fatalf("order rejected for emergency stop after resume: %v", decision.Reasons)
				}
			}
		}
	})
}

// TestProperty_EmergencyStop_CallbacksInvokedOnTrigger verifies that when
// EmergencyStop triggers, all registered callbacks are invoked (cancel orders,
// stop strategies, send alert).
func TestProperty_EmergencyStop_CallbacksInvokedOnTrigger(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		esm := NewEmergencyStopManager(nil)

		cb := &propertyMockEmergencyCallback{}
		esm.RegisterEmergencyCallback(cb)

		reason := rapid.StringMatching(`[a-zA-Z ]{5,50}`).Draw(t, "reason")
		esm.EmergencyStop(reason)

		cb.mu.Lock()
		defer cb.mu.Unlock()

		if cb.cancelCalled != 1 {
			t.Fatalf("expected CancelAllOrders to be called once, got %d", cb.cancelCalled)
		}
		if cb.stopCalled != 1 {
			t.Fatalf("expected StopAllStrategies to be called once, got %d", cb.stopCalled)
		}
		if cb.alertCalled != 1 {
			t.Fatalf("expected SendCriticalAlert to be called once, got %d", cb.alertCalled)
		}
		if cb.lastAlertReason != reason {
			t.Fatalf("expected alert reason %q, got %q", reason, cb.lastAlertReason)
		}
	})
}

// TestProperty_EmergencyStop_IdempotentTrigger verifies that calling EmergencyStop
// multiple times does not invoke callbacks more than once (idempotent).
func TestProperty_EmergencyStop_IdempotentTrigger(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		esm := NewEmergencyStopManager(nil)

		cb := &propertyMockEmergencyCallback{}
		esm.RegisterEmergencyCallback(cb)

		reason := rapid.StringMatching(`[a-zA-Z]{5,20}`).Draw(t, "reason")
		numTriggers := rapid.IntRange(2, 10).Draw(t, "numTriggers")

		for i := 0; i < numTriggers; i++ {
			esm.EmergencyStop(reason)
		}

		cb.mu.Lock()
		defer cb.mu.Unlock()

		// Callbacks should only be invoked once
		if cb.cancelCalled != 1 {
			t.Fatalf("CancelAllOrders called %d times, expected 1 (idempotent)", cb.cancelCalled)
		}
		if cb.stopCalled != 1 {
			t.Fatalf("StopAllStrategies called %d times, expected 1 (idempotent)", cb.stopCalled)
		}
		if cb.alertCalled != 1 {
			t.Fatalf("SendCriticalAlert called %d times, expected 1 (idempotent)", cb.alertCalled)
		}
	})
}
