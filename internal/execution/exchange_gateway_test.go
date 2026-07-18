package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/internal/config"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// --- Test helpers ---

func newGatewayTestClient(serverURL string) *APIClient {
	creds := &config.Credentials{
		APIKey:     "test-api-key",
		SecretKey:  "test-secret-key",
		Passphrase: "test-passphrase",
	}
	return NewAPIClient(serverURL, creds, &mockRateLimiter{})
}

func newTestGateway(serverURL string, opts ...OKXGatewayOption) *OKXGateway {
	client := newGatewayTestClient(serverURL)
	return NewOKXGateway(client, opts...)
}

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// --- TestAPIClientCashMode ---
// Validates: Requirements 2.10, 2.11 - All spot orders use tdMode=cash

func TestAPIClientCashMode(t *testing.T) {
	t.Run("PlaceOrder_uses_cash_mode", func(t *testing.T) {
		var captured OKXOrderRequest
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&captured)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"code":"0","msg":"","data":[{"ordId":"x1","clOrdId":"c1","sCode":"0","sMsg":""}]}`)
		}))
		defer server.Close()

		client := newGatewayTestClient(server.URL)
		_, err := client.PlaceOrder(&OrderRequest{
			Symbol:    "DOGE-USDT",
			Side:      models.SideBuy,
			OrderType: models.OrderTypePostOnly,
			Price:     decimal.NewFromFloat(0.15),
			Quantity:  decimal.NewFromInt(100),
		})
		if err != nil {
			t.Fatalf("PlaceOrder error: %v", err)
		}
		if captured.TdMode != "cash" {
			t.Errorf("expected tdMode=cash, got %q", captured.TdMode)
		}
	})

	t.Run("BatchPlaceOrders_uses_cash_mode", func(t *testing.T) {
		var capturedBatch []OKXOrderRequest
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&capturedBatch)
			data := make([]OKXOrderData, len(capturedBatch))
			for i := range data {
				data[i] = OKXOrderData{OrdID: fmt.Sprintf("o%d", i), SCode: "0"}
			}
			resp := OKXResponse{Code: "0"}
			resp.Data, _ = json.Marshal(data)
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		client := newGatewayTestClient(server.URL)
		orders := []*OrderRequest{
			{Symbol: "DOGE-USDT", Side: models.SideBuy, OrderType: models.OrderTypeLimit,
				Price: decimal.NewFromFloat(0.15), Quantity: decimal.NewFromInt(100)},
			{Symbol: "WIF-USDT", Side: models.SideSell, OrderType: models.OrderTypePostOnly,
				Price: decimal.NewFromFloat(2.5), Quantity: decimal.NewFromInt(10)},
		}
		_, err := client.BatchPlaceOrders(orders)
		if err != nil {
			t.Fatalf("BatchPlaceOrders error: %v", err)
		}
		for i, req := range capturedBatch {
			if req.TdMode != "cash" {
				t.Errorf("batch[%d]: expected tdMode=cash, got %q", i, req.TdMode)
			}
		}
	})

	t.Run("Gateway_PlaceOrder_uses_cash_and_clOrdId", func(t *testing.T) {
		var captured map[string]string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&captured)
			fmt.Fprint(w, `{"code":"0","msg":"","data":[{"ordId":"ex1","clOrdId":"tb1abc","sCode":"0","sMsg":""}]}`)
		}))
		defer server.Close()

		gw := newTestGateway(server.URL)
		req := NormalizedOrderRequest{
			Symbol:    "DOGE-USDT",
			Side:      models.SideBuy,
			OrderType: models.OrderTypePostOnly,
			Price:     decimal.NewFromFloat(0.15000),
			Quantity:  decimal.NewFromInt(100),
			ClOrdID:   "tb1abc123456789012345678901",
		}
		result, err := gw.PlaceOrder(context.Background(), req)
		if err != nil {
			t.Fatalf("PlaceOrder error: %v", err)
		}
		if result.Err != nil {
			t.Fatalf("PlaceOrder result error: %v", result.Err)
		}
		if captured["tdMode"] != "cash" {
			t.Errorf("expected tdMode=cash, got %q", captured["tdMode"])
		}
		if captured["clOrdId"] == "" {
			t.Error("expected non-empty clOrdId in request")
		}
		if result.ExchangeOrderID != "ex1" {
			t.Errorf("expected exchange order ID ex1, got %q", result.ExchangeOrderID)
		}
	})
}

// --- TestStructuredOKXResult ---
// Validates: Requirements 2.5 - Structured error reporting

func TestStructuredOKXResult(t *testing.T) {
	t.Run("envelope_error_returns_structured_code", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"code":"51000","msg":"Parameter error","data":[]}`)
		}))
		defer server.Close()

		gw := newTestGateway(server.URL)
		result, err := gw.PlaceOrder(context.Background(), NormalizedOrderRequest{
			Symbol: "DOGE-USDT", Side: models.SideBuy, OrderType: models.OrderTypePostOnly,
			Price: decimal.NewFromFloat(0.15), Quantity: decimal.NewFromInt(100),
			ClOrdID: "tb1test123",
		})
		if err == nil {
			t.Fatal("expected error for OKX envelope failure")
		}
		if result.Err == nil {
			t.Fatal("expected structured error")
		}
		if result.Err.Code != "51000" {
			t.Errorf("expected code=51000, got %q", result.Err.Code)
		}
		if result.Err.Msg != "Parameter error" {
			t.Errorf("expected msg=Parameter error, got %q", result.Err.Msg)
		}
	})

	t.Run("per_item_error_returns_sCode_sMsg", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"code":"0","msg":"","data":[{"ordId":"","clOrdId":"c1","sCode":"51008","sMsg":"Insufficient balance"}]}`)
		}))
		defer server.Close()

		gw := newTestGateway(server.URL)
		result, err := gw.PlaceOrder(context.Background(), NormalizedOrderRequest{
			Symbol: "DOGE-USDT", Side: models.SideBuy, OrderType: models.OrderTypePostOnly,
			Price: decimal.NewFromFloat(0.15), Quantity: decimal.NewFromInt(100),
			ClOrdID: "tb1test456",
		})
		if err == nil {
			t.Fatal("expected error for per-item failure")
		}
		if result.Err == nil {
			t.Fatal("expected structured error")
		}
		if result.Err.SCode != "51008" {
			t.Errorf("expected sCode=51008, got %q", result.Err.SCode)
		}
		if result.Err.SMsg != "Insufficient balance" {
			t.Errorf("expected sMsg=Insufficient balance, got %q", result.Err.SMsg)
		}
	})

	t.Run("transport_error_is_marked", func(t *testing.T) {
		// Use an unreachable address to trigger transport error
		gw := newTestGateway("http://127.0.0.1:1") // port 1 won't be listening
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		result, err := gw.PlaceOrder(ctx, NormalizedOrderRequest{
			Symbol: "DOGE-USDT", Side: models.SideBuy, OrderType: models.OrderTypePostOnly,
			Price: decimal.NewFromFloat(0.15), Quantity: decimal.NewFromInt(100),
			ClOrdID: "tb1timeout",
		})
		if err == nil {
			t.Fatal("expected transport error")
		}
		if result.Err == nil || !result.Err.IsTransportError() {
			t.Error("expected transport error flag in result")
		}
	})
}

// --- TestContextDeadline ---
// Validates: Requirements 2.1, 2.5 - Context deadline cancels requests

func TestContextDeadline(t *testing.T) {
	t.Run("cancelled_context_before_send", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("request should not reach server with cancelled context")
		}))
		defer server.Close()

		gw := newTestGateway(server.URL)
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // pre-cancel

		_, err := gw.PlaceOrder(ctx, NormalizedOrderRequest{
			Symbol: "DOGE-USDT", Side: models.SideBuy, OrderType: models.OrderTypePostOnly,
			Price: decimal.NewFromFloat(0.15), Quantity: decimal.NewFromInt(100),
			ClOrdID: "tb1cancelled",
		})
		if err == nil {
			t.Fatal("expected error for cancelled context")
		}
	})

	t.Run("deadline_exceeded_during_request", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond) // slow server
			fmt.Fprint(w, `{"code":"0","msg":"","data":[{"ordId":"x","sCode":"0"}]}`)
		}))
		defer server.Close()

		gw := newTestGateway(server.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		_, err := gw.PlaceOrder(ctx, NormalizedOrderRequest{
			Symbol: "DOGE-USDT", Side: models.SideBuy, OrderType: models.OrderTypePostOnly,
			Price: decimal.NewFromFloat(0.15), Quantity: decimal.NewFromInt(100),
			ClOrdID: "tb1deadline",
		})
		if err == nil {
			t.Fatal("expected deadline exceeded error")
		}
	})
}

// --- TestQueryByClientID ---
// Validates: Requirements 2.4, 2.5 - Query by deterministic clOrdId

func TestQueryByClientID(t *testing.T) {
	t.Run("query_order_by_clOrdId", func(t *testing.T) {
		var capturedPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedPath = r.URL.String()
			fmt.Fprint(w, `{"code":"0","msg":"","data":[{
				"instId":"DOGE-USDT","ordId":"ex123","clOrdId":"tb1myid",
				"side":"buy","state":"live","px":"0.15","sz":"100",
				"accFillSz":"0","avgPx":"0","cTime":"1700000000000","uTime":"1700000001000"
			}]}`)
		}))
		defer server.Close()

		gw := newTestGateway(server.URL)
		info, err := gw.QueryOrder(context.Background(), OrderRef{
			Symbol:        "DOGE-USDT",
			ClientOrderID: "tb1myid",
		})
		if err != nil {
			t.Fatalf("QueryOrder error: %v", err)
		}
		if !strings.Contains(capturedPath, "clOrdId=tb1myid") {
			t.Errorf("expected clOrdId in path, got %q", capturedPath)
		}
		if info.ClientOrderID != "tb1myid" {
			t.Errorf("expected clOrdId=tb1myid, got %q", info.ClientOrderID)
		}
		if info.ExchangeOrderID != "ex123" {
			t.Errorf("expected ordId=ex123, got %q", info.ExchangeOrderID)
		}
		if info.Status != models.OrderStatusOpen {
			t.Errorf("expected status Open, got %v", info.Status)
		}
	})

	t.Run("query_order_by_exchangeID", func(t *testing.T) {
		var capturedPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedPath = r.URL.String()
			fmt.Fprint(w, `{"code":"0","msg":"","data":[{
				"instId":"WIF-USDT","ordId":"ex999","clOrdId":"tb1other",
				"side":"sell","state":"filled","px":"2.5","sz":"10",
				"accFillSz":"10","avgPx":"2.5","cTime":"1700000000000","uTime":"1700000002000"
			}]}`)
		}))
		defer server.Close()

		gw := newTestGateway(server.URL)
		info, err := gw.QueryOrder(context.Background(), OrderRef{
			Symbol:          "WIF-USDT",
			ExchangeOrderID: "ex999",
		})
		if err != nil {
			t.Fatalf("QueryOrder error: %v", err)
		}
		if !strings.Contains(capturedPath, "ordId=ex999") {
			t.Errorf("expected ordId in path, got %q", capturedPath)
		}
		if info.Status != models.OrderStatusFilled {
			t.Errorf("expected status Filled, got %v", info.Status)
		}
	})
}

// --- TestPagination ---
// Validates: Requirements 2.3 - Pagination support for fills/orders

func TestPagination(t *testing.T) {
	t.Run("ListPendingOrders_paginates", func(t *testing.T) {
		var callCount int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count := atomic.AddInt32(&callCount, 1)
			if count == 1 {
				// First page: return 100 orders to trigger pagination
				orders := make([]map[string]string, 100)
				for i := range orders {
					orders[i] = map[string]string{
						"instId": "DOGE-USDT", "ordId": fmt.Sprintf("o%d", i),
						"clOrdId": fmt.Sprintf("c%d", i), "side": "buy",
						"state": "live", "px": "0.15", "sz": "100",
						"accFillSz": "0", "avgPx": "0",
						"cTime": "1700000000000", "uTime": "1700000000000",
					}
				}
				resp := map[string]interface{}{"code": "0", "msg": "", "data": orders}
				json.NewEncoder(w).Encode(resp)
			} else {
				// Second page: empty to stop pagination
				fmt.Fprint(w, `{"code":"0","msg":"","data":[]}`)
			}
		}))
		defer server.Close()

		gw := newTestGateway(server.URL)
		orders, err := gw.ListPendingOrders(context.Background(), "DOGE-USDT")
		if err != nil {
			t.Fatalf("ListPendingOrders error: %v", err)
		}
		if len(orders) != 100 {
			t.Errorf("expected 100 orders, got %d", len(orders))
		}
		if atomic.LoadInt32(&callCount) != 2 {
			t.Errorf("expected 2 API calls for pagination, got %d", atomic.LoadInt32(&callCount))
		}
	})

	t.Run("ListFills_uses_cursor", func(t *testing.T) {
		var capturedPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedPath = r.URL.String()
			fmt.Fprint(w, `{"code":"0","msg":"","data":[{
				"instId":"DOGE-USDT","ordId":"o1","tradeId":"t1","billId":"b1",
				"side":"buy","fillPx":"0.15","fillSz":"100","fee":"-0.1","ts":"1700000000000"
			}]}`)
		}))
		defer server.Close()

		gw := newTestGateway(server.URL)
		page, err := gw.ListFills(context.Background(), "DOGE-USDT", FillCursor{
			After: "prev_cursor_123",
			Limit: 50,
		})
		if err != nil {
			t.Fatalf("ListFills error: %v", err)
		}
		if !strings.Contains(capturedPath, "after=prev_cursor_123") {
			t.Errorf("expected cursor in path, got %q", capturedPath)
		}
		if !strings.Contains(capturedPath, "limit=50") {
			t.Errorf("expected limit in path, got %q", capturedPath)
		}
		if len(page.Fills) != 1 {
			t.Errorf("expected 1 fill, got %d", len(page.Fills))
		}
	})
}

// --- TestInstrumentRules ---
// Validates: Requirements 2.11 - Instrument rules fetched and cached

func TestInstrumentRules(t *testing.T) {
	t.Run("fetches_and_caches_rules", func(t *testing.T) {
		var fetchCount int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&fetchCount, 1)
			fmt.Fprint(w, `{"code":"0","msg":"","data":[{
				"instId":"DOGE-USDT","tickSz":"0.00001","lotSz":"1","minSz":"10","instType":"SPOT","state":"live"
			}]}`)
		}))
		defer server.Close()

		now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		gw := newTestGateway(server.URL, WithGatewayClock(fixedClock(now)))
		cache := NewInstrumentRulesCache(gw,
			WithInstrumentRulesClock(fixedClock(now)),
			WithInstrumentRulesHardTTL(15*time.Minute),
		)

		rules, err := cache.Current(context.Background(), "DOGE-USDT")
		if err != nil {
			t.Fatalf("Current error: %v", err)
		}
		if rules.Symbol != "DOGE-USDT" {
			t.Errorf("expected symbol DOGE-USDT, got %q", rules.Symbol)
		}
		if !rules.TickSize.Equal(decimal.RequireFromString("0.00001")) {
			t.Errorf("expected tickSz=0.00001, got %s", rules.TickSize)
		}
		if !rules.LotSize.Equal(decimal.NewFromInt(1)) {
			t.Errorf("expected lotSz=1, got %s", rules.LotSize)
		}

		// Second call should use cache (no additional fetch)
		_, err = cache.Current(context.Background(), "DOGE-USDT")
		if err != nil {
			t.Fatalf("second Current error: %v", err)
		}
		if atomic.LoadInt32(&fetchCount) != 1 {
			t.Errorf("expected 1 fetch (cached), got %d", atomic.LoadInt32(&fetchCount))
		}
	})

	t.Run("non_standard_tick_size", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"code":"0","msg":"","data":[{
				"instId":"TEST-USDT","tickSz":"0.0025","lotSz":"0.001","minSz":"0.01","instType":"SPOT","state":"live"
			}]}`)
		}))
		defer server.Close()

		now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		gw := newTestGateway(server.URL, WithGatewayClock(fixedClock(now)))
		cache := NewInstrumentRulesCache(gw,
			WithInstrumentRulesClock(fixedClock(now)),
		)

		rules, err := cache.Current(context.Background(), "TEST-USDT")
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if !rules.TickSize.Equal(decimal.RequireFromString("0.0025")) {
			t.Errorf("expected tickSz=0.0025, got %s", rules.TickSize)
		}
	})
}

