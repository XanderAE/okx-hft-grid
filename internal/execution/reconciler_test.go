package execution

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// mockExchangeQuerier implements ExchangeQuerier for testing.
type mockExchangeQuerier struct {
	mu        sync.Mutex
	orders    map[string][]*models.Order
	positions map[string][]*models.Position
	orderErr  error
	posErr    error
}

func newMockQuerier() *mockExchangeQuerier {
	return &mockExchangeQuerier{
		orders:    make(map[string][]*models.Order),
		positions: make(map[string][]*models.Position),
	}
}

func (m *mockExchangeQuerier) QueryOrders(symbol string) ([]*models.Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.orderErr != nil {
		return nil, m.orderErr
	}
	return m.orders[symbol], nil
}

func (m *mockExchangeQuerier) QueryPositions(symbol string) ([]*models.Position, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.posErr != nil {
		return nil, m.posErr
	}
	return m.positions[symbol], nil
}

func (m *mockExchangeQuerier) setOrders(symbol string, orders []*models.Order) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orders[symbol] = orders
}

func (m *mockExchangeQuerier) setPositions(symbol string, positions []*models.Position) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.positions[symbol] = positions
}

func (m *mockExchangeQuerier) setOrderErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orderErr = err
}

func (m *mockExchangeQuerier) setPositionErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.posErr = err
}

func TestNewReconciler(t *testing.T) {
	om := NewOrderManager()
	querier := newMockQuerier()
	interval := 60 * time.Second

	r := NewReconciler(om, querier, interval)

	if r == nil {
		t.Fatal("NewReconciler returned nil")
	}
	if r.om != om {
		t.Error("OrderManager reference mismatch")
	}
	if r.interval != interval {
		t.Errorf("Expected interval %v, got %v", interval, r.interval)
	}
}

func TestReconciler_StartStop(t *testing.T) {
	om := NewOrderManager()
	querier := newMockQuerier()
	r := NewReconciler(om, querier, 50*time.Millisecond)

	if r.IsRunning() {
		t.Fatal("Reconciler should not be running before Start()")
	}

	r.Start()

	if !r.IsRunning() {
		t.Fatal("Reconciler should be running after Start()")
	}

	// Double start should be idempotent
	r.Start()

	r.Stop()

	if r.IsRunning() {
		t.Fatal("Reconciler should not be running after Stop()")
	}

	// Double stop should be safe
	r.Stop()
}

func TestReconciler_ReconcileOrderStatusDiscrepancy(t *testing.T) {
	om := NewOrderManager()
	querier := newMockQuerier()
	r := NewReconciler(om, querier, 60*time.Second)

	// Add a local order in OPEN state
	localOrder := &models.Order{
		OrderId:         "local-1",
		ExchangeOrderId: "exch-1",
		Symbol:          "BTC-USDT",
		Side:            models.SideBuy,
		OrderType:       models.OrderTypeLimit,
		Price:           decimal.NewFromFloat(50000),
		Quantity:        decimal.NewFromFloat(0.1),
		Status:          models.OrderStatusPending,
	}
	if err := om.AddOrder(localOrder); err != nil {
		t.Fatalf("Failed to add order: %v", err)
	}
	// Transition to SUBMITTED -> OPEN
	if err := om.TransitionOrder("local-1", models.OrderStatusSubmitted); err != nil {
		t.Fatalf("Failed to transition to SUBMITTED: %v", err)
	}
	if err := om.TransitionOrder("local-1", models.OrderStatusOpen); err != nil {
		t.Fatalf("Failed to transition to OPEN: %v", err)
	}

	// Exchange says the order is FILLED
	querier.setOrders("BTC-USDT", []*models.Order{
		{
			ExchangeOrderId: "exch-1",
			Symbol:          "BTC-USDT",
			Status:          models.OrderStatusFilled,
			FilledQuantity:  decimal.NewFromFloat(0.1),
			AvgFillPrice:    decimal.NewFromFloat(49950),
		},
	})
	querier.setPositions("BTC-USDT", []*models.Position{})

	// Run reconciliation
	err := r.Reconcile("BTC-USDT")
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// Local order should now be FILLED
	order, err := om.GetOrder("local-1")
	if err != nil {
		t.Fatalf("GetOrder failed: %v", err)
	}
	if order.Status != models.OrderStatusFilled {
		t.Errorf("Expected status FILLED, got %s", order.Status.String())
	}
	if !order.FilledQuantity.Equal(decimal.NewFromFloat(0.1)) {
		t.Errorf("Expected filled qty 0.1, got %s", order.FilledQuantity.String())
	}
	if !order.AvgFillPrice.Equal(decimal.NewFromFloat(49950)) {
		t.Errorf("Expected avg fill price 49950, got %s", order.AvgFillPrice.String())
	}
}

