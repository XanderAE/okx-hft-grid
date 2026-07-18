package strategy

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/internal/execution"
	"github.com/yourname/okx-hft-grid/internal/monitor"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// GridDriftEngine manages automatic grid range relocation.
// It monitors price proximity to grid boundaries and shifts the entire range —
// cancelling stale orders and placing new ones — to keep the grid centered
// around the market price.
type GridDriftEngine struct {
	config      *models.GridConfig
	driftConfig *models.DriftConfig
	state       *GridState
	execEngine  execution.OrderExecutionEngine
	logger      *monitor.StructuredLogger
	metrics     *monitor.MetricsServer
	alerter     *monitor.Alerter

	// Runtime drift state
	lastDriftTime time.Time
	driftCount    int
	gridSpacing   decimal.Decimal // cached: (upper - lower) / gridCount
	mu            sync.Mutex
}

// NewGridDriftEngine creates a new drift engine from config and dependencies.
// It caches the grid spacing and initializes drift state from DriftConfig.
func NewGridDriftEngine(
	config *models.GridConfig,
	state *GridState,
	execEngine execution.OrderExecutionEngine,
	logger *monitor.StructuredLogger,
	metrics *monitor.MetricsServer,
	alerter *monitor.Alerter,
) *GridDriftEngine {
	// Cache grid spacing = (UpperPrice - LowerPrice) / GridCount
	gridCount := decimal.NewFromInt(int64(config.GridCount))
	gridSpacing := config.UpperPrice.Sub(config.LowerPrice).Div(gridCount)

	return &GridDriftEngine{
		config:      config,
		driftConfig: config.Drift,
		state:       state,
		execEngine:  execEngine,
		logger:      logger,
		metrics:     metrics,
		alerter:     alerter,
		gridSpacing: gridSpacing,
	}
}

// Reset clears the drift engine runtime state (lastDriftTime and driftCount).
// Typically called on strategy restart.
func (e *GridDriftEngine) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastDriftTime = time.Time{}
	e.driftCount = 0
}

// DriftCount returns the number of drifts executed in the current session.
func (e *GridDriftEngine) DriftCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.driftCount
}

// shouldTriggerDrift evaluates whether a drift is needed given the current price.
// Returns the drift direction and whether it's an emergency override.
//
// Logic:
//   - Price > UpperPrice → DriftUp (immediate)
//   - Price < LowerPrice → DriftDown (immediate)
//   - Price >= UpperPrice - DriftThreshold × (UpperPrice - LowerPrice) → DriftUp (boundary zone)
//   - Price <= LowerPrice + DriftThreshold × (UpperPrice - LowerPrice) → DriftDown (boundary zone)
//   - Otherwise → DriftNone
func (e *GridDriftEngine) shouldTriggerDrift(price decimal.Decimal) (models.DriftDirection, bool) {
	upper := e.config.UpperPrice
	lower := e.config.LowerPrice
	rangeWidth := upper.Sub(lower)
	threshold := e.driftConfig.DriftThreshold.Mul(rangeWidth)

	// Price exceeds upper boundary → immediate DriftUp
	if price.GreaterThan(upper) {
		emergency := e.isEmergencyOverride(price, models.DriftUp)
		return models.DriftUp, emergency
	}

	// Price below lower boundary → immediate DriftDown
	if price.LessThan(lower) {
		emergency := e.isEmergencyOverride(price, models.DriftDown)
		return models.DriftDown, emergency
	}

	// Upper boundary zone: price >= UpperPrice - threshold
	upperZone := upper.Sub(threshold)
	if price.GreaterThanOrEqual(upperZone) {
		emergency := e.isEmergencyOverride(price, models.DriftUp)
		return models.DriftUp, emergency
	}

	// Lower boundary zone: price <= LowerPrice + threshold
	lowerZone := lower.Add(threshold)
	if price.LessThanOrEqual(lowerZone) {
		emergency := e.isEmergencyOverride(price, models.DriftDown)
		return models.DriftDown, emergency
	}

	return models.DriftNone, false
}

// isCooldownActive returns true if the cooldown period has not elapsed since the last drift.
func (e *GridDriftEngine) isCooldownActive() bool {
	if e.lastDriftTime.IsZero() {
		return false
	}
	return time.Since(e.lastDriftTime) < e.driftConfig.CooldownPeriod
}

