package orderbook

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// **Validates: Requirements 2.9, 2.10**

// genSeqPriceLevel generates a random PriceLevel with positive price and quantity.
func genSeqPriceLevel(t *rapid.T, prefix string) PriceLevel {
	price := rapid.Float64Range(0.01, 99999.0).Draw(t, prefix+"_price")
	qty := rapid.Float64Range(0.01, 1000.0).Draw(t, prefix+"_qty")
	return PriceLevel{
		Price:    decimal.NewFromFloat(price),
		Quantity: decimal.NewFromFloat(qty),
	}
}

// genSnapshot generates a valid OrderBookSnapshot with the given sequenceID.
func genSnapshot(t *rapid.T, symbol string, seqID int64) *OrderBookSnapshot {
	numBids := rapid.IntRange(1, 10).Draw(t, "numBids")
	numAsks := rapid.IntRange(1, 10).Draw(t, "numAsks")

	bids := make([]PriceLevel, numBids)
	for i := 0; i < numBids; i++ {
		bids[i] = genSeqPriceLevel(t, "bid")
	}

	asks := make([]PriceLevel, numAsks)
	for i := 0; i < numAsks; i++ {
		asks[i] = genSeqPriceLevel(t, "ask")
	}

	return &OrderBookSnapshot{
		Symbol:     symbol,
		Bids:       bids,
		Asks:       asks,
		SequenceID: seqID,
		Timestamp:  1000,
	}
}

// genDelta generates an OrderBookDelta with the given sequenceID.
func genDelta(t *rapid.T, symbol string, seqID int64) *OrderBookDelta {
	numBidUpdates := rapid.IntRange(0, 5).Draw(t, "numBidUpdates")
	numAskUpdates := rapid.IntRange(0, 5).Draw(t, "numAskUpdates")

	bids := make([]PriceLevel, numBidUpdates)
	for i := 0; i < numBidUpdates; i++ {
		bids[i] = genSeqPriceLevel(t, "deltaBid")
	}

	asks := make([]PriceLevel, numAskUpdates)
	for i := 0; i < numAskUpdates; i++ {
		asks[i] = genSeqPriceLevel(t, "deltaAsk")
	}

	return &OrderBookDelta{
		Symbol:     symbol,
		Bids:       bids,
		Asks:       asks,
		SequenceID: seqID,
		Timestamp:  2000,
	}
}

// TestProperty_SequenceGap_ConsecutiveUpdatesSucceed tests that for any random initial
// sequenceId and any number of consecutive incremental updates (seqId = lastSeqId + 1),
// all updates are accepted without error and the book remains in a valid (non-resyncing) state.
func TestProperty_SequenceGap_ConsecutiveUpdatesSucceed(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		symbol := "TEST-USDT"
		initialSeqID := rapid.Int64Range(0, 100000).Draw(t, "initialSeqID")
		numUpdates := rapid.IntRange(1, 50).Draw(t, "numUpdates")

		ob := NewLocalOrderBook()

		// Apply initial snapshot with the generated sequenceID
		snapshot := genSnapshot(t, symbol, initialSeqID)
		err := ob.UpdateFromSnapshot(symbol, snapshot)
		if err != nil {
			t.Fatalf("failed to apply snapshot: %v", err)
		}

		// Apply consecutive incremental updates
		for i := 0; i < numUpdates; i++ {
			expectedSeq := initialSeqID + int64(i) + 1
			delta := genDelta(t, symbol, expectedSeq)

			err := ob.UpdateIncremental(symbol, delta)
			if err != nil {
				t.Fatalf("consecutive update %d (seqID=%d) failed: %v", i+1, expectedSeq, err)
			}
		}

		// Verify the book is NOT in resyncing state
		if ob.IsResyncing(symbol) {
			t.Fatal("book should not be resyncing after consecutive updates")
		}

		// Verify the sequenceID matches the last applied delta
		expectedFinalSeq := initialSeqID + int64(numUpdates)
		finalSeq, exists := ob.GetSequenceID(symbol)
		if !exists {
			t.Fatal("book should exist after updates")
		}
		if finalSeq != expectedFinalSeq {
			t.Fatalf("expected final sequenceID %d, got %d", expectedFinalSeq, finalSeq)
		}
	})
}