func TestReconciler_OrderNotOnExchange_MarkedCancelled(t *testing.T) {
	om := NewOrderManager()
	querier := newMockQuerier()
	r := NewReconciler(om, querier, 60*time.Second)

	// Add a local order in OPEN state
	localOrder := &models.Order{
		OrderId:         "local-2",
		ExchangeOrderId: "exch-2",
		Symbol:          "ETH-USDT",
		Side:            models.SideSell,
		OrderType:       models.OrderTypeLimit,
		Price:           decimal.NewFromFloat(3000),
		Quantity:        decimal.NewFromFloat(1),
		Status:          models.OrderStatusPending,
	}
	if err := om.AddOrder(localOrder); err != nil {
		t.Fatalf("Failed to add order: %v", err)
	}
	if err := om.TransitionOrder("local-2", models.OrderStatusSubmitted); err != nil {
		t.Fatalf("Failed to transition: %v", err)
	}
	if err := om.TransitionOrder("local-2", models.OrderStatusOpen); err != nil {
		t.Fatalf("Failed to transition: %v", err)
	}

	// Exchange returns empty orders list - order not found
	querier.setOrders("ETH-USDT", []*models.Order{})
	querier.setPositions("ETH-USDT", []*models.Position{})

	err := r.Reconcile("ETH-USDT")
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// Local order should be marked as CANCELLED
	order, err := om.GetOrder("local-2")
	if err != nil {
		t.Fatalf("GetOrder failed: %v", err)
	}
	if order.Status != models.OrderStatusCancelled {
		t.Errorf("Expected status CANCELLED, got %s", order.Status.String())
	}
}

func TestReconciler_TerminalOrderNotModified(t *testing.T) {
	om := NewOrderManager()
	querier := newMockQuerier()
	r := NewReconciler(om, querier, 60*time.Second)

	// Add a local order already in FILLED state
	localOrder := &models.Order{
		OrderId:         "local-3",
		ExchangeOrderId: "exch-3",
		Symbol:          "BTC-USDT",
		Side:            models.SideBuy,
		OrderType:       models.OrderTypeLimit,
		Price:           decimal.NewFromFloat(50000),
		Quantity:        decimal.NewFromFloat(0.1),
		FilledQuantity:  decimal.NewFromFloat(0.1),
		Status:          models.OrderStatusPending,
	}
	if err := om.AddOrder(localOrder); err != nil {
		t.Fatalf("Failed to add order: %v", err)
	}
	// Transition to FILLED
	if err := om.TransitionOrder("local-3", models.OrderStatusSubmitted); err != nil {
		t.Fatalf("Failed to transition: %v", err)
	}
	if err := om.TransitionOrder("local-3", models.OrderStatusOpen); err != nil {
		t.Fatalf("Failed to transition: %v", err)
	}
	if err := om.TransitionOrder("local-3", models.OrderStatusFilled); err != nil {
		t.Fatalf("Failed to transition: %v", err)
	}

	// Exchange returns empty orders (filled orders may not appear)
	querier.setOrders("BTC-USDT", []*models.Order{})
	querier.setPositions("BTC-USDT", []*models.Position{})

	err := r.Reconcile("BTC-USDT")
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// Should remain FILLED (terminal state not modified)
	order, err := om.GetOrder("local-3")
	if err != nil {
		t.Fatalf("GetOrder failed: %v", err)
	}
	if order.Status != models.OrderStatusFilled {
		t.Errorf("Expected status FILLED (terminal, should not change), got %s", order.Status.String())
	}
}

