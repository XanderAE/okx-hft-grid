package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// GridType represents the grid spacing type.
type GridType int

const (
	GridTypeArithmetic GridType = iota // 等差网格
	GridTypeGeometric                  // 等比网格
)

// String returns the string representation of GridType.
func (t GridType) String() string {
	switch t {
	case GridTypeArithmetic:
		return "ARITHMETIC"
	case GridTypeGeometric:
		return "GEOMETRIC"
	default:
		return "UNKNOWN"
	}
}

// GridConfig holds the configuration for a grid trading strategy.
type GridConfig struct {
	Symbol          string          `json:"symbol" yaml:"symbol"`                     // 交易对
	UpperPrice      decimal.Decimal `json:"upperPrice" yaml:"upper_price"`            // 网格上界
	LowerPrice      decimal.Decimal `json:"lowerPrice" yaml:"lower_price"`            // 网格下界
	GridCount       int             `json:"gridCount" yaml:"grid_count"`              // 网格数量
	GridType        GridType        `json:"gridType" yaml:"grid_type"`                // 等差/等比
	OrderSize       decimal.Decimal `json:"orderSize" yaml:"order_size"`              // 每格下单量
	MaxPosition     decimal.Decimal `json:"maxPosition" yaml:"max_position"`          // 最大持仓量
	FeeRate         decimal.Decimal `json:"feeRate" yaml:"fee_rate"`                  // 单边手续费率 (e.g., 0.001 = 0.1%)
	TakeProfitRatio decimal.Decimal `json:"takeProfitRatio" yaml:"take_profit_ratio"` // 止盈比例（可选）
	StopLossRatio   decimal.Decimal `json:"stopLossRatio" yaml:"stop_loss_ratio"`     // 止损比例（可选）
	ReinvestProfit  bool            `json:"reinvestProfit" yaml:"reinvest_profit"`     // 是否复投利润
	Drift           *DriftConfig    `json:"drift,omitempty" yaml:"drift,omitempty"`    // Drift configuration (nil = disabled)
}

// GridLevel represents a single price level in the grid.
type GridLevel struct {
	Index        int             `json:"index"`        // 网格档位索引
	Price        decimal.Decimal `json:"price"`        // 价格
	HasBuyOrder  bool            `json:"hasBuyOrder"`  // 是否有买单
	HasSellOrder bool            `json:"hasSellOrder"` // 是否有卖单
	OrderId      string          `json:"orderId"`      // 当前挂单ID
}

// DriftDirection represents the direction of a grid drift.
type DriftDirection int

const (
	DriftNone DriftDirection = iota // No drift
	DriftUp                         // Drift grid upward
	DriftDown                       // Drift grid downward
)

// String returns the string representation of DriftDirection.
func (d DriftDirection) String() string {
	switch d {
	case DriftUp:
		return "UP"
	case DriftDown:
		return "DOWN"
	default:
		return "NONE"
	}
}

// DriftConfig holds configuration for automatic grid range relocation.
type DriftConfig struct {
	Enabled        bool            `json:"enabled" yaml:"enabled"`                // Enable/disable drift
	DriftThreshold decimal.Decimal `json:"driftThreshold" yaml:"drift_threshold"` // Boundary zone as fraction of range [0.01, 0.50]
	DriftStep      int             `json:"driftStep" yaml:"drift_step"`           // Grid intervals to shift per drift (positive integer)
	CooldownPeriod time.Duration   `json:"cooldownPeriod" yaml:"cooldown_period"` // Minimum time between drifts (min 5s, default 30s)
	MaxDrifts      int             `json:"maxDrifts" yaml:"max_drifts"`           // Max drifts per session (0 = unlimited)
}

// DriftEvent records a single drift operation for audit and replay.
type DriftEvent struct {
	Timestamp       time.Time       `json:"timestamp"`
	Direction       DriftDirection  `json:"direction"`
	TriggerReason   string          `json:"triggerReason"`
	TriggerPrice    decimal.Decimal `json:"triggerPrice"`
	OldLower        decimal.Decimal `json:"oldLower"`
	OldUpper        decimal.Decimal `json:"oldUpper"`
	NewLower        decimal.Decimal `json:"newLower"`
	NewUpper        decimal.Decimal `json:"newUpper"`
	OrdersCancelled int             `json:"ordersCancelled"`
	OrdersPlaced    int             `json:"ordersPlaced"`
	OrdersFailed    int             `json:"ordersFailed"`
	ElapsedMs       int64           `json:"elapsedMs"`
	Success         bool            `json:"success"`
	AbortReason     string          `json:"abortReason,omitempty"`
}
