package strategy

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirements 5.3, 5.4, 5.6, 5.7, 5.8**

// TestProperty_MeanReversionSignalDirection_BuyWhenZBelowNegEntry verifies that
// when Z_Score < -entryThreshold, the signal generator produces a BUY signal.
func TestProperty_MeanReversionSignalDirection_BuyWhenZBelowNegEntry(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate valid thresholds: entry in [1.0, 5.0], exit in [0.1, entry-0.1]
		entryInt := rapid.IntRange(10, 50).Draw(t, "entryTenths") // 1.0 to 5.0
		entryThreshold := decimal.NewFromInt(int64(entryInt)).Div(decimal.NewFromInt(10))

		exitMax := entryInt - 1
		if exitMax < 1 {
			exitMax = 1
		}
		exitInt := rapid.IntRange(1, exitMax).Draw(t, "exitTenths") // 0.1 to entry-0.1
		exitThreshold := decimal.NewFromInt(int64(exitInt)).Div(decimal.NewFromInt(10))

		// Generate lookback period [5, 50]
		lookback := rapid.IntRange(5, 50).Draw(t, "lookback")

		config := SignalGeneratorConfig{
			EntryThreshold: entryThreshold,
			ExitThreshold:  exitThreshold,
			CooldownMs:     100,
			LookbackPeriod: lookback,
			MAType:         models.MATypeSMA,
		}
		sg := NewSignalGenerator(config)

		// Generate a price history with known mean and stdDev > 0.
		// We use a symmetric distribution around a chosen mean to get a predictable stdDev.
		// prices: mean - spread, mean, mean + spread (repeated to fill lookback)
		// For simplicity: generate `lookback` prices centered on a mean with known stdDev.
		meanCents := rapid.IntRange(1000, 100000).Draw(t, "meanCents") // mean in [10.00, 1000.00]
		mean := decimal.NewFromInt(int64(meanCents)).Div(decimal.NewFromInt(100))

		// Generate a stdDev that's positive and reasonable relative to the mean
		stdDevCents := rapid.IntRange(10, meanCents/5).Draw(t, "stdDevCents") // stdDev in [0.10, mean/5]
		stdDev := decimal.NewFromInt(int64(stdDevCents)).Div(decimal.NewFromInt(100))

		// Create price history: half at (mean - stdDev), half at (mean + stdDev)
		// This gives population stdDev = stdDev exactly.
		lowPrice := mean.Sub(stdDev)
		highPrice := mean.Add(stdDev)

		for i := 0; i < lookback; i++ {
			if i%2 == 0 {
				sg.AddDataPoint(lowPrice, decimal.NewFromInt(1))
			} else {
				sg.AddDataPoint(highPrice, decimal.NewFromInt(1))
			}
		}

		// Compute actual mean from the signal generator's perspective (SMA)
		// For even lookback: n/2 low + n/2 high → mean = (low+high)/2 = mean ✓
		// For odd lookback: (n/2+1) low + n/2 high → slightly below mean
		// We'll compute the actual SMA and stdDev to determine the correct currentPrice.
		data := sg.calculator.buffer.GetAll()
		prices := make([]decimal.Decimal, len(data))
		for i, d := range data {
			prices[i] = d.Price
		}
		actualMean := CalculateSMA(prices)
		actualStdDev := CalculateStdDev(prices, actualMean)

		if actualStdDev.IsZero() {
			return // skip this case (degenerate)
		}

		// Generate currentPrice such that Z < -entryThreshold
		// Z = (currentPrice - actualMean) / actualStdDev < -entryThreshold
		// currentPrice < actualMean - entryThreshold * actualStdDev
		// Use a margin of [1.01, 3.0] * entryThreshold to ensure we're well below
		marginInt := rapid.IntRange(101, 300).Draw(t, "marginPercent")
		margin := decimal.NewFromInt(int64(marginInt)).Div(decimal.NewFromInt(100))
		currentPrice := actualMean.Sub(margin.Mul(entryThreshold).Mul(actualStdDev))

		now := time.Now()
		signal := sg.GenerateSignal(currentPrice, now)

		if signal != models.SignalDirectionBuy {
			actualZ, _ := CalculateZScore(currentPrice, actualMean, actualStdDev)
			t.Fatalf("expected BUY when Z < -entryThreshold, got %v (Z=%v, entry=%v)",
				signal, actualZ, entryThreshold)
		}
	})
}

