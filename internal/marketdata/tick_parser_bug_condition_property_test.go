package marketdata

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirements 1.4, 1.5, 2.4, 2.5**
//
// Bug Condition Exploration — Tick Parser Zero SeqId Bypass
//
// For tick sequences where seqId=0 (tickers channel data that lacks sequence IDs),
// ALL ticks (including second and subsequent) SHOULD pass sequence validation.
// On unfixed code, the second tick fails with "sequence out of order: current=0, previous=0"
// because the validation rule `tick.SequenceId <= prev` evaluates `0 <= 0` as true.
//
// isBugCondition_TickParser: X.seqId = 0
func TestBugCondition_TickParser_ZeroSeqIdAllTicksPass(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		now := time.Now()

		// Track any validation failures
		var failureSymbol, failureReason string
		var failureCount int
		parser := NewParser(func(symbol, reason string) {
			failureSymbol = symbol
			failureReason = reason
			failureCount++
		})
		parser.timeNow = func() time.Time { return now }

		// Generate N consecutive ticks with seqId=0 (simulating tickers channel)
		// N >= 2 to trigger the bug (first tick passes, second fails on unfixed code)
		numTicks := rapid.IntRange(2, 10).Draw(rt, "numTicks")

		// Generate valid price data for the ticks
		bidCents := rapid.Int64Range(100, 4999999900).Draw(rt, "bidCents")
		bestBid := decimal.NewFromInt(bidCents).Div(decimal.NewFromInt(100))
		bestAsk := decimal.NewFromInt(bidCents + 100).Div(decimal.NewFromInt(100))
		lastPrice := bestBid.Add(decimal.NewFromInt(50).Div(decimal.NewFromInt(100))) // midpoint

		// Use a fixed symbol for all ticks (simulates same subscription)
		symbol := "WIF-USDT"

		for i := 0; i < numTicks; i++ {
			// Each tick has seqId=0 (tickers channel omits this field)
			// Slight timestamp variation to keep within ±5s tolerance
			tickTime := now.Add(time.Duration(i) * time.Millisecond)

			tick := &models.TickData{
				Symbol:     symbol,
				Timestamp:  tickTime.UnixMicro(),
				LastPrice:  lastPrice,
				BestBid:    bestBid,
				BestAsk:    bestAsk,
				BidSize:    decimal.NewFromFloat(1.0),
				AskSize:    decimal.NewFromFloat(1.0),
				Volume24h:  decimal.NewFromFloat(1000.0),
				SequenceId: 0, // Zero — tickers channel has no seqId
			}

			err := parser.ParseTickDirect(tick)
			if err != nil {
				rt.Fatalf("BUG CONFIRMED: tick #%d (seqId=0) rejected: %v\n"+
					"  symbol=%s, failureSymbol=%s, failureReason=%s, totalFailures=%d\n"+
					"  Expected: all ticks with seqId=0 should pass sequence validation",
					i+1, err, symbol, failureSymbol, failureReason, failureCount)
			}
		}

		// Verify no validation failures occurred
		if failureCount > 0 {
			rt.Fatalf("BUG CONFIRMED: %d validation failures for seqId=0 ticks\n"+
				"  symbol=%s, reason=%s",
				failureCount, failureSymbol, failureReason)
		}
	})
}
