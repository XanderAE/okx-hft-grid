package strategy

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirements 3.1**
//
// PRE-01 records the healthy, non-duplicate fill path. The generated fill
// quantity is exactly the configured quantity so this preservation oracle does
// not overlap the cumulative/delta bug condition explored by Property 1.
func TestProperty2_Preservation_PRE01_HealthyBuySellFillSemantics(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		symbols := []string{"DOGE-USDT", "WIF-USDT"}
		symbol := symbols[rapid.IntRange(0, len(symbols)-1).Draw(t, "symbol_index")]
		base := decimal.NewFromInt(int64(rapid.IntRange(1000, 5000).Draw(t, "base_cents"))).Div(decimal.NewFromInt(100))
		step := decimal.NewFromInt(int64(rapid.IntRange(100, 500).Draw(t, "step_cents"))).Div(decimal.NewFromInt(100))
		quantity := decimal.NewFromInt(int64(rapid.IntRange(2, 200).Draw(t, "quantity_tenths"))).Div(decimal.NewFromInt(10))

		levels := make([]models.GridLevel, 4)
		for i := range levels {
			levels[i] = models.GridLevel{Index: i, Price: base.Add(step.Mul(decimal.NewFromInt(int64(i))))}
		}
		state := &GridState{Levels: levels}
		cfg := &models.GridConfig{
			Symbol:      symbol,
			OrderSize:   quantity,
			MaxPosition: quantity.Mul(decimal.NewFromInt(10)),
			FeeRate:     decimal.RequireFromString("0.001"),
		}

		buy := models.FillEvent{
			Symbol: symbol, Side: models.SideBuy, Price: levels[1].Price,
			Quantity: quantity, GridLevel: 1, IsMaker: true,
		}
		buyResult := HandleGridFill(buy, state, cfg)
		if buyResult.Error != nil {
			t.Fatalf("PRE-01 BUY error: %v", buyResult.Error)
		}
		assertPreservedOrder(t, buyResult.CounterOrder, symbol, models.SideSell, levels[2].Price, quantity)
		if !state.Position.Equal(quantity) || !state.AvgEntryPrice.Equal(buy.Price) || !state.RealizedPnL.IsZero() {
			t.Fatalf("PRE-01 BUY state mismatch: position=%s avg=%s pnl=%s, want %s/%s/0",
				state.Position, state.AvgEntryPrice, state.RealizedPnL, quantity, buy.Price)
		}
		if state.TotalBuys != 1 || state.TotalSells != 0 {
			t.Fatalf("PRE-01 BUY counters changed: buys=%d sells=%d", state.TotalBuys, state.TotalSells)
		}

		sellQuantity := quantity.Div(decimal.NewFromInt(2))
		sell := models.FillEvent{
			Symbol: symbol, Side: models.SideSell, Price: levels[2].Price,
			Quantity: sellQuantity, GridLevel: 2, IsMaker: true,
		}
		sellResult := HandleGridFill(sell, state, cfg)
		if sellResult.Error != nil {
			t.Fatalf("PRE-01 SELL error: %v", sellResult.Error)
		}
		assertPreservedOrder(t, sellResult.CounterOrder, symbol, models.SideBuy, levels[1].Price, sellQuantity)

		expectedPnL := sell.Price.Sub(buy.Price).Mul(sellQuantity)
		expectedPosition := quantity.Sub(sellQuantity)
		if !state.Position.Equal(expectedPosition) || !state.AvgEntryPrice.Equal(buy.Price) || !state.RealizedPnL.Equal(expectedPnL) {
			t.Fatalf("PRE-01 SELL state mismatch: position=%s avg=%s pnl=%s, want %s/%s/%s",
				state.Position, state.AvgEntryPrice, state.RealizedPnL, expectedPosition, buy.Price, expectedPnL)
		}
		if !sellResult.RealizedPnL.Equal(expectedPnL) || state.TotalBuys != 1 || state.TotalSells != 1 {
			t.Fatalf("PRE-01 result/counter mismatch: result_pnl=%s buys=%d sells=%d", sellResult.RealizedPnL, state.TotalBuys, state.TotalSells)
		}
		if !state.Levels[1].HasBuyOrder || state.Levels[2].HasSellOrder {
			t.Fatalf("PRE-01 level occupancy changed: level1.buy=%t level2.sell=%t", state.Levels[1].HasBuyOrder, state.Levels[2].HasSellOrder)
		}
	})
}

