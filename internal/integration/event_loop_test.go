package integration

import (
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/internal/execution"
	"github.com/yourname/okx-hft-grid/internal/marketdata"
	"github.com/yourname/okx-hft-grid/internal/orderbook"
	"github.com/yourname/okx-hft-grid/internal/risk"
	"github.com/yourname/okx-hft-grid/internal/strategy"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// --- Mock implementations for testing ---

// mockOrderBook implements orderbook.OrderBookManager.
type mockOrderBook struct {
	mu        sync.Mutex
	snapshots map[string]*orderbook.OrderBookSnapshot
}

func newMockOrderBook() *mockOrderBook {
	return &mockOrderBook{
		snapshots: make(map[string]*orderbook.OrderBookSnapshot),
	}
}

func (m *mockOrderBook) UpdateFromSnapshot(symbol string, snapshot *orderbook.OrderBookSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshots[symbol] = snapshot
	return nil
}

func (m *mockOrderBook) UpdateIncremental(_ string, _ *orderbook.OrderBookDelta) error {
	return nil
}

func (m *mockOrderBook) GetBestBid(symbol string) (*orderbook.PriceLevel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.snapshots[symbol]
	if !ok || len(s.Bids) == 0 {
		return nil, nil
	}
	return &s.Bids[0], nil
}

func (m *mockOrderBook) GetBestAsk(symbol string) (*orderbook.PriceLevel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.snapshots[symbol]
	if !ok || len(s.Asks) == 0 {
		return nil, nil
	}
	return &s.Asks[0], nil
}

func (m *mockOrderBook) GetMidPrice(_ string) (decimal.Decimal, error) {
	return decimal.NewFromFloat(100.0), nil
}

func (m *mockOrderBook) GetSpread(_ string) (decimal.Decimal, error) {
	return decimal.NewFromFloat(0.01), nil
}

func (m *mockOrderBook) GetVWAP(_ string, _ models.Side, _ decimal.Decimal) (decimal.Decimal, error) {
	return decimal.NewFromFloat(100.0), nil
}

func (m *mockOrderBook) GetDepth(_ string, _ models.Side, _ int) ([]orderbook.PriceLevel, error) {
	return nil, nil
}

func (m *mockOrderBook) RequestResync(_ string) error {
	return nil
}

// mockStrategyEngine implements strategy.StrategyEngine.
type mockStrategyEngine struct {
	mu            sync.Mutex
	marketUpdates int
	fills         int
}

func newMockStrategyEngine() *mockStrategyEngine {
	return &mockStrategyEngine{}
}

func (m *mockStrategyEngine) LoadStrategy(_ strategy.StrategyConfig) error { return nil }
func (m *mockStrategyEngine) StartStrategy(_ string) error                 { return nil }
func (m *mockStrategyEngine) StopStrategy(_ string) error                  { return nil }

func (m *mockStrategyEngine) OnMarketUpdate(_ string, _ *models.TickData) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marketUpdates++
}

func (m *mockStrategyEngine) OnOrderFill(_ models.FillEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fills++
}

func (m *mockStrategyEngine) GetActiveStrategies() []strategy.StrategyStatus {
	return nil
}

func (m *mockStrategyEngine) GetStrategyPnL(_ string) (*strategy.PnLReport, error) {
	return nil, nil
}

func (m *mockStrategyEngine) StopAll() {}

func (m *mockStrategyEngine) MarketUpdateCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.marketUpdates
}

func (m *mockStrategyEngine) FillCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fills
}

// mockRiskManager implements risk.RiskManager.
type mockRiskManager struct {
	mu               sync.Mutex
	positionUpdates  int
	pnlUpdates       int
	emergencyStopped bool
}

func newMockRiskManager() *mockRiskManager {
	return &mockRiskManager{}
}

func (m *mockRiskManager) CheckOrder(_ *risk.OrderRequest) *risk.RiskDecision {
	return &risk.RiskDecision{Approved: true}
}

func (m *mockRiskManager) CheckBatchOrders(_ []*risk.OrderRequest) *risk.RiskDecision {
	return &risk.RiskDecision{Approved: true}
}

func (m *mockRiskManager) UpdatePosition(_ string, _ *models.Position) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.positionUpdates++
}

