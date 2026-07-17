package persistence

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

func tempDBPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "test.db")
}

func TestNewSQLiteStore_CreatesDB(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	// DB file should exist
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("expected database file to be created")
	}
}

func TestNewSQLiteStore_CorruptedPath(t *testing.T) {
	// Try to open a directory as a database file
	dir := t.TempDir()
	_, err := NewSQLiteStore(dir)
	if err == nil {
		t.Fatal("expected error for invalid db path")
	}
	if !errors.Is(err, ErrCorrupted) {
		t.Errorf("expected ErrCorrupted, got %v", err)
	}
}

func TestSaveAndLoadOrders(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	order := &models.Order{
		OrderId:         "order-001",
		ExchangeOrderId: "exch-001",
		Symbol:          "BTC-USDT",
		Side:            models.SideBuy,
		Price:           decimal.NewFromFloat(50000.50),
		Quantity:        decimal.NewFromFloat(0.1),
		FilledQuantity:  decimal.NewFromFloat(0.05),
		Status:          models.OrderStatusOpen,
		StrategyId:      "grid-1",
		CreateTime:      1700000000000,
		UpdateTime:      1700000001000,
	}

	// Save order
	if err := store.SaveOrder(order); err != nil {
		t.Fatalf("SaveOrder() error = %v", err)
	}

	// Load orders
	orders, err := store.LoadOrders()
	if err != nil {
		t.Fatalf("LoadOrders() error = %v", err)
	}

	if len(orders) != 1 {
		t.Fatalf("expected 1 order, got %d", len(orders))
	}

	loaded := orders[0]
	if loaded.OrderId != order.OrderId {
		t.Errorf("OrderId = %s, want %s", loaded.OrderId, order.OrderId)
	}
	if loaded.ExchangeOrderId != order.ExchangeOrderId {
		t.Errorf("ExchangeOrderId = %s, want %s", loaded.ExchangeOrderId, order.ExchangeOrderId)
	}
	if loaded.Symbol != order.Symbol {
		t.Errorf("Symbol = %s, want %s", loaded.Symbol, order.Symbol)
	}
	if loaded.Side != order.Side {
		t.Errorf("Side = %v, want %v", loaded.Side, order.Side)
	}
	if !loaded.Price.Equal(order.Price) {
		t.Errorf("Price = %s, want %s", loaded.Price, order.Price)
	}
	if !loaded.Quantity.Equal(order.Quantity) {
		t.Errorf("Quantity = %s, want %s", loaded.Quantity, order.Quantity)
	}
	if !loaded.FilledQuantity.Equal(order.FilledQuantity) {
		t.Errorf("FilledQuantity = %s, want %s", loaded.FilledQuantity, order.FilledQuantity)
	}
	if loaded.Status != order.Status {
		t.Errorf("Status = %v, want %v", loaded.Status, order.Status)
	}
	if loaded.StrategyId != order.StrategyId {
		t.Errorf("StrategyId = %s, want %s", loaded.StrategyId, order.StrategyId)
	}
	if loaded.CreateTime != order.CreateTime {
		t.Errorf("CreateTime = %d, want %d", loaded.CreateTime, order.CreateTime)
	}
	if loaded.UpdateTime != order.UpdateTime {
		t.Errorf("UpdateTime = %d, want %d", loaded.UpdateTime, order.UpdateTime)
	}
}

func TestSaveOrder_UpdateExisting(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	order := &models.Order{
		OrderId:    "order-001",
		Symbol:     "ETH-USDT",
		Side:       models.SideSell,
		Price:      decimal.NewFromFloat(3000.00),
		Quantity:   decimal.NewFromFloat(1.0),
		Status:     models.OrderStatusPending,
		CreateTime: 1700000000000,
		UpdateTime: 1700000000000,
	}

	if err := store.SaveOrder(order); err != nil {
		t.Fatalf("SaveOrder() error = %v", err)
	}

	// Update the order
	order.Status = models.OrderStatusFilled
	order.FilledQuantity = decimal.NewFromFloat(1.0)
	order.UpdateTime = 1700000005000

	if err := store.SaveOrder(order); err != nil {
		t.Fatalf("SaveOrder() update error = %v", err)
	}

	orders, err := store.LoadOrders()
	if err != nil {
		t.Fatalf("LoadOrders() error = %v", err)
	}

	if len(orders) != 1 {
		t.Fatalf("expected 1 order after update, got %d", len(orders))
	}

	if orders[0].Status != models.OrderStatusFilled {
		t.Errorf("Status = %v, want FILLED", orders[0].Status)
	}
	if !orders[0].FilledQuantity.Equal(decimal.NewFromFloat(1.0)) {
		t.Errorf("FilledQuantity = %s, want 1.0", orders[0].FilledQuantity)
	}
}

