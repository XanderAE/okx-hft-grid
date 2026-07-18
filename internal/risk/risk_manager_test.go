package risk

import (
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

func defaultLimits() *models.RiskLimits {
	return &models.RiskLimits{
		MaxPositionPerSymbol: decimal.NewFromInt(10000), // 10,000 USDT per symbol
		MaxTotalPosition:     decimal.NewFromInt(50000), // 50,000 USDT total
		MaxDailyLoss:         decimal.NewFromInt(500),   // 500 USDT max daily loss
		MaxOrdersPerSecond:   10,                        // 10 orders/sec
		MaxOpenOrders:        20,                        // 20 open orders per symbol
		MinSpreadBps:         5,                         // 5 bps minimum spread
	}
}

func newTestOrder(symbol string, price, qty float64, spreadBps int) *OrderRequest {
	return &OrderRequest{
		Symbol:     symbol,
		Side:       models.SideBuy,
		OrderType:  models.OrderTypeLimit,
		Price:      decimal.NewFromFloat(price),
		Quantity:   decimal.NewFromFloat(qty),
		StrategyID: "test-strategy",
		SpreadBps:  spreadBps,
	}
}

func TestCheckOrder_Approved(t *testing.T) {
	rm := NewRiskManager(defaultLimits())

	order := newTestOrder("BTC-USDT", 100.0, 10.0, 10) // notional = 1000 USDT, spread = 10 bps
	decision := rm.CheckOrder(order)

	if !decision.Approved {
		t.Errorf("expected order to be approved, got rejected: %v", decision.Reasons)
	}
}

func TestCheckOrder_PositionLimitExceeded(t *testing.T) {
	rm := NewRiskManager(defaultLimits())

	// Set existing position to near limit
	rm.UpdatePosition("BTC-USDT", &models.Position{
		Symbol:        "BTC-USDT",
		Quantity:      decimal.NewFromFloat(90),
		AvgEntryPrice: decimal.NewFromFloat(100),
	})

	// This order would push position notional to 9000 + 2000 = 11000 > 10000
	order := newTestOrder("BTC-USDT", 100.0, 20.0, 10)
	decision := rm.CheckOrder(order)

	if decision.Approved {
		t.Error("expected order to be rejected due to position limit")
	}
	if len(decision.Reasons) == 0 {
		t.Error("expected rejection reasons")
	}
	found := false
	for _, r := range decision.Reasons {
		if contains(r, "position limit exceeded") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected position limit reason, got: %v", decision.Reasons)
	}
}

func TestCheckOrder_TotalExposureLimitExceeded(t *testing.T) {
	rm := NewRiskManager(defaultLimits())

	// Set positions across multiple symbols to near the total limit
	rm.UpdatePosition("ETH-USDT", &models.Position{
		Symbol:        "ETH-USDT",
		Quantity:      decimal.NewFromFloat(200),
		AvgEntryPrice: decimal.NewFromFloat(100),
	})
	rm.UpdatePosition("SOL-USDT", &models.Position{
		Symbol:        "SOL-USDT",
		Quantity:      decimal.NewFromFloat(200),
		AvgEntryPrice: decimal.NewFromFloat(100),
	})

	// Current total exposure = 20000 + 20000 = 40000
	// Adding 15000 would push to 55000 > 50000
	order := newTestOrder("DOGE-USDT", 150.0, 100.0, 10)
	decision := rm.CheckOrder(order)

	if decision.Approved {
		t.Error("expected order to be rejected due to total exposure limit")
	}
	found := false
	for _, r := range decision.Reasons {
		if contains(r, "total exposure limit exceeded") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected total exposure reason, got: %v", decision.Reasons)
	}
}

func TestCheckOrder_DailyLossLimitExceeded(t *testing.T) {
	rm := NewRiskManager(defaultLimits())

	// Set daily PnL to exceed loss limit
	rm.UpdatePnL("strategy-1", decimal.NewFromFloat(-300))
	rm.UpdatePnL("strategy-2", decimal.NewFromFloat(-250))
	// Total PnL = -550 < -500

	order := newTestOrder("BTC-USDT", 100.0, 1.0, 10)
	decision := rm.CheckOrder(order)

	if decision.Approved {
		t.Error("expected order to be rejected due to daily loss limit")
	}
	found := false
	for _, r := range decision.Reasons {
		if contains(r, "daily loss limit exceeded") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected daily loss reason, got: %v", decision.Reasons)
	}
}

func TestCheckOrder_OrderRateLimitExceeded(t *testing.T) {
	limits := defaultLimits()
	limits.MaxOrdersPerSecond = 3
	rm := NewRiskManager(limits)

	// Submit 3 orders (filling the window)
	for i := 0; i < 3; i++ {
		order := newTestOrder("BTC-USDT", 100.0, 1.0, 10)
		d := rm.CheckOrder(order)
		if !d.Approved {
			t.Fatalf("order %d should be approved, got: %v", i, d.Reasons)
		}
	}

	// 4th order should be rejected
	order := newTestOrder("BTC-USDT", 100.0, 1.0, 10)
	decision := rm.CheckOrder(order)

	if decision.Approved {
		t.Error("expected order to be rejected due to rate limit")
	}
	found := false
	for _, r := range decision.Reasons {
		if contains(r, "order rate limit exceeded") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected rate limit reason, got: %v", decision.Reasons)
	}
}

func TestCheckOrder_OpenOrderLimitExceeded(t *testing.T) {
	limits := defaultLimits()
	limits.MaxOpenOrders = 5
	rm := NewRiskManager(limits)

	rm.UpdateOpenOrderCount("BTC-USDT", 5)

	order := newTestOrder("BTC-USDT", 100.0, 1.0, 10)
	decision := rm.CheckOrder(order)

	if decision.Approved {
		t.Error("expected order to be rejected due to open order limit")
	}
	found := false
	for _, r := range decision.Reasons {
		if contains(r, "open order limit exceeded") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected open order limit reason, got: %v", decision.Reasons)
	}
}

func TestCheckOrder_SpreadTooNarrow(t *testing.T) {
	rm := NewRiskManager(defaultLimits())

	// Spread of 3 bps is less than minimum of 5 bps
	order := newTestOrder("BTC-USDT", 100.0, 1.0, 3)
	decision := rm.CheckOrder(order)

	if decision.Approved {
		t.Error("expected order to be rejected due to narrow spread")
	}
	found := false
	for _, r := range decision.Reasons {
		if contains(r, "spread too narrow") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected spread too narrow reason, got: %v", decision.Reasons)
	}
}

func TestCheckOrder_EmergencyStop(t *testing.T) {
	rm := NewRiskManager(defaultLimits())

	rm.EmergencyStop("test emergency")

	order := newTestOrder("BTC-USDT", 100.0, 1.0, 10)
	decision := rm.CheckOrder(order)

	if decision.Approved {
		t.Error("expected order to be rejected due to emergency stop")
	}
	found := false
	for _, r := range decision.Reasons {
		if contains(r, "emergency stop active") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected emergency stop reason, got: %v", decision.Reasons)
	}
}

func TestResumeFromEmergencyStop(t *testing.T) {
	rm := NewRiskManager(defaultLimits())

	rm.EmergencyStop("test emergency")
	if !rm.IsEmergencyStopActive() {
		t.Error("expected emergency stop to be active")
	}

	err := rm.ResumeFromEmergencyStop()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if rm.IsEmergencyStopActive() {
		t.Error("expected emergency stop to be inactive after resume")
	}

	// Orders should now be approved
	order := newTestOrder("BTC-USDT", 100.0, 1.0, 10)
	decision := rm.CheckOrder(order)
	if !decision.Approved {
		t.Errorf("expected order to be approved after resume, got: %v", decision.Reasons)
	}
}

func TestCheckBatchOrders(t *testing.T) {
	rm := NewRiskManager(defaultLimits())

	orders := []*OrderRequest{
		newTestOrder("BTC-USDT", 100.0, 1.0, 10),
		newTestOrder("ETH-USDT", 50.0, 2.0, 10),
	}

	decision := rm.CheckBatchOrders(orders)
	if !decision.Approved {
		t.Errorf("expected batch to be approved, got: %v", decision.Reasons)
	}
}

func TestCheckBatchOrders_Rejected(t *testing.T) {
	rm := NewRiskManager(defaultLimits())

	// One order has too narrow spread
	orders := []*OrderRequest{
		newTestOrder("BTC-USDT", 100.0, 1.0, 10),
		newTestOrder("ETH-USDT", 50.0, 2.0, 2), // spread too narrow
	}

	decision := rm.CheckBatchOrders(orders)
	if decision.Approved {
		t.Error("expected batch to be rejected")
	}
}

func TestGetRiskMetrics(t *testing.T) {
	rm := NewRiskManager(defaultLimits())

	rm.UpdatePosition("BTC-USDT", &models.Position{
		Symbol:        "BTC-USDT",
		Quantity:      decimal.NewFromFloat(10),
		AvgEntryPrice: decimal.NewFromFloat(100),
	})
	rm.UpdatePnL("strategy-1", decimal.NewFromFloat(-50))
	rm.UpdateOpenOrderCount("BTC-USDT", 3)

	metrics := rm.GetRiskMetrics()

	if !metrics.DailyPnL.Equal(decimal.NewFromFloat(-50)) {
		t.Errorf("expected daily PnL -50, got %s", metrics.DailyPnL.String())
	}
	if !metrics.TotalExposure.Equal(decimal.NewFromFloat(1000)) {
		t.Errorf("expected total exposure 1000, got %s", metrics.TotalExposure.String())
	}
	if metrics.OpenOrdersBySymbol["BTC-USDT"] != 3 {
		t.Errorf("expected 3 open orders for BTC-USDT, got %d", metrics.OpenOrdersBySymbol["BTC-USDT"])
	}
	if metrics.EmergencyStopActive {
		t.Error("expected emergency stop to be inactive")
	}
}

func TestSetRiskLimits(t *testing.T) {
	rm := NewRiskManager(defaultLimits())

	newLimits := &models.RiskLimits{
		MaxPositionPerSymbol: decimal.NewFromInt(5000),
		MaxTotalPosition:     decimal.NewFromInt(20000),
		MaxDailyLoss:         decimal.NewFromInt(200),
		MaxOrdersPerSecond:   5,
		MaxOpenOrders:        10,
		MinSpreadBps:         10,
	}
	rm.SetRiskLimits(newLimits)

	// Verify new limits are applied: order with 6 bps spread should now be rejected (min 10)
	order := newTestOrder("BTC-USDT", 100.0, 1.0, 6)
	decision := rm.CheckOrder(order)

	if decision.Approved {
		t.Error("expected order to be rejected with new spread limit of 10 bps")
	}
}

func TestConcurrentAccess(t *testing.T) {
	rm := NewRiskManager(defaultLimits())

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			order := newTestOrder("BTC-USDT", 100.0, 1.0, 10)
			rm.CheckOrder(order)
		}()
	}

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rm.UpdatePosition("BTC-USDT", &models.Position{
				Symbol:        "BTC-USDT",
				Quantity:      decimal.NewFromFloat(float64(idx)),
				AvgEntryPrice: decimal.NewFromFloat(100),
			})
		}(i)
	}

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rm.GetRiskMetrics()
		}()
	}

	wg.Wait()
}

