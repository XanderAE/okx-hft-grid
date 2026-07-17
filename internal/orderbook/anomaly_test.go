package orderbook

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestCheckCrossedBook_Detected(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	// Create a crossed book: bestBid (101) >= bestAsk (100)
	snapshot := &OrderBookSnapshot{
		Symbol: "ETH-USDT",
		Bids:   []PriceLevel{{Price: d("101.0"), Quantity: d("1.0")}},
		Asks:   []PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)

	detected := ad.CheckCrossedBook("ETH-USDT")
	if !detected {
		t.Fatal("expected crossed book to be detected")
	}

	if !ob.IsResyncing("ETH-USDT") {
		t.Fatal("expected symbol to be in resyncing state after crossed book detection")
	}
}

func TestCheckCrossedBook_EqualPrices(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	// bestBid == bestAsk (both 100) should trigger anomaly
	snapshot := &OrderBookSnapshot{
		Symbol: "ETH-USDT",
		Bids:   []PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		Asks:   []PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)

	detected := ad.CheckCrossedBook("ETH-USDT")
	if !detected {
		t.Fatal("expected crossed book to be detected when bestBid == bestAsk")
	}
}

func TestCheckCrossedBook_Normal(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	// Normal book: bestBid (99) < bestAsk (101)
	snapshot := &OrderBookSnapshot{
		Symbol: "ETH-USDT",
		Bids:   []PriceLevel{{Price: d("99.0"), Quantity: d("1.0")}},
		Asks:   []PriceLevel{{Price: d("101.0"), Quantity: d("1.0")}},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)

	detected := ad.CheckCrossedBook("ETH-USDT")
	if detected {
		t.Fatal("did not expect anomaly for normal book")
	}

	if ob.IsResyncing("ETH-USDT") {
		t.Fatal("should not be resyncing for normal book")
	}
}

func TestCheckCrossedBook_EmptyBids(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	snapshot := &OrderBookSnapshot{
		Symbol:     "ETH-USDT",
		Bids:       nil,
		Asks:       []PriceLevel{{Price: d("101.0"), Quantity: d("1.0")}},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)

	detected := ad.CheckCrossedBook("ETH-USDT")
	if detected {
		t.Fatal("should not detect anomaly when bids are empty")
	}
}

func TestCheckCrossedBook_EmptyAsks(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	snapshot := &OrderBookSnapshot{
		Symbol:     "ETH-USDT",
		Bids:       []PriceLevel{{Price: d("99.0"), Quantity: d("1.0")}},
		Asks:       nil,
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)

	detected := ad.CheckCrossedBook("ETH-USDT")
	if detected {
		t.Fatal("should not detect anomaly when asks are empty")
	}
}

func TestCheckCrossedBook_UnknownSymbol(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	detected := ad.CheckCrossedBook("UNKNOWN")
	if detected {
		t.Fatal("should not detect anomaly for unknown symbol")
	}
}

func TestCheckCrossedBook_AlreadyResyncing(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	// Set up a book then put it into resyncing state
	snapshot := &OrderBookSnapshot{
		Symbol: "ETH-USDT",
		Bids:   []PriceLevel{{Price: d("101.0"), Quantity: d("1.0")}},
		Asks:   []PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)
	ob.RequestResync("ETH-USDT")

	// Drain the resync channel
	<-ob.ResyncCh

	// Should not detect anomaly since already resyncing
	detected := ad.CheckCrossedBook("ETH-USDT")
	if detected {
		t.Fatal("should not detect anomaly when already resyncing")
	}
}

func TestCheckDepthChange_OverFiftyPercent(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	// Start with 10 levels
	snapshot := &OrderBookSnapshot{
		Symbol: "ETH-USDT",
		Bids: []PriceLevel{
			{Price: d("100.0"), Quantity: d("1.0")},
			{Price: d("99.0"), Quantity: d("1.0")},
			{Price: d("98.0"), Quantity: d("1.0")},
			{Price: d("97.0"), Quantity: d("1.0")},
			{Price: d("96.0"), Quantity: d("1.0")},
		},
		Asks: []PriceLevel{
			{Price: d("101.0"), Quantity: d("1.0")},
			{Price: d("102.0"), Quantity: d("1.0")},
			{Price: d("103.0"), Quantity: d("1.0")},
			{Price: d("104.0"), Quantity: d("1.0")},
			{Price: d("105.0"), Quantity: d("1.0")},
		},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)

	// Previous depth was 10, current depth drops to 4 (change of 6 > 50% of 10)
	detected := ad.CheckDepthChange("ETH-USDT", 10, 4)
	if !detected {
		t.Fatal("expected depth change >50% to be detected")
	}

	if !ob.IsResyncing("ETH-USDT") {
		t.Fatal("expected symbol to be in resyncing state after depth change detection")
	}
}

