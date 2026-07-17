package orderbook

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

func d(val string) decimal.Decimal {
	return decimal.RequireFromString(val)
}

func makeSnapshot(bids, asks []PriceLevel, seqID int64) *OrderBookSnapshot {
	return &OrderBookSnapshot{
		Symbol:     "BTC-USDT",
		Bids:       bids,
		Asks:       asks,
		SequenceID: seqID,
		Timestamp:  1000,
	}
}

func makeDelta(bids, asks []PriceLevel, seqID int64) *OrderBookDelta {
	return &OrderBookDelta{
		Symbol:     "BTC-USDT",
		Bids:       bids,
		Asks:       asks,
		SequenceID: seqID,
		Timestamp:  1001,
	}
}

func TestApplySnapshot_Basic(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{
			{Price: d("100.5"), Quantity: d("1.0")},
			{Price: d("100.0"), Quantity: d("2.0")},
			{Price: d("99.5"), Quantity: d("3.0")},
		},
		[]PriceLevel{
			{Price: d("101.0"), Quantity: d("1.5")},
			{Price: d("101.5"), Quantity: d("2.5")},
			{Price: d("102.0"), Quantity: d("3.5")},
		},
		100,
	)

	err := ob.UpdateFromSnapshot("BTC-USDT", snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify bids are sorted descending
	bid, err := ob.GetBestBid("BTC-USDT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bid.Price.Equal(d("100.5")) {
		t.Errorf("expected best bid 100.5, got %s", bid.Price)
	}

	// Verify asks are sorted ascending
	ask, err := ob.GetBestAsk("BTC-USDT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ask.Price.Equal(d("101.0")) {
		t.Errorf("expected best ask 101.0, got %s", ask.Price)
	}
}

func TestApplySnapshot_UnsortedInput(t *testing.T) {
	ob := NewLocalOrderBook()

	// Provide bids/asks in random order - should be sorted after apply
	snapshot := makeSnapshot(
		[]PriceLevel{
			{Price: d("99.5"), Quantity: d("3.0")},
			{Price: d("100.5"), Quantity: d("1.0")},
			{Price: d("100.0"), Quantity: d("2.0")},
		},
		[]PriceLevel{
			{Price: d("102.0"), Quantity: d("3.5")},
			{Price: d("101.0"), Quantity: d("1.5")},
			{Price: d("101.5"), Quantity: d("2.5")},
		},
		100,
	)

	err := ob.UpdateFromSnapshot("BTC-USDT", snapshot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	bid, _ := ob.GetBestBid("BTC-USDT")
	if !bid.Price.Equal(d("100.5")) {
		t.Errorf("expected best bid 100.5, got %s", bid.Price)
	}

	ask, _ := ob.GetBestAsk("BTC-USDT")
	if !ask.Price.Equal(d("101.0")) {
		t.Errorf("expected best ask 101.0, got %s", ask.Price)
	}
}

func TestApplySnapshot_ReplacesExisting(t *testing.T) {
	ob := NewLocalOrderBook()

	// First snapshot
	snapshot1 := makeSnapshot(
		[]PriceLevel{{Price: d("50.0"), Quantity: d("1.0")}},
		[]PriceLevel{{Price: d("51.0"), Quantity: d("1.0")}},
		10,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot1)

	// Second snapshot replaces
	snapshot2 := makeSnapshot(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("5.0")}},
		[]PriceLevel{{Price: d("101.0"), Quantity: d("5.0")}},
		20,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot2)

	bid, _ := ob.GetBestBid("BTC-USDT")
	if !bid.Price.Equal(d("100.0")) {
		t.Errorf("expected best bid 100.0 after replace, got %s", bid.Price)
	}

	seqID, _ := ob.GetSequenceID("BTC-USDT")
	if seqID != 20 {
		t.Errorf("expected sequenceID 20, got %d", seqID)
	}
}

