package strategy

import (
	"testing"

	"github.com/shopspring/decimal"
)

// --- RingBuffer Tests ---

func TestMeanReversion_RingBuffer_NewRingBuffer(t *testing.T) {
	rb := NewRingBuffer(5)
	if rb.Len() != 0 {
		t.Errorf("expected empty buffer, got len %d", rb.Len())
	}
	if rb.Cap() != 5 {
		t.Errorf("expected capacity 5, got %d", rb.Cap())
	}
}

func TestMeanReversion_RingBuffer_ZeroCapacity(t *testing.T) {
	rb := NewRingBuffer(0)
	if rb.Cap() != 1 {
		t.Errorf("expected capacity 1 for zero input, got %d", rb.Cap())
	}
}

func TestMeanReversion_RingBuffer_PushAndLen(t *testing.T) {
	rb := NewRingBuffer(3)
	rb.Push(PriceVolume{Price: decimal.NewFromFloat(100.0), Volume: decimal.NewFromFloat(10.0)})
	if rb.Len() != 1 {
		t.Errorf("expected len 1, got %d", rb.Len())
	}
	rb.Push(PriceVolume{Price: decimal.NewFromFloat(101.0), Volume: decimal.NewFromFloat(20.0)})
	rb.Push(PriceVolume{Price: decimal.NewFromFloat(102.0), Volume: decimal.NewFromFloat(30.0)})
	if rb.Len() != 3 {
		t.Errorf("expected len 3, got %d", rb.Len())
	}
}

func TestMeanReversion_RingBuffer_Overflow(t *testing.T) {
	rb := NewRingBuffer(3)
	rb.Push(PriceVolume{Price: decimal.NewFromFloat(1.0), Volume: decimal.NewFromFloat(1.0)})
	rb.Push(PriceVolume{Price: decimal.NewFromFloat(2.0), Volume: decimal.NewFromFloat(1.0)})
	rb.Push(PriceVolume{Price: decimal.NewFromFloat(3.0), Volume: decimal.NewFromFloat(1.0)})
	rb.Push(PriceVolume{Price: decimal.NewFromFloat(4.0), Volume: decimal.NewFromFloat(1.0)}) // overwrites first

	if rb.Len() != 3 {
		t.Errorf("expected len 3 after overflow, got %d", rb.Len())
	}

	all := rb.GetAll()
	expected := []float64{2.0, 3.0, 4.0}
	for i, pv := range all {
		if !pv.Price.Equal(decimal.NewFromFloat(expected[i])) {
			t.Errorf("index %d: expected price %v, got %v", i, expected[i], pv.Price)
		}
	}
}

func TestMeanReversion_RingBuffer_GetAll_Empty(t *testing.T) {
	rb := NewRingBuffer(5)
	all := rb.GetAll()
	if all != nil {
		t.Errorf("expected nil for empty buffer, got %v", all)
	}
}

func TestMeanReversion_RingBuffer_GetLast(t *testing.T) {
	rb := NewRingBuffer(5)
	for i := 1; i <= 5; i++ {
		rb.Push(PriceVolume{Price: decimal.NewFromInt(int64(i)), Volume: decimal.NewFromInt(1)})
	}

	last3 := rb.GetLast(3)
	if len(last3) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(last3))
	}
	expected := []int64{3, 4, 5}
	for i, pv := range last3 {
		if !pv.Price.Equal(decimal.NewFromInt(expected[i])) {
			t.Errorf("index %d: expected %d, got %v", i, expected[i], pv.Price)
		}
	}
}

func TestMeanReversion_RingBuffer_GetLast_MoreThanAvailable(t *testing.T) {
	rb := NewRingBuffer(10)
	rb.Push(PriceVolume{Price: decimal.NewFromInt(1), Volume: decimal.NewFromInt(1)})
	rb.Push(PriceVolume{Price: decimal.NewFromInt(2), Volume: decimal.NewFromInt(1)})

	last5 := rb.GetLast(5)
	if len(last5) != 2 {
		t.Errorf("expected 2 elements (all available), got %d", len(last5))
	}
}