func TestReconciler_QueryOrdersFailure_NoLocalModification(t *testing.T) {
	om := NewOrderManager()
	querier := newMockQuerier()
	r := NewReconciler(om, querier, 60*time.Second)

	// Add a local order
	localOrder := &models.Order{
		OrderId:         "local-4",
		ExchangeOrderId: "exch-4",
		Symbol:          "BTC-USDT",
		Side:            models.SideBuy,
		OrderType:       models.OrderTypeLimit,
		Price:           decimal.NewFromFloat(50000),
		Quantity:        decimal.NewFromFloat(0.1),
		Status:          models.OrderStatusPending,
	}
	if err := om.AddOrder(localOrder); err != nil {
		t.Fatalf("Failed to add order: %v", err)
	}
	if err := om.TransitionOrder("local-4", models.OrderStatusSubmitted); err != nil {
		t.Fatalf("Failed to transition: %v", err)
	}
	if err := om.TransitionOrder("local-4", models.OrderStatusOpen); err != nil {
		t.Fatalf("Failed to transition: %v", err)
	}

	// Simulate network failure
	querier.setOrderErr(errors.New("network timeout"))

	err := r.Reconcile("BTC-USDT")
	if err == nil {
		t.Fatal("Expected error from failed reconciliation")
	}

	// Local state should NOT be modified
	order, err := om.GetOrder("local-4")
	if err != nil {
		t.Fatalf("GetOrder failed: %v", err)
	}
	if order.Status != models.OrderStatusOpen {
		t.Errorf("Local state should be unchanged on failure; expected OPEN, got %s", order.Status.String())
	}
}

func TestReconciler_QueryPositionsFailure_NoLocalModification(t *testing.T) {
	om := NewOrderManager()
	querier := newMockQuerier()
	r := NewReconciler(om, querier, 60*time.Second)

	// Add a local order
	localOrder := &models.Order{
		OrderId:         "local-5",
		ExchangeOrderId: "exch-5",
		Symbol:          "BTC-USDT",
		Side:            models.SideBuy,
		OrderType:       models.OrderTypeLimit,
		Price:           decimal.NewFromFloat(50000),
		Quantity:        decimal.NewFromFloat(0.1),
		Status:          models.OrderStatusPending,
	}
	if err := om.AddOrder(localOrder); err != nil {
		t.Fatalf("Failed to add order: %v", err)
	}
	if err := om.TransitionOrder("local-5", models.OrderStatusSubmitted); err != nil {
		t.Fatalf("Failed to transition: %v", err)
	}
	if err := om.TransitionOrder("local-5", models.OrderStatusOpen); err != nil {
		t.Fatalf("Failed to transition: %v", err)
	}

	// Orders query succeeds but positions fails
	querier.setOrders("BTC-USDT", []*models.Order{
		{
			ExchangeOrderId: "exch-5",
			Symbol:          "BTC-USDT",
			Status:          models.OrderStatusOpen,
		},
	})
	querier.setPositionErr(errors.New("API error"))

	err := r.Reconcile("BTC-USDT")
	if err == nil {
		t.Fatal("Expected error from failed position query")
	}

	// Local state should NOT be modified (early return on position failure)
	order, err := om.GetOrder("local-5")
	if err != nil {
		t.Fatalf("GetOrder failed: %v", err)
	}
	if order.Status != models.OrderStatusOpen {
		t.Errorf("Expected OPEN (no modification on failure), got %s", order.Status.String())
	}
}

