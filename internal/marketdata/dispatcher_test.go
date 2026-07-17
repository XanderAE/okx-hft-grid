package marketdata

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

func newTestEvent(symbol string) models.MarketEvent {
	return models.MarketEvent{
		Symbol:    symbol,
		Timestamp: time.Now(),
		LastPrice: decimal.NewFromFloat(100.0),
		BestBid:   decimal.NewFromFloat(99.5),
		BestAsk:   decimal.NewFromFloat(100.5),
		BidSize:   decimal.NewFromFloat(10.0),
		AskSize:   decimal.NewFromFloat(10.0),
		SeqID:     1,
	}
}

func TestNewDispatcher_DefaultConfig(t *testing.T) {
	cfg := DefaultDispatcherConfig()
	d := NewDispatcher(cfg)
	if d == nil {
		t.Fatal("expected non-nil dispatcher")
	}
	if d.marketCh == nil || d.fillCh == nil || d.orderUpdateCh == nil {
		t.Fatal("expected all channels to be initialized")
	}
}

func TestNewDispatcher_ZeroConfig(t *testing.T) {
	d := NewDispatcher(DispatcherConfig{})
	if d == nil {
		t.Fatal("expected non-nil dispatcher with zero config")
	}
	// Should use defaults
	if cap(d.marketCh) != 4096 {
		t.Errorf("expected market channel size 4096, got %d", cap(d.marketCh))
	}
}

func TestDispatcher_RegisterAndDispatchMarketEvent(t *testing.T) {
	d := NewDispatcher(DefaultDispatcherConfig())
	d.Start()
	defer d.Stop()

	var received atomic.Int64
	handler := func(event models.MarketEvent) {
		received.Add(1)
	}

	d.RegisterMarketHandler(models.EventMarketData, handler)

	event := newTestEvent("BTC-USDT")
	ok := d.DispatchMarketEvent(models.EventMarketData, event)
	if !ok {
		t.Fatal("expected dispatch to succeed")
	}

	// Wait for async delivery
	time.Sleep(10 * time.Millisecond)
	if received.Load() != 1 {
		t.Errorf("expected 1 event received, got %d", received.Load())
	}
}

func TestDispatcher_MultipleHandlers(t *testing.T) {
	d := NewDispatcher(DefaultDispatcherConfig())
	d.Start()
	defer d.Stop()

	var count1, count2 atomic.Int64

	d.RegisterMarketHandler(models.EventMarketData, func(event models.MarketEvent) {
		count1.Add(1)
	})
	d.RegisterMarketHandler(models.EventMarketData, func(event models.MarketEvent) {
		count2.Add(1)
	})

	d.DispatchMarketEvent(models.EventMarketData, newTestEvent("ETH-USDT"))

	time.Sleep(10 * time.Millisecond)
	if count1.Load() != 1 || count2.Load() != 1 {
		t.Errorf("expected both handlers called, got count1=%d count2=%d", count1.Load(), count2.Load())
	}
}

func TestDispatcher_UnregisterMarketHandler(t *testing.T) {
	d := NewDispatcher(DefaultDispatcherConfig())
	d.Start()
	defer d.Stop()

	var received atomic.Int64
	handler := func(event models.MarketEvent) {
		received.Add(1)
	}

	id := d.RegisterMarketHandler(models.EventMarketData, handler)

	// First dispatch should work
	d.DispatchMarketEvent(models.EventMarketData, newTestEvent("BTC-USDT"))
	time.Sleep(10 * time.Millisecond)
	if received.Load() != 1 {
		t.Fatalf("expected 1 before unregister, got %d", received.Load())
	}

	// Unregister and dispatch again
	d.UnregisterMarketHandler(models.EventMarketData, id)
	d.DispatchMarketEvent(models.EventMarketData, newTestEvent("BTC-USDT"))
	time.Sleep(10 * time.Millisecond)
	if received.Load() != 1 {
		t.Errorf("expected still 1 after unregister, got %d", received.Load())
	}
}

func TestDispatcher_DifferentEventTypes(t *testing.T) {
	d := NewDispatcher(DefaultDispatcherConfig())
	d.Start()
	defer d.Stop()

	var marketCount, invalidCount atomic.Int64

	d.RegisterMarketHandler(models.EventMarketData, func(event models.MarketEvent) {
		marketCount.Add(1)
	})
	d.RegisterMarketHandler(models.EventDataInvalid, func(event models.MarketEvent) {
		invalidCount.Add(1)
	})

	// Dispatch market event — only market handler should fire
	d.DispatchMarketEvent(models.EventMarketData, newTestEvent("BTC-USDT"))
	time.Sleep(10 * time.Millisecond)

	if marketCount.Load() != 1 {
		t.Errorf("expected marketCount=1, got %d", marketCount.Load())
	}
	if invalidCount.Load() != 0 {
		t.Errorf("expected invalidCount=0, got %d", invalidCount.Load())
	}

	// Dispatch invalid event — only invalid handler should fire
	d.DispatchMarketEvent(models.EventDataInvalid, newTestEvent("BTC-USDT"))
	time.Sleep(10 * time.Millisecond)

	if invalidCount.Load() != 1 {
		t.Errorf("expected invalidCount=1, got %d", invalidCount.Load())
	}
}