func TestCheckDepthChange_ExactlyFiftyPercent(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	snapshot := &OrderBookSnapshot{
		Symbol:     "ETH-USDT",
		Bids:       []PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		Asks:       []PriceLevel{{Price: d("101.0"), Quantity: d("1.0")}},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)

	// Previous depth 10, current depth 5. diff=5, 5*2=10 which is NOT > 10
	// So exactly 50% should NOT trigger
	detected := ad.CheckDepthChange("ETH-USDT", 10, 5)
	if detected {
		t.Fatal("exactly 50% depth change should NOT trigger anomaly (requirement is >50%)")
	}
}

func TestCheckDepthChange_UnderFiftyPercent(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	snapshot := &OrderBookSnapshot{
		Symbol:     "ETH-USDT",
		Bids:       []PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		Asks:       []PriceLevel{{Price: d("101.0"), Quantity: d("1.0")}},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)

	// Previous depth 10, current depth 8. diff=2, 2*2=4 which is NOT > 10
	detected := ad.CheckDepthChange("ETH-USDT", 10, 8)
	if detected {
		t.Fatal("small depth change should NOT trigger anomaly")
	}
}

func TestCheckDepthChange_Increase(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	snapshot := &OrderBookSnapshot{
		Symbol:     "ETH-USDT",
		Bids:       []PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		Asks:       []PriceLevel{{Price: d("101.0"), Quantity: d("1.0")}},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)

	// Previous depth 4, current depth 11. diff=7, 7*2=14 > 4
	detected := ad.CheckDepthChange("ETH-USDT", 4, 11)
	if !detected {
		t.Fatal("large depth increase (>50%) should trigger anomaly")
	}
}

func TestCheckDepthChange_ZeroPreviousDepth(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	// Previous depth of 0 should skip the check (first update scenario)
	detected := ad.CheckDepthChange("ETH-USDT", 0, 10)
	if detected {
		t.Fatal("should not trigger anomaly when previous depth is 0")
	}
}

func TestCheckDepthChange_AlreadyResyncing(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	snapshot := &OrderBookSnapshot{
		Symbol:     "ETH-USDT",
		Bids:       []PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		Asks:       []PriceLevel{{Price: d("101.0"), Quantity: d("1.0")}},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)
	ob.RequestResync("ETH-USDT")

	// Drain the resync channel
	<-ob.ResyncCh

	// Should not detect anomaly since already resyncing
	detected := ad.CheckDepthChange("ETH-USDT", 10, 2)
	if detected {
		t.Fatal("should not detect anomaly when already resyncing")
	}
}

func TestRecordDepth(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	snapshot := &OrderBookSnapshot{
		Symbol: "ETH-USDT",
		Bids: []PriceLevel{
			{Price: d("100.0"), Quantity: d("1.0")},
			{Price: d("99.0"), Quantity: d("1.0")},
		},
		Asks: []PriceLevel{
			{Price: d("101.0"), Quantity: d("1.0")},
			{Price: d("102.0"), Quantity: d("1.0")},
			{Price: d("103.0"), Quantity: d("1.0")},
		},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)

	ad.RecordDepth("ETH-USDT")
	prevDepth := ad.GetPreviousDepth("ETH-USDT")
	if prevDepth != 5 { // 2 bids + 3 asks
		t.Errorf("expected previous depth 5, got %d", prevDepth)
	}
}

func TestRecordDepth_UnknownSymbol(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	ad.RecordDepth("UNKNOWN")
	prevDepth := ad.GetPreviousDepth("UNKNOWN")
	if prevDepth != 0 {
		t.Errorf("expected previous depth 0 for unknown symbol, got %d", prevDepth)
	}
}