// TestProperty_MeanReversionSignalDirection_SellWhenZAboveEntry verifies that
// when Z_Score > +entryThreshold, the signal generator produces a SELL signal.
func TestProperty_MeanReversionSignalDirection_SellWhenZAboveEntry(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate valid thresholds
		entryInt := rapid.IntRange(10, 50).Draw(t, "entryTenths")
		entryThreshold := decimal.NewFromInt(int64(entryInt)).Div(decimal.NewFromInt(10))

		exitMax := entryInt - 1
		if exitMax < 1 {
			exitMax = 1
		}
		exitInt := rapid.IntRange(1, exitMax).Draw(t, "exitTenths")
		exitThreshold := decimal.NewFromInt(int64(exitInt)).Div(decimal.NewFromInt(10))

		lookback := rapid.IntRange(5, 50).Draw(t, "lookback")

		config := SignalGeneratorConfig{
			EntryThreshold: entryThreshold,
			ExitThreshold:  exitThreshold,
			CooldownMs:     100,
			LookbackPeriod: lookback,
			MAType:         models.MATypeSMA,
		}
		sg := NewSignalGenerator(config)

		// Generate price history with known mean and stdDev
		meanCents := rapid.IntRange(1000, 100000).Draw(t, "meanCents")
		mean := decimal.NewFromInt(int64(meanCents)).Div(decimal.NewFromInt(100))

		stdDevCents := rapid.IntRange(10, meanCents/5).Draw(t, "stdDevCents")
		stdDev := decimal.NewFromInt(int64(stdDevCents)).Div(decimal.NewFromInt(100))

		lowPrice := mean.Sub(stdDev)
		highPrice := mean.Add(stdDev)

		for i := 0; i < lookback; i++ {
			if i%2 == 0 {
				sg.AddDataPoint(lowPrice, decimal.NewFromInt(1))
			} else {
				sg.AddDataPoint(highPrice, decimal.NewFromInt(1))
			}
		}

		data := sg.calculator.buffer.GetAll()
		prices := make([]decimal.Decimal, len(data))
		for i, d := range data {
			prices[i] = d.Price
		}
		actualMean := CalculateSMA(prices)
		actualStdDev := CalculateStdDev(prices, actualMean)

		if actualStdDev.IsZero() {
			return
		}

		// Generate currentPrice such that Z > +entryThreshold
		// currentPrice > actualMean + entryThreshold * actualStdDev
		marginInt := rapid.IntRange(101, 300).Draw(t, "marginPercent")
		margin := decimal.NewFromInt(int64(marginInt)).Div(decimal.NewFromInt(100))
		currentPrice := actualMean.Add(margin.Mul(entryThreshold).Mul(actualStdDev))

		now := time.Now()
		signal := sg.GenerateSignal(currentPrice, now)

		if signal != models.SignalDirectionSell {
			actualZ, _ := CalculateZScore(currentPrice, actualMean, actualStdDev)
			t.Fatalf("expected SELL when Z > +entryThreshold, got %v (Z=%v, entry=%v)",
				signal, actualZ, entryThreshold)
		}
	})
}

