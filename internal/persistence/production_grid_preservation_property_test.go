package persistence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirements 3.8**
//
// PRE-08 records the logical order, position, strategy and PnL state supported
// by the unfixed store. Every database and WAL lives below t.TempDir(). Added
// durability records may be ignored later, but these logical fields may not.
func TestProperty2_Preservation_PRE08_PersistenceLogicalRoundTrip(t *testing.T) {
	root := t.TempDir()
	iteration := 0

	rapid.Check(t, func(t *rapid.T) {
		iteration++
		dbPath := filepath.Join(root, fmt.Sprintf("preservation-%03d.db", iteration))
		store, err := NewSQLiteStore(dbPath)
		if err != nil {
			t.Fatalf("PRE-08 open: %v", err)
		}

		basePrice := decimal.NewFromInt(int64(rapid.IntRange(1_000, 100_000).Draw(t, "base_price_cents"))).Div(decimal.NewFromInt(100))
		quantity := decimal.NewFromInt(int64(rapid.IntRange(1, 10_000).Draw(t, "quantity_millis"))).Div(decimal.NewFromInt(1000))
		filled := quantity.Div(decimal.NewFromInt(2))
		realizedPnL := decimal.NewFromInt(int64(rapid.IntRange(-100_000, 100_000).Draw(t, "realized_pnl_cents"))).Div(decimal.NewFromInt(100))
		unrealizedPnL := decimal.NewFromInt(int64(rapid.IntRange(-100_000, 100_000).Draw(t, "unrealized_pnl_cents"))).Div(decimal.NewFromInt(100))

		symbols := []string{"DOGE-USDT", "WIF-USDT"}
		expectedOrders := make([]*models.Order, 0, len(symbols))
		expectedPositions := make([]*models.Position, 0, len(symbols))
		expectedStates := make([]StrategyStateRow, 0, len(symbols))
		for i, symbol := range symbols {
			price := basePrice.Add(decimal.NewFromInt(int64(i)))
			side := models.Side(i % 2)
			order := &models.Order{
				OrderId: fmt.Sprintf("pre08-%d-%s", iteration, symbol), ExchangeOrderId: fmt.Sprintf("sim-ex-%d-%d", iteration, i),
				Symbol: symbol, Side: side, Price: price, Quantity: quantity,
				FilledQuantity: filled, Status: models.OrderStatusPartiallyFilled,
				StrategyId: "grid-" + symbol, CreateTime: 1_735_787_045_000 + int64(i),
				UpdateTime: 1_735_787_046_000 + int64(i),
			}
			position := &models.Position{
				Symbol: symbol, Side: side, Quantity: quantity, AvgEntryPrice: price,
				UnrealizedPnL: unrealizedPnL, RealizedPnL: realizedPnL,
			}
			configJSON, marshalErr := json.Marshal(map[string]any{
				"symbol": symbol, "order_size": quantity.String(), "max_position": quantity.Mul(decimal.NewFromInt(10)).String(),
			})
			if marshalErr != nil {
				t.Fatalf("PRE-08 config JSON: %v", marshalErr)
			}
			stateJSON, marshalErr := json.Marshal(map[string]any{
				"position": quantity.String(), "avg_entry_price": price.String(),
				"realized_pnl": realizedPnL.String(), "total_buys": 7 + i, "total_sells": 3 + i,
			})
			if marshalErr != nil {
				t.Fatalf("PRE-08 state JSON: %v", marshalErr)
			}

			if err := store.SaveOrder(order); err != nil {
				t.Fatalf("PRE-08 SaveOrder(%s): %v", symbol, err)
			}
			if err := store.SavePosition(position); err != nil {
				t.Fatalf("PRE-08 SavePosition(%s): %v", symbol, err)
			}
			strategyID := "grid-" + symbol
			if err := store.SaveStrategyState(strategyID, "grid", true, configJSON, stateJSON); err != nil {
				t.Fatalf("PRE-08 SaveStrategyState(%s): %v", symbol, err)
			}
			expectedOrders = append(expectedOrders, order)
			expectedPositions = append(expectedPositions, position)
			expectedStates = append(expectedStates, StrategyStateRow{
				StrategyID: strategyID, Type: "grid", IsActive: true, ConfigJSON: configJSON, StateJSON: stateJSON,
			})
		}
		if err := store.Close(); err != nil {
			t.Fatalf("PRE-08 close first session: %v", err)
		}

		reopened, err := NewSQLiteStore(dbPath)
		if err != nil {
			t.Fatalf("PRE-08 reopen: %v", err)
		}
		orders, orderErr := reopened.LoadOrders()
		positions, positionErr := reopened.LoadPositions()
		states, stateErr := reopened.LoadStrategyStates()
		if closeErr := reopened.Close(); closeErr != nil {
			t.Fatalf("PRE-08 close second session: %v", closeErr)
		}
		if orderErr != nil || positionErr != nil || stateErr != nil {
			t.Fatalf("PRE-08 load errors: orders=%v positions=%v states=%v", orderErr, positionErr, stateErr)
		}

		sort.Slice(expectedOrders, func(i, j int) bool { return expectedOrders[i].Symbol < expectedOrders[j].Symbol })
		sort.Slice(orders, func(i, j int) bool { return orders[i].Symbol < orders[j].Symbol })
		sort.Slice(expectedPositions, func(i, j int) bool { return expectedPositions[i].Symbol < expectedPositions[j].Symbol })
		sort.Slice(positions, func(i, j int) bool { return positions[i].Symbol < positions[j].Symbol })
		sort.Slice(expectedStates, func(i, j int) bool { return expectedStates[i].StrategyID < expectedStates[j].StrategyID })
		sort.Slice(states, func(i, j int) bool { return states[i].StrategyID < states[j].StrategyID })

		if len(orders) != len(expectedOrders) || len(positions) != len(expectedPositions) || len(states) != len(expectedStates) {
			t.Fatalf("PRE-08 row counts changed: orders=%d/%d positions=%d/%d states=%d/%d",
				len(orders), len(expectedOrders), len(positions), len(expectedPositions), len(states), len(expectedStates))
		}
		for i := range expectedOrders {
			assertPreservedStoredOrder(t, orders[i], expectedOrders[i])
			assertPreservedStoredPosition(t, positions[i], expectedPositions[i])
			if states[i].StrategyID != expectedStates[i].StrategyID || states[i].Type != expectedStates[i].Type ||
				states[i].IsActive != expectedStates[i].IsActive || !bytes.Equal(states[i].ConfigJSON, expectedStates[i].ConfigJSON) ||
				!bytes.Equal(states[i].StateJSON, expectedStates[i].StateJSON) {
				t.Fatalf("PRE-08 strategy state changed: got=%+v want=%+v", states[i], expectedStates[i])
			}
		}
	})
}

func assertPreservedStoredOrder(t interface{ Fatalf(string, ...any) }, got, want *models.Order) {
	if got.OrderId != want.OrderId || got.ExchangeOrderId != want.ExchangeOrderId || got.Symbol != want.Symbol ||
		got.Side != want.Side || !got.Price.Equal(want.Price) || !got.Quantity.Equal(want.Quantity) ||
		!got.FilledQuantity.Equal(want.FilledQuantity) || got.Status != want.Status || got.StrategyId != want.StrategyId ||
		got.CreateTime != want.CreateTime || got.UpdateTime != want.UpdateTime {
		t.Fatalf("PRE-08 order changed: got=%+v want=%+v", got, want)
	}
}

func assertPreservedStoredPosition(t interface{ Fatalf(string, ...any) }, got, want *models.Position) {
	if got.Symbol != want.Symbol || got.Side != want.Side || !got.Quantity.Equal(want.Quantity) ||
		!got.AvgEntryPrice.Equal(want.AvgEntryPrice) || !got.UnrealizedPnL.Equal(want.UnrealizedPnL) ||
		!got.RealizedPnL.Equal(want.RealizedPnL) {
		t.Fatalf("PRE-08 position changed: got=%+v want=%+v", got, want)
	}
}