// --- TestArbitraryDecimalMultiple ---
// Validates: Requirements 2.11 - Decimal integer-multiple normalization
// **Validates: Requirements 2.11**

func TestArbitraryDecimalMultiple(t *testing.T) {
	t.Run("BUY_price_floors_to_tickSz", func(t *testing.T) {
		rules := models.InstrumentRules{
			Symbol:    "TEST-USDT",
			TickSize:  decimal.RequireFromString("0.0025"),
			LotSize:   decimal.RequireFromString("1"),
			MinSize:   decimal.RequireFromString("1"),
			FetchedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		}
		candidate := NormalizedOrderRequest{
			Symbol:   "TEST-USDT",
			Side:     models.SideBuy,
			Price:    decimal.RequireFromString("1.2367"),
			Quantity: decimal.NewFromInt(5),
			ClOrdID:  "test",
		}
		result := NormalizeOrder(candidate, rules, time.Now())
		if result.NoSend {
			t.Fatalf("unexpected no-send: %s", result.Reason)
		}
		// Floor(1.2367 / 0.0025) * 0.0025 = 494 * 0.0025 = 1.2350
		expected := decimal.RequireFromString("1.2350")
		if !result.Normalized.Price.Equal(expected) {
			t.Errorf("expected price %s, got %s", expected, result.Normalized.Price)
		}
		if !models.IsExactMultiple(result.Normalized.Price, rules.TickSize) {
			t.Error("price is not exact multiple of tickSz")
		}
	})

	t.Run("SELL_price_ceils_to_tickSz", func(t *testing.T) {
		rules := models.InstrumentRules{
			Symbol:    "TEST-USDT",
			TickSize:  decimal.RequireFromString("0.0025"),
			LotSize:   decimal.RequireFromString("1"),
			MinSize:   decimal.RequireFromString("1"),
			FetchedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		}
		candidate := NormalizedOrderRequest{
			Symbol:   "TEST-USDT",
			Side:     models.SideSell,
			Price:    decimal.RequireFromString("1.2367"),
			Quantity: decimal.NewFromInt(5),
			ClOrdID:  "test",
		}
		result := NormalizeOrder(candidate, rules, time.Now())
		if result.NoSend {
			t.Fatalf("unexpected no-send: %s", result.Reason)
		}
		// Ceil(1.2367 / 0.0025) * 0.0025 = 495 * 0.0025 = 1.2375
		expected := decimal.RequireFromString("1.2375")
		if !result.Normalized.Price.Equal(expected) {
			t.Errorf("expected price %s, got %s", expected, result.Normalized.Price)
		}
		if !models.IsExactMultiple(result.Normalized.Price, rules.TickSize) {
			t.Error("price is not exact multiple of tickSz")
		}
	})

	t.Run("qty_floors_to_lotSz", func(t *testing.T) {
		rules := models.InstrumentRules{
			Symbol:    "TEST-USDT",
			TickSize:  decimal.RequireFromString("0.01"),
			LotSize:   decimal.RequireFromString("0.001"),
			MinSize:   decimal.RequireFromString("0.001"),
			FetchedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		}
		candidate := NormalizedOrderRequest{
			Symbol:   "TEST-USDT",
			Side:     models.SideBuy,
			Price:    decimal.RequireFromString("100.00"),
			Quantity: decimal.RequireFromString("1.23456"),
			ClOrdID:  "test",
		}
		result := NormalizeOrder(candidate, rules, time.Now())
		if result.NoSend {
			t.Fatalf("unexpected no-send: %s", result.Reason)
		}
		// Floor(1.23456 / 0.001) * 0.001 = 1234 * 0.001 = 1.234
		expected := decimal.RequireFromString("1.234")
		if !result.Normalized.Quantity.Equal(expected) {
			t.Errorf("expected qty %s, got %s", expected, result.Normalized.Quantity)
		}
		if !models.IsExactMultiple(result.Normalized.Quantity, rules.LotSize) {
			t.Error("quantity is not exact multiple of lotSz")
		}
	})

	t.Run("exact_value_unchanged", func(t *testing.T) {
		rules := models.InstrumentRules{
			Symbol:    "DOGE-USDT",
			TickSize:  decimal.RequireFromString("0.00001"),
			LotSize:   decimal.RequireFromString("1"),
			MinSize:   decimal.RequireFromString("10"),
			FetchedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		}
		candidate := NormalizedOrderRequest{
			Symbol:   "DOGE-USDT",
			Side:     models.SideBuy,
			Price:    decimal.RequireFromString("0.15000"),
			Quantity: decimal.NewFromInt(100),
			ClOrdID:  "test",
		}
		result := NormalizeOrder(candidate, rules, time.Now())
		if result.NoSend {
			t.Fatalf("unexpected no-send: %s", result.Reason)
		}
		if !result.Normalized.Price.Equal(decimal.RequireFromString("0.15000")) {
			t.Errorf("exact price should be unchanged, got %s", result.Normalized.Price)
		}
	})

	t.Run("property_arbitrary_tick_lot", func(t *testing.T) {
		rapid.Check(t, func(t *rapid.T) {
			// Generate arbitrary positive tick/lot sizes that are valid decimals
			tickExp := rapid.IntRange(-6, -1).Draw(t, "tick_exp")
			tickMantissa := rapid.IntRange(1, 999).Draw(t, "tick_mantissa")
			tickSz := decimal.New(int64(tickMantissa), int32(tickExp))

			lotExp := rapid.IntRange(-6, 0).Draw(t, "lot_exp")
			lotMantissa := rapid.IntRange(1, 999).Draw(t, "lot_mantissa")
			lotSz := decimal.New(int64(lotMantissa), int32(lotExp))

			minSz := lotSz

			rules := models.InstrumentRules{
				Symbol:    "GEN-USDT",
				TickSize:  tickSz,
				LotSize:   lotSz,
				MinSize:   minSz,
				FetchedAt: time.Now(),
				ExpiresAt: time.Now().Add(time.Hour),
			}

			// Generate an arbitrary price and quantity
			priceInt := rapid.IntRange(1, 100000).Draw(t, "price_int")
			priceFrac := rapid.IntRange(0, 999999).Draw(t, "price_frac")
			price := decimal.New(int64(priceInt), 0).Add(decimal.New(int64(priceFrac), -6))

			qtyInt := rapid.IntRange(1, 10000).Draw(t, "qty_int")
			qtyFrac := rapid.IntRange(0, 999999).Draw(t, "qty_frac")
			qty := decimal.New(int64(qtyInt), 0).Add(decimal.New(int64(qtyFrac), -6))

			side := models.SideBuy
			if rapid.Bool().Draw(t, "is_sell") {
				side = models.SideSell
			}

			candidate := NormalizedOrderRequest{
				Symbol:   "GEN-USDT",
				Side:     side,
				Price:    price,
				Quantity: qty,
				ClOrdID:  "gen-test",
			}
			result := NormalizeOrder(candidate, rules, time.Now())

			if result.NoSend {
				return // no-send is valid (e.g., qty below min)
			}

			// Property: normalized price is exact multiple of tickSz
			if !models.IsExactMultiple(result.Normalized.Price, tickSz) {
				t.Fatalf("price %s not exact multiple of tickSz %s",
					result.Normalized.Price, tickSz)
			}
			// Property: normalized qty is exact multiple of lotSz
			if !models.IsExactMultiple(result.Normalized.Quantity, lotSz) {
				t.Fatalf("qty %s not exact multiple of lotSz %s",
					result.Normalized.Quantity, lotSz)
			}
			// Property: quantity >= minSz
			if result.Normalized.Quantity.LessThan(minSz) {
				t.Fatalf("qty %s below minSz %s",
					result.Normalized.Quantity, minSz)
			}
			// Property: BUY price <= original, SELL price >= original
			if side == models.SideBuy {
				if result.Normalized.Price.GreaterThan(price) {
					t.Fatalf("BUY normalized price %s > original %s",
						result.Normalized.Price, price)
				}
			} else {
				if result.Normalized.Price.LessThan(price) {
					t.Fatalf("SELL normalized price %s < original %s",
						result.Normalized.Price, price)
				}
			}
		})
	})
}

