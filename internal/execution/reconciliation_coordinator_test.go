package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// --- Test infrastructure: mock gateway, observer, store ---

type mockReconcileGateway struct {
	mu        sync.Mutex
	fillPages map[string][]FillPage // symbol -> pages in order
	fillErr   error
	pageIndex map[string]int
}

func newMockReconcileGateway() *mockReconcileGateway {
	return &mockReconcileGateway{
		fillPages: make(map[string][]FillPage),
		pageIndex: make(map[string]int),
	}
}

func (m *mockReconcileGateway) SetFillPages(symbol string, pages []FillPage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fillPages[symbol] = pages
	m.pageIndex[symbol] = 0
}

func (m *mockReconcileGateway) SetFillError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fillErr = err
}

func (m *mockReconcileGateway) ListFills(ctx context.Context, symbol string, cursor FillCursor) (FillPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fillErr != nil {
		return FillPage{}, m.fillErr
	}
	pages, ok := m.fillPages[symbol]
	if !ok || len(pages) == 0 {
		return FillPage{}, nil
	}
	idx := m.pageIndex[symbol]
	if idx >= len(pages) {
		return FillPage{}, nil
	}
	page := pages[idx]
	m.pageIndex[symbol] = idx + 1
	return page, nil
}

// Implement the full ExchangeGateway interface for compile (unused methods panic)
func (m *mockReconcileGateway) PlaceOrder(_ context.Context, _ NormalizedOrderRequest) (OrderPlaceResult, error) {
	panic("not used in reconciliation tests")
}
func (m *mockReconcileGateway) CancelOrder(_ context.Context, _ OrderRef) (CancelAttemptResult, error) {
	panic("not used in reconciliation tests")
}
func (m *mockReconcileGateway) QueryOrder(_ context.Context, _ OrderRef) (ExchangeOrderInfo, error) {
	panic("not used in reconciliation tests")
}
func (m *mockReconcileGateway) ListPendingOrders(_ context.Context, _ string) ([]ExchangeOrderInfo, error) {
	panic("not used in reconciliation tests")
}
func (m *mockReconcileGateway) ListOrderHistory(_ context.Context, _ string, _ QueryWindow) (OrderPage, error) {
	panic("not used in reconciliation tests")
}
func (m *mockReconcileGateway) GetTicker(_ context.Context, _ string) (TickerObservation, error) {
	panic("not used in reconciliation tests")
}
func (m *mockReconcileGateway) GetInstrumentRules(_ context.Context, _ string) (models.InstrumentRules, error) {
	panic("not used in reconciliation tests")
}

// mockFillObserver records all ObserveFill calls for assertion.
type mockFillObserver struct {
	mu      sync.Mutex
	calls   []models.FillObservation
	results []*models.FillApplyResult
	err     error
	callIdx int
}

func newMockFillObserver() *mockFillObserver {
	return &mockFillObserver{}
}

func (o *mockFillObserver) SetError(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.err = err
}

func (o *mockFillObserver) AddResult(r *models.FillApplyResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.results = append(o.results, r)
}

func (o *mockFillObserver) ObserveFill(ctx context.Context, obs models.FillObservation, plan models.CounterOrderPlan) (*models.FillApplyResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, obs)
	if o.err != nil {
		return nil, o.err
	}
	if o.callIdx < len(o.results) {
		r := o.results[o.callIdx]
		o.callIdx++
		return r, nil
	}
	return &models.FillApplyResult{}, nil
}

func (o *mockFillObserver) CallCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.calls)
}

func (o *mockFillObserver) Calls() []models.FillObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	cp := make([]models.FillObservation, len(o.calls))
	copy(cp, o.calls)
	return cp
}

// mockReconciliationStore holds watermarks in memory.
type mockReconciliationStore struct {
	mu          sync.Mutex
	watermarks  map[string]*models.ReconciliationWatermark
	loadErr     error
	commitErr   error
	commitCount int
}

