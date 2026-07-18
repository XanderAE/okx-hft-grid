package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

func setupStoreWithFill(t *testing.T) (*SQLiteStore, *fakeClock, *models.FillApplyResult) {
	t.Helper()
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}

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
	if result.Outbox == nil {
		t.Fatal("expected outbox entry from setup")
	}
	return store, clock, result
}

func TestOutboxLease_ClaimAndComplete(t *testing.T) {
	store, clock, _ := setupStoreWithFill(t)
	defer store.Close()

	// Claim the outbox entry
	outbox, intent, err := store.ClaimOutbox(context.Background(), "worker-1", clock)
	if err != nil {
		t.Fatalf("ClaimOutbox error: %v", err)
	}
	if outbox == nil {
		t.Fatal("expected outbox record")
	}
	if intent == nil {
		t.Fatal("expected intent record")
	}
	if outbox.Status != models.OutboxLeased {
		t.Errorf("status = %s, want leased", outbox.Status)
	}
	if outbox.LeaseOwner != "worker-1" {
		t.Errorf("lease_owner = %s, want worker-1", outbox.LeaseOwner)
	}
	if intent.DeterministicClientOrderID == "" {
		t.Error("expected deterministic client order ID on intent")
	}

	// Complete it
	err = store.CompleteOutbox(context.Background(), outbox.OutboxID, "exchange-order-123", clock)
	if err != nil {
		t.Fatalf("CompleteOutbox error: %v", err)
	}

	// No more work available
	_, _, err = store.ClaimOutbox(context.Background(), "worker-1", clock)
	if !errors.Is(err, ErrNoOutboxWork) {
		t.Errorf("expected ErrNoOutboxWork after complete, got %v", err)
	}
}

func TestOutboxLease_NoWorkAvailable(t *testing.T) {
	dbPath := tempDBPath(t)
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	clock := &fakeClock{now: time.Now()}
	_, _, err = store.ClaimOutbox(context.Background(), "worker-1", clock)
	if !errors.Is(err, ErrNoOutboxWork) {
		t.Errorf("expected ErrNoOutboxWork on empty outbox, got %v", err)
	}
}

func TestOutboxLease_LeasePreventsDuplicateClaim(t *testing.T) {
	store, clock, _ := setupStoreWithFill(t)
	defer store.Close()

	// First claim succeeds
	outbox, _, err := store.ClaimOutbox(context.Background(), "worker-1", clock)
	if err != nil {
		t.Fatal(err)
	}
	if outbox == nil {
		t.Fatal("expected first claim to succeed")
	}

	// Second claim while leased should find no work
	_, _, err = store.ClaimOutbox(context.Background(), "worker-2", clock)
	if !errors.Is(err, ErrNoOutboxWork) {
		t.Errorf("expected ErrNoOutboxWork while leased, got %v", err)
	}
}

func TestCrashRecovery_ExpiredLeaseReclaimed(t *testing.T) {
	store, clock, _ := setupStoreWithFill(t)
	defer store.Close()

	// Worker-1 claims
	_, _, err := store.ClaimOutbox(context.Background(), "worker-1", clock)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate crash: advance time past lease timeout
	clock.Advance(OutboxLeaseTimeout + time.Second)

	// Recover expired leases
	recovered, err := store.RecoverExpiredLeases(context.Background(), clock)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Errorf("expected 1 recovered lease, got %d", recovered)
	}

	// Worker-2 can now claim
	outbox, _, err := store.ClaimOutbox(context.Background(), "worker-2", clock)
	if err != nil {
		t.Fatalf("expected reclaimed outbox to be claimable: %v", err)
	}
	if outbox.LeaseOwner != "worker-2" {
		t.Errorf("new owner = %s, want worker-2", outbox.LeaseOwner)
	}
}

func TestCrashRecovery_LoadRecoveryState(t *testing.T) {
	store, clock, _ := setupStoreWithFill(t)
	defer store.Close()

	// Load recovery state should show the pending entry
	records, err := store.LoadRecoveryState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 recovery record, got %d", len(records))
	}
	if records[0].Status != models.OutboxPending {
		t.Errorf("status = %s, want pending", records[0].Status)
	}

	// Claim and complete
	outbox, _, err := store.ClaimOutbox(context.Background(), "recovery-worker", clock)
	if err != nil {
		t.Fatal(err)
	}
	err = store.CompleteOutbox(context.Background(), outbox.OutboxID, "exch-recovered", clock)
	if err != nil {
		t.Fatal(err)
	}

	// After completion, recovery should be empty
	records, err = store.LoadRecoveryState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Errorf("expected 0 recovery records after completion, got %d", len(records))
	}
}

