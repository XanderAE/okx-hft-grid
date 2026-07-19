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
// - < 1h: buyPrice + 0.3% (initial profit target)
// - 1h-6h: buyPrice + 0.25% (reduced expectation)
// - 6h-12h: buyPrice + 0.2% (fee floor)
// - > 12h: decimal.Zero (signal to use market price / force exit)
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
	case elapsed < 1*time.Hour:
		return buyPrice.Mul(decimal.NewFromFloat(1.003)), true // +0.3%
	case elapsed < 6*time.Hour:
		return buyPrice.Mul(decimal.NewFromFloat(1.0025)), true // +0.25%
	case elapsed < 12*time.Hour:
		return buyPrice.Mul(decimal.NewFromFloat(1.002)), true // +0.2%
	default:
		return decimal.Zero, true // market sell signal
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
