package strategy

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// --- CalculateStdDev Tests ---

func TestCalculateStdDev_Basic(t *testing.T) {
	// prices: 2, 4, 4, 4, 5, 5, 7, 9 → mean=5, variance=4, stddev=2
	prices := []decimal.Decimal{
		decimal.NewFromInt(2),
		decimal.NewFromInt(4),
		decimal.NewFromInt(4),
		decimal.NewFromInt(4),
		decimal.NewFromInt(5),
		decimal.NewFromInt(5),
		decimal.NewFromInt(7),
		decimal.NewFromInt(9),
	}
	mean := decimal.NewFromInt(5)
	result := CalculateStdDev(prices, mean)
	expected := decimal.NewFromInt(2)

	// Allow small floating point tolerance
	diff := result.Sub(expected).Abs()
	if diff.GreaterThan(decimal.NewFromFloat(0.0001)) {
		t.Errorf("StdDev basic: expected ~%v, got %v (diff=%v)", expected, result, diff)
	}
}

func TestCalculateStdDev_Empty(t *testing.T) {
	result := CalculateStdDev(nil, decimal.NewFromInt(5))
	if !result.IsZero() {
		t.Errorf("StdDev empty: expected zero, got %v", result)
	}
}

func TestCalculateStdDev_AllEqual(t *testing.T) {
	prices := []decimal.Decimal{
		decimal.NewFromInt(10),
		decimal.NewFromInt(10),
		decimal.NewFromInt(10),
	}
	mean := decimal.NewFromInt(10)
	result := CalculateStdDev(prices, mean)
	if !result.IsZero() {
		t.Errorf("StdDev all equal: expected zero, got %v", result)
	}
}

func TestCalculateStdDev_SingleElement(t *testing.T) {
	prices := []decimal.Decimal{decimal.NewFromInt(42)}
	mean := decimal.NewFromInt(42)
	result := CalculateStdDev(prices, mean)
	if !result.IsZero() {
		t.Errorf("StdDev single: expected zero, got %v", result)
	}
}

func TestCalculateStdDev_TwoElements(t *testing.T) {
	// prices: 3, 7 → mean=5, variance=((3-5)^2 + (7-5)^2)/2 = (4+4)/2 = 4, stddev=2
	prices := []decimal.Decimal{
		decimal.NewFromInt(3),
		decimal.NewFromInt(7),
	}
	mean := decimal.NewFromInt(5)
	result := CalculateStdDev(prices, mean)
	expected := decimal.NewFromInt(2)

	diff := result.Sub(expected).Abs()
	if diff.GreaterThan(decimal.NewFromFloat(0.0001)) {
		t.Errorf("StdDev two elements: expected ~%v, got %v", expected, result)
	}
}

// --- CalculateZScore Tests ---

func TestCalculateZScore_Basic(t *testing.T) {
	currentPrice := decimal.NewFromInt(110)
	mean := decimal.NewFromInt(100)
	stdDev := decimal.NewFromInt(5)

	zscore, valid := CalculateZScore(currentPrice, mean, stdDev)
	if !valid {
		t.Fatal("expected valid z-score")
	}
	// (110-100)/5 = 2
	expected := decimal.NewFromInt(2)
	if !zscore.Equal(expected) {
		t.Errorf("ZScore basic: expected %v, got %v", expected, zscore)
	}
}

func TestCalculateZScore_Negative(t *testing.T) {
	currentPrice := decimal.NewFromInt(90)
	mean := decimal.NewFromInt(100)
	stdDev := decimal.NewFromInt(5)

	zscore, valid := CalculateZScore(currentPrice, mean, stdDev)
	if !valid {
		t.Fatal("expected valid z-score")
	}
	// (90-100)/5 = -2
	expected := decimal.NewFromInt(-2)
	if !zscore.Equal(expected) {
		t.Errorf("ZScore negative: expected %v, got %v", expected, zscore)
	}
}

func TestCalculateZScore_ZeroStdDev(t *testing.T) {
	currentPrice := decimal.NewFromInt(100)
	mean := decimal.NewFromInt(100)
	stdDev := decimal.Zero

	_, valid := CalculateZScore(currentPrice, mean, stdDev)
	if valid {
		t.Error("expected invalid z-score when stdDev is zero")
	}
}

