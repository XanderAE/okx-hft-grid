package models

import "github.com/shopspring/decimal"

// Position represents the current holding for a trading pair.
type Position struct {
	Symbol         string          `json:"symbol"`         // 交易对
	Side           Side            `json:"side"`           // 持仓方向
	Quantity       decimal.Decimal `json:"quantity"`       // 持仓数量
	AvgEntryPrice  decimal.Decimal `json:"avgEntryPrice"`  // 平均入场价
	UnrealizedPnL  decimal.Decimal `json:"unrealizedPnL"`  // 未实现盈亏
	RealizedPnL    decimal.Decimal `json:"realizedPnL"`    // 已实现盈亏
	LastUpdateTime int64           `json:"lastUpdateTime"` // 最后更新时间
}
