package orderbook

import (
	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// PriceLevel represents a single price level in the order book.
type PriceLevel struct {
	Price    decimal.Decimal // Price at this level
	Quantity decimal.Decimal // Total quantity at this level
}

// OrderBookSnapshot represents a full order book snapshot.
type OrderBookSnapshot struct {
	Symbol     string       // Trading pair
	Bids       []PriceLevel // Bid levels, sorted descending by price
	Asks       []PriceLevel // Ask levels, sorted ascending by price
	SequenceID int64        // Snapshot sequence ID
	Timestamp  int64        // Snapshot timestamp
}

// OrderBookDelta represents an incremental order book update.
type OrderBookDelta struct {
	Symbol     string       // Trading pair
	Bids       []PriceLevel // Bid level updates (quantity=0 means remove)
	Asks       []PriceLevel // Ask level updates (quantity=0 means remove)
	SequenceID int64        // Delta sequence ID
	Timestamp  int64        // Delta timestamp
}

// OrderBookManager defines the interface for maintaining and querying local order books.
type OrderBookManager interface {
	// UpdateFromSnapshot replaces the local order book for the symbol with the given snapshot.
	UpdateFromSnapshot(symbol string, snapshot *OrderBookSnapshot) error

	// UpdateIncremental applies an incremental delta update to the existing local order book.
	// Returns an error if sequence gap is detected (triggering a resync).
	UpdateIncremental(symbol string, delta *OrderBookDelta) error

	// GetBestBid returns the best (highest) bid price level for the symbol.
	// Returns an error if the bid side is empty.
	GetBestBid(symbol string) (*PriceLevel, error)

	// GetBestAsk returns the best (lowest) ask price level for the symbol.
	// Returns an error if the ask side is empty.
	GetBestAsk(symbol string) (*PriceLevel, error)

	// GetMidPrice returns (bestBid + bestAsk) / 2 with up to 8 decimal places.
	// Returns an error if either side is empty.
	GetMidPrice(symbol string) (decimal.Decimal, error)

	// GetSpread returns bestAsk - bestBid as a non-negative decimal with up to 8 decimal places.
	// Returns an error if either side is empty.
	GetSpread(symbol string) (decimal.Decimal, error)

	// GetVWAP calculates the volume-weighted average price for a given side and quantity.
	// Walks from the best price level, accumulating volume until the requested quantity is filled.
	// Returns an error if insufficient liquidity (includes max fillable quantity in error).
	GetVWAP(symbol string, side models.Side, quantity decimal.Decimal) (decimal.Decimal, error)

	// GetDepth returns the top N price levels for the given side.
	GetDepth(symbol string, side models.Side, levels int) ([]PriceLevel, error)

	// RequestResync triggers a full order book resynchronization for the symbol.
	// During resync, all incremental updates for that symbol are discarded.
	RequestResync(symbol string) error
}