// TestProperty_MeanReversionSignalDirection_CloseWhenZNearZero verifies that
// when |Z_Score| < exitThreshold, the signal generator produces a CLOSE signal.
func TestProperty_MeanReversionSignalDirection_CloseWhenZNearZero(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate valid thresholds
		entryInt := rapid.IntRange(10, 50).Draw(t, "entryTenths")
		entryThreshold := decimal.NewFromInt(int64(entryInt)).Div(decimal.NewFromInt(10))

		exitMax := entryInt - 1
		if exitMax < 1 {
			exitMax = 1
		}
		exitInt := rapid.IntRange(1, exitMax).Draw(t, "exitTenths")
		exitThreshold := decimal.NewFromInt(int64(exitInt)).Div(decimal.NewFromInt(10))

		lookback := rapid.IntRange(5, 50).Draw(t, "lookback")

		config := SignalGeneratorConfig{
			EntryThreshold: entryThreshold,
			ExitThreshold:  exitThreshold,
			CooldownMs:     100,
			LookbackPeriod: lookback,
			MAType:         models.MATypeSMA,
		}
		sg := NewSignalGenerator(config)

		// Generate price history with known mean and stdDev
		meanCents := rapid.IntRange(1000, 100000).Draw(t, "meanCents")
		mean := decimal.NewFromInt(int64(meanCents)).Div(decimal.NewFromInt(100))

		stdDevCents := rapid.IntRange(10, meanCents/5).Draw(t, "stdDevCents")
		stdDev := decimal.NewFromInt(int64(stdDevCents)).Div(decimal.NewFromInt(100))

		lowPrice := mean.Sub(stdDev)
		highPrice := mean.Add(stdDev)

		for i := 0; i < lookback; i++ {
			if i%2 == 0 {
				sg.AddDataPoint(lowPrice, decimal.NewFromInt(1))
			} else {
				sg.AddDataPoint(highPrice, decimal.NewFromInt(1))
			}
		}

		data := sg.calculator.buffer.GetAll()
		prices := make([]decimal.Decimal, len(data))
		for i, d := range data {
			prices[i] = d.Price
		}
		actualMean := CalculateSMA(prices)
		actualStdDev := CalculateStdDev(prices, actualMean)

		if actualStdDev.IsZero() {
			return
		}

		// First, generate a non-NONE signal so the signal generator has a previous state.
		// We need a previous BUY or SELL to transition to CLOSE.
		buyPrice := actualMean.Sub(entryThreshold.Add(decimal.NewFromInt(1)).Mul(actualStdDev))
		now := time.Now()
		sg.GenerateSignal(buyPrice, now)

		// Now generate currentPrice such that |Z| < exitThreshold
		// Z = (currentPrice - actualMean) / actualStdDev
		// |Z| < exitThreshold → currentPrice in (mean - exit*stdDev, mean + exit*stdDev)
		// Use a fraction of exitThreshold: [0, exitThreshold * 0.9]
		fractionInt := rapid.IntRange(0, 90).Draw(t, "fractionPercent") // 0% to 90%
		fraction := decimal.NewFromInt(int64(fractionInt)).Div(decimal.NewFromInt(100))
		zTarget := exitThreshold.Mul(fraction)

		// Randomly choose positive or negative Z within the exit range
		positive := rapid.Bool().Draw(t, "positiveZ")
		var currentPrice decimal.Decimal
		if positive {
			currentPrice = actualMean.Add(zTarget.Mul(actualStdDev))
		} else {
			currentPrice = actualMean.Sub(zTarget.Mul(actualStdDev))
		}

		// Use a later time to avoid cooldown
		laterTime := now.Add(10 * time.Second)
		signal := sg.GenerateSignal(currentPrice, laterTime)

		if signal != models.SignalDirectionClose {
			actualZ, _ := CalculateZScore(currentPrice, actualMean, actualStdDev)
			t.Fatalf("expected CLOSE when |Z| < exitThreshold, got %v (Z=%v, exit=%v)",
				signal, actualZ, exitThreshold)
		}
	})
}

