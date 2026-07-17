package marketdata

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirements 1.3, 1.4, 1.5, 9.1, 9.2, 9.3, 9.4**

// TestProperty_TickValidation_ValidTickAlwaysPasses tests that for any TickData
// with lastPrice/bestBid/bestAsk > 0 and <= 99,999,999.99, bestBid < bestAsk,
// timestamp within ±5s, and sequenceId > previous, validation PASSES.
func TestProperty_TickValidation_ValidTickAlwaysPasses(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		now := time.Now()

		parser := NewParser(func(symbol, reason string) {
			t.Fatalf("DATA_INVALID callback should not fire for valid tick: symbol=%s reason=%s", symbol, reason)
		})
		parser.timeNow = func() time.Time { return now }

		// Generate valid prices: > 0 and <= 99,999,999.99
		// Use cents to avoid floating-point issues, then divide by 100.
		lastPriceCents := rapid.Int64Range(1, 9999999999).Draw(t, "lastPriceCents")
		bidPriceCents := rapid.Int64Range(1, 9999999998).Draw(t, "bidPriceCents")

		// Ensure bestAsk > bestBid by adding at least 1 cent
		askMinCents := bidPriceCents + 1
		askMaxCents := int64(9999999999)
		if askMinCents > askMaxCents {
			askMinCents = askMaxCents
		}
		askPriceCents := rapid.Int64Range(askMinCents, askMaxCents).Draw(t, "askPriceCents")

		lastPrice := decimal.NewFromInt(lastPriceCents).Div(decimal.NewFromInt(100))
		bestBid := decimal.NewFromInt(bidPriceCents).Div(decimal.NewFromInt(100))
		bestAsk := decimal.NewFromInt(askPriceCents).Div(decimal.NewFromInt(100))

		// Generate timestamp within ±5s of now
		offsetMs := rapid.Int64Range(-4999, 4999).Draw(t, "offsetMs")
		tickTime := now.Add(time.Duration(offsetMs) * time.Millisecond)

		// Generate a sequenceId > 0 (first tick for a new symbol, no previous)
		seqId := rapid.Int64Range(1, 1000000).Draw(t, "seqId")

		// Generate a unique symbol per test to avoid sequence conflicts
		symbolIdx := rapid.IntRange(0, 9999).Draw(t, "symbolIdx")
		symbol := rapid.StringMatching(`[A-Z]{2,5}-USDT`).Draw(t, "symbol")
		_ = symbolIdx // just used for entropy

		tick := &models.TickData{
			Symbol:     symbol,
			Timestamp:  tickTime.UnixMicro(),
			LastPrice:  lastPrice,
			BestBid:    bestBid,
			BestAsk:    bestAsk,
			BidSize:    decimal.NewFromFloat(1.0),
			AskSize:    decimal.NewFromFloat(1.0),
			Volume24h:  decimal.NewFromFloat(1000.0),
			SequenceId: seqId,
		}

		err := parser.ParseTickDirect(tick)
		if err != nil {
			t.Fatalf("valid tick should pass validation: %v (lastPrice=%s, bestBid=%s, bestAsk=%s, offset=%dms, seqId=%d)",
				err, lastPrice.String(), bestBid.String(), bestAsk.String(), offsetMs, seqId)
		}
	})
}

