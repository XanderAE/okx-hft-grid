package execution

import (
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// PositionEntry represents a single filled BUY position awaiting a SELL.
type PositionEntry struct {
	Symbol   string
	BuyPrice decimal.Decimal
	Quantity decimal.Decimal
	BuyTime  time.Time
	OrderID  string
}

// InventoryTracker tracks open positions (bought but not yet sold).
type InventoryTracker struct {
	mu                sync.RWMutex
	positions         map[string]*PositionEntry // symbol -> current position (single-grid = one position per symbol)
	maxValuePerSymbol decimal.Decimal           // max position value in USDT per symbol
}

// NewInventoryTracker creates a tracker with the given max position value per symbol.
func NewInventoryTracker(maxValuePerSymbol decimal.Decimal) *InventoryTracker {
	return &InventoryTracker{
		positions:         make(map[string]*PositionEntry),
		maxValuePerSymbol: maxValuePerSymbol,
	}
}

// RecordBuy records a new BUY fill. Replaces any existing position for the symbol.
func (t *InventoryTracker) RecordBuy(symbol string, price, quantity decimal.Decimal, orderID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.positions[symbol] = &PositionEntry{
		Symbol:   symbol,
		BuyPrice: price,
		Quantity: quantity,
		BuyTime:  time.Now(),
		OrderID:  orderID,
	}
}

// ClearPosition clears the position for a symbol (called after SELL fills).
func (t *InventoryTracker) ClearPosition(symbol string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.positions, symbol)
}

// GetPosition returns the current position for a symbol, or nil if none.
func (t *InventoryTracker) GetPosition(symbol string) *PositionEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	pos := t.positions[symbol]
	if pos == nil {
		return nil
	}
	// Return a copy
	cpy := *pos
	return &cpy
}

// HasPosition returns true if there's an open position for the symbol.
func (t *InventoryTracker) HasPosition(symbol string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.positions[symbol] != nil
}

// IsInventoryFull returns true if the position value exceeds the max for the symbol.
func (t *InventoryTracker) IsInventoryFull(symbol string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	pos := t.positions[symbol]
	if pos == nil {
		return false
	}
	posValue := pos.BuyPrice.Mul(pos.Quantity)
	return posValue.GreaterThanOrEqual(t.maxValuePerSymbol)
}

// CalculateSellPrice returns the appropriate SELL price based on time decay.
// - < 5 min: buyPrice + 0.2% (normal profit)
// - 5-15 min: buyPrice + 0.1% (reduced expectation)
// - 15-30 min: buyPrice - 0.05% (small loss to exit)
// - > 30 min: decimal.Zero (signal to use market price)
func (t *InventoryTracker) CalculateSellPrice(symbol string) (decimal.Decimal, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	pos := t.positions[symbol]
	if pos == nil {
		return decimal.Zero, false
	}

	elapsed := time.Since(pos.BuyTime)
	buyPrice := pos.BuyPrice

	switch {
	case elapsed < 5*time.Minute:
		// Normal: +0.2%
		return buyPrice.Mul(decimal.NewFromFloat(1.002)), true
	case elapsed < 15*time.Minute:
		// Reduced: +0.1%
		return buyPrice.Mul(decimal.NewFromFloat(1.001)), true
	case elapsed < 30*time.Minute:
		// Small loss: -0.05%
		return buyPrice.Mul(decimal.NewFromFloat(0.9995)), true
	default:
		// Force exit: return zero to signal market sell
		return decimal.Zero, true
	}
}

// ShouldHardStop returns true if the position has unrealized loss exceeding the threshold.
func (t *InventoryTracker) ShouldHardStop(symbol string, currentPrice decimal.Decimal, maxLossPct decimal.Decimal) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	pos := t.positions[symbol]
	if pos == nil {
		return false
	}
	// loss% = (buyPrice - currentPrice) / buyPrice
	if !currentPrice.IsPositive() || !pos.BuyPrice.IsPositive() {
		return false
	}
	lossPct := pos.BuyPrice.Sub(currentPrice).Div(pos.BuyPrice)
	return lossPct.GreaterThanOrEqual(maxLossPct)
}