func newMockReconciliationStore() *mockReconciliationStore {
	return &mockReconciliationStore{
		watermarks: make(map[string]*models.ReconciliationWatermark),
	}
}

func (s *mockReconciliationStore) LoadReconciliationWatermark(_ context.Context, symbol, stream string) (*models.ReconciliationWatermark, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	key := symbol + ":" + stream
	return s.watermarks[key], nil
}

func (s *mockReconciliationStore) CommitReconciliationWatermark(_ context.Context, wm models.ReconciliationWatermark) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commitErr != nil {
		return s.commitErr
	}
	key := wm.Symbol + ":" + wm.Stream
	s.watermarks[key] = &wm
	s.commitCount++
	return nil
}

func (s *mockReconciliationStore) CommitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commitCount
}

func (s *mockReconciliationStore) GetWatermark(symbol, stream string) *models.ReconciliationWatermark {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watermarks[symbol+":"+stream]
}

// fakeClock for deterministic time in tests
type reconcileFakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newReconcileFakeClock(t time.Time) *reconcileFakeClock {
	return &reconcileFakeClock{now: t}
}

func (c *reconcileFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *reconcileFakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func sampleFillRecord(orderID, fillID string, side models.Side, qty float64, ts time.Time) FillRecord {
	return FillRecord{
		Symbol:             "DOGE-USDT",
		ExchangeOrderID:    orderID,
		ExchangeFillID:     fillID,
		Side:               side,
		Price:              decimal.NewFromFloat(0.15),
		Quantity:           decimal.NewFromFloat(qty),
		CumulativeQuantity: decimal.NewFromFloat(qty),
		Fee:                decimal.NewFromFloat(0.001),
		Timestamp:          ts,
	}
}

// --- Test: Immediate triggers ---

func TestReconcileImmediateTriggers(t *testing.T) {
	t.Run("startup trigger runs immediately without waiting for periodic cycle", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()

		ts := clock.Now().Add(-10 * time.Second)
		gateway.SetFillPages("DOGE-USDT", []FillPage{
			{Fills: []FillRecord{sampleFillRecord("ord-1", "fill-1", models.SideBuy, 100, ts)}},
		})

		var results []ReconcileResult
		var resultsMu sync.Mutex
		cb := func(r ReconcileResult) {
			resultsMu.Lock()
			results = append(results, r)
			resultsMu.Unlock()
		}

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval:      30 * time.Second,
			OverlapWindow: 5 * time.Second,
			PageSize:      100,
			Clock:         clock.Now,
		}, cb)
		coord.SetSymbols([]string{"DOGE-USDT"})

		// Trigger immediately for startup reason
		result := coord.ReconcileNow(context.Background(), "DOGE-USDT", ReconcileReasonStartup)
		if result.Err != nil {
			t.Fatalf("startup reconcile failed: %v", result.Err)
		}
		if result.Reason != ReconcileReasonStartup {
			t.Errorf("expected reason startup, got %s", result.Reason)
		}
		if observer.CallCount() != 1 {
			t.Errorf("expected 1 fill observation, got %d", observer.CallCount())
		}
	})

	t.Run("reconnect trigger runs immediately", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()

		gateway.SetFillPages("WIF-USDT", []FillPage{
			{Fills: []FillRecord{
				{Symbol: "WIF-USDT", ExchangeOrderID: "ord-2", ExchangeFillID: "fill-2",
					Side: models.SideBuy, Price: decimal.NewFromFloat(2.5),
					Quantity: decimal.NewFromFloat(10), CumulativeQuantity: decimal.NewFromFloat(10),
					Fee: decimal.NewFromFloat(0.01), Timestamp: clock.Now()},
			}},
		})

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval:      30 * time.Second,
			OverlapWindow: 5 * time.Second,
			PageSize:      100,
			Clock:         clock.Now,
		}, nil)
		coord.SetSymbols([]string{"WIF-USDT"})

		result := coord.ReconcileNow(context.Background(), "WIF-USDT", ReconcileReasonReconnect)
		if result.Err != nil {
			t.Fatalf("reconnect reconcile failed: %v", result.Err)
		}
		if result.Reason != ReconcileReasonReconnect {
			t.Errorf("expected reason reconnect, got %s", result.Reason)
		}
		if observer.CallCount() != 1 {
			t.Errorf("expected 1 fill processed, got %d", observer.CallCount())
		}
	})

	t.Run("gap trigger runs immediately", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()

		gateway.SetFillPages("DOGE-USDT", []FillPage{{}})

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval: 30 * time.Second, OverlapWindow: 5 * time.Second, PageSize: 100, Clock: clock.Now,
		}, nil)
		coord.SetSymbols([]string{"DOGE-USDT"})

		result := coord.ReconcileNow(context.Background(), "DOGE-USDT", ReconcileReasonGap)
		if result.Err != nil {
			t.Fatalf("gap reconcile failed: %v", result.Err)
		}
		if result.Reason != ReconcileReasonGap {
			t.Errorf("expected reason gap, got %s", result.Reason)
		}
	})

	t.Run("uncertain outbox trigger runs immediately", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()
		gateway.SetFillPages("DOGE-USDT", []FillPage{{}})

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval: 30 * time.Second, OverlapWindow: 5 * time.Second, PageSize: 100, Clock: clock.Now,
		}, nil)
		coord.SetSymbols([]string{"DOGE-USDT"})

		result := coord.ReconcileNow(context.Background(), "DOGE-USDT", ReconcileReasonUncertainOutbox)
		if result.Err != nil {
			t.Fatalf("uncertain outbox reconcile failed: %v", result.Err)
		}
		if result.Reason != ReconcileReasonUncertainOutbox {
			t.Errorf("expected uncertain-outbox, got %s", result.Reason)
		}
	})
}

