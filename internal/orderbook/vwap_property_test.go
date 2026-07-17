package orderbook

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirement 2.6**

// genAskLevels generates a sorted list of ask levels (ascending price).
func genAskLevels(t *rapid.T, count int, basePrice decimal.Decimal) []PriceLevel {
	levels := make([]PriceLevel, count)
	currentPrice := basePrice
	for i := 0; i < count; i++ {
		// Each successive ask price is higher by a random increment
		increment := decimal.NewFromInt(int64(rapid.IntRange(1, 1000).Draw(t, "ask_incr")))
		increment = increment.Div(decimal.NewFromInt(100))
		currentPrice = currentPrice.Add(increment)

		qtyInt := rapid.IntRange(1, 10000).Draw(t, "ask_qty")
		qty := decimal.NewFromInt(int64(qtyInt)).Div(decimal.NewFromInt(1000))

		levels[i] = PriceLevel{Price: currentPrice, Quantity: qty}
	}
	return levels
}

// genBidLevels generates a sorted list of bid levels (descending price).
func genBidLevels(t *rapid.T, count int, basePrice decimal.Decimal) []PriceLevel {
	levels := make([]PriceLevel, count)
	currentPrice := basePrice
	for i := 0; i < count; i++ {
		// Each successive bid price is lower by a random decrement
		decrement := decimal.NewFromInt(int64(rapid.IntRange(1, 1000).Draw(t, "bid_decr")))
		decrement = decrement.Div(decimal.NewFromInt(100))
		currentPrice = currentPrice.Sub(decrement)
		if currentPrice.LessThanOrEqual(decimal.Zero) {
			currentPrice = decimal.NewFromFloat(0.01)
		}

		qtyInt := rapid.IntRange(1, 10000).Draw(t, "bid_qty")
		qty := decimal.NewFromInt(int64(qtyInt)).Div(decimal.NewFromInt(1000))

		levels[i] = PriceLevel{Price: currentPrice, Quantity: qty}
	}
	return levels
}

// totalLiquidity calculates the sum of all quantities across levels.
func totalLiquidity(levels []PriceLevel) decimal.Decimal {
	total := decimal.Zero
	for _, l := range levels {
		total = total.Add(l.Quantity)
	}
	return total
}

// manualVWAP computes the expected VWAP by walking through levels from the start.
func manualVWAP(levels []PriceLevel, quantity decimal.Decimal) decimal.Decimal {
	remaining := quantity
	totalNotional := decimal.Zero

	for _, level := range levels {
		if remaining.IsZero() || remaining.IsNegative() {
			break
		}
		fillQty := decimal.Min(remaining, level.Quantity)
		totalNotional = totalNotional.Add(fillQty.Mul(level.Price))
		remaining = remaining.Sub(fillQty)
	}

	return totalNotional.Div(quantity)
}

// TestProperty_VWAPCalculation_MatchesManual tests that for any order book with
// sufficient liquidity and a requested quantity Q, the VWAP equals the manual
// calculation: sum(price * qty_consumed_at_level) / Q.
func TestProperty_VWAPCalculation_MatchesManual(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a random mid-price
		midPriceInt := rapid.IntRange(100, 100000).Draw(t, "midPrice")
		midPrice := decimal.NewFromInt(int64(midPriceInt)).Div(decimal.NewFromInt(100))

		// Generate 1-20 ask levels starting above midPrice
		numAsks := rapid.IntRange(1, 20).Draw(t, "numAsks")
		asks := genAskLevels(t, numAsks, midPrice)

		// Generate 1-20 bid levels starting below midPrice
		numBids := rapid.IntRange(1, 20).Draw(t, "numBids")
		bids := genBidLevels(t, numBids, midPrice)

		ob := NewLocalOrderBook()
		snapshot := &OrderBookSnapshot{
			Symbol:     "TEST-USDT",
			Bids:       bids,
			Asks:       asks,
			SequenceID: 1,
			Timestamp:  1000,
		}
		ob.UpdateFromSnapshot("TEST-USDT", snapshot)

		// Test buy side (consumes asks)
		askLiquidity := totalLiquidity(asks)
		if askLiquidity.IsPositive() {
			// Generate a quantity <= available liquidity
			maxQtyInt := askLiquidity.Mul(decimal.NewFromInt(1000)).IntPart()
			if maxQtyInt > 1 {
				qtyInt := rapid.Int64Range(1, maxQtyInt).Draw(t, "buyQty")
				quantity := decimal.NewFromInt(qtyInt).Div(decimal.NewFromInt(1000))

				vwap, err := ob.GetVWAP("TEST-USDT", models.SideBuy, quantity)
				if err != nil {
					t.Fatalf("unexpected error for buy VWAP: %v", err)
				}

				expected := manualVWAP(asks, quantity).Round(8)
				if !vwap.Equal(expected) {
					t.Fatalf("buy VWAP mismatch: got %s, expected %s (quantity=%s)",
						vwap, expected, quantity)
				}
			}
		}

		// Test sell side (consumes bids)
		bidLiquidity := totalLiquidity(bids)
		if bidLiquidity.IsPositive() {
			maxQtyInt := bidLiquidity.Mul(decimal.NewFromInt(1000)).IntPart()
			if maxQtyInt > 1 {
				qtyInt := rapid.Int64Range(1, maxQtyInt).Draw(t, "sellQty")
				quantity := decimal.NewFromInt(qtyInt).Div(decimal.NewFromInt(1000))

				vwap, err := ob.GetVWAP("TEST-USDT", models.SideSell, quantity)
				if err != nil {
					t.Fatalf("unexpected error for sell VWAP: %v", err)
				}

				expected := manualVWAP(bids, quantity).Round(8)
				if !vwap.Equal(expected) {
					t.Fatalf("sell VWAP mismatch: got %s, expected %s (quantity=%s)",
						vwap, expected, quantity)
				}
			}
		}
	})
}