func TestApplySnapshot_ClearsResyncFlag(t *testing.T) {
	ob := NewLocalOrderBook()

	// Set up initial state then trigger resync
	snapshot := makeSnapshot(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		[]PriceLevel{{Price: d("101.0"), Quantity: d("1.0")}},
		10,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)
	ob.RequestResync("BTC-USDT")

	if !ob.IsResyncing("BTC-USDT") {
		t.Fatal("expected resyncing to be true")
	}

	// Apply new snapshot should clear resync flag
	snapshot2 := makeSnapshot(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("2.0")}},
		[]PriceLevel{{Price: d("101.0"), Quantity: d("2.0")}},
		20,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot2)

	if ob.IsResyncing("BTC-USDT") {
		t.Fatal("expected resyncing to be false after snapshot")
	}
}

func TestApplyDelta_InsertNewLevel(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{
			{Price: d("100.0"), Quantity: d("1.0")},
			{Price: d("99.0"), Quantity: d("2.0")},
		},
		[]PriceLevel{
			{Price: d("101.0"), Quantity: d("1.0")},
			{Price: d("102.0"), Quantity: d("2.0")},
		},
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	// Insert a new bid level between existing ones
	delta := makeDelta(
		[]PriceLevel{{Price: d("99.5"), Quantity: d("5.0")}},
		nil,
		101,
	)
	err := ob.UpdateIncremental("BTC-USDT", delta)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the bids now have 3 levels in correct order
	depth, _ := ob.GetDepth("BTC-USDT", models.SideBuy, 10)
	if len(depth) != 3 {
		t.Fatalf("expected 3 bid levels, got %d", len(depth))
	}
	if !depth[0].Price.Equal(d("100.0")) {
		t.Errorf("expected bid[0]=100.0, got %s", depth[0].Price)
	}
	if !depth[1].Price.Equal(d("99.5")) {
		t.Errorf("expected bid[1]=99.5, got %s", depth[1].Price)
	}
	if !depth[2].Price.Equal(d("99.0")) {
		t.Errorf("expected bid[2]=99.0, got %s", depth[2].Price)
	}
}

func TestApplyDelta_UpdateExistingLevel(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{
			{Price: d("100.0"), Quantity: d("1.0")},
			{Price: d("99.0"), Quantity: d("2.0")},
		},
		[]PriceLevel{
			{Price: d("101.0"), Quantity: d("1.0")},
		},
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	// Update quantity at existing price
	delta := makeDelta(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("10.0")}},
		nil,
		101,
	)
	err := ob.UpdateIncremental("BTC-USDT", delta)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	bid, _ := ob.GetBestBid("BTC-USDT")
	if !bid.Quantity.Equal(d("10.0")) {
		t.Errorf("expected updated quantity 10.0, got %s", bid.Quantity)
	}
}

func TestApplyDelta_RemoveLevel(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{
			{Price: d("100.0"), Quantity: d("1.0")},
			{Price: d("99.0"), Quantity: d("2.0")},
		},
		[]PriceLevel{
			{Price: d("101.0"), Quantity: d("1.0")},
		},
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	// Remove level by setting quantity=0
	delta := makeDelta(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("0")}},
		nil,
		101,
	)
	err := ob.UpdateIncremental("BTC-USDT", delta)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	bid, _ := ob.GetBestBid("BTC-USDT")
	if !bid.Price.Equal(d("99.0")) {
		t.Errorf("expected best bid 99.0 after removal, got %s", bid.Price)
	}
}

func TestApplyDelta_SequenceGapTriggersResync(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		[]PriceLevel{{Price: d("101.0"), Quantity: d("1.0")}},
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	// Delta with non-consecutive sequence (expected 101, got 103)
	delta := makeDelta(
		[]PriceLevel{{Price: d("99.0"), Quantity: d("5.0")}},
		nil,
		103,
	)
	err := ob.UpdateIncremental("BTC-USDT", delta)
	if err == nil {
		t.Fatal("expected error for sequence gap")
	}

	if !ob.IsResyncing("BTC-USDT") {
		t.Fatal("expected resyncing to be true after sequence gap")
	}
}

func TestApplyDelta_DiscardedDuringResync(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		[]PriceLevel{{Price: d("101.0"), Quantity: d("1.0")}},
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	// Trigger resync
	ob.RequestResync("BTC-USDT")

	// Try to apply delta during resync - should be discarded
	delta := makeDelta(
		[]PriceLevel{{Price: d("99.0"), Quantity: d("5.0")}},
		nil,
		101,
	)
	err := ob.UpdateIncremental("BTC-USDT", delta)
	if err == nil {
		t.Fatal("expected error when applying delta during resync")
	}
}

