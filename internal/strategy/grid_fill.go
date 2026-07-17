package strategy

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

const (
	// maxRetries is the number of times to retry counter order placement.
	maxRetries = 3
	// retryInterval is the interval between retries.
	retryInterval = 1 * time.Second
)

var (
	ErrInvalidFillLevel    = errors.New("fill grid level is out of range")
	ErrInvalidFillSide     = errors.New("fill side must be BUY or SELL")
	ErrNoLevelsConfigured  = errors.New("grid state has no levels configured")
	ErrNilState            = errors.New("grid state is nil")
	ErrNilConfig           = errors.New("grid config is nil")
	ErrNegativeFeeRate     = errors.New("fee rate must be non-negative")
	ErrInvalidMaxPosition  = errors.New("max position must be positive")
)

// GridState tracks the runtime state of a grid trading strategy.
type GridState struct {
	Levels        []models.GridLevel // Grid levels with order tracking
	Position      decimal.Decimal    // Current net position (positive = long)
	AvgEntryPrice decimal.Decimal    // Volume-weighted average buy price
	RealizedPnL   decimal.Decimal    // Cumulative realized profit/loss
	TotalBuys     int                // Total buy fills count
	TotalSells    int                // Total sell fills count
}

// GridFillResult contains the result of processing a grid fill event.
type GridFillResult struct {
	CounterOrder          *models.Order   // Counter order to place (nil if none)
	RealizedPnL           decimal.Decimal // Updated realized PnL after this fill
	NeedsManualIntervention bool          // True if placement failed after retries
	Error                 error           // Error if any
	Reason                string          // Reason for skipping counter order or requiring intervention
}

// OrderPlacer is an interface for placing orders, allowing dependency injection for testing.
type OrderPlacer interface {
	PlaceOrder(order *models.Order) error
}

// HandleGridFill processes a grid fill event and determines whether to place a counter order.
//
// BUY fill at level[i] -> counter SELL at level[i+1] (same quantity)
// SELL fill at level[i] -> counter BUY at level[i-1] (same quantity)
//
// Checks performed:
// - Boundary: no counter at level[0] for BUY fill or level[last] for SELL fill
// - Profit: net profit > double-sided fees before placing counter SELL
// - Position limit: skip counter BUY if position + qty > maxPosition
// - Updates realizedPnL on SELL fills
func HandleGridFill(fill models.FillEvent, state *GridState, config *models.GridConfig) *GridFillResult {
	result := &GridFillResult{
		RealizedPnL: state.RealizedPnL,
	}

	// Validate inputs
	if state == nil {
		result.Error = ErrNilState
		return result
	}
	if config == nil {
		result.Error = ErrNilConfig
		return result
	}
	if len(state.Levels) == 0 {
		result.Error = ErrNoLevelsConfigured
		return result
	}
	if fill.GridLevel < 0 || fill.GridLevel >= len(state.Levels) {
		result.Error = ErrInvalidFillLevel
		return result
	}

	switch fill.Side {
	case models.SideBuy:
		return handleBuyFill(fill, state, config, result)
	case models.SideSell:
		return handleSellFill(fill, state, config, result)
	default:
		result.Error = ErrInvalidFillSide
		return result
	}
}

// handleBuyFill processes a BUY fill and potentially places a SELL counter order at the next higher level.
func handleBuyFill(fill models.FillEvent, state *GridState, config *models.GridConfig, result *GridFillResult) *GridFillResult {
	// Update position and average entry price
	updatePositionOnBuy(state, fill.Price, fill.Quantity)

	// Increment buy count
	state.TotalBuys++

	// Mark the current level as no longer having a buy order
	state.Levels[fill.GridLevel].HasBuyOrder = false

	// Boundary check: if this is the last level, no counter order above
	targetLevel := fill.GridLevel + 1
	if targetLevel >= len(state.Levels) {
		result.Reason = "BUY fill at highest grid level, no counter SELL possible"
		return result
	}

	// Profit check: expected sell price - buy price must exceed double-sided fees
	sellPrice := state.Levels[targetLevel].Price
	expectedProfit := sellPrice.Sub(fill.Price).Mul(fill.Quantity)
	// Fee = quantity * price * feeRate for each side (buy + sell)
	buyNotional := fill.Quantity.Mul(fill.Price)
	sellNotional := fill.Quantity.Mul(sellPrice)
	fees := buyNotional.Add(sellNotional).Mul(config.FeeRate)

	if expectedProfit.LessThanOrEqual(fees) {
		result.Reason = fmt.Sprintf("profit check failed: expected profit %s <= fees %s", expectedProfit, fees)
		return result
	}

	// Create counter SELL order
	counterOrder := &models.Order{
		Symbol:    config.Symbol,
		Side:      models.SideSell,
		OrderType: models.OrderTypePostOnly,
		Price:     sellPrice,
		Quantity:  fill.Quantity,
		Status:    models.OrderStatusPending,
	}

	result.CounterOrder = counterOrder
	state.Levels[targetLevel].HasSellOrder = true

	return result
}

