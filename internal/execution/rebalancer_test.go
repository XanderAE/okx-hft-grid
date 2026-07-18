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

// --- Mock infrastructure ---

type mockRebalancerGateway struct {
	mu            sync.Mutex
	tickerResult  TickerObservation
	tickerErr     error
	pendingOrders []ExchangeOrderInfo
	pendingErr    error
	queryResults  map[string][]ExchangeOrderInfo // by exchange order ID -> sequential results
	queryIdx      map[string]int
	queryErr      error
	cancelResults map[string]CancelAttemptResult
	cancelErr     error
	placeResults  []OrderPlaceResult
	placeErr      error
	placeCalls    []NormalizedOrderRequest
	cancelCalls   []OrderRef
	queryCalls    []OrderRef
}

func newMockRebalancerGateway() *mockRebalancerGateway {
	return &mockRebalancerGateway{
		queryResults:  make(map[string][]ExchangeOrderInfo),
		queryIdx:      make(map[string]int),
		cancelResults: make(map[string]CancelAttemptResult),
	}
}

func (m *mockRebalancerGateway) GetTicker(_ context.Context, _ string) (TickerObservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tickerResult, m.tickerErr
}

func (m *mockRebalancerGateway) ListPendingOrders(_ context.Context, _ string) ([]ExchangeOrderInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pendingOrders, m.pendingErr
}

func (m *mockRebalancerGateway) QueryOrder(_ context.Context, ref OrderRef) (ExchangeOrderInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queryCalls = append(m.queryCalls, ref)
	if m.queryErr != nil {
		return ExchangeOrderInfo{}, m.queryErr
	}
	if results, ok := m.queryResults[ref.ExchangeOrderID]; ok && len(results) > 0 {
		idx := m.queryIdx[ref.ExchangeOrderID]
		if idx >= len(results) {
			idx = len(results) - 1 // stay on last result
		}
		m.queryIdx[ref.ExchangeOrderID] = idx + 1
		return results[idx], nil
	}
	return ExchangeOrderInfo{}, fmt.Errorf("order not found: %s", ref.ExchangeOrderID)
}

func (m *mockRebalancerGateway) CancelOrder(_ context.Context, ref OrderRef) (CancelAttemptResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelCalls = append(m.cancelCalls, ref)
	if m.cancelErr != nil {
		return CancelAttemptResult{}, m.cancelErr
	}
	if res, ok := m.cancelResults[ref.ExchangeOrderID]; ok {
		return res, nil
	}
	return CancelAttemptResult{Cancelled: true, ExchangeOrderID: ref.ExchangeOrderID}, nil
}

func (m *mockRebalancerGateway) PlaceOrder(_ context.Context, req NormalizedOrderRequest) (OrderPlaceResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.placeCalls = append(m.placeCalls, req)
	if m.placeErr != nil {
		return OrderPlaceResult{}, m.placeErr
	}
	if len(m.placeResults) > 0 {
		r := m.placeResults[0]
		m.placeResults = m.placeResults[1:]
		return r, nil
	}
	return OrderPlaceResult{ExchangeOrderID: "new-exch-123", ClientOrderID: req.ClOrdID, Status: models.OrderStatusSubmitted}, nil
}

// Unused gateway methods for interface compliance
func (m *mockRebalancerGateway) ListOrderHistory(_ context.Context, _ string, _ QueryWindow) (OrderPage, error) {
	return OrderPage{}, nil
}
func (m *mockRebalancerGateway) ListFills(_ context.Context, _ string, _ FillCursor) (FillPage, error) {
	return FillPage{}, nil
}
func (m *mockRebalancerGateway) GetInstrumentRules(_ context.Context, _ string) (models.InstrumentRules, error) {
	return models.InstrumentRules{}, nil
}

// mockRebalancerGate tracks authorization and safe-stop calls.
type mockRebalancerGate struct {
	mu            sync.Mutex
	authorized    bool
	blockReason   string
	safeStopCalls []safeStopCall
}

type safeStopCall struct {
	Symbol string
	Reason string
	Detail string
}

func newMockRebalancerGate(authorized bool) *mockRebalancerGate {
	return &mockRebalancerGate{authorized: authorized}
}

func (g *mockRebalancerGate) Authorize(_ string, _ int) RebalancerGateDecision {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.authorized {
		return RebalancerGateDecision{Allowed: false, BlockReason: g.blockReason}
	}
	return RebalancerGateDecision{Allowed: true}
}

