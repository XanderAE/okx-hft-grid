// Package integration wires all core components together into a single event loop
// that drives the end-to-end trading cycle:
// MarketData → OrderBook → StrategyEngine → RiskManager → OrderExecution.
package integration

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yourname/okx-hft-grid/internal/execution"
	"github.com/yourname/okx-hft-grid/internal/marketdata"
	"github.com/yourname/okx-hft-grid/internal/orderbook"
	"github.com/yourname/okx-hft-grid/internal/risk"
	"github.com/yourname/okx-hft-grid/internal/strategy"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// DefaultMarketDataBufferSize is the default channel buffer for market data events.
const DefaultMarketDataBufferSize = 4096

// DefaultOrderBufferSize is the default channel buffer for order/fill events.
const DefaultOrderBufferSize = 1024

// LatencyThreshold is the p99 latency target for end-to-end processing (2ms).
const LatencyThreshold = 2 * time.Millisecond

// EventLoopConfig holds configurable parameters for the event loop.
type EventLoopConfig struct {
	// MarketDataBufferSize is the channel buffer for market data events.
	// Default: 4096.
	MarketDataBufferSize int

	// OrderBufferSize is the channel buffer for order/fill events.
	// Default: 1024.
	OrderBufferSize int

	// MaxActiveSymbols is the maximum number of trading pairs supported concurrently.
	// Default: 100.
	MaxActiveSymbols int

	// Symbols is the list of active trading pairs.
	Symbols []string
}

// DefaultEventLoopConfig returns an EventLoopConfig with sensible defaults.
func DefaultEventLoopConfig() EventLoopConfig {
	return EventLoopConfig{
		MarketDataBufferSize: DefaultMarketDataBufferSize,
		OrderBufferSize:      DefaultOrderBufferSize,
		MaxActiveSymbols:     100,
	}
}

// EventLoopDeps holds references to all components that the EventLoop depends on.
type EventLoopDeps struct {
	// WSClient handles real-time market data from OKX WebSocket.
	WSClient *marketdata.WSClient

	// OrderBook manages the local order book for each symbol.
	OrderBook orderbook.OrderBookManager

	// StrategyEngine schedules and routes events to trading strategies.
	StrategyEngine strategy.StrategyEngine

	// RiskManager evaluates orders against risk limits.
	RiskManager risk.RiskManager

	// ExecutionEngine manages order placement and lifecycle.
	ExecutionEngine execution.OrderExecutionEngine

	// Dispatcher distributes events to registered handlers.
	Dispatcher *marketdata.Dispatcher

	// Logger is an optional structured logger. If nil, the standard log package is used.
	Logger *log.Logger
}

// EventLoop wires all components together and drives the main trading loop.
// It uses goroutines communicating via channels to achieve low-latency event processing.
type EventLoop struct {
	deps   EventLoopDeps
	config EventLoopConfig
	logger *log.Logger

	// Internal channels
	marketDataCh  chan models.MarketEvent
	fillCh        chan models.FillEvent
	orderSignalCh chan orderSignal

	// Lifecycle
	stopCh  chan struct{}
	stopped atomic.Bool
	wg      sync.WaitGroup

	// Metrics
	latencyViolations atomic.Int64
	eventsProcessed   atomic.Int64
}

// orderSignal carries a strategy-generated order through the risk check pipeline.
type orderSignal struct {
	Request    *execution.OrderRequest
	ReceivedAt time.Time // timestamp when the originating market data was received
}

// NewEventLoop creates a new EventLoop with the given dependencies and configuration.
func NewEventLoop(deps EventLoopDeps, config EventLoopConfig) *EventLoop {
	if config.MarketDataBufferSize <= 0 {
		config.MarketDataBufferSize = DefaultMarketDataBufferSize
	}
	if config.OrderBufferSize <= 0 {
		config.OrderBufferSize = DefaultOrderBufferSize
	}
	if config.MaxActiveSymbols <= 0 {
		config.MaxActiveSymbols = 100
	}

	logger := deps.Logger
	if logger == nil {
		logger = log.Default()
	}

	return &EventLoop{
		deps:          deps,
		config:        config,
		logger:        logger,
		marketDataCh:  make(chan models.MarketEvent, config.MarketDataBufferSize),
		fillCh:        make(chan models.FillEvent, config.OrderBufferSize),
		orderSignalCh: make(chan orderSignal, config.OrderBufferSize),
		stopCh:        make(chan struct{}),
	}
}

