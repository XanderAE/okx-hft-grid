package marketdata

import (
	"sync"
	"sync/atomic"

	"github.com/yourname/okx-hft-grid/pkg/models"
)

// EventHandler is a generic handler function that processes a MarketEvent.
type EventHandler func(event models.MarketEvent)

// FillHandler processes fill events.
type FillHandler func(event models.FillEvent)

// OrderUpdateHandler processes order update events.
type OrderUpdateHandler func(event models.OrderUpdateEvent)

// handlerEntry wraps a handler with an ID for unregistration.
type handlerEntry struct {
	id      uint64
	handler EventHandler
}

// fillHandlerEntry wraps a fill handler with an ID.
type fillHandlerEntry struct {
	id      uint64
	handler FillHandler
}

// orderUpdateHandlerEntry wraps an order update handler with an ID.
type orderUpdateHandlerEntry struct {
	id      uint64
	handler OrderUpdateHandler
}

// Dispatcher distributes events to registered handlers via Go channels.
// It uses non-blocking sends to avoid slow consumers blocking the system.
// Design targets < 50μs dispatch latency for all event types.
type Dispatcher struct {
	// Market event handlers keyed by EventType
	marketHandlers map[models.EventType][]handlerEntry
	marketMu       sync.RWMutex

	// Fill event handlers
	fillHandlers []fillHandlerEntry
	fillMu       sync.RWMutex

	// Order update handlers
	orderUpdateHandlers []orderUpdateHandlerEntry
	orderUpdateMu       sync.RWMutex

	// Handler ID counter for unique identification
	nextID atomic.Uint64

	// Channels for async dispatch (buffered to avoid blocking producers)
	marketCh      chan marketDispatchItem
	fillCh        chan models.FillEvent
	orderUpdateCh chan models.OrderUpdateEvent

	// Lifecycle
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// marketDispatchItem carries the event type and data for dispatch.
type marketDispatchItem struct {
	eventType models.EventType
	event     models.MarketEvent
}

// DispatcherConfig holds configuration for the Dispatcher.
type DispatcherConfig struct {
	// MarketChannelSize is the buffer size for the market event channel.
	// Default: 4096
	MarketChannelSize int

	// FillChannelSize is the buffer size for the fill event channel.
	// Default: 1024
	FillChannelSize int

	// OrderUpdateChannelSize is the buffer size for the order update event channel.
	// Default: 1024
	OrderUpdateChannelSize int
}

// DefaultDispatcherConfig returns a DispatcherConfig with sensible defaults.
func DefaultDispatcherConfig() DispatcherConfig {
	return DispatcherConfig{
		MarketChannelSize:      4096,
		FillChannelSize:        1024,
		OrderUpdateChannelSize: 1024,
	}
}

// NewDispatcher creates a new Dispatcher with the given configuration.
func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	if cfg.MarketChannelSize <= 0 {
		cfg.MarketChannelSize = 4096
	}
	if cfg.FillChannelSize <= 0 {
		cfg.FillChannelSize = 1024
	}
	if cfg.OrderUpdateChannelSize <= 0 {
		cfg.OrderUpdateChannelSize = 1024
	}

	d := &Dispatcher{
		marketHandlers: make(map[models.EventType][]handlerEntry),
		marketCh:       make(chan marketDispatchItem, cfg.MarketChannelSize),
		fillCh:         make(chan models.FillEvent, cfg.FillChannelSize),
		orderUpdateCh:  make(chan models.OrderUpdateEvent, cfg.OrderUpdateChannelSize),
		stopCh:         make(chan struct{}),
	}

	return d
}

// Start begins the dispatcher's event processing goroutines.
func (d *Dispatcher) Start() {
	d.wg.Add(3)
	go d.processMarketEvents()
	go d.processFillEvents()
	go d.processOrderUpdateEvents()
}

// Stop halts all dispatcher goroutines and waits for them to finish.
func (d *Dispatcher) Stop() {
	close(d.stopCh)
	d.wg.Wait()
}