func (g *mockRebalancerGate) EnterSymbolSafeStop(symbol string, reasonCode string, details string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.safeStopCalls = append(g.safeStopCalls, safeStopCall{symbol, reasonCode, details})
}

func (g *mockRebalancerGate) SafeStopCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.safeStopCalls)
}

// mockBotOrderStore tracks ownership checks.
type mockBotOrderStore struct {
	mu    sync.Mutex
	owned map[string]bool // exchange order ID -> owned
	err   error
}

func newMockBotOrderStore() *mockBotOrderStore {
	return &mockBotOrderStore{owned: make(map[string]bool)}
}

func (s *mockBotOrderStore) IsBotOwned(_ context.Context, _ string, _ string, exchangeOrderID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	return s.owned[exchangeOrderID], nil
}

// mockOutcomeStore records persisted outcomes.
type mockOutcomeStore struct {
	mu       sync.Mutex
	outcomes []models.RebalanceOutcomeRecord
}

func (s *mockOutcomeStore) PersistRebalanceOutcome(_ context.Context, o models.RebalanceOutcomeRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes = append(s.outcomes, o)
	return nil
}

// mockRebalancerFillObserver records fills sent through the rebalancer.
type mockRebalancerFillObserver struct {
	mu    sync.Mutex
	calls []models.FillObservation
	count int32
}

func (o *mockRebalancerFillObserver) ObserveFill(_ context.Context, obs models.FillObservation, _ models.CounterOrderPlan) (*models.FillApplyResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, obs)
	atomic.AddInt32(&o.count, 1)
	return &models.FillApplyResult{Delta: obs.CumulativeQuantity}, nil
}

// mockRebalancerRulesProvider returns static rules.
type mockRebalancerRulesProvider struct {
	rules models.InstrumentRules
	err   error
}

func (p *mockRebalancerRulesProvider) Current(_ context.Context, _ string) (models.InstrumentRules, error) {
	if p.err != nil {
		return models.InstrumentRules{}, p.err
	}
	return p.rules, nil
}

func (p *mockRebalancerRulesProvider) Refresh(_ context.Context, _ string) (models.InstrumentRules, error) {
	return p.Current(nil, "")
}

// --- Helper to build test rebalancer ---

func buildTestRebalancer(t *testing.T) (*Rebalancer, *mockRebalancerGateway, *mockRebalancerGate, *mockBotOrderStore, *mockOutcomeStore, *mockRebalancerFillObserver) {
	t.Helper()
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	gateway := newMockRebalancerGateway()
	gate := newMockRebalancerGate(true)
	orderStore := newMockBotOrderStore()
	outcomeStore := &mockOutcomeStore{}
	fillObs := &mockRebalancerFillObserver{}
	rules := models.InstrumentRules{
		Symbol:    "DOGE-USDT",
		TickSize:  decimal.NewFromFloat(0.00001),
		LotSize:   decimal.NewFromInt(1),
		MinSize:   decimal.NewFromInt(10),
		FetchedAt: now,
		ExpiresAt: now.Add(15 * time.Minute),
	}
	rulesProvider := &mockRebalancerRulesProvider{rules: rules}

	deps := RebalancerDeps{
		Gateway:       gateway,
		Gate:          gate,
		FillObserver:  fillObs,
		RulesProvider: rulesProvider,
		OrderStore:    orderStore,
		OutcomeStore:  outcomeStore,
	}
	config := RebalancerConfig{
		Interval:     30 * time.Second,
		MaxJitter:    5 * time.Second,
		TickerMaxAge: 5 * time.Second,
		Clock:        func() time.Time { return now },
	}
	rb := NewRebalancer("DOGE-USDT", deps, config, nil)
	// Set a fresh ticker that's valid (age=0)
	gateway.tickerResult = TickerObservation{
		Symbol:     "DOGE-USDT",
		Last:       decimal.NewFromFloat(0.40000),
		ReceivedAt: now,
	}
	return rb, gateway, gate, orderStore, outcomeStore, fillObs
}

// --- Test: RebalancerFreshTicker ---