// --- CalculateSMA Tests ---

func TestMeanReversion_SMA_Basic(t *testing.T) {
	prices := []decimal.Decimal{
		decimal.NewFromInt(10),
		decimal.NewFromInt(20),
		decimal.NewFromInt(30),
	}
	result := CalculateSMA(prices)
	expected := decimal.NewFromInt(20)
	if !result.Equal(expected) {
		t.Errorf("SMA: expected %v, got %v", expected, result)
	}
}

func TestMeanReversion_SMA_SinglePrice(t *testing.T) {
	prices := []decimal.Decimal{decimal.NewFromFloat(42.5)}
	result := CalculateSMA(prices)
	if !result.Equal(decimal.NewFromFloat(42.5)) {
		t.Errorf("SMA single: expected 42.5, got %v", result)
	}
}

func TestMeanReversion_SMA_Empty(t *testing.T) {
	result := CalculateSMA(nil)
	if !result.IsZero() {
		t.Errorf("SMA empty: expected zero, got %v", result)
	}
}

func TestMeanReversion_SMA_EqualPrices(t *testing.T) {
	prices := []decimal.Decimal{
		decimal.NewFromInt(50),
		decimal.NewFromInt(50),
		decimal.NewFromInt(50),
		decimal.NewFromInt(50),
	}
	result := CalculateSMA(prices)
	if !result.Equal(decimal.NewFromInt(50)) {
		t.Errorf("SMA equal prices: expected 50, got %v", result)
	}
}

// --- CalculateEMA Tests ---

func TestMeanReversion_EMA_Basic(t *testing.T) {
	prices := []decimal.Decimal{
		decimal.NewFromInt(10),
		decimal.NewFromInt(11),
		decimal.NewFromInt(12),
		decimal.NewFromInt(13),
		decimal.NewFromInt(14),
	}
	result := CalculateEMA(prices, 5)
	// EMA should be between min (10) and max (14)
	if result.LessThan(decimal.NewFromInt(10)) || result.GreaterThan(decimal.NewFromInt(14)) {
		t.Errorf("EMA out of bounds: got %v, expected in [10, 14]", result)
	}
}

func TestMeanReversion_EMA_SinglePrice(t *testing.T) {
	prices := []decimal.Decimal{decimal.NewFromFloat(100.0)}
	result := CalculateEMA(prices, 5)
	if !result.Equal(decimal.NewFromFloat(100.0)) {
		t.Errorf("EMA single: expected 100, got %v", result)
	}
}

func TestMeanReversion_EMA_Empty(t *testing.T) {
	result := CalculateEMA(nil, 5)
	if !result.IsZero() {
		t.Errorf("EMA empty: expected zero, got %v", result)
	}
}

func TestMeanReversion_EMA_InvalidLookback(t *testing.T) {
	prices := []decimal.Decimal{decimal.NewFromInt(10)}
	result := CalculateEMA(prices, 0)
	if !result.IsZero() {
		t.Errorf("EMA invalid lookback: expected zero, got %v", result)
	}
}

func TestMeanReversion_EMA_Bounded(t *testing.T) {
	// EMA must always be within [min(prices), max(prices)]
	prices := []decimal.Decimal{
		decimal.NewFromInt(100),
		decimal.NewFromInt(50),
		decimal.NewFromInt(200),
		decimal.NewFromInt(75),
		decimal.NewFromInt(150),
	}
	result := CalculateEMA(prices, 3)
	minP := decimal.NewFromInt(50)
	maxP := decimal.NewFromInt(200)
	if result.LessThan(minP) || result.GreaterThan(maxP) {
		t.Errorf("EMA bounded: got %v, expected in [%v, %v]", result, minP, maxP)
	}
}

func TestMeanReversion_EMA_EqualPrices(t *testing.T) {
	prices := []decimal.Decimal{
		decimal.NewFromInt(50),
		decimal.NewFromInt(50),
		decimal.NewFromInt(50),
	}
	result := CalculateEMA(prices, 3)
	if !result.Equal(decimal.NewFromInt(50)) {
		t.Errorf("EMA equal prices: expected 50, got %v", result)
	}
}