func TestGetCurrentDepth(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	snapshot := &OrderBookSnapshot{
		Symbol: "ETH-USDT",
		Bids: []PriceLevel{
			{Price: d("100.0"), Quantity: d("1.0")},
			{Price: d("99.0"), Quantity: d("1.0")},
		},
		Asks: []PriceLevel{
			{Price: d("101.0"), Quantity: d("1.0")},
		},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)

	depth := ad.GetCurrentDepth("ETH-USDT")
	if depth != 3 {
		t.Errorf("expected current depth 3, got %d", depth)
	}
}

func TestGetCurrentDepth_UnknownSymbol(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	depth := ad.GetCurrentDepth("UNKNOWN")
	if depth != 0 {
		t.Errorf("expected current depth 0 for unknown symbol, got %d", depth)
	}
}

func TestCheckAfterUpdate_CrossedBookDetected(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	// Set up a normal book first to record initial depth
	snapshot := &OrderBookSnapshot{
		Symbol: "ETH-USDT",
		Bids: []PriceLevel{
			{Price: d("99.0"), Quantity: d("1.0")},
			{Price: d("98.0"), Quantity: d("1.0")},
		},
		Asks: []PriceLevel{
			{Price: d("101.0"), Quantity: d("1.0")},
			{Price: d("102.0"), Quantity: d("1.0")},
		},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)
	ad.RecordDepth("ETH-USDT")

	// Now apply a delta that creates a crossed book
	// Simulate: new bid at 103 (above best ask 101)
	delta := &OrderBookDelta{
		Symbol:     "ETH-USDT",
		Bids:       []PriceLevel{{Price: d("103.0"), Quantity: d("5.0")}},
		Asks:       nil,
		SequenceID: 2,
		Timestamp:  1001,
	}
	ob.UpdateIncremental("ETH-USDT", delta)

	detected := ad.CheckAfterUpdate("ETH-USDT")
	if !detected {
		t.Fatal("expected anomaly to be detected after creating crossed book")
	}
}

func TestCheckAfterUpdate_DepthChangeDetected(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	// Set up book with 10 levels
	snapshot := &OrderBookSnapshot{
		Symbol: "ETH-USDT",
		Bids: []PriceLevel{
			{Price: d("100.0"), Quantity: d("1.0")},
			{Price: d("99.0"), Quantity: d("1.0")},
			{Price: d("98.0"), Quantity: d("1.0")},
			{Price: d("97.0"), Quantity: d("1.0")},
			{Price: d("96.0"), Quantity: d("1.0")},
		},
		Asks: []PriceLevel{
			{Price: d("101.0"), Quantity: d("1.0")},
			{Price: d("102.0"), Quantity: d("1.0")},
			{Price: d("103.0"), Quantity: d("1.0")},
			{Price: d("104.0"), Quantity: d("1.0")},
			{Price: d("105.0"), Quantity: d("1.0")},
		},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)

	// Record depth as 10
	ad.RecordDepth("ETH-USDT")

	// Remove most levels to trigger >50% depth change
	delta := &OrderBookDelta{
		Symbol: "ETH-USDT",
		Bids: []PriceLevel{
			{Price: d("99.0"), Quantity: decimal.Zero},
			{Price: d("98.0"), Quantity: decimal.Zero},
			{Price: d("97.0"), Quantity: decimal.Zero},
			{Price: d("96.0"), Quantity: decimal.Zero},
		},
		Asks: []PriceLevel{
			{Price: d("102.0"), Quantity: decimal.Zero},
			{Price: d("103.0"), Quantity: decimal.Zero},
			{Price: d("104.0"), Quantity: decimal.Zero},
			{Price: d("105.0"), Quantity: decimal.Zero},
		},
		SequenceID: 2,
		Timestamp:  1001,
	}
	ob.UpdateIncremental("ETH-USDT", delta)

	// After the delta, we should have 2 levels (1 bid + 1 ask), previous was 10
	// Change is 8, 8*2=16 > 10 → anomaly
	detected := ad.CheckAfterUpdate("ETH-USDT")
	if !detected {
		t.Fatal("expected depth change anomaly to be detected")
	}
}

