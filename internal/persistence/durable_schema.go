package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const durableSchemaVersion = 1

// migrateDurableSchema applies only additive DDL. Existing order, position and
// strategy_state tables are intentionally left in place for preservation and
// legacy migration compatibility.
func migrateDurableSchema(db *sql.DB) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin additive migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS fill_ledger (
			fill_key TEXT PRIMARY KEY,
			symbol TEXT NOT NULL,
			exchange_order_id TEXT NOT NULL,
			exchange_fill_id TEXT NOT NULL,
			side INTEGER NOT NULL,
			cumulative_qty TEXT NOT NULL,
			delta_qty TEXT NOT NULL,
			fill_price TEXT NOT NULL,
			fee TEXT NOT NULL DEFAULT '0',
			source TEXT NOT NULL,
			exchange_ts INTEGER NOT NULL DEFAULT 0,
			observed_at INTEGER NOT NULL,
			eligibility TEXT NOT NULL,
			ineligible_reason TEXT NOT NULL DEFAULT '',
			UNIQUE(symbol, exchange_order_id, exchange_fill_id, cumulative_qty)
		)`,
		`CREATE TABLE IF NOT EXISTS order_fill_watermarks (
			symbol TEXT NOT NULL,
			exchange_order_id TEXT NOT NULL,
			processed_cumulative_qty TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(symbol, exchange_order_id)
		)`,
		`CREATE TABLE IF NOT EXISTS grid_state (
			symbol TEXT PRIMARY KEY,
			strategy_id TEXT NOT NULL DEFAULT '',
			position TEXT NOT NULL DEFAULT '0',
			avg_entry_price TEXT NOT NULL DEFAULT '0',
			realized_pnl TEXT NOT NULL DEFAULT '0',
			total_buys INTEGER NOT NULL DEFAULT 0,
			total_sells INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS counter_order_intents (
			intent_id TEXT PRIMARY KEY,
			fill_key TEXT NOT NULL UNIQUE,
			symbol TEXT NOT NULL,
			side INTEGER NOT NULL,
			price TEXT NOT NULL,
			quantity TEXT NOT NULL,
			purpose TEXT NOT NULL,
			deterministic_client_order_id TEXT NOT NULL UNIQUE,
			observed_at INTEGER NOT NULL,
			initiation_deadline INTEGER NOT NULL,
			terminal_deadline INTEGER NOT NULL,
			initiated_at INTEGER NOT NULL DEFAULT 0,
			terminal_at INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			exchange_order_id TEXT NOT NULL DEFAULT '',
			final_error_class TEXT NOT NULL DEFAULT '',
			final_scode TEXT NOT NULL DEFAULT '',
			final_smsg TEXT NOT NULL DEFAULT '',
			FOREIGN KEY(fill_key) REFERENCES fill_ledger(fill_key)
		)`,
		`CREATE TABLE IF NOT EXISTS order_outbox (
			outbox_id TEXT PRIMARY KEY,
			intent_id TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL,
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_until INTEGER NOT NULL DEFAULT 0,
			next_attempt_at INTEGER NOT NULL,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			last_error_class TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY(intent_id) REFERENCES counter_order_intents(intent_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_order_outbox_ready
			ON order_outbox(status, next_attempt_at, lease_until)`,
		`CREATE TABLE IF NOT EXISTS reconciliation_watermarks (
			symbol TEXT NOT NULL,
			stream TEXT NOT NULL,
			exchange_ts INTEGER NOT NULL,
			stable_id TEXT NOT NULL,
			completed_at INTEGER NOT NULL,
			PRIMARY KEY(symbol, stream)
		)`,
		`CREATE TABLE IF NOT EXISTS bot_orders (
			client_order_id TEXT PRIMARY KEY,
			exchange_order_id TEXT,
			symbol TEXT NOT NULL,
			strategy_id TEXT NOT NULL DEFAULT '',
			purpose TEXT NOT NULL,
			parent_order_id TEXT NOT NULL DEFAULT '',
			intent_id TEXT NOT NULL DEFAULT '',
			side INTEGER NOT NULL,
			state INTEGER NOT NULL,
			cumulative_fill_qty TEXT NOT NULL DEFAULT '0',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_orders_exchange_lineage
			ON bot_orders(symbol, exchange_order_id)
			WHERE exchange_order_id IS NOT NULL AND exchange_order_id <> ''`,
		`CREATE TABLE IF NOT EXISTS safe_stop_state (
			scope TEXT NOT NULL,
			symbol TEXT NOT NULL DEFAULT '',
			reason_code TEXT NOT NULL,
			active INTEGER NOT NULL,
			since INTEGER NOT NULL,
			recovery_epoch INTEGER NOT NULL DEFAULT 0,
			details TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(scope, symbol, reason_code)
		)`,
		`CREATE TABLE IF NOT EXISTS rebalance_outcomes (
			run_id TEXT NOT NULL,
			symbol TEXT NOT NULL,
			old_client_order_id TEXT NOT NULL DEFAULT '',
			old_exchange_order_id TEXT NOT NULL,
			reference_price TEXT NOT NULL,
			reference_age_ms INTEGER NOT NULL,
			deviation TEXT NOT NULL,
			terminal_outcome TEXT NOT NULL,
			replacement_client_order_id TEXT NOT NULL DEFAULT '',
			error_class TEXT NOT NULL DEFAULT '',
			recorded_at INTEGER NOT NULL,
			PRIMARY KEY(run_id, old_exchange_order_id)
		)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("apply additive migration: %w", err)
		}
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (?, ?)`, durableSchemaVersion, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("record additive migration: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", durableSchemaVersion)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit additive migration: %w", err)
	}
	committed = true
	return nil
}