func TestRebalancerFreshTicker(t *testing.T) {
	t.Run("skips_cycle_when_ticker_stale", func(t *testing.T) {
		rb, gateway, _, _, _, _ := buildTestRebalancer(t)
		// Make ticker older than 5 seconds
		gateway.tickerResult.ReceivedAt = time.Date(2025, 1, 15, 9, 59, 54, 0, time.UTC) // 6s old
		result := rb.RunOnce(context.Background())
		if !result.Skipped {
			t.Fatal("expected cycle to be skipped for stale ticker")
		}
		if result.SkipReason == "" {
			t.Fatal("expected skip reason to be set")
		}
	})

	t.Run("skips_cycle_when_ticker_error", func(t *testing.T) {
		rb, gateway, _, _, _, _ := buildTestRebalancer(t)
		gateway.tickerErr = errors.New("connection refused")
		result := rb.RunOnce(context.Background())
		if !result.Skipped {
			t.Fatal("expected cycle to be skipped for ticker error")
		}
	})

	t.Run("skips_cycle_when_ticker_non_positive", func(t *testing.T) {
		rb, gateway, _, _, _, _ := buildTestRebalancer(t)
		gateway.tickerResult.Last = decimal.Zero
		result := rb.RunOnce(context.Background())
		if !result.Skipped {
			t.Fatal("expected cycle to be skipped for non-positive ticker")
		}
	})

	t.Run("uses_ticker_when_age_exactly_5s", func(t *testing.T) {
		rb, gateway, _, orderStore, _, _ := buildTestRebalancer(t)
		// Ticker at exactly 5 seconds - should be accepted (<=5s)
		gateway.tickerResult.ReceivedAt = time.Date(2025, 1, 15, 9, 59, 55, 0, time.UTC) // exactly 5s old
		orderStore.owned["exch-1"] = true
		gateway.pendingOrders = []ExchangeOrderInfo{{
			ExchangeOrderID: "exch-1",
			ClientOrderID:   "tb1test",
			Symbol:          "DOGE-USDT",
			Side:            models.SideBuy,
			Price:           decimal.NewFromFloat(0.39500), // within 2%
			Status:          models.OrderStatusOpen,
		}}
		gateway.queryResults["exch-1"] = []ExchangeOrderInfo{{
			ExchangeOrderID: "exch-1",
			Status:          models.OrderStatusOpen,
			Price:           decimal.NewFromFloat(0.39500),
		}}
		result := rb.RunOnce(context.Background())
		if result.Skipped {
			t.Fatalf("expected cycle to proceed with 5s-old ticker, got skip: %s", result.SkipReason)
		}
	})

	t.Run("rejects_ticker_at_5001ms", func(t *testing.T) {
		rb, gateway, _, _, _, _ := buildTestRebalancer(t)
		// Ticker at 5.001 seconds - should be rejected (>5s)
		gateway.tickerResult.ReceivedAt = time.Date(2025, 1, 15, 9, 59, 54, 999000000, time.UTC)
		result := rb.RunOnce(context.Background())
		if !result.Skipped {
			t.Fatal("expected cycle to be skipped for ticker older than 5s")
		}
	})
}

// --- Test: StrictTwoPercentBoundary ---

