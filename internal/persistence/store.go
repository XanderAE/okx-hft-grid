package persistence

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// ErrCorrupted indicates that the database file is corrupted or unreadable.
var ErrCorrupted = errors.New("persistence: database is corrupted or unreadable")

// StrategyStateRow represents a row in the strategy_state table.
type StrategyStateRow struct {
	StrategyID string
	Type       string
	IsActive   bool
	ConfigJSON []byte
	StateJSON  []byte
}

// SQLiteStore implements state persistence using SQLite with WAL mode.
type SQLiteStore struct {
	db         *sql.DB
	mu         sync.Mutex
	dirty      bool
	flushTimer *time.Timer
	closed     bool
}

// NewSQLiteStore opens a SQLite database with the durability contract used by
// the production recovery path: WAL, synchronous=FULL, foreign keys, and
// BEGIN IMMEDIATE for every database/sql transaction. Schema changes are
// additive so databases created by older releases remain readable.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	dsn := dbPath + "?_journal_mode=WAL&_busy_timeout=5000&_synchronous=FULL&_foreign_keys=on&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupted, err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("%w: %v", ErrCorrupted, err)
	}

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=FULL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%w: failed to apply %s: %v", ErrCorrupted, pragma, err)
		}
	}

	if err := createTables(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("%w: failed to create legacy-compatible tables: %v", ErrCorrupted, err)
	}
	if err := migrateDurableSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("%w: failed to migrate durable schema: %v", ErrCorrupted, err)
	}

	return &SQLiteStore{db: db}, nil
}

func createTables(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS orders (
			order_id TEXT PRIMARY KEY,
			exchange_order_id TEXT NOT NULL DEFAULT '',
			symbol TEXT NOT NULL,
			side INTEGER NOT NULL,
			price TEXT NOT NULL,
			quantity TEXT NOT NULL,
			filled_qty TEXT NOT NULL DEFAULT '0',
			status INTEGER NOT NULL,
			strategy_id TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS positions (
			symbol TEXT PRIMARY KEY,
			side INTEGER NOT NULL,
			quantity TEXT NOT NULL,
			avg_entry_price TEXT NOT NULL,
			unrealized_pnl TEXT NOT NULL DEFAULT '0',
			realized_pnl TEXT NOT NULL DEFAULT '0'
		)`,
		`CREATE TABLE IF NOT EXISTS strategy_state (
			strategy_id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			is_active INTEGER NOT NULL DEFAULT 0,
			config_json BLOB,
			state_json BLOB
		)`,
	}

	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// SaveOrder persists an order to the database. If the order already exists, it is updated.
func (s *SQLiteStore) SaveOrder(order *models.Order) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return errors.New("persistence: store is closed")
	}

	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO orders (order_id, exchange_order_id, symbol, side, price, quantity, filled_qty, status, strategy_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		order.OrderId,
		order.ExchangeOrderId,
		order.Symbol,
		int(order.Side),
		order.Price.String(),
		order.Quantity.String(),
		order.FilledQuantity.String(),
		int(order.Status),
		order.StrategyId,
		order.CreateTime,
		order.UpdateTime,
	)
	if err != nil {
		return fmt.Errorf("persistence: failed to save order: %w", err)
	}

	s.markDirty()
	return nil
}

// LoadOrders retrieves all persisted orders from the database.
func (s *SQLiteStore) LoadOrders() ([]*models.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, errors.New("persistence: store is closed")
	}

	rows, err := s.db.Query(`
		SELECT order_id, exchange_order_id, symbol, side, price, quantity, filled_qty, status, strategy_id, created_at, updated_at
		FROM orders`)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to load orders: %v", ErrCorrupted, err)
	}
	defer rows.Close()

	var orders []*models.Order
	for rows.Next() {
		var (
			orderID, exchangeOrderID, symbol, priceStr, qtyStr, filledQtyStr, strategyID string
			side, status                                                                   int
			createdAt, updatedAt                                                           int64
		)
		if err := rows.Scan(&orderID, &exchangeOrderID, &symbol, &side, &priceStr, &qtyStr, &filledQtyStr, &status, &strategyID, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("%w: failed to scan order row: %v", ErrCorrupted, err)
		}

		price, err := decimal.NewFromString(priceStr)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid price value: %v", ErrCorrupted, err)
		}
		qty, err := decimal.NewFromString(qtyStr)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid quantity value: %v", ErrCorrupted, err)
		}
		filledQty, err := decimal.NewFromString(filledQtyStr)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid filled_qty value: %v", ErrCorrupted, err)
		}

		orders = append(orders, &models.Order{
			OrderId:         orderID,
			ExchangeOrderId: exchangeOrderID,
			Symbol:          symbol,
			Side:            models.Side(side),
			Price:           price,
			Quantity:        qty,
			FilledQuantity:  filledQty,
			Status:          models.OrderStatus(status),
			StrategyId:      strategyID,
			CreateTime:      createdAt,
			UpdateTime:      updatedAt,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: error iterating order rows: %v", ErrCorrupted, err)
	}

	return orders, nil
}

// SavePosition persists a position to the database. If the position already exists, it is updated.
func (s *SQLiteStore) SavePosition(pos *models.Position) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return errors.New("persistence: store is closed")
	}

	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO positions (symbol, side, quantity, avg_entry_price, unrealized_pnl, realized_pnl)
		VALUES (?, ?, ?, ?, ?, ?)`,
		pos.Symbol,
		int(pos.Side),
		pos.Quantity.String(),
		pos.AvgEntryPrice.String(),
		pos.UnrealizedPnL.String(),
		pos.RealizedPnL.String(),
	)
	if err != nil {
		return fmt.Errorf("persistence: failed to save position: %w", err)
	}

	s.markDirty()
	return nil
}

