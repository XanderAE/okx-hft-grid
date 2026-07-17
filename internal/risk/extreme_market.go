package risk

import (
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// ExtremeMarketCallback defines the interface for handlers invoked when extreme market conditions are detected.
type ExtremeMarketCallback interface {
	// CancelGridOrders cancels all active grid orders.
	CancelGridOrders()
	// PauseMeanReversion pauses the mean reversion strategy.
	PauseMeanReversion()
	// SendAlert sends an alert with the specified reason.
	SendAlert(reason string)
}

// priceEntry stores a timestamped price data point.
type priceEntry struct {
	price     decimal.Decimal
	timestamp time.Time
}

// spreadEntry stores a timestamped spread data point.
type spreadEntry struct {
	spread    decimal.Decimal
	timestamp time.Time
}

// ExtremeMarketDetector detects extreme market conditions:
// - Price change > 5% within 1 minute
// - Spread > 3× rolling 5-minute average spread
// When detected, it triggers callbacks to cancel grid orders, pause mean reversion, and send alerts.
type ExtremeMarketDetector struct {
	mu sync.RWMutex

	// Per-symbol price history (ring buffer of 1-minute prices)
	priceHistory map[string][]priceEntry

	// Per-symbol spread history (5-minute rolling average)
	spreadHistory map[string][]spreadEntry

	// Configuration thresholds
	priceChangeThreshold decimal.Decimal // default 0.05 (5%)
	spreadMultiplier     decimal.Decimal // default 3.0
	priceWindow          time.Duration   // default 1 minute
	spreadWindow         time.Duration   // default 5 minutes

	// Registered callbacks
	callbacks []ExtremeMarketCallback
}

// NewExtremeMarketDetector creates a new ExtremeMarketDetector with default thresholds:
// - 5% price change within 1 minute
// - Spread exceeding 3× rolling 5-minute average
func NewExtremeMarketDetector() *ExtremeMarketDetector {
	return &ExtremeMarketDetector{
		priceHistory:         make(map[string][]priceEntry),
		spreadHistory:        make(map[string][]spreadEntry),
		priceChangeThreshold: decimal.NewFromFloat(0.05),
		spreadMultiplier:     decimal.NewFromInt(3),
		priceWindow:          time.Minute,
		spreadWindow:         5 * time.Minute,
		callbacks:            make([]ExtremeMarketCallback, 0),
	}
}

// RegisterCallback registers a callback handler that will be invoked when extreme market conditions are detected.
func (emd *ExtremeMarketDetector) RegisterCallback(cb ExtremeMarketCallback) {
	emd.mu.Lock()
	defer emd.mu.Unlock()
	emd.callbacks = append(emd.callbacks, cb)
}

// RecordPrice records a price data point for a symbol at the given timestamp.
// Old entries outside the 1-minute window are pruned.
func (emd *ExtremeMarketDetector) RecordPrice(symbol string, price decimal.Decimal, timestamp time.Time) {
	emd.mu.Lock()
	defer emd.mu.Unlock()

	entry := priceEntry{price: price, timestamp: timestamp}
	emd.priceHistory[symbol] = append(emd.priceHistory[symbol], entry)

	// Prune entries older than the price window
	emd.pruneOldPrices(symbol, timestamp)
}

// RecordSpread records a spread data point for a symbol at the given timestamp.
// Old entries outside the 5-minute window are pruned.
func (emd *ExtremeMarketDetector) RecordSpread(symbol string, spread decimal.Decimal, timestamp time.Time) {
	emd.mu.Lock()
	defer emd.mu.Unlock()

	entry := spreadEntry{spread: spread, timestamp: timestamp}
	emd.spreadHistory[symbol] = append(emd.spreadHistory[symbol], entry)

	// Prune entries older than the spread window
	emd.pruneOldSpreads(symbol, timestamp)
}

// CheckPriceChange checks if the price change for a symbol in the last 1 minute exceeds 5%.
// Returns true if extreme price movement is detected.
func (emd *ExtremeMarketDetector) CheckPriceChange(symbol string, currentPrice decimal.Decimal) bool {
	emd.mu.RLock()
	defer emd.mu.RUnlock()

	history, ok := emd.priceHistory[symbol]
	if !ok || len(history) == 0 {
		return false
	}

	// Get the oldest price in the window
	oldestPrice := history[0].price
	if oldestPrice.IsZero() {
		return false
	}

	// Calculate absolute percentage change: |current - oldest| / oldest
	change := currentPrice.Sub(oldestPrice).Abs()
	percentChange := change.Div(oldestPrice)

	return percentChange.GreaterThan(emd.priceChangeThreshold)
}

// CheckSpreadAnomaly checks if the current spread exceeds 3× the rolling 5-minute average spread.
// Returns true if spread anomaly is detected.
func (emd *ExtremeMarketDetector) CheckSpreadAnomaly(symbol string, currentSpread decimal.Decimal) bool {
	emd.mu.RLock()
	defer emd.mu.RUnlock()

	history, ok := emd.spreadHistory[symbol]
	if !ok || len(history) == 0 {
		return false
	}

	// Calculate rolling average spread
	sum := decimal.Zero
	for _, entry := range history {
		sum = sum.Add(entry.spread)
	}
	avgSpread := sum.Div(decimal.NewFromInt(int64(len(history))))

	if avgSpread.IsZero() {
		return false
	}

	// Check if current spread > 3× average
	threshold := avgSpread.Mul(emd.spreadMultiplier)
	return currentSpread.GreaterThan(threshold)
}

// Detect performs both price change and spread anomaly checks for a symbol.
// If either condition is triggered, it invokes all registered callbacks.
// Returns true if an extreme condition was detected.
func (emd *ExtremeMarketDetector) Detect(symbol string, currentPrice decimal.Decimal, currentSpread decimal.Decimal) bool {
	priceTriggered := emd.CheckPriceChange(symbol, currentPrice)
	spreadTriggered := emd.CheckSpreadAnomaly(symbol, currentSpread)

	if !priceTriggered && !spreadTriggered {
		return false
	}

	// Build reason string
	reason := "extreme market detected for " + symbol + ":"
	if priceTriggered {
		reason += " price change >5% in 1min"
	}
	if spreadTriggered {
		if priceTriggered {
			reason += ","
		}
		reason += " spread >3x rolling average"
	}

	// Invoke callbacks
	emd.mu.RLock()
	callbacks := make([]ExtremeMarketCallback, len(emd.callbacks))
	copy(callbacks, emd.callbacks)
	emd.mu.RUnlock()

	for _, cb := range callbacks {
		cb.CancelGridOrders()
		cb.PauseMeanReversion()
		cb.SendAlert(reason)
	}

	return true
}

// pruneOldPrices removes price entries older than the price window.
// Must be called while holding the write lock.
func (emd *ExtremeMarketDetector) pruneOldPrices(symbol string, now time.Time) {
	history := emd.priceHistory[symbol]
	cutoff := now.Add(-emd.priceWindow)

	startIdx := 0
	for startIdx < len(history) && history[startIdx].timestamp.Before(cutoff) {
		startIdx++
	}

	if startIdx > 0 {
		emd.priceHistory[symbol] = history[startIdx:]
	}
}

// pruneOldSpreads removes spread entries older than the spread window.
// Must be called while holding the write lock.
func (emd *ExtremeMarketDetector) pruneOldSpreads(symbol string, now time.Time) {
	history := emd.spreadHistory[symbol]
	cutoff := now.Add(-emd.spreadWindow)

	startIdx := 0
	for startIdx < len(history) && history[startIdx].timestamp.Before(cutoff) {
		startIdx++
	}

	if startIdx > 0 {
		emd.spreadHistory[symbol] = history[startIdx:]
	}
}

// GetPriceChangeThreshold returns the configured price change threshold (e.g. 0.05 for 5%).
func (emd *ExtremeMarketDetector) GetPriceChangeThreshold() decimal.Decimal {
	emd.mu.RLock()
	defer emd.mu.RUnlock()
	return emd.priceChangeThreshold
}

// GetSpreadMultiplier returns the configured spread multiplier (e.g. 3 for 3×).
func (emd *ExtremeMarketDetector) GetSpreadMultiplier() decimal.Decimal {
	emd.mu.RLock()
	defer emd.mu.RUnlock()
	return emd.spreadMultiplier
}
