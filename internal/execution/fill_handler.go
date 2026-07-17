package execution

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/internal/monitor"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// GridFillHandler receives fill notifications from the private WebSocket and
// places counter-orders to maintain the grid trading loop.
// BUY filled → place SELL at next higher grid level
// SELL filled → place BUY at next lower grid level
type GridFillHandler struct {
	apiClient   *APIClient
	gridConfigs []models.GridConfig
	gridLevels  map[string][]decimal.Decimal // symbol -> computed grid levels (sorted ascending)
	logger      *monitor.StructuredLogger
}

// NewGridFillHandler creates a new GridFillHandler.
func NewGridFillHandler(
	apiClient *APIClient,
	gridConfigs []models.GridConfig,
	gridLevels map[string][]decimal.Decimal,
	logger *monitor.StructuredLogger,
) *GridFillHandler {
	return &GridFillHandler{
		apiClient:   apiClient,
		gridConfigs: gridConfigs,
		gridLevels:  gridLevels,
		logger:      logger,
	}
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

	// Find grid levels for this instrument
	levels, ok := h.gridLevels[instId]
	if !ok || len(levels) < 2 {
		h.logger.LogWarn("no grid levels found for instrument", map[string]string{
			"instId": instId,
		})
		return
	}

	// Find the nearest grid level to the fill price
	levelIdx := h.findNearestLevel(levels, price)
	if levelIdx < 0 {
		h.logger.LogWarn("could not match fill to grid level", map[string]string{
			"instId": instId,
			"fillPx": fillPx,
		})
		return
	}

	// Determine counter-order parameters
	var counterSide models.Side
	var counterPrice decimal.Decimal

	switch side {
	case "buy":
		// BUY filled → place SELL at next higher grid level
		counterSide = models.SideSell
		if levelIdx+1 < len(levels) {
			counterPrice = levels[levelIdx+1]
		} else {
			h.logger.LogWarn("buy fill at highest grid level, no counter-order possible", map[string]string{
				"instId":   instId,
				"fillPx":   fillPx,
				"levelIdx": fmt.Sprintf("%d", levelIdx),
			})
			return
		}
	case "sell":
		// SELL filled → place BUY at next lower grid level
		counterSide = models.SideBuy
		if levelIdx-1 >= 0 {
			counterPrice = levels[levelIdx-1]
		} else {
			h.logger.LogWarn("sell fill at lowest grid level, no counter-order possible", map[string]string{
				"instId":   instId,
				"fillPx":   fillPx,
				"levelIdx": fmt.Sprintf("%d", levelIdx),
			})
			return
		}
	default:
		h.logger.LogError("unknown fill side", map[string]string{
			"side": side,
		})
		return
	}

	// Round counter price to appropriate precision for the instrument
	counterPrice = roundPriceForSymbol(counterPrice, instId)

	// Find the grid config for this instrument to get order size
	orderSize := size // Default to fill size
	for _, cfg := range h.gridConfigs {
		if cfg.Symbol == instId {
			orderSize = cfg.OrderSize
			break
		}
	}

	// Place counter-order
	req := &OrderRequest{
		Symbol:    instId,
		Side:      counterSide,
		OrderType: models.OrderTypePostOnly,
		Price:     counterPrice,
		Quantity:  orderSize,
		GridLevel: levelIdx,
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
		return price.Round(6) // Safe default
	}
}
