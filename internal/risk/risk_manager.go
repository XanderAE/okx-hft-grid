package risk

import (
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// RiskManagerImpl implements the RiskManager interface with thread-safe risk checks.
// All checks target < 50μs p99 latency.
type RiskManagerImpl struct {
	mu sync.RWMutex

	limits *models.RiskLimits

	// Per-symbol position tracking (notional value = qty * price)
	positions map[string]*models.Position

	// Daily PnL tracking per strategy
	dailyPnL map[string]decimal.Decimal

	// Sliding window of order timestamps for rate limiting
	orderTimestamps []time.Time

	// Per-symbol open order counts
	openOrderCounts map[string]int

	// Emergency stop state
	emergencyStop       bool
	emergencyStopReason string
}

// NewRiskManager creates a new RiskManagerImpl with default configuration.
func NewRiskManager(limits *models.RiskLimits) *RiskManagerImpl {
	return &RiskManagerImpl{
		limits:          limits,
		positions:       make(map[string]*models.Position),
		dailyPnL:        make(map[string]decimal.Decimal),
		orderTimestamps: make([]time.Time, 0, 128),
		openOrderCounts: make(map[string]int),
	}
}

// CheckOrder evaluates a single order against all risk rules.
func (rm *RiskManagerImpl) CheckOrder(order *OrderRequest) *RiskDecision {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	return rm.checkOrderLocked(order)
}

// checkOrderLocked performs the actual checks while holding the lock.
func (rm *RiskManagerImpl) checkOrderLocked(order *OrderRequest) *RiskDecision {
	decision := &RiskDecision{Approved: true}

	// Check 0: Emergency stop
	if rm.emergencyStop {
		decision.Approved = false
		decision.Reasons = append(decision.Reasons, "emergency stop active: "+rm.emergencyStopReason)
		return decision
	}

	if rm.limits == nil {
		return decision
	}

	// Check 1: Single symbol position limit
	rm.checkPositionLimit(order, decision)

	// Check 2: Total portfolio exposure limit
	rm.checkTotalExposure(order, decision)

	// Check 3: Daily loss limit
	rm.checkDailyLoss(decision)

	// Check 4: Order rate limit (sliding window)
	rm.checkOrderRate(decision)

	// Check 5: Open order count limit
	rm.checkOpenOrderCount(order, decision)

	// Check 6: Minimum spread check
	rm.checkMinSpread(order, decision)

	if len(decision.Reasons) > 0 {
		decision.Approved = false
	}

	// Record this order timestamp if approved
	if decision.Approved {
		rm.orderTimestamps = append(rm.orderTimestamps, time.Now())
	}

	return decision
}

// checkPositionLimit checks if adding this order would exceed the per-symbol position limit.
func (rm *RiskManagerImpl) checkPositionLimit(order *OrderRequest, decision *RiskDecision) {
	if rm.limits.MaxPositionPerSymbol.IsZero() {
		return
	}

	notional := order.Price.Mul(order.Quantity)

	// Get existing position notional
	existingNotional := decimal.Zero
	if pos, ok := rm.positions[order.Symbol]; ok {
		existingNotional = pos.Quantity.Mul(pos.AvgEntryPrice)
	}

	// Calculate new total notional for this symbol
	newNotional := existingNotional.Add(notional)

	if newNotional.Abs().GreaterThan(rm.limits.MaxPositionPerSymbol) {
		decision.Reasons = append(decision.Reasons,
			"position limit exceeded for "+order.Symbol+": "+newNotional.Abs().String()+" > "+rm.limits.MaxPositionPerSymbol.String())
	}
}

// checkTotalExposure checks if adding this order would exceed total portfolio exposure.
func (rm *RiskManagerImpl) checkTotalExposure(order *OrderRequest, decision *RiskDecision) {
	if rm.limits.MaxTotalPosition.IsZero() {
		return
	}

	orderNotional := order.Price.Mul(order.Quantity)

	// Sum all current position notionals
	totalExposure := decimal.Zero
	for _, pos := range rm.positions {
		totalExposure = totalExposure.Add(pos.Quantity.Mul(pos.AvgEntryPrice).Abs())
	}

	// Add this order's notional
	newTotal := totalExposure.Add(orderNotional.Abs())

	if newTotal.GreaterThan(rm.limits.MaxTotalPosition) {
		decision.Reasons = append(decision.Reasons,
			"total exposure limit exceeded: "+newTotal.String()+" > "+rm.limits.MaxTotalPosition.String())
	}
}

// checkDailyLoss checks if the daily PnL has exceeded the maximum daily loss.
func (rm *RiskManagerImpl) checkDailyLoss(decision *RiskDecision) {
	if rm.limits.MaxDailyLoss.IsZero() {
		return
	}

	totalPnL := decimal.Zero
	for _, pnl := range rm.dailyPnL {
		totalPnL = totalPnL.Add(pnl)
	}

	// MaxDailyLoss is a positive number; reject if daily PnL is worse than -MaxDailyLoss
	if totalPnL.LessThan(rm.limits.MaxDailyLoss.Neg()) {
		decision.Reasons = append(decision.Reasons,
			"daily loss limit exceeded: PnL "+totalPnL.String()+" < -"+rm.limits.MaxDailyLoss.String())
	}
}

// checkOrderRate checks if the order rate within the 1-second sliding window is exceeded.
func (rm *RiskManagerImpl) checkOrderRate(decision *RiskDecision) {
	if rm.limits.MaxOrdersPerSecond <= 0 {
		return
	}

	now := time.Now()
	cutoff := now.Add(-1 * time.Second)

	// Prune old timestamps
	startIdx := 0
	for startIdx < len(rm.orderTimestamps) && rm.orderTimestamps[startIdx].Before(cutoff) {
		startIdx++
	}
	if startIdx > 0 {
		rm.orderTimestamps = rm.orderTimestamps[startIdx:]
	}

	// Count orders in the last second
	count := len(rm.orderTimestamps)

	if count >= rm.limits.MaxOrdersPerSecond {
		decision.Reasons = append(decision.Reasons,
			"order rate limit exceeded: "+string(rune('0'+count))+" orders in last 1s (max: "+string(rune('0'+rm.limits.MaxOrdersPerSecond))+")")
	}
}

// checkOpenOrderCount checks if the symbol already has too many open orders.
func (rm *RiskManagerImpl) checkOpenOrderCount(order *OrderRequest, decision *RiskDecision) {
	if rm.limits.MaxOpenOrders <= 0 {
		return
	}

	currentCount := rm.openOrderCounts[order.Symbol]
	if currentCount >= rm.limits.MaxOpenOrders {
		decision.Reasons = append(decision.Reasons,
			"open order limit exceeded for "+order.Symbol+": "+itoa(currentCount)+" >= "+itoa(rm.limits.MaxOpenOrders))
	}
}

// checkMinSpread checks if the order price is too close to the other side (spread too narrow).
// The spread in basis points is passed in as part of the order's price context.
// For simplicity, the spread check uses the order's Price field: if the order price implies
// a spread less than minSpreadBps, it is rejected.
// In practice, the caller should set SpreadBps on the order request.
func (rm *RiskManagerImpl) checkMinSpread(order *OrderRequest, decision *RiskDecision) {
	if rm.limits.MinSpreadBps <= 0 {
		return
	}

	if order.SpreadBps < rm.limits.MinSpreadBps {
		decision.Reasons = append(decision.Reasons,
			"spread too narrow: "+itoa(order.SpreadBps)+" bps < min "+itoa(rm.limits.MinSpreadBps)+" bps")
	}
}

// CheckBatchOrders evaluates a batch of orders against all risk rules.
func (rm *RiskManagerImpl) CheckBatchOrders(orders []*OrderRequest) *RiskDecision {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	combined := &RiskDecision{Approved: true}

	for _, order := range orders {
		d := rm.checkOrderLocked(order)
		if !d.Approved {
			combined.Approved = false
			combined.Reasons = append(combined.Reasons, d.Reasons...)
		}
	}

	return combined
}

// UpdatePosition updates the tracked position for a symbol.
func (rm *RiskManagerImpl) UpdatePosition(symbol string, position *models.Position) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rm.positions[symbol] = position
}