func (m *mockRiskManager) UpdatePnL(_ string, _ decimal.Decimal) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pnlUpdates++
}

func (m *mockRiskManager) GetRiskMetrics() *risk.RiskMetrics {
	return &risk.RiskMetrics{}
}

func (m *mockRiskManager) SetRiskLimits(_ *models.RiskLimits) {}

func (m *mockRiskManager) EmergencyStop(_ string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emergencyStopped = true
}

func (m *mockRiskManager) ResumeFromEmergencyStop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.emergencyStopped = false
	return nil
}

func (m *mockRiskManager) IsEmergencyStopActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emergencyStopped
}

func (m *mockRiskManager) PositionUpdateCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.positionUpdates
}

// mockExecutionEngine implements execution.OrderExecutionEngine.
type mockExecutionEngine struct {
	mu           sync.Mutex
	ordersPlaced int
}

func newMockExecutionEngine() *mockExecutionEngine {
	return &mockExecutionEngine{}
}

func (m *mockExecutionEngine) PlaceOrder(_ *execution.OrderRequest) (*execution.OrderResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ordersPlaced++
	return &execution.OrderResult{
		Success: true,
		OrderID: "test-order-id",
		Status:  models.OrderStatusSubmitted,
	}, nil
}

func (m *mockExecutionEngine) CancelOrder(_ string) (*execution.CancelResult, error) {
	return &execution.CancelResult{Success: true}, nil
}

func (m *mockExecutionEngine) BatchPlaceOrders(orders []*execution.OrderRequest) ([]*execution.OrderResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	results := make([]*execution.OrderResult, len(orders))
	for i := range orders {
		m.ordersPlaced++
		results[i] = &execution.OrderResult{Success: true, OrderID: "batch-order"}
	}
	return results, nil
}

func (m *mockExecutionEngine) BatchCancelOrders(orderIDs []string) ([]*execution.CancelResult, error) {
	results := make([]*execution.CancelResult, len(orderIDs))
	for i := range orderIDs {
		results[i] = &execution.CancelResult{Success: true}
	}
	return results, nil
}

func (m *mockExecutionEngine) GetOpenOrders(_ string) ([]*models.Order, error) {
	return nil, nil
}

func (m *mockExecutionEngine) GetOrderStatus(_ string) (models.OrderStatus, error) {
	return models.OrderStatusOpen, nil
}

func (m *mockExecutionEngine) OnOrderUpdate(_ models.OrderUpdateEvent) {}

func (m *mockExecutionEngine) GetPosition(_ string) (*models.Position, error) {
	return nil, nil
}

func (m *mockExecutionEngine) GetAllOpenOrders() ([]*models.Order, error) {
	return nil, nil
}

func (m *mockExecutionEngine) OrdersPlacedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ordersPlaced
}

// --- Tests ---

func TestEventLoop_StartStop(t *testing.T) {
	dispatcher := marketdata.NewDispatcher(marketdata.DefaultDispatcherConfig())
	dispatcher.Start()
	defer dispatcher.Stop()

	deps := EventLoopDeps{
		OrderBook:       newMockOrderBook(),
		StrategyEngine:  newMockStrategyEngine(),
		RiskManager:     newMockRiskManager(),
		ExecutionEngine: newMockExecutionEngine(),
		Dispatcher:      dispatcher,
	}

	el := NewEventLoop(deps, DefaultEventLoopConfig())

	// Start should succeed.
	if err := el.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}

	if !el.IsRunning() {
		t.Fatal("IsRunning() should return true after Start()")
	}

	// Stop should be clean.
	el.Stop()

	if el.IsRunning() {
		t.Fatal("IsRunning() should return false after Stop()")
	}

	// Double stop should be safe.
	el.Stop()
}

func TestEventLoop_StartMissingDeps(t *testing.T) {
	dispatcher := marketdata.NewDispatcher(marketdata.DefaultDispatcherConfig())
	dispatcher.Start()
	defer dispatcher.Stop()

	// Missing OrderBook
	deps := EventLoopDeps{
		StrategyEngine:  newMockStrategyEngine(),
		RiskManager:     newMockRiskManager(),
		ExecutionEngine: newMockExecutionEngine(),
		Dispatcher:      dispatcher,
	}
	el := NewEventLoop(deps, DefaultEventLoopConfig())
	if err := el.Start(); err == nil {
		t.Fatal("Start() should fail when OrderBook is nil")
	}

	// Missing Dispatcher
	deps2 := EventLoopDeps{
		OrderBook:       newMockOrderBook(),
		StrategyEngine:  newMockStrategyEngine(),
		RiskManager:     newMockRiskManager(),
		ExecutionEngine: newMockExecutionEngine(),
	}
	el2 := NewEventLoop(deps2, DefaultEventLoopConfig())
	if err := el2.Start(); err == nil {
		t.Fatal("Start() should fail when Dispatcher is nil")
	}
}

