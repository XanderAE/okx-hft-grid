package execution

import (
	"sync"

	"github.com/shopspring/decimal"
)

// MomentumFilter tracks recent prices per symbol and detects short-term downtrends.
type MomentumFilter struct {
	mu      sync.RWMutex
	history map[string][]decimal.Decimal
	maxLen  int
}

// NewMomentumFilter creates a filter tracking up to maxLen prices per symbol.
func NewMomentumFilter(maxLen int) *MomentumFilter {
	if maxLen < 4 {
		maxLen = 6
	}
	return &MomentumFilter{history: make(map[string][]decimal.Decimal), maxLen: maxLen}
}

// RecordPrice appends a price observation for the symbol.
func (f *MomentumFilter) RecordPrice(symbol string, price decimal.Decimal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.history[symbol]
	h = append(h, price)
	if len(h) > f.maxLen {
		h = h[len(h)-f.maxLen:]
	}
	f.history[symbol] = h
}

// IsDowntrend returns true if the last 3 consecutive price transitions were all declining.
func (f *MomentumFilter) IsDowntrend(symbol string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	h := f.history[symbol]
	if len(h) < 4 {
		return false
	}
	n := len(h)
	return h[n-4].GreaterThan(h[n-3]) && h[n-3].GreaterThan(h[n-2]) && h[n-2].GreaterThan(h[n-1])
}
