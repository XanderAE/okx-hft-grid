package orderbook

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// **Validates: Requirement 2.8**

// TestProperty_AnomalyDetection_CrossedBookDetected tests that for any order book state
// where bestBid >= bestAsk (crossed book), the system SHALL detect the anomaly and
// request full resynchronization.
func TestProperty_AnomalyDetection_CrossedBookDetected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()
		ad := NewAnomalyDetector(ob)

		// Generate a random price for bestAsk (must be positive)
		bestAskFloat := rapid.Float64Range(0.01, 99999.0).Draw(t, "bestAsk")
		bestAsk := decimal.NewFromFloat(bestAskFloat)

		// Generate bestBid >= bestAsk (crossed book condition)
		// Add a random non-negative offset to bestAsk
		offsetFloat := rapid.Float64Range(0.0, 10000.0).Draw(t, "offset")
		bestBid := bestAsk.Add(decimal.NewFromFloat(offsetFloat))

		// Generate random quantities (positive)
		bidQtyFloat := rapid.Float64Range(0.001, 1000.0).Draw(t, "bidQty")
		askQtyFloat := rapid.Float64Range(0.001, 1000.0).Draw(t, "askQty")
		bidQty := decimal.NewFromFloat(bidQtyFloat)
		askQty := decimal.NewFromFloat(askQtyFloat)

		symbol := "TEST-USDT"

		snapshot := &OrderBookSnapshot{
			Symbol:     symbol,
			Bids:       []PriceLevel{{Price: bestBid, Quantity: bidQty}},
			Asks:       []PriceLevel{{Price: bestAsk, Quantity: askQty}},
			SequenceID: 1,
			Timestamp:  1000,
		}
		ob.UpdateFromSnapshot(symbol, snapshot)

		detected := ad.CheckCrossedBook(symbol)

		if !detected {
			t.Fatalf("expected crossed book to be detected: bestBid=%s >= bestAsk=%s",
				bestBid.String(), bestAsk.String())
		}

		if !ob.IsResyncing(symbol) {
			t.Fatalf("expected symbol to be in resyncing state after crossed book detection")
		}
	})
}

// TestProperty_AnomalyDetection_NormalBookNotDetected tests that for any order book state
// where bestBid < bestAsk (normal book), CheckCrossedBook returns false and no resync
// is triggered.
func TestProperty_AnomalyDetection_NormalBookNotDetected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()
		ad := NewAnomalyDetector(ob)

		// Generate bestBid as a random positive price
		bestBidFloat := rapid.Float64Range(0.01, 99999.0).Draw(t, "bestBid")
		bestBid := decimal.NewFromFloat(bestBidFloat)

		// Generate bestAsk strictly greater than bestBid
		spreadFloat := rapid.Float64Range(0.0001, 5000.0).Draw(t, "spread")
		bestAsk := bestBid.Add(decimal.NewFromFloat(spreadFloat))

		// Generate random quantities (positive)
		bidQtyFloat := rapid.Float64Range(0.001, 1000.0).Draw(t, "bidQty")
		askQtyFloat := rapid.Float64Range(0.001, 1000.0).Draw(t, "askQty")
		bidQty := decimal.NewFromFloat(bidQtyFloat)
		askQty := decimal.NewFromFloat(askQtyFloat)

		symbol := "TEST-USDT"

		snapshot := &OrderBookSnapshot{
			Symbol:     symbol,
			Bids:       []PriceLevel{{Price: bestBid, Quantity: bidQty}},
			Asks:       []PriceLevel{{Price: bestAsk, Quantity: askQty}},
			SequenceID: 1,
			Timestamp:  1000,
		}
		ob.UpdateFromSnapshot(symbol, snapshot)

		detected := ad.CheckCrossedBook(symbol)

		if detected {
			t.Fatalf("did not expect crossed book detection for normal book: bestBid=%s < bestAsk=%s",
				bestBid.String(), bestAsk.String())
		}

		if ob.IsResyncing(symbol) {
			t.Fatalf("should not be resyncing for normal book")
		}
	})
}

// TestProperty_AnomalyDetection_DepthChangeOverFiftyPercent tests that for any depth change
// exceeding 50% of total levels in a single update, the system SHALL detect the anomaly
// and trigger resynchronization.
func TestProperty_AnomalyDetection_DepthChangeOverFiftyPercent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()
		ad := NewAnomalyDetector(ob)

		// Generate a previous depth > 0
		previousDepth := rapid.IntRange(1, 200).Draw(t, "previousDepth")

		// Generate a current depth such that the change exceeds 50%
		// |currentDepth - previousDepth| / previousDepth > 0.5
		// i.e., |currentDepth - previousDepth| > previousDepth / 2
		// We need diff * 2 > previousDepth (matching the implementation logic)
		halfPlus := previousDepth/2 + 1 // minimum diff to exceed 50%

		// Either decrease or increase by more than 50%
		direction := rapid.IntRange(0, 1).Draw(t, "direction")
		var currentDepth int
		if direction == 0 {
			// Decrease: currentDepth = previousDepth - diff, where diff > previousDepth/2
			maxDecrease := previousDepth - 1 // can't go below 0 but need at least some depth sometimes
			if halfPlus > maxDecrease {
				// Can't decrease enough, force to 0
				currentDepth = 0
			} else {
				decrease := rapid.IntRange(halfPlus, maxDecrease).Draw(t, "decrease")
				currentDepth = previousDepth - decrease
			}
		} else {
			// Increase: currentDepth = previousDepth + diff, where diff > previousDepth/2
			increase := rapid.IntRange(halfPlus, halfPlus+200).Draw(t, "increase")
			currentDepth = previousDepth + increase
		}

		// Ensure the change actually exceeds 50% per the implementation logic: diff*2 > previousDepth
		diff := currentDepth - previousDepth
		if diff < 0 {
			diff = -diff
		}
		if diff*2 <= previousDepth {
			// Skip this case - shouldn't happen with our generation logic, but just in case
			return
		}

		// Set up a valid (non-resyncing) book so the check can proceed
		symbol := "TEST-USDT"
		snapshot := &OrderBookSnapshot{
			Symbol:     symbol,
			Bids:       []PriceLevel{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1)}},
			Asks:       []PriceLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(1)}},
			SequenceID: 1,
			Timestamp:  1000,
		}
		ob.UpdateFromSnapshot(symbol, snapshot)

		detected := ad.CheckDepthChange(symbol, previousDepth, currentDepth)

		if !detected {
			t.Fatalf("expected depth change >50%% to be detected: previous=%d, current=%d, diff=%d",
				previousDepth, currentDepth, diff)
		}

		if !ob.IsResyncing(symbol) {
			t.Fatalf("expected symbol to be in resyncing state after depth change detection")
		}
	})
}