// LoadPositions retrieves all persisted positions from the database.
func (s *SQLiteStore) LoadPositions() ([]*models.Position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, errors.New("persistence: store is closed")
	}

	rows, err := s.db.Query(`
		SELECT symbol, side, quantity, avg_entry_price, unrealized_pnl, realized_pnl
		FROM positions`)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to load positions: %v", ErrCorrupted, err)
	}
	defer rows.Close()

	var positions []*models.Position
	for rows.Next() {
		var (
			symbol, qtyStr, avgPriceStr, unrealizedStr, realizedStr string
			side                                                     int
		)
		if err := rows.Scan(&symbol, &side, &qtyStr, &avgPriceStr, &unrealizedStr, &realizedStr); err != nil {
			return nil, fmt.Errorf("%w: failed to scan position row: %v", ErrCorrupted, err)
		}

		qty, err := decimal.NewFromString(qtyStr)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid quantity value: %v", ErrCorrupted, err)
		}
		avgPrice, err := decimal.NewFromString(avgPriceStr)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid avg_entry_price value: %v", ErrCorrupted, err)
		}
		unrealized, err := decimal.NewFromString(unrealizedStr)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid unrealized_pnl value: %v", ErrCorrupted, err)
		}
		realized, err := decimal.NewFromString(realizedStr)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid realized_pnl value: %v", ErrCorrupted, err)
		}

		positions = append(positions, &models.Position{
			Symbol:        symbol,
			Side:          models.Side(side),
			Quantity:      qty,
			AvgEntryPrice: avgPrice,
			UnrealizedPnL: unrealized,
			RealizedPnL:   realized,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: error iterating position rows: %v", ErrCorrupted, err)
	}

	return positions, nil
}

// SaveStrategyState persists a strategy state entry.
func (s *SQLiteStore) SaveStrategyState(id, stateType string, isActive bool, configJSON, stateJSON []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return errors.New("persistence: store is closed")
	}

	// Validate JSON if provided
	if len(configJSON) > 0 && !json.Valid(configJSON) {
		return errors.New("persistence: invalid config JSON")
	}
	if len(stateJSON) > 0 && !json.Valid(stateJSON) {
		return errors.New("persistence: invalid state JSON")
	}

	isActiveInt := 0
	if isActive {
		isActiveInt = 1
	}

	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO strategy_state (strategy_id, type, is_active, config_json, state_json)
		VALUES (?, ?, ?, ?, ?)`,
		id, stateType, isActiveInt, configJSON, stateJSON,
	)
	if err != nil {
		return fmt.Errorf("persistence: failed to save strategy state: %w", err)
	}

	s.markDirty()
	return nil
}

// LoadStrategyStates retrieves all persisted strategy state entries.
func (s *SQLiteStore) LoadStrategyStates() ([]StrategyStateRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, errors.New("persistence: store is closed")
	}

	rows, err := s.db.Query(`
		SELECT strategy_id, type, is_active, config_json, state_json
		FROM strategy_state`)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to load strategy states: %v", ErrCorrupted, err)
	}
	defer rows.Close()

	var states []StrategyStateRow
	for rows.Next() {
		var (
			strategyID, stateType string
			isActive              int
			configJSON, stateJSON []byte
		)
		if err := rows.Scan(&strategyID, &stateType, &isActive, &configJSON, &stateJSON); err != nil {
			return nil, fmt.Errorf("%w: failed to scan strategy state row: %v", ErrCorrupted, err)
		}

		states = append(states, StrategyStateRow{
			StrategyID: strategyID,
			Type:       stateType,
			IsActive:   isActive != 0,
			ConfigJSON: configJSON,
			StateJSON:  stateJSON,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: error iterating strategy state rows: %v", ErrCorrupted, err)
	}

	return states, nil
}

// Close flushes any pending writes and closes the database connection.
func (s *SQLiteStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	if s.flushTimer != nil {
		s.flushTimer.Stop()
		s.flushTimer = nil
	}

	return s.db.Close()
}

// markDirty sets the dirty flag and schedules a flush within 1 second.
// Must be called with s.mu held.
func (s *SQLiteStore) markDirty() {
	s.dirty = true
	if s.flushTimer == nil {
		s.flushTimer = time.AfterFunc(1*time.Second, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.dirty = false
			s.flushTimer = nil
			// WAL mode ensures durability via write-ahead logging.
			// The timer here tracks that persistence happens within 1 second of state change.
		})
	}
}
