package execution

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/internal/config"
	"github.com/yourname/okx-hft-grid/internal/monitor"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

type preservationRoundTripFunc func(*http.Request) (*http.Response, error)

func (f preservationRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func preservationResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func preservationClient(transport http.RoundTripper) *APIClient {
	client := NewAPIClient("http://127.0.0.1", &config.Credentials{
		APIKey: "synthetic-preservation-api-key", SecretKey: "synthetic-preservation-secret-key", Passphrase: "synthetic-preservation-passphrase",
	}, nil)
	client.httpClient.Transport = transport
	client.httpClient.Timeout = time.Second
	return client
}

func preservationHandler(client *APIClient, quantity decimal.Decimal) *GridFillHandler {
	return NewGridFillHandler(
		client,
		[]models.GridConfig{{Symbol: "DOGE-USDT", OrderSize: quantity}, {Symbol: "WIF-USDT", OrderSize: quantity}},
		map[string][]decimal.Decimal{
			"DOGE-USDT": {decimal.NewFromInt(1), decimal.NewFromInt(2), decimal.NewFromInt(3)},
			"WIF-USDT":  {decimal.NewFromInt(1), decimal.NewFromInt(2), decimal.NewFromInt(3)},
		},
		monitor.NewStructuredLogger(io.Discard),
	)
}

func capturePreservationOrders(quantity decimal.Decimal, includeWIF bool, dogeFirst bool) ([]OKXOrderRequest, error) {
	var (
		mu       sync.Mutex
		requests []OKXOrderRequest
		firstErr error
	)
	transport := preservationRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() != "127.0.0.1" {
			return nil, fmt.Errorf("non-loopback host intercepted: %s", req.URL.Redacted())
		}
		defer req.Body.Close()
		var order OKXOrderRequest
		if err := json.NewDecoder(req.Body).Decode(&order); err != nil {
			return nil, err
		}
		mu.Lock()
		requests = append(requests, order)
		requestNumber := len(requests)
		mu.Unlock()
		if order.InstID == "WIF-USDT" {
			return preservationResponse(`{"code":"0","msg":"","data":[{"ordId":"","clOrdId":"","sCode":"51008","sMsg":"simulated local rejection"}]}`), nil
		}
		return preservationResponse(fmt.Sprintf(
			`{"code":"0","msg":"","data":[{"ordId":"sim-doge-%d","clOrdId":"","sCode":"0","sMsg":""}]}`,
			requestNumber,
		)), nil
	})
	handler := preservationHandler(preservationClient(transport), quantity)
	invokeDOGE := func() { handler.OnFill("DOGE-USDT", "buy", "2", quantity.String(), "doge-owned-fill", "filled") }
	invokeWIF := func() { handler.OnFill("WIF-USDT", "buy", "2", quantity.String(), "wif-owned-fill", "filled") }
	if includeWIF && !dogeFirst {
		invokeWIF()
	}
	invokeDOGE()
	if includeWIF && dogeFirst {
		invokeWIF()
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]OKXOrderRequest(nil), requests...), firstErr
}