func TestCheckAfterUpdate_NoAnomaly(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	// Set up a normal book
	snapshot := &OrderBookSnapshot{
		Symbol: "ETH-USDT",
		Bids: []PriceLevel{
			{Price: d("100.0"), Quantity: d("1.0")},
			{Price: d("99.0"), Quantity: d("1.0")},
		},
		Asks: []PriceLevel{
			{Price: d("101.0"), Quantity: d("1.0")},
			{Price: d("102.0"), Quantity: d("1.0")},
		},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)
	ad.RecordDepth("ETH-USDT")

	// Small normal update - add one level
	delta := &OrderBookDelta{
		Symbol:     "ETH-USDT",
		Bids:       []PriceLevel{{Price: d("98.0"), Quantity: d("1.0")}},
		Asks:       nil,
		SequenceID: 2,
		Timestamp:  1001,
	}
	ob.UpdateIncremental("ETH-USDT", delta)

	detected := ad.CheckAfterUpdate("ETH-USDT")
	if detected {
		t.Fatal("should not detect anomaly for normal update")
	}
}

func TestCheckAfterUpdate_UpdatesRecordedDepth(t *testing.T) {
	ob := NewLocalOrderBook()
	ad := NewAnomalyDetector(ob)

	snapshot := &OrderBookSnapshot{
		Symbol: "ETH-USDT",
		Bids: []PriceLevel{
			{Price: d("100.0"), Quantity: d("1.0")},
			{Price: d("99.0"), Quantity: d("1.0")},
		},
		Asks: []PriceLevel{
			{Price: d("101.0"), Quantity: d("1.0")},
			{Price: d("102.0"), Quantity: d("1.0")},
		},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)
	ad.RecordDepth("ETH-USDT")

	// Add a level (5 total after)
	delta := &OrderBookDelta{
		Symbol:     "ETH-USDT",
		Bids:       []PriceLevel{{Price: d("98.0"), Quantity: d("1.0")}},
		Asks:       nil,
		SequenceID: 2,
		Timestamp:  1001,
	}
	ob.UpdateIncremental("ETH-USDT", delta)

	// No anomaly, should update recorded depth to current (5)
	ad.CheckAfterUpdate("ETH-USDT")

	prevDepth := ad.GetPreviousDepth("ETH-USDT")
	if prevDepth != 5 { // 3 bids + 2 asks
		t.Errorf("expected recorded depth to be 5 after non-anomalous update, got %d", prevDepth)
	}
}

func TestResyncDiscardsIncrementalUpdates(t *testing.T) {
	ob := NewLocalOrderBook()

	// Set up a book and trigger resync
	snapshot := &OrderBookSnapshot{
		Symbol: "ETH-USDT",
		Bids:   []PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		Asks:   []PriceLevel{{Price: d("101.0"), Quantity: d("1.0")}},
		SequenceID: 1,
		Timestamp:  1000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot)
	ob.RequestResync("ETH-USDT")

	// Try to apply incremental during resync - should be discarded
	delta := &OrderBookDelta{
		Symbol:     "ETH-USDT",
		Bids:       []PriceLevel{{Price: d("99.0"), Quantity: d("5.0")}},
		Asks:       nil,
		SequenceID: 2,
		Timestamp:  1001,
	}
	err := ob.UpdateIncremental("ETH-USDT", delta)
	if err == nil {
		t.Fatal("expected error when applying incremental during resync")
	}

	// Verify still in resyncing state
	if !ob.IsResyncing("ETH-USDT") {
		t.Fatal("should still be resyncing")
	}

	// New snapshot should clear resyncing flag
	snapshot2 := &OrderBookSnapshot{
		Symbol: "ETH-USDT",
		Bids:   []PriceLevel{{Price: d("100.0"), Quantity: d("2.0")}},
		Asks:   []PriceLevel{{Price: d("101.0"), Quantity: d("2.0")}},
		SequenceID: 10,
		Timestamp:  2000,
	}
	ob.UpdateFromSnapshot("ETH-USDT", snapshot2)

	if ob.IsResyncing("ETH-USDT") {
		t.Fatal("should no longer be resyncing after new snapshot")
	}
}