func TestSaveAndLoadPositions(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	pos := &models.Position{
		Symbol:        "BTC-USDT",
		Side:          models.SideBuy,
		Quantity:      decimal.NewFromFloat(0.5),
		AvgEntryPrice: decimal.NewFromFloat(48000.25),
		UnrealizedPnL: decimal.NewFromFloat(500.75),
		RealizedPnL:   decimal.NewFromFloat(120.30),
	}

	if err := store.SavePosition(pos); err != nil {
		t.Fatalf("SavePosition() error = %v", err)
	}

	positions, err := store.LoadPositions()
	if err != nil {
		t.Fatalf("LoadPositions() error = %v", err)
	}

	if len(positions) != 1 {
		t.Fatalf("expected 1 position, got %d", len(positions))
	}

	loaded := positions[0]
	if loaded.Symbol != pos.Symbol {
		t.Errorf("Symbol = %s, want %s", loaded.Symbol, pos.Symbol)
	}
	if loaded.Side != pos.Side {
		t.Errorf("Side = %v, want %v", loaded.Side, pos.Side)
	}
	if !loaded.Quantity.Equal(pos.Quantity) {
		t.Errorf("Quantity = %s, want %s", loaded.Quantity, pos.Quantity)
	}
	if !loaded.AvgEntryPrice.Equal(pos.AvgEntryPrice) {
		t.Errorf("AvgEntryPrice = %s, want %s", loaded.AvgEntryPrice, pos.AvgEntryPrice)
	}
	if !loaded.UnrealizedPnL.Equal(pos.UnrealizedPnL) {
		t.Errorf("UnrealizedPnL = %s, want %s", loaded.UnrealizedPnL, pos.UnrealizedPnL)
	}
	if !loaded.RealizedPnL.Equal(pos.RealizedPnL) {
		t.Errorf("RealizedPnL = %s, want %s", loaded.RealizedPnL, pos.RealizedPnL)
	}
}

func TestSavePosition_UpdateExisting(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	pos := &models.Position{
		Symbol:        "ETH-USDT",
		Side:          models.SideBuy,
		Quantity:      decimal.NewFromFloat(10.0),
		AvgEntryPrice: decimal.NewFromFloat(3000.00),
		UnrealizedPnL: decimal.Zero,
		RealizedPnL:   decimal.Zero,
	}

	if err := store.SavePosition(pos); err != nil {
		t.Fatalf("SavePosition() error = %v", err)
	}

	// Update
	pos.Quantity = decimal.NewFromFloat(15.0)
	pos.UnrealizedPnL = decimal.NewFromFloat(200.50)

	if err := store.SavePosition(pos); err != nil {
		t.Fatalf("SavePosition() update error = %v", err)
	}

	positions, err := store.LoadPositions()
	if err != nil {
		t.Fatalf("LoadPositions() error = %v", err)
	}

	if len(positions) != 1 {
		t.Fatalf("expected 1 position after update, got %d", len(positions))
	}
	if !positions[0].Quantity.Equal(decimal.NewFromFloat(15.0)) {
		t.Errorf("Quantity = %s, want 15.0", positions[0].Quantity)
	}
}

func TestSaveAndLoadStrategyState(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	configJSON := []byte(`{"upperPrice":"60000","lowerPrice":"40000","gridCount":20}`)
	stateJSON := []byte(`{"activeOrders":5,"lastFillPrice":"52000"}`)

	if err := store.SaveStrategyState("grid-1", "grid", true, configJSON, stateJSON); err != nil {
		t.Fatalf("SaveStrategyState() error = %v", err)
	}

	states, err := store.LoadStrategyStates()
	if err != nil {
		t.Fatalf("LoadStrategyStates() error = %v", err)
	}

	if len(states) != 1 {
		t.Fatalf("expected 1 strategy state, got %d", len(states))
	}

	s := states[0]
	if s.StrategyID != "grid-1" {
		t.Errorf("StrategyID = %s, want grid-1", s.StrategyID)
	}
	if s.Type != "grid" {
		t.Errorf("Type = %s, want grid", s.Type)
	}
	if !s.IsActive {
		t.Error("IsActive = false, want true")
	}
	if string(s.ConfigJSON) != string(configJSON) {
		t.Errorf("ConfigJSON = %s, want %s", s.ConfigJSON, configJSON)
	}
	if string(s.StateJSON) != string(stateJSON) {
		t.Errorf("StateJSON = %s, want %s", s.StateJSON, stateJSON)
	}
}

