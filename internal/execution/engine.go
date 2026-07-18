package execution

import (
	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// OrderRequest represents a request to place an order.
type OrderRequest struct {
	Symbol        string           // Trading pair
	Side          models.Side      // BUY or SELL
	OrderType     models.OrderType // LIMIT, MARKET, POST_ONLY
	Price         decimal.Decimal  // Order price
	Quantity      decimal.Decimal  // Order quantity
	StrategyID    string           // Associated strategy ID
	GridLevel     int              // Grid level index (-1 if not grid)
	ClientOrderID string           // Deterministic client order ID (caller-supplied)
}

// OrderResult represents the result of placing an order.
type OrderResult struct {
	Success         bool               // Whether the order was placed successfully
	OrderID         string             // Local order ID (if successful)
	ExchangeOrderID string             // Exchange-assigned order ID (if successful)
	Error           string             // Error reason (if failed)
	Status          models.OrderStatus // Initial order status
}

// CancelResult represents the result of canceling an order.
type CancelResult struct {
	Success bool   // Whether the cancellation succeeded
	OrderID string // The order ID that was canceled
	Error   string // Error reason (if failed)
}

// OrderExecutionEngine defines the interface for managing orders and interacting with the exchange.
type OrderExecutionEngine interface {
	// PlaceOrder submits a single order to the exchange.
	PlaceOrder(order *OrderRequest) (*OrderResult, error)

	// CancelOrder cancels a single order by its local order ID.
	CancelOrder(orderID string) (*CancelResult, error)

	// BatchPlaceOrders submits multiple orders (up to 20 per batch).
	// Returns per-order results indicating success or failure.
	BatchPlaceOrders(orders []*OrderRequest) ([]*OrderResult, error)

	// BatchCancelOrders cancels multiple orders by their local order IDs.
	// Returns per-order cancellation results.
	BatchCancelOrders(orderIDs []string) ([]*CancelResult, error)

	// GetOpenOrders returns all open orders for the given symbol.
	GetOpenOrders(symbol string) ([]*models.Order, error)

	// GetOrderStatus returns the current status of an order.
	GetOrderStatus(orderID string) (models.OrderStatus, error)

	// OnOrderUpdate processes an order update event from the exchange WebSocket.
	OnOrderUpdate(update models.OrderUpdateEvent)

	// GetPosition returns the current position for the given symbol.
	GetPosition(symbol string) (*models.Position, error)

	// GetAllOpenOrders returns all open orders across all symbols.
	GetAllOpenOrders() ([]*models.Order, error)
}