// Start launches the event loop goroutines and begins processing.
// It registers handlers on the Dispatcher and starts the market data, strategy,
// and execution pipelines.
func (el *EventLoop) Start() error {
	if el.stopped.Load() {
		return fmt.Errorf("event loop has been stopped and cannot be restarted")
	}

	// Validate that all required dependencies are present.
	if el.deps.OrderBook == nil {
		return fmt.Errorf("OrderBook dependency is nil")
	}
	if el.deps.StrategyEngine == nil {
		return fmt.Errorf("StrategyEngine dependency is nil")
	}
	if el.deps.RiskManager == nil {
		return fmt.Errorf("RiskManager dependency is nil")
	}
	if el.deps.ExecutionEngine == nil {
		return fmt.Errorf("ExecutionEngine dependency is nil")
	}
	if el.deps.Dispatcher == nil {
		return fmt.Errorf("Dispatcher dependency is nil")
	}

	// Register market data handler on the dispatcher.
	el.deps.Dispatcher.RegisterMarketHandler(models.EventMarketData, el.onMarketData)

	// Register fill handler on the dispatcher.
	el.deps.Dispatcher.RegisterFillHandler(el.onFill)

	// Start processing goroutines.
	el.wg.Add(3)
	go el.processMarketData()
	go el.processOrderSignals()
	go el.processFills()

	el.logger.Printf("[EventLoop] Started with buffer sizes: marketData=%d, orders=%d, maxSymbols=%d",
		el.config.MarketDataBufferSize, el.config.OrderBufferSize, el.config.MaxActiveSymbols)

	return nil
}

// Stop gracefully shuts down the event loop and waits for all goroutines to finish.
func (el *EventLoop) Stop() {
	if el.stopped.Swap(true) {
		return // already stopped
	}
	close(el.stopCh)
	el.wg.Wait()
	el.logger.Printf("[EventLoop] Stopped. Events processed: %d, latency violations: %d",
		el.eventsProcessed.Load(), el.latencyViolations.Load())
}

// IsRunning returns whether the event loop is currently active.
func (el *EventLoop) IsRunning() bool {
	return !el.stopped.Load()
}

// LatencyViolations returns the number of events that exceeded the 2ms latency threshold.
func (el *EventLoop) LatencyViolations() int64 {
	return el.latencyViolations.Load()
}

// EventsProcessed returns the total number of market events processed.
func (el *EventLoop) EventsProcessed() int64 {
	return el.eventsProcessed.Load()
}

// --- Event handlers (called by Dispatcher) ---

// onMarketData is the callback registered with the Dispatcher for market data events.
// It forwards events into the internal marketDataCh with non-blocking semantics.
func (el *EventLoop) onMarketData(event models.MarketEvent) {
	select {
	case el.marketDataCh <- event:
	default:
		// Channel full – drop to keep the hot path non-blocking.
		el.logger.Printf("[EventLoop] WARNING: marketDataCh full, dropping event for %s", event.Symbol)
	}
}

// onFill is the callback registered with the Dispatcher for fill events.
func (el *EventLoop) onFill(event models.FillEvent) {
	select {
	case el.fillCh <- event:
	default:
		el.logger.Printf("[EventLoop] WARNING: fillCh full, dropping fill for order %s", event.OrderID)
	}
}

// --- Processing goroutines ---

// processMarketData reads market events and drives the main pipeline:
// Market data → Update OrderBook → Notify Strategy Engine → Emit order signals.
func (el *EventLoop) processMarketData() {
	defer el.wg.Done()
	for {
		select {
		case <-el.stopCh:
			return
		case event := <-el.marketDataCh:
			receiveTime := time.Now()
			el.handleMarketEvent(event, receiveTime)
			el.eventsProcessed.Add(1)
		}
	}
}

// handleMarketEvent processes a single market event through the pipeline.
func (el *EventLoop) handleMarketEvent(event models.MarketEvent, receiveTime time.Time) {
	symbol := event.Symbol

	// Step 1: Update order book with the latest tick data.
	// Convert the market event into an order book delta/snapshot update.
	// For simplicity, we update the order book with the best bid/ask from the tick.
	el.updateOrderBookFromTick(symbol, event)

	// Step 2: Notify the strategy engine of the market update.
	tick := &models.TickData{
		Symbol:     symbol,
		Timestamp:  event.Timestamp.UnixMicro(),
		LastPrice:  event.LastPrice,
		BestBid:    event.BestBid,
		BestAsk:    event.BestAsk,
		BidSize:    event.BidSize,
		AskSize:    event.AskSize,
		Volume24h:  event.Volume24h,
		SequenceId: event.SeqID,
	}
	el.deps.StrategyEngine.OnMarketUpdate(symbol, tick)

	// Step 3: Measure end-to-end latency from data receipt to post-strategy processing.
	elapsed := time.Since(receiveTime)
	if elapsed > LatencyThreshold {
		el.latencyViolations.Add(1)
		el.logger.Printf("[EventLoop] LATENCY VIOLATION: %s processing took %v (threshold: %v)",
			symbol, elapsed, LatencyThreshold)
	}
}

// updateOrderBookFromTick updates the local order book with tick data.
func (el *EventLoop) updateOrderBookFromTick(symbol string, event models.MarketEvent) {
	if event.BestBid.IsZero() || event.BestAsk.IsZero() {
		return
	}

	// Build a minimal snapshot from the tick's best bid/ask.
	snapshot := &orderbook.OrderBookSnapshot{
		Symbol: symbol,
		Bids: []orderbook.PriceLevel{
			{Price: event.BestBid, Quantity: event.BidSize},
		},
		Asks: []orderbook.PriceLevel{
			{Price: event.BestAsk, Quantity: event.AskSize},
		},
		SequenceID: event.SeqID,
		Timestamp:  event.Timestamp.UnixMilli(),
	}

	if err := el.deps.OrderBook.UpdateFromSnapshot(symbol, snapshot); err != nil {
		el.logger.Printf("[EventLoop] OrderBook update failed for %s: %v", symbol, err)
	}
}

