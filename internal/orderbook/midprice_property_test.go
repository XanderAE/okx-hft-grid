package orderbook

import (
	"math"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// **Validates: Requirements 2.3, 2.5**

// TestProperty_OrderBook_MidPriceCalculation tests that for any order book with at least
// one bid and one ask level where bestBid < bestAsk, mid_price equals (bestBid + bestAsk) / 2
// with precision up to 8 decimal places.
func TestProperty_OrderBook_MidPriceCalculation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()

		// Generate a random best bid price (positive, up to 8 decimal places)
		bestBidFloat := rapid.Float64Range(0.00000001, 99999999.0).Draw(t, "bestBid")
		// Ensure bestAsk > bestBid by adding a positive spread
		spreadFloat := rapid.Float64Range(0.00000001, 1000.0).Draw(t, "spread")
		bestAskFloat := bestBidFloat + spreadFloat

		// Round to 8 decimal places to avoid floating point issues
		bestBid := decimal.NewFromFloat(bestBidFloat).Round(8)
		bestAsk := decimal.NewFromFloat(bestAskFloat).Round(8)

		// Ensure bestBid < bestAsk after rounding
		if bestBid.GreaterThanOrEqual(bestAsk) {
			bestAsk = bestBid.Add(decimal.NewFromFloat(0.00000001))
		}

		// Generate additional bid levels (lower than bestBid)
		numExtraBids := rapid.IntRange(0, 5).Draw(t, "numExtraBids")
		bids := []PriceLevel{{Price: bestBid, Quantity: decimal.NewFromInt(1)}}
		for i := 0; i < numExtraBids; i++ {
			offset := decimal.NewFromFloat(rapid.Float64Range(0.00000001, 100.0).Draw(t, "bidOffset"))
			bids = append(bids, PriceLevel{
				Price:    bestBid.Sub(offset).Round(8),
				Quantity: decimal.NewFromFloat(rapid.Float64Range(0.1, 100.0).Draw(t, "bidQty")),
			})
		}

		// Generate additional ask levels (higher than bestAsk)
		numExtraAsks := rapid.IntRange(0, 5).Draw(t, "numExtraAsks")
		asks := []PriceLevel{{Price: bestAsk, Quantity: decimal.NewFromInt(1)}}
		for i := 0; i < numExtraAsks; i++ {
			offset := decimal.NewFromFloat(rapid.Float64Range(0.00000001, 100.0).Draw(t, "askOffset"))
			asks = append(asks, PriceLevel{
				Price:    bestAsk.Add(offset).Round(8),
				Quantity: decimal.NewFromFloat(rapid.Float64Range(0.1, 100.0).Draw(t, "askQty")),
			})
		}

		snapshot := &OrderBookSnapshot{
			Symbol:     "TEST-USDT",
			Bids:       bids,
			Asks:       asks,
			SequenceID: 1,
			Timestamp:  1000,
		}

		err := ob.UpdateFromSnapshot("TEST-USDT", snapshot)
		if err != nil {
			t.Fatalf("unexpected error from UpdateFromSnapshot: %v", err)
		}

		midPrice, err := ob.GetMidPrice("TEST-USDT")
		if err != nil {
			t.Fatalf("unexpected error from GetMidPrice: %v", err)
		}

		// Expected: (bestBid + bestAsk) / 2, rounded to 8 decimal places
		two := decimal.NewFromInt(2)
		expectedMidPrice := bestBid.Add(bestAsk).Div(two).Round(8)

		if !midPrice.Equal(expectedMidPrice) {
			t.Fatalf("mid price mismatch: expected %s, got %s (bestBid=%s, bestAsk=%s)",
				expectedMidPrice, midPrice, bestBid, bestAsk)
		}

		// Verify mid price has at most 8 decimal places
		decPlaces := getDecimalPlaces(midPrice)
		if decPlaces > 8 {
			t.Fatalf("mid price has more than 8 decimal places: %s (%d places)",
				midPrice, decPlaces)
		}
	})
}

