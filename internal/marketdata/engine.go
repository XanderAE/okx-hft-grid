package marketdata

import (
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// MarketDataEngine defines the interface for receiving, validating, and distributing
// real-time market data from OKX via WebSocket.
type MarketDataEngine interface {
	// Connect establishes WebSocket connection and subscribes to channels for the given symbols.
	Connect(symbols []string) error

	// Disconnect gracefully closes the WebSocket connection.
	Disconnect() error

	// Subscribe adds a subscription for a specific symbol and channel (e.g. "ticker", "depth", "trades").
	Subscribe(symbol string, channel string) error

	// Unsubscribe removes a subscription for a specific symbol and channel.
	Unsubscribe(symbol string, channel string) error

	// GetLatestTick returns the most recent valid tick data for the given symbol.
	// Returns an error if no data is available or data is stale.
	GetLatestTick(symbol string) (*models.TickData, error)

	// RegisterCallback registers an event callback handler for a specific event type.
	RegisterCallback(eventType models.EventType, handler models.MarketEventCallback)

	// UnregisterCallback removes a previously registered callback for the event type.
	UnregisterCallback(eventType models.EventType, handler models.MarketEventCallback)

	// IsConnected returns whether the WebSocket connection is currently active.
	IsConnected() bool

	// IsDataStale returns whether market data for the given symbol is stale.
	IsDataStale(symbol string) bool
}
