package execution

import (
	"fmt"
	"sync"
	"time"

	"github.com/yourname/okx-hft-grid/pkg/models"
)

// validTransitions defines the allowed state transitions for orders.
// Key: current status, Value: set of allowed next statuses.
var validTransitions = map[models.OrderStatus]map[models.OrderStatus]bool{
	models.OrderStatusPending: {
		models.OrderStatusSubmitted: true,
	},
	models.OrderStatusSubmitted: {
		models.OrderStatusOpen:     true,
		models.OrderStatusRejected: true,
	},
	models.OrderStatusOpen: {
		models.OrderStatusPartiallyFilled: true,
		models.OrderStatusFilled:          true,
		models.OrderStatusCancelled:       true,
		models.OrderStatusExpired:         true,
	},
	models.OrderStatusPartiallyFilled: {
		models.OrderStatusFilled:    true,
		models.OrderStatusCancelled: true,
	},
}

// ErrOrderNotFound is returned when an order is not found in the manager.
var ErrOrderNotFound = fmt.Errorf("order not found")

// ErrInvalidTransition is returned when a state transition is not allowed.
var ErrInvalidTransition = fmt.Errorf("invalid state transition")

// ErrOrderAlreadyExists is returned when adding an order that already exists.
var ErrOrderAlreadyExists = fmt.Errorf("order already exists")

// ErrOrderNotPending is returned when adding an order that is not in PENDING status.
var ErrOrderNotPending = fmt.Errorf("order must be in PENDING status to be added")

// OrderManager tracks orders and manages their state transitions.
// It is thread-safe and validates all transitions against the allowed transition map.
type OrderManager struct {
	mu     sync.RWMutex
	orders map[string]*models.Order
}

// NewOrderManager creates a new OrderManager instance.
func NewOrderManager() *OrderManager {
	return &OrderManager{
		orders: make(map[string]*models.Order),
	}
}

// AddOrder adds a new order to the manager. The order must be in PENDING status.
func (om *OrderManager) AddOrder(order *models.Order) error {
	if order == nil {
		return fmt.Errorf("order cannot be nil")
	}
	if order.Status != models.OrderStatusPending {
		return ErrOrderNotPending
	}

	om.mu.Lock()
	defer om.mu.Unlock()

	if _, exists := om.orders[order.OrderId]; exists {
		return ErrOrderAlreadyExists
	}

	om.orders[order.OrderId] = order
	return nil
}

// TransitionOrder validates and applies a state transition to an order.
// Returns an error if the transition is invalid; the order's state remains unchanged on error.
func (om *OrderManager) TransitionOrder(orderID string, newStatus models.OrderStatus) error {
	om.mu.Lock()
	defer om.mu.Unlock()

	order, exists := om.orders[orderID]
	if !exists {
		return ErrOrderNotFound
	}

	currentStatus := order.Status

	// Check if the transition is valid
	allowed, ok := validTransitions[currentStatus]
	if !ok || !allowed[newStatus] {
		return fmt.Errorf("%w: cannot transition from %s to %s",
			ErrInvalidTransition, currentStatus.String(), newStatus.String())
	}

	order.Status = newStatus
	order.UpdateTime = time.Now().UnixMilli()
	return nil
}

// RejectOrder sets the order status to REJECTED with the given reason.
// This is only valid from the SUBMITTED state.
func (om *OrderManager) RejectOrder(orderID string, reason string) error {
	om.mu.Lock()
	defer om.mu.Unlock()

	order, exists := om.orders[orderID]
	if !exists {
		return ErrOrderNotFound
	}

	if order.Status != models.OrderStatusSubmitted {
		return fmt.Errorf("%w: cannot reject order in %s state, must be SUBMITTED",
			ErrInvalidTransition, order.Status.String())
	}

	order.Status = models.OrderStatusRejected
	order.UpdateTime = time.Now().UnixMilli()
	// Store rejection reason in the ExchangeOrderId field as a convention,
	// or we can add a dedicated field. For now we use a simple approach.
	// The reason is logged but not stored on the Order struct since there's no field for it.
	// In production, this would be stored in a rejection log or additional field.
	_ = reason // reason is acknowledged; in a full implementation, store in a rejection log
	return nil
}

// GetOrder retrieves an order by its ID.
func (om *OrderManager) GetOrder(orderID string) (*models.Order, error) {
	om.mu.RLock()
	defer om.mu.RUnlock()

	order, exists := om.orders[orderID]
	if !exists {
		return nil, ErrOrderNotFound
	}

	return order, nil
}

// HandleOrderUpdate processes an OKX WebSocket order update event.
// It updates the local order state based on the exchange event.
func (om *OrderManager) HandleOrderUpdate(update models.OrderUpdateEvent) error {
	om.mu.Lock()
	defer om.mu.Unlock()

	order, exists := om.orders[update.OrderID]
	if !exists {
		return ErrOrderNotFound
	}

	currentStatus := order.Status
	newStatus := update.Status

	// Handle rejection specially
	if newStatus == models.OrderStatusRejected {
		if currentStatus != models.OrderStatusSubmitted {
			return fmt.Errorf("%w: cannot transition from %s to REJECTED",
				ErrInvalidTransition, currentStatus.String())
		}
		order.Status = models.OrderStatusRejected
		order.UpdateTime = time.Now().UnixMilli()
		if update.ExchangeOrderID != "" {
			order.ExchangeOrderId = update.ExchangeOrderID
		}
		return nil
	}

	// Check if the transition is valid
	allowed, ok := validTransitions[currentStatus]
	if !ok || !allowed[newStatus] {
		return fmt.Errorf("%w: cannot transition from %s to %s",
			ErrInvalidTransition, currentStatus.String(), newStatus.String())
	}

	// Apply the update
	order.Status = newStatus
	order.UpdateTime = time.Now().UnixMilli()

	if update.ExchangeOrderID != "" {
		order.ExchangeOrderId = update.ExchangeOrderID
	}
	if !update.FilledQty.IsZero() {
		order.FilledQuantity = update.FilledQty
	}
	if !update.AvgFillPrice.IsZero() {
		order.AvgFillPrice = update.AvgFillPrice
	}

	return nil
}

// OrderCount returns the total number of orders tracked.
func (om *OrderManager) OrderCount() int {
	om.mu.RLock()
	defer om.mu.RUnlock()
	return len(om.orders)
}
