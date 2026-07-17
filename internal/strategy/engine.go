package strategy

import (
	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// StrategyStatus holds the runtime status of a strategy instance.
type StrategyStatus struct {
	StrategyID   string          // Unique strategy identifier
	Symbol       string          // Trading pair
	Type         string          // Strategy type: "grid" or "mean_reversion"
	IsActive     bool            // Whether the strategy is currently running
	RealizedPnL  decimal.Decimal // Cumulative realized PnL
	UnrealizedPnL decimal.Decimal // Current unrealized PnL
}

// PnLReport holds the profit and loss report for a strategy.
type PnLReport struct {
	StrategyID    string          // Strategy identifier
	RealizedPnL   decimal.Decimal // Cumulative realized PnL
	UnrealizedPnL decimal.Decimal // Current unrealized PnL
	TotalTrades   int             // Total number of completed trades
	WinRate       decimal.Decimal // Win rate percentage
}

// StrategyConfig is a union configuration that can represent either grid or mean reversion config.
type StrategyConfig struct {
	StrategyID      string                     // Unique strategy ID
	Type            string                     // "grid" or "mean_reversion"
	Grid            *models.GridConfig         // Grid config (nil if mean reversion)
	MeanReversion   *models.MeanReversionConfig // Mean reversion config (nil if grid)
}

// StrategyEngine defines the interface for managing and scheduling trading strategies.
type StrategyEngine interface {
	// LoadStrategy loads a strategy configuration and prepares it for execution.
	LoadStrategy(config StrategyConfig) error

	// StartStrategy activates a loaded strategy by its ID.
	StartStrategy(strategyID string) error

	// StopStrategy halts an active strategy by its ID.
	StopStrategy(strategyID string) error

	// OnMarketUpdate receives a market data update and routes it to relevant strategies.
	OnMarketUpdate(symbol string, tick *models.TickData)

	// OnOrderFill receives a fill event and routes it to the owning strategy.
	OnOrderFill(fill models.FillEvent)

	// GetActiveStrategies returns the status of all loaded strategies.
	GetActiveStrategies() []StrategyStatus

	// GetStrategyPnL returns the PnL report for a specific strategy.
	GetStrategyPnL(strategyID string) (*PnLReport, error)

	// StopAll halts all active strategies (used during emergency stop).
	StopAll()
}