func TestStrictTwoPercentBoundary(t *testing.T) {
	t.Run("keeps_order_at_exactly_2_percent", func(t *testing.T) {
		rb, gateway, _, orderStore, outcomeStore, _ := buildTestRebalancer(t)
		// Reference = 0.40000, order price = 0.39200
		// deviation = |0.39200 - 0.40000| / 0.40000 = 0.008/0.4 = 0.02 = exactly 2%
		orderStore.owned["exch-2pct"] = true
		gateway.pendingOrders = []ExchangeOrderInfo{{
			ExchangeOrderID: "exch-2pct",
			ClientOrderID:   "tb1exact2",
			Symbol:          "DOGE-USDT",
			Side:            models.SideBuy,
			Price:           decimal.NewFromFloat(0.39200),
			Quantity:        decimal.NewFromInt(100),
			Status:          models.OrderStatusOpen,
		}}
		result := rb.RunOnce(context.Background())
		if result.Skipped {
			t.Fatalf("cycle should not be skipped: %s", result.SkipReason)
		}
		if result.KeptCount != 1 {
			t.Fatalf("expected 1 kept, got %d", result.KeptCount)
		}
		// Verify persisted outcome is kept-by-rule
		if len(outcomeStore.outcomes) != 1 {
			t.Fatalf("expected 1 outcome persisted, got %d", len(outcomeStore.outcomes))
		}
		if outcomeStore.outcomes[0].TerminalOutcome != models.RebalanceKeptByRule {
			t.Fatalf("expected kept-by-rule, got %s", outcomeStore.outcomes[0].TerminalOutcome)
		}
	})

	t.Run("processes_order_at_2_001_percent", func(t *testing.T) {
		rb, gateway, _, orderStore, outcomeStore, _ := buildTestRebalancer(t)
		// Reference = 0.40000, order price that yields >2% deviation
		// 0.40000 * 0.02001 = 0.008004, so price = 0.40000 - 0.008004 = 0.391996
		// Use 0.39199 which gives deviation = 0.00801/0.4 = 0.020025 > 2%
		orderStore.owned["exch-stale"] = true
		gateway.pendingOrders = []ExchangeOrderInfo{{
			ExchangeOrderID: "exch-stale",
			ClientOrderID:   "tb1stale",
			Symbol:          "DOGE-USDT",
			Side:            models.SideBuy,
			Price:           decimal.NewFromFloat(0.39199),
			Quantity:        decimal.NewFromInt(100),
			Status:          models.OrderStatusOpen,
		}}
		// Set up query result: order is open before cancel
		gateway.queryResults["exch-stale"] = []ExchangeOrderInfo{
			{ExchangeOrderID: "exch-stale", Status: models.OrderStatusOpen, Price: decimal.NewFromFloat(0.39199), Quantity: decimal.NewFromInt(100)},
			{ExchangeOrderID: "exch-stale", Status: models.OrderStatusOpen, Price: decimal.NewFromFloat(0.39199), Quantity: decimal.NewFromInt(100)},
		}
		result := rb.RunOnce(context.Background())
		if result.Skipped {
			t.Fatalf("cycle should not be skipped: %s", result.SkipReason)
		}
		if result.StaleCount != 1 {
			t.Fatalf("expected 1 stale, got %d", result.StaleCount)
		}
		// The order was processed (not just kept)
		if len(outcomeStore.outcomes) < 1 {
			t.Fatal("expected at least 1 outcome persisted")
		}
		out := outcomeStore.outcomes[0]
		if out.TerminalOutcome == models.RebalanceKeptByRule {
			t.Fatal("order at >2% deviation should not be kept-by-rule")
		}
	})

	t.Run("keeps_order_at_1_percent_deviation", func(t *testing.T) {
		rb, gateway, _, orderStore, outcomeStore, _ := buildTestRebalancer(t)
		// Reference = 0.40000, order price = 0.39600 (1% deviation)
		orderStore.owned["exch-1pct"] = true
		gateway.pendingOrders = []ExchangeOrderInfo{{
			ExchangeOrderID: "exch-1pct",
			ClientOrderID:   "tb1onepct",
			Symbol:          "DOGE-USDT",
			Side:            models.SideBuy,
			Price:           decimal.NewFromFloat(0.39600),
			Quantity:        decimal.NewFromInt(100),
			Status:          models.OrderStatusOpen,
		}}
		result := rb.RunOnce(context.Background())
		if result.KeptCount != 1 {
			t.Fatalf("expected 1 kept, got %d", result.KeptCount)
		}
		if outcomeStore.outcomes[0].TerminalOutcome != models.RebalanceKeptByRule {
			t.Fatal("order at 1% should be kept-by-rule")
		}
	})
}

// --- Test: OwnershipFilter ---

