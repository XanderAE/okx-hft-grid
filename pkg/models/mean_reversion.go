package models

import "github.com/shopspring/decimal"

// MAType represents the type of moving average calculation.
type MAType int

const (
	MATypeSMA  MAType = iota // 简单移动平均
	MATypeEMA                // 指数移动平均
	MATypeVWAP               // 成交量加权均价
)

// String returns the string representation of MAType.
func (t MAType) String() string {
	switch t {
	case MATypeSMA:
		return "SMA"
	case MATypeEMA:
		return "EMA"
	case MATypeVWAP:
		return "VWAP"
	default:
		return "UNKNOWN"
	}
}

// SignalDirection represents the direction of a trading signal.
type SignalDirection int

const (
	SignalDirectionNone  SignalDirection = iota // 无信号
	SignalDirectionBuy                         // 买入信号
	SignalDirectionSell                        // 卖出信号
	SignalDirectionClose                       // 平仓信号
)

// String returns the string representation of SignalDirection.
func (s SignalDirection) String() string {
	switch s {
	case SignalDirectionNone:
		return "NONE"
	case SignalDirectionBuy:
		return "BUY"
	case SignalDirectionSell:
		return "SELL"
	case SignalDirectionClose:
		return "CLOSE"
	default:
		return "UNKNOWN"
	}
}

// MeanReversionConfig holds the configuration for a mean reversion strategy.
type MeanReversionConfig struct {
	Symbol         string          `json:"symbol" yaml:"symbol"`                   // 交易对
	LookbackPeriod int             `json:"lookbackPeriod" yaml:"lookback_period"`  // 回看周期
	EntryThreshold decimal.Decimal `json:"entryThreshold" yaml:"entry_threshold"` // 入场偏离阈值（标准差倍数）
	ExitThreshold  decimal.Decimal `json:"exitThreshold" yaml:"exit_threshold"`   // 出场阈值
	MAType         MAType          `json:"maType" yaml:"ma_type"`                  // 均线类型
	OrderSize      decimal.Decimal `json:"orderSize" yaml:"order_size"`            // 每次下单量
	MaxPosition    decimal.Decimal `json:"maxPosition" yaml:"max_position"`        // 最大持仓量
	CooldownMs     int             `json:"cooldownMs" yaml:"cooldown_ms"`          // 信号冷却时间（毫秒）
}