// RegisterMarketHandler registers a handler for a specific market event type.
// Returns a handler ID that can be used to unregister.
func (d *Dispatcher) RegisterMarketHandler(eventType models.EventType, handler EventHandler) uint64 {
	id := d.nextID.Add(1)
	d.marketMu.Lock()
	d.marketHandlers[eventType] = append(d.marketHandlers[eventType], handlerEntry{
		id:      id,
		handler: handler,
	})
	d.marketMu.Unlock()
	return id
}

// UnregisterMarketHandler removes a previously registered market handler by ID.
func (d *Dispatcher) UnregisterMarketHandler(eventType models.EventType, handlerID uint64) {
	d.marketMu.Lock()
	handlers := d.marketHandlers[eventType]
	for i, h := range handlers {
		if h.id == handlerID {
			d.marketHandlers[eventType] = append(handlers[:i], handlers[i+1:]...)
			break
		}
	}
	d.marketMu.Unlock()
}

// RegisterFillHandler registers a handler for fill events.
// Returns a handler ID that can be used to unregister.
func (d *Dispatcher) RegisterFillHandler(handler FillHandler) uint64 {
	id := d.nextID.Add(1)
	d.fillMu.Lock()
	d.fillHandlers = append(d.fillHandlers, fillHandlerEntry{
		id:      id,
		handler: handler,
	})
	d.fillMu.Unlock()
	return id
}

// UnregisterFillHandler removes a previously registered fill handler by ID.
func (d *Dispatcher) UnregisterFillHandler(handlerID uint64) {
	d.fillMu.Lock()
	for i, h := range d.fillHandlers {
		if h.id == handlerID {
			d.fillHandlers = append(d.fillHandlers[:i], d.fillHandlers[i+1:]...)
			break
		}
	}
	d.fillMu.Unlock()
}

// RegisterOrderUpdateHandler registers a handler for order update events.
// Returns a handler ID that can be used to unregister.
func (d *Dispatcher) RegisterOrderUpdateHandler(handler OrderUpdateHandler) uint64 {
	id := d.nextID.Add(1)
	d.orderUpdateMu.Lock()
	d.orderUpdateHandlers = append(d.orderUpdateHandlers, orderUpdateHandlerEntry{
		id:      id,
		handler: handler,
	})
	d.orderUpdateMu.Unlock()
	return id
}

// UnregisterOrderUpdateHandler removes a previously registered order update handler by ID.
func (d *Dispatcher) UnregisterOrderUpdateHandler(handlerID uint64) {
	d.orderUpdateMu.Lock()
	for i, h := range d.orderUpdateHandlers {
		if h.id == handlerID {
			d.orderUpdateHandlers = append(d.orderUpdateHandlers[:i], d.orderUpdateHandlers[i+1:]...)
			break
		}
	}
	d.orderUpdateMu.Unlock()
}

// DispatchMarketEvent sends a market event for async distribution to registered handlers.
// Uses non-blocking send — if the channel is full, the event is dropped to avoid
// slow consumers blocking the producer (market data engine).
// Returns true if the event was queued, false if dropped.
func (d *Dispatcher) DispatchMarketEvent(eventType models.EventType, event models.MarketEvent) bool {
	select {
	case d.marketCh <- marketDispatchItem{eventType: eventType, event: event}:
		return true
	default:
		// Channel full — drop to avoid blocking the hot path.
		return false
	}
}

// DispatchFillEvent sends a fill event for async distribution to registered handlers.
// Uses non-blocking send. Returns true if queued, false if dropped.
func (d *Dispatcher) DispatchFillEvent(event models.FillEvent) bool {
	select {
	case d.fillCh <- event:
		return true
	default:
		return false
	}
}

