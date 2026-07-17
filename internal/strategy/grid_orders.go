package strategy

import (
	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// PlaceGridOrders generates grid orders given a set of grid levels, a current price, and config.
// Orders below the current price are BUY orders, orders above are SELL orders.
// No order is placed at a level equal to the current price.
// All orders use POST_ONLY type.
func PlaceGridOrders(levels []decimal.Decimal, currentPrice decimal.Decimal, config *models.GridConfig) []*models.Order {
	orders := make([]*models.Order, 0, len(levels))

	for i, levelPrice := range levels {
		if levelPrice.LessThan(currentPrice) {
			// Below current price -> BUY order
			order := &models.Order{
				Symbol:    config.Symbol,
				Side:      models.SideBuy,
				OrderType: models.OrderTypePostOnly,
				Price:     levelPrice,
				Quantity:  config.OrderSize,
				Status:    models.OrderStatusPending,
			}
			_ = i // grid level index available for tracking
			orders = append(orders, order)
		} else if levelPrice.GreaterThan(currentPrice) {
			// Above current price -> SELL order
			order := &models.Order{
				Symbol:    config.Symbol,
				Side:      models.SideSell,
				OrderType: models.OrderTypePostOnly,
				Price:     levelPrice,
				Quantity:  config.OrderSize,
				Status:    models.OrderStatusPending,
			}
			orders = append(orders, order)
		}
		// Equal to currentPrice -> no order placed
	}

	return orders
}