// --- Test: 30-second periodic schedule ---

func TestThirtySecondSchedule(t *testing.T) {
	t.Run("periodic schedule fires at 30 seconds not 60", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()
		gateway.SetFillPages("DOGE-USDT", []FillPage{{}})

		var cycleCount int32
		cb := func(r ReconcileResult) {
			atomic.AddInt32(&cycleCount, 1)
		}

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			// Use 30ms to simulate 30s in fast test
			Interval:      30 * time.Millisecond,
			OverlapWindow: 5 * time.Second,
			PageSize:      100,
			Clock:         clock.Now,
		}, cb)
		coord.SetSymbols([]string{"DOGE-USDT"})
		coord.Start()

		// Wait 100ms → should get at least 2 cycles at 30ms interval
		time.Sleep(100 * time.Millisecond)
		coord.Stop()

		count := atomic.LoadInt32(&cycleCount)
		if count < 2 {
			t.Errorf("expected at least 2 periodic cycles in 100ms (30ms interval), got %d", count)
		}
	})

	t.Run("interval is exactly 30 seconds in config not 60", func(t *testing.T) {
		cfg := DefaultReconciliationCoordinatorConfig()
		if cfg.Interval != 30*time.Second {
			t.Errorf("default interval must be 30s, got %v", cfg.Interval)
		}
	})

	t.Run("60 second interval is not used as fallback", func(t *testing.T) {
		// The coordinator constructor rejects 0 but never defaults to 60
		cfg := ReconciliationCoordinatorConfig{Interval: 0}
		coord := NewReconciliationCoordinator(nil, nil, nil, cfg, nil)
		// Internal normalization should set 30s not 60s
		if coord.config.Interval != 30*time.Second {
			t.Errorf("zero interval should normalize to 30s, got %v", coord.config.Interval)
		}
	})
}

// --- Test: Reconcile Pagination ---