func TestApplyDelta_ConsecutiveSequence(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		[]PriceLevel{{Price: d("101.0"), Quantity: d("1.0")}},
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	// Apply multiple consecutive deltas
	for i := int64(101); i <= 105; i++ {
		delta := makeDelta(nil, nil, i)
		err := ob.UpdateIncremental("BTC-USDT", delta)
		if err != nil {
			t.Fatalf("unexpected error at seq %d: %v", i, err)
		}
	}

	seqID, _ := ob.GetSequenceID("BTC-USDT")
	if seqID != 105 {
		t.Errorf("expected sequenceID 105, got %d", seqID)
	}
}

func TestGetMidPrice(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		[]PriceLevel{{Price: d("102.0"), Quantity: d("1.0")}},
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	mid, err := ob.GetMidPrice("BTC-USDT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mid.Equal(d("101.0")) {
		t.Errorf("expected mid price 101.0, got %s", mid)
	}
}

func TestGetMidPrice_EmptySide(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		nil,
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	_, err := ob.GetMidPrice("BTC-USDT")
	if err == nil {
		t.Fatal("expected error when asks are empty")
	}
}

func TestGetSpread(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{{Price: d("99.5"), Quantity: d("1.0")}},
		[]PriceLevel{{Price: d("100.5"), Quantity: d("1.0")}},
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	spread, err := ob.GetSpread("BTC-USDT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !spread.Equal(d("1.0")) {
		t.Errorf("expected spread 1.0, got %s", spread)
	}
}

