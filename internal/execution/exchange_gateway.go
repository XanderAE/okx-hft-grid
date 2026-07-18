package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// ---- Structured result types ----

// GatewayError contains structured OKX error information without forcing callers
// to parse concatenated error strings.
type GatewayError struct {
	HTTPStatus int
	Code       string // OKX envelope "code"
	Msg        string // OKX envelope "msg"
	SCode      string // per-item "sCode"
	SMsg       string // per-item "sMsg"
	Transport  error  // underlying transport/context error
}

func (e *GatewayError) Error() string {
	if e.Transport != nil {
		return fmt.Sprintf("gateway transport: %v", e.Transport)
	}
	if e.SCode != "" && e.SCode != "0" {
		return fmt.Sprintf("gateway OKX item: sCode=%s sMsg=%s", e.SCode, e.SMsg)
	}
	if e.Code != "" && e.Code != "0" {
		return fmt.Sprintf("gateway OKX envelope: code=%s msg=%s", e.Code, e.Msg)
	}
	return "gateway: unknown error"
}

// IsTransportError returns true when the error is due to network/context issues.
func (e *GatewayError) IsTransportError() bool {
	return e.Transport != nil
}

// OrderPlaceResult is the structured outcome of placing an order.
type OrderPlaceResult struct {
	ExchangeOrderID string
	ClientOrderID   string
	Status          models.OrderStatus
	Err             *GatewayError
}

// CancelAttemptResult is the structured outcome of cancelling an order.
type CancelAttemptResult struct {
	ExchangeOrderID string
	ClientOrderID   string
	Cancelled       bool
	Err             *GatewayError
}

// ExchangeOrderInfo is the structured order state from a query.
type ExchangeOrderInfo struct {
	ExchangeOrderID   string
	ClientOrderID     string
	Symbol            string
	Side              models.Side
	Status            models.OrderStatus
	Price             decimal.Decimal
	Quantity          decimal.Decimal
	CumulativeFillQty decimal.Decimal
	AvgFillPrice      decimal.Decimal
	CreateTime        time.Time
	UpdateTime        time.Time
}

// OrderRef identifies an order for query/cancel by exchange ID or client order ID.
type OrderRef struct {
	Symbol          string
	ExchangeOrderID string
	ClientOrderID   string
}

// QueryWindow defines the time-based filter for order history queries.
type QueryWindow struct {
	After  time.Time
	Before time.Time
}

// FillCursor supports paginated fill queries.
type FillCursor struct {
	After     string // cursor/ID to start after
	Limit     int
	StartTime time.Time
}

// OrderPage is a paginated list of orders.
type OrderPage struct {
	Orders  []ExchangeOrderInfo
	HasMore bool
	Cursor  string
}

// FillRecord is a single fill from the exchange.
type FillRecord struct {
	Symbol             string
	ExchangeOrderID    string
	ExchangeFillID     string
	Side               models.Side
	Price              decimal.Decimal
	Quantity           decimal.Decimal
	CumulativeQuantity decimal.Decimal
	Fee                decimal.Decimal
	Timestamp          time.Time
}

// FillPage is a paginated list of fills.
type FillPage struct {
	Fills   []FillRecord
	HasMore bool
	Cursor  string
}

// TickerObservation is a validated ticker snapshot.
type TickerObservation struct {
	Symbol            string
	Last              decimal.Decimal
	BestBid           decimal.Decimal
	BestAsk           decimal.Decimal
	High24h           decimal.Decimal
	Low24h            decimal.Decimal
	ExchangeTimestamp time.Time
	ReceivedAt        time.Time
}

// ---- ExchangeGateway interface ----

