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

// FillApplyOpts controls the behavior of ObserveFill within a transaction.
type FillApplyOpts struct {
	// Now supplies the current timestamp for deadlines and records. In
	// production this is time.Now; property tests inject a fake clock.
	Now func() time.Time
}

// ObserveFill implements the atomic fill→intent→outbox path described in the
// design. It executes entirely within a DurableTx and performs NO network I/O.
//
// Return values:
//   - FillApplyResult describes what was persisted (or if duplicate/ooo).
//   - An error wraps ErrUnownedFill or ErrInvariantViolation on caller logic
//     errors; DurableTx commit failures are classified by WithImmediateTx.
func ObserveFill(
	ctx context.Context,
	dtx *DurableTx,
	observation models.FillObservation,
	plan models.CounterOrderPlan,
	opts FillApplyOpts,
) (*models.FillApplyResult, error) {
	if err := models.ValidateFillObservation(observation); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvariantViolation, err)
	}

	fillKey, err := models.CanonicalUniqueFillKey(observation)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvariantViolation, err)
	}

	now := time.Now()
	if opts.Now != nil {
		now = opts.Now()
	}

	// 1. Compute delta from persisted watermark
	var prevQtyStr sql.NullString
	err = dtx.QueryRowContext(ctx,
		`SELECT processed_cumulative_qty FROM order_fill_watermarks WHERE symbol=? AND exchange_order_id=?`,
		observation.Symbol, observation.ExchangeOrderID,
	).Scan(&prevQtyStr)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, writeFailure("read-watermark", err)
	}

	previous := decimal.Zero
	if prevQtyStr.Valid {
		previous, err = decimal.NewFromString(prevQtyStr.String)
		if err != nil {
			return nil, fmt.Errorf("%w: corrupted watermark: %v", ErrInvariantViolation, err)
		}
	}

	delta, isNew, err := models.CumulativeDelta(previous, observation.CumulativeQuantity)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvariantViolation, err)
	}

	result := &models.FillApplyResult{}

	if !isNew {
		// Duplicate or out-of-order: idempotent, no second intent.
		result.Duplicate = true
		if observation.CumulativeQuantity.LessThan(previous) {
			result.OutOfOrder = true
		}
		return result, nil
	}

	// 2. Attempt insert into fill_ledger (unique constraint is final dedupe).
	res, err := dtx.ExecContext(ctx,
		`INSERT OR IGNORE INTO fill_ledger
			(fill_key, symbol, exchange_order_id, exchange_fill_id, side,
			 cumulative_qty, delta_qty, fill_price, fee, source,
			 exchange_ts, observed_at, eligibility, ineligible_reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fillKey,
		observation.Symbol,
		observation.ExchangeOrderID,
		observation.ExchangeFillID,
		int(observation.Side),
		observation.CumulativeQuantity.String(),
		delta.String(),
		observation.FillPrice.String(),
		observation.Fee.String(),
		string(observation.Source),
		observation.ExchangeTimestamp.UnixNano(),
		observation.ObservedAt.UnixNano(),
		string(plan.Eligibility),
		plan.IneligibleReason,
	)
	if err != nil {
		return nil, writeFailure("insert-fill-ledger", err)
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		// Already exists via unique constraint: treat as duplicate.
		result.Duplicate = true
		return result, nil
	}

	result.Delta = delta
	result.Ledger = &models.FillLedgerRecord{
		FillKey:            fillKey,
		Symbol:             observation.Symbol,
		ExchangeOrderID:    observation.ExchangeOrderID,
		ExchangeFillID:     observation.ExchangeFillID,
		Side:               observation.Side,
		CumulativeQuantity: observation.CumulativeQuantity,
		DeltaQuantity:      delta,
		FillPrice:          observation.FillPrice,
		Fee:                observation.Fee,
		Source:             observation.Source,
		ExchangeTimestamp:  observation.ExchangeTimestamp,
		ObservedAt:         observation.ObservedAt,
		Eligibility:        plan.Eligibility,
		IneligibleReason:   plan.IneligibleReason,
	}

	// 3. Update cumulative watermark.
	_, err = dtx.ExecContext(ctx,
		`INSERT INTO order_fill_watermarks (symbol, exchange_order_id, processed_cumulative_qty, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(symbol, exchange_order_id) DO UPDATE SET
			processed_cumulative_qty = excluded.processed_cumulative_qty,
			updated_at = excluded.updated_at`,
		observation.Symbol, observation.ExchangeOrderID,
		observation.CumulativeQuantity.String(), now.UnixNano(),
	)
	if err != nil {
		return nil, writeFailure("update-watermark", err)
	}

	// 4. Update GridState (position/PnL) for the delta.
	gridState, err := applyGridStateDelta(ctx, dtx, observation, delta, now)
	if err != nil {
		return nil, err
	}
	result.GridState = gridState

	// 5. Create intent + outbox for eligible BUY fills.
	if observation.Side == models.SideBuy && plan.Eligibility == models.FillEligible {
		intent, outbox, err := createCounterIntent(ctx, dtx, fillKey, observation, plan, delta, now)
		if err != nil {
			return nil, err
		}
		result.Intent = intent
		result.Outbox = outbox
	}

	return result, nil
}

func applyGridStateDelta(
	ctx context.Context,
	dtx *DurableTx,
	obs models.FillObservation,
	delta decimal.Decimal,
	now time.Time,
) (*models.GridStateRecord, error) {
	// Read current grid state
	var posStr, avgStr, pnlStr, strategyID string
	var totalBuys, totalSells int64
	err := dtx.QueryRowContext(ctx,
		`SELECT strategy_id, position, avg_entry_price, realized_pnl, total_buys, total_sells
		FROM grid_state WHERE symbol=?`, obs.Symbol,
	).Scan(&strategyID, &posStr, &avgStr, &pnlStr, &totalBuys, &totalSells)

	isNew := errors.Is(err, sql.ErrNoRows)
	if err != nil && !isNew {
		return nil, writeFailure("read-grid-state", err)
	}

	position := decimal.Zero
	avgEntry := decimal.Zero
	realizedPnL := decimal.Zero
	if !isNew {
		position, _ = decimal.NewFromString(posStr)
		avgEntry, _ = decimal.NewFromString(avgStr)
		realizedPnL, _ = decimal.NewFromString(pnlStr)
	}

	// Apply standard grid PnL semantics:
	// BUY: increase position, update average entry
	// SELL: decrease position, realize PnL
	switch obs.Side {
	case models.SideBuy:
		cost := position.Mul(avgEntry).Add(delta.Mul(obs.FillPrice))
		position = position.Add(delta)
		if position.IsPositive() {
			avgEntry = cost.Div(position)
		}
		totalBuys++
	case models.SideSell:
		if position.IsPositive() && avgEntry.IsPositive() {
			profit := delta.Mul(obs.FillPrice.Sub(avgEntry)).Sub(obs.Fee)
			realizedPnL = realizedPnL.Add(profit)
		}
		position = position.Sub(delta)
		if position.IsNegative() {
			position = decimal.Zero
		}
		totalSells++
	}

	sid := obs.StrategyID
	if sid == "" {
		sid = strategyID
	}

	_, err = dtx.ExecContext(ctx,
		`INSERT INTO grid_state (symbol, strategy_id, position, avg_entry_price, realized_pnl, total_buys, total_sells, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(symbol) DO UPDATE SET
			strategy_id = excluded.strategy_id,
			position = excluded.position,
			avg_entry_price = excluded.avg_entry_price,
			realized_pnl = excluded.realized_pnl,
			total_buys = excluded.total_buys,
			total_sells = excluded.total_sells,
			updated_at = excluded.updated_at`,
		obs.Symbol, sid,
		position.String(), avgEntry.String(), realizedPnL.String(),
		totalBuys, totalSells, now.UnixNano(),
	)
	if err != nil {
		return nil, writeFailure("update-grid-state", err)
	}

	return &models.GridStateRecord{
		Symbol:        obs.Symbol,
		StrategyID:    sid,
		Position:      position,
		AvgEntryPrice: avgEntry,
		RealizedPnL:   realizedPnL,
		TotalBuys:     totalBuys,
		TotalSells:    totalSells,
		UpdatedAt:     now,
	}, nil
}

func createCounterIntent(
	ctx context.Context,
	dtx *DurableTx,
	fillKey string,
	obs models.FillObservation,
	plan models.CounterOrderPlan,
	delta decimal.Decimal,
	now time.Time,
) (*models.CounterOrderIntent, *models.OutboxRecord, error) {
	intentID, err := models.CounterIntentID(fillKey, obs.Symbol, plan.Purpose)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvariantViolation, err)
	}

	clOrdID, err := models.DeterministicClientOrderID(intentID, obs.Symbol, plan.Purpose)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvariantViolation, err)
	}

	initiationDeadline := obs.ObservedAt.Add(models.CounterOrderInitiationSLO)
	terminalDeadline := obs.ObservedAt.Add(models.CounterOrderTerminalSLO)

	intent := &models.CounterOrderIntent{
		IntentID:                   intentID,
		FillKey:                    fillKey,
		Symbol:                     obs.Symbol,
		Side:                       models.SideSell,
		Price:                      plan.Price,
		Quantity:                   delta,
		Purpose:                    plan.Purpose,
		DeterministicClientOrderID: clOrdID,
		ObservedAt:                 obs.ObservedAt,
		InitiationDeadline:         initiationDeadline,
		TerminalDeadline:           terminalDeadline,
		Status:                     models.IntentPending,
	}

	_, err = dtx.ExecContext(ctx,
		`INSERT INTO counter_order_intents
			(intent_id, fill_key, symbol, side, price, quantity, purpose,
			 deterministic_client_order_id, observed_at, initiation_deadline,
			 terminal_deadline, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		intent.IntentID, intent.FillKey, intent.Symbol,
		int(intent.Side), intent.Price.String(), intent.Quantity.String(),
		intent.Purpose, intent.DeterministicClientOrderID,
		intent.ObservedAt.UnixNano(), initiationDeadline.UnixNano(),
		terminalDeadline.UnixNano(), string(intent.Status),
	)
	if err != nil {
		return nil, nil, writeFailure("insert-counter-intent", err)
	}

	outboxID := "ob_" + intentID[4:] // derive from intent_id
	outbox := &models.OutboxRecord{
		OutboxID:      outboxID,
		IntentID:      intentID,
		Status:        models.OutboxPending,
		NextAttemptAt: now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	_, err = dtx.ExecContext(ctx,
		`INSERT INTO order_outbox
			(outbox_id, intent_id, status, next_attempt_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		outbox.OutboxID, outbox.IntentID, string(outbox.Status),
		outbox.NextAttemptAt.UnixNano(), outbox.CreatedAt.UnixNano(), outbox.UpdatedAt.UnixNano(),
	)
	if err != nil {
		return nil, nil, writeFailure("insert-outbox", err)
	}

	return intent, outbox, nil
}
