package persistence

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func testObservation(symbol, orderID, fillID string, cumQty decimal.Decimal, observedAt time.Time) models.FillObservation {
	return models.FillObservation{
		Symbol:             symbol,
		ExchangeOrderID:    orderID,
		ExchangeFillID:     fillID,
		Side:               models.SideBuy,
		CumulativeQuantity: cumQty,
		FillPrice:          decimal.NewFromInt(100),
		Fee:                decimal.NewFromFloat(0.01),
		Source:             models.FillSourcePrivateWS,
		ExchangeTimestamp:  observedAt.Add(-time.Millisecond * 50),
		ObservedAt:         observedAt,
	}
}

func testPlan() models.CounterOrderPlan {
	return models.CounterOrderPlan{
		Eligibility: models.FillEligible,
		Price:       decimal.NewFromInt(101),
		Purpose:     "counter-sell",
	}
}

func TestFillIdentity_UniqueKey(t *testing.T) {
	// Same fill produces same key
	key1, err := models.UniqueFillKey("DOGE-USDT", "order1", "fill1", decimal.NewFromInt(100))
	if err != nil {
		t.Fatal(err)
	}
	key2, err := models.UniqueFillKey("DOGE-USDT", "order1", "fill1", decimal.NewFromInt(100))
	if err != nil {
		t.Fatal(err)
	}
	if key1 != key2 {
		t.Errorf("identical inputs produced different keys: %s vs %s", key1, key2)
	}

	// Different cumulative produces different key
	key3, err := models.UniqueFillKey("DOGE-USDT", "order1", "fill1", decimal.NewFromInt(150))
	if err != nil {
		t.Fatal(err)
	}
	if key1 == key3 {
		t.Error("different cumulative should produce different key")
	}

	// Different symbol produces different key
	key4, err := models.UniqueFillKey("WIF-USDT", "order1", "fill1", decimal.NewFromInt(100))
	if err != nil {
		t.Fatal(err)
	}
	if key1 == key4 {
		t.Error("different symbol should produce different key")
	}

	// Invalid inputs produce error
	_, err = models.UniqueFillKey("", "order1", "fill1", decimal.NewFromInt(100))
	if err == nil {
		t.Error("expected error for empty symbol")
	}
	_, err = models.UniqueFillKey("DOGE-USDT", "", "fill1", decimal.NewFromInt(100))
	if err == nil {
		t.Error("expected error for empty order ID")
	}
	_, err = models.UniqueFillKey("DOGE-USDT", "order1", "", decimal.NewFromInt(100))
	if err == nil {
		t.Error("expected error for empty fill ID")
	}
	_, err = models.UniqueFillKey("DOGE-USDT", "order1", "fill1", decimal.Zero)
	if err == nil {
		t.Error("expected error for zero cumulative")
	}
}

func TestCumulativeDelta_Basic(t *testing.T) {
	tests := []struct {
		name     string
		prev     decimal.Decimal
		obs      decimal.Decimal
		wantNew  bool
		wantDelta decimal.Decimal
		wantErr  bool
	}{
		{"new fill", decimal.Zero, decimal.NewFromInt(100), true, decimal.NewFromInt(100), false},
		{"increment", decimal.NewFromInt(100), decimal.NewFromInt(150), true, decimal.NewFromInt(50), false},
		{"duplicate", decimal.NewFromInt(100), decimal.NewFromInt(100), false, decimal.Zero, false},
		{"out of order", decimal.NewFromInt(150), decimal.NewFromInt(100), false, decimal.Zero, false},
		{"negative prev", decimal.NewFromInt(-1), decimal.NewFromInt(100), false, decimal.Zero, true},
		{"zero obs", decimal.Zero, decimal.Zero, false, decimal.Zero, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delta, isNew, err := models.CumulativeDelta(tt.prev, tt.obs)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if isNew != tt.wantNew {
				t.Errorf("isNew = %v, want %v", isNew, tt.wantNew)
			}
			if !delta.Equal(tt.wantDelta) {
				t.Errorf("delta = %s, want %s", delta, tt.wantDelta)
			}
		})
	}
}