func TestReconciler_NoDiscrepancy_NoChange(t *testing.T) {
	om := NewOrderManager()
	querier := newMockQuerier()
	r := NewReconciler(om, querier, 60*time.Second)

	// Add a local order in OPEN state
	localOrder := &models.Order{
		OrderId:         "local-6",
		ExchangeOrderId: "exch-6",
		Symbol:          "BTC-USDT",
		Side:            models.SideBuy,
		OrderType:       models.OrderTypeLimit,
		Price:           decimal.NewFromFloat(50000),
		Quantity:        decimal.NewFromFloat(0.1),
		Status:          models.OrderStatusPending,
	}
	if err := om.AddOrder(localOrder); err != nil {
		t.Fatalf("Failed to add order: %v", err)
	}
	if err := om.TransitionOrder("local-6", models.OrderStatusSubmitted); err != nil {
		t.Fatalf("Failed to transition: %v", err)
	}
	if err := om.TransitionOrder("local-6", models.OrderStatusOpen); err != nil {
		t.Fatalf("Failed to transition: %v", err)
	}

	// Exchange reports same state
	querier.setOrders("BTC-USDT", []*models.Order{
		{
			ExchangeOrderId: "exch-6",
			Symbol:          "BTC-USDT",
			Status:          models.OrderStatusOpen,
		},
	})
	querier.setPositions("BTC-USDT", []*models.Position{})

	originalUpdateTime := localOrder.UpdateTime

	err := r.Reconcile("BTC-USDT")
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// No discrepancy - UpdateTime should not change
	order, err := om.GetOrder("local-6")
	if err != nil {
		t.Fatalf("GetOrder failed: %v", err)
	}
	if order.Status != models.OrderStatusOpen {
		t.Errorf("Expected status OPEN, got %s", order.Status.String())
	}
	if order.UpdateTime != originalUpdateTime {
		t.Error("UpdateTime should not change when there's no discrepancy")
	}
}

func TestReconciler_OrderWithoutExchangeID_Skipped(t *testing.T) {
	om := NewOrderManager()
	querier := newMockQuerier()
	r := NewReconciler(om, querier, 60*time.Second)

	// Add a local order without exchange ID (not yet submitted to exchange)
	localOrder := &models.Order{
		OrderId:         "local-7",
		ExchangeOrderId: "", // No exchange ID yet
		Symbol:          "BTC-USDT",
		Side:            models.SideBuy,
		OrderType:       models.OrderTypeLimit,
		Price:           decimal.NewFromFloat(50000),
		Quantity:        decimal.NewFromFloat(0.1),
		Status:          models.OrderStatusPending,
	}
	if err := om.AddOrder(localOrder); err != nil {
		t.Fatalf("Failed to add order: %v", err)
	}

	// Exchange returns empty
	querier.setOrders("BTC-USDT", []*models.Order{})
	querier.setPositions("BTC-USDT", []*models.Position{})

	err := r.Reconcile("BTC-USDT")
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// Order should remain PENDING (skipped since no exchange ID)
	order, err := om.GetOrder("local-7")
	if err != nil {
		t.Fatalf("GetOrder failed: %v", err)
	}
	if order.Status != models.OrderStatusPending {
		t.Errorf("Expected PENDING (skipped), got %s", order.Status.String())
	}
}

