package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// EventType represents the type of system event.
type EventType int

const (
	EventMarketData    EventType = iota // Market data update
	EventOrderUpdate                    // Order status changed
	EventFill                           // Order filled (partial or full)
	EventDataInvalid                    // Invalid data received
	EventDataStale                      // Market data is stale
	EventRiskBreach                     // Risk limit breached
	EventEmergencyStop                  // Emergency stop triggered
)

// MarketEvent represents a market data event dispatched to downstream components.
type MarketEvent struct {
	Symbol    string          // Trading pair, e.g. "BTC-USDT"
	Timestamp time.Time       // Event timestamp
	LastPrice decimal.Decimal // Last traded price
	BestBid   decimal.Decimal // Best bid price
	BestAsk   decimal.Decimal // Best ask price
	BidSize   decimal.Decimal // Best bid size
	AskSize   decimal.Decimal // Best ask size
	Volume24h decimal.Decimal // 24h volume
	SeqID     int64           // Sequence ID for ordering
}

// FillEvent represents a trade fill event.
type FillEvent struct {
	OrderID            string          // Local order ID
	ClientOrderID      string          // Stable bot-assigned client order ID
	ExchangeOrderID    string          // Exchange-assigned order ID
	ExchangeFillID     string          // Exchange-assigned stable fill ID
	Symbol             string          // Trading pair
	Side               Side            // BUY or SELL
	Price              decimal.Decimal // Fill price
	Quantity           decimal.Decimal // Incremental fill quantity
	CumulativeQuantity decimal.Decimal // Exchange-reported cumulative filled quantity
	Fee                decimal.Decimal // Trading fee
	FeeCurrency        string          // Fee currency
	Timestamp          time.Time       // Fill timestamp
	StrategyID         string          // Associated strategy ID
	GridLevel          int             // Grid level index (-1 if not grid)
	IsMaker            bool            // Whether this was a maker fill
}

// OrderUpdateEvent represents an order status update from the exchange.
type OrderUpdateEvent struct {
	OrderID         string          // Local order ID
	ExchangeOrderID string          // Exchange-assigned order ID
	Symbol          string          // Trading pair
	Side            Side            // BUY or SELL
	OrderType       OrderType       // Order type
	Price           decimal.Decimal // Order price
	Quantity        decimal.Decimal // Original quantity
	FilledQty       decimal.Decimal // Filled quantity so far
	AvgFillPrice    decimal.Decimal // Average fill price
	Status          OrderStatus     // New order status
	RejectReason    string          // Reason if rejected
	Timestamp       time.Time       // Update timestamp
	StrategyID      string          // Associated strategy ID
}

// MarketEventCallback is a callback function type for market events.
type MarketEventCallback func(event MarketEvent)

// FillEventCallback is a callback function type for fill events.
type FillEventCallback func(event FillEvent)

// OrderUpdateCallback is a callback function type for order update events.
type OrderUpdateCallback func(event OrderUpdateEvent)

// EventChannel types for channel-based communication between components.
type (
	MarketEventChan      chan MarketEvent
	FillEventChan        chan FillEvent
	OrderUpdateEventChan chan OrderUpdateEvent
)

// NewMarketEventChan creates a buffered market event channel.
func NewMarketEventChan(bufSize int) MarketEventChan {
	return make(MarketEventChan, bufSize)
}

// NewFillEventChan creates a buffered fill event channel.
func NewFillEventChan(bufSize int) FillEventChan {
	return make(FillEventChan, bufSize)
}

// NewOrderUpdateEventChan creates a buffered order update event channel.
func NewOrderUpdateEventChan(bufSize int) OrderUpdateEventChan {
	return make(OrderUpdateEventChan, bufSize)
}