// --- TestMinimums ---
// Validates: Requirements 2.11 - minSz and minNotional enforcement

func TestMinimums(t *testing.T) {
	t.Run("below_minSz_is_no_send", func(t *testing.T) {
		rules := models.InstrumentRules{
			Symbol:    "DOGE-USDT",
			TickSize:  decimal.RequireFromString("0.00001"),
			LotSize:   decimal.NewFromInt(1),
			MinSize:   decimal.NewFromInt(10),
			FetchedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		}
		candidate := NormalizedOrderRequest{
			Symbol:   "DOGE-USDT",
			Side:     models.SideBuy,
			Price:    decimal.RequireFromString("0.15"),
			Quantity: decimal.NewFromInt(5), // below minSz=10
			ClOrdID:  "test",
		}
		result := NormalizeOrder(candidate, rules, time.Now())
		if !result.NoSend {
			t.Fatal("expected no-send for qty below minSz")
		}
		if !strings.Contains(result.Reason, "minSz") {
			t.Errorf("expected minSz in reason, got %q", result.Reason)
		}
	})

	t.Run("below_minNotional_is_no_send", func(t *testing.T) {
		rules := models.InstrumentRules{
			Symbol:      "WIF-USDT",
			TickSize:    decimal.RequireFromString("0.0001"),
			LotSize:     decimal.RequireFromString("0.1"),
			MinSize:     decimal.RequireFromString("0.1"),
			MinNotional: decimal.NewFromInt(5), // price*qty must be >= 5
			FetchedAt:   time.Now(),
			ExpiresAt:   time.Now().Add(time.Hour),
		}
		candidate := NormalizedOrderRequest{
			Symbol:   "WIF-USDT",
			Side:     models.SideBuy,
			Price:    decimal.RequireFromString("2.0000"),
			Quantity: decimal.RequireFromString("2.0"), // 2*2=4 < minNotional=5
			ClOrdID:  "test",
		}
		result := NormalizeOrder(candidate, rules, time.Now())
		if !result.NoSend {
			t.Fatal("expected no-send for notional below minNotional")
		}
		if !strings.Contains(result.Reason, "minNotional") {
			t.Errorf("expected minNotional in reason, got %q", result.Reason)
		}
	})

	t.Run("at_exact_minSz_is_allowed", func(t *testing.T) {
		rules := models.InstrumentRules{
			Symbol:    "DOGE-USDT",
			TickSize:  decimal.RequireFromString("0.00001"),
			LotSize:   decimal.NewFromInt(1),
			MinSize:   decimal.NewFromInt(10),
			FetchedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		}
		candidate := NormalizedOrderRequest{
			Symbol:   "DOGE-USDT",
			Side:     models.SideBuy,
			Price:    decimal.RequireFromString("0.15000"),
			Quantity: decimal.NewFromInt(10),
			ClOrdID:  "test",
		}
		result := NormalizeOrder(candidate, rules, time.Now())
		if result.NoSend {
			t.Fatalf("at-minSz should be allowed: %s", result.Reason)
		}
	})
}

