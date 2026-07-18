package models

import "github.com/shopspring/decimal"

// OrderStatus represents the lifecycle state of an order.
type OrderStatus int

const (
	OrderStatusPending         OrderStatus = iota // 等待提交
	OrderStatusSubmitted                          // 已提交
	OrderStatusOpen                               // 已挂单
	OrderStatusPartiallyFilled                    // 部分成交
	OrderStatusFilled                             // 完全成交
	OrderStatusCancelled                          // 已撤销
	OrderStatusRejected                           // 被拒绝
	OrderStatusExpired                            // 已过期
	OrderStatusError                              // 错误状态
)

// String returns the string representation of OrderStatus.
func (s OrderStatus) String() string {
	switch s {
	case OrderStatusPending:
		return "PENDING"
	case OrderStatusSubmitted:
		return "SUBMITTED"
	case OrderStatusOpen:
		return "OPEN"
	case OrderStatusPartiallyFilled:
		return "PARTIALLY_FILLED"
	case OrderStatusFilled:
		return "FILLED"
	case OrderStatusCancelled:
		return "CANCELLED"
	case OrderStatusRejected:
		return "REJECTED"
	case OrderStatusExpired:
		return "EXPIRED"
	case OrderStatusError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Side represents the order direction.
type Side int

const (
	SideBuy  Side = iota // 买入
	SideSell             // 卖出
)

// String returns the string representation of Side.
func (s Side) String() string {
	switch s {
	case SideBuy:
		return "BUY"
	case SideSell:
		return "SELL"
	default:
		return "UNKNOWN"
	}
}

// OrderType represents the type of order.
type OrderType int

const (
	OrderTypeLimit    OrderType = iota // 限价单
	OrderTypeMarket                    // 市价单
	OrderTypePostOnly                  // 仅挂单 (Maker Only)
)

// String returns the string representation of OrderType.
func (t OrderType) String() string {
	switch t {
	case OrderTypeLimit:
		return "LIMIT"
	case OrderTypeMarket:
		return "MARKET"
	case OrderTypePostOnly:
		return "POST_ONLY"
	default:
		return "UNKNOWN"
	}
}

// Order represents a trading order with full lifecycle information.
type Order struct {
	OrderId         string          `json:"orderId"`         // 本地订单ID
	ClientOrderID   string          `json:"clientOrderId"`   // 确定性的客户端订单ID
	ExchangeOrderId string          `json:"exchangeOrderId"` // 交易所订单ID
	Symbol          string          `json:"symbol"`          // 交易对
	Side            Side            `json:"side"`            // 买/卖方向
	OrderType       OrderType       `json:"orderType"`       // 订单类型
	Price           decimal.Decimal `json:"price"`           // 委托价格
	Quantity        decimal.Decimal `json:"quantity"`        // 委托数量
	FilledQuantity  decimal.Decimal `json:"filledQuantity"`  // 已成交数量
	AvgFillPrice    decimal.Decimal `json:"avgFillPrice"`    // 平均成交价
	Status          OrderStatus     `json:"status"`          // 订单状态
	StrategyId      string          `json:"strategyId"`      // 关联策略ID
	CreateTime      int64           `json:"createTime"`      // 创建时间戳
	UpdateTime      int64           `json:"updateTime"`      // 更新时间戳
}