func TestReconciler_PeriodicReconciliation(t *testing.T) {
	om := NewOrderManager()
	querier := newMockQuerier()
	// Use a very short interval for testing
	r := NewReconciler(om, querier, 30*time.Millisecond)
	r.SetSymbols([]string{"BTC-USDT"})

	// Add a local order
	localOrder := &models.Order{
		OrderId:         "local-8",
		ExchangeOrderId: "exch-8",
		Symbol:          "BTC-USDT",
		Side:            models.SideBuy,
		OrderType:       models.OrderTypeLimit,
		Price:           decimal.NewFromFloat(50000),
		Quantity:        decimal.NewFromFloat(0.1),
		Status:          models.OrderStatusPending,
	}
	if err := om.AddOrder(localOrder); err != nil {
		t.Fatalf("Failed to add order: %v", err)
	}
	if err := om.TransitionOrder("local-8", models.OrderStatusSubmitted); err != nil {
		t.Fatalf("Failed to transition: %v", err)
	}
	if err := om.TransitionOrder("local-8", models.OrderStatusOpen); err != nil {
		t.Fatalf("Failed to transition: %v", err)
	}

	// Exchange reports FILLED
	querier.setOrders("BTC-USDT", []*models.Order{
		{
			ExchangeOrderId: "exch-8",
			Symbol:          "BTC-USDT",
			Status:          models.OrderStatusFilled,
			FilledQuantity:  decimal.NewFromFloat(0.1),
		},
	})
	querier.setPositions("BTC-USDT", []*models.Position{})

	r.Start()
	// Wait long enough for at least one tick
	time.Sleep(100 * time.Millisecond)
	r.Stop()

	// After periodic reconciliation, local order should be FILLED
	order, err := om.GetOrder("local-8")
	if err != nil {
		t.Fatalf("GetOrder failed: %v", err)
	}
	if order.Status != models.OrderStatusFilled {
		t.Errorf("Expected FILLED after periodic reconciliation, got %s", order.Status.String())
	}
}

func TestReconciler_MultipleSymbols(t *testing.T) {
	om := NewOrderManager()
	querier := newMockQuerier()
	r := NewReconciler(om, querier, 60*time.Second)

	// Add orders for two different symbols
	orderBTC := &models.Order{
		OrderId:         "local-btc",
		ExchangeOrderId: "exch-btc",
		Symbol:          "BTC-USDT",
		Side:            models.SideBuy,
		OrderType:       models.OrderTypeLimit,
		Price:           decimal.NewFromFloat(50000),
		Quantity:        decimal.NewFromFloat(0.1),
		Status:          models.OrderStatusPending,
	}
	orderETH := &models.Order{
		OrderId:         "local-eth",
		ExchangeOrderId: "exch-eth",
		Symbol:          "ETH-USDT",
		Side:            models.SideSell,
		OrderType:       models.OrderTypeLimit,
		Price:           decimal.NewFromFloat(3000),
		Quantity:        decimal.NewFromFloat(1),
		Status:          models.OrderStatusPending,
	}

	om.AddOrder(orderBTC)
	om.TransitionOrder("local-btc", models.OrderStatusSubmitted)
	om.TransitionOrder("local-btc", models.OrderStatusOpen)

	om.AddOrder(orderETH)
	om.TransitionOrder("local-eth", models.OrderStatusSubmitted)
	om.TransitionOrder("local-eth", models.OrderStatusOpen)

	// BTC order is FILLED on exchange, ETH order is still OPEN
	querier.setOrders("BTC-USDT", []*models.Order{
		{ExchangeOrderId: "exch-btc", Symbol: "BTC-USDT", Status: models.OrderStatusFilled, FilledQuantity: decimal.NewFromFloat(0.1)},
	})
	querier.setOrders("ETH-USDT", []*models.Order{
		{ExchangeOrderId: "exch-eth", Symbol: "ETH-USDT", Status: models.OrderStatusOpen},
	})
	querier.setPositions("BTC-USDT", []*models.Position{})
	querier.setPositions("ETH-USDT", []*models.Position{})

	// Reconcile BTC
	if err := r.Reconcile("BTC-USDT"); err != nil {
		t.Fatalf("BTC reconciliation failed: %v", err)
	}
	// Reconcile ETH
	if err := r.Reconcile("ETH-USDT"); err != nil {
		t.Fatalf("ETH reconciliation failed: %v", err)
	}

	btcOrder, _ := om.GetOrder("local-btc")
	ethOrder, _ := om.GetOrder("local-eth")

	if btcOrder.Status != models.OrderStatusFilled {
		t.Errorf("BTC order: expected FILLED, got %s", btcOrder.Status.String())
	}
	if ethOrder.Status != models.OrderStatusOpen {
		t.Errorf("ETH order: expected OPEN (no discrepancy), got %s", ethOrder.Status.String())
	}
}