// TestProperty_OrderBook_SpreadCalculation tests that for any order book with at least
// one bid and one ask level where bestBid < bestAsk, spread equals bestAsk - bestBid
// as a non-negative value with precision up to 8 decimal places.
func TestProperty_OrderBook_SpreadCalculation(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()

		// Generate a random best bid price
		bestBidFloat := rapid.Float64Range(0.00000001, 99999999.0).Draw(t, "bestBid")
		// Ensure bestAsk > bestBid
		spreadFloat := rapid.Float64Range(0.00000001, 1000.0).Draw(t, "spread")
		bestAskFloat := bestBidFloat + spreadFloat

		bestBid := decimal.NewFromFloat(bestBidFloat).Round(8)
		bestAsk := decimal.NewFromFloat(bestAskFloat).Round(8)

		// Ensure bestBid < bestAsk after rounding
		if bestBid.GreaterThanOrEqual(bestAsk) {
			bestAsk = bestBid.Add(decimal.NewFromFloat(0.00000001))
		}

		// Generate additional levels
		numExtraBids := rapid.IntRange(0, 5).Draw(t, "numExtraBids")
		bids := []PriceLevel{{Price: bestBid, Quantity: decimal.NewFromInt(1)}}
		for i := 0; i < numExtraBids; i++ {
			offset := decimal.NewFromFloat(rapid.Float64Range(0.00000001, 100.0).Draw(t, "bidOffset"))
			bids = append(bids, PriceLevel{
				Price:    bestBid.Sub(offset).Round(8),
				Quantity: decimal.NewFromFloat(rapid.Float64Range(0.1, 100.0).Draw(t, "bidQty")),
			})
		}

		numExtraAsks := rapid.IntRange(0, 5).Draw(t, "numExtraAsks")
		asks := []PriceLevel{{Price: bestAsk, Quantity: decimal.NewFromInt(1)}}
		for i := 0; i < numExtraAsks; i++ {
			offset := decimal.NewFromFloat(rapid.Float64Range(0.00000001, 100.0).Draw(t, "askOffset"))
			asks = append(asks, PriceLevel{
				Price:    bestAsk.Add(offset).Round(8),
				Quantity: decimal.NewFromFloat(rapid.Float64Range(0.1, 100.0).Draw(t, "askQty")),
			})
		}

		snapshot := &OrderBookSnapshot{
			Symbol:     "TEST-USDT",
			Bids:       bids,
			Asks:       asks,
			SequenceID: 1,
			Timestamp:  1000,
		}

		err := ob.UpdateFromSnapshot("TEST-USDT", snapshot)
		if err != nil {
			t.Fatalf("unexpected error from UpdateFromSnapshot: %v", err)
		}

		spread, err := ob.GetSpread("TEST-USDT")
		if err != nil {
			t.Fatalf("unexpected error from GetSpread: %v", err)
		}

		// Expected: bestAsk - bestBid, rounded to 8 decimal places
		expectedSpread := bestAsk.Sub(bestBid).Round(8)

		if !spread.Equal(expectedSpread) {
			t.Fatalf("spread mismatch: expected %s, got %s (bestBid=%s, bestAsk=%s)",
				expectedSpread, spread, bestBid, bestAsk)
		}

		// Verify spread is non-negative
		if spread.IsNegative() {
			t.Fatalf("spread is negative: %s (bestBid=%s, bestAsk=%s)",
				spread, bestBid, bestAsk)
		}

		// Verify spread has at most 8 decimal places
		decPlaces := getDecimalPlaces(spread)
		if decPlaces > 8 {
			t.Fatalf("spread has more than 8 decimal places: %s (%d places)",
				spread, decPlaces)
		}
	})
}

// TestProperty_OrderBook_MidPriceAndSpreadPrecision tests that both mid_price and spread
// always have at most 8 decimal places for any valid order book configuration.
func TestProperty_OrderBook_MidPriceAndSpreadPrecision(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()

		// Generate prices with varying decimal places (1 to 8)
		bidDecPlaces := rapid.IntRange(1, 8).Draw(t, "bidDecPlaces")
		askDecPlaces := rapid.IntRange(1, 8).Draw(t, "askDecPlaces")

		// Generate integer parts and fractional parts separately for better control
		bidInt := int64(rapid.IntRange(1, 99999).Draw(t, "bidInt"))
		bidFracMax := int64(math.Pow10(bidDecPlaces)) - 1
		bidFrac := rapid.Int64Range(1, bidFracMax).Draw(t, "bidFrac")
		askInt := int64(rapid.IntRange(int(bidInt), 99999).Draw(t, "askInt"))
		askFracMax := int64(math.Pow10(askDecPlaces)) - 1
		askFrac := rapid.Int64Range(1, askFracMax).Draw(t, "askFrac")

		// Construct decimal values
		bidDivisor := decimal.NewFromFloat(math.Pow10(bidDecPlaces))
		bestBid := decimal.NewFromInt(bidInt).Add(decimal.NewFromInt(bidFrac).Div(bidDivisor)).Round(8)

		askDivisor := decimal.NewFromFloat(math.Pow10(askDecPlaces))
		bestAsk := decimal.NewFromInt(askInt).Add(decimal.NewFromInt(askFrac).Div(askDivisor)).Round(8)

		// Ensure bestBid < bestAsk
		if bestBid.GreaterThanOrEqual(bestAsk) {
			bestAsk = bestBid.Add(decimal.NewFromFloat(0.00000001))
		}

		snapshot := &OrderBookSnapshot{
			Symbol:     "TEST-USDT",
			Bids:       []PriceLevel{{Price: bestBid, Quantity: decimal.NewFromInt(1)}},
			Asks:       []PriceLevel{{Price: bestAsk, Quantity: decimal.NewFromInt(1)}},
			SequenceID: 1,
			Timestamp:  1000,
		}

		err := ob.UpdateFromSnapshot("TEST-USDT", snapshot)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		midPrice, err := ob.GetMidPrice("TEST-USDT")
		if err != nil {
			t.Fatalf("unexpected error from GetMidPrice: %v", err)
		}

		spread, err := ob.GetSpread("TEST-USDT")
		if err != nil {
			t.Fatalf("unexpected error from GetSpread: %v", err)
		}

		// Verify both have at most 8 decimal places
		midPlaces := getDecimalPlaces(midPrice)
		if midPlaces > 8 {
			t.Fatalf("mid price has %d decimal places (>8): %s (bestBid=%s, bestAsk=%s)",
				midPlaces, midPrice, bestBid, bestAsk)
		}

		spreadPlaces := getDecimalPlaces(spread)
		if spreadPlaces > 8 {
			t.Fatalf("spread has %d decimal places (>8): %s (bestBid=%s, bestAsk=%s)",
				spreadPlaces, spread, bestBid, bestAsk)
		}
	})
}