// **Validates: Requirements 3.7**
//
// PRE-07 freezes the pre-normalization grid decisions. Mandatory cash mode and
// exact instrument normalization are intentionally outside this comparator;
// symbol, side, level price, quantity, POST_ONLY, profit and risk decisions are not.
func TestProperty2_Preservation_PRE07_PostOnlyDirectionProfitAndRisk(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		symbols := []string{"DOGE-USDT", "WIF-USDT"}
		symbol := symbols[rapid.IntRange(0, len(symbols)-1).Draw(t, "symbol_index")]
		levelCount := rapid.IntRange(5, 12).Draw(t, "level_count")
		currentIndex := rapid.IntRange(1, levelCount-2).Draw(t, "current_index")
		base := decimal.NewFromInt(int64(rapid.IntRange(1000, 5000).Draw(t, "base_cents"))).Div(decimal.NewFromInt(100))
		step := decimal.NewFromInt(int64(rapid.IntRange(100, 500).Draw(t, "step_cents"))).Div(decimal.NewFromInt(100))
		quantity := decimal.NewFromInt(int64(rapid.IntRange(1, 100).Draw(t, "quantity")))

		prices := make([]decimal.Decimal, levelCount)
		gridLevels := make([]models.GridLevel, levelCount)
		for i := range prices {
			prices[i] = base.Add(step.Mul(decimal.NewFromInt(int64(i))))
			gridLevels[i] = models.GridLevel{Index: i, Price: prices[i]}
		}
		current := prices[currentIndex]
		cfg := &models.GridConfig{Symbol: symbol, OrderSize: quantity, MaxPosition: quantity.Mul(decimal.NewFromInt(10))}
		orders := PlaceGridOrders(prices, current, cfg)
		if len(orders) != len(prices)-1 {
			t.Fatalf("PRE-07 order count=%d, want %d", len(orders), len(prices)-1)
		}
		seen := make(map[string]models.Side, len(orders))
		for _, order := range orders {
			if order.Symbol != symbol || order.OrderType != models.OrderTypePostOnly || !order.Quantity.Equal(quantity) {
				t.Fatalf("PRE-07 order metadata changed: symbol=%s type=%s qty=%s", order.Symbol, order.OrderType, order.Quantity)
			}
			expectedSide := models.SideSell
			if order.Price.LessThan(current) {
				expectedSide = models.SideBuy
			} else if !order.Price.GreaterThan(current) {
				t.Fatalf("PRE-07 emitted an order at current price %s", current)
			}
			if order.Side != expectedSide {
				t.Fatalf("PRE-07 side changed at level %s: got %s want %s", order.Price, order.Side, expectedSide)
			}
			seen[order.Price.String()] = order.Side
		}
		for i, price := range prices {
			if i == currentIndex {
				if _, ok := seen[price.String()]; ok {
					t.Fatalf("PRE-07 current level %s unexpectedly occupied", price)
				}
				continue
			}
			if _, ok := seen[price.String()]; !ok {
				t.Fatalf("PRE-07 level %s missing", price)
			}
		}

		feeRate := decimal.NewFromInt(int64(rapid.IntRange(0, 10).Draw(t, "fee_bps"))).Div(decimal.NewFromInt(10000))
		profitCfg := *cfg
		profitCfg.FeeRate = feeRate
		profitState := &GridState{Levels: append([]models.GridLevel(nil), gridLevels...)}
		buyLevel := currentIndex - 1
		buyFill := models.FillEvent{Symbol: symbol, Side: models.SideBuy, Price: prices[buyLevel], Quantity: quantity, GridLevel: buyLevel}
		profitResult := HandleGridFill(buyFill, profitState, &profitCfg)
		netProfit := prices[buyLevel+1].Sub(prices[buyLevel]).Mul(quantity).
			Sub(prices[buyLevel].Add(prices[buyLevel+1]).Mul(quantity).Mul(feeRate))
		if !netProfit.IsPositive() || profitResult.CounterOrder == nil {
			t.Fatalf("PRE-07 profitable counter decision changed: net=%s reason=%q", netProfit, profitResult.Reason)
		}
		assertPreservedOrder(t, profitResult.CounterOrder, symbol, models.SideSell, prices[buyLevel+1], quantity)

		blockedCfg := *cfg
		blockedCfg.MaxPosition = quantity.Sub(decimal.RequireFromString("0.1"))
		blockedState := &GridState{
			Levels: append([]models.GridLevel(nil), gridLevels...), Position: quantity,
			AvgEntryPrice: prices[currentIndex-1],
		}
		sellFill := models.FillEvent{Symbol: symbol, Side: models.SideSell, Price: prices[currentIndex], Quantity: quantity, GridLevel: currentIndex}
		blocked := HandleGridFill(sellFill, blockedState, &blockedCfg)
		if blocked.CounterOrder != nil || !strings.Contains(blocked.Reason, "position limit") {
			t.Fatalf("PRE-07 risk rejection changed: order=%+v reason=%q", blocked.CounterOrder, blocked.Reason)
		}

		allowedCfg := *cfg
		allowedCfg.MaxPosition = quantity
		allowedState := &GridState{
			Levels: append([]models.GridLevel(nil), gridLevels...), Position: quantity,
			AvgEntryPrice: prices[currentIndex-1],
		}
		allowed := HandleGridFill(sellFill, allowedState, &allowedCfg)
		assertPreservedOrder(t, allowed.CounterOrder, symbol, models.SideBuy, prices[currentIndex-1], quantity)
	})
}

func assertPreservedOrder(t interface{ Fatalf(string, ...any) }, order *models.Order, symbol string, side models.Side, price, quantity decimal.Decimal) {
	if order == nil {
		t.Fatalf("expected preserved counter order, got nil")
	}
	if order.Symbol != symbol || order.Side != side || order.OrderType != models.OrderTypePostOnly ||
		!order.Price.Equal(price) || !order.Quantity.Equal(quantity) || order.Status != models.OrderStatusPending {
		t.Fatalf("counter order mismatch: got {symbol:%s side:%s type:%s price:%s qty:%s status:%s}, want {%s %s POST_ONLY %s %s PENDING}",
			order.Symbol, order.Side, order.OrderType, order.Price, order.Quantity, order.Status,
			symbol, side, price, quantity)
	}
}
