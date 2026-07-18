package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// Clock abstracts time for deterministic testing of 5-second initiation and
// 15-second terminal deadlines without real sleeps or flaky timing.
type Clock interface {
	Now() time.Time
}

// RealClock uses the system clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// OutboxLeaseTimeout is how long a worker holds exclusive access to a pending
// outbox entry before another worker may reclaim it on crash recovery.
const OutboxLeaseTimeout = 10 * time.Second

// ClaimOutbox atomically leases one ready outbox entry for the given workerID.
// Returns ErrNoOutboxWork when no entry is eligible. The claim uses the
// provided Clock rather than system time.
func (s *SQLiteStore) ClaimOutbox(ctx context.Context, workerID string, clock Clock) (*models.OutboxRecord, *models.CounterOrderIntent, error) {
	if workerID == "" {
		return nil, nil, errors.New("persistence: workerID is required for outbox claim")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, errors.New("persistence: store is closed")
	}

	now := clock.Now()
	nowNano := now.UnixNano()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, writeFailure("claim-outbox-begin", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Find one entry that is pending/uncertain and either unleased or with
	// expired lease, and whose next_attempt_at has passed.
	var (
		outboxID, intentID, status, leaseOwner, lastErrClass string
		leaseUntil, nextAttemptAt, createdAt, updatedAt       int64
		attemptCount                                          int
	)
	err = tx.QueryRowContext(ctx,
		`SELECT outbox_id, intent_id, status, lease_owner, lease_until,
		        next_attempt_at, attempt_count, last_error_class, created_at, updated_at
		FROM order_outbox
		WHERE status IN ('pending','uncertain')
		  AND next_attempt_at <= ?
		  AND (lease_until <= ? OR lease_owner = '')
		ORDER BY next_attempt_at ASC
		LIMIT 1`,
		nowNano, nowNano,
	).Scan(&outboxID, &intentID, &status, &leaseOwner, &leaseUntil,
		&nextAttemptAt, &attemptCount, &lastErrClass, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNoOutboxWork
	}
	if err != nil {
		return nil, nil, writeFailure("claim-outbox-select", err)
	}

	// Lease it
	leaseExpiry := now.Add(OutboxLeaseTimeout)
	_, err = tx.ExecContext(ctx,
		`UPDATE order_outbox SET lease_owner=?, lease_until=?, status='leased', updated_at=?
		WHERE outbox_id=? AND (lease_until <= ? OR lease_owner = '')`,
		workerID, leaseExpiry.UnixNano(), nowNano,
		outboxID, nowNano,
	)
	if err != nil {
		return nil, nil, writeFailure("claim-outbox-update", err)
	}

	// Load the associated intent
	var (
		iSymbol, iPrice, iQty, iPurpose, iClOrdID, iFinalErr, iFinalSCode, iFinalSMsg string
		iSide, iAttempts                                                                int
		iObservedAt, iInitDeadline, iTermDeadline, iInitiatedAt, iTerminalAt            int64
		iStatus, iExchangeOrderID                                                       string
	)
	err = tx.QueryRowContext(ctx,
		`SELECT symbol, side, price, quantity, purpose, deterministic_client_order_id,
		        observed_at, initiation_deadline, terminal_deadline, initiated_at, terminal_at,
		        status, attempts, exchange_order_id, final_error_class, final_scode, final_smsg
		FROM counter_order_intents WHERE intent_id=?`, intentID,
	).Scan(&iSymbol, &iSide, &iPrice, &iQty, &iPurpose, &iClOrdID,
		&iObservedAt, &iInitDeadline, &iTermDeadline, &iInitiatedAt, &iTerminalAt,
		&iStatus, &iAttempts, &iExchangeOrderID, &iFinalErr, &iFinalSCode, &iFinalSMsg)
	if err != nil {
		return nil, nil, writeFailure("claim-outbox-load-intent", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, &PersistenceFailure{
			Operation: "commit-claim",
			Class:     models.FailureCriticalCommit,
			Uncertain: true,
			Err:       fmt.Errorf("%w: %v", ErrCriticalCommitUncertain, err),
		}
	}
	committed = true

	price, _ := decimalFromStr(iPrice)
	qty, _ := decimalFromStr(iQty)

	outbox := &models.OutboxRecord{
		OutboxID:       outboxID,
		IntentID:       intentID,
		Status:         models.OutboxLeased,
		LeaseOwner:     workerID,
		LeaseUntil:     leaseExpiry,
		NextAttemptAt:  time.Unix(0, nextAttemptAt),
		AttemptCount:   attemptCount,
		LastErrorClass: models.FailureClass(lastErrClass),
		CreatedAt:      time.Unix(0, createdAt),
		UpdatedAt:      now,
	}

	intent := &models.CounterOrderIntent{
		IntentID:                   intentID,
		Symbol:                     iSymbol,
		Side:                       models.Side(iSide),
		Price:                      price,
		Quantity:                   qty,
		Purpose:                    iPurpose,
		DeterministicClientOrderID: iClOrdID,
		ObservedAt:                 time.Unix(0, iObservedAt),
		InitiationDeadline:         time.Unix(0, iInitDeadline),
		TerminalDeadline:           time.Unix(0, iTermDeadline),
		InitiatedAt:                time.Unix(0, iInitiatedAt),
		TerminalAt:                 time.Unix(0, iTerminalAt),
		Status:                     models.IntentStatus(iStatus),
		Attempts:                   iAttempts,
		ExchangeOrderID:            iExchangeOrderID,
		FinalErrorClass:            models.FailureClass(iFinalErr),
		FinalSCode:                 iFinalSCode,
		FinalSMsg:                  iFinalSMsg,
	}

	return outbox, intent, nil
}

// CompleteOutbox marks an outbox entry as completed after exchange confirmation.
func (s *SQLiteStore) CompleteOutbox(ctx context.Context, outboxID string, exchangeOrderID string, clock Clock) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("persistence: store is closed")
	}

	now := clock.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return writeFailure("complete-outbox-begin", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Update outbox
	_, err = tx.ExecContext(ctx,
		`UPDATE order_outbox SET status='completed', updated_at=? WHERE outbox_id=?`,
		now.UnixNano(), outboxID)
	if err != nil {
		return writeFailure("complete-outbox-status", err)
	}

	// Update intent to confirmed
	var intentID string
	err = tx.QueryRowContext(ctx, `SELECT intent_id FROM order_outbox WHERE outbox_id=?`, outboxID).Scan(&intentID)
	if err != nil {
		return writeFailure("complete-outbox-lookup", err)
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE counter_order_intents SET status='confirmed', exchange_order_id=?, terminal_at=?, attempts=attempts+1
		WHERE intent_id=?`,
		exchangeOrderID, now.UnixNano(), intentID)
	if err != nil {
		return writeFailure("complete-intent-confirm", err)
	}

	if err := tx.Commit(); err != nil {
		return &PersistenceFailure{
			Operation: "commit-complete",
			Class:     models.FailureCriticalCommit,
			Uncertain: true,
			Err:       fmt.Errorf("%w: %v", ErrCriticalCommitUncertain, err),
		}
	}
	committed = true
	return nil
}

// FailOutbox marks an outbox entry as failed (safe failure terminal).
func (s *SQLiteStore) FailOutbox(ctx context.Context, outboxID string, errClass models.FailureClass, sCode, sMsg string, clock Clock) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("persistence: store is closed")
	}

	now := clock.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return writeFailure("fail-outbox-begin", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	_, err = tx.ExecContext(ctx,
		`UPDATE order_outbox SET status='failed', last_error_class=?, updated_at=? WHERE outbox_id=?`,
		string(errClass), now.UnixNano(), outboxID)
	if err != nil {
		return writeFailure("fail-outbox-status", err)
	}

	var intentID string
	err = tx.QueryRowContext(ctx, `SELECT intent_id FROM order_outbox WHERE outbox_id=?`, outboxID).Scan(&intentID)
	if err != nil {
		return writeFailure("fail-outbox-lookup", err)
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE counter_order_intents
		SET status='safe-failure-terminal', final_error_class=?, final_scode=?, final_smsg=?,
		    terminal_at=?, attempts=attempts+1
		WHERE intent_id=?`,
		string(errClass), sCode, sMsg, now.UnixNano(), intentID)
	if err != nil {
		return writeFailure("fail-intent-terminal", err)
	}

	if err := tx.Commit(); err != nil {
		return &PersistenceFailure{
			Operation: "commit-fail",
			Class:     models.FailureCriticalCommit,
			Uncertain: true,
			Err:       fmt.Errorf("%w: %v", ErrCriticalCommitUncertain, err),
		}
	}
	committed = true
	return nil
}

// RecoverExpiredLeases returns any leased entries whose lease has expired back
// to pending status. This allows crashed workers' entries to be reclaimed.
func (s *SQLiteStore) RecoverExpiredLeases(ctx context.Context, clock Clock) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("persistence: store is closed")
	}

	now := clock.Now()
	res, err := s.db.ExecContext(ctx,
		`UPDATE order_outbox SET status='pending', lease_owner='', lease_until=0, updated_at=?
		WHERE status='leased' AND lease_until > 0 AND lease_until <= ?`,
		now.UnixNano(), now.UnixNano())
	if err != nil {
		return 0, writeFailure("recover-leases", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// QueryOutboxByIntentID checks if an outbox entry already exists. Used for
// query-before-same-ID-retry logic.
func (s *SQLiteStore) QueryOutboxByIntentID(ctx context.Context, intentID string) (*models.OutboxRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("persistence: store is closed")
	}

	var (
		outboxID, status, leaseOwner, lastErrClass string
		leaseUntil, nextAttemptAt, createdAt, updatedAt int64
		attemptCount                                    int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT outbox_id, status, lease_owner, lease_until, next_attempt_at,
		        attempt_count, last_error_class, created_at, updated_at
		FROM order_outbox WHERE intent_id=?`, intentID,
	).Scan(&outboxID, &status, &leaseOwner, &leaseUntil, &nextAttemptAt,
		&attemptCount, &lastErrClass, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, writeFailure("query-outbox", err)
	}

	return &models.OutboxRecord{
		OutboxID:       outboxID,
		IntentID:       intentID,
		Status:         models.OutboxStatus(status),
		LeaseOwner:     leaseOwner,
		LeaseUntil:     time.Unix(0, leaseUntil),
		NextAttemptAt:  time.Unix(0, nextAttemptAt),
		AttemptCount:   attemptCount,
		LastErrorClass: models.FailureClass(lastErrClass),
		CreatedAt:      time.Unix(0, createdAt),
		UpdatedAt:      time.Unix(0, updatedAt),
	}, nil
}

// ScheduleRetry moves a leased outbox entry back to pending with a bounded
// next attempt time.
func (s *SQLiteStore) ScheduleRetry(ctx context.Context, outboxID string, nextAttempt time.Time, errClass models.FailureClass, clock Clock) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("persistence: store is closed")
	}

	now := clock.Now()
	_, err := s.db.ExecContext(ctx,
		`UPDATE order_outbox
		SET status='uncertain', lease_owner='', lease_until=0,
		    next_attempt_at=?, attempt_count=attempt_count+1,
		    last_error_class=?, updated_at=?
		WHERE outbox_id=?`,
		nextAttempt.UnixNano(), string(errClass), now.UnixNano(), outboxID)
	if err != nil {
		return writeFailure("schedule-retry", err)
	}
	return nil
}

// LoadRecoveryState loads all pending and uncertain outbox entries for startup
// outbox recovery.
func (s *SQLiteStore) LoadRecoveryState(ctx context.Context) ([]models.OutboxRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("persistence: store is closed")
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT outbox_id, intent_id, status, lease_owner, lease_until,
		        next_attempt_at, attempt_count, last_error_class, created_at, updated_at
		FROM order_outbox
		WHERE status IN ('pending','leased','uncertain')
		ORDER BY created_at ASC`)
	if err != nil {
		return nil, writeFailure("load-recovery", err)
	}
	defer rows.Close()

	var records []models.OutboxRecord
	for rows.Next() {
		var (
			outboxID, intentID, status, leaseOwner, lastErrClass string
			leaseUntil, nextAttemptAt, createdAt, updatedAt       int64
			attemptCount                                          int
		)
		if err := rows.Scan(&outboxID, &intentID, &status, &leaseOwner, &leaseUntil,
			&nextAttemptAt, &attemptCount, &lastErrClass, &createdAt, &updatedAt); err != nil {
			return nil, writeFailure("scan-recovery", err)
		}
		records = append(records, models.OutboxRecord{
			OutboxID:       outboxID,
			IntentID:       intentID,
			Status:         models.OutboxStatus(status),
			LeaseOwner:     leaseOwner,
			LeaseUntil:     time.Unix(0, leaseUntil),
			NextAttemptAt:  time.Unix(0, nextAttemptAt),
			AttemptCount:   attemptCount,
			LastErrorClass: models.FailureClass(lastErrClass),
			CreatedAt:      time.Unix(0, createdAt),
			UpdatedAt:      time.Unix(0, updatedAt),
		})
	}
	return records, rows.Err()
}

func decimalFromStr(s string) (decimal.Decimal, error) {
	if s == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(s)
}