// TestProperty_OrderBook_ErrorWhenEmptySide tests that GetMidPrice and GetSpread
// return errors when either the bid or ask side is empty.
func TestProperty_OrderBook_ErrorWhenEmptySide(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()

		// Choose which side to leave empty: 0=no bids, 1=no asks, 2=both empty
		emptyCase := rapid.IntRange(0, 2).Draw(t, "emptyCase")

		var bids, asks []PriceLevel
		price := decimal.NewFromFloat(rapid.Float64Range(1.0, 10000.0).Draw(t, "price")).Round(8)
		qty := decimal.NewFromFloat(rapid.Float64Range(0.1, 100.0).Draw(t, "qty"))

		switch emptyCase {
		case 0:
			// No bids, has asks
			asks = []PriceLevel{{Price: price, Quantity: qty}}
		case 1:
			// Has bids, no asks
			bids = []PriceLevel{{Price: price, Quantity: qty}}
		case 2:
			// Both empty
		}

		snapshot := &OrderBookSnapshot{
			Symbol:     "TEST-USDT",
			Bids:       bids,
			Asks:       asks,
			SequenceID: 1,
			Timestamp:  1000,
		}

		err := ob.UpdateFromSnapshot("TEST-USDT", snapshot)
		if err != nil {
			t.Fatalf("unexpected error from UpdateFromSnapshot: %v", err)
		}

		// GetMidPrice should return error
		_, midErr := ob.GetMidPrice("TEST-USDT")
		if midErr == nil {
			t.Fatalf("expected error from GetMidPrice with empty side (case=%d), got nil", emptyCase)
		}

		// GetSpread should return error
		_, spreadErr := ob.GetSpread("TEST-USDT")
		if spreadErr == nil {
			t.Fatalf("expected error from GetSpread with empty side (case=%d), got nil", emptyCase)
		}
	})
}

// TestProperty_OrderBook_MidPriceBetweenBidAndAsk tests that mid_price is always
// between bestBid and bestAsk (inclusive) for any valid order book.
func TestProperty_OrderBook_MidPriceBetweenBidAndAsk(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()

		bestBidFloat := rapid.Float64Range(0.01, 99999.0).Draw(t, "bestBid")
		spreadFloat := rapid.Float64Range(0.00000001, 500.0).Draw(t, "spread")
		bestAskFloat := bestBidFloat + spreadFloat

		bestBid := decimal.NewFromFloat(bestBidFloat).Round(8)
		bestAsk := decimal.NewFromFloat(bestAskFloat).Round(8)

		if bestBid.GreaterThanOrEqual(bestAsk) {
			bestAsk = bestBid.Add(decimal.NewFromFloat(0.00000001))
		}

		snapshot := &OrderBookSnapshot{
			Symbol:     "TEST-USDT",
			Bids:       []PriceLevel{{Price: bestBid, Quantity: decimal.NewFromInt(1)}},
			Asks:       []PriceLevel{{Price: bestAsk, Quantity: decimal.NewFromInt(1)}},
			SequenceID: 1,
			Timestamp:  1000,
		}

		ob.UpdateFromSnapshot("TEST-USDT", snapshot)

		midPrice, err := ob.GetMidPrice("TEST-USDT")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Mid price should be >= bestBid and <= bestAsk
		if midPrice.LessThan(bestBid) {
			t.Fatalf("mid price %s is less than bestBid %s", midPrice, bestBid)
		}
		if midPrice.GreaterThan(bestAsk) {
			t.Fatalf("mid price %s is greater than bestAsk %s", midPrice, bestAsk)
		}
	})
}

// getDecimalPlaces counts the number of decimal places in a decimal value.
func getDecimalPlaces(d decimal.Decimal) int {
	str := d.String()
	dotIdx := strings.Index(str, ".")
	if dotIdx == -1 {
		return 0
	}
	// Remove trailing zeros
	frac := strings.TrimRight(str[dotIdx+1:], "0")
	return len(frac)
}