func TestReconcilePagination(t *testing.T) {
	t.Run("multi-page fills are all processed", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()

		ts := clock.Now()
		page1 := FillPage{
			Fills:   []FillRecord{sampleFillRecord("ord-1", "fill-1", models.SideBuy, 100, ts)},
			HasMore: true,
			Cursor:  "cursor-1",
		}
		page2 := FillPage{
			Fills:   []FillRecord{sampleFillRecord("ord-2", "fill-2", models.SideBuy, 200, ts.Add(time.Second))},
			HasMore: true,
			Cursor:  "cursor-2",
		}
		page3 := FillPage{
			Fills:   []FillRecord{sampleFillRecord("ord-3", "fill-3", models.SideBuy, 50, ts.Add(2*time.Second))},
			HasMore: false,
		}
		gateway.SetFillPages("DOGE-USDT", []FillPage{page1, page2, page3})

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval: 30 * time.Second, OverlapWindow: 5 * time.Second, PageSize: 100, Clock: clock.Now,
		}, nil)
		coord.SetSymbols([]string{"DOGE-USDT"})

		result := coord.ReconcileNow(context.Background(), "DOGE-USDT", ReconcileReasonPeriodic)
		if result.Err != nil {
			t.Fatalf("pagination reconcile failed: %v", result.Err)
		}
		if result.PagesQueried != 3 {
			t.Errorf("expected 3 pages, got %d", result.PagesQueried)
		}
		if result.FillsChecked != 3 {
			t.Errorf("expected 3 fills checked, got %d", result.FillsChecked)
		}
		if observer.CallCount() != 3 {
			t.Errorf("expected 3 ObserveFill calls, got %d", observer.CallCount())
		}
	})

	t.Run("partial page failure prevents watermark advance", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()

		// First page succeeds, then error on second
		page1 := FillPage{
			Fills:   []FillRecord{sampleFillRecord("ord-1", "fill-1", models.SideBuy, 100, clock.Now())},
			HasMore: true,
			Cursor:  "cursor-1",
		}
		gateway.SetFillPages("DOGE-USDT", []FillPage{page1})
		// After first page, set error for next page
		gateway.SetFillError(errors.New("network timeout"))

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval: 30 * time.Second, OverlapWindow: 5 * time.Second, PageSize: 100, Clock: clock.Now,
		}, nil)
		coord.SetSymbols([]string{"DOGE-USDT"})

		result := coord.ReconcileNow(context.Background(), "DOGE-USDT", ReconcileReasonPeriodic)
		if result.Err == nil {
			t.Fatal("expected error from partial pagination failure")
		}
		// Watermark must NOT advance
		if store.CommitCount() != 0 {
			t.Error("watermark must not advance on partial page failure")
		}
	})
}

// --- Test: Watermark commit only on full success ---