// --- CalculateVWAP Tests ---

func TestMeanReversion_VWAP_Basic(t *testing.T) {
	prices := []decimal.Decimal{
		decimal.NewFromInt(10),
		decimal.NewFromInt(20),
	}
	volumes := []decimal.Decimal{
		decimal.NewFromInt(100),
		decimal.NewFromInt(100),
	}
	result := CalculateVWAP(prices, volumes)
	// (10*100 + 20*100) / (100+100) = 3000/200 = 15
	expected := decimal.NewFromInt(15)
	if !result.Equal(expected) {
		t.Errorf("VWAP basic: expected %v, got %v", expected, result)
	}
}

func TestMeanReversion_VWAP_WeightedToHighVolume(t *testing.T) {
	prices := []decimal.Decimal{
		decimal.NewFromInt(10),
		decimal.NewFromInt(20),
	}
	volumes := []decimal.Decimal{
		decimal.NewFromInt(900), // much more volume at price 10
		decimal.NewFromInt(100),
	}
	result := CalculateVWAP(prices, volumes)
	// (10*900 + 20*100) / (900+100) = 11000/1000 = 11
	expected := decimal.NewFromInt(11)
	if !result.Equal(expected) {
		t.Errorf("VWAP weighted: expected %v, got %v", expected, result)
	}
}

func TestMeanReversion_VWAP_Empty(t *testing.T) {
	result := CalculateVWAP(nil, nil)
	if !result.IsZero() {
		t.Errorf("VWAP empty: expected zero, got %v", result)
	}
}

func TestMeanReversion_VWAP_MismatchedLengths(t *testing.T) {
	prices := []decimal.Decimal{decimal.NewFromInt(10)}
	volumes := []decimal.Decimal{decimal.NewFromInt(1), decimal.NewFromInt(2)}
	result := CalculateVWAP(prices, volumes)
	if !result.IsZero() {
		t.Errorf("VWAP mismatched: expected zero, got %v", result)
	}
}

func TestMeanReversion_VWAP_ZeroVolume(t *testing.T) {
	prices := []decimal.Decimal{
		decimal.NewFromInt(10),
		decimal.NewFromInt(20),
	}
	volumes := []decimal.Decimal{
		decimal.Zero,
		decimal.Zero,
	}
	result := CalculateVWAP(prices, volumes)
	if !result.IsZero() {
		t.Errorf("VWAP zero volume: expected zero, got %v", result)
	}
}

// --- MeanReversionCalculator Tests ---