// handleSellFill processes a SELL fill and potentially places a BUY counter order at the next lower level.
func handleSellFill(fill models.FillEvent, state *GridState, config *models.GridConfig, result *GridFillResult) *GridFillResult {
	// Update realized PnL: (fill_price - avg_entry_price) * quantity
	if state.AvgEntryPrice.IsPositive() {
		pnl := fill.Price.Sub(state.AvgEntryPrice).Mul(fill.Quantity)
		state.RealizedPnL = state.RealizedPnL.Add(pnl)
	}
	result.RealizedPnL = state.RealizedPnL

	// Update position (reduce)
	updatePositionOnSell(state, fill.Quantity)

	// Increment sell count
	state.TotalSells++

	// Mark the current level as no longer having a sell order
	state.Levels[fill.GridLevel].HasSellOrder = false

	// Boundary check: if this is the first level, no counter order below
	targetLevel := fill.GridLevel - 1
	if targetLevel < 0 {
		result.Reason = "SELL fill at lowest grid level, no counter BUY possible"
		return result
	}

	// Position limit check: would placing a BUY exceed maxPosition?
	buyPrice := state.Levels[targetLevel].Price
	newPosition := state.Position.Add(fill.Quantity)
	if config.MaxPosition.IsPositive() && newPosition.GreaterThan(config.MaxPosition) {
		result.Reason = fmt.Sprintf("position limit: placing BUY would bring position to %s, exceeding max %s",
			newPosition, config.MaxPosition)
		return result
	}

	// Create counter BUY order
	counterOrder := &models.Order{
		Symbol:    config.Symbol,
		Side:      models.SideBuy,
		OrderType: models.OrderTypePostOnly,
		Price:     buyPrice,
		Quantity:  fill.Quantity,
		Status:    models.OrderStatusPending,
	}

	result.CounterOrder = counterOrder
	state.Levels[targetLevel].HasBuyOrder = true
	_ = counterOrder // suppress unused warning

	return result
}

// HandleGridFillWithRetry processes a grid fill and attempts to place the counter order
// with up to maxRetries retries (1-second intervals). If all retries fail, it marks
// the result as needing manual intervention.
func HandleGridFillWithRetry(fill models.FillEvent, state *GridState, config *models.GridConfig, placer OrderPlacer) *GridFillResult {
	result := HandleGridFill(fill, state, config)
	if result.Error != nil || result.CounterOrder == nil {
		return result
	}

	// Try to place the counter order with retries
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := placer.PlaceOrder(result.CounterOrder)
		if err == nil {
			return result
		}
		lastErr = err
		if attempt < maxRetries-1 {
			time.Sleep(retryInterval)
		}
	}

	// All retries exhausted
	result.NeedsManualIntervention = true
	result.Error = fmt.Errorf("counter order placement failed after %d retries: %w", maxRetries, lastErr)
	result.Reason = "order placement failed, requires manual intervention"

	return result
}

// updatePositionOnBuy updates the grid state position and average entry price when a buy fills.
func updatePositionOnBuy(state *GridState, fillPrice, fillQty decimal.Decimal) {
	if state.Position.IsZero() {
		// First buy, set average entry directly
		state.AvgEntryPrice = fillPrice
		state.Position = fillQty
	} else if state.Position.IsPositive() {
		// Adding to existing long position: calculate new VWAP
		totalCost := state.AvgEntryPrice.Mul(state.Position).Add(fillPrice.Mul(fillQty))
		state.Position = state.Position.Add(fillQty)
		state.AvgEntryPrice = totalCost.Div(state.Position)
	} else {
		// Position is negative (short), buying reduces it
		state.Position = state.Position.Add(fillQty)
		if state.Position.IsPositive() {
			// Crossed to long, reset avg entry to fill price
			state.AvgEntryPrice = fillPrice
		}
	}
}

// updatePositionOnSell updates the grid state position when a sell fills.
func updatePositionOnSell(state *GridState, fillQty decimal.Decimal) {
	state.Position = state.Position.Sub(fillQty)
	// If position becomes zero or negative, avg entry price is no longer meaningful
	// but we keep it for PnL calculation purposes until next buy
	if state.Position.LessThanOrEqual(decimal.Zero) {
		state.Position = decimal.Zero
		state.AvgEntryPrice = decimal.Zero
	}
}