func TestCalculateZScore_PriceEqualsMean(t *testing.T) {
	currentPrice := decimal.NewFromInt(100)
	mean := decimal.NewFromInt(100)
	stdDev := decimal.NewFromInt(5)

	zscore, valid := CalculateZScore(currentPrice, mean, stdDev)
	if !valid {
		t.Fatal("expected valid z-score")
	}
	if !zscore.IsZero() {
		t.Errorf("ZScore at mean: expected 0, got %v", zscore)
	}
}

// --- SignalGenerator Tests ---

func newTestConfig() SignalGeneratorConfig {
	return SignalGeneratorConfig{
		EntryThreshold: decimal.NewFromFloat(2.0),
		ExitThreshold:  decimal.NewFromFloat(0.5),
		CooldownMs:     1000,
		LookbackPeriod: 5,
		MAType:         models.MATypeSMA,
	}
}

func TestSignalGenerator_New(t *testing.T) {
	sg := NewSignalGenerator(newTestConfig())
	if sg == nil {
		t.Fatal("expected non-nil signal generator")
	}
	if sg.LastSignal() != models.SignalDirectionNone {
		t.Errorf("expected initial signal NONE, got %v", sg.LastSignal())
	}
}

func TestSignalGenerator_CooldownClampLow(t *testing.T) {
	config := newTestConfig()
	config.CooldownMs = 50 // below 100
	sg := NewSignalGenerator(config)
	if sg.config.CooldownMs != 100 {
		t.Errorf("expected cooldown clamped to 100, got %d", sg.config.CooldownMs)
	}
}

func TestSignalGenerator_CooldownClampHigh(t *testing.T) {
	config := newTestConfig()
	config.CooldownMs = 100000 // above 60000
	sg := NewSignalGenerator(config)
	if sg.config.CooldownMs != 60000 {
		t.Errorf("expected cooldown clamped to 60000, got %d", sg.config.CooldownMs)
	}
}

func TestSignalGenerator_SuppressWhenInsufficientData(t *testing.T) {
	sg := NewSignalGenerator(newTestConfig())
	now := time.Now()

	// Add only 3 data points (lookback is 5)
	sg.AddDataPoint(decimal.NewFromInt(100), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(101), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(102), decimal.NewFromInt(1))

	signal := sg.GenerateSignal(decimal.NewFromInt(110), now)
	if signal != models.SignalDirectionNone {
		t.Errorf("expected NONE when data insufficient, got %v", signal)
	}
}

func TestSignalGenerator_SuppressWhenStdDevZero(t *testing.T) {
	sg := NewSignalGenerator(newTestConfig())
	now := time.Now()

	// All prices are the same → stdDev = 0
	for i := 0; i < 5; i++ {
		sg.AddDataPoint(decimal.NewFromInt(100), decimal.NewFromInt(1))
	}

	signal := sg.GenerateSignal(decimal.NewFromInt(110), now)
	// stdDev is 0, so signal should be suppressed (retain previous = NONE)
	if signal != models.SignalDirectionNone {
		t.Errorf("expected NONE when stdDev=0, got %v", signal)
	}
}

func TestSignalGenerator_BuySignal(t *testing.T) {
	config := newTestConfig()
	config.LookbackPeriod = 5
	config.EntryThreshold = decimal.NewFromFloat(2.0)
	sg := NewSignalGenerator(config)
	now := time.Now()

	// Create prices with known mean and stdDev
	// prices: 100, 100, 100, 100, 100 → mean=100, stdDev=0... that won't work
	// prices: 98, 99, 100, 101, 102 → mean=100, variance=(4+1+0+1+4)/5=2, stddev~1.414
	sg.AddDataPoint(decimal.NewFromInt(98), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(99), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(100), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(101), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(102), decimal.NewFromInt(1))

	// mean=100, stddev≈1.414
	// To get Z < -2: currentPrice < 100 - 2*1.414 = 97.17
	signal := sg.GenerateSignal(decimal.NewFromInt(96), now)
	if signal != models.SignalDirectionBuy {
		t.Errorf("expected BUY signal for very low price, got %v", signal)
	}
}

func TestSignalGenerator_SellSignal(t *testing.T) {
	config := newTestConfig()
	config.LookbackPeriod = 5
	config.EntryThreshold = decimal.NewFromFloat(2.0)
	sg := NewSignalGenerator(config)
	now := time.Now()

	// prices: 98, 99, 100, 101, 102 → mean=100, stddev≈1.414
	sg.AddDataPoint(decimal.NewFromInt(98), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(99), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(100), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(101), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(102), decimal.NewFromInt(1))

	// To get Z > +2: currentPrice > 100 + 2*1.414 = 102.83
	signal := sg.GenerateSignal(decimal.NewFromInt(104), now)
	if signal != models.SignalDirectionSell {
		t.Errorf("expected SELL signal for very high price, got %v", signal)
	}
}