func TestSaveStrategyState_InvalidJSON(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	// Invalid config JSON
	err = store.SaveStrategyState("strat-1", "grid", true, []byte(`{invalid`), nil)
	if err == nil {
		t.Error("expected error for invalid config JSON")
	}

	// Invalid state JSON
	err = store.SaveStrategyState("strat-1", "grid", true, nil, []byte(`not json`))
	if err == nil {
		t.Error("expected error for invalid state JSON")
	}
}

func TestSaveStrategyState_EmptyJSON(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	// Empty JSON (nil) is valid
	if err := store.SaveStrategyState("strat-2", "mean_reversion", false, nil, nil); err != nil {
		t.Fatalf("SaveStrategyState() with nil JSON error = %v", err)
	}

	states, err := store.LoadStrategyStates()
	if err != nil {
		t.Fatalf("LoadStrategyStates() error = %v", err)
	}

	if len(states) != 1 {
		t.Fatalf("expected 1 state, got %d", len(states))
	}
	if states[0].IsActive {
		t.Error("IsActive = true, want false")
	}
}

func TestClose_DoubleClose(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	// Second close should be a no-op
	if err := store.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestOperationsAfterClose(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	store.Close()

	order := &models.Order{
		OrderId:  "order-x",
		Symbol:   "BTC-USDT",
		Side:     models.SideBuy,
		Price:    decimal.NewFromFloat(50000),
		Quantity: decimal.NewFromFloat(0.1),
	}

	if err := store.SaveOrder(order); err == nil {
		t.Error("expected error when saving to closed store")
	}
	if _, err := store.LoadOrders(); err == nil {
		t.Error("expected error when loading from closed store")
	}
}

func TestMultipleOrders(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	for i := 0; i < 10; i++ {
		order := &models.Order{
			OrderId:    fmt.Sprintf("order-%03d", i),
			Symbol:     "BTC-USDT",
			Side:       models.Side(i % 2),
			Price:      decimal.NewFromFloat(50000.0 + float64(i)*100),
			Quantity:   decimal.NewFromFloat(0.1),
			Status:     models.OrderStatusOpen,
			StrategyId: "grid-1",
			CreateTime: int64(1700000000000 + i),
			UpdateTime: int64(1700000000000 + i),
		}
		if err := store.SaveOrder(order); err != nil {
			t.Fatalf("SaveOrder(%d) error = %v", i, err)
		}
	}

	orders, err := store.LoadOrders()
	if err != nil {
		t.Fatalf("LoadOrders() error = %v", err)
	}
	if len(orders) != 10 {
		t.Errorf("expected 10 orders, got %d", len(orders))
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	dbPath := tempDBPath(t)

	// First session: save data
	store1, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}

	order := &models.Order{
		OrderId:    "persist-order",
		Symbol:     "SOL-USDT",
		Side:       models.SideBuy,
		Price:      decimal.NewFromFloat(100.50),
		Quantity:   decimal.NewFromFloat(5.0),
		Status:     models.OrderStatusOpen,
		StrategyId: "grid-sol",
		CreateTime: 1700000000000,
		UpdateTime: 1700000000000,
	}
	if err := store1.SaveOrder(order); err != nil {
		t.Fatalf("SaveOrder() error = %v", err)
	}

	pos := &models.Position{
		Symbol:        "SOL-USDT",
		Side:          models.SideBuy,
		Quantity:      decimal.NewFromFloat(5.0),
		AvgEntryPrice: decimal.NewFromFloat(100.50),
		UnrealizedPnL: decimal.Zero,
		RealizedPnL:   decimal.Zero,
	}
	if err := store1.SavePosition(pos); err != nil {
		t.Fatalf("SavePosition() error = %v", err)
	}

	store1.Close()

	// Second session: load data
	store2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() reopen error = %v", err)
	}
	defer store2.Close()

	orders, err := store2.LoadOrders()
	if err != nil {
		t.Fatalf("LoadOrders() error = %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("expected 1 order after restart, got %d", len(orders))
	}
	if orders[0].OrderId != "persist-order" {
		t.Errorf("OrderId = %s, want persist-order", orders[0].OrderId)
	}

	positions, err := store2.LoadPositions()
	if err != nil {
		t.Fatalf("LoadPositions() error = %v", err)
	}
	if len(positions) != 1 {
		t.Fatalf("expected 1 position after restart, got %d", len(positions))
	}
	if positions[0].Symbol != "SOL-USDT" {
		t.Errorf("Symbol = %s, want SOL-USDT", positions[0].Symbol)
	}
}