// DispatchOrderUpdateEvent sends an order update event for async distribution.
// Uses non-blocking send. Returns true if queued, false if dropped.
func (d *Dispatcher) DispatchOrderUpdateEvent(event models.OrderUpdateEvent) bool {
	select {
	case d.orderUpdateCh <- event:
		return true
	default:
		return false
	}
}

// DispatchMarketEventSync dispatches a market event synchronously to all registered handlers.
// This bypasses the channel and invokes handlers directly in the caller's goroutine.
// Use when minimal latency is critical and the number of handlers is small.
func (d *Dispatcher) DispatchMarketEventSync(eventType models.EventType, event models.MarketEvent) {
	d.marketMu.RLock()
	handlers := d.marketHandlers[eventType]
	d.marketMu.RUnlock()

	for _, h := range handlers {
		h.handler(event)
	}
}

// processMarketEvents is the goroutine that reads from the market channel
// and dispatches to registered handlers.
func (d *Dispatcher) processMarketEvents() {
	defer d.wg.Done()
	for {
		select {
		case <-d.stopCh:
			// Drain remaining events before exiting
			for {
				select {
				case item := <-d.marketCh:
					d.deliverMarketEvent(item)
				default:
					return
				}
			}
		case item := <-d.marketCh:
			d.deliverMarketEvent(item)
		}
	}
}

// processFillEvents is the goroutine that reads from the fill channel
// and dispatches to registered handlers.
func (d *Dispatcher) processFillEvents() {
	defer d.wg.Done()
	for {
		select {
		case <-d.stopCh:
			for {
				select {
				case event := <-d.fillCh:
					d.deliverFillEvent(event)
				default:
					return
				}
			}
		case event := <-d.fillCh:
			d.deliverFillEvent(event)
		}
	}
}

// processOrderUpdateEvents is the goroutine that reads from the order update channel
// and dispatches to registered handlers.
func (d *Dispatcher) processOrderUpdateEvents() {
	defer d.wg.Done()
	for {
		select {
		case <-d.stopCh:
			for {
				select {
				case event := <-d.orderUpdateCh:
					d.deliverOrderUpdateEvent(event)
				default:
					return
				}
			}
		case event := <-d.orderUpdateCh:
			d.deliverOrderUpdateEvent(event)
		}
	}
}

// deliverMarketEvent invokes all registered handlers for the event type.
func (d *Dispatcher) deliverMarketEvent(item marketDispatchItem) {
	d.marketMu.RLock()
	handlers := d.marketHandlers[item.eventType]
	d.marketMu.RUnlock()

	for _, h := range handlers {
		h.handler(item.event)
	}
}

// deliverFillEvent invokes all registered fill handlers.
func (d *Dispatcher) deliverFillEvent(event models.FillEvent) {
	d.fillMu.RLock()
	handlers := d.fillHandlers
	d.fillMu.RUnlock()

	for _, h := range handlers {
		h.handler(event)
	}
}

// deliverOrderUpdateEvent invokes all registered order update handlers.
func (d *Dispatcher) deliverOrderUpdateEvent(event models.OrderUpdateEvent) {
	d.orderUpdateMu.RLock()
	handlers := d.orderUpdateHandlers
	d.orderUpdateMu.RUnlock()

	for _, h := range handlers {
		h.handler(event)
	}
}

// HandlerCount returns the number of registered market event handlers for a given event type.
func (d *Dispatcher) HandlerCount(eventType models.EventType) int {
	d.marketMu.RLock()
	defer d.marketMu.RUnlock()
	return len(d.marketHandlers[eventType])
}

// FillHandlerCount returns the number of registered fill event handlers.
func (d *Dispatcher) FillHandlerCount() int {
	d.fillMu.RLock()
	defer d.fillMu.RUnlock()
	return len(d.fillHandlers)
}

// OrderUpdateHandlerCount returns the number of registered order update handlers.
func (d *Dispatcher) OrderUpdateHandlerCount() int {
	d.orderUpdateMu.RLock()
	defer d.orderUpdateMu.RUnlock()
	return len(d.orderUpdateHandlers)
}