func TestDispatcher_FillEvent(t *testing.T) {
	d := NewDispatcher(DefaultDispatcherConfig())
	d.Start()
	defer d.Stop()

	var received atomic.Int64
	d.RegisterFillHandler(func(event models.FillEvent) {
		received.Add(1)
	})

	fill := models.FillEvent{
		OrderID:  "order-1",
		Symbol:   "BTC-USDT",
		Side:     models.SideBuy,
		Price:    decimal.NewFromFloat(100.0),
		Quantity: decimal.NewFromFloat(1.0),
	}

	ok := d.DispatchFillEvent(fill)
	if !ok {
		t.Fatal("expected fill dispatch to succeed")
	}

	time.Sleep(10 * time.Millisecond)
	if received.Load() != 1 {
		t.Errorf("expected 1 fill received, got %d", received.Load())
	}
}

func TestDispatcher_UnregisterFillHandler(t *testing.T) {
	d := NewDispatcher(DefaultDispatcherConfig())
	d.Start()
	defer d.Stop()

	var received atomic.Int64
	id := d.RegisterFillHandler(func(event models.FillEvent) {
		received.Add(1)
	})

	fill := models.FillEvent{Symbol: "BTC-USDT"}
	d.DispatchFillEvent(fill)
	time.Sleep(10 * time.Millisecond)
	if received.Load() != 1 {
		t.Fatalf("expected 1, got %d", received.Load())
	}

	d.UnregisterFillHandler(id)
	d.DispatchFillEvent(fill)
	time.Sleep(10 * time.Millisecond)
	if received.Load() != 1 {
		t.Errorf("expected still 1 after unregister, got %d", received.Load())
	}
}

func TestDispatcher_OrderUpdateEvent(t *testing.T) {
	d := NewDispatcher(DefaultDispatcherConfig())
	d.Start()
	defer d.Stop()

	var received atomic.Int64
	d.RegisterOrderUpdateHandler(func(event models.OrderUpdateEvent) {
		received.Add(1)
	})

	update := models.OrderUpdateEvent{
		OrderID: "order-1",
		Symbol:  "BTC-USDT",
		Status:  models.OrderStatusOpen,
	}

	ok := d.DispatchOrderUpdateEvent(update)
	if !ok {
		t.Fatal("expected order update dispatch to succeed")
	}

	time.Sleep(10 * time.Millisecond)
	if received.Load() != 1 {
		t.Errorf("expected 1 order update received, got %d", received.Load())
	}
}

func TestDispatcher_UnregisterOrderUpdateHandler(t *testing.T) {
	d := NewDispatcher(DefaultDispatcherConfig())
	d.Start()
	defer d.Stop()

	var received atomic.Int64
	id := d.RegisterOrderUpdateHandler(func(event models.OrderUpdateEvent) {
		received.Add(1)
	})

	update := models.OrderUpdateEvent{Symbol: "BTC-USDT"}
	d.DispatchOrderUpdateEvent(update)
	time.Sleep(10 * time.Millisecond)
	if received.Load() != 1 {
		t.Fatalf("expected 1, got %d", received.Load())
	}

	d.UnregisterOrderUpdateHandler(id)
	d.DispatchOrderUpdateEvent(update)
	time.Sleep(10 * time.Millisecond)
	if received.Load() != 1 {
		t.Errorf("expected still 1 after unregister, got %d", received.Load())
	}
}

func TestDispatcher_NonBlockingDispatch(t *testing.T) {
	// Create a dispatcher with a very small channel to test non-blocking behavior
	cfg := DispatcherConfig{
		MarketChannelSize:      2,
		FillChannelSize:        2,
		OrderUpdateChannelSize: 2,
	}
	d := NewDispatcher(cfg)
	// Don't start — channels will fill up

	event := newTestEvent("BTC-USDT")

	// Fill the channel
	d.DispatchMarketEvent(models.EventMarketData, event)
	d.DispatchMarketEvent(models.EventMarketData, event)

	// Third dispatch should return false (dropped)
	ok := d.DispatchMarketEvent(models.EventMarketData, event)
	if ok {
		t.Error("expected dispatch to return false when channel is full")
	}
}