// isEmergencyOverride returns true if the price exceeds the grid boundary
// by more than 2 × DriftStep × gridSpacing, warranting an override of cooldown.
func (e *GridDriftEngine) isEmergencyOverride(price decimal.Decimal, direction models.DriftDirection) bool {
	driftStep := decimal.NewFromInt(int64(e.driftConfig.DriftStep))
	emergencyThreshold := driftStep.Mul(e.gridSpacing).Mul(decimal.NewFromInt(2))

	switch direction {
	case models.DriftUp:
		// Price exceeds upper boundary by > 2 × DriftStep × spacing
		overshoot := price.Sub(e.config.UpperPrice)
		return overshoot.GreaterThan(emergencyThreshold)
	case models.DriftDown:
		// Price is below lower boundary by > 2 × DriftStep × spacing
		undershoot := e.config.LowerPrice.Sub(price)
		return undershoot.GreaterThan(emergencyThreshold)
	default:
		return false
	}
}

// computeNewRange calculates the new LowerPrice and UpperPrice given the drift direction.
// shift = DriftStep × gridSpacing
// DriftUp: newLower = LowerPrice + shift, newUpper = UpperPrice + shift
// DriftDown: newLower = LowerPrice - shift, newUpper = UpperPrice - shift
// If newLower <= 0, clamp to minimum tick size (0.00000001).
func (e *GridDriftEngine) computeNewRange(direction models.DriftDirection) (newLower, newUpper decimal.Decimal) {
	driftStep := decimal.NewFromInt(int64(e.driftConfig.DriftStep))
	shift := driftStep.Mul(e.gridSpacing)
	rangeWidth := e.config.UpperPrice.Sub(e.config.LowerPrice)

	switch direction {
	case models.DriftUp:
		newLower = e.config.LowerPrice.Add(shift)
		newUpper = e.config.UpperPrice.Add(shift)
	case models.DriftDown:
		newLower = e.config.LowerPrice.Sub(shift)
		newUpper = e.config.UpperPrice.Sub(shift)
	default:
		return e.config.LowerPrice, e.config.UpperPrice
	}

	// Clamp newLower to minimum tick size if <= 0
	minTick := decimal.NewFromFloat(0.00000001)
	if newLower.LessThanOrEqual(decimal.Zero) {
		newLower = minTick
		newUpper = newLower.Add(rangeWidth)
	}

	return newLower, newUpper
}

// cancelStaleOrders cancels orders at levels that are no longer in the new range.
// It compares the current state.Levels with newLevels to identify stale levels,
// then cancels each stale order via execEngine.CancelOrder with up to 3 retries.
// Returns the count of successfully cancelled orders and an error if zero were cancelled
// (when there were stale orders to cancel), which signals an abort.
func (e *GridDriftEngine) cancelStaleOrders(newLevels []decimal.Decimal) (int, error) {
	// Build a set of new level prices for quick lookup
	newLevelSet := make(map[string]struct{}, len(newLevels))
	for _, lvl := range newLevels {
		newLevelSet[lvl.String()] = struct{}{}
	}

	// Identify stale levels: levels in current state that are NOT in newLevels
	var staleOrders []string
	for _, level := range e.state.Levels {
		if _, exists := newLevelSet[level.Price.String()]; exists {
			// Level remains in new range — do not cancel
			continue
		}
		// Level is stale — check if it has an active order
		if (level.HasBuyOrder || level.HasSellOrder) && level.OrderId != "" {
			staleOrders = append(staleOrders, level.OrderId)
		}
	}

	// No stale orders to cancel — nothing to do (not an error)
	if len(staleOrders) == 0 {
		return 0, nil
	}

	// Cancel each stale order with up to 3 retries per order
	cancelled := 0
	for _, orderID := range staleOrders {
		success := false
		for attempt := 0; attempt < 3; attempt++ {
			result, err := e.execEngine.CancelOrder(orderID)
			if err == nil && result != nil && result.Success {
				success = true
				break
			}
			// Log retry attempt
			if e.logger != nil {
				e.logger.LogWarn("drift cancel order retry", map[string]string{
					"orderID": orderID,
					"attempt": fmt.Sprintf("%d", attempt+1),
				})
			}
			if attempt < 2 {
				time.Sleep(1 * time.Second)
			}
		}
		if success {
			cancelled++
		} else {
			if e.logger != nil {
				e.logger.LogError("drift cancel order failed after retries", map[string]string{
					"orderID": orderID,
				})
			}
		}
	}

	// If zero orders cancelled but there were stale orders → abort signal
	if cancelled == 0 {
		return 0, errors.New("drift aborted: failed to cancel any stale orders")
	}

	return cancelled, nil
}