// TestProperty_TickValidation_InvalidPriceAlwaysFails tests that for any TickData
// with lastPrice <= 0 or > 99,999,999.99, validation FAILS.
func TestProperty_TickValidation_InvalidPriceAlwaysFails(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		now := time.Now()

		var invalidFired bool
		parser := NewParser(func(symbol, reason string) {
			invalidFired = true
		})
		parser.timeNow = func() time.Time { return now }

		// Choose which type of invalid price to generate
		invalidType := rapid.IntRange(0, 2).Draw(t, "invalidType")

		var lastPrice decimal.Decimal
		switch invalidType {
		case 0:
			// Negative price
			negativeCents := rapid.Int64Range(1, 9999999999).Draw(t, "negativeCents")
			lastPrice = decimal.NewFromInt(-negativeCents).Div(decimal.NewFromInt(100))
		case 1:
			// Zero price
			lastPrice = decimal.Zero
		case 2:
			// Price exceeding 99,999,999.99
			excessCents := rapid.Int64Range(10000000000, 99999999999).Draw(t, "excessCents")
			lastPrice = decimal.NewFromInt(excessCents).Div(decimal.NewFromInt(100))
		}

		// Use valid values for all other fields
		bidCents := rapid.Int64Range(1, 4999999999).Draw(t, "bidCents")
		bestBid := decimal.NewFromInt(bidCents).Div(decimal.NewFromInt(100))
		bestAsk := decimal.NewFromInt(bidCents + 100).Div(decimal.NewFromInt(100))

		tick := &models.TickData{
			Symbol:     "TEST-USDT",
			Timestamp:  now.UnixMicro(),
			LastPrice:  lastPrice,
			BestBid:    bestBid,
			BestAsk:    bestAsk,
			BidSize:    decimal.NewFromFloat(1.0),
			AskSize:    decimal.NewFromFloat(1.0),
			Volume24h:  decimal.NewFromFloat(1000.0),
			SequenceId: rapid.Int64Range(1, 1000000).Draw(t, "seqId"),
		}

		err := parser.ParseTickDirect(tick)
		if err == nil {
			t.Fatalf("tick with invalid lastPrice=%s should fail validation", lastPrice.String())
		}
		if !invalidFired {
			t.Fatal("DATA_INVALID callback should have fired for invalid price")
		}
	})
}

// TestProperty_TickValidation_CrossedBookAlwaysFails tests that for any TickData
// with bestBid >= bestAsk (crossed book), validation FAILS.
func TestProperty_TickValidation_CrossedBookAlwaysFails(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		now := time.Now()

		var invalidFired bool
		parser := NewParser(func(symbol, reason string) {
			invalidFired = true
		})
		parser.timeNow = func() time.Time { return now }

		// Generate bestBid >= bestAsk
		crossType := rapid.IntRange(0, 1).Draw(t, "crossType")

		var bestBid, bestAsk decimal.Decimal
		bidCents := rapid.Int64Range(100, 9999999999).Draw(t, "bidCents")

		switch crossType {
		case 0:
			// bestBid == bestAsk (equal, still crossed)
			bestBid = decimal.NewFromInt(bidCents).Div(decimal.NewFromInt(100))
			bestAsk = bestBid
		case 1:
			// bestBid > bestAsk
			bestBid = decimal.NewFromInt(bidCents).Div(decimal.NewFromInt(100))
			askCents := rapid.Int64Range(1, bidCents-1).Draw(t, "askCents")
			bestAsk = decimal.NewFromInt(askCents).Div(decimal.NewFromInt(100))
		}

		// Use a valid lastPrice
		lastPrice := decimal.NewFromInt(rapid.Int64Range(1, 9999999999).Draw(t, "lastPriceCents")).Div(decimal.NewFromInt(100))

		tick := &models.TickData{
			Symbol:     "CROSS-USDT",
			Timestamp:  now.UnixMicro(),
			LastPrice:  lastPrice,
			BestBid:    bestBid,
			BestAsk:    bestAsk,
			BidSize:    decimal.NewFromFloat(1.0),
			AskSize:    decimal.NewFromFloat(1.0),
			Volume24h:  decimal.NewFromFloat(1000.0),
			SequenceId: rapid.Int64Range(1, 1000000).Draw(t, "seqId"),
		}

		err := parser.ParseTickDirect(tick)
		if err == nil {
			t.Fatalf("crossed book tick should fail validation: bestBid=%s, bestAsk=%s",
				bestBid.String(), bestAsk.String())
		}
		if !invalidFired {
			t.Fatal("DATA_INVALID callback should have fired for crossed book")
		}
	})
}

