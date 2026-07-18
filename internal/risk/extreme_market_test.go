package risk

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// mockExtremeMarketCallback records calls for testing.
type mockExtremeMarketCallback struct {
	cancelGridCalled   int
	pauseMeanRevCalled int
	sendAlertCalled    int
	lastAlertReason    string
}

func (m *mockExtremeMarketCallback) CancelGridOrders() {
	m.cancelGridCalled++
}

func (m *mockExtremeMarketCallback) PauseMeanReversion() {
	m.pauseMeanRevCalled++
}

func (m *mockExtremeMarketCallback) SendAlert(reason string) {
	m.sendAlertCalled++
	m.lastAlertReason = reason
}

func TestNewExtremeMarketDetector(t *testing.T) {
	emd := NewExtremeMarketDetector()
	if emd == nil {
		t.Fatal("expected non-nil detector")
	}
	if !emd.priceChangeThreshold.Equal(decimal.NewFromFloat(0.05)) {
		t.Errorf("expected price change threshold 0.05, got %s", emd.priceChangeThreshold)
	}
	if !emd.spreadMultiplier.Equal(decimal.NewFromInt(3)) {
		t.Errorf("expected spread multiplier 3, got %s", emd.spreadMultiplier)
	}
	if emd.priceWindow != time.Minute {
		t.Errorf("expected price window 1 minute, got %v", emd.priceWindow)
	}
	if emd.spreadWindow != 5*time.Minute {
		t.Errorf("expected spread window 5 minutes, got %v", emd.spreadWindow)
	}
}

func TestRegisterCallback(t *testing.T) {
	emd := NewExtremeMarketDetector()
	cb := &mockExtremeMarketCallback{}
	emd.RegisterCallback(cb)

	if len(emd.callbacks) != 1 {
		t.Errorf("expected 1 callback, got %d", len(emd.callbacks))
	}
}

func TestCheckPriceChange_NoHistory(t *testing.T) {
	emd := NewExtremeMarketDetector()
	result := emd.CheckPriceChange("BTC-USDT", decimal.NewFromFloat(100))
	if result {
		t.Error("expected false with no history")
	}
}

func TestCheckPriceChange_NormalMovement(t *testing.T) {
	emd := NewExtremeMarketDetector()
	symbol := "BTC-USDT"
	now := time.Now()

	// Record a price at the start of the window
	emd.RecordPrice(symbol, decimal.NewFromFloat(100), now.Add(-30*time.Second))

	// Check with 3% change (below threshold)
	result := emd.CheckPriceChange(symbol, decimal.NewFromFloat(103))
	if result {
		t.Error("expected false for 3% change (below 5% threshold)")
	}
}

func TestCheckPriceChange_ExtremeMovement(t *testing.T) {
	emd := NewExtremeMarketDetector()
	symbol := "BTC-USDT"
	now := time.Now()

	// Record a price at the start of the window
	emd.RecordPrice(symbol, decimal.NewFromFloat(100), now.Add(-30*time.Second))

	// Check with 6% change (above threshold)
	result := emd.CheckPriceChange(symbol, decimal.NewFromFloat(106))
	if result != true {
		t.Error("expected true for 6% change (above 5% threshold)")
	}
}

func TestCheckPriceChange_NegativeMovement(t *testing.T) {
	emd := NewExtremeMarketDetector()
	symbol := "ETH-USDT"
	now := time.Now()

	// Record a price at the start of the window
	emd.RecordPrice(symbol, decimal.NewFromFloat(100), now.Add(-30*time.Second))

	// Check with -7% change (above threshold)
	result := emd.CheckPriceChange(symbol, decimal.NewFromFloat(93))
	if result != true {
		t.Error("expected true for -7% change (above 5% threshold)")
	}
}

func TestCheckPriceChange_ExactlyAtThreshold(t *testing.T) {
	emd := NewExtremeMarketDetector()
	symbol := "SOL-USDT"
	now := time.Now()

	// Record price
	emd.RecordPrice(symbol, decimal.NewFromFloat(100), now.Add(-30*time.Second))

	// Check with exactly 5% change (not greater than, so should be false)
	result := emd.CheckPriceChange(symbol, decimal.NewFromFloat(105))
	if result {
		t.Error("expected false for exactly 5% change (threshold is >5%, not >=5%)")
	}
}