// ExchangeGateway is the context-aware typed adapter for OKX REST API.
// All spot orders use tdMode=cash and deterministic clOrdId from callers.
// Results are structured per-item with HTTP/transport/OKX code/sCode/sMsg.
// Request-send is never treated as terminal state.
type ExchangeGateway interface {
	PlaceOrder(ctx context.Context, req NormalizedOrderRequest) (OrderPlaceResult, error)
	CancelOrder(ctx context.Context, ref OrderRef) (CancelAttemptResult, error)
	QueryOrder(ctx context.Context, ref OrderRef) (ExchangeOrderInfo, error)
	ListPendingOrders(ctx context.Context, symbol string) ([]ExchangeOrderInfo, error)
	ListOrderHistory(ctx context.Context, symbol string, window QueryWindow) (OrderPage, error)
	ListFills(ctx context.Context, symbol string, cursor FillCursor) (FillPage, error)
	GetTicker(ctx context.Context, symbol string) (TickerObservation, error)
	GetInstrumentRules(ctx context.Context, symbol string) (models.InstrumentRules, error)
}

// ---- OKXGateway implementation ----

// OKXGateway adapts the APIClient into a context-aware, structured gateway.
// It enforces tdMode=cash, requires deterministic clOrdId from callers, and
// returns structured errors without forcing callers to parse error strings.
type OKXGateway struct {
	client *APIClient
	clock  func() time.Time
}

// OKXGatewayOption configures the gateway.
type OKXGatewayOption func(*OKXGateway)

// WithGatewayClock injects a fake clock for testing.
func WithGatewayClock(clock func() time.Time) OKXGatewayOption {
	return func(g *OKXGateway) { g.clock = clock }
}