func TestReconciler_PartialFillUpdate(t *testing.T) {
	om := NewOrderManager()
	querier := newMockQuerier()
	r := NewReconciler(om, querier, 60*time.Second)

	// Add a local order in OPEN state with no fills
	localOrder := &models.Order{
		OrderId:         "local-9",
		ExchangeOrderId: "exch-9",
		Symbol:          "BTC-USDT",
		Side:            models.SideBuy,
		OrderType:       models.OrderTypeLimit,
		Price:           decimal.NewFromFloat(50000),
		Quantity:        decimal.NewFromFloat(1),
		FilledQuantity:  decimal.Zero,
		Status:          models.OrderStatusPending,
	}
	if err := om.AddOrder(localOrder); err != nil {
		t.Fatalf("Failed to add order: %v", err)
	}
	om.TransitionOrder("local-9", models.OrderStatusSubmitted)
	om.TransitionOrder("local-9", models.OrderStatusOpen)

	// Exchange says partially filled
	querier.setOrders("BTC-USDT", []*models.Order{
		{
			ExchangeOrderId: "exch-9",
			Symbol:          "BTC-USDT",
			Status:          models.OrderStatusPartiallyFilled,
			FilledQuantity:  decimal.NewFromFloat(0.5),
			AvgFillPrice:    decimal.NewFromFloat(49900),
		},
	})
	querier.setPositions("BTC-USDT", []*models.Position{})

	err := r.Reconcile("BTC-USDT")
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	order, _ := om.GetOrder("local-9")
	if order.Status != models.OrderStatusPartiallyFilled {
		t.Errorf("Expected PARTIALLY_FILLED, got %s", order.Status.String())
	}
	if !order.FilledQuantity.Equal(decimal.NewFromFloat(0.5)) {
		t.Errorf("Expected filled qty 0.5, got %s", order.FilledQuantity.String())
	}
	if !order.AvgFillPrice.Equal(decimal.NewFromFloat(49900)) {
		t.Errorf("Expected avg fill price 49900, got %s", order.AvgFillPrice.String())
	}
}

func TestReconciler_SetSymbols(t *testing.T) {
	om := NewOrderManager()
	querier := newMockQuerier()
	r := NewReconciler(om, querier, 60*time.Second)

	symbols := []string{"BTC-USDT", "ETH-USDT", "SOL-USDT"}
	r.SetSymbols(symbols)

	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.symbols) != 3 {
		t.Errorf("Expected 3 symbols, got %d", len(r.symbols))
	}
}

func TestReconciler_DifferentSymbolsNotAffected(t *testing.T) {
	om := NewOrderManager()
	querier := newMockQuerier()
	r := NewReconciler(om, querier, 60*time.Second)

	// Add an ETH order
	ethOrder := &models.Order{
		OrderId:         "local-eth-only",
		ExchangeOrderId: "exch-eth-only",
		Symbol:          "ETH-USDT",
		Side:            models.SideBuy,
		OrderType:       models.OrderTypeLimit,
		Price:           decimal.NewFromFloat(3000),
		Quantity:        decimal.NewFromFloat(1),
		Status:          models.OrderStatusPending,
	}
	om.AddOrder(ethOrder)
	om.TransitionOrder("local-eth-only", models.OrderStatusSubmitted)
	om.TransitionOrder("local-eth-only", models.OrderStatusOpen)

	// Reconcile BTC only - ETH order should NOT be affected
	querier.setOrders("BTC-USDT", []*models.Order{})
	querier.setPositions("BTC-USDT", []*models.Position{})

	err := r.Reconcile("BTC-USDT")
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	// ETH order should remain unchanged
	order, _ := om.GetOrder("local-eth-only")
	if order.Status != models.OrderStatusOpen {
		t.Errorf("ETH order should not be affected by BTC reconciliation; got %s", order.Status.String())
	}
}
