package models

import "github.com/shopspring/decimal"

// RiskLimits defines the risk management boundaries for the trading system.
type RiskLimits struct {
	MaxPositionPerSymbol decimal.Decimal `json:"maxPositionPerSymbol" yaml:"max_position_per_symbol"` // 单币种最大持仓（USDT计）
	MaxTotalPosition     decimal.Decimal `json:"maxTotalPosition" yaml:"max_total_position"`          // 总最大持仓
	MaxDailyLoss         decimal.Decimal `json:"maxDailyLoss" yaml:"max_daily_loss"`                  // 单日最大亏损
	MaxStrategyLoss      decimal.Decimal `json:"maxStrategyLoss" yaml:"max_strategy_loss"`            // 单策略最大亏损
	MaxOrdersPerSecond   int             `json:"maxOrdersPerSecond" yaml:"max_orders_per_second"`     // 每秒最大下单数
	MaxOpenOrders        int             `json:"maxOpenOrders" yaml:"max_open_orders"`                 // 最大挂单数
	MinSpreadBps         int             `json:"minSpreadBps" yaml:"min_spread_bps"`                   // 最小价差（基点）
	EmergencyStopLoss    decimal.Decimal `json:"emergencyStopLoss" yaml:"emergency_stop_loss"`        // 紧急止损线
}