func TestEventLoop_MarketDataFlow(t *testing.T) {
	dispatcher := marketdata.NewDispatcher(marketdata.DefaultDispatcherConfig())
	dispatcher.Start()
	defer dispatcher.Stop()

	stratEngine := newMockStrategyEngine()
	ob := newMockOrderBook()

	deps := EventLoopDeps{
		OrderBook:       ob,
		StrategyEngine:  stratEngine,
		RiskManager:     newMockRiskManager(),
		ExecutionEngine: newMockExecutionEngine(),
		Dispatcher:      dispatcher,
	}

	el := NewEventLoop(deps, DefaultEventLoopConfig())
	if err := el.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	defer el.Stop()

	// Dispatch a market event through the dispatcher.
	event := models.MarketEvent{
		Symbol:    "BTC-USDT",
		Timestamp: time.Now(),
		LastPrice: decimal.NewFromFloat(50000.0),
		BestBid:   decimal.NewFromFloat(49999.0),
		BestAsk:   decimal.NewFromFloat(50001.0),
		BidSize:   decimal.NewFromFloat(1.5),
		AskSize:   decimal.NewFromFloat(2.0),
		Volume24h: decimal.NewFromFloat(10000.0),
		SeqID:     1,
	}

	dispatcher.DispatchMarketEvent(models.EventMarketData, event)

	// Wait for processing.
	time.Sleep(50 * time.Millisecond)

	if stratEngine.MarketUpdateCount() < 1 {
		t.Errorf("Expected at least 1 market update on strategy engine, got %d", stratEngine.MarketUpdateCount())
	}

	if el.EventsProcessed() < 1 {
		t.Errorf("Expected at least 1 event processed, got %d", el.EventsProcessed())
	}
}

func TestEventLoop_FillFlow(t *testing.T) {
	dispatcher := marketdata.NewDispatcher(marketdata.DefaultDispatcherConfig())
	dispatcher.Start()
	defer dispatcher.Stop()

	stratEngine := newMockStrategyEngine()
	riskMgr := newMockRiskManager()

	deps := EventLoopDeps{
		OrderBook:       newMockOrderBook(),
		StrategyEngine:  stratEngine,
		RiskManager:     riskMgr,
		ExecutionEngine: newMockExecutionEngine(),
		Dispatcher:      dispatcher,
	}

	el := NewEventLoop(deps, DefaultEventLoopConfig())
	if err := el.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	defer el.Stop()

	// Dispatch a fill event through the dispatcher.
	fill := models.FillEvent{
		OrderID:    "order-1",
		Symbol:     "ETH-USDT",
		Side:       models.SideBuy,
		Price:      decimal.NewFromFloat(3000.0),
		Quantity:   decimal.NewFromFloat(1.0),
		Fee:        decimal.NewFromFloat(0.3),
		Timestamp:  time.Now(),
		StrategyID: "grid-1",
	}

	dispatcher.DispatchFillEvent(fill)

	// Wait for processing.
	time.Sleep(50 * time.Millisecond)

	if stratEngine.FillCount() < 1 {
		t.Errorf("Expected at least 1 fill on strategy engine, got %d", stratEngine.FillCount())
	}

	if riskMgr.PositionUpdateCount() < 1 {
		t.Errorf("Expected at least 1 position update on risk manager, got %d", riskMgr.PositionUpdateCount())
	}
}