func TestCrashRecovery_DeterministicClientOrderIDStable(t *testing.T) {
	store, clock, result := setupStoreWithFill(t)
	defer store.Close()

	originalClOrdID := result.Intent.DeterministicClientOrderID

	// Claim
	_, intent, err := store.ClaimOutbox(context.Background(), "w1", clock)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the clOrdID is the same - deterministic for recovery
	if intent.DeterministicClientOrderID != originalClOrdID {
		t.Errorf("claimed intent clOrdID changed: %s vs %s",
			intent.DeterministicClientOrderID, originalClOrdID)
	}

	// Schedule retry
	clock.Advance(2 * time.Second)
	err = store.ScheduleRetry(context.Background(), result.Outbox.OutboxID,
		clock.Now().Add(time.Second), models.FailureUncertainTransport, clock)
	if err != nil {
		t.Fatal(err)
	}

	// Advance past retry time
	clock.Advance(2 * time.Second)

	// Reclaim should still have the same deterministic clOrdID
	_, intent2, err := store.ClaimOutbox(context.Background(), "w2", clock)
	if err != nil {
		t.Fatal(err)
	}
	if intent2.DeterministicClientOrderID != originalClOrdID {
		t.Errorf("retry clOrdID changed: %s vs %s",
			intent2.DeterministicClientOrderID, originalClOrdID)
	}
}

func TestCounterSellDeadline_FiveSecondInitiation(t *testing.T) {
	store, clock, result := setupStoreWithFill(t)
	defer store.Close()

	// The intent should have a 5-second initiation deadline
	if result.Intent.InitiationDeadline.Sub(result.Intent.ObservedAt) != 5*time.Second {
		t.Errorf("initiation SLO = %v, want 5s",
			result.Intent.InitiationDeadline.Sub(result.Intent.ObservedAt))
	}

	// And a 15-second terminal deadline
	if result.Intent.TerminalDeadline.Sub(result.Intent.ObservedAt) != 15*time.Second {
		t.Errorf("terminal SLO = %v, want 15s",
			result.Intent.TerminalDeadline.Sub(result.Intent.ObservedAt))
	}

	// Simulate: advance past terminal deadline, then fail
	clock.Advance(16 * time.Second)
	err := store.FailOutbox(context.Background(), result.Outbox.OutboxID,
		models.FailureUnknownTerminal, "51000", "order not found after deadline", clock)
	if err != nil {
		t.Fatal(err)
	}

	// Verify it's marked as safe-failure-terminal
	rec, err := store.QueryOutboxByIntentID(context.Background(), result.Intent.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != models.OutboxFailed {
		t.Errorf("outbox status = %s, want failed", rec.Status)
	}
}

func TestCounterSellDeadline_QueryBeforeSameIDRetry(t *testing.T) {
	store, clock, result := setupStoreWithFill(t)
	defer store.Close()

	// Query-before-same-ID-retry: verify we can look up the outbox by intent
	rec, err := store.QueryOutboxByIntentID(context.Background(), result.Intent.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil {
		t.Fatal("expected to find outbox by intent ID (query-before-retry)")
	}
	if rec.OutboxID != result.Outbox.OutboxID {
		t.Errorf("outbox_id mismatch: %s vs %s", rec.OutboxID, result.Outbox.OutboxID)
	}

	// Claim and schedule retry
	_, _, err = store.ClaimOutbox(context.Background(), "w1", clock)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Second)
	err = store.ScheduleRetry(context.Background(), result.Outbox.OutboxID,
		clock.Now().Add(time.Second), models.FailureUncertainTransport, clock)
	if err != nil {
		t.Fatal(err)
	}

	// Query again should show uncertain status
	rec, err = store.QueryOutboxByIntentID(context.Background(), result.Intent.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != models.OutboxUncertain {
		t.Errorf("expected uncertain status after retry schedule, got %s", rec.Status)
	}
	if rec.AttemptCount != 1 {
		t.Errorf("attempt_count = %d, want 1", rec.AttemptCount)
	}
}

func TestOutboxLease_CriticalCommitClassification(t *testing.T) {
	// Verify that the PersistenceFailure error hierarchy works correctly
	err := &PersistenceFailure{
		Operation: "commit",
		Class:     models.FailureCriticalCommit,
		Uncertain: true,
		Err:       ErrCriticalCommitUncertain,
	}

	if !errors.Is(err, ErrSharedPersistenceFailure) {
		t.Error("critical commit should match ErrSharedPersistenceFailure")
	}
	if !errors.Is(err, ErrCriticalCommitUncertain) {
		t.Error("critical commit should match ErrCriticalCommitUncertain")
	}
	if !IsSharedPersistenceFailure(err) {
		t.Error("IsSharedPersistenceFailure should return true")
	}
	if !IsCriticalCommitUncertain(err) {
		t.Error("IsCriticalCommitUncertain should return true")
	}

	// Non-critical write should match shared but not critical
	writeErr := &PersistenceFailure{
		Operation: "insert",
		Class:     models.FailurePersistenceWrite,
		Uncertain: false,
		Err:       errors.New("disk full"),
	}
	if !errors.Is(writeErr, ErrSharedPersistenceFailure) {
		t.Error("any persistence failure should match ErrSharedPersistenceFailure")
	}
	if errors.Is(writeErr, ErrCriticalCommitUncertain) {
		t.Error("non-commit failure should NOT match ErrCriticalCommitUncertain")
	}
}
