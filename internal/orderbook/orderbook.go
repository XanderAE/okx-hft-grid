package orderbook

import (
	"fmt"
	"sort"
	"sync"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// symbolBook holds the order book state for a single symbol.
type symbolBook struct {
	bids       []PriceLevel // sorted descending by price
	asks       []PriceLevel // sorted ascending by price
	sequenceID int64
	resyncing  bool // when true, discard all incremental updates
}

// LocalOrderBook implements the OrderBookManager interface using sorted slices
// with binary search for efficient insert/update/delete operations.
type LocalOrderBook struct {
	mu    sync.RWMutex
	books map[string]*symbolBook

	// ResyncRequested is set to the symbol name when a resync is triggered.
	// External consumers can watch this to issue full snapshot requests.
	ResyncCh chan string
}

// NewLocalOrderBook creates a new LocalOrderBook instance.
func NewLocalOrderBook() *LocalOrderBook {
	return &LocalOrderBook{
		books:    make(map[string]*symbolBook),
		ResyncCh: make(chan string, 100),
	}
}

// getOrCreateBook returns the book for a symbol, creating it if needed.
func (ob *LocalOrderBook) getOrCreateBook(symbol string) *symbolBook {
	book, exists := ob.books[symbol]
	if !exists {
		book = &symbolBook{}
		ob.books[symbol] = book
	}
	return book
}

// UpdateFromSnapshot replaces the local order book for the symbol with the given snapshot.
func (ob *LocalOrderBook) UpdateFromSnapshot(symbol string, snapshot *OrderBookSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("snapshot is nil")
	}

	ob.mu.Lock()
	defer ob.mu.Unlock()

	book := ob.getOrCreateBook(symbol)

	// Replace bids - ensure descending order by price
	book.bids = make([]PriceLevel, len(snapshot.Bids))
	copy(book.bids, snapshot.Bids)
	sort.Slice(book.bids, func(i, j int) bool {
		return book.bids[i].Price.GreaterThan(book.bids[j].Price)
	})

	// Replace asks - ensure ascending order by price
	book.asks = make([]PriceLevel, len(snapshot.Asks))
	copy(book.asks, snapshot.Asks)
	sort.Slice(book.asks, func(i, j int) bool {
		return book.asks[i].Price.LessThan(book.asks[j].Price)
	})

	book.sequenceID = snapshot.SequenceID
	book.resyncing = false

	return nil
}

// UpdateIncremental applies an incremental delta update to the existing local order book.
// Returns an error if a sequence gap is detected (triggering a resync).
func (ob *LocalOrderBook) UpdateIncremental(symbol string, delta *OrderBookDelta) error {
	if delta == nil {
		return fmt.Errorf("delta is nil")
	}

	ob.mu.Lock()
	defer ob.mu.Unlock()

	book, exists := ob.books[symbol]
	if !exists {
		// No existing book, request a full snapshot
		ob.requestResyncLocked(symbol)
		return fmt.Errorf("no existing order book for symbol %s, requesting full snapshot", symbol)
	}

	// If resyncing, discard all incremental updates (Req 2.10)
	if book.resyncing {
		return fmt.Errorf("symbol %s is resyncing, discarding incremental update", symbol)
	}

	// Sequence ID gap detection (Req 2.9)
	// The delta sequenceID must be exactly 1 greater than the last processed sequence
	expectedSeq := book.sequenceID + 1
	if delta.SequenceID != expectedSeq {
		ob.requestResyncLocked(symbol)
		return fmt.Errorf("sequence gap detected for %s: expected %d, got %d", symbol, expectedSeq, delta.SequenceID)
	}

	// Apply bid updates
	for _, level := range delta.Bids {
		book.bids = applyDeltaDescending(book.bids, level)
	}

	// Apply ask updates
	for _, level := range delta.Asks {
		book.asks = applyDeltaAscending(book.asks, level)
	}

	book.sequenceID = delta.SequenceID

	return nil
}

