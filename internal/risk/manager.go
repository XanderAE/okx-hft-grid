package risk

import (
	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// RiskDecision represents the outcome of a risk check.
type RiskDecision struct {
	Approved bool     // Whether the order is approved
	Reasons  []string // Rejection reasons (empty if approved)
}

// RiskMetrics holds the current risk state summary.
type RiskMetrics struct {
	DailyPnL            decimal.Decimal            // Today's realized + unrealized PnL
	TotalExposure       decimal.Decimal            // Sum of absolute notional across all symbols
	PositionsBySymbol   map[string]decimal.Decimal // Per-symbol notional exposure
	OpenOrdersBySymbol  map[string]int             // Per-symbol open order count
	OrdersInLastSecond  int                        // Orders submitted in the last 1s window
	EmergencyStopActive bool                       // Whether emergency stop is engaged
	EmergencyStopReason string                     // Reason for emergency stop (if active)
}

// RiskManager defines the interface for real-time trading risk management.
type RiskManager interface {
	// CheckOrder evaluates a single order against all risk rules.
	// Returns a RiskDecision indicating approval or rejection with reasons.
	CheckOrder(order *OrderRequest) *RiskDecision

	// CheckBatchOrders evaluates a batch of orders against all risk rules.
	// Returns a RiskDecision for the entire batch (rejected if any check fails).
	CheckBatchOrders(orders []*OrderRequest) *RiskDecision

	// UpdatePosition updates the tracked position for a symbol.
	UpdatePosition(symbol string, position *models.Position)

	// UpdatePnL updates the PnL for a specific strategy.
	UpdatePnL(strategyID string, pnl decimal.Decimal)

	// GetRiskMetrics returns the current risk metrics snapshot.
	GetRiskMetrics() *RiskMetrics

	// SetRiskLimits configures the risk limits.
	SetRiskLimits(limits *models.RiskLimits)

	// EmergencyStop triggers emergency stop: cancels all orders, halts strategies, sends CRITICAL alert.
	// All trading operations are rejected until manual confirmation.
	EmergencyStop(reason string)

	// ResumeFromEmergencyStop deactivates emergency stop after manual confirmation.
	ResumeFromEmergencyStop() error

	// IsEmergencyStopActive returns whether emergency stop is currently active.
	IsEmergencyStopActive() bool
}

// OrderRequest is re-exported here for convenience in risk check signatures.
// This avoids circular imports with the execution package.
type OrderRequest struct {
	Symbol     string           // Trading pair
	Side       models.Side      // BUY or SELL
	OrderType  models.OrderType // Order type
	Price      decimal.Decimal  // Order price
	Quantity   decimal.Decimal  // Order quantity
	StrategyID string           // Associated strategy ID
	SpreadBps  int              // Current spread in basis points (for min spread check)
}