func TestSignalGenerator_CloseSignal(t *testing.T) {
	config := newTestConfig()
	config.LookbackPeriod = 5
	config.EntryThreshold = decimal.NewFromFloat(2.0)
	config.ExitThreshold = decimal.NewFromFloat(0.5)
	sg := NewSignalGenerator(config)
	now := time.Now()

	// prices: 98, 99, 100, 101, 102 → mean=100, stddev≈1.414
	sg.AddDataPoint(decimal.NewFromInt(98), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(99), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(100), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(101), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(102), decimal.NewFromInt(1))

	// First generate a BUY signal to change from NONE
	sig := sg.GenerateSignal(decimal.NewFromInt(96), now)
	if sig != models.SignalDirectionBuy {
		t.Fatalf("setup: expected BUY, got %v", sig)
	}

	// |Z| < 0.5: currentPrice within 100 ± 0.5*1.414 ≈ [99.29, 100.71]
	// Price at 100 → Z=0 → |Z|=0 < 0.5 → CLOSE
	laterTime := now.Add(2 * time.Second)
	signal := sg.GenerateSignal(decimal.NewFromInt(100), laterTime)
	if signal != models.SignalDirectionClose {
		t.Errorf("expected CLOSE signal when near mean, got %v", signal)
	}
}

func TestSignalGenerator_CooldownEnforcement(t *testing.T) {
	config := newTestConfig()
	config.LookbackPeriod = 5
	config.CooldownMs = 5000 // 5 seconds
	config.EntryThreshold = decimal.NewFromFloat(2.0)
	config.ExitThreshold = decimal.NewFromFloat(0.5)
	sg := NewSignalGenerator(config)
	now := time.Now()

	// prices: 98, 99, 100, 101, 102
	sg.AddDataPoint(decimal.NewFromInt(98), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(99), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(100), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(101), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(102), decimal.NewFromInt(1))

	// Generate BUY signal
	sig1 := sg.GenerateSignal(decimal.NewFromInt(96), now)
	if sig1 != models.SignalDirectionBuy {
		t.Fatalf("expected BUY, got %v", sig1)
	}

	// Try to generate CLOSE signal 1 second later (within 5s cooldown)
	sig2 := sg.GenerateSignal(decimal.NewFromInt(100), now.Add(1*time.Second))
	if sig2 != models.SignalDirectionBuy {
		t.Errorf("expected BUY (cooldown active), got %v", sig2)
	}

	// After cooldown expires (6 seconds later), signal should change
	sig3 := sg.GenerateSignal(decimal.NewFromInt(100), now.Add(6*time.Second))
	if sig3 != models.SignalDirectionClose {
		t.Errorf("expected CLOSE after cooldown, got %v", sig3)
	}
}

func TestSignalGenerator_RetainPreviousSignalInDeadZone(t *testing.T) {
	config := newTestConfig()
	config.LookbackPeriod = 5
	config.EntryThreshold = decimal.NewFromFloat(2.0)
	config.ExitThreshold = decimal.NewFromFloat(0.5)
	sg := NewSignalGenerator(config)
	now := time.Now()

	// prices: 98, 99, 100, 101, 102 → mean=100, stddev≈1.414
	sg.AddDataPoint(decimal.NewFromInt(98), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(99), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(100), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(101), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(102), decimal.NewFromInt(1))

	// Generate BUY signal first
	sg.GenerateSignal(decimal.NewFromInt(96), now)

	// Price in dead zone: |Z| between exitThreshold (0.5) and entryThreshold (2.0)
	// Z = 1.0 → price = 100 + 1.0*1.414 ≈ 101.4
	// |Z|=1.0 is > 0.5 (exit) and < 2.0 (entry) → dead zone
	laterTime := now.Add(10 * time.Second)
	signal := sg.GenerateSignal(decimal.NewFromFloat(101.4), laterTime)
	if signal != models.SignalDirectionBuy {
		t.Errorf("expected BUY retained in dead zone, got %v", signal)
	}
}