// TestProperty_SequenceGap_GapTriggersResync tests that for any sequence of incremental
// updates, if the incoming sequenceId is not exactly lastSequenceId + 1, the system
// returns an error and enters resyncing state.
func TestProperty_SequenceGap_GapTriggersResync(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		symbol := "TEST-USDT"
		initialSeqID := rapid.Int64Range(1, 100000).Draw(t, "initialSeqID")

		ob := NewLocalOrderBook()

		// Apply initial snapshot
		snapshot := genSnapshot(t, symbol, initialSeqID)
		err := ob.UpdateFromSnapshot(symbol, snapshot)
		if err != nil {
			t.Fatalf("failed to apply snapshot: %v", err)
		}

		// Generate a gap: delta sequenceID that is NOT initialSeqID + 1
		// Gap can be forward (skip) or backward (duplicate/old)
		gapOffset := rapid.Int64Range(2, 1000).Draw(t, "gapOffset")
		direction := rapid.IntRange(0, 1).Draw(t, "direction")

		var gapSeqID int64
		if direction == 0 {
			// Forward gap (skip ahead)
			gapSeqID = initialSeqID + gapOffset
		} else {
			// Backward gap (duplicate/old) - ensure it's not exactly +1
			gapSeqID = initialSeqID - gapOffset
			if gapSeqID == initialSeqID+1 {
				gapSeqID = initialSeqID + 2 // ensure gap
			}
		}

		// Ensure gapSeqID is truly not the expected next sequence
		if gapSeqID == initialSeqID+1 {
			gapSeqID = initialSeqID + 2
		}

		delta := genDelta(t, symbol, gapSeqID)
		err = ob.UpdateIncremental(symbol, delta)

		// Should return an error
		if err == nil {
			t.Fatalf("expected error for sequence gap (initial=%d, delta=%d), but got nil",
				initialSeqID, gapSeqID)
		}

		// Should be in resyncing state
		if !ob.IsResyncing(symbol) {
			t.Fatalf("expected resyncing=true after sequence gap (initial=%d, delta=%d)",
				initialSeqID, gapSeqID)
		}
	})
}

// TestProperty_SequenceGap_DiscardDuringResync tests that during resync, all
// incremental updates for that symbol are discarded (return error) regardless of
// their sequence number.
func TestProperty_SequenceGap_DiscardDuringResync(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		symbol := "TEST-USDT"
		initialSeqID := rapid.Int64Range(1, 100000).Draw(t, "initialSeqID")

		ob := NewLocalOrderBook()

		// Apply initial snapshot
		snapshot := genSnapshot(t, symbol, initialSeqID)
		err := ob.UpdateFromSnapshot(symbol, snapshot)
		if err != nil {
			t.Fatalf("failed to apply snapshot: %v", err)
		}

		// Trigger resync
		ob.RequestResync(symbol)

		if !ob.IsResyncing(symbol) {
			t.Fatal("expected resyncing=true after RequestResync")
		}

		// Attempt multiple incremental updates with various sequenceIDs
		numAttempts := rapid.IntRange(1, 20).Draw(t, "numAttempts")
		for i := 0; i < numAttempts; i++ {
			// Generate any sequence ID - even the "correct" next one
			anySeqID := rapid.Int64Range(0, 200000).Draw(t, "anySeqID")
			delta := genDelta(t, symbol, anySeqID)

			err := ob.UpdateIncremental(symbol, delta)
			if err == nil {
				t.Fatalf("expected error during resync (attempt %d, seqID=%d), but got nil",
					i+1, anySeqID)
			}
		}

		// Should still be in resyncing state (resync only clears on snapshot)
		if !ob.IsResyncing(symbol) {
			t.Fatal("expected resyncing=true to persist after discarded updates")
		}
	})
}

// TestProperty_SequenceGap_SnapshotClearsResyncAndAllowsNewIncrementals tests that
// applying a new snapshot clears the resync flag and allows subsequent incremental
// updates to succeed with proper sequence continuity.
func TestProperty_SequenceGap_SnapshotClearsResyncAndAllowsNewIncrementals(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		symbol := "TEST-USDT"
		initialSeqID := rapid.Int64Range(1, 100000).Draw(t, "initialSeqID")

		ob := NewLocalOrderBook()

		// Apply initial snapshot
		snapshot := genSnapshot(t, symbol, initialSeqID)
		err := ob.UpdateFromSnapshot(symbol, snapshot)
		if err != nil {
			t.Fatalf("failed to apply initial snapshot: %v", err)
		}

		// Trigger resync (e.g., due to sequence gap)
		ob.RequestResync(symbol)
		if !ob.IsResyncing(symbol) {
			t.Fatal("expected resyncing=true after RequestResync")
		}

		// Apply a new snapshot with a different sequenceID
		newSeqID := rapid.Int64Range(1, 200000).Draw(t, "newSeqID")
		newSnapshot := genSnapshot(t, symbol, newSeqID)
		err = ob.UpdateFromSnapshot(symbol, newSnapshot)
		if err != nil {
			t.Fatalf("failed to apply new snapshot: %v", err)
		}

		// Resync flag should be cleared
		if ob.IsResyncing(symbol) {
			t.Fatal("expected resyncing=false after applying new snapshot")
		}

		// Now apply consecutive incremental updates - should succeed
		numUpdates := rapid.IntRange(1, 20).Draw(t, "numUpdates")
		for i := 0; i < numUpdates; i++ {
			nextSeq := newSeqID + int64(i) + 1
			delta := genDelta(t, symbol, nextSeq)

			err := ob.UpdateIncremental(symbol, delta)
			if err != nil {
				t.Fatalf("incremental update %d (seqID=%d) after resync failed: %v",
					i+1, nextSeq, err)
			}
		}

		// Verify final state
		if ob.IsResyncing(symbol) {
			t.Fatal("book should not be resyncing after successful incrementals")
		}

		expectedFinalSeq := newSeqID + int64(numUpdates)
		finalSeq, exists := ob.GetSequenceID(symbol)
		if !exists {
			t.Fatal("book should exist")
		}
		if finalSeq != expectedFinalSeq {
			t.Fatalf("expected final sequenceID %d, got %d", expectedFinalSeq, finalSeq)
		}
	})
}