// TestProperty_VWAPCalculation_InsufficientLiquidityReturnsError tests that if the
// total available liquidity < requested quantity, an error is returned.
func TestProperty_VWAPCalculation_InsufficientLiquidityReturnsError(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a random order book
		midPriceInt := rapid.IntRange(100, 100000).Draw(t, "midPrice")
		midPrice := decimal.NewFromInt(int64(midPriceInt)).Div(decimal.NewFromInt(100))

		numAsks := rapid.IntRange(1, 10).Draw(t, "numAsks")
		asks := genAskLevels(t, numAsks, midPrice)

		numBids := rapid.IntRange(1, 10).Draw(t, "numBids")
		bids := genBidLevels(t, numBids, midPrice)

		ob := NewLocalOrderBook()
		snapshot := &OrderBookSnapshot{
			Symbol:     "TEST-USDT",
			Bids:       bids,
			Asks:       asks,
			SequenceID: 1,
			Timestamp:  1000,
		}
		ob.UpdateFromSnapshot("TEST-USDT", snapshot)

		// Request more than available on ask side
		askLiquidity := totalLiquidity(asks)
		excessInt := rapid.IntRange(1, 10000).Draw(t, "excess")
		excess := decimal.NewFromInt(int64(excessInt)).Div(decimal.NewFromInt(1000))
		requestedQty := askLiquidity.Add(excess)

		_, err := ob.GetVWAP("TEST-USDT", models.SideBuy, requestedQty)
		if err == nil {
			t.Fatalf("expected error for insufficient buy liquidity: requested %s, available %s",
				requestedQty, askLiquidity)
		}

		// Request more than available on bid side
		bidLiquidity := totalLiquidity(bids)
		excessInt2 := rapid.IntRange(1, 10000).Draw(t, "excess2")
		excess2 := decimal.NewFromInt(int64(excessInt2)).Div(decimal.NewFromInt(1000))
		requestedQty2 := bidLiquidity.Add(excess2)

		_, err = ob.GetVWAP("TEST-USDT", models.SideSell, requestedQty2)
		if err == nil {
			t.Fatalf("expected error for insufficient sell liquidity: requested %s, available %s",
				requestedQty2, bidLiquidity)
		}
	})
}