// TestProperty_TickValidation_TimestampOutOfRangeAlwaysFails tests that for any
// TickData with timestamp outside ±5s of server time, validation FAILS.
func TestProperty_TickValidation_TimestampOutOfRangeAlwaysFails(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		now := time.Now()

		var invalidFired bool
		parser := NewParser(func(symbol, reason string) {
			invalidFired = true
		})
		parser.timeNow = func() time.Time { return now }

		// Generate timestamp outside ±5s
		// Choose whether it's too old or too far in the future
		direction := rapid.IntRange(0, 1).Draw(t, "direction")
		var tickTime time.Time
		switch direction {
		case 0:
			// Too old: more than 5 seconds in the past
			extraMs := rapid.Int64Range(5001, 60000).Draw(t, "extraMsPast")
			tickTime = now.Add(-time.Duration(extraMs) * time.Millisecond)
		case 1:
			// Too far in future: more than 5 seconds ahead
			extraMs := rapid.Int64Range(5001, 60000).Draw(t, "extraMsFuture")
			tickTime = now.Add(time.Duration(extraMs) * time.Millisecond)
		}

		// Use valid values for all other fields
		bidCents := rapid.Int64Range(1, 4999999999).Draw(t, "bidCents")
		bestBid := decimal.NewFromInt(bidCents).Div(decimal.NewFromInt(100))
		bestAsk := decimal.NewFromInt(bidCents + 100).Div(decimal.NewFromInt(100))
		lastPrice := decimal.NewFromInt(rapid.Int64Range(1, 9999999999).Draw(t, "lastPriceCents")).Div(decimal.NewFromInt(100))

		tick := &models.TickData{
			Symbol:     "TIME-USDT",
			Timestamp:  tickTime.UnixMicro(),
			LastPrice:  lastPrice,
			BestBid:    bestBid,
			BestAsk:    bestAsk,
			BidSize:    decimal.NewFromFloat(1.0),
			AskSize:    decimal.NewFromFloat(1.0),
			Volume24h:  decimal.NewFromFloat(1000.0),
			SequenceId: rapid.Int64Range(1, 1000000).Draw(t, "seqId"),
		}

		err := parser.ParseTickDirect(tick)
		if err == nil {
			t.Fatalf("tick with out-of-range timestamp should fail: tickTime=%v, now=%v, diff=%v",
				tickTime, now, now.Sub(tickTime))
		}
		if !invalidFired {
			t.Fatal("DATA_INVALID callback should have fired for out-of-range timestamp")
		}
	})
}