func TestCheckPriceChange_OldEntriesPruned(t *testing.T) {
	emd := NewExtremeMarketDetector()
	symbol := "BTC-USDT"
	now := time.Now()

	// Record an old price (outside window) and a recent price
	emd.RecordPrice(symbol, decimal.NewFromFloat(50), now.Add(-2*time.Minute))
	emd.RecordPrice(symbol, decimal.NewFromFloat(100), now.Add(-10*time.Second))

	// The old entry (50) should be pruned after recording the new one.
	// The newest entry is 100, so a current price of 103 is only 3% change.
	result := emd.CheckPriceChange(symbol, decimal.NewFromFloat(103))
	if result {
		t.Error("expected false: old entry should be pruned, so oldest is 100 (3% change)")
	}
}

func TestCheckSpreadAnomaly_NoHistory(t *testing.T) {
	emd := NewExtremeMarketDetector()
	result := emd.CheckSpreadAnomaly("BTC-USDT", decimal.NewFromFloat(0.5))
	if result {
		t.Error("expected false with no history")
	}
}

func TestCheckSpreadAnomaly_NormalSpread(t *testing.T) {
	emd := NewExtremeMarketDetector()
	symbol := "BTC-USDT"
	now := time.Now()

	// Record spreads to establish an average of 0.10
	for i := 0; i < 10; i++ {
		emd.RecordSpread(symbol, decimal.NewFromFloat(0.10), now.Add(-time.Duration(i)*30*time.Second))
	}

	// Current spread of 0.20 is 2× average (below 3× threshold)
	result := emd.CheckSpreadAnomaly(symbol, decimal.NewFromFloat(0.20))
	if result {
		t.Error("expected false for 2× average spread (below 3× threshold)")
	}
}

func TestCheckSpreadAnomaly_ExtremeSpread(t *testing.T) {
	emd := NewExtremeMarketDetector()
	symbol := "BTC-USDT"
	now := time.Now()

	// Record spreads to establish an average of 0.10
	for i := 0; i < 10; i++ {
		emd.RecordSpread(symbol, decimal.NewFromFloat(0.10), now.Add(-time.Duration(i)*30*time.Second))
	}

	// Current spread of 0.40 is 4× average (above 3× threshold)
	result := emd.CheckSpreadAnomaly(symbol, decimal.NewFromFloat(0.40))
	if result != true {
		t.Error("expected true for 4× average spread (above 3× threshold)")
	}
}

func TestCheckSpreadAnomaly_ExactlyAtThreshold(t *testing.T) {
	emd := NewExtremeMarketDetector()
	symbol := "BTC-USDT"
	now := time.Now()

	// Record spreads to establish an average of 0.10
	for i := 0; i < 10; i++ {
		emd.RecordSpread(symbol, decimal.NewFromFloat(0.10), now.Add(-time.Duration(i)*30*time.Second))
	}

	// Current spread of 0.30 is exactly 3× average (not > 3×, so false)
	result := emd.CheckSpreadAnomaly(symbol, decimal.NewFromFloat(0.30))
	if result {
		t.Error("expected false for exactly 3× average spread (threshold is >3×)")
	}
}

func TestDetect_NoTrigger(t *testing.T) {
	emd := NewExtremeMarketDetector()
	cb := &mockExtremeMarketCallback{}
	emd.RegisterCallback(cb)

	symbol := "BTC-USDT"
	now := time.Now()

	// Record normal price and spread data
	emd.RecordPrice(symbol, decimal.NewFromFloat(100), now.Add(-30*time.Second))
	emd.RecordSpread(symbol, decimal.NewFromFloat(0.10), now.Add(-30*time.Second))

	// Normal price (2% change) and normal spread (2× average)
	triggered := emd.Detect(symbol, decimal.NewFromFloat(102), decimal.NewFromFloat(0.20))
	if triggered {
		t.Error("expected no trigger for normal conditions")
	}
	if cb.cancelGridCalled != 0 {
		t.Error("expected CancelGridOrders not called")
	}
	if cb.pauseMeanRevCalled != 0 {
		t.Error("expected PauseMeanReversion not called")
	}
	if cb.sendAlertCalled != 0 {
		t.Error("expected SendAlert not called")
	}
}

