package execution

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/shopspring/decimal"
)

const (
	// BTC-USDT-SWAP: 1 contract = 0.01 BTC
	ContractFaceValue = 0.01
	SwapInstrument    = "BTC-USDT-SWAP"
)

// SwapGateway handles perpetual swap order placement with proper OKX parameters.
type SwapGateway struct {
	client *APIClient
}

func NewSwapGateway(client *APIClient) *SwapGateway {
	return &SwapGateway{client: client}
}

// SetLeverage sets the leverage for the instrument via OKX API.
// Must be called before placing any swap order.
func (g *SwapGateway) SetLeverage(leverage int, posSide string) error {
	body := map[string]interface{}{
		"instId":  SwapInstrument,
		"lever":   fmt.Sprintf("%d", leverage),
		"mgnMode": "isolated",
		"posSide": posSide, // "long" or "short"
	}
	resp, err := g.client.DoRequest("POST", "/api/v5/account/set-leverage", body)
	if err != nil {
		return fmt.Errorf("set-leverage request failed: %w", err)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("set-leverage read body: %w", err)
	}
	var result struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return fmt.Errorf("set-leverage parse: %w", err)
	}
	if result.Code != "0" {
		return fmt.Errorf("set-leverage OKX error: code=%s msg=%s", result.Code, result.Msg)
	}
	return nil
}

// ComputeContracts converts USDT notional to integer contract count.
// Returns 0 if the notional is too small for even 1 contract.
func ComputeContracts(notionalUSDT float64, price float64) int {
	if price <= 0 || notionalUSDT <= 0 {
		return 0
	}
	// 1 contract = ContractFaceValue BTC = ContractFaceValue * price USDT
	contractValue := ContractFaceValue * price
	contracts := int(math.Floor(notionalUSDT / contractValue))
	return contracts
}

// VerifyLiquidationDistance checks that the liquidation distance is greater than
// the hard stop distance. If not, returns the reduced leverage that would be safe.
// liqDistance ≈ 1/leverage * 0.9 (accounting for maintenance margin).
// We need liqDistance > stopPct.
func VerifyLiquidationDistance(leverage float64, stopPct float64) (safeLeverage float64, ok bool) {
	liqDist := (1.0 / leverage) * 0.9
	if liqDist > stopPct {
		return leverage, true
	}
	// Reduce leverage until safe: need (1/L)*0.9 > stopPct → L < 0.9/stopPct
	safeLev := 0.9 / stopPct
	if safeLev < 1.0 {
		safeLev = 1.0
	}
	return math.Floor(safeLev), false
}

// PlaceSwapOrder places a swap order with proper OKX parameters.
// side: "buy" or "sell"
// posSide: "long" or "short"
// reduceOnly: true for close orders
func (g *SwapGateway) PlaceSwapOrder(side, posSide string, price decimal.Decimal, contracts int, reduceOnly bool, leverage int) (*OrderResult, error) {
	clOrdID := fmt.Sprintf("tb1%s%d", side[:1], time.Now().UnixNano()%10000000000)

	ordType := "post_only"

	body := map[string]interface{}{
		"instId":  SwapInstrument,
		"tdMode":  "isolated",
		"side":    side,
		"posSide": posSide,
		"ordType": ordType,
		"px":      price.String(),
		"sz":      fmt.Sprintf("%d", contracts),
		"clOrdId": clOrdID,
	}
	if reduceOnly {
		body["reduceOnly"] = "true"
	}

	resp, err := g.client.DoRequest("POST", "/api/v5/trade/order", body)
	if err != nil {
		return &OrderResult{Success: false, Error: err.Error()}, err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return &OrderResult{Success: false, Error: "read body failed"}, err
	}

	var okxResp OKXResponse
	if err := json.Unmarshal(bodyBytes, &okxResp); err != nil {
		return &OrderResult{Success: false, Error: "parse failed"}, err
	}

	if okxResp.Code != "0" {
		errMsg := fmt.Sprintf("OKX error: code=%s, msg=%s", okxResp.Code, okxResp.Msg)
		var orderData []OKXOrderData
		if json.Unmarshal(okxResp.Data, &orderData) == nil && len(orderData) > 0 {
			errMsg = fmt.Sprintf("OKX error: code=%s, msg=%s, sCode=%s, sMsg=%s",
				okxResp.Code, okxResp.Msg, orderData[0].SCode, orderData[0].SMsg)
		}
		return &OrderResult{Success: false, Error: errMsg}, nil
	}

	var orderData []OKXOrderData
	if err := json.Unmarshal(okxResp.Data, &orderData); err != nil || len(orderData) == 0 {
		return &OrderResult{Success: false, Error: "no order data"}, nil
	}

	data := orderData[0]
	if data.SCode != "0" {
		return &OrderResult{Success: false, Error: fmt.Sprintf("sCode=%s, sMsg=%s", data.SCode, data.SMsg)}, nil
	}

	return &OrderResult{
		Success:         true,
		ExchangeOrderID: data.OrdID,
		OrderID:         clOrdID,
	}, nil
}