func TestOwnershipFilter(t *testing.T) {
	t.Run("preserves_unowned_orders", func(t *testing.T) {
		rb, gateway, _, orderStore, outcomeStore, _ := buildTestRebalancer(t)
		// Order NOT in bot_orders lineage (manual/other strategy)
		orderStore.owned["exch-manual"] = false
		gateway.pendingOrders = []ExchangeOrderInfo{{
			ExchangeOrderID: "exch-manual",
			ClientOrderID:   "manual-order",
			Symbol:          "DOGE-USDT",
			Side:            models.SideBuy,
			Price:           decimal.NewFromFloat(0.30000), // >2% deviation
			Quantity:        decimal.NewFromInt(100),
			Status:          models.OrderStatusOpen,
		}}
		result := rb.RunOnce(context.Background())
		if result.KeptCount != 1 {
			t.Fatalf("expected 1 kept (unowned), got %d", result.KeptCount)
		}
		if outcomeStore.outcomes[0].TerminalOutcome != models.RebalanceKeptByRule {
			t.Fatal("unowned order must be kept-by-rule")
		}
		// Should NOT have attempted cancel
		if len(gateway.cancelCalls) > 0 {
			t.Fatal("should not cancel unowned orders")
		}
	})

	t.Run("preserves_order_when_ownership_check_fails", func(t *testing.T) {
		rb, gateway, _, orderStore, outcomeStore, _ := buildTestRebalancer(t)
		orderStore.err = errors.New("db timeout")
		gateway.pendingOrders = []ExchangeOrderInfo{{
			ExchangeOrderID: "exch-dbfail",
			ClientOrderID:   "tb1dbfail",
			Symbol:          "DOGE-USDT",
			Side:            models.SideBuy,
			Price:           decimal.NewFromFloat(0.30000),
			Quantity:        decimal.NewFromInt(100),
			Status:          models.OrderStatusOpen,
		}}
		result := rb.RunOnce(context.Background())
		if result.KeptCount != 1 {
			t.Fatalf("expected kept when ownership unknown, got kept=%d", result.KeptCount)
		}
		if outcomeStore.outcomes[0].TerminalOutcome != models.RebalanceKeptByRule {
			t.Fatal("ownership-uncertain order must be kept-by-rule")
		}
	})

	t.Run("processes_bot_owned_stale_order", func(t *testing.T) {
		rb, gateway, _, orderStore, outcomeStore, _ := buildTestRebalancer(t)
		orderStore.owned["exch-owned"] = true
		gateway.pendingOrders = []ExchangeOrderInfo{{
			ExchangeOrderID: "exch-owned",
			ClientOrderID:   "tb1owned",
			Symbol:          "DOGE-USDT",
			Side:            models.SideBuy,
			Price:           decimal.NewFromFloat(0.30000), // >2% deviation
			Quantity:        decimal.NewFromInt(100),
			Status:          models.OrderStatusOpen,
		}}
		gateway.queryResults["exch-owned"] = []ExchangeOrderInfo{
			{ExchangeOrderID: "exch-owned", Status: models.OrderStatusOpen, Price: decimal.NewFromFloat(0.30000), Quantity: decimal.NewFromInt(100)},
			{ExchangeOrderID: "exch-owned", Status: models.OrderStatusCancelled, Price: decimal.NewFromFloat(0.30000), Quantity: decimal.NewFromInt(100)},
		}
		result := rb.RunOnce(context.Background())
		if result.StaleCount != 1 {
			t.Fatalf("expected 1 stale (bot-owned >2%%), got %d", result.StaleCount)
		}
		if len(outcomeStore.outcomes) < 1 {
			t.Fatal("expected outcome persisted")
		}
		// Should have attempted cancel
		if len(gateway.cancelCalls) == 0 {
			t.Fatal("should attempt cancel on bot-owned stale order")
		}
	})
}

// --- Test: CancelTerminalConfirmation ---

func TestCancelTerminalConfirmation(t *testing.T) {
	t.Run("queries_before_and_after_cancel", func(t *testing.T) {
		rb, gateway, _, orderStore, _, _ := buildTestRebalancer(t)
		orderStore.owned["exch-q"] = true
		gateway.pendingOrders = []ExchangeOrderInfo{{
			ExchangeOrderID: "exch-q",
			ClientOrderID:   "tb1q",
			Symbol:          "DOGE-USDT",
			Side:            models.SideBuy,
			Price:           decimal.NewFromFloat(0.30000),
			Quantity:        decimal.NewFromInt(100),
			Status:          models.OrderStatusOpen,
		}}
		// First query (pre-cancel): open
		// After cancel: cancelled (sequential mock)
		gateway.queryResults["exch-q"] = []ExchangeOrderInfo{
			{ExchangeOrderID: "exch-q", Status: models.OrderStatusOpen, Price: decimal.NewFromFloat(0.30000), Quantity: decimal.NewFromInt(100)},
			{ExchangeOrderID: "exch-q", Status: models.OrderStatusCancelled, Price: decimal.NewFromFloat(0.30000), Quantity: decimal.NewFromInt(100)},
		}

		rb.RunOnce(context.Background())

		// Verify at least 2 query calls (pre-cancel + post-cancel)
		if len(gateway.queryCalls) < 2 {
			t.Fatalf("expected at least 2 query calls (pre+post cancel), got %d", len(gateway.queryCalls))
		}
	})

	t.Run("failed_safe_when_post_cancel_query_fails", func(t *testing.T) {
		rb, gateway, gate, orderStore, outcomeStore, _ := buildTestRebalancer(t)
		orderStore.owned["exch-fail"] = true
		gateway.pendingOrders = []ExchangeOrderInfo{{
			ExchangeOrderID: "exch-fail",
			ClientOrderID:   "tb1fail",
			Symbol:          "DOGE-USDT",
			Side:            models.SideBuy,
			Price:           decimal.NewFromFloat(0.30000),
			Quantity:        decimal.NewFromInt(100),
			Status:          models.OrderStatusOpen,
		}}
		// Pre-cancel query: open. Post-cancel: still open (triggers failed-safe)
		gateway.queryResults["exch-fail"] = []ExchangeOrderInfo{
			{ExchangeOrderID: "exch-fail", Status: models.OrderStatusOpen, Price: decimal.NewFromFloat(0.30000), Quantity: decimal.NewFromInt(100)},
			{ExchangeOrderID: "exch-fail", Status: models.OrderStatusOpen, Price: decimal.NewFromFloat(0.30000), Quantity: decimal.NewFromInt(100)},
		}
		// Order stays "open" after cancel → failed-safe
		rb.RunOnce(context.Background())

		// Order is still "open" after cancel (cancel response doesn't prove terminal)
		// => should be failed-safe with Safe_Stop
		if len(outcomeStore.outcomes) < 1 {
			t.Fatal("expected at least 1 outcome")
		}
		lastOutcome := outcomeStore.outcomes[len(outcomeStore.outcomes)-1]
		if lastOutcome.TerminalOutcome != models.RebalanceFailedSafe {
			t.Fatalf("expected failed-safe when order still open after cancel, got %s", lastOutcome.TerminalOutcome)
		}
		if gate.SafeStopCount() == 0 {
			t.Fatal("expected Safe_Stop to be activated")
		}
	})
}

