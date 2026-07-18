package marketdata

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirements 3.3, 3.4, 3.5**
//
// Preservation Property — Non-Zero SeqId Validation Unchanged
//
// For ticks with seqId > 0:
//   - Accept iff seqId is strictly greater than previous for that symbol
//   - Reject otherwise (seqId <= previous)
//
// This test PASSES on unfixed code (confirms baseline monotonic validation to preserve).
func TestPreservation_TickParser_NonZeroSeqIdMonotonicValidation(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		now := time.Now()

		var failureCount int
		var lastFailureReason string
		parser := NewParser(func(symbol, reason string) {
			failureCount++
			lastFailureReason = reason
		})
		parser.timeNow = func() time.Time { return now }

		// Generate a sequence of ticks with seqId > 0
		// First tick establishes the baseline, subsequent ticks test monotonic behavior
		numTicks := rapid.IntRange(2, 20).Draw(rt, "numTicks")

		// Generate valid price data
		bidCents := rapid.Int64Range(100, 4999999900).Draw(rt, "bidCents")
		bestBid := decimal.NewFromInt(bidCents).Div(decimal.NewFromInt(100))
		bestAsk := decimal.NewFromInt(bidCents + 100).Div(decimal.NewFromInt(100))
		lastPrice := bestBid.Add(decimal.NewFromInt(50).Div(decimal.NewFromInt(100)))

		symbol := "DOGE-USDT"

		// Generate a starting seqId > 0
		startSeqId := rapid.Int64Range(1, 1000000).Draw(rt, "startSeqId")

		// Generate sequence of deltas: positive = increasing (should accept), zero or negative = non-increasing (should reject)
		// First tick always passes (no previous stored)
		firstTick := &models.TickData{
			Symbol:     symbol,
			Timestamp:  now.UnixMicro(),
			LastPrice:  lastPrice,
			BestBid:    bestBid,
			BestAsk:    bestAsk,
			BidSize:    decimal.NewFromFloat(1.0),
			AskSize:    decimal.NewFromFloat(1.0),
			Volume24h:  decimal.NewFromFloat(1000.0),
			SequenceId: startSeqId,
		}

		err := parser.ParseTickDirect(firstTick)
		if err != nil {
			rt.Fatalf("First tick (seqId=%d) should be accepted (no previous): %v", startSeqId, err)
		}

		prevSeqId := startSeqId

		for i := 1; i < numTicks; i++ {
			// Decide whether this tick should be accepted (increasing) or rejected (non-increasing)
			shouldAccept := rapid.Bool().Draw(rt, fmt.Sprintf("accept_%d", i))

			var nextSeqId int64
			if shouldAccept {
				// Generate strictly increasing seqId
				increment := rapid.Int64Range(1, 1000).Draw(rt, fmt.Sprintf("increment_%d", i))
				nextSeqId = prevSeqId + increment
			} else {
				// Generate non-increasing seqId (equal or less than previous)
				// Could be equal to prevSeqId, or any value <= prevSeqId but > 0
				if prevSeqId <= 1 {
					nextSeqId = 1 // minimum seqId > 0
				} else {
					nextSeqId = rapid.Int64Range(1, prevSeqId).Draw(rt, fmt.Sprintf("nonIncr_%d", i))
				}
			}

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
				SequenceId: nextSeqId,
			}

			failuresBefore := failureCount
			err := parser.ParseTickDirect(tick)

			if shouldAccept {
				// Strictly increasing seqId → must be accepted
				if err != nil {
					rt.Fatalf("PRESERVATION VIOLATED: tick #%d with seqId=%d > prev=%d should be accepted, got error: %v",
						i+1, nextSeqId, prevSeqId, err)
				}
				// Update prevSeqId for next iteration
				prevSeqId = nextSeqId
			} else {
				// Non-increasing seqId → must be rejected
				if err == nil {
					rt.Fatalf("PRESERVATION VIOLATED: tick #%d with seqId=%d <= prev=%d should be rejected, but was accepted",
						i+1, nextSeqId, prevSeqId)
				}
				// Verify the failure was recorded
				if failureCount <= failuresBefore {
					rt.Fatalf("PRESERVATION VIOLATED: rejection of seqId=%d did not increment failure counter", nextSeqId)
				}
				// Verify failure reason mentions sequence
				if lastFailureReason == "" {
					rt.Fatalf("PRESERVATION VIOLATED: rejection had empty failure reason")
				}
				// prevSeqId stays the same (rejected tick doesn't update stored sequence)
			}
		}
	})
}

// **Validates: Requirements 3.3, 3.4**
//
// Preservation Property — Strictly Increasing SeqId Always Accepted
//
// For a sequence of ticks with seqId > 0 that are all strictly increasing,
// every single tick must be accepted. This is a simpler focused property.
func TestPreservation_TickParser_StrictlyIncreasingSeqIdAlwaysAccepted(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		now := time.Now()

		var failureCount int
		parser := NewParser(func(symbol, reason string) {
			failureCount++
		})
		parser.timeNow = func() time.Time { return now }

		numTicks := rapid.IntRange(2, 50).Draw(rt, "numTicks")

		// Generate valid price data
		bidCents := rapid.Int64Range(100, 4999999900).Draw(rt, "bidCents")
		bestBid := decimal.NewFromInt(bidCents).Div(decimal.NewFromInt(100))
		bestAsk := decimal.NewFromInt(bidCents + 100).Div(decimal.NewFromInt(100))
		lastPrice := bestBid.Add(decimal.NewFromInt(50).Div(decimal.NewFromInt(100)))

		symbol := "WIF-USDT"

		// Generate strictly increasing sequence IDs
		currentSeqId := rapid.Int64Range(1, 100000).Draw(rt, "startSeqId")

		for i := 0; i < numTicks; i++ {
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
				SequenceId: currentSeqId,
			}

			err := parser.ParseTickDirect(tick)
			if err != nil {
				rt.Fatalf("PRESERVATION VIOLATED: strictly increasing seqId=%d (tick #%d) rejected: %v",
					currentSeqId, i+1, err)
			}

			// Increment for next tick
			increment := rapid.Int64Range(1, 100).Draw(rt, fmt.Sprintf("incr_%d", i))
			currentSeqId += increment
		}

		if failureCount != 0 {
			rt.Fatalf("PRESERVATION VIOLATED: %d failures recorded for strictly increasing sequence", failureCount)
		}
	})
}