// **Validates: Requirements 3.9**
//
// PRE-09 compares the healthy DOGE effect with and without an interleaved WIF
// local rejection. WIF's bug-condition behavior is deliberately not blessed;
// the peer symbol's exact side, price, quantity and order type are preserved.
func TestProperty2_Preservation_PRE09_SymbolLocalFailureLeavesHealthyPeerUnchanged(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		quantity := decimal.NewFromInt(int64(rapid.IntRange(1, 10_000).Draw(t, "quantity")))
		dogeFirst := rapid.Bool().Draw(t, "doge_first")
		baseline, err := capturePreservationOrders(quantity, false, true)
		if err != nil {
			t.Fatalf("PRE-09 baseline transport: %v", err)
		}
		interleaved, err := capturePreservationOrders(quantity, true, dogeFirst)
		if err != nil {
			t.Fatalf("PRE-09 interleaved transport: %v", err)
		}
		baselineDOGE := filterPreservationOrders(baseline, "DOGE-USDT")
		interleavedDOGE := filterPreservationOrders(interleaved, "DOGE-USDT")
		if len(baselineDOGE) != 1 || len(interleavedDOGE) != 1 {
			t.Fatalf("PRE-09 DOGE effects changed: baseline=%v interleaved=%v", baselineDOGE, interleavedDOGE)
		}
		want, got := baselineDOGE[0], interleavedDOGE[0]
		if got.InstID != want.InstID || got.Side != want.Side || got.OrdType != want.OrdType || got.Px != want.Px || got.Sz != want.Sz {
			t.Fatalf("PRE-09 peer output changed: got=%+v want=%+v", got, want)
		}
		if got.InstID != "DOGE-USDT" || got.Side != "sell" || got.OrdType != "post_only" || got.Px != "3" || got.Sz != quantity.String() {
			t.Fatalf("PRE-09 DOGE baseline semantics changed: %+v", got)
		}
	})
}

func filterPreservationOrders(orders []OKXOrderRequest, symbol string) []OKXOrderRequest {
	result := make([]OKXOrderRequest, 0, len(orders))
	for _, order := range orders {
		if order.InstID == symbol {
			result = append(result, order)
		}
	}
	return result
}

type preservationIsolationRecorder struct {
	mu                         sync.Mutex
	transportCalls             int
	productionRequests         int
	orderMutations             int
	realCredentialObservations int
}

func (r *preservationIsolationRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transportCalls++
	if req.URL.Hostname() != "127.0.0.1" {
		r.productionRequests++
	}
	if req.Method != http.MethodGet {
		r.orderMutations++
	}
	if req.Header.Get("OK-ACCESS-KEY") != "synthetic-preservation-api-key" ||
		req.Header.Get("OK-ACCESS-PASSPHRASE") != "synthetic-preservation-passphrase" {
		r.realCredentialObservations++
	}
	return preservationResponse(`{"code":"0","msg":"","data":[]}`), nil
}

func (r *preservationIsolationRecorder) snapshot() preservationIsolationRecorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	return preservationIsolationRecorder{
		transportCalls: r.transportCalls, productionRequests: r.productionRequests,
		orderMutations: r.orderMutations, realCredentialObservations: r.realCredentialObservations,
	}
}

// **Validates: Requirements 3.12**
//
// PRE-12 dynamically proves that the preservation transport is in-memory,
// literal-loopback only and read-only. Because RoundTrip is replaced, DNS and
// dial counts are structurally zero; production request/order counts are asserted.
func TestProperty2_Preservation_PRE12_InMemoryTransportHasNoProductionEffects(t *testing.T) {
	t.Setenv("OKX_API_KEY", "")
	t.Setenv("OKX_SECRET_KEY", "")
	t.Setenv("OKX_PASSPHRASE", "")

	rapid.Check(t, func(t *rapid.T) {
		paths := []string{
			"/api/v5/account/balance",
			"/api/v5/market/ticker?instId=DOGE-USDT",
			"/api/v5/market/ticker?instId=WIF-USDT",
		}
		path := paths[rapid.IntRange(0, len(paths)-1).Draw(t, "read_only_path")]
		recorder := &preservationIsolationRecorder{}
		client := preservationClient(recorder)
		response, err := client.DoRequest(http.MethodGet, path, nil)
		if response != nil {
			response.Body.Close()
		}
		if err != nil {
			t.Fatalf("PRE-12 intercepted request error: %v", err)
		}
		observed := recorder.snapshot()
		if observed.transportCalls != 1 || observed.productionRequests != 0 || observed.orderMutations != 0 || observed.realCredentialObservations != 0 {
			t.Fatalf("PRE-12 isolation changed: transport=%d production=%d mutations=%d real_credentials=%d dns=0 dial=0",
				observed.transportCalls, observed.productionRequests, observed.orderMutations, observed.realCredentialObservations)
		}
	})
}