func TestDispatcher_SyncDispatch(t *testing.T) {
	d := NewDispatcher(DefaultDispatcherConfig())
	// No need to Start for sync dispatch

	var received int
	d.RegisterMarketHandler(models.EventMarketData, func(event models.MarketEvent) {
		received++
	})

	event := newTestEvent("BTC-USDT")
	d.DispatchMarketEventSync(models.EventMarketData, event)

	if received != 1 {
		t.Errorf("expected 1 sync delivery, got %d", received)
	}
}

func TestDispatcher_HandlerCount(t *testing.T) {
	d := NewDispatcher(DefaultDispatcherConfig())

	if d.HandlerCount(models.EventMarketData) != 0 {
		t.Error("expected 0 handlers initially")
	}

	id1 := d.RegisterMarketHandler(models.EventMarketData, func(event models.MarketEvent) {})
	if d.HandlerCount(models.EventMarketData) != 1 {
		t.Error("expected 1 handler")
	}

	d.RegisterMarketHandler(models.EventMarketData, func(event models.MarketEvent) {})
	if d.HandlerCount(models.EventMarketData) != 2 {
		t.Error("expected 2 handlers")
	}

	d.UnregisterMarketHandler(models.EventMarketData, id1)
	if d.HandlerCount(models.EventMarketData) != 1 {
		t.Error("expected 1 handler after unregister")
	}
}

func TestDispatcher_FillHandlerCount(t *testing.T) {
	d := NewDispatcher(DefaultDispatcherConfig())

	if d.FillHandlerCount() != 0 {
		t.Error("expected 0 fill handlers initially")
	}

	id := d.RegisterFillHandler(func(event models.FillEvent) {})
	if d.FillHandlerCount() != 1 {
		t.Error("expected 1 fill handler")
	}

	d.UnregisterFillHandler(id)
	if d.FillHandlerCount() != 0 {
		t.Error("expected 0 fill handlers after unregister")
	}
}

func TestDispatcher_ConcurrentRegisterAndDispatch(t *testing.T) {
	d := NewDispatcher(DefaultDispatcherConfig())
	d.Start()
	defer d.Stop()

	var totalReceived atomic.Int64
	var wg sync.WaitGroup

	// Concurrently register handlers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.RegisterMarketHandler(models.EventMarketData, func(event models.MarketEvent) {
				totalReceived.Add(1)
			})
		}()
	}
	wg.Wait()

	// Dispatch one event — all 10 handlers should receive it
	d.DispatchMarketEvent(models.EventMarketData, newTestEvent("BTC-USDT"))
	time.Sleep(20 * time.Millisecond)

	if totalReceived.Load() != 10 {
		t.Errorf("expected 10 total received, got %d", totalReceived.Load())
	}
}

func TestDispatcher_StopDrainsEvents(t *testing.T) {
	d := NewDispatcher(DefaultDispatcherConfig())

	var received atomic.Int64
	d.RegisterMarketHandler(models.EventMarketData, func(event models.MarketEvent) {
		received.Add(1)
	})

	// Queue events before starting
	for i := 0; i < 5; i++ {
		d.DispatchMarketEvent(models.EventMarketData, newTestEvent("BTC-USDT"))
	}

	d.Start()
	// Give a brief moment for processing then stop
	time.Sleep(10 * time.Millisecond)
	d.Stop()

	if received.Load() != 5 {
		t.Errorf("expected 5 events drained, got %d", received.Load())
	}
}

func TestDispatcher_LatencyBenchmark(t *testing.T) {
	// Verify dispatch + delivery completes well under 50μs target
	d := NewDispatcher(DefaultDispatcherConfig())
	d.Start()
	defer d.Stop()

	var deliveryTime atomic.Int64
	d.RegisterMarketHandler(models.EventMarketData, func(event models.MarketEvent) {
		deliveryTime.Store(time.Now().UnixNano())
	})

	// Warm up
	for i := 0; i < 100; i++ {
		d.DispatchMarketEvent(models.EventMarketData, newTestEvent("BTC-USDT"))
	}
	time.Sleep(50 * time.Millisecond)

	// Measure
	start := time.Now().UnixNano()
	d.DispatchMarketEvent(models.EventMarketData, newTestEvent("BTC-USDT"))
	time.Sleep(5 * time.Millisecond)

	delivery := deliveryTime.Load()
	if delivery == 0 {
		t.Fatal("handler was not called")
	}

	latencyNs := delivery - start
	latencyUs := float64(latencyNs) / 1000.0

	// Log the latency — under normal conditions should be well under 50μs
	// but CI environments may be slower, so we use a generous threshold
	t.Logf("Dispatch latency: %.2f μs", latencyUs)
	if latencyUs > 5000 { // 5ms generous threshold for CI
		t.Errorf("dispatch latency %.2f μs exceeds 5ms threshold", latencyUs)
	}
}