// TestProperty_VWAPCalculation_BetweenBestAndWorstPrice tests that the computed VWAP
// is between the best price (first level consumed) and the worst price (last level
// consumed) for any valid request.
func TestProperty_VWAPCalculation_BetweenBestAndWorstPrice(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		midPriceInt := rapid.IntRange(100, 100000).Draw(t, "midPrice")
		midPrice := decimal.NewFromInt(int64(midPriceInt)).Div(decimal.NewFromInt(100))

		// Need at least 2 levels to have a meaningful best/worst check
		numAsks := rapid.IntRange(2, 15).Draw(t, "numAsks")
		asks := genAskLevels(t, numAsks, midPrice)

		numBids := rapid.IntRange(2, 15).Draw(t, "numBids")
		bids := genBidLevels(t, numBids, midPrice)

		ob := NewLocalOrderBook()
		snapshot := &OrderBookSnapshot{
			Symbol:     "TEST-USDT",
			Bids:       bids,
			Asks:       asks,
			SequenceID: 1,
			Timestamp:  1000,
		}
		ob.UpdateFromSnapshot("TEST-USDT", snapshot)

		// Test buy side: VWAP should be between best ask and worst ask consumed
		askLiquidity := totalLiquidity(asks)
		if askLiquidity.IsPositive() {
			maxQtyInt := askLiquidity.Mul(decimal.NewFromInt(1000)).IntPart()
			if maxQtyInt > 1 {
				qtyInt := rapid.Int64Range(1, maxQtyInt).Draw(t, "buyQty")
				quantity := decimal.NewFromInt(qtyInt).Div(decimal.NewFromInt(1000))

				vwap, err := ob.GetVWAP("TEST-USDT", models.SideBuy, quantity)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				// Find worst (highest) ask price consumed
				bestAsk := asks[0].Price
				worstAsk := findWorstPriceConsumed(asks, quantity)

				if vwap.LessThan(bestAsk) {
					t.Fatalf("buy VWAP %s is less than best ask %s", vwap, bestAsk)
				}
				if vwap.GreaterThan(worstAsk) {
					t.Fatalf("buy VWAP %s is greater than worst ask consumed %s", vwap, worstAsk)
				}
			}
		}

		// Test sell side: VWAP should be between best bid and worst bid consumed
		bidLiquidity := totalLiquidity(bids)
		if bidLiquidity.IsPositive() {
			maxQtyInt := bidLiquidity.Mul(decimal.NewFromInt(1000)).IntPart()
			if maxQtyInt > 1 {
				qtyInt := rapid.Int64Range(1, maxQtyInt).Draw(t, "sellQty")
				quantity := decimal.NewFromInt(qtyInt).Div(decimal.NewFromInt(1000))

				vwap, err := ob.GetVWAP("TEST-USDT", models.SideSell, quantity)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				// For bids (descending), best is first (highest), worst is last consumed (lowest)
				bestBid := bids[0].Price
				worstBid := findWorstPriceConsumed(bids, quantity)

				// For sell side, best price is highest (first bid), worst is lowest consumed
				if vwap.GreaterThan(bestBid) {
					t.Fatalf("sell VWAP %s is greater than best bid %s", vwap, bestBid)
				}
				if vwap.LessThan(worstBid) {
					t.Fatalf("sell VWAP %s is less than worst bid consumed %s", vwap, worstBid)
				}
			}
		}
	})
}

// TestProperty_VWAPCalculation_SingleLevelEqualsThatLevelPrice tests that if the
// requested quantity can be filled entirely from the first level, the VWAP equals
// that level's price.
func TestProperty_VWAPCalculation_SingleLevelEqualsThatLevelPrice(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		midPriceInt := rapid.IntRange(100, 100000).Draw(t, "midPrice")
		midPrice := decimal.NewFromInt(int64(midPriceInt)).Div(decimal.NewFromInt(100))

		numAsks := rapid.IntRange(1, 10).Draw(t, "numAsks")
		asks := genAskLevels(t, numAsks, midPrice)

		numBids := rapid.IntRange(1, 10).Draw(t, "numBids")
		bids := genBidLevels(t, numBids, midPrice)

		ob := NewLocalOrderBook()
		snapshot := &OrderBookSnapshot{
			Symbol:     "TEST-USDT",
			Bids:       bids,
			Asks:       asks,
			SequenceID: 1,
			Timestamp:  1000,
		}
		ob.UpdateFromSnapshot("TEST-USDT", snapshot)

		// Buy side: request quantity <= first ask level quantity
		firstAskQty := asks[0].Quantity
		if firstAskQty.IsPositive() {
			maxQtyInt := firstAskQty.Mul(decimal.NewFromInt(1000)).IntPart()
			if maxQtyInt > 0 {
				qtyInt := rapid.Int64Range(1, maxQtyInt).Draw(t, "buyQty")
				quantity := decimal.NewFromInt(qtyInt).Div(decimal.NewFromInt(1000))

				vwap, err := ob.GetVWAP("TEST-USDT", models.SideBuy, quantity)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				expectedPrice := asks[0].Price.Round(8)
				if !vwap.Equal(expectedPrice) {
					t.Fatalf("buy VWAP for single level should be %s, got %s (qty=%s, level qty=%s)",
						expectedPrice, vwap, quantity, firstAskQty)
				}
			}
		}

		// Sell side: request quantity <= first bid level quantity
		firstBidQty := bids[0].Quantity
		if firstBidQty.IsPositive() {
			maxQtyInt := firstBidQty.Mul(decimal.NewFromInt(1000)).IntPart()
			if maxQtyInt > 0 {
				qtyInt := rapid.Int64Range(1, maxQtyInt).Draw(t, "sellQty")
				quantity := decimal.NewFromInt(qtyInt).Div(decimal.NewFromInt(1000))

				vwap, err := ob.GetVWAP("TEST-USDT", models.SideSell, quantity)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				expectedPrice := bids[0].Price.Round(8)
				if !vwap.Equal(expectedPrice) {
					t.Fatalf("sell VWAP for single level should be %s, got %s (qty=%s, level qty=%s)",
						expectedPrice, vwap, quantity, firstBidQty)
				}
			}
		}
	})
}