// executeDrift performs the full drift sequence: cancel stale → update config → recalculate levels → place new orders.
// It records drift metadata (lastDriftTime, driftCount), logs a DriftEvent, emits metrics,
// and sends an alert if MaxDrifts is reached.
func (e *GridDriftEngine) executeDrift(direction models.DriftDirection, currentPrice decimal.Decimal, emergency bool) error {
	startTime := time.Now()

	// Save old range for logging
	oldLower := e.config.LowerPrice
	oldUpper := e.config.UpperPrice

	// Compute new range
	newLower, newUpper := e.computeNewRange(direction)

	// Compute new levels using a temporary config with new bounds
	tmpConfig := *e.config
	tmpConfig.LowerPrice = newLower
	tmpConfig.UpperPrice = newUpper
	newLevels, err := CalculateGridLevels(&tmpConfig)
	if err != nil {
		if e.logger != nil {
			e.logger.LogError("drift: failed to calculate new grid levels", map[string]string{
				"error":     err.Error(),
				"direction": direction.String(),
			})
		}
		return fmt.Errorf("drift: calculate grid levels: %w", err)
	}

	// Determine trigger reason
	triggerReason := "boundary_zone"
	if currentPrice.GreaterThan(oldUpper) || currentPrice.LessThan(oldLower) {
		triggerReason = "price_exceeded"
	}
	if emergency {
		triggerReason = "emergency"
	}

	// Log drift initiated
	if e.logger != nil {
		e.logger.LogInfo("drift initiated", map[string]string{
			"direction":     direction.String(),
			"triggerReason": triggerReason,
			"triggerPrice":  currentPrice.String(),
			"oldLower":      oldLower.String(),
			"oldUpper":      oldUpper.String(),
			"newLower":      newLower.String(),
			"newUpper":      newUpper.String(),
		})
	}

	// Phase 1: Cancel stale orders
	cancelled, cancelErr := e.cancelStaleOrders(newLevels)
	if cancelErr != nil {
		// Abort — log and return error; state is unchanged
		elapsed := time.Since(startTime).Milliseconds()
		if e.logger != nil {
			e.logger.LogError("drift aborted: cancellation phase failed", map[string]string{
				"error":     cancelErr.Error(),
				"elapsedMs": fmt.Sprintf("%d", elapsed),
			})
		}
		if e.metrics != nil {
			e.metrics.IncrementErrorCount()
		}
		return cancelErr
	}

	// Phase 2: Update config boundaries and state levels
	e.config.LowerPrice = newLower
	e.config.UpperPrice = newUpper

	// Build new GridLevel slice from computed levels
	newGridLevels := make([]models.GridLevel, len(newLevels))
	for i, lvl := range newLevels {
		newGridLevels[i] = models.GridLevel{
			Index: i,
			Price: lvl,
		}
	}
	e.state.Levels = newGridLevels

	// Phase 3: Place new orders (partial failure is acceptable)
	placed, _ := e.placeNewOrders(newLevels, currentPrice)

	// Record drift metadata
	e.lastDriftTime = time.Now()
	e.driftCount++

	// Update grid spacing for the new range
	gridCount := decimal.NewFromInt(int64(e.config.GridCount))
	e.gridSpacing = newUpper.Sub(newLower).Div(gridCount)

	// Compute elapsed time
	elapsed := time.Since(startTime).Milliseconds()

	// Log DriftEvent
	ordersFailed := len(newLevels) - cancelled - placed
	if ordersFailed < 0 {
		ordersFailed = 0
	}
	event := models.DriftEvent{
		Timestamp:       startTime,
		Direction:       direction,
		TriggerReason:   triggerReason,
		TriggerPrice:    currentPrice,
		OldLower:        oldLower,
		OldUpper:        oldUpper,
		NewLower:        newLower,
		NewUpper:        newUpper,
		OrdersCancelled: cancelled,
		OrdersPlaced:    placed,
		OrdersFailed:    ordersFailed,
		ElapsedMs:       elapsed,
		Success:         true,
	}
	_ = event // event used for audit logging below

	if e.logger != nil {
		e.logger.LogInfo("drift completed", map[string]string{
			"direction":       direction.String(),
			"triggerReason":   triggerReason,
			"oldLower":        oldLower.String(),
			"oldUpper":        oldUpper.String(),
			"newLower":        newLower.String(),
			"newUpper":        newUpper.String(),
			"ordersCancelled": fmt.Sprintf("%d", cancelled),
			"ordersPlaced":    fmt.Sprintf("%d", placed),
			"ordersFailed":    fmt.Sprintf("%d", ordersFailed),
			"elapsedMs":       fmt.Sprintf("%d", elapsed),
		})
	}

	// Emit metrics
	if e.metrics != nil {
		e.metrics.IncrementOrderCount()
	}

	// Alert if MaxDrifts reached
	if e.driftConfig.MaxDrifts > 0 && e.driftCount >= e.driftConfig.MaxDrifts {
		if e.alerter != nil {
			e.alerter.SendCritical("grid drift: MaxDrifts limit reached", map[string]string{
				"driftCount": fmt.Sprintf("%d", e.driftCount),
				"maxDrifts":  fmt.Sprintf("%d", e.driftConfig.MaxDrifts),
				"symbol":     e.config.Symbol,
			})
		}
	}

	return nil
}