// TestProperty_MeanReversionSignalDirection_RetainInDeadZone verifies that
// when exitThreshold <= |Z_Score| <= entryThreshold (dead zone), the signal generator
// retains the previous signal.
func TestProperty_MeanReversionSignalDirection_RetainInDeadZone(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate valid thresholds with enough gap for dead zone
		entryInt := rapid.IntRange(15, 50).Draw(t, "entryTenths") // 1.5 to 5.0
		entryThreshold := decimal.NewFromInt(int64(entryInt)).Div(decimal.NewFromInt(10))

		// Exit must be < entry, and we need dead zone gap
		exitMax := entryInt - 3 // ensure at least 0.3 gap
		if exitMax < 1 {
			exitMax = 1
		}
		exitInt := rapid.IntRange(1, exitMax).Draw(t, "exitTenths")
		exitThreshold := decimal.NewFromInt(int64(exitInt)).Div(decimal.NewFromInt(10))

		lookback := rapid.IntRange(5, 50).Draw(t, "lookback")

		config := SignalGeneratorConfig{
			EntryThreshold: entryThreshold,
			ExitThreshold:  exitThreshold,
			CooldownMs:     100,
			LookbackPeriod: lookback,
			MAType:         models.MATypeSMA,
		}
		sg := NewSignalGenerator(config)

		// Generate price history with known mean and stdDev
		meanCents := rapid.IntRange(1000, 100000).Draw(t, "meanCents")
		mean := decimal.NewFromInt(int64(meanCents)).Div(decimal.NewFromInt(100))

		stdDevCents := rapid.IntRange(10, meanCents/5).Draw(t, "stdDevCents")
		stdDev := decimal.NewFromInt(int64(stdDevCents)).Div(decimal.NewFromInt(100))

		lowPrice := mean.Sub(stdDev)
		highPrice := mean.Add(stdDev)

		for i := 0; i < lookback; i++ {
			if i%2 == 0 {
				sg.AddDataPoint(lowPrice, decimal.NewFromInt(1))
			} else {
				sg.AddDataPoint(highPrice, decimal.NewFromInt(1))
			}
		}

		data := sg.calculator.buffer.GetAll()
		prices := make([]decimal.Decimal, len(data))
		for i, d := range data {
			prices[i] = d.Price
		}
		actualMean := CalculateSMA(prices)
		actualStdDev := CalculateStdDev(prices, actualMean)

		if actualStdDev.IsZero() {
			return
		}

		// First establish a previous signal (BUY)
		buyPrice := actualMean.Sub(entryThreshold.Add(decimal.NewFromInt(1)).Mul(actualStdDev))
		now := time.Now()
		sig := sg.GenerateSignal(buyPrice, now)
		if sig != models.SignalDirectionBuy {
			return // skip if we couldn't establish previous signal
		}

		// Generate currentPrice in dead zone: exit <= |Z| <= entry
		// Pick a Z value between exitThreshold and entryThreshold
		// Use linear interpolation: Z = exit + fraction * (entry - exit)
		fractionInt := rapid.IntRange(5, 95).Draw(t, "fractionPercent") // 5% to 95% into dead zone
		fraction := decimal.NewFromInt(int64(fractionInt)).Div(decimal.NewFromInt(100))
		zInDeadZone := exitThreshold.Add(fraction.Mul(entryThreshold.Sub(exitThreshold)))

		// Randomly choose positive or negative dead zone
		positive := rapid.Bool().Draw(t, "positiveDeadZone")
		var currentPrice decimal.Decimal
		if positive {
			currentPrice = actualMean.Add(zInDeadZone.Mul(actualStdDev))
		} else {
			currentPrice = actualMean.Sub(zInDeadZone.Mul(actualStdDev))
		}

		// Use a later time to avoid cooldown
		laterTime := now.Add(10 * time.Second)
		signal := sg.GenerateSignal(currentPrice, laterTime)

		// Should retain previous signal (BUY)
		if signal != models.SignalDirectionBuy {
			actualZ, _ := CalculateZScore(currentPrice, actualMean, actualStdDev)
			t.Fatalf("expected BUY retained in dead zone, got %v (|Z|=%v, exit=%v, entry=%v)",
				signal, actualZ.Abs(), exitThreshold, entryThreshold)
		}
	})
}

