package persistence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/yourname/okx-hft-grid/pkg/models"
)

// LoadReconciliationWatermark loads the committed watermark for a symbol+stream.
// Returns nil (not error) if no watermark exists yet.
func (s *SQLiteStore) LoadReconciliationWatermark(
	ctx context.Context, symbol, stream string,
) (*models.ReconciliationWatermark, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("persistence: store is closed")
	}

	var exchangeTS, completedAt int64
	var stableID string
	err := s.db.QueryRowContext(ctx,
		`SELECT exchange_ts, stable_id, completed_at
		 FROM reconciliation_watermarks
		 WHERE symbol=? AND stream=?`,
		symbol, stream,
	).Scan(&exchangeTS, &stableID, &completedAt)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, writeFailure("load-reconciliation-watermark", err)
	}

	return &models.ReconciliationWatermark{
		Symbol:      symbol,
		Stream:      stream,
		ExchangeAt:  time.Unix(0, exchangeTS),
		StableID:    stableID,
		CompletedAt: time.Unix(0, completedAt),
	}, nil
}

// CommitReconciliationWatermark atomically persists the new watermark.
// This is only called after all pages are complete and all fill applies
// have been transactionally committed. Partial/parse/auth/DB failures
// must never call this.
func (s *SQLiteStore) CommitReconciliationWatermark(
	ctx context.Context, wm models.ReconciliationWatermark,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("persistence: store is closed")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return writeFailure("commit-watermark-begin", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO reconciliation_watermarks
			(symbol, stream, exchange_ts, stable_id, completed_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(symbol, stream) DO UPDATE SET
			exchange_ts = excluded.exchange_ts,
			stable_id = excluded.stable_id,
			completed_at = excluded.completed_at`,
		wm.Symbol, wm.Stream,
		wm.ExchangeAt.UnixNano(), wm.StableID,
		wm.CompletedAt.UnixNano(),
	)
	if err != nil {
		return writeFailure("commit-watermark-upsert", err)
	}

	if err := tx.Commit(); err != nil {
		return &PersistenceFailure{
			Operation: "commit-watermark",
			Class:     models.FailureCriticalCommit,
			Uncertain: true,
			Err:       fmt.Errorf("%w: %v", ErrCriticalCommitUncertain, err),
		}
	}
	committed = true
	return nil
}