func TestGetVWAP_SingleLevel(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("10.0")}},
		[]PriceLevel{{Price: d("101.0"), Quantity: d("10.0")}},
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	// Buy VWAP - consumes asks
	vwap, err := ob.GetVWAP("BTC-USDT", models.SideBuy, d("5.0"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !vwap.Equal(d("101.0")) {
		t.Errorf("expected VWAP 101.0, got %s", vwap)
	}
}

func TestGetVWAP_MultipleLevels(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{
			{Price: d("100.0"), Quantity: d("5.0")},
			{Price: d("99.0"), Quantity: d("5.0")},
		},
		[]PriceLevel{
			{Price: d("101.0"), Quantity: d("3.0")},
			{Price: d("102.0"), Quantity: d("7.0")},
		},
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	// Buy 5 units: 3 @ 101, 2 @ 102 = (303 + 204) / 5 = 101.4
	vwap, err := ob.GetVWAP("BTC-USDT", models.SideBuy, d("5.0"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := d("101.4")
	if !vwap.Equal(expected) {
		t.Errorf("expected VWAP %s, got %s", expected, vwap)
	}
}

func TestGetVWAP_InsufficientLiquidity(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("5.0")}},
		[]PriceLevel{{Price: d("101.0"), Quantity: d("3.0")}},
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	// Try to buy 10, only 3 available
	_, err := ob.GetVWAP("BTC-USDT", models.SideBuy, d("10.0"))
	if err == nil {
		t.Fatal("expected error for insufficient liquidity")
	}
}

func TestGetDepth(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{
			{Price: d("100.0"), Quantity: d("1.0")},
			{Price: d("99.0"), Quantity: d("2.0")},
			{Price: d("98.0"), Quantity: d("3.0")},
		},
		[]PriceLevel{
			{Price: d("101.0"), Quantity: d("1.0")},
			{Price: d("102.0"), Quantity: d("2.0")},
		},
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	bids, err := ob.GetDepth("BTC-USDT", models.SideBuy, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(bids) != 2 {
		t.Fatalf("expected 2 bid levels, got %d", len(bids))
	}
	if !bids[0].Price.Equal(d("100.0")) {
		t.Errorf("expected bid[0]=100.0, got %s", bids[0].Price)
	}
	if !bids[1].Price.Equal(d("99.0")) {
		t.Errorf("expected bid[1]=99.0, got %s", bids[1].Price)
	}
}

func TestApplyDelta_AskOperations(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		[]PriceLevel{
			{Price: d("101.0"), Quantity: d("1.0")},
			{Price: d("103.0"), Quantity: d("3.0")},
		},
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	// Insert new ask level between existing
	delta := makeDelta(
		nil,
		[]PriceLevel{{Price: d("102.0"), Quantity: d("2.0")}},
		101,
	)
	err := ob.UpdateIncremental("BTC-USDT", delta)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	asks, _ := ob.GetDepth("BTC-USDT", models.SideSell, 10)
	if len(asks) != 3 {
		t.Fatalf("expected 3 ask levels, got %d", len(asks))
	}
	if !asks[0].Price.Equal(d("101.0")) {
		t.Errorf("expected asks[0]=101.0, got %s", asks[0].Price)
	}
	if !asks[1].Price.Equal(d("102.0")) {
		t.Errorf("expected asks[1]=102.0, got %s", asks[1].Price)
	}
	if !asks[2].Price.Equal(d("103.0")) {
		t.Errorf("expected asks[2]=103.0, got %s", asks[2].Price)
	}
}

func TestApplySnapshot_NilReturnsError(t *testing.T) {
	ob := NewLocalOrderBook()
	err := ob.UpdateFromSnapshot("BTC-USDT", nil)
	if err == nil {
		t.Fatal("expected error for nil snapshot")
	}
}

func TestApplyDelta_NilReturnsError(t *testing.T) {
	ob := NewLocalOrderBook()
	err := ob.UpdateIncremental("BTC-USDT", nil)
	if err == nil {
		t.Fatal("expected error for nil delta")
	}
}

func TestApplyDelta_NoExistingBookTriggersResync(t *testing.T) {
	ob := NewLocalOrderBook()

	delta := makeDelta(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		nil,
		1,
	)
	err := ob.UpdateIncremental("BTC-USDT", delta)
	if err == nil {
		t.Fatal("expected error when no book exists")
	}

	if !ob.IsResyncing("BTC-USDT") {
		t.Fatal("expected resyncing to be true")
	}
}

func TestResyncChannel_ReceivesSymbol(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		[]PriceLevel{{Price: d("101.0"), Quantity: d("1.0")}},
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	// Trigger resync
	ob.RequestResync("BTC-USDT")

	// Should receive the symbol on the channel
	select {
	case sym := <-ob.ResyncCh:
		if sym != "BTC-USDT" {
			t.Errorf("expected BTC-USDT on resync channel, got %s", sym)
		}
	default:
		t.Fatal("expected symbol on resync channel")
	}
}

func TestGetBestBid_EmptyBook(t *testing.T) {
	ob := NewLocalOrderBook()
	_, err := ob.GetBestBid("UNKNOWN")
	if err == nil {
		t.Fatal("expected error for unknown symbol")
	}
}

func TestGetBestAsk_EmptyBook(t *testing.T) {
	ob := NewLocalOrderBook()
	_, err := ob.GetBestAsk("UNKNOWN")
	if err == nil {
		t.Fatal("expected error for unknown symbol")
	}
}

func TestGetSpread_EmptySide(t *testing.T) {
	ob := NewLocalOrderBook()
	_, err := ob.GetSpread("UNKNOWN")
	if err == nil {
		t.Fatal("expected error for unknown symbol")
	}
}

func TestApplyDelta_RemoveNonExistentLevel(t *testing.T) {
	ob := NewLocalOrderBook()

	snapshot := makeSnapshot(
		[]PriceLevel{{Price: d("100.0"), Quantity: d("1.0")}},
		[]PriceLevel{{Price: d("101.0"), Quantity: d("1.0")}},
		100,
	)
	ob.UpdateFromSnapshot("BTC-USDT", snapshot)

	// Remove a non-existent price level - should be a no-op
	delta := makeDelta(
		[]PriceLevel{{Price: d("50.0"), Quantity: d("0")}},
		nil,
		101,
	)
	err := ob.UpdateIncremental("BTC-USDT", delta)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Book should be unchanged
	depth, _ := ob.GetDepth("BTC-USDT", models.SideBuy, 10)
	if len(depth) != 1 {
		t.Errorf("expected 1 bid level, got %d", len(depth))
	}
}
