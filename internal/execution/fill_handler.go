package execution

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/internal/monitor"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// GridFillHandler receives fill notifications from the private WebSocket and
// places counter-orders to maintain the grid trading loop.
// BUY filled → place SELL at next higher grid level
// SELL filled → place BUY at next lower grid level
type GridFillHandler struct {
	apiClient        *APIClient
	gridConfigs      []models.GridConfig
	gridLevels       map[string][]decimal.Decimal // symbol -> computed grid levels (sorted ascending)
	logger           *monitor.StructuredLogger
	inventoryTracker *InventoryTracker
}

// NewGridFillHandler creates a new GridFillHandler.
func NewGridFillHandler(
	apiClient *APIClient,
	gridConfigs []models.GridConfig,
	gridLevels map[string][]decimal.Decimal,
	logger *monitor.StructuredLogger,
	inventoryTracker ...*InventoryTracker,
) *GridFillHandler {
	h := &GridFillHandler{
		apiClient:   apiClient,
		gridConfigs: gridConfigs,
		gridLevels:  gridLevels,
		logger:      logger,
	}
	if len(inventoryTracker) > 0 && inventoryTracker[0] != nil {
		h.inventoryTracker = inventoryTracker[0]
	}
	return h
}

// OnFill is the callback invoked when an order fill is received from the private WebSocket.
// It determines the counter-order price based on the grid level and places the counter-order.
func (h *GridFillHandler) OnFill(instId, side, fillPx, fillSz, ordId, state string) {
	h.logger.LogInfo("fill received", map[string]string{
		"instId": instId,
		"side":   side,
		"fillPx": fillPx,
		"fillSz": fillSz,
		"ordId":  ordId,
		"state":  state,
	})

	// Parse fill price
	price, err := decimal.NewFromString(fillPx)
	if err != nil {
		h.logger.LogError("failed to parse fill price", map[string]string{
			"fillPx": fillPx,
			"error":  err.Error(),
		})
		return
	}

	// Parse fill size
	size, err := decimal.NewFromString(fillSz)
	if err != nil {
		h.logger.LogError("failed to parse fill size", map[string]string{
			"fillSz": fillSz,
			"error":  err.Error(),
		})
		return
	}

	// Find grid levels for this instrument (used for logging only)
	levels := h.gridLevels[instId]
	levelIdx := h.findNearestLevel(levels, price)

	// Determine counter-order parameters using fixed spread (0.3%)
	var counterSide models.Side
	var counterPrice decimal.Decimal

	switch side {
	case "buy":
		counterSide = models.SideSell
		if h.inventoryTracker != nil {
			h.inventoryTracker.RecordBuy(instId, price, size, ordId)
		}
		// Market-making: SELL at +1.0% (wider spread for altcoin volatility)
		counterPrice = price.Mul(decimal.NewFromFloat(1.01))
	case "sell":
		// SELL filled → place BUY at fillPrice - 0.2% (fast cycle, small profit)
		counterSide = models.SideBuy
		if h.inventoryTracker != nil {
			h.inventoryTracker.ClearPosition(instId)
		}
		counterPrice = price.Mul(decimal.NewFromFloat(0.99))
	default:
		h.logger.LogError("unknown fill side", map[string]string{
			"side": side,
		})
		return
	}

	// Round counter price to appropriate precision for the instrument
	counterPrice = roundPriceForSymbol(counterPrice, instId)

	// Determine counter-order quantity based on fill side and fee
	orderSize := size // Default to fill size
	for _, cfg := range h.gridConfigs {
		if cfg.Symbol == instId {
			if side == "buy" {
				// BUY filled → counter SELL: use fee-adjusted fill size
				// In cash mode, OKX deducts maker fee from received asset,
				// so available balance = fillSz * (1 - feeRate)
				orderSize = size.Mul(decimal.NewFromInt(1).Sub(cfg.FeeRate))
			} else {
				// SELL filled → counter BUY: use configured order size (USDT spend)
				orderSize = cfg.OrderSize
			}
			break
		}
	}

	// Place counter-order
	req := &OrderRequest{
		Symbol:        instId,
		Side:          counterSide,
		OrderType:     models.OrderTypePostOnly,
		Price:         counterPrice,
		Quantity:      orderSize,
		GridLevel:     levelIdx,
		ClientOrderID: fmt.Sprintf("tb1_%s_%d", counterSide.String()[:1], time.Now().UnixNano()%1000000000000),
	}

	result, err := h.apiClient.PlaceOrder(req)
	if err != nil {
		h.logger.LogError("failed to place counter-order", map[string]string{
			"instId":       instId,
			"counterSide":  counterSide.String(),
			"counterPrice": counterPrice.String(),
			"error":        err.Error(),
		})
		return
	}

	if !result.Success {
		h.logger.LogWarn("counter-order rejected", map[string]string{
			"instId":       instId,
			"counterSide":  counterSide.String(),
			"counterPrice": counterPrice.String(),
			"reason":       result.Error,
		})
		return
	}

	// Log successful counter-order placement
	h.logger.LogTradeAction(monitor.TradeAction{
		ActionType: "COUNTER_ORDER",
		Instrument: instId,
		Quantity:   orderSize.String(),
		Price:      counterPrice.String(),
		OrderID:    result.ExchangeOrderID,
		Result:     "SUCCESS",
		Extra: map[string]string{
			"trigger_side":   side,
			"trigger_price":  fillPx,
			"trigger_order":  ordId,
			"counter_side":   counterSide.String(),
			"grid_level_idx": fmt.Sprintf("%d", levelIdx),
		},
	})
}