// GetBestBid returns the best (highest) bid price level for the symbol.
func (ob *LocalOrderBook) GetBestBid(symbol string) (*PriceLevel, error) {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	book, exists := ob.books[symbol]
	if !exists || len(book.bids) == 0 {
		return nil, fmt.Errorf("no bids available for symbol %s", symbol)
	}

	result := book.bids[0]
	return &result, nil
}

// GetBestAsk returns the best (lowest) ask price level for the symbol.
func (ob *LocalOrderBook) GetBestAsk(symbol string) (*PriceLevel, error) {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	book, exists := ob.books[symbol]
	if !exists || len(book.asks) == 0 {
		return nil, fmt.Errorf("no asks available for symbol %s", symbol)
	}

	result := book.asks[0]
	return &result, nil
}

// GetMidPrice returns (bestBid + bestAsk) / 2 with up to 8 decimal places.
func (ob *LocalOrderBook) GetMidPrice(symbol string) (decimal.Decimal, error) {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	book, exists := ob.books[symbol]
	if !exists {
		return decimal.Zero, fmt.Errorf("no order book for symbol %s", symbol)
	}
	if len(book.bids) == 0 || len(book.asks) == 0 {
		return decimal.Zero, fmt.Errorf("insufficient book depth for symbol %s: bids=%d, asks=%d",
			symbol, len(book.bids), len(book.asks))
	}

	two := decimal.NewFromInt(2)
	midPrice := book.bids[0].Price.Add(book.asks[0].Price).Div(two)
	return midPrice.Round(8), nil
}

// GetSpread returns bestAsk - bestBid as a non-negative decimal with up to 8 decimal places.
func (ob *LocalOrderBook) GetSpread(symbol string) (decimal.Decimal, error) {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	book, exists := ob.books[symbol]
	if !exists {
		return decimal.Zero, fmt.Errorf("no order book for symbol %s", symbol)
	}
	if len(book.bids) == 0 || len(book.asks) == 0 {
		return decimal.Zero, fmt.Errorf("insufficient book depth for symbol %s: bids=%d, asks=%d",
			symbol, len(book.bids), len(book.asks))
	}

	spread := book.asks[0].Price.Sub(book.bids[0].Price)
	if spread.IsNegative() {
		spread = decimal.Zero
	}
	return spread.Round(8), nil
}

// GetVWAP calculates the volume-weighted average price for a given side and quantity.
func (ob *LocalOrderBook) GetVWAP(symbol string, side models.Side, quantity decimal.Decimal) (decimal.Decimal, error) {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	book, exists := ob.books[symbol]
	if !exists {
		return decimal.Zero, fmt.Errorf("no order book for symbol %s", symbol)
	}

	var levels []PriceLevel
	switch side {
	case models.SideBuy:
		levels = book.asks // buying consumes asks
	case models.SideSell:
		levels = book.bids // selling consumes bids
	default:
		return decimal.Zero, fmt.Errorf("invalid side: %s", side)
	}

	if len(levels) == 0 {
		return decimal.Zero, fmt.Errorf("no liquidity on %s side for symbol %s", side, symbol)
	}

	remaining := quantity
	totalNotional := decimal.Zero
	totalFilled := decimal.Zero

	for _, level := range levels {
		if remaining.IsZero() || remaining.IsNegative() {
			break
		}

		fillQty := decimal.Min(remaining, level.Quantity)
		totalNotional = totalNotional.Add(fillQty.Mul(level.Price))
		totalFilled = totalFilled.Add(fillQty)
		remaining = remaining.Sub(fillQty)
	}

	if remaining.IsPositive() {
		return decimal.Zero, fmt.Errorf("insufficient liquidity for symbol %s side %s: requested %s, max fillable %s",
			symbol, side, quantity.String(), totalFilled.String())
	}

	vwap := totalNotional.Div(totalFilled)
	return vwap.Round(8), nil
}