// --- TestStaleRules ---
// Validates: Requirements 2.11 - Expired rules block orders

func TestStaleRules(t *testing.T) {
	t.Run("expired_rules_return_no_send", func(t *testing.T) {
		expired := models.InstrumentRules{
			Symbol:    "DOGE-USDT",
			TickSize:  decimal.RequireFromString("0.00001"),
			LotSize:   decimal.NewFromInt(1),
			MinSize:   decimal.NewFromInt(10),
			FetchedAt: time.Now().Add(-20 * time.Minute),
			ExpiresAt: time.Now().Add(-5 * time.Minute), // expired 5 min ago
		}
		candidate := NormalizedOrderRequest{
			Symbol:   "DOGE-USDT",
			Side:     models.SideBuy,
			Price:    decimal.RequireFromString("0.15000"),
			Quantity: decimal.NewFromInt(100),
			ClOrdID:  "test",
		}
		result := NormalizeOrder(candidate, expired, time.Now())
		if !result.NoSend {
			t.Fatal("expected no-send for expired rules")
		}
		if !strings.Contains(result.Reason, "expired") {
			t.Errorf("expected 'expired' in reason, got %q", result.Reason)
		}
	})

	t.Run("symbol_mismatch_returns_no_send", func(t *testing.T) {
		rules := models.InstrumentRules{
			Symbol:    "WIF-USDT",
			TickSize:  decimal.RequireFromString("0.0001"),
			LotSize:   decimal.RequireFromString("0.1"),
			MinSize:   decimal.RequireFromString("0.1"),
			FetchedAt: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		}
		candidate := NormalizedOrderRequest{
			Symbol:   "DOGE-USDT",
			Side:     models.SideBuy,
			Price:    decimal.RequireFromString("0.15000"),
			Quantity: decimal.NewFromInt(100),
			ClOrdID:  "test",
		}
		result := NormalizeOrder(candidate, rules, time.Now())
		if !result.NoSend {
			t.Fatal("expected no-send for symbol mismatch")
		}
		if !strings.Contains(result.Reason, "mismatch") {
			t.Errorf("expected 'mismatch' in reason, got %q", result.Reason)
		}
	})

	t.Run("cache_refreshes_on_expiry", func(t *testing.T) {
		var fetchCount int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&fetchCount, 1)
			fmt.Fprint(w, `{"code":"0","msg":"","data":[{
				"instId":"DOGE-USDT","tickSz":"0.00001","lotSz":"1","minSz":"10","instType":"SPOT","state":"live"
			}]}`)
		}))
		defer server.Close()

		// Start with current time
		now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		currentTime := now
		clock := func() time.Time { return currentTime }

		gw := newTestGateway(server.URL, WithGatewayClock(clock))
		cache := NewInstrumentRulesCache(gw,
			WithInstrumentRulesClock(clock),
			WithInstrumentRulesHardTTL(15*time.Minute),
		)

		// First fetch
		_, err := cache.Current(context.Background(), "DOGE-USDT")
		if err != nil {
			t.Fatalf("first fetch error: %v", err)
		}

		// Advance time past hard TTL
		currentTime = now.Add(16 * time.Minute)
		_, err = cache.Current(context.Background(), "DOGE-USDT")
		if err != nil {
			t.Fatalf("refresh error: %v", err)
		}

		if atomic.LoadInt32(&fetchCount) != 2 {
			t.Errorf("expected 2 fetches (initial + refresh), got %d", atomic.LoadInt32(&fetchCount))
		}
	})
}