// TestProperty_MeanReversionSignalDirection_SuppressWhenStdDevZero verifies that
// when standard deviation is zero, signal generation is suppressed and the previous
// signal is retained.
func TestProperty_MeanReversionSignalDirection_SuppressWhenStdDevZero(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate valid thresholds
		entryInt := rapid.IntRange(10, 50).Draw(t, "entryTenths")
		entryThreshold := decimal.NewFromInt(int64(entryInt)).Div(decimal.NewFromInt(10))

		exitMax := entryInt - 1
		if exitMax < 1 {
			exitMax = 1
		}
		exitInt := rapid.IntRange(1, exitMax).Draw(t, "exitTenths")
		exitThreshold := decimal.NewFromInt(int64(exitInt)).Div(decimal.NewFromInt(10))

		lookback := rapid.IntRange(5, 50).Draw(t, "lookback")

		config := SignalGeneratorConfig{
			EntryThreshold: entryThreshold,
			ExitThreshold:  exitThreshold,
			CooldownMs:     100,
			LookbackPeriod: lookback,
			MAType:         models.MATypeSMA,
		}

		// First create a signal generator with varied data to establish a BUY signal
		sgSetup := NewSignalGenerator(config)

		meanCents := rapid.IntRange(1000, 100000).Draw(t, "meanCents")
		mean := decimal.NewFromInt(int64(meanCents)).Div(decimal.NewFromInt(100))

		stdDevCents := rapid.IntRange(10, meanCents/5).Draw(t, "stdDevCents")
		stdDev := decimal.NewFromInt(int64(stdDevCents)).Div(decimal.NewFromInt(100))

		lowPrice := mean.Sub(stdDev)
		highPrice := mean.Add(stdDev)

		for i := 0; i < lookback; i++ {
			if i%2 == 0 {
				sgSetup.AddDataPoint(lowPrice, decimal.NewFromInt(1))
			} else {
				sgSetup.AddDataPoint(highPrice, decimal.NewFromInt(1))
			}
		}

		// Establish a BUY signal
		data := sgSetup.calculator.buffer.GetAll()
		prices := make([]decimal.Decimal, len(data))
		for i, d := range data {
			prices[i] = d.Price
		}
		actualMean := CalculateSMA(prices)
		actualStdDev := CalculateStdDev(prices, actualMean)
		if actualStdDev.IsZero() {
			return
		}

		now := time.Now()
		buyPrice := actualMean.Sub(entryThreshold.Add(decimal.NewFromInt(1)).Mul(actualStdDev))
		sig := sgSetup.GenerateSignal(buyPrice, now)
		if sig != models.SignalDirectionBuy {
			return // skip if couldn't establish signal
		}

		// Now overwrite the buffer with identical prices → stdDev becomes 0
		constantPrice := rapid.IntRange(100, 10000).Draw(t, "constantPriceCents")
		constP := decimal.NewFromInt(int64(constantPrice)).Div(decimal.NewFromInt(100))
		for i := 0; i < lookback; i++ {
			sgSetup.AddDataPoint(constP, decimal.NewFromInt(1))
		}

		// Generate signal with any price — stdDev is 0, should retain BUY
		anyPrice := decimal.NewFromInt(int64(rapid.IntRange(1, 100000).Draw(t, "anyPrice")))
		laterTime := now.Add(10 * time.Second)
		signal := sgSetup.GenerateSignal(anyPrice, laterTime)

		if signal != models.SignalDirectionBuy {
			t.Fatalf("expected BUY retained when stdDev=0, got %v", signal)
		}
	})
}