// GetDepth returns the top N price levels for the given side.
func (ob *LocalOrderBook) GetDepth(symbol string, side models.Side, levels int) ([]PriceLevel, error) {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	book, exists := ob.books[symbol]
	if !exists {
		return nil, fmt.Errorf("no order book for symbol %s", symbol)
	}

	var source []PriceLevel
	switch side {
	case models.SideBuy:
		source = book.bids
	case models.SideSell:
		source = book.asks
	default:
		return nil, fmt.Errorf("invalid side: %s", side)
	}

	if levels > len(source) {
		levels = len(source)
	}

	result := make([]PriceLevel, levels)
	copy(result, source[:levels])
	return result, nil
}

// RequestResync triggers a full order book resynchronization for the symbol.
// During resync, all incremental updates for that symbol are discarded.
func (ob *LocalOrderBook) RequestResync(symbol string) error {
	ob.mu.Lock()
	defer ob.mu.Unlock()

	ob.requestResyncLocked(symbol)
	return nil
}

// requestResyncLocked triggers resync while already holding the lock.
func (ob *LocalOrderBook) requestResyncLocked(symbol string) {
	book := ob.getOrCreateBook(symbol)
	book.bids = nil
	book.asks = nil
	book.resyncing = true

	// Non-blocking send to resync channel
	select {
	case ob.ResyncCh <- symbol:
	default:
	}
}

// IsResyncing returns whether the given symbol is currently in resync mode.
func (ob *LocalOrderBook) IsResyncing(symbol string) bool {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	book, exists := ob.books[symbol]
	if !exists {
		return false
	}
	return book.resyncing
}

// GetSequenceID returns the current sequence ID for the symbol's book.
func (ob *LocalOrderBook) GetSequenceID(symbol string) (int64, bool) {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	book, exists := ob.books[symbol]
	if !exists {
		return 0, false
	}
	return book.sequenceID, true
}

// --- Internal helpers ---

// applyDeltaDescending applies a price level update to a descending-sorted slice (bids).
// If quantity is zero, the level is removed. Otherwise, it's inserted or updated.
func applyDeltaDescending(levels []PriceLevel, update PriceLevel) []PriceLevel {
	// Binary search for the position where this price should be
	idx := sort.Search(len(levels), func(i int) bool {
		// Descending: we want to find the first element that is <= update.Price
		return levels[i].Price.LessThanOrEqual(update.Price)
	})

	// Check if we found an exact match
	if idx < len(levels) && levels[idx].Price.Equal(update.Price) {
		if update.Quantity.IsZero() {
			// Remove the level
			levels = append(levels[:idx], levels[idx+1:]...)
		} else {
			// Update quantity
			levels[idx].Quantity = update.Quantity
		}
	} else {
		// Not found at this position
		if !update.Quantity.IsZero() {
			// Insert new level at idx
			levels = append(levels, PriceLevel{})
			copy(levels[idx+1:], levels[idx:])
			levels[idx] = update
		}
		// If quantity is zero and level doesn't exist, no-op
	}

	return levels
}

// applyDeltaAscending applies a price level update to an ascending-sorted slice (asks).
// If quantity is zero, the level is removed. Otherwise, it's inserted or updated.
func applyDeltaAscending(levels []PriceLevel, update PriceLevel) []PriceLevel {
	// Binary search for the position where this price should be
	idx := sort.Search(len(levels), func(i int) bool {
		// Ascending: we want to find the first element that is >= update.Price
		return levels[i].Price.GreaterThanOrEqual(update.Price)
	})

	// Check if we found an exact match
	if idx < len(levels) && levels[idx].Price.Equal(update.Price) {
		if update.Quantity.IsZero() {
			// Remove the level
			levels = append(levels[:idx], levels[idx+1:]...)
		} else {
			// Update quantity
			levels[idx].Quantity = update.Quantity
		}
	} else {
		// Not found at this position
		if !update.Quantity.IsZero() {
			// Insert new level at idx
			levels = append(levels, PriceLevel{})
			copy(levels[idx+1:], levels[idx:])
			levels[idx] = update
		}
		// If quantity is zero and level doesn't exist, no-op
	}

	return levels
}