// TestProperty_TickValidation_SequenceIdNotIncreasingAlwaysFails tests that for any
// TickData with sequenceId <= previous sequenceId for same symbol, validation FAILS.
func TestProperty_TickValidation_SequenceIdNotIncreasingAlwaysFails(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		now := time.Now()

		var invalidFired bool
		parser := NewParser(func(symbol, reason string) {
			invalidFired = true
		})
		parser.timeNow = func() time.Time { return now }

		symbol := "SEQ-USDT"

		// First, establish a known previous sequenceId by sending a valid tick
		prevSeqId := rapid.Int64Range(100, 999999).Draw(t, "prevSeqId")
		bidCents := rapid.Int64Range(1, 4999999999).Draw(t, "bidCents")
		bestBid := decimal.NewFromInt(bidCents).Div(decimal.NewFromInt(100))
		bestAsk := decimal.NewFromInt(bidCents + 100).Div(decimal.NewFromInt(100))
		lastPrice := decimal.NewFromInt(rapid.Int64Range(1, 9999999999).Draw(t, "lastPriceCents")).Div(decimal.NewFromInt(100))

		firstTick := &models.TickData{
			Symbol:     symbol,
			Timestamp:  now.UnixMicro(),
			LastPrice:  lastPrice,
			BestBid:    bestBid,
			BestAsk:    bestAsk,
			BidSize:    decimal.NewFromFloat(1.0),
			AskSize:    decimal.NewFromFloat(1.0),
			Volume24h:  decimal.NewFromFloat(1000.0),
			SequenceId: prevSeqId,
		}

		err := parser.ParseTickDirect(firstTick)
		if err != nil {
			t.Fatalf("first tick should pass: %v", err)
		}

		// Now send a tick with sequenceId <= prevSeqId
		invalidSeqType := rapid.IntRange(0, 1).Draw(t, "invalidSeqType")
		var badSeqId int64
		switch invalidSeqType {
		case 0:
			// Same sequenceId
			badSeqId = prevSeqId
		case 1:
			// Lower sequenceId
			if prevSeqId > 1 {
				badSeqId = rapid.Int64Range(1, prevSeqId-1).Draw(t, "lowerSeqId")
			} else {
				badSeqId = prevSeqId
			}
		}

		invalidFired = false // Reset for the second tick
		secondTick := &models.TickData{
			Symbol:     symbol,
			Timestamp:  now.UnixMicro(),
			LastPrice:  lastPrice,
			BestBid:    bestBid,
			BestAsk:    bestAsk,
			BidSize:    decimal.NewFromFloat(1.0),
			AskSize:    decimal.NewFromFloat(1.0),
			Volume24h:  decimal.NewFromFloat(1000.0),
			SequenceId: badSeqId,
		}

		err = parser.ParseTickDirect(secondTick)
		if err == nil {
			t.Fatalf("tick with non-increasing seqId should fail: prevSeqId=%d, badSeqId=%d",
				prevSeqId, badSeqId)
		}
		if !invalidFired {
			t.Fatal("DATA_INVALID callback should have fired for non-increasing sequenceId")
		}
	})
}

// TestProperty_TickValidation_FailureCounterAndCallback tests that on any validation
// failure, the failure counter increments and DATA_INVALID callback fires.
func TestProperty_TickValidation_FailureCounterAndCallback(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		now := time.Now()

		var callbackCount int64
		parser := NewParser(func(symbol, reason string) {
			callbackCount++
		})
		parser.timeNow = func() time.Time { return now }

		// Generate a number of invalid ticks
		numInvalid := rapid.IntRange(1, 20).Draw(t, "numInvalid")
		counterBefore := parser.GetValidationFailureCount()
		callbackBefore := callbackCount

		for i := 0; i < numInvalid; i++ {
			// Generate an invalid tick (zero lastPrice, guaranteed to fail)
			tick := &models.TickData{
				Symbol:     "FAIL-USDT",
				Timestamp:  now.UnixMicro(),
				LastPrice:  decimal.Zero,
				BestBid:    decimal.NewFromFloat(100.0),
				BestAsk:    decimal.NewFromFloat(101.0),
				BidSize:    decimal.NewFromFloat(1.0),
				AskSize:    decimal.NewFromFloat(1.0),
				Volume24h:  decimal.NewFromFloat(1000.0),
				SequenceId: int64(i + 1),
			}
			parser.ParseTickDirect(tick)
		}

		counterAfter := parser.GetValidationFailureCount()
		callbackAfter := callbackCount

		// Property: failure counter increments by exactly numInvalid
		counterDiff := counterAfter - counterBefore
		if counterDiff != int64(numInvalid) {
			t.Fatalf("failure counter should increment by %d, got increment of %d (before=%d, after=%d)",
				numInvalid, counterDiff, counterBefore, counterAfter)
		}

		// Property: callback fires exactly numInvalid times
		callbackDiff := callbackAfter - callbackBefore
		if callbackDiff != int64(numInvalid) {
			t.Fatalf("DATA_INVALID callback should fire %d times, got %d (before=%d, after=%d)",
				numInvalid, callbackDiff, callbackBefore, callbackAfter)
		}
	})
}
