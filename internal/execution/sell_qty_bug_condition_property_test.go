package execution

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/internal/config"
	"github.com/yourname/okx-hft-grid/internal/monitor"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirements 1.1, 1.2, 2.1, 2.2**
//
// Bug Condition Exploration — SELL Quantity Fee Deduction
//
// For BUY fill events in cash mode with feeRate > 0, the counter SELL quantity
// SHOULD equal fillSz * (1 - feeRate). On unfixed code, the handler uses
// cfg.OrderSize instead, so this test is EXPECTED TO FAIL on unfixed code.
//
// isBugCondition_SellQuantity: X.side = "buy" AND X.tdMode = "cash" AND X.feeRate > 0
func TestBugCondition_SellQuantity_FeeAdjusted(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate random fill parameters satisfying bug condition:
		// feeRate in (0, 0.01] — OKX maker fees are typically 0.02%-0.1%
		feeRateBps := rapid.IntRange(1, 100).Draw(rt, "feeRateBps") // 1-100 basis points
		feeRate := decimal.NewFromInt(int64(feeRateBps)).Div(decimal.NewFromInt(10000))

		// fillSz > 0 — random fill size between 1 and 10000
		fillSzCents := rapid.Int64Range(100, 1000000).Draw(rt, "fillSzCents")
		fillSz := decimal.NewFromInt(fillSzCents).Div(decimal.NewFromInt(100))

		// OrderSize is intentionally DIFFERENT from fillSz*(1-feeRate) to expose the bug
		orderSizeCents := rapid.Int64Range(100, 1000000).Draw(rt, "orderSizeCents")
		orderSize := decimal.NewFromInt(orderSizeCents).Div(decimal.NewFromInt(100))

		// Grid levels: fill happens at level[0], counter-SELL at level[1]
		fillPrice := decimal.NewFromFloat(1.50)
		counterPrice := decimal.NewFromFloat(1.55)
		levels := []decimal.Decimal{fillPrice, counterPrice}

		// Capture the placed order via HTTP transport
		var capturedOrder OKXOrderRequest
		var orderCaptured bool
		transport := sellQtyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			defer req.Body.Close()
			if err := json.NewDecoder(req.Body).Decode(&capturedOrder); err != nil {
				return nil, err
			}
			orderCaptured = true
			return sellQtyResponse(fmt.Sprintf(
				`{"code":"0","msg":"","data":[{"ordId":"mock-sell-%d","clOrdId":"","sCode":"0","sMsg":""}]}`,
				fillSzCents,
			)), nil
		})

		client := sellQtyAPIClient(transport)
		handler := NewGridFillHandler(
			client,
			[]models.GridConfig{{
				Symbol:    "WIF-USDT",
				OrderSize: orderSize,
				FeeRate:   feeRate,
			}},
			map[string][]decimal.Decimal{
				"WIF-USDT": levels,
			},
			monitor.NewStructuredLogger(io.Discard),
		)

		// Call OnFill with a BUY fill — this should produce counter SELL
		handler.OnFill(
			"WIF-USDT",
			"buy",
			fillPrice.String(),
			fillSz.String(),
			"order-123",
			"filled",
		)

		if !orderCaptured {
			rt.Fatalf("counter-order was not placed")
		}

		// Verify counter-order is a SELL
		if capturedOrder.Side != "sell" {
			rt.Fatalf("counter-order side should be 'sell', got %q", capturedOrder.Side)
		}

		// Expected: counter SELL quantity = fillSz * (1 - feeRate)
		expectedQty := fillSz.Mul(decimal.NewFromInt(1).Sub(feeRate))
		capturedQty, err := decimal.NewFromString(capturedOrder.Sz)
		if err != nil {
			rt.Fatalf("failed to parse captured order size %q: %v", capturedOrder.Sz, err)
		}

		if !capturedQty.Equal(expectedQty) {
			rt.Fatalf("BUG CONFIRMED: counter SELL quantity = %s (cfg.OrderSize=%s), expected = %s (fillSz * (1 - feeRate))\n"+
				"  fillSz=%s, feeRate=%s",
				capturedQty.String(), orderSize.String(), expectedQty.String(),
				fillSz.String(), feeRate.String())
		}
	})
}

// --- Test helpers ---

type sellQtyRoundTripFunc func(*http.Request) (*http.Response, error)

func (f sellQtyRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func sellQtyResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func sellQtyAPIClient(transport http.RoundTripper) *APIClient {
	client := NewAPIClient("http://127.0.0.1", &config.Credentials{
		APIKey:     "synthetic-bug-exploration-key",
		SecretKey:  "synthetic-bug-exploration-secret",
		Passphrase: "synthetic-bug-exploration-passphrase",
	}, nil)
	client.httpClient.Transport = transport
	client.httpClient.Timeout = time.Second
	return client
}