// TestProperty_VWAPCalculation_WalksFromBestPrice tests that the VWAP walks from
// the best price level (best ask for buys, best bid for sells) accumulating volume
// sequentially through levels.
func TestProperty_VWAPCalculation_WalksFromBestPrice(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		midPriceInt := rapid.IntRange(100, 100000).Draw(t, "midPrice")
		midPrice := decimal.NewFromInt(int64(midPriceInt)).Div(decimal.NewFromInt(100))

		numAsks := rapid.IntRange(2, 10).Draw(t, "numAsks")
		asks := genAskLevels(t, numAsks, midPrice)

		numBids := rapid.IntRange(2, 10).Draw(t, "numBids")
		bids := genBidLevels(t, numBids, midPrice)

		ob := NewLocalOrderBook()
		snapshot := &OrderBookSnapshot{
			Symbol:     "TEST-USDT",
			Bids:       bids,
			Asks:       asks,
			SequenceID: 1,
			Timestamp:  1000,
		}
		ob.UpdateFromSnapshot("TEST-USDT", snapshot)

		// For buy side: request quantity that spans exactly 2 levels
		// This verifies the walking behavior
		if len(asks) >= 2 {
			// Request exactly the quantity of the first ask level + half of the second
			firstLevelQty := asks[0].Quantity
			secondLevelHalf := asks[1].Quantity.Div(decimal.NewFromInt(2))
			if secondLevelHalf.IsPositive() {
				totalRequest := firstLevelQty.Add(secondLevelHalf)
				askLiquidity := totalLiquidity(asks)

				if totalRequest.LessThanOrEqual(askLiquidity) {
					vwap, err := ob.GetVWAP("TEST-USDT", models.SideBuy, totalRequest)
					if err != nil {
						t.Fatalf("unexpected error: %v", err)
					}

					// Manual calculation: full first level + half of second level
					notional := asks[0].Price.Mul(firstLevelQty).Add(asks[1].Price.Mul(secondLevelHalf))
					expected := notional.Div(totalRequest).Round(8)

					if !vwap.Equal(expected) {
						t.Fatalf("buy VWAP walking verification failed: got %s, expected %s", vwap, expected)
					}
				}
			}
		}

		// For sell side: similar verification
		if len(bids) >= 2 {
			firstLevelQty := bids[0].Quantity
			secondLevelHalf := bids[1].Quantity.Div(decimal.NewFromInt(2))
			if secondLevelHalf.IsPositive() {
				totalRequest := firstLevelQty.Add(secondLevelHalf)
				bidLiquidity := totalLiquidity(bids)

				if totalRequest.LessThanOrEqual(bidLiquidity) {
					vwap, err := ob.GetVWAP("TEST-USDT", models.SideSell, totalRequest)
					if err != nil {
						t.Fatalf("unexpected error: %v", err)
					}

					// Manual calculation: full first level + half of second level
					notional := bids[0].Price.Mul(firstLevelQty).Add(bids[1].Price.Mul(secondLevelHalf))
					expected := notional.Div(totalRequest).Round(8)

					if !vwap.Equal(expected) {
						t.Fatalf("sell VWAP walking verification failed: got %s, expected %s", vwap, expected)
					}
				}
			}
		}
	})
}

// findWorstPriceConsumed returns the price of the last level that gets (partially) consumed
// when filling the given quantity by walking through levels sequentially.
func findWorstPriceConsumed(levels []PriceLevel, quantity decimal.Decimal) decimal.Decimal {
	remaining := quantity
	worstPrice := levels[0].Price

	for _, level := range levels {
		if remaining.IsZero() || remaining.IsNegative() {
			break
		}
		worstPrice = level.Price
		remaining = remaining.Sub(level.Quantity)
	}

	return worstPrice
}