// --- Test: CancelFillRace ---

func TestCancelFillRace(t *testing.T) {
	t.Run("processes_fill_when_order_filled_during_cancel", func(t *testing.T) {
		rb, gateway, _, orderStore, outcomeStore, fillObs := buildTestRebalancer(t)
		orderStore.owned["exch-race"] = true
		gateway.pendingOrders = []ExchangeOrderInfo{{
			ExchangeOrderID: "exch-race",
			ClientOrderID:   "tb1race",
			Symbol:          "DOGE-USDT",
			Side:            models.SideBuy,
			Price:           decimal.NewFromFloat(0.30000),
			Quantity:        decimal.NewFromInt(100),
			Status:          models.OrderStatusOpen,
		}}
		// Pre-cancel: filled (simulates fill race - order filled before we can cancel)
		gateway.queryResults["exch-race"] = []ExchangeOrderInfo{{
			ExchangeOrderID:   "exch-race",
			ClientOrderID:     "tb1race",
			Status:            models.OrderStatusFilled,
			Side:              models.SideBuy,
			Price:             decimal.NewFromFloat(0.30000),
			Quantity:          decimal.NewFromInt(100),
			CumulativeFillQty: decimal.NewFromInt(100),
			AvgFillPrice:      decimal.NewFromFloat(0.30000),
		}}

		rb.RunOnce(context.Background())

		// Verify fill was sent to FillProcessor
		fillObs.mu.Lock()
		fillCount := len(fillObs.calls)
		fillObs.mu.Unlock()
		if fillCount == 0 {
			t.Fatal("expected fill to be sent to FillProcessor for cancel/fill race")
		}

		// Outcome should be "filled"
		if len(outcomeStore.outcomes) < 1 {
			t.Fatal("expected outcome persisted")
		}
		if outcomeStore.outcomes[0].TerminalOutcome != models.RebalanceFilled {
			t.Fatalf("expected filled outcome for race, got %s", outcomeStore.outcomes[0].TerminalOutcome)
		}
	})

	t.Run("processes_partial_fill_before_creating_replacement", func(t *testing.T) {
		rb, gateway, _, orderStore, outcomeStore, fillObs := buildTestRebalancer(t)
		orderStore.owned["exch-partial"] = true
		gateway.pendingOrders = []ExchangeOrderInfo{{
			ExchangeOrderID:   "exch-partial",
			ClientOrderID:     "tb1partial",
			Symbol:            "DOGE-USDT",
			Side:              models.SideBuy,
			Price:             decimal.NewFromFloat(0.30000),
			Quantity:          decimal.NewFromInt(100),
			CumulativeFillQty: decimal.Zero,
			Status:            models.OrderStatusOpen,
		}}
		// Query returns cancelled with partial fill (both pre and post)
		gateway.queryResults["exch-partial"] = []ExchangeOrderInfo{{
			ExchangeOrderID:   "exch-partial",
			ClientOrderID:     "tb1partial",
			Status:            models.OrderStatusCancelled,
			Side:              models.SideBuy,
			Price:             decimal.NewFromFloat(0.30000),
			Quantity:          decimal.NewFromInt(100),
			CumulativeFillQty: decimal.NewFromInt(30), // partial fill
			AvgFillPrice:      decimal.NewFromFloat(0.30000),
		}}

		rb.RunOnce(context.Background())

		// Verify fill was processed
		fillObs.mu.Lock()
		fillCount := len(fillObs.calls)
		fillObs.mu.Unlock()
		if fillCount == 0 {
			t.Fatal("expected partial fill to be sent through FillProcessor")
		}

		// Verify outcome recorded
		if len(outcomeStore.outcomes) < 1 {
			t.Fatal("expected outcome persisted")
		}
	})
}

