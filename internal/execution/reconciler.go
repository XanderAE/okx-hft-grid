package execution

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/yourname/okx-hft-grid/pkg/models"
)

// ExchangeQuerier defines the interface for querying the exchange for order and position state.
type ExchangeQuerier interface {
	// QueryOrders retrieves the current orders for a symbol from the exchange.
	QueryOrders(symbol string) ([]*models.Order, error)

	// QueryPositions retrieves the current positions for a symbol from the exchange.
	QueryPositions(symbol string) ([]*models.Position, error)
}

// Reconciler periodically reconciles local order/position state with the exchange.
// When discrepancies are found, the exchange state is treated as authoritative and
// the local state is updated to match. On failure, the local state is not modified
// and the reconciliation is retried on the next cycle.
type Reconciler struct {
	mu       sync.Mutex
	om       *OrderManager
	querier  ExchangeQuerier
	interval time.Duration
	stopCh   chan struct{}
	done     chan struct{}
	running  bool
	symbols  []string
}

// NewReconciler creates a new Reconciler that will reconcile local state with the exchange
// at the specified interval.
func NewReconciler(om *OrderManager, querier ExchangeQuerier, interval time.Duration) *Reconciler {
	return &Reconciler{
		om:       om,
		querier:  querier,
		interval: interval,
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// SetSymbols configures the symbols to reconcile.
func (r *Reconciler) SetSymbols(symbols []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.symbols = symbols
}

// Start begins the background reconciliation goroutine.
// It runs Reconcile for each configured symbol at the configured interval.
func (r *Reconciler) Start() {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return
	}
	r.running = true
	r.stopCh = make(chan struct{})
	r.done = make(chan struct{})
	r.mu.Unlock()

	go r.loop()
}

// Stop stops the background reconciliation goroutine and waits for it to finish.
func (r *Reconciler) Stop() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	r.running = false
	r.mu.Unlock()

	close(r.stopCh)
	<-r.done
}

// IsRunning returns whether the reconciler is currently running.
func (r *Reconciler) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

func (r *Reconciler) loop() {
	defer close(r.done)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.mu.Lock()
			symbols := make([]string, len(r.symbols))
			copy(symbols, r.symbols)
			r.mu.Unlock()

			for _, symbol := range symbols {
				if err := r.Reconcile(symbol); err != nil {
					log.Printf("[Reconciler] reconciliation failed for %s: %v (will retry next cycle)", symbol, err)
				}
			}
		}
	}
}

// Reconcile queries the exchange for the current state of orders and positions
// for the given symbol and updates local state to match when discrepancies are found.
// On failure (network/API error), it logs the error and does NOT modify local state.
func (r *Reconciler) Reconcile(symbol string) error {
	// Query exchange orders
	exchangeOrders, err := r.querier.QueryOrders(symbol)
	if err != nil {
		log.Printf("[Reconciler] failed to query orders for %s: %v", symbol, err)
		return fmt.Errorf("query orders failed for %s: %w", symbol, err)
	}

	// Query exchange positions
	exchangePositions, err := r.querier.QueryPositions(symbol)
	if err != nil {
		log.Printf("[Reconciler] failed to query positions for %s: %v", symbol, err)
		return fmt.Errorf("query positions failed for %s: %w", symbol, err)
	}

	// Reconcile orders: exchange is authoritative
	r.reconcileOrders(symbol, exchangeOrders)

	// Reconcile positions: exchange is authoritative
	r.reconcilePositions(symbol, exchangePositions)

	return nil
}

// reconcileOrders compares exchange orders with local orders and updates local state
// to match the exchange when discrepancies are found.
func (r *Reconciler) reconcileOrders(symbol string, exchangeOrders []*models.Order) {
	// Build a map of exchange orders by exchange order ID for efficient lookup
	exchangeMap := make(map[string]*models.Order, len(exchangeOrders))
	for _, o := range exchangeOrders {
		if o.ExchangeOrderId != "" {
			exchangeMap[o.ExchangeOrderId] = o
		}
	}

	// Check each local order against exchange state
	r.om.mu.Lock()
	defer r.om.mu.Unlock()

	for _, localOrder := range r.om.orders {
		if localOrder.Symbol != symbol {
			continue
		}

		// Skip orders without exchange IDs (not yet submitted)
		if localOrder.ExchangeOrderId == "" {
			continue
		}

		exchangeOrder, exists := exchangeMap[localOrder.ExchangeOrderId]
		if !exists {
			// Order exists locally but not on exchange - it may have been filled/cancelled
			// For terminal states, no action needed. For non-terminal states, mark as cancelled.
			if !isTerminalStatus(localOrder.Status) {
				log.Printf("[Reconciler] order %s (exchange: %s) not found on exchange, updating to CANCELLED",
					localOrder.OrderId, localOrder.ExchangeOrderId)
				localOrder.Status = models.OrderStatusCancelled
				localOrder.UpdateTime = time.Now().UnixMilli()
			}
			continue
		}

		// Compare statuses - exchange is authoritative
		if localOrder.Status != exchangeOrder.Status {
			log.Printf("[Reconciler] order %s status discrepancy: local=%s, exchange=%s; updating to exchange state",
				localOrder.OrderId, localOrder.Status.String(), exchangeOrder.Status.String())
			localOrder.Status = exchangeOrder.Status
			localOrder.UpdateTime = time.Now().UnixMilli()
		}

		// Update fill information if available
		if !exchangeOrder.FilledQuantity.IsZero() && !exchangeOrder.FilledQuantity.Equal(localOrder.FilledQuantity) {
			localOrder.FilledQuantity = exchangeOrder.FilledQuantity
			localOrder.UpdateTime = time.Now().UnixMilli()
		}
		if !exchangeOrder.AvgFillPrice.IsZero() && !exchangeOrder.AvgFillPrice.Equal(localOrder.AvgFillPrice) {
			localOrder.AvgFillPrice = exchangeOrder.AvgFillPrice
			localOrder.UpdateTime = time.Now().UnixMilli()
		}
	}
}

// reconcilePositions compares exchange positions with local state.
// For now, this logs discrepancies. A full implementation would update a position manager.
func (r *Reconciler) reconcilePositions(symbol string, exchangePositions []*models.Position) {
	// Position reconciliation: exchange is authoritative.
	// Log any discrepancies found. In a full implementation, this would update
	// a PositionManager to match the exchange state.
	for _, pos := range exchangePositions {
		if pos.Symbol == symbol {
			log.Printf("[Reconciler] exchange position for %s: qty=%s, side=%s, avgEntry=%s",
				pos.Symbol, pos.Quantity.String(), pos.Side.String(), pos.AvgEntryPrice.String())
		}
	}
}

// isTerminalStatus returns true if the order status is a final/terminal state.
func isTerminalStatus(status models.OrderStatus) bool {
	switch status {
	case models.OrderStatusFilled, models.OrderStatusCancelled,
		models.OrderStatusRejected, models.OrderStatusExpired, models.OrderStatusError:
		return true
	default:
		return false
	}
}