// UpdatePnL updates the PnL for a specific strategy.
func (rm *RiskManagerImpl) UpdatePnL(strategyID string, pnl decimal.Decimal) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rm.dailyPnL[strategyID] = pnl
}

// GetRiskMetrics returns the current risk metrics snapshot.
func (rm *RiskManagerImpl) GetRiskMetrics() *RiskMetrics {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	// Calculate total daily PnL
	totalPnL := decimal.Zero
	for _, pnl := range rm.dailyPnL {
		totalPnL = totalPnL.Add(pnl)
	}

	// Calculate total exposure and per-symbol positions
	totalExposure := decimal.Zero
	positionsBySymbol := make(map[string]decimal.Decimal)
	for symbol, pos := range rm.positions {
		notional := pos.Quantity.Mul(pos.AvgEntryPrice).Abs()
		totalExposure = totalExposure.Add(notional)
		positionsBySymbol[symbol] = notional
	}

	// Count orders in last second
	now := time.Now()
	cutoff := now.Add(-1 * time.Second)
	ordersInLastSec := 0
	for i := len(rm.orderTimestamps) - 1; i >= 0; i-- {
		if rm.orderTimestamps[i].After(cutoff) {
			ordersInLastSec++
		} else {
			break
		}
	}

	// Copy open order counts
	openOrders := make(map[string]int, len(rm.openOrderCounts))
	for k, v := range rm.openOrderCounts {
		openOrders[k] = v
	}

	return &RiskMetrics{
		DailyPnL:            totalPnL,
		TotalExposure:       totalExposure,
		PositionsBySymbol:   positionsBySymbol,
		OpenOrdersBySymbol:  openOrders,
		OrdersInLastSecond:  ordersInLastSec,
		EmergencyStopActive: rm.emergencyStop,
		EmergencyStopReason: rm.emergencyStopReason,
	}
}

// SetRiskLimits configures the risk limits.
func (rm *RiskManagerImpl) SetRiskLimits(limits *models.RiskLimits) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rm.limits = limits
}

// EmergencyStop triggers emergency stop.
func (rm *RiskManagerImpl) EmergencyStop(reason string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rm.emergencyStop = true
	rm.emergencyStopReason = reason
}

// ResumeFromEmergencyStop deactivates emergency stop.
func (rm *RiskManagerImpl) ResumeFromEmergencyStop() error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rm.emergencyStop = false
	rm.emergencyStopReason = ""
	return nil
}

// IsEmergencyStopActive returns whether emergency stop is active.
func (rm *RiskManagerImpl) IsEmergencyStopActive() bool {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	return rm.emergencyStop
}

// UpdateOpenOrderCount updates the tracked open order count for a symbol.
func (rm *RiskManagerImpl) UpdateOpenOrderCount(symbol string, count int) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rm.openOrderCounts[symbol] = count
}

// ResetDailyPnL clears the daily PnL tracking (called at day boundary).
func (rm *RiskManagerImpl) ResetDailyPnL() {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rm.dailyPnL = make(map[string]decimal.Decimal)
}

// itoa converts an int to string without importing strconv to keep the hot path minimal.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	buf := make([]byte, 0, 12)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	if neg {
		buf = append(buf, '-')
	}
	// reverse
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
