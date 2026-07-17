package orderbook

import (
	"log"
	"sync"
	"time"
)

// AnomalyDetector monitors order book state after each update and detects anomalies.
// It checks for crossed book conditions and sudden depth changes, triggering resync when needed.
type AnomalyDetector struct {
	mu sync.Mutex

	// previousDepth tracks the total number of levels (bids + asks) per symbol
	// before each update, used to detect > 50% depth changes.
	previousDepth map[string]int

	// ob is the order book instance to monitor and trigger resync on.
	ob *LocalOrderBook

	// resyncDeadline specifies the maximum duration within which a resync must be requested
	// after anomaly detection. Default is 1 second as per requirements.
	resyncDeadline time.Duration
}

// NewAnomalyDetector creates a new AnomalyDetector that monitors the given LocalOrderBook.
func NewAnomalyDetector(ob *LocalOrderBook) *AnomalyDetector {
	return &AnomalyDetector{
		previousDepth:  make(map[string]int),
		ob:             ob,
		resyncDeadline: 1 * time.Second,
	}
}

// CheckCrossedBook detects if bestBid >= bestAsk for the given symbol.
// If a crossed book is detected, it logs a warning and requests a full resync.
// Returns true if an anomaly was detected.
func (ad *AnomalyDetector) CheckCrossedBook(symbol string) bool {
	ad.ob.mu.RLock()
	book, exists := ad.ob.books[symbol]
	if !exists {
		ad.ob.mu.RUnlock()
		return false
	}

	// If already resyncing, no need to check
	if book.resyncing {
		ad.ob.mu.RUnlock()
		return false
	}

	if len(book.bids) == 0 || len(book.asks) == 0 {
		ad.ob.mu.RUnlock()
		return false
	}

	bestBid := book.bids[0].Price
	bestAsk := book.asks[0].Price
	ad.ob.mu.RUnlock()

	if bestBid.GreaterThanOrEqual(bestAsk) {
		log.Printf("[ANOMALY] Crossed book detected for %s: bestBid=%s >= bestAsk=%s, requesting resync",
			symbol, bestBid.String(), bestAsk.String())
		ad.ob.RequestResync(symbol)
		return true
	}

	return false
}

// CheckDepthChange detects if the depth (total number of levels) changed by more than 50%
// in a single update. If detected, it logs a warning and requests a full resync.
// Returns true if an anomaly was detected.
//
// previousDepth is the total number of price levels (bids + asks) before the update.
// currentDepth is the total number of price levels (bids + asks) after the update.
func (ad *AnomalyDetector) CheckDepthChange(symbol string, previousDepth, currentDepth int) bool {
	// If already resyncing, no need to check
	if ad.ob.IsResyncing(symbol) {
		return false
	}

	// If there was no previous depth (first update), skip check
	if previousDepth == 0 {
		return false
	}

	// Calculate change ratio: |currentDepth - previousDepth| / previousDepth > 0.5
	diff := currentDepth - previousDepth
	if diff < 0 {
		diff = -diff
	}

	// Change exceeds 50% of previous depth
	if diff*2 > previousDepth {
		log.Printf("[ANOMALY] Depth change >50%% detected for %s: previous=%d, current=%d, requesting resync",
			symbol, previousDepth, currentDepth)
		ad.ob.RequestResync(symbol)
		return true
	}

	return false
}

// RecordDepth records the current depth for the symbol before an update is applied.
// This should be called before UpdateIncremental to capture the pre-update state.
func (ad *AnomalyDetector) RecordDepth(symbol string) {
	ad.mu.Lock()
	defer ad.mu.Unlock()

	ad.ob.mu.RLock()
	defer ad.ob.mu.RUnlock()

	book, exists := ad.ob.books[symbol]
	if !exists {
		ad.previousDepth[symbol] = 0
		return
	}

	ad.previousDepth[symbol] = len(book.bids) + len(book.asks)
}

// GetPreviousDepth returns the recorded previous depth for a symbol.
func (ad *AnomalyDetector) GetPreviousDepth(symbol string) int {
	ad.mu.Lock()
	defer ad.mu.Unlock()
	return ad.previousDepth[symbol]
}

// GetCurrentDepth returns the current total depth (bids + asks) for a symbol.
func (ad *AnomalyDetector) GetCurrentDepth(symbol string) int {
	ad.ob.mu.RLock()
	defer ad.ob.mu.RUnlock()

	book, exists := ad.ob.books[symbol]
	if !exists {
		return 0
	}

	return len(book.bids) + len(book.asks)
}

// CheckAfterUpdate runs all anomaly checks after an incremental update is applied.
// It checks for crossed book and depth changes, triggering resync if anomalies are found.
// Returns true if any anomaly was detected.
func (ad *AnomalyDetector) CheckAfterUpdate(symbol string) bool {
	// Check crossed book
	if ad.CheckCrossedBook(symbol) {
		return true
	}

	// Check depth change
	ad.mu.Lock()
	prevDepth := ad.previousDepth[symbol]
	ad.mu.Unlock()

	currentDepth := ad.GetCurrentDepth(symbol)
	if ad.CheckDepthChange(symbol, prevDepth, currentDepth) {
		return true
	}

	// Update recorded depth for next check
	ad.mu.Lock()
	ad.previousDepth[symbol] = currentDepth
	ad.mu.Unlock()

	return false
}