// NewOKXGateway creates a gateway adapter around the existing APIClient.
func NewOKXGateway(client *APIClient, opts ...OKXGatewayOption) *OKXGateway {
	g := &OKXGateway{
		client: client,
		clock:  time.Now,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// PlaceOrder sends a POST to /api/v5/trade/order with tdMode=cash and the
// caller-provided deterministic clOrdId. Returns structured per-item result.
func (g *OKXGateway) PlaceOrder(ctx context.Context, req NormalizedOrderRequest) (OrderPlaceResult, error) {
	if req.ClOrdID == "" {
		return OrderPlaceResult{}, fmt.Errorf("gateway: deterministic clOrdId is required")
	}

	okxReq := map[string]string{
		"instId":  req.Symbol,
		"tdMode":  "cash",
		"side":    sideToOKX(req.Side),
		"ordType": orderTypeToOKX(req.OrderType),
		"px":      req.Price.String(),
		"sz":      req.Quantity.String(),
		"clOrdId": req.ClOrdID,
	}

	resp, err := g.doContextRequest(ctx, "POST", "/api/v5/trade/order", okxReq)
	if err != nil {
		return OrderPlaceResult{Err: &GatewayError{Transport: err}}, err
	}
	defer resp.Body.Close()

	envelope, items, gatewayErr := g.parseOrderResponse(resp)
	if gatewayErr != nil {
		return OrderPlaceResult{Err: gatewayErr}, gatewayErr
	}

	if envelope.Code != "0" {
		ge := &GatewayError{HTTPStatus: resp.StatusCode, Code: envelope.Code, Msg: envelope.Msg}
		return OrderPlaceResult{Err: ge}, ge
	}

	if len(items) == 0 {
		ge := &GatewayError{Code: envelope.Code, Msg: "no order data in response"}
		return OrderPlaceResult{Err: ge}, ge
	}

	item := items[0]
	if item.SCode != "0" {
		ge := &GatewayError{HTTPStatus: resp.StatusCode, Code: envelope.Code, Msg: envelope.Msg, SCode: item.SCode, SMsg: item.SMsg}
		return OrderPlaceResult{ClientOrderID: item.ClOrdID, Err: ge}, ge
	}

	return OrderPlaceResult{
		ExchangeOrderID: item.OrdID,
		ClientOrderID:   item.ClOrdID,
		Status:          models.OrderStatusSubmitted,
	}, nil
}

// CancelOrder sends a cancel request using instId + ordId or clOrdId.
func (g *OKXGateway) CancelOrder(ctx context.Context, ref OrderRef) (CancelAttemptResult, error) {
	if ref.Symbol == "" {
		return CancelAttemptResult{}, fmt.Errorf("gateway: symbol is required for cancel")
	}
	if ref.ExchangeOrderID == "" && ref.ClientOrderID == "" {
		return CancelAttemptResult{}, fmt.Errorf("gateway: exchange order ID or client order ID required for cancel")
	}

	cancelReq := map[string]string{
		"instId": ref.Symbol,
	}
	if ref.ExchangeOrderID != "" {
		cancelReq["ordId"] = ref.ExchangeOrderID
	}
	if ref.ClientOrderID != "" {
		cancelReq["clOrdId"] = ref.ClientOrderID
	}

	resp, err := g.doContextRequest(ctx, "POST", "/api/v5/trade/cancel-order", cancelReq)
	if err != nil {
		return CancelAttemptResult{Err: &GatewayError{Transport: err}}, err
	}
	defer resp.Body.Close()

	envelope, items, gatewayErr := g.parseOrderResponse(resp)
	if gatewayErr != nil {
		return CancelAttemptResult{Err: gatewayErr}, gatewayErr
	}

	if envelope.Code != "0" {
		ge := &GatewayError{HTTPStatus: resp.StatusCode, Code: envelope.Code, Msg: envelope.Msg}
		return CancelAttemptResult{Err: ge}, ge
	}

	if len(items) > 0 && items[0].SCode != "0" {
		ge := &GatewayError{Code: envelope.Code, Msg: envelope.Msg, SCode: items[0].SCode, SMsg: items[0].SMsg}
		return CancelAttemptResult{ExchangeOrderID: items[0].OrdID, ClientOrderID: items[0].ClOrdID, Err: ge}, ge
	}

	result := CancelAttemptResult{Cancelled: true}
	if len(items) > 0 {
		result.ExchangeOrderID = items[0].OrdID
		result.ClientOrderID = items[0].ClOrdID
	}
	return result, nil
}

// QueryOrder queries order state by exchange ID or client order ID.
func (g *OKXGateway) QueryOrder(ctx context.Context, ref OrderRef) (ExchangeOrderInfo, error) {
	if ref.Symbol == "" {
		return ExchangeOrderInfo{}, fmt.Errorf("gateway: symbol is required for query")
	}

	path := "/api/v5/trade/order?instId=" + ref.Symbol
	if ref.ExchangeOrderID != "" {
		path += "&ordId=" + ref.ExchangeOrderID
	} else if ref.ClientOrderID != "" {
		path += "&clOrdId=" + ref.ClientOrderID
	} else {
		return ExchangeOrderInfo{}, fmt.Errorf("gateway: exchange order ID or client order ID required for query")
	}

	resp, err := g.doContextRequest(ctx, "GET", path, nil)
	if err != nil {
		return ExchangeOrderInfo{}, &GatewayError{Transport: err}
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return ExchangeOrderInfo{}, &GatewayError{Transport: fmt.Errorf("read body: %w", err)}
	}

	var raw struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			InstID    string `json:"instId"`
			OrdID     string `json:"ordId"`
			ClOrdID   string `json:"clOrdId"`
			Side      string `json:"side"`
			State     string `json:"state"`
			Px        string `json:"px"`
			Sz        string `json:"sz"`
			AccFillSz string `json:"accFillSz"`
			AvgPx     string `json:"avgPx"`
			CTime     string `json:"cTime"`
			UTime     string `json:"uTime"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		return ExchangeOrderInfo{}, &GatewayError{Transport: fmt.Errorf("parse query response: %w", err)}
	}

	if raw.Code != "0" {
		return ExchangeOrderInfo{}, &GatewayError{Code: raw.Code, Msg: raw.Msg}
	}

	if len(raw.Data) == 0 {
		return ExchangeOrderInfo{}, &GatewayError{Code: raw.Code, Msg: "order not found"}
	}

	d := raw.Data[0]
	info := ExchangeOrderInfo{
		ExchangeOrderID: d.OrdID,
		ClientOrderID:   d.ClOrdID,
		Symbol:          d.InstID,
		Side:            okxSideToModel(d.Side),
		Status:          okxStateToStatus(d.State),
	}
	info.Price, _ = decimal.NewFromString(d.Px)
	info.Quantity, _ = decimal.NewFromString(d.Sz)
	info.CumulativeFillQty, _ = decimal.NewFromString(d.AccFillSz)
	info.AvgFillPrice, _ = decimal.NewFromString(d.AvgPx)
	info.CreateTime = okxTimestampToTime(d.CTime)
	info.UpdateTime = okxTimestampToTime(d.UTime)

	return info, nil
}

// ListPendingOrders retrieves all open orders for a symbol with pagination.
func (g *OKXGateway) ListPendingOrders(ctx context.Context, symbol string) ([]ExchangeOrderInfo, error) {
	var all []ExchangeOrderInfo
	after := ""
	for {
		path := "/api/v5/trade/orders-pending?instId=" + symbol + "&instType=SPOT"
		if after != "" {
			path += "&after=" + after
		}

		resp, err := g.doContextRequest(ctx, "GET", path, nil)
		if err != nil {
			return nil, &GatewayError{Transport: err}
		}

		orders, nextCursor, err := g.parseOrderListResponse(resp)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		all = append(all, orders...)
		if nextCursor == "" || len(orders) == 0 {
			break
		}
		after = nextCursor
	}
	return all, nil
}

// ListOrderHistory retrieves historical orders with pagination.
func (g *OKXGateway) ListOrderHistory(ctx context.Context, symbol string, window QueryWindow) (OrderPage, error) {
	path := "/api/v5/trade/orders-history-archive?instId=" + symbol + "&instType=SPOT"
	if !window.After.IsZero() {
		path += fmt.Sprintf("&begin=%d", window.After.UnixMilli())
	}
	if !window.Before.IsZero() {
		path += fmt.Sprintf("&end=%d", window.Before.UnixMilli())
	}

	resp, err := g.doContextRequest(ctx, "GET", path, nil)
	if err != nil {
		return OrderPage{}, &GatewayError{Transport: err}
	}
	defer resp.Body.Close()

	orders, cursor, err := g.parseOrderListResponse(resp)
	if err != nil {
		return OrderPage{}, err
	}

	return OrderPage{
		Orders:  orders,
		HasMore: cursor != "",
		Cursor:  cursor,
	}, nil
}

// ListFills retrieves fills for a symbol with cursor-based pagination.
func (g *OKXGateway) ListFills(ctx context.Context, symbol string, cursor FillCursor) (FillPage, error) {
	path := "/api/v5/trade/fills?instId=" + symbol + "&instType=SPOT"
	if cursor.After != "" {
		path += "&after=" + cursor.After
	}
	if cursor.Limit > 0 {
		path += fmt.Sprintf("&limit=%d", cursor.Limit)
	}
	if !cursor.StartTime.IsZero() {
		path += fmt.Sprintf("&begin=%d", cursor.StartTime.UnixMilli())
	}

	resp, err := g.doContextRequest(ctx, "GET", path, nil)
	if err != nil {
		return FillPage{}, &GatewayError{Transport: err}
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return FillPage{}, &GatewayError{Transport: fmt.Errorf("read fills response: %w", err)}
	}

	var raw struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			InstID  string `json:"instId"`
			OrdID   string `json:"ordId"`
			TradeID string `json:"tradeId"`
			BillID  string `json:"billId"`
			Side    string `json:"side"`
			FillPx  string `json:"fillPx"`
			FillSz  string `json:"fillSz"`
			Fee     string `json:"fee"`
			Ts      string `json:"ts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		return FillPage{}, &GatewayError{Transport: fmt.Errorf("parse fills: %w", err)}
	}
	if raw.Code != "0" {
		return FillPage{}, &GatewayError{Code: raw.Code, Msg: raw.Msg}
	}

	var fills []FillRecord
	var lastID string
	for _, d := range raw.Data {
		fr := FillRecord{
			Symbol:          d.InstID,
			ExchangeOrderID: d.OrdID,
			ExchangeFillID:  d.TradeID,
			Side:            okxSideToModel(d.Side),
		}
		if d.TradeID == "" {
			fr.ExchangeFillID = d.BillID
		}
		fr.Price, _ = decimal.NewFromString(d.FillPx)
		fr.Quantity, _ = decimal.NewFromString(d.FillSz)
		fr.Fee, _ = decimal.NewFromString(d.Fee)
		fr.Timestamp = okxTimestampToTime(d.Ts)
		fills = append(fills, fr)
		lastID = d.BillID
		if lastID == "" {
			lastID = d.TradeID
		}
	}

	hasMore := len(raw.Data) > 0 && cursor.Limit > 0 && len(raw.Data) >= cursor.Limit
	nextCursor := ""
	if hasMore {
		nextCursor = lastID
	}

	return FillPage{Fills: fills, HasMore: hasMore, Cursor: nextCursor}, nil
}

// GetTicker fetches the current ticker for a symbol.
func (g *OKXGateway) GetTicker(ctx context.Context, symbol string) (TickerObservation, error) {
	path := "/api/v5/market/ticker?instId=" + symbol

	resp, err := g.doContextRequest(ctx, "GET", path, nil)
	if err != nil {
		return TickerObservation{}, &GatewayError{Transport: err}
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return TickerObservation{}, &GatewayError{Transport: fmt.Errorf("read ticker: %w", err)}
	}

	var raw struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			InstID  string `json:"instId"`
			Last    string `json:"last"`
			BidPx   string `json:"bidPx"`
			AskPx   string `json:"askPx"`
			High24h string `json:"high24h"`
			Low24h  string `json:"low24h"`
			Ts      string `json:"ts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		return TickerObservation{}, &GatewayError{Transport: fmt.Errorf("parse ticker: %w", err)}
	}
	if raw.Code != "0" {
		return TickerObservation{}, &GatewayError{Code: raw.Code, Msg: raw.Msg}
	}
	if len(raw.Data) == 0 {
		return TickerObservation{}, &GatewayError{Code: raw.Code, Msg: "no ticker data"}
	}

	d := raw.Data[0]
	obs := TickerObservation{
		Symbol:     d.InstID,
		ReceivedAt: g.clock(),
	}
	obs.Last, _ = decimal.NewFromString(d.Last)
	obs.BestBid, _ = decimal.NewFromString(d.BidPx)
	obs.BestAsk, _ = decimal.NewFromString(d.AskPx)
	obs.High24h, _ = decimal.NewFromString(d.High24h)
	obs.Low24h, _ = decimal.NewFromString(d.Low24h)
	obs.ExchangeTimestamp = okxTimestampToTime(d.Ts)
	return obs, nil
}

// GetInstrumentRules fetches instrument metadata from /api/v5/public/instruments.
func (g *OKXGateway) GetInstrumentRules(ctx context.Context, symbol string) (models.InstrumentRules, error) {
	path := "/api/v5/public/instruments?instType=SPOT&instId=" + symbol

	resp, err := g.doContextRequest(ctx, "GET", path, nil)
	if err != nil {
		return models.InstrumentRules{}, &GatewayError{Transport: err}
	}
	defer resp.Body.Close()

	instruments, err := parseInstrumentsResponse(resp.Body)
	if err != nil {
		return models.InstrumentRules{}, err
	}

	for _, inst := range instruments {
		if strings.EqualFold(inst.InstID, symbol) {
			return ParseInstrumentRules(inst, g.clock(), g.client.hardTTL())
		}
	}

	return models.InstrumentRules{}, fmt.Errorf("instrument %s not found in response", symbol)
}

// ---- Internal helpers ----

// doContextRequest wraps the APIClient's DoRequest with context support.
func (g *OKXGateway) doContextRequest(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	// Check context before making the request
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resp, err := g.client.DoRequestContext(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (g *OKXGateway) parseOrderResponse(resp *http.Response) (OKXResponse, []OKXOrderData, *GatewayError) {
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return OKXResponse{}, nil, &GatewayError{Transport: fmt.Errorf("read response: %w", err)}
	}

	var envelope OKXResponse
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		return OKXResponse{}, nil, &GatewayError{Transport: fmt.Errorf("parse envelope: %w", err)}
	}

	var items []OKXOrderData
	if len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, &items); err != nil {
			return envelope, nil, &GatewayError{Transport: fmt.Errorf("parse items: %w", err)}
		}
	}

	return envelope, items, nil
}

func (g *OKXGateway) parseOrderListResponse(resp *http.Response) ([]ExchangeOrderInfo, string, error) {
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", &GatewayError{Transport: fmt.Errorf("read order list: %w", err)}
	}

	var raw struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			InstID    string `json:"instId"`
			OrdID     string `json:"ordId"`
			ClOrdID   string `json:"clOrdId"`
			Side      string `json:"side"`
			State     string `json:"state"`
			Px        string `json:"px"`
			Sz        string `json:"sz"`
			AccFillSz string `json:"accFillSz"`
			AvgPx     string `json:"avgPx"`
			CTime     string `json:"cTime"`
			UTime     string `json:"uTime"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &raw); err != nil {
		return nil, "", &GatewayError{Transport: fmt.Errorf("parse order list: %w", err)}
	}
	if raw.Code != "0" {
		return nil, "", &GatewayError{Code: raw.Code, Msg: raw.Msg}
	}

	var orders []ExchangeOrderInfo
	var lastID string
	for _, d := range raw.Data {
		info := ExchangeOrderInfo{
			ExchangeOrderID: d.OrdID,
			ClientOrderID:   d.ClOrdID,
			Symbol:          d.InstID,
			Side:            okxSideToModel(d.Side),
			Status:          okxStateToStatus(d.State),
		}
		info.Price, _ = decimal.NewFromString(d.Px)
		info.Quantity, _ = decimal.NewFromString(d.Sz)
		info.CumulativeFillQty, _ = decimal.NewFromString(d.AccFillSz)
		info.AvgFillPrice, _ = decimal.NewFromString(d.AvgPx)
		info.CreateTime = okxTimestampToTime(d.CTime)
		info.UpdateTime = okxTimestampToTime(d.UTime)
		orders = append(orders, info)
		lastID = d.OrdID
	}

	cursor := ""
	if len(raw.Data) >= 100 { // OKX default page size
		cursor = lastID
	}

	return orders, cursor, nil
}

func okxSideToModel(side string) models.Side {
	switch strings.ToLower(side) {
	case "sell":
		return models.SideSell
	default:
		return models.SideBuy
	}
}

func okxStateToStatus(state string) models.OrderStatus {
	switch strings.ToLower(state) {
	case "live":
		return models.OrderStatusOpen
	case "partially_filled":
		return models.OrderStatusPartiallyFilled
	case "filled":
		return models.OrderStatusFilled
	case "canceled", "cancelled":
		return models.OrderStatusCancelled
	default:
		return models.OrderStatusPending
	}
}

func okxTimestampToTime(ts string) time.Time {
	if ts == "" {
		return time.Time{}
	}
	// OKX uses millisecond timestamps
	var ms int64
	fmt.Sscanf(ts, "%d", &ms)
	if ms > 0 {
		return time.UnixMilli(ms)
	}
	return time.Time{}
}