func TestMeanReversion_Calculator_New(t *testing.T) {
	calc, err := NewMeanReversionCalculator(10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calc.DataCount() != 0 {
		t.Errorf("expected 0 data points, got %d", calc.DataCount())
	}
	if calc.HasEnoughData() {
		t.Error("should not have enough data initially")
	}
}

func TestMeanReversion_Calculator_InvalidLookback(t *testing.T) {
	_, err := NewMeanReversionCalculator(0)
	if err == nil {
		t.Error("expected error for zero lookback")
	}
	_, err = NewMeanReversionCalculator(-5)
	if err == nil {
		t.Error("expected error for negative lookback")
	}
}

func TestMeanReversion_Calculator_AddDataAndHasEnoughData(t *testing.T) {
	calc, _ := NewMeanReversionCalculator(3)
	calc.AddDataPoint(decimal.NewFromInt(10), decimal.NewFromInt(100))
	calc.AddDataPoint(decimal.NewFromInt(20), decimal.NewFromInt(200))
	if calc.HasEnoughData() {
		t.Error("should not have enough data with 2/3 points")
	}
	calc.AddDataPoint(decimal.NewFromInt(30), decimal.NewFromInt(300))
	if !calc.HasEnoughData() {
		t.Error("should have enough data with 3/3 points")
	}
}

func TestMeanReversion_Calculator_GetSMA(t *testing.T) {
	calc, _ := NewMeanReversionCalculator(3)
	calc.AddDataPoint(decimal.NewFromInt(10), decimal.NewFromInt(1))
	calc.AddDataPoint(decimal.NewFromInt(20), decimal.NewFromInt(1))
	calc.AddDataPoint(decimal.NewFromInt(30), decimal.NewFromInt(1))

	sma := calc.GetSMA()
	expected := decimal.NewFromInt(20)
	if !sma.Equal(expected) {
		t.Errorf("Calculator SMA: expected %v, got %v", expected, sma)
	}
}

func TestMeanReversion_Calculator_GetEMA(t *testing.T) {
	calc, _ := NewMeanReversionCalculator(3)
	calc.AddDataPoint(decimal.NewFromInt(10), decimal.NewFromInt(1))
	calc.AddDataPoint(decimal.NewFromInt(20), decimal.NewFromInt(1))
	calc.AddDataPoint(decimal.NewFromInt(30), decimal.NewFromInt(1))

	ema := calc.GetEMA()
	// EMA should be between 10 and 30
	if ema.LessThan(decimal.NewFromInt(10)) || ema.GreaterThan(decimal.NewFromInt(30)) {
		t.Errorf("Calculator EMA: got %v, expected in [10, 30]", ema)
	}
}

func TestMeanReversion_Calculator_GetVWAP(t *testing.T) {
	calc, _ := NewMeanReversionCalculator(3)
	calc.AddDataPoint(decimal.NewFromInt(10), decimal.NewFromInt(100))
	calc.AddDataPoint(decimal.NewFromInt(20), decimal.NewFromInt(200))
	calc.AddDataPoint(decimal.NewFromInt(30), decimal.NewFromInt(300))

	vwap := calc.GetVWAP()
	// VWAP = (10*100 + 20*200 + 30*300) / (100+200+300) = (1000+4000+9000) / 600 = 14000/600 ≈ 23.33
	expected := decimal.NewFromInt(14000).Div(decimal.NewFromInt(600))
	if !vwap.Equal(expected) {
		t.Errorf("Calculator VWAP: expected %v, got %v", expected, vwap)
	}
}

func TestMeanReversion_Calculator_RingBufferOverflow(t *testing.T) {
	calc, _ := NewMeanReversionCalculator(3)
	// Add 5 data points; buffer only keeps last 3
	calc.AddDataPoint(decimal.NewFromInt(1), decimal.NewFromInt(1))
	calc.AddDataPoint(decimal.NewFromInt(2), decimal.NewFromInt(1))
	calc.AddDataPoint(decimal.NewFromInt(3), decimal.NewFromInt(1))
	calc.AddDataPoint(decimal.NewFromInt(4), decimal.NewFromInt(1))
	calc.AddDataPoint(decimal.NewFromInt(5), decimal.NewFromInt(1))

	sma := calc.GetSMA()
	// Should be (3+4+5)/3 = 4
	expected := decimal.NewFromInt(4)
	if !sma.Equal(expected) {
		t.Errorf("Calculator overflow SMA: expected %v, got %v", expected, sma)
	}
}

func TestMeanReversion_Calculator_EmptyGetSMA(t *testing.T) {
	calc, _ := NewMeanReversionCalculator(5)
	sma := calc.GetSMA()
	if !sma.IsZero() {
		t.Errorf("empty GetSMA: expected zero, got %v", sma)
	}
}

func TestMeanReversion_Calculator_EmptyGetEMA(t *testing.T) {
	calc, _ := NewMeanReversionCalculator(5)
	ema := calc.GetEMA()
	if !ema.IsZero() {
		t.Errorf("empty GetEMA: expected zero, got %v", ema)
	}
}

func TestMeanReversion_Calculator_EmptyGetVWAP(t *testing.T) {
	calc, _ := NewMeanReversionCalculator(5)
	vwap := calc.GetVWAP()
	if !vwap.IsZero() {
		t.Errorf("empty GetVWAP: expected zero, got %v", vwap)
	}
}