// processOrderSignals reads order signals from strategies and runs them through
// the risk manager before submitting to the execution engine.
func (el *EventLoop) processOrderSignals() {
	defer el.wg.Done()
	for {
		select {
		case <-el.stopCh:
			return
		case sig := <-el.orderSignalCh:
			el.handleOrderSignal(sig)
		}
	}
}

// handleOrderSignal runs a single order signal through risk check and execution.
func (el *EventLoop) handleOrderSignal(sig orderSignal) {
	// Convert execution.OrderRequest to risk.OrderRequest for risk check.
	riskReq := &risk.OrderRequest{
		Symbol:     sig.Request.Symbol,
		Side:       sig.Request.Side,
		OrderType:  sig.Request.OrderType,
		Price:      sig.Request.Price,
		Quantity:   sig.Request.Quantity,
		StrategyID: sig.Request.StrategyID,
	}

	decision := el.deps.RiskManager.CheckOrder(riskReq)
	if !decision.Approved {
		el.logger.Printf("[EventLoop] Order rejected by risk manager for %s: %v",
			sig.Request.Symbol, decision.Reasons)
		return
	}

	// Risk approved – submit to execution engine.
	result, err := el.deps.ExecutionEngine.PlaceOrder(sig.Request)
	if err != nil {
		el.logger.Printf("[EventLoop] Order placement error for %s: %v", sig.Request.Symbol, err)
		return
	}

	if !result.Success {
		el.logger.Printf("[EventLoop] Order placement failed for %s: %s", sig.Request.Symbol, result.Error)
		return
	}

	// Measure latency from market data receipt to order placement.
	elapsed := time.Since(sig.ReceivedAt)
	if elapsed > LatencyThreshold {
		el.latencyViolations.Add(1)
		el.logger.Printf("[EventLoop] LATENCY VIOLATION (order path): %s took %v (threshold: %v)",
			sig.Request.Symbol, elapsed, LatencyThreshold)
	}
}

// processFills reads fill events and updates strategy state and risk positions.
func (el *EventLoop) processFills() {
	defer el.wg.Done()
	for {
		select {
		case <-el.stopCh:
			return
		case fill := <-el.fillCh:
			el.handleFill(fill)
		}
	}
}

// handleFill processes a fill event by notifying the strategy engine and updating risk positions.
func (el *EventLoop) handleFill(fill models.FillEvent) {
	// Step 1: Notify strategy engine of the fill.
	el.deps.StrategyEngine.OnOrderFill(fill)

	// Step 2: Update risk manager position.
	// Construct a position update based on the fill.
	position := &models.Position{
		Symbol:         fill.Symbol,
		Quantity:       fill.Quantity,
		AvgEntryPrice:  fill.Price,
		LastUpdateTime: fill.Timestamp.UnixMicro(),
	}

	if fill.Side == models.SideBuy {
		position.Side = models.SideBuy
	} else {
		position.Side = models.SideSell
	}

	el.deps.RiskManager.UpdatePosition(fill.Symbol, position)

	// Step 3: Update PnL if this is a sell fill (realized profit).
	if fill.Side == models.SideSell {
		pnl := fill.Price.Sub(fill.Fee).Mul(fill.Quantity)
		el.deps.RiskManager.UpdatePnL(fill.StrategyID, pnl)
	}
}

// SubmitOrderSignal allows the strategy engine to submit an order signal for risk check and execution.
// This is the bridge that strategies call to propose orders.
func (el *EventLoop) SubmitOrderSignal(request *execution.OrderRequest) {
	sig := orderSignal{
		Request:    request,
		ReceivedAt: time.Now(),
	}
	select {
	case el.orderSignalCh <- sig:
	default:
		el.logger.Printf("[EventLoop] WARNING: orderSignalCh full, dropping order signal for %s", request.Symbol)
	}
}

// SubmitOrderSignalWithTimestamp allows the strategy engine to submit an order signal
// with the original market data receive timestamp for accurate latency measurement.
func (el *EventLoop) SubmitOrderSignalWithTimestamp(request *execution.OrderRequest, marketDataReceivedAt time.Time) {
	sig := orderSignal{
		Request:    request,
		ReceivedAt: marketDataReceivedAt,
	}
	select {
	case el.orderSignalCh <- sig:
	default:
		el.logger.Printf("[EventLoop] WARNING: orderSignalCh full, dropping order signal for %s", request.Symbol)
	}
}

// --- Utility methods ---

// MarketDataChannelLen returns the current number of pending market data events.
func (el *EventLoop) MarketDataChannelLen() int {
	return len(el.marketDataCh)
}

// OrderSignalChannelLen returns the current number of pending order signals.
func (el *EventLoop) OrderSignalChannelLen() int {
	return len(el.orderSignalCh)
}

// FillChannelLen returns the current number of pending fill events.
func (el *EventLoop) FillChannelLen() int {
	return len(el.fillCh)
}