func TestSignalGenerator_EMAType(t *testing.T) {
	config := newTestConfig()
	config.MAType = models.MATypeEMA
	config.LookbackPeriod = 5
	config.EntryThreshold = decimal.NewFromFloat(2.0)
	sg := NewSignalGenerator(config)
	now := time.Now()

	// Add diverse prices
	sg.AddDataPoint(decimal.NewFromInt(98), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(99), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(100), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(101), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(102), decimal.NewFromInt(1))

	// With EMA, it should still produce valid signals
	signal := sg.GenerateSignal(decimal.NewFromInt(96), now)
	// EMA weights recent prices more, so mean might be slightly higher than SMA=100
	// Either way, 96 far below should trigger BUY
	if signal != models.SignalDirectionBuy {
		t.Errorf("expected BUY with EMA type, got %v", signal)
	}
}

func TestSignalGenerator_VWAPType(t *testing.T) {
	config := newTestConfig()
	config.MAType = models.MATypeVWAP
	config.LookbackPeriod = 3
	config.EntryThreshold = decimal.NewFromFloat(2.0)
	sg := NewSignalGenerator(config)
	now := time.Now()

	// volumes differ significantly
	sg.AddDataPoint(decimal.NewFromInt(100), decimal.NewFromInt(1000))
	sg.AddDataPoint(decimal.NewFromInt(110), decimal.NewFromInt(10))
	sg.AddDataPoint(decimal.NewFromInt(120), decimal.NewFromInt(10))

	// VWAP = (100*1000 + 110*10 + 120*10) / (1000+10+10) = (100000+1100+1200)/1020 ≈ 100.29
	// price very high → should generate SELL
	signal := sg.GenerateSignal(decimal.NewFromInt(150), now)
	if signal != models.SignalDirectionSell {
		t.Errorf("expected SELL with VWAP type, got %v", signal)
	}
}

func TestSignalGenerator_StdDevZeroAfterSignalChange(t *testing.T) {
	config := newTestConfig()
	config.LookbackPeriod = 3
	config.CooldownMs = 100
	sg := NewSignalGenerator(config)
	now := time.Now()

	// First generate a valid signal with varied prices
	sg.AddDataPoint(decimal.NewFromInt(98), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(100), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(102), decimal.NewFromInt(1))

	sig := sg.GenerateSignal(decimal.NewFromInt(110), now)
	if sig != models.SignalDirectionSell {
		t.Fatalf("setup: expected SELL, got %v", sig)
	}

	// Now add equal prices to make stdDev=0
	sg.AddDataPoint(decimal.NewFromInt(100), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(100), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(100), decimal.NewFromInt(1))

	// With stdDev=0, previous signal (SELL) should be retained
	laterTime := now.Add(2 * time.Second)
	signal := sg.GenerateSignal(decimal.NewFromInt(90), laterTime)
	if signal != models.SignalDirectionSell {
		t.Errorf("expected SELL retained when stdDev=0, got %v", signal)
	}
}

func TestSignalGenerator_NoSignalChangeWhenSameSignal(t *testing.T) {
	config := newTestConfig()
	config.LookbackPeriod = 5
	config.CooldownMs = 5000
	sg := NewSignalGenerator(config)
	now := time.Now()

	sg.AddDataPoint(decimal.NewFromInt(98), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(99), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(100), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(101), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(102), decimal.NewFromInt(1))

	// Generate BUY at t=0
	sig1 := sg.GenerateSignal(decimal.NewFromInt(96), now)
	if sig1 != models.SignalDirectionBuy {
		t.Fatalf("expected BUY, got %v", sig1)
	}

	// Same signal (BUY) at t=1s should not be affected by cooldown
	sig2 := sg.GenerateSignal(decimal.NewFromInt(95), now.Add(1*time.Second))
	if sig2 != models.SignalDirectionBuy {
		t.Errorf("expected BUY (same signal, no cooldown needed), got %v", sig2)
	}
}

func TestSignalGenerator_LastSignalTime(t *testing.T) {
	config := newTestConfig()
	config.LookbackPeriod = 5
	sg := NewSignalGenerator(config)
	now := time.Now()

	sg.AddDataPoint(decimal.NewFromInt(98), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(99), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(100), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(101), decimal.NewFromInt(1))
	sg.AddDataPoint(decimal.NewFromInt(102), decimal.NewFromInt(1))

	// Initially, lastSignalTime should be zero
	if !sg.LastSignalTime().IsZero() {
		t.Error("expected zero last signal time initially")
	}

	// After signal generation
	sg.GenerateSignal(decimal.NewFromInt(96), now)
	if sg.LastSignalTime() != now {
		t.Errorf("expected last signal time %v, got %v", now, sg.LastSignalTime())
	}
}