// TestProperty_AnomalyDetection_DepthChangeWithinFiftyPercent tests that for any depth change
// that does not exceed 50% of total levels, CheckDepthChange returns false and no resync
// is triggered.
func TestProperty_AnomalyDetection_DepthChangeWithinFiftyPercent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()
		ad := NewAnomalyDetector(ob)

		// Generate a previous depth > 0
		previousDepth := rapid.IntRange(2, 200).Draw(t, "previousDepth")

		// Generate a current depth such that the change is <=50%
		// diff*2 <= previousDepth => diff <= previousDepth/2
		maxDiff := previousDepth / 2

		// Generate a diff in [0, maxDiff]
		diff := rapid.IntRange(0, maxDiff).Draw(t, "diff")

		// Direction: increase or decrease
		direction := rapid.IntRange(0, 1).Draw(t, "direction")
		var currentDepth int
		if direction == 0 && diff <= previousDepth {
			currentDepth = previousDepth - diff
		} else {
			currentDepth = previousDepth + diff
		}

		// Verify our constraint: diff*2 <= previousDepth
		actualDiff := currentDepth - previousDepth
		if actualDiff < 0 {
			actualDiff = -actualDiff
		}
		if actualDiff*2 > previousDepth {
			// Shouldn't happen with our generation logic, but skip if it does
			return
		}

		// Set up a valid (non-resyncing) book so the check can proceed
		symbol := "TEST-USDT"
		snapshot := &OrderBookSnapshot{
			Symbol:     symbol,
			Bids:       []PriceLevel{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1)}},
			Asks:       []PriceLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(1)}},
			SequenceID: 1,
			Timestamp:  1000,
		}
		ob.UpdateFromSnapshot(symbol, snapshot)

		detected := ad.CheckDepthChange(symbol, previousDepth, currentDepth)

		if detected {
			t.Fatalf("did not expect depth change anomaly for change <=50%%: previous=%d, current=%d, diff=%d",
				previousDepth, currentDepth, actualDiff)
		}

		if ob.IsResyncing(symbol) {
			t.Fatalf("should not be resyncing for normal depth change")
		}
	})
}

// TestProperty_AnomalyDetection_AlreadyResyncingNotRetriggered tests that if a symbol
// is already in resyncing state, neither crossed book nor depth change anomalies will
// re-trigger a resync request.
func TestProperty_AnomalyDetection_AlreadyResyncingNotRetriggered(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()
		ad := NewAnomalyDetector(ob)

		symbol := "TEST-USDT"

		// Generate a crossed book to create an anomalous state
		bestAskFloat := rapid.Float64Range(0.01, 99999.0).Draw(t, "bestAsk")
		bestAsk := decimal.NewFromFloat(bestAskFloat)
		offsetFloat := rapid.Float64Range(0.0, 5000.0).Draw(t, "offset")
		bestBid := bestAsk.Add(decimal.NewFromFloat(offsetFloat))

		snapshot := &OrderBookSnapshot{
			Symbol:     symbol,
			Bids:       []PriceLevel{{Price: bestBid, Quantity: decimal.NewFromInt(1)}},
			Asks:       []PriceLevel{{Price: bestAsk, Quantity: decimal.NewFromInt(1)}},
			SequenceID: 1,
			Timestamp:  1000,
		}
		ob.UpdateFromSnapshot(symbol, snapshot)

		// Manually trigger resync to put symbol in resyncing state
		ob.RequestResync(symbol)

		// Drain the resync channel to avoid blocking
		select {
		case <-ob.ResyncCh:
		default:
		}

		// Now check - should NOT detect since already resyncing
		crossedDetected := ad.CheckCrossedBook(symbol)
		if crossedDetected {
			t.Fatal("should not detect crossed book anomaly when already resyncing")
		}

		// Also check depth change - should NOT trigger
		previousDepth := rapid.IntRange(1, 100).Draw(t, "previousDepth")
		// Force a large depth change
		currentDepth := 0
		depthDetected := ad.CheckDepthChange(symbol, previousDepth, currentDepth)
		if depthDetected {
			t.Fatal("should not detect depth change anomaly when already resyncing")
		}
	})
}