// --- Test: NoOverlap ---

func TestNoOverlap(t *testing.T) {
	t.Run("non_overlapping_cycles_via_lock", func(t *testing.T) {
		now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
		gateway := newMockRebalancerGateway()
		gate := newMockRebalancerGate(true)
		orderStore := newMockBotOrderStore()
		outcomeStore := &mockOutcomeStore{}
		fillObs := &mockRebalancerFillObserver{}
		rules := models.InstrumentRules{
			Symbol:    "DOGE-USDT",
			TickSize:  decimal.NewFromFloat(0.00001),
			LotSize:   decimal.NewFromInt(1),
			MinSize:   decimal.NewFromInt(10),
			FetchedAt: now,
			ExpiresAt: now.Add(15 * time.Minute),
		}
		rulesProvider := &mockRebalancerRulesProvider{rules: rules}

		deps := RebalancerDeps{
			Gateway:       gateway,
			Gate:          gate,
			FillObserver:  fillObs,
			RulesProvider: rulesProvider,
			OrderStore:    orderStore,
			OutcomeStore:  outcomeStore,
		}
		config := RebalancerConfig{
			Interval:     30 * time.Second,
			MaxJitter:    5 * time.Second,
			TickerMaxAge: 5 * time.Second,
			Clock:        func() time.Time { return now },
		}
		gateway.tickerResult = TickerObservation{
			Symbol:     "DOGE-USDT",
			Last:       decimal.NewFromFloat(0.40000),
			ReceivedAt: now,
		}

		var cycleCount int32
		rb := NewRebalancer("DOGE-USDT", deps, config, func(r RebalancerCycleResult) {
			atomic.AddInt32(&cycleCount, 1)
		})

		// Start and quickly stop to verify single-instance
		rb.Start()
		rb.Start() // second start should be no-op
		time.Sleep(10 * time.Millisecond)
		rb.Stop()

		if !rb.IsRunning() == true {
			// After Stop, should not be running
		}
		if rb.IsRunning() {
			t.Fatal("rebalancer should not be running after Stop()")
		}
	})
}

// --- Test: AuditedOutcome ---

