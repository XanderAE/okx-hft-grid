// Property 23: Extreme Market Condition Detection
// **Validates: Requirement 14.4**
//
// Within 1 minute, if price change exceeds 5% or spread exceeds 3x the rolling
// 5-minute average spread, the condition is detected as extreme market.
package risk

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// TestProperty_ExtremeMarket_PriceChange5PercentDetected verifies that when price
// changes by more than 5% within 1 minute, CheckPriceChange returns true.
func TestProperty_ExtremeMarket_PriceChange5PercentDetected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		emd := NewExtremeMarketDetector()
		symbol := "BTC-USDT"

		// Record initial price
		basePrice := rapid.Float64Range(10.0, 100000.0).Draw(t, "basePrice")
		now := time.Now()
		emd.RecordPrice(symbol, decimal.NewFromFloat(basePrice), now)

		// Generate a price change > 5% (either up or down)
		direction := rapid.SampledFrom([]float64{1.0, -1.0}).Draw(t, "direction")
		changePct := rapid.Float64Range(0.0501, 0.50).Draw(t, "changePct")
		newPrice := basePrice * (1.0 + direction*changePct)

		// Record some intermediate prices within the window to ensure history is populated
		emd.RecordPrice(symbol, decimal.NewFromFloat(basePrice), now.Add(10*time.Second))

		// Check with the new price (still within 1-minute window)
		detected := emd.CheckPriceChange(symbol, decimal.NewFromFloat(newPrice))

		if !detected {
			t.Fatalf("expected extreme price change detection: base=%f, new=%f, change=%.2f%%",
				basePrice, newPrice, changePct*100)
		}
	})
}

// TestProperty_ExtremeMarket_PriceChangeBelow5PercentNotDetected verifies that when
// price changes by less than 5% within 1 minute, CheckPriceChange returns false.
func TestProperty_ExtremeMarket_PriceChangeBelow5PercentNotDetected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		emd := NewExtremeMarketDetector()
		symbol := "ETH-USDT"

		basePrice := rapid.Float64Range(10.0, 100000.0).Draw(t, "basePrice")
		now := time.Now()
		emd.RecordPrice(symbol, decimal.NewFromFloat(basePrice), now)

		// Generate a price change < 5%
		direction := rapid.SampledFrom([]float64{1.0, -1.0}).Draw(t, "direction")
		changePct := rapid.Float64Range(0.001, 0.0499).Draw(t, "changePct")
		newPrice := basePrice * (1.0 + direction*changePct)

		detected := emd.CheckPriceChange(symbol, decimal.NewFromFloat(newPrice))

		if detected {
			t.Fatalf("should NOT detect extreme price when change is %.2f%% (<5%%): base=%f, new=%f",
				changePct*100, basePrice, newPrice)
		}
	})
}

// TestProperty_ExtremeMarket_SpreadExceeds3xAverageDetected verifies that when
// the current spread exceeds 3x the rolling 5-minute average, CheckSpreadAnomaly returns true.
func TestProperty_ExtremeMarket_SpreadExceeds3xAverageDetected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		emd := NewExtremeMarketDetector()
		symbol := "SOL-USDT"

		// Build a 5-minute spread history with a uniform spread so average is predictable
		baseSpread := rapid.Float64Range(0.01, 100.0).Draw(t, "baseSpread")
		now := time.Now()
		numEntries := rapid.IntRange(5, 20).Draw(t, "numEntries")

		for i := 0; i < numEntries; i++ {
			ts := now.Add(-time.Duration(numEntries-i) * 10 * time.Second)
			emd.RecordSpread(symbol, decimal.NewFromFloat(baseSpread), ts)
		}

		// With uniform entries, average = baseSpread exactly.
		// Generate a current spread that clearly exceeds 3× baseSpread.
		multiplier := rapid.Float64Range(3.01, 10.0).Draw(t, "multiplier")
		currentSpread := baseSpread * multiplier

		detected := emd.CheckSpreadAnomaly(symbol, decimal.NewFromFloat(currentSpread))

		if !detected {
			t.Fatalf("expected spread anomaly detection: avgSpread=%f, current=%f (%.2fx)",
				baseSpread, currentSpread, multiplier)
		}
	})
}

// TestProperty_ExtremeMarket_SpreadBelow3xAverageNotDetected verifies that when
// the current spread is at or below 3x the rolling 5-minute average, no anomaly is detected.
func TestProperty_ExtremeMarket_SpreadBelow3xAverageNotDetected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		emd := NewExtremeMarketDetector()
		symbol := "DOGE-USDT"

		// Build a 5-minute spread history with a uniform spread (so average = that value)
		baseSpread := rapid.Float64Range(0.1, 50.0).Draw(t, "baseSpread")
		now := time.Now()
		numEntries := rapid.IntRange(5, 20).Draw(t, "numEntries")

		for i := 0; i < numEntries; i++ {
			ts := now.Add(-time.Duration(numEntries-i) * 10 * time.Second)
			emd.RecordSpread(symbol, decimal.NewFromFloat(baseSpread), ts)
		}

		// Generate a current spread that is at or below 3× the average
		// Since all recorded spreads are the same, average = baseSpread
		multiplier := rapid.Float64Range(0.1, 2.99).Draw(t, "multiplier")
		currentSpread := baseSpread * multiplier

		detected := emd.CheckSpreadAnomaly(symbol, decimal.NewFromFloat(currentSpread))

		if detected {
			t.Fatalf("should NOT detect spread anomaly when spread is %.1fx average: base=%f, current=%f",
				multiplier, baseSpread, currentSpread)
		}
	})
}

// TestProperty_ExtremeMarket_DetectTriggersBothConditions verifies that the
// Detect method triggers when either price or spread condition is met.
func TestProperty_ExtremeMarket_DetectTriggersBothConditions(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		emd := NewExtremeMarketDetector()
		symbol := "AVAX-USDT"
		now := time.Now()

		// Set up price history with a large change
		basePrice := rapid.Float64Range(10.0, 10000.0).Draw(t, "basePrice")
		emd.RecordPrice(symbol, decimal.NewFromFloat(basePrice), now.Add(-30*time.Second))

		// New price with >5% change
		newPrice := basePrice * 1.06

		// Set up spread history
		baseSpread := rapid.Float64Range(0.1, 10.0).Draw(t, "baseSpread")
		for i := 0; i < 10; i++ {
			ts := now.Add(-time.Duration(10-i) * 20 * time.Second)
			emd.RecordSpread(symbol, decimal.NewFromFloat(baseSpread), ts)
		}

		// Current spread well above 3x
		currentSpread := baseSpread * 4.0

		detected := emd.Detect(symbol, decimal.NewFromFloat(newPrice), decimal.NewFromFloat(currentSpread))

		if !detected {
			t.Fatal("expected Detect to return true when both conditions are met")
		}
	})
}