func TestOrderRateWindowExpiry(t *testing.T) {
	limits := defaultLimits()
	limits.MaxOrdersPerSecond = 2
	rm := NewRiskManager(limits)

	// Submit 2 orders
	for i := 0; i < 2; i++ {
		order := newTestOrder("BTC-USDT", 100.0, 1.0, 10)
		d := rm.CheckOrder(order)
		if !d.Approved {
			t.Fatalf("order %d should be approved", i)
		}
	}

	// Wait for the window to expire
	time.Sleep(1100 * time.Millisecond)

	// Should be approved now
	order := newTestOrder("BTC-USDT", 100.0, 1.0, 10)
	decision := rm.CheckOrder(order)
	if !decision.Approved {
		t.Errorf("expected order to be approved after window expiry, got: %v", decision.Reasons)
	}
}

func TestResetDailyPnL(t *testing.T) {
	rm := NewRiskManager(defaultLimits())

	rm.UpdatePnL("strategy-1", decimal.NewFromFloat(-600))

	// Should reject due to daily loss
	order := newTestOrder("BTC-USDT", 100.0, 1.0, 10)
	decision := rm.CheckOrder(order)
	if decision.Approved {
		t.Error("expected rejection before reset")
	}

	// Reset PnL
	rm.ResetDailyPnL()

	// Should approve now
	decision = rm.CheckOrder(order)
	if !decision.Approved {
		t.Errorf("expected approval after PnL reset, got: %v", decision.Reasons)
	}
}

// contains checks if substr is found in s.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