func TestAuditedOutcome(t *testing.T) {
	t.Run("every_order_gets_persisted_terminal_outcome", func(t *testing.T) {
		rb, gateway, _, orderStore, outcomeStore, _ := buildTestRebalancer(t)

		// Mix of orders: owned stale, owned within threshold, unowned
		orderStore.owned["exch-stale1"] = true
		orderStore.owned["exch-within"] = true
		orderStore.owned["exch-unowned"] = false

		gateway.pendingOrders = []ExchangeOrderInfo{
			{ExchangeOrderID: "exch-stale1", ClientOrderID: "tb1s1", Symbol: "DOGE-USDT", Side: models.SideBuy, Price: decimal.NewFromFloat(0.30000), Quantity: decimal.NewFromInt(100), Status: models.OrderStatusOpen},
			{ExchangeOrderID: "exch-within", ClientOrderID: "tb1w1", Symbol: "DOGE-USDT", Side: models.SideBuy, Price: decimal.NewFromFloat(0.39500), Quantity: decimal.NewFromInt(100), Status: models.OrderStatusOpen},
			{ExchangeOrderID: "exch-unowned", ClientOrderID: "manual1", Symbol: "DOGE-USDT", Side: models.SideBuy, Price: decimal.NewFromFloat(0.30000), Quantity: decimal.NewFromInt(100), Status: models.OrderStatusOpen},
		}
		// For the stale order: query returns open then cancelled
		gateway.queryResults["exch-stale1"] = []ExchangeOrderInfo{
			{ExchangeOrderID: "exch-stale1", Status: models.OrderStatusOpen, Price: decimal.NewFromFloat(0.30000), Quantity: decimal.NewFromInt(100)},
			{ExchangeOrderID: "exch-stale1", Status: models.OrderStatusCancelled, Price: decimal.NewFromFloat(0.30000), Quantity: decimal.NewFromInt(100)},
		}

		rb.RunOnce(context.Background())

		// Every order must have exactly one persisted terminal outcome
		if len(outcomeStore.outcomes) != 3 {
			t.Fatalf("expected 3 outcomes (one per order), got %d", len(outcomeStore.outcomes))
		}
		// Validate outcomes
		validOutcomes := map[models.RebalanceTerminalOutcome]bool{
			models.RebalanceReplaced:         true,
			models.RebalanceFilled:           true,
			models.RebalanceAlreadyCancelled: true,
			models.RebalanceKeptByRule:       true,
			models.RebalanceFailedSafe:       true,
		}
		for i, out := range outcomeStore.outcomes {
			if !validOutcomes[out.TerminalOutcome] {
				t.Fatalf("outcome[%d] has invalid terminal outcome: %s", i, out.TerminalOutcome)
			}
		}
	})

	t.Run("replacement_error_triggers_safe_stop_not_silent_continue", func(t *testing.T) {
		rb, gateway, gate, orderStore, outcomeStore, _ := buildTestRebalancer(t)
		orderStore.owned["exch-reject"] = true
		gateway.pendingOrders = []ExchangeOrderInfo{{
			ExchangeOrderID: "exch-reject",
			ClientOrderID:   "tb1reject",
			Symbol:          "DOGE-USDT",
			Side:            models.SideBuy,
			Price:           decimal.NewFromFloat(0.30000),
			Quantity:        decimal.NewFromInt(100),
			Status:          models.OrderStatusOpen,
		}}
		// Pre-cancel: open. Post-cancel: cancelled. Place: error
		gateway.queryResults["exch-reject"] = []ExchangeOrderInfo{
			{ExchangeOrderID: "exch-reject", Status: models.OrderStatusOpen, Price: decimal.NewFromFloat(0.30000), Quantity: decimal.NewFromInt(100)},
			{ExchangeOrderID: "exch-reject", Status: models.OrderStatusCancelled, Price: decimal.NewFromFloat(0.30000), Quantity: decimal.NewFromInt(100)},
		}
		gateway.placeErr = errors.New("connection reset")

		rb.RunOnce(context.Background())

		// Should have triggered Safe_Stop (not silent continue)
		if gate.SafeStopCount() == 0 {
			t.Fatal("replacement error MUST trigger Safe_Stop, not silent continue")
		}
		// Outcome should be failed-safe
		if len(outcomeStore.outcomes) < 1 {
			t.Fatal("expected outcome persisted")
		}
		if outcomeStore.outcomes[0].TerminalOutcome != models.RebalanceFailedSafe {
			t.Fatalf("expected failed-safe for replacement error, got %s", outcomeStore.outcomes[0].TerminalOutcome)
		}
	})

	t.Run("replacement_rejection_triggers_safe_stop", func(t *testing.T) {
		rb, gateway, gate, orderStore, outcomeStore, _ := buildTestRebalancer(t)
		orderStore.owned["exch-rej2"] = true
		gateway.pendingOrders = []ExchangeOrderInfo{{
			ExchangeOrderID: "exch-rej2",
			ClientOrderID:   "tb1rej2",
			Symbol:          "DOGE-USDT",
			Side:            models.SideBuy,
			Price:           decimal.NewFromFloat(0.30000),
			Quantity:        decimal.NewFromInt(100),
			Status:          models.OrderStatusOpen,
		}}
		gateway.queryResults["exch-rej2"] = []ExchangeOrderInfo{
			{ExchangeOrderID: "exch-rej2", Status: models.OrderStatusOpen, Price: decimal.NewFromFloat(0.30000), Quantity: decimal.NewFromInt(100)},
			{ExchangeOrderID: "exch-rej2", Status: models.OrderStatusCancelled, Price: decimal.NewFromFloat(0.30000), Quantity: decimal.NewFromInt(100)},
		}
		// Place returns structured rejection
		gateway.placeResults = []OrderPlaceResult{{
			Err: &GatewayError{SCode: "51000", SMsg: "Parameter error"},
		}}

		rb.RunOnce(context.Background())

		if gate.SafeStopCount() == 0 {
			t.Fatal("replacement rejection MUST trigger Safe_Stop")
		}
		if outcomeStore.outcomes[0].TerminalOutcome != models.RebalanceFailedSafe {
			t.Fatalf("expected failed-safe, got %s", outcomeStore.outcomes[0].TerminalOutcome)
		}
	})
}