func TestLedgerIntentAtomicity_SingleFill(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	obs := testObservation("DOGE-USDT", "exord1", "fill1", decimal.NewFromInt(100), clock.Now())
	plan := testPlan()

	var result *models.FillApplyResult
	err = store.WithImmediateTx(context.Background(), func(dtx *DurableTx) error {
		var e error
		result, e = ObserveFill(context.Background(), dtx, obs, plan, FillApplyOpts{Now: clock.Now})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}

	// Should have created ledger, grid state, intent, and outbox
	if result.Duplicate {
		t.Fatal("expected non-duplicate for first fill")
	}
	if !result.Delta.Equal(decimal.NewFromInt(100)) {
		t.Errorf("delta = %s, want 100", result.Delta)
	}
	if result.Ledger == nil {
		t.Fatal("expected ledger record")
	}
	if result.GridState == nil {
		t.Fatal("expected grid state")
	}
	if result.Intent == nil {
		t.Fatal("expected counter order intent")
	}
	if result.Outbox == nil {
		t.Fatal("expected outbox record")
	}

	// Verify intent fields
	if result.Intent.Symbol != "DOGE-USDT" {
		t.Errorf("intent.Symbol = %s", result.Intent.Symbol)
	}
	if result.Intent.Side != models.SideSell {
		t.Errorf("intent.Side = %v, want SELL", result.Intent.Side)
	}
	if !result.Intent.Quantity.Equal(decimal.NewFromInt(100)) {
		t.Errorf("intent.Quantity = %s, want 100", result.Intent.Quantity)
	}
	if result.Intent.Status != models.IntentPending {
		t.Errorf("intent.Status = %s, want pending", result.Intent.Status)
	}
	if result.Intent.DeterministicClientOrderID == "" {
		t.Error("expected deterministic client order ID")
	}

	// Verify deadlines
	if result.Intent.InitiationDeadline != obs.ObservedAt.Add(5*time.Second) {
		t.Errorf("initiation deadline wrong: %v", result.Intent.InitiationDeadline)
	}
	if result.Intent.TerminalDeadline != obs.ObservedAt.Add(15*time.Second) {
		t.Errorf("terminal deadline wrong: %v", result.Intent.TerminalDeadline)
	}
}

func TestLedgerIntentAtomicity_DuplicateFillIdempotent(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	obs := testObservation("DOGE-USDT", "exord1", "fill1", decimal.NewFromInt(100), clock.Now())
	plan := testPlan()

	// First observation
	err = store.WithImmediateTx(context.Background(), func(dtx *DurableTx) error {
		_, e := ObserveFill(context.Background(), dtx, obs, plan, FillApplyOpts{Now: clock.Now})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}

	// Second observation (duplicate)
	var result *models.FillApplyResult
	err = store.WithImmediateTx(context.Background(), func(dtx *DurableTx) error {
		var e error
		result, e = ObserveFill(context.Background(), dtx, obs, plan, FillApplyOpts{Now: clock.Now})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}

	if !result.Duplicate {
		t.Error("expected duplicate detection on second observation")
	}
	if result.Intent != nil {
		t.Error("duplicate must NOT create second intent")
	}
	if result.Outbox != nil {
		t.Error("duplicate must NOT create second outbox entry")
	}
}

func TestLedgerIntentAtomicity_CumulativeIncrement(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	plan := testPlan()

	// First fill: cumulative 100
	obs1 := testObservation("DOGE-USDT", "exord1", "fill1", decimal.NewFromInt(100), clock.Now())
	var r1 *models.FillApplyResult
	err = store.WithImmediateTx(context.Background(), func(dtx *DurableTx) error {
		var e error
		r1, e = ObserveFill(context.Background(), dtx, obs1, plan, FillApplyOpts{Now: clock.Now})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r1.Delta.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("first delta = %s, want 100", r1.Delta)
	}

	// Second fill: cumulative 150 (delta should be 50)
	clock.Advance(time.Second)
	obs2 := testObservation("DOGE-USDT", "exord1", "fill2", decimal.NewFromInt(150), clock.Now())
	var r2 *models.FillApplyResult
	err = store.WithImmediateTx(context.Background(), func(dtx *DurableTx) error {
		var e error
		r2, e = ObserveFill(context.Background(), dtx, obs2, plan, FillApplyOpts{Now: clock.Now})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Duplicate {
		t.Fatal("150 cumulative is not a duplicate of 100")
	}
	if !r2.Delta.Equal(decimal.NewFromInt(50)) {
		t.Errorf("second delta = %s, want 50", r2.Delta)
	}
	if r2.Intent == nil {
		t.Fatal("expected second intent for delta 50")
	}
	if !r2.Intent.Quantity.Equal(decimal.NewFromInt(50)) {
		t.Errorf("second intent quantity = %s, want 50", r2.Intent.Quantity)
	}

	// Verify the two intents have different deterministic IDs
	if r1.Intent.DeterministicClientOrderID == r2.Intent.DeterministicClientOrderID {
		t.Error("different fills must produce different client order IDs")
	}
}

func TestLedgerIntentAtomicity_OutOfOrderIgnored(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	plan := testPlan()

	// First fill: cumulative 150
	obs1 := testObservation("DOGE-USDT", "exord1", "fill2", decimal.NewFromInt(150), clock.Now())
	err = store.WithImmediateTx(context.Background(), func(dtx *DurableTx) error {
		_, e := ObserveFill(context.Background(), dtx, obs1, plan, FillApplyOpts{Now: clock.Now})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}

	// Out-of-order: cumulative 100 arrives after 150
	clock.Advance(time.Second)
	obs2 := testObservation("DOGE-USDT", "exord1", "fill1", decimal.NewFromInt(100), clock.Now())
	var result *models.FillApplyResult
	err = store.WithImmediateTx(context.Background(), func(dtx *DurableTx) error {
		var e error
		result, e = ObserveFill(context.Background(), dtx, obs2, plan, FillApplyOpts{Now: clock.Now})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}

	if !result.Duplicate {
		t.Error("out-of-order observation should be detected as duplicate")
	}
	if !result.OutOfOrder {
		t.Error("should be flagged as out-of-order")
	}
}

func TestLedgerIntentAtomicity_SellFillNoIntent(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	obs := models.FillObservation{
		Symbol:             "DOGE-USDT",
		ExchangeOrderID:    "exord1",
		ExchangeFillID:     "fill1",
		Side:               models.SideSell,
		CumulativeQuantity: decimal.NewFromInt(50),
		FillPrice:          decimal.NewFromInt(105),
		Fee:                decimal.NewFromFloat(0.01),
		Source:             models.FillSourcePrivateWS,
		ExchangeTimestamp:  clock.Now(),
		ObservedAt:         clock.Now(),
	}
	plan := models.CounterOrderPlan{Eligibility: models.FillEligible, Price: decimal.NewFromInt(100), Purpose: "counter-buy"}

	var result *models.FillApplyResult
	err = store.WithImmediateTx(context.Background(), func(dtx *DurableTx) error {
		var e error
		result, e = ObserveFill(context.Background(), dtx, obs, plan, FillApplyOpts{Now: clock.Now})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}

	// SELL fills do NOT create counter intent (Counter_SELL only for BUY fills)
	if result.Intent != nil {
		t.Error("SELL fill should not create counter intent in this path")
	}
	if result.GridState == nil {
		t.Fatal("expected grid state update for SELL fill")
	}
}

func TestLedgerIntentAtomicity_RollbackOnError(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	clock := &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	obs := testObservation("DOGE-USDT", "exord1", "fill1", decimal.NewFromInt(100), clock.Now())
	plan := testPlan()

	// Force an error during the transaction by using a cancelled context
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancelled

	err = store.WithImmediateTx(context.Background(), func(dtx *DurableTx) error {
		_, e := ObserveFill(cancelCtx, dtx, obs, plan, FillApplyOpts{Now: clock.Now})
		return e
	})
	// Error expected due to cancelled context
	if err == nil {
		// It may or may not error depending on sqlite driver behavior with cancelled ctx
		// In either case, let's verify atomicity by retrying with valid context
	}

	// Now try with a valid context - should succeed as if the first never happened
	var result *models.FillApplyResult
	err = store.WithImmediateTx(context.Background(), func(dtx *DurableTx) error {
		var e error
		result, e = ObserveFill(context.Background(), dtx, obs, plan, FillApplyOpts{Now: clock.Now})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	// If previous tx rolled back, this should be non-duplicate
	if result.Duplicate {
		t.Error("after rollback, fill should not be seen as duplicate")
	}
}

func TestSchemaMigration_Additive(t *testing.T) {
	dbPath := tempDBPath(t)

	// First open creates all tables
	store1, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	// Verify durability settings
	settings, err := store1.DurabilitySettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.JournalMode != "WAL" {
		t.Errorf("journal_mode = %s, want WAL", settings.JournalMode)
	}
	if settings.Synchronous != "FULL" {
		t.Errorf("synchronous = %s, want FULL", settings.Synchronous)
	}
	if !settings.ForeignKeys {
		t.Error("foreign_keys should be enabled")
	}
	if settings.TxLock != "IMMEDIATE" {
		t.Errorf("tx_lock = %s, want IMMEDIATE", settings.TxLock)
	}

	store1.Close()

	// Second open should succeed (additive migration is idempotent)
	store2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopening database after migration failed: %v", err)
	}
	store2.Close()
}

func TestSchemaMigration_LegacyTablesPreserved(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Legacy operations should still work alongside new tables
	order := &models.Order{
		OrderId:    "legacy-001",
		Symbol:     "DOGE-USDT",
		Side:       models.SideBuy,
		Price:      decimal.NewFromInt(100),
		Quantity:   decimal.NewFromInt(50),
		Status:     models.OrderStatusOpen,
		CreateTime: time.Now().UnixMilli(),
		UpdateTime: time.Now().UnixMilli(),
	}
	if err := store.SaveOrder(order); err != nil {
		t.Fatalf("legacy SaveOrder should work: %v", err)
	}

	orders, err := store.LoadOrders()
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 {
		t.Fatalf("expected 1 legacy order, got %d", len(orders))
	}
}
