package models

import "github.com/shopspring/decimal"

// TickData represents real-time market tick data from OKX.
type TickData struct {
	Symbol     string          `json:"symbol"`     // 交易对, e.g. "BTC-USDT"
	Timestamp  int64           `json:"timestamp"`  // 微秒级时间戳
	LastPrice  decimal.Decimal `json:"lastPrice"`  // 最新成交价
	BestBid    decimal.Decimal `json:"bestBid"`    // 最优买价
	BestAsk    decimal.Decimal `json:"bestAsk"`    // 最优卖价
	BidSize    decimal.Decimal `json:"bidSize"`    // 最优买量
	AskSize    decimal.Decimal `json:"askSize"`    // 最优卖量
	Volume24h  decimal.Decimal `json:"volume24h"`  // 24小时成交量
	SequenceId int64           `json:"sequenceId"` // 数据序列号
}