func TestDetect_PriceTriggered(t *testing.T) {
	emd := NewExtremeMarketDetector()
	cb := &mockExtremeMarketCallback{}
	emd.RegisterCallback(cb)

	symbol := "BTC-USDT"
	now := time.Now()

	// Record price and spread data
	emd.RecordPrice(symbol, decimal.NewFromFloat(100), now.Add(-30*time.Second))
	emd.RecordSpread(symbol, decimal.NewFromFloat(0.10), now.Add(-30*time.Second))

	// Extreme price (8% change), normal spread
	triggered := emd.Detect(symbol, decimal.NewFromFloat(108), decimal.NewFromFloat(0.20))
	if !triggered {
		t.Error("expected trigger for extreme price change")
	}
	if cb.cancelGridCalled != 1 {
		t.Errorf("expected CancelGridOrders called 1 time, got %d", cb.cancelGridCalled)
	}
	if cb.pauseMeanRevCalled != 1 {
		t.Errorf("expected PauseMeanReversion called 1 time, got %d", cb.pauseMeanRevCalled)
	}
	if cb.sendAlertCalled != 1 {
		t.Errorf("expected SendAlert called 1 time, got %d", cb.sendAlertCalled)
	}
}

func TestDetect_SpreadTriggered(t *testing.T) {
	emd := NewExtremeMarketDetector()
	cb := &mockExtremeMarketCallback{}
	emd.RegisterCallback(cb)

	symbol := "BTC-USDT"
	now := time.Now()

	// Record price and spread data
	emd.RecordPrice(symbol, decimal.NewFromFloat(100), now.Add(-30*time.Second))
	for i := 0; i < 5; i++ {
		emd.RecordSpread(symbol, decimal.NewFromFloat(0.10), now.Add(-time.Duration(i)*time.Minute))
	}

	// Normal price (1% change), extreme spread (5× average)
	triggered := emd.Detect(symbol, decimal.NewFromFloat(101), decimal.NewFromFloat(0.50))
	if !triggered {
		t.Error("expected trigger for extreme spread")
	}
	if cb.cancelGridCalled != 1 {
		t.Errorf("expected CancelGridOrders called 1 time, got %d", cb.cancelGridCalled)
	}
	if cb.pauseMeanRevCalled != 1 {
		t.Errorf("expected PauseMeanReversion called 1 time, got %d", cb.pauseMeanRevCalled)
	}
	if cb.sendAlertCalled != 1 {
		t.Errorf("expected SendAlert called 1 time, got %d", cb.sendAlertCalled)
	}
}

func TestDetect_BothTriggered(t *testing.T) {
	emd := NewExtremeMarketDetector()
	cb := &mockExtremeMarketCallback{}
	emd.RegisterCallback(cb)

	symbol := "BTC-USDT"
	now := time.Now()

	// Record price and spread data
	emd.RecordPrice(symbol, decimal.NewFromFloat(100), now.Add(-30*time.Second))
	for i := 0; i < 5; i++ {
		emd.RecordSpread(symbol, decimal.NewFromFloat(0.10), now.Add(-time.Duration(i)*time.Minute))
	}

	// Both extreme price (10%) and extreme spread (5× average)
	triggered := emd.Detect(symbol, decimal.NewFromFloat(110), decimal.NewFromFloat(0.50))
	if !triggered {
		t.Error("expected trigger for both conditions")
	}
	if cb.cancelGridCalled != 1 {
		t.Errorf("expected CancelGridOrders called 1 time, got %d", cb.cancelGridCalled)
	}
	if cb.pauseMeanRevCalled != 1 {
		t.Errorf("expected PauseMeanReversion called 1 time, got %d", cb.pauseMeanRevCalled)
	}
	if cb.sendAlertCalled != 1 {
		t.Errorf("expected SendAlert called 1 time, got %d", cb.sendAlertCalled)
	}
}

func TestDetect_MultipleCallbacks(t *testing.T) {
	emd := NewExtremeMarketDetector()
	cb1 := &mockExtremeMarketCallback{}
	cb2 := &mockExtremeMarketCallback{}
	emd.RegisterCallback(cb1)
	emd.RegisterCallback(cb2)

	symbol := "BTC-USDT"
	now := time.Now()

	emd.RecordPrice(symbol, decimal.NewFromFloat(100), now.Add(-30*time.Second))

	// Trigger via price change
	emd.Detect(symbol, decimal.NewFromFloat(108), decimal.Zero)

	if cb1.cancelGridCalled != 1 || cb2.cancelGridCalled != 1 {
		t.Error("expected both callbacks to have CancelGridOrders called")
	}
	if cb1.pauseMeanRevCalled != 1 || cb2.pauseMeanRevCalled != 1 {
		t.Error("expected both callbacks to have PauseMeanReversion called")
	}
	if cb1.sendAlertCalled != 1 || cb2.sendAlertCalled != 1 {
		t.Error("expected both callbacks to have SendAlert called")
	}
}