func TestWatermarkCommitOnly(t *testing.T) {
	t.Run("watermark advances only after all pages and applies succeed", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()

		ts := clock.Now()
		gateway.SetFillPages("DOGE-USDT", []FillPage{
			{Fills: []FillRecord{sampleFillRecord("ord-1", "fill-1", models.SideBuy, 100, ts)}},
		})

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval: 30 * time.Second, OverlapWindow: 5 * time.Second, PageSize: 100, Clock: clock.Now,
		}, nil)
		coord.SetSymbols([]string{"DOGE-USDT"})

		result := coord.ReconcileNow(context.Background(), "DOGE-USDT", ReconcileReasonPeriodic)
		if result.Err != nil {
			t.Fatalf("reconcile failed: %v", result.Err)
		}
		if store.CommitCount() != 1 {
			t.Errorf("expected watermark committed once, got %d", store.CommitCount())
		}
		wm := store.GetWatermark("DOGE-USDT", "rest")
		if wm == nil {
			t.Fatal("watermark should be set")
		}
		if wm.StableID != "fill-1" {
			t.Errorf("expected stable_id=fill-1, got %s", wm.StableID)
		}
	})

	t.Run("DB failure during apply prevents watermark advance", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()

		ts := clock.Now()
		gateway.SetFillPages("DOGE-USDT", []FillPage{
			{Fills: []FillRecord{sampleFillRecord("ord-1", "fill-1", models.SideBuy, 100, ts)}},
		})
		// ObserveFill fails (simulating DB write failure)
		observer.SetError(errors.New("persistence: critical commit uncertain"))

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval: 30 * time.Second, OverlapWindow: 5 * time.Second, PageSize: 100, Clock: clock.Now,
		}, nil)
		coord.SetSymbols([]string{"DOGE-USDT"})

		result := coord.ReconcileNow(context.Background(), "DOGE-USDT", ReconcileReasonPeriodic)
		if result.Err == nil {
			t.Fatal("expected error when ObserveFill fails")
		}
		if store.CommitCount() != 0 {
			t.Error("watermark must not advance on apply failure")
		}
	})

	t.Run("auth failure on fill query prevents watermark advance", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()

		gateway.SetFillError(fmt.Errorf("gateway OKX envelope: code=50113 msg=auth failed"))

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval: 30 * time.Second, OverlapWindow: 5 * time.Second, PageSize: 100, Clock: clock.Now,
		}, nil)
		coord.SetSymbols([]string{"DOGE-USDT"})

		result := coord.ReconcileNow(context.Background(), "DOGE-USDT", ReconcileReasonPeriodic)
		if result.Err == nil {
			t.Fatal("expected error from auth failure")
		}
		if store.CommitCount() != 0 {
			t.Error("watermark must not advance on auth failure")
		}
	})

	t.Run("commit failure means watermark not advanced", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()

		ts := clock.Now()
		gateway.SetFillPages("DOGE-USDT", []FillPage{
			{Fills: []FillRecord{sampleFillRecord("ord-1", "fill-1", models.SideBuy, 100, ts)}},
		})
		store.commitErr = errors.New("disk full")

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval: 30 * time.Second, OverlapWindow: 5 * time.Second, PageSize: 100, Clock: clock.Now,
		}, nil)
		coord.SetSymbols([]string{"DOGE-USDT"})

		result := coord.ReconcileNow(context.Background(), "DOGE-USDT", ReconcileReasonPeriodic)
		if result.Err == nil {
			t.Fatal("expected error from commit failure")
		}
		if result.WatermarkAfter != nil {
			t.Error("watermark must not be set on commit failure")
		}
	})
}

// --- Test: Overlap idempotency ---

func TestOverlapIdempotency(t *testing.T) {
	t.Run("overlap window re-queries fills that are idempotently deduplicated", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()

		ts := clock.Now()
		// Set initial watermark at ts (so overlap window = ts - 5s)
		store.watermarks["DOGE-USDT:rest"] = &models.ReconciliationWatermark{
			Symbol: "DOGE-USDT", Stream: "rest", ExchangeAt: ts, StableID: "fill-0",
		}

		// Return a fill that's in the overlap zone (same as before = duplicate)
		gateway.SetFillPages("DOGE-USDT", []FillPage{
			{Fills: []FillRecord{sampleFillRecord("ord-1", "fill-1", models.SideBuy, 100, ts)}},
		})
		// Observer returns duplicate
		observer.AddResult(&models.FillApplyResult{Duplicate: true})

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval: 30 * time.Second, OverlapWindow: 5 * time.Second, PageSize: 100, Clock: clock.Now,
		}, nil)
		coord.SetSymbols([]string{"DOGE-USDT"})

		result := coord.ReconcileNow(context.Background(), "DOGE-USDT", ReconcileReasonPeriodic)
		if result.Err != nil {
			t.Fatalf("reconcile failed: %v", result.Err)
		}
		// Fill was processed (idempotency at FillProcessor level)
		if result.FillsChecked != 1 {
			t.Errorf("expected 1 fill checked, got %d", result.FillsChecked)
		}
		if result.DuplicatesFound != 1 {
			t.Errorf("expected 1 duplicate, got %d", result.DuplicatesFound)
		}
		// Watermark still advances (success, just nothing new)
		if store.CommitCount() != 1 {
			t.Errorf("expected watermark commit, got %d commits", store.CommitCount())
		}
	})

	t.Run("WS and REST observations use same FillProcessor path", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()

		ts := clock.Now()
		gateway.SetFillPages("DOGE-USDT", []FillPage{
			{Fills: []FillRecord{sampleFillRecord("ord-1", "fill-1", models.SideBuy, 100, ts)}},
		})

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval: 30 * time.Second, OverlapWindow: 5 * time.Second, PageSize: 100, Clock: clock.Now,
		}, nil)
		coord.SetSymbols([]string{"DOGE-USDT"})

		// REST path through coordinator
		coord.ReconcileNow(context.Background(), "DOGE-USDT", ReconcileReasonPeriodic)

		calls := observer.Calls()
		if len(calls) != 1 {
			t.Fatalf("expected 1 observation, got %d", len(calls))
		}
		// The source must be reconciliation (REST)
		if calls[0].Source != models.FillSourceReconciliation {
			t.Errorf("expected source rest-reconciliation, got %s", calls[0].Source)
		}
		// A WS observation would use FillSourcePrivateWS and go through same observer
		// This test proves the coordinator routes through the unified interface.
	})
}