func TestEventLoop_OrderSignalFlow(t *testing.T) {
	dispatcher := marketdata.NewDispatcher(marketdata.DefaultDispatcherConfig())
	dispatcher.Start()
	defer dispatcher.Stop()

	execEngine := newMockExecutionEngine()

	deps := EventLoopDeps{
		OrderBook:       newMockOrderBook(),
		StrategyEngine:  newMockStrategyEngine(),
		RiskManager:     newMockRiskManager(),
		ExecutionEngine: execEngine,
		Dispatcher:      dispatcher,
	}

	el := NewEventLoop(deps, DefaultEventLoopConfig())
	if err := el.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	defer el.Stop()

	// Submit an order signal.
	order := &execution.OrderRequest{
		Symbol:     "DOGE-USDT",
		Side:       models.SideBuy,
		OrderType:  models.OrderTypePostOnly,
		Price:      decimal.NewFromFloat(0.08),
		Quantity:   decimal.NewFromFloat(1000),
		StrategyID: "grid-1",
	}

	el.SubmitOrderSignal(order)

	// Wait for processing.
	time.Sleep(50 * time.Millisecond)

	if execEngine.OrdersPlacedCount() < 1 {
		t.Errorf("Expected at least 1 order placed, got %d", execEngine.OrdersPlacedCount())
	}
}

func TestEventLoop_RiskRejection(t *testing.T) {
	dispatcher := marketdata.NewDispatcher(marketdata.DefaultDispatcherConfig())
	dispatcher.Start()
	defer dispatcher.Stop()

	execEngine := newMockExecutionEngine()
	// Use a risk manager that rejects all orders.
	rejectingRM := &rejectingRiskManager{}

	deps := EventLoopDeps{
		OrderBook:       newMockOrderBook(),
		StrategyEngine:  newMockStrategyEngine(),
		RiskManager:     rejectingRM,
		ExecutionEngine: execEngine,
		Dispatcher:      dispatcher,
	}

	el := NewEventLoop(deps, DefaultEventLoopConfig())
	if err := el.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	defer el.Stop()

	order := &execution.OrderRequest{
		Symbol:     "SOL-USDT",
		Side:       models.SideBuy,
		OrderType:  models.OrderTypeLimit,
		Price:      decimal.NewFromFloat(150.0),
		Quantity:   decimal.NewFromFloat(10),
		StrategyID: "mr-1",
	}

	el.SubmitOrderSignal(order)

	// Wait for processing.
	time.Sleep(50 * time.Millisecond)

	if execEngine.OrdersPlacedCount() != 0 {
		t.Errorf("Expected 0 orders placed (risk rejection), got %d", execEngine.OrdersPlacedCount())
	}
}

func TestEventLoop_DefaultConfig(t *testing.T) {
	cfg := DefaultEventLoopConfig()
	if cfg.MarketDataBufferSize != DefaultMarketDataBufferSize {
		t.Errorf("Expected market data buffer size %d, got %d", DefaultMarketDataBufferSize, cfg.MarketDataBufferSize)
	}
	if cfg.OrderBufferSize != DefaultOrderBufferSize {
		t.Errorf("Expected order buffer size %d, got %d", DefaultOrderBufferSize, cfg.OrderBufferSize)
	}
	if cfg.MaxActiveSymbols != 100 {
		t.Errorf("Expected max active symbols 100, got %d", cfg.MaxActiveSymbols)
	}
}

// --- Helper mock that rejects all orders ---

type rejectingRiskManager struct{}

func (r *rejectingRiskManager) CheckOrder(_ *risk.OrderRequest) *risk.RiskDecision {
	return &risk.RiskDecision{Approved: false, Reasons: []string{"position limit exceeded"}}
}
func (r *rejectingRiskManager) CheckBatchOrders(_ []*risk.OrderRequest) *risk.RiskDecision {
	return &risk.RiskDecision{Approved: false, Reasons: []string{"position limit exceeded"}}
}
func (r *rejectingRiskManager) UpdatePosition(_ string, _ *models.Position) {}
func (r *rejectingRiskManager) UpdatePnL(_ string, _ decimal.Decimal)       {}
func (r *rejectingRiskManager) GetRiskMetrics() *risk.RiskMetrics           { return &risk.RiskMetrics{} }
func (r *rejectingRiskManager) SetRiskLimits(_ *models.RiskLimits)          {}
func (r *rejectingRiskManager) EmergencyStop(_ string)                      {}
func (r *rejectingRiskManager) ResumeFromEmergencyStop() error              { return nil }
func (r *rejectingRiskManager) IsEmergencyStopActive() bool                 { return false }