// OnPriceUpdate is the public entry point called by the Scheduler on each price tick.
// It evaluates drift conditions and executes a drift if warranted.
// Returns true if a drift was executed successfully, false otherwise.
func (e *GridDriftEngine) OnPriceUpdate(currentPrice decimal.Decimal) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Check if drift is enabled
	if e.driftConfig == nil || !e.driftConfig.Enabled {
		return false
	}

	// Check if MaxDrifts reached
	if e.driftConfig.MaxDrifts > 0 && e.driftCount >= e.driftConfig.MaxDrifts {
		return false
	}

	// Evaluate drift trigger
	direction, isEmergency := e.shouldTriggerDrift(currentPrice)
	if direction == models.DriftNone {
		return false
	}

	// Check cooldown
	if e.isCooldownActive() {
		if !isEmergency {
			// Suppressed by cooldown — log and return
			if e.logger != nil {
				remaining := e.driftConfig.CooldownPeriod - time.Since(e.lastDriftTime)
				e.logger.LogInfo("drift suppressed: cooldown active", map[string]string{
					"direction":         direction.String(),
					"remainingCooldown": remaining.String(),
					"price":             currentPrice.String(),
				})
			}
			return false
		}
		// Emergency override — proceed despite cooldown
	}

	// Execute drift
	err := e.executeDrift(direction, currentPrice, isEmergency)
	if err != nil {
		return false
	}

	return true
}

// placeNewOrders places orders at newly created levels that were not in the old range.
// For new levels below currentPrice → BUY (POST_ONLY, OrderSize from config).
// For new levels above currentPrice → SELL (POST_ONLY, OrderSize from config).
// Retries up to 3 times per order with 1-second sleep between retries.
// Returns the count of successfully placed orders.
// Partial placement failure is acceptable — the method logs failures and continues.
func (e *GridDriftEngine) placeNewOrders(newLevels []decimal.Decimal, currentPrice decimal.Decimal) (int, error) {
	// Build a set of old level prices for quick lookup
	oldLevelSet := make(map[string]struct{}, len(e.state.Levels))
	for _, level := range e.state.Levels {
		oldLevelSet[level.Price.String()] = struct{}{}
	}

	// Identify new levels: levels in newLevels that are NOT in the old state
	type orderTarget struct {
		price decimal.Decimal
		side  models.Side
		index int
	}
	var targets []orderTarget
	for i, lvl := range newLevels {
		if _, exists := oldLevelSet[lvl.String()]; exists {
			// Level already existed — skip
			continue
		}
		// Determine side based on price relative to current market price
		if lvl.LessThan(currentPrice) {
			targets = append(targets, orderTarget{price: lvl, side: models.SideBuy, index: i})
		} else if lvl.GreaterThan(currentPrice) {
			targets = append(targets, orderTarget{price: lvl, side: models.SideSell, index: i})
		}
		// Level equal to currentPrice → no order placed
	}

	// No new levels to place
	if len(targets) == 0 {
		return 0, nil
	}

	// Place each order with up to 3 retries
	placed := 0
	for _, target := range targets {
		req := &execution.OrderRequest{
			Symbol:     e.config.Symbol,
			Side:       target.side,
			OrderType:  models.OrderTypePostOnly,
			Price:      target.price,
			Quantity:   e.config.OrderSize,
			StrategyID: "grid-drift",
			GridLevel:  target.index,
		}

		success := false
		for attempt := 0; attempt < 3; attempt++ {
			result, err := e.execEngine.PlaceOrder(req)
			if err == nil && result != nil && result.Success {
				success = true
				break
			}
			// Log retry attempt
			if e.logger != nil {
				e.logger.LogWarn("drift place order retry", map[string]string{
					"price":   target.price.String(),
					"side":    target.side.String(),
					"attempt": fmt.Sprintf("%d", attempt+1),
				})
			}
			if attempt < 2 {
				time.Sleep(1 * time.Second)
			}
		}
		if success {
			placed++
		} else {
			// Partial placement failure is acceptable — just log and continue
			if e.logger != nil {
				e.logger.LogWarn("drift place order failed after retries", map[string]string{
					"price": target.price.String(),
					"side":  target.side.String(),
				})
			}
		}
	}

	return placed, nil
}