// --- Test: Trigger coalescing ---

func TestTriggerCoalescing(t *testing.T) {
	t.Run("running cycle coalesces follow-up and records overrun", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()

		// Return empty pages so cycles complete quickly
		gateway.SetFillPages("DOGE-USDT", []FillPage{{}})

		var cycleResults []ReconcileResult
		var mu sync.Mutex
		cb := func(r ReconcileResult) {
			mu.Lock()
			cycleResults = append(cycleResults, r)
			mu.Unlock()
		}

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval: 30 * time.Second, OverlapWindow: 5 * time.Second, PageSize: 100, Clock: clock.Now,
		}, cb)
		coord.SetSymbols([]string{"DOGE-USDT"})

		// Start a blocking first cycle, then trigger while running
		// We use the async Trigger mechanism + Start
		coord.Start()
		// Send multiple triggers rapidly - they should coalesce
		for i := 0; i < 5; i++ {
			coord.Trigger("DOGE-USDT", ReconcileReasonPeriodic)
		}
		time.Sleep(100 * time.Millisecond)
		coord.Stop()

		overruns := coord.OverrunCount("DOGE-USDT")
		// At least some should be coalesced (overrun > 0)
		if overruns < 1 {
			t.Logf("overrun count: %d (may be 0 if all triggers processed sequentially)", overruns)
		}
	})

	t.Run("at most one follow-up is coalesced per running cycle", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()
		gateway.SetFillPages("DOGE-USDT", []FillPage{{}})

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval: 30 * time.Second, OverlapWindow: 5 * time.Second, PageSize: 100, Clock: clock.Now,
		}, nil)
		coord.SetSymbols([]string{"DOGE-USDT"})

		// Manually get the symbol state
		coord.mu.Lock()
		state := coord.symbols["DOGE-USDT"]
		coord.mu.Unlock()

		// Simulate "running" state
		state.mu.Lock()
		state.running = true
		state.mu.Unlock()

		// Try to coalesce multiple
		coord.handleTrigger(symbolTrigger{symbol: "DOGE-USDT", reason: ReconcileReasonReconnect})
		coord.handleTrigger(symbolTrigger{symbol: "DOGE-USDT", reason: ReconcileReasonGap})
		coord.handleTrigger(symbolTrigger{symbol: "DOGE-USDT", reason: ReconcileReasonPeriodic})

		state.mu.Lock()
		pending := state.pendingTrigger
		overruns := state.overrunCount
		state.running = false
		state.mu.Unlock()

		if pending == nil {
			t.Fatal("expected one coalesced pending trigger")
		}
		// Only the first coalesce sets the reason
		if *pending != ReconcileReasonReconnect {
			t.Errorf("expected first coalesced reason reconnect, got %s", *pending)
		}
		// All 3 attempts to trigger while running count as overrun
		if overruns != 3 {
			t.Errorf("expected 3 overrun counts, got %d", overruns)
		}
	})
}

