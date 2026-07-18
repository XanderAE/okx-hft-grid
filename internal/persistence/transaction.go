package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/yourname/okx-hft-grid/pkg/models"
)

var (
	ErrCriticalCommitUncertain  = errors.New("persistence: critical commit outcome is uncertain")
	ErrSharedPersistenceFailure = errors.New("persistence: shared dependency failure")
	ErrNoOutboxWork             = errors.New("persistence: no outbox work is ready")
	ErrUnownedFill              = errors.New("persistence: fill does not belong to bot lineage")
	ErrInvariantViolation       = errors.New("persistence: durable state invariant violated")
)

// PersistenceFailure is deliberately machine-classifiable. A critical commit
// error is always shared-scope and uncertain: callers must not continue
// increasing risk until integrity, outbox recovery and reconciliation succeed.
type PersistenceFailure struct {
	Operation string
	Class     models.FailureClass
	Uncertain bool
	Err       error
}

func (e *PersistenceFailure) Error() string {
	return fmt.Sprintf("persistence %s failed (class=%s, shared=true, uncertain=%t): %v", e.Operation, e.Class, e.Uncertain, e.Err)
}

func (e *PersistenceFailure) Unwrap() error { return e.Err }

func (e *PersistenceFailure) Is(target error) bool {
	if target == ErrSharedPersistenceFailure {
		return true
	}
	return target == ErrCriticalCommitUncertain && e.Class == models.FailureCriticalCommit && e.Uncertain
}

func IsSharedPersistenceFailure(err error) bool {
	return errors.Is(err, ErrSharedPersistenceFailure)
}

func IsCriticalCommitUncertain(err error) bool {
	return errors.Is(err, ErrCriticalCommitUncertain)
}

func writeFailure(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &PersistenceFailure{Operation: operation, Class: models.FailurePersistenceWrite, Err: err}
}

// DurableTx intentionally exposes only local SQL operations. Production fill
// and outbox methods do not invoke any callback capable of network I/O while a
// DurableTx is active.
type DurableTx struct {
	tx *sql.Tx
}

func (t *DurableTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, query, args...)
}

func (t *DurableTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, query, args...)
}

func (t *DurableTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, query, args...)
}

// WithImmediateTx uses the driver's _txlock=immediate contract configured by
// NewSQLiteStore. It serializes with legacy Save* methods through the store
// mutex and classifies a failed COMMIT as shared critical uncertainty.
func (s *SQLiteStore) WithImmediateTx(ctx context.Context, fn func(*DurableTx) error) error {
	if fn == nil {
		return errors.New("persistence: immediate transaction callback is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("persistence: store is closed")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return writeFailure("begin-immediate", err)
	}
	wrapped := &DurableTx{tx: tx}
	if err := fn(wrapped); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return writeFailure("rollback", fmt.Errorf("callback error: %v; rollback error: %w", err, rollbackErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return &PersistenceFailure{
			Operation: "commit",
			Class:     models.FailureCriticalCommit,
			Uncertain: true,
			Err:       fmt.Errorf("%w: %v", ErrCriticalCommitUncertain, err),
		}
	}
	return nil
}

type DurabilitySettings struct {
	JournalMode string
	Synchronous string
	ForeignKeys bool
	TxLock      string
}

func (s *SQLiteStore) DurabilitySettings(ctx context.Context) (DurabilitySettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return DurabilitySettings{}, errors.New("persistence: store is closed")
	}
	var settings DurabilitySettings
	var synchronous int
	var foreignKeys int
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&settings.JournalMode); err != nil {
		return settings, writeFailure("read-journal-mode", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return settings, writeFailure("read-synchronous", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return settings, writeFailure("read-foreign-keys", err)
	}
	settings.JournalMode = strings.ToUpper(settings.JournalMode)
	switch synchronous {
	case 0:
		settings.Synchronous = "OFF"
	case 1:
		settings.Synchronous = "NORMAL"
	case 2:
		settings.Synchronous = "FULL"
	case 3:
		settings.Synchronous = "EXTRA"
	default:
		settings.Synchronous = fmt.Sprintf("UNKNOWN(%d)", synchronous)
	}
	settings.ForeignKeys = foreignKeys == 1
	settings.TxLock = "IMMEDIATE"
	return settings, nil
}