// findNearestLevel finds the index of the grid level closest to the given price.
// Returns -1 if levels is empty.
func (h *GridFillHandler) findNearestLevel(levels []decimal.Decimal, price decimal.Decimal) int {
	if len(levels) == 0 {
		return -1
	}

	// Binary search for the nearest level
	idx := sort.Search(len(levels), func(i int) bool {
		return levels[i].GreaterThanOrEqual(price)
	})

	// Check adjacent levels for the closest match
	if idx == 0 {
		return 0
	}
	if idx >= len(levels) {
		return len(levels) - 1
	}

	// Compare distance to idx-1 and idx
	diffLower := price.Sub(levels[idx-1]).Abs()
	diffUpper := levels[idx].Sub(price).Abs()

	if diffLower.LessThanOrEqual(diffUpper) {
		return idx - 1
	}
	return idx
}

// roundPriceForSymbol rounds a price to the appropriate decimal places for the given symbol.
// OKX has different tick sizes per instrument.
func roundPriceForSymbol(price decimal.Decimal, symbol string) decimal.Decimal {
	switch {
	case strings.Contains(symbol, "BTC"):
		return price.Round(1) // BTC: $0.1 tick
	case strings.Contains(symbol, "ETH"):
		return price.Round(2) // ETH: $0.01 tick
	case strings.Contains(symbol, "DOGE"):
		return price.Round(5) // DOGE: $0.00001 tick
	case strings.Contains(symbol, "PEPE"):
		return price.Round(10) // PEPE: very small tick
	case strings.Contains(symbol, "WIF"):
		return price.Round(4) // WIF: $0.0001 tick
	case strings.Contains(symbol, "KOR"):
		return price.Round(4) // KOR: $0.0001 tick
	default:
		return price.Round(5) // Safe default
	}
}

// UpdateGridLevels updates the grid levels for a given symbol in the fill handler.
// This allows the rebalancer to keep fill handler levels in sync with the new grid range.
func (h *GridFillHandler) UpdateGridLevels(symbol string, levels []decimal.Decimal) {
	h.gridLevels[symbol] = levels
}

// getTickSizeForFillHandler returns the minimum price increment (tick) for a given symbol.
// Used by the fill handler to calculate SELL price at fillPrice + 2 ticks.
func getTickSizeForFillHandler(symbol string) decimal.Decimal {
	switch {
	case strings.Contains(symbol, "DOGE"):
		return decimal.NewFromFloat(0.00001) // 5 decimal places
	case strings.Contains(symbol, "WIF"):
		return decimal.NewFromFloat(0.0001) // 4 decimal places
	case strings.Contains(symbol, "BTC"):
		return decimal.NewFromFloat(0.1)
	case strings.Contains(symbol, "ETH"):
		return decimal.NewFromFloat(0.01)
	default:
		return decimal.NewFromFloat(0.00001)
	}
}