func TestRecordPrice_MultiplePricesWithinWindow(t *testing.T) {
	emd := NewExtremeMarketDetector()
	symbol := "ETH-USDT"
	now := time.Now()

	// Record multiple prices within the 1-minute window
	emd.RecordPrice(symbol, decimal.NewFromFloat(100), now.Add(-50*time.Second))
	emd.RecordPrice(symbol, decimal.NewFromFloat(101), now.Add(-40*time.Second))
	emd.RecordPrice(symbol, decimal.NewFromFloat(102), now.Add(-30*time.Second))
	emd.RecordPrice(symbol, decimal.NewFromFloat(103), now.Add(-20*time.Second))

	// Check uses the oldest in the window (100)
	// 106 is 6% from 100 → triggers
	result := emd.CheckPriceChange(symbol, decimal.NewFromFloat(106))
	if !result {
		t.Error("expected true: 6% from oldest entry (100)")
	}
}

func TestRecordSpread_WindowPruning(t *testing.T) {
	emd := NewExtremeMarketDetector()
	symbol := "BTC-USDT"
	now := time.Now()

	// Record an old spread outside the 5-min window
	emd.RecordSpread(symbol, decimal.NewFromFloat(1.0), now.Add(-10*time.Minute))

	// Record a recent spread within the window
	emd.RecordSpread(symbol, decimal.NewFromFloat(0.10), now.Add(-1*time.Minute))

	// The old entry (1.0) should be pruned, leaving only 0.10 as the average.
	// Current spread of 0.40 is 4× of 0.10 → triggers
	result := emd.CheckSpreadAnomaly(symbol, decimal.NewFromFloat(0.40))
	if !result {
		t.Error("expected true: old entry pruned, average is 0.10, current 0.40 is 4×")
	}
}

func TestCheckPriceChange_ZeroOldestPrice(t *testing.T) {
	emd := NewExtremeMarketDetector()
	symbol := "BTC-USDT"
	now := time.Now()

	// Record a zero price (edge case)
	emd.RecordPrice(symbol, decimal.Zero, now.Add(-30*time.Second))

	// Should not trigger (division by zero protection)
	result := emd.CheckPriceChange(symbol, decimal.NewFromFloat(100))
	if result {
		t.Error("expected false when oldest price is zero")
	}
}

func TestCheckSpreadAnomaly_ZeroAverage(t *testing.T) {
	emd := NewExtremeMarketDetector()
	symbol := "BTC-USDT"
	now := time.Now()

	// Record zero spreads
	emd.RecordSpread(symbol, decimal.Zero, now.Add(-1*time.Minute))
	emd.RecordSpread(symbol, decimal.Zero, now.Add(-30*time.Second))

	// Should not trigger (zero average protection)
	result := emd.CheckSpreadAnomaly(symbol, decimal.NewFromFloat(0.10))
	if result {
		t.Error("expected false when average spread is zero")
	}
}

func TestDetect_DifferentSymbols(t *testing.T) {
	emd := NewExtremeMarketDetector()
	cb := &mockExtremeMarketCallback{}
	emd.RegisterCallback(cb)

	now := time.Now()

	// Record data for BTC
	emd.RecordPrice("BTC-USDT", decimal.NewFromFloat(100), now.Add(-30*time.Second))
	// Record data for ETH
	emd.RecordPrice("ETH-USDT", decimal.NewFromFloat(200), now.Add(-30*time.Second))

	// BTC has 8% change → trigger
	triggered := emd.Detect("BTC-USDT", decimal.NewFromFloat(108), decimal.Zero)
	if !triggered {
		t.Error("expected BTC trigger")
	}

	// ETH has only 2% change → no trigger
	triggered = emd.Detect("ETH-USDT", decimal.NewFromFloat(204), decimal.Zero)
	if triggered {
		t.Error("expected ETH no trigger for 2% change")
	}
}