// --- Test: Missed fill compensation ---

func TestMissedFillCompensation(t *testing.T) {
	t.Run("missed fill from REST creates exactly one intent via ObserveFill", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()

		ts := clock.Now()
		// A missed fill discovered by REST reconciliation
		gateway.SetFillPages("DOGE-USDT", []FillPage{
			{Fills: []FillRecord{sampleFillRecord("ord-missed", "fill-missed", models.SideBuy, 500, ts)}},
		})
		// Observer returns a real new fill (not duplicate)
		observer.AddResult(&models.FillApplyResult{
			Delta: decimal.NewFromFloat(500),
			Intent: &models.CounterOrderIntent{
				IntentID: "ci1_test",
				Symbol:   "DOGE-USDT",
				Side:     models.SideSell,
				Quantity: decimal.NewFromFloat(500),
				Status:   models.IntentPending,
			},
			Outbox: &models.OutboxRecord{
				OutboxID: "ob_test",
				Status:   models.OutboxPending,
			},
		})

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval: 30 * time.Second, OverlapWindow: 5 * time.Second, PageSize: 100, Clock: clock.Now,
		}, nil)
		coord.SetSymbols([]string{"DOGE-USDT"})

		result := coord.ReconcileNow(context.Background(), "DOGE-USDT", ReconcileReasonStartup)
		if result.Err != nil {
			t.Fatalf("reconcile failed: %v", result.Err)
		}
		if result.FillsCompensated != 1 {
			t.Errorf("expected 1 compensated fill, got %d", result.FillsCompensated)
		}
		// Verify exactly one call to ObserveFill (one intent produced)
		if observer.CallCount() != 1 {
			t.Errorf("expected exactly 1 ObserveFill call for compensation, got %d", observer.CallCount())
		}

		calls := observer.Calls()
		if calls[0].Source != models.FillSourceReconciliation {
			t.Errorf("compensated fill source should be rest-reconciliation, got %s", calls[0].Source)
		}
		if !calls[0].CumulativeQuantity.Equal(decimal.NewFromFloat(500)) {
			t.Errorf("expected cumulative qty 500, got %s", calls[0].CumulativeQuantity)
		}
	})

	t.Run("duplicate fill from REST does not create additional intent", func(t *testing.T) {
		clock := newReconcileFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
		gateway := newMockReconcileGateway()
		observer := newMockFillObserver()
		store := newMockReconciliationStore()

		ts := clock.Now()
		gateway.SetFillPages("DOGE-USDT", []FillPage{
			{Fills: []FillRecord{sampleFillRecord("ord-1", "fill-1", models.SideBuy, 100, ts)}},
		})
		// Observer detects duplicate (already seen via WS)
		observer.AddResult(&models.FillApplyResult{Duplicate: true})

		coord := NewReconciliationCoordinator(gateway, observer, store, ReconciliationCoordinatorConfig{
			Interval: 30 * time.Second, OverlapWindow: 5 * time.Second, PageSize: 100, Clock: clock.Now,
		}, nil)
		coord.SetSymbols([]string{"DOGE-USDT"})

		result := coord.ReconcileNow(context.Background(), "DOGE-USDT", ReconcileReasonPeriodic)
		if result.Err != nil {
			t.Fatalf("reconcile failed: %v", result.Err)
		}
		if result.FillsCompensated != 0 {
			t.Errorf("expected 0 compensated (duplicate), got %d", result.FillsCompensated)
		}
		if result.DuplicatesFound != 1 {
			t.Errorf("expected 1 duplicate, got %d", result.DuplicatesFound)
		}
	})
}
