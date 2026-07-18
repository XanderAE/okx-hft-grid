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

// **Validates: Requirements 3.1, 3.2**
//
// Preservation Property — Non-BUY Fills & Zero-Fee BUY Fills Unchanged
//
// For SELL fills: counter BUY order uses cfg.OrderSize (USDT spend, no fee deduction).
// For BUY fills with feeRate=0: counter SELL quantity equals full fillSz.
//
// These behaviors must remain unchanged after the bugfix is applied.
// This test PASSES on unfixed code (confirms baseline to preserve).
func TestPreservation_SellQuantity_NonBugConditionUnchanged(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Choose which preservation path to test
		testSellFill := rapid.Bool().Draw(rt, "testSellFill")

		// Generate random order size (cfg.OrderSize) — between 1 and 10000
		orderSizeCents := rapid.Int64Range(100, 1000000).Draw(rt, "orderSizeCents")
		orderSize := decimal.NewFromInt(orderSizeCents).Div(decimal.NewFromInt(100))

		// Generate random fill size — between 1 and 10000
		fillSzCents := rapid.Int64Range(100, 1000000).Draw(rt, "fillSzCents")
		fillSz := decimal.NewFromInt(fillSzCents).Div(decimal.NewFromInt(100))

		// Grid levels: level[0]=1.00, level[1]=1.50, level[2]=2.00
		levels := []decimal.Decimal{
			decimal.NewFromFloat(1.00),
			decimal.NewFromFloat(1.50),
			decimal.NewFromFloat(2.00),
		}

		// Capture the placed order via HTTP transport
		var capturedOrder OKXOrderRequest
		var orderCaptured bool
		transport := preservationSellQtyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			defer req.Body.Close()
			if err := json.NewDecoder(req.Body).Decode(&capturedOrder); err != nil {
				return nil, err
			}
			orderCaptured = true
			return preservationSellQtyResponse(
				`{"code":"0","msg":"","data":[{"ordId":"mock-pres-order","clOrdId":"","sCode":"0","sMsg":""}]}`,
			), nil
		})

		client := preservationSellQtyAPIClient(transport)

		if testSellFill {
			// --- SELL fill path: counter BUY must use cfg.OrderSize ---
			handler := NewGridFillHandler(
				client,
				[]models.GridConfig{{
					Symbol:    "DOGE-USDT",
					OrderSize: orderSize,
					FeeRate:   decimal.NewFromFloat(0.0008), // fee rate irrelevant for sell fills
				}},
				map[string][]decimal.Decimal{"DOGE-USDT": levels},
				monitor.NewStructuredLogger(io.Discard),
			)

			// Fill at level[1] (1.50) — counter BUY at level[0] (1.00)
			handler.OnFill("DOGE-USDT", "sell", "1.50", fillSz.String(), "sell-fill-order", "filled")

			if !orderCaptured {
				rt.Fatalf("SELL fill: counter BUY order was not placed")
			}

			// Verify counter-order is a BUY
			if capturedOrder.Side != "buy" {
				rt.Fatalf("SELL fill: counter-order side should be 'buy', got %q", capturedOrder.Side)
			}

			// PRESERVATION: counter BUY quantity must equal cfg.OrderSize
			capturedQty, err := decimal.NewFromString(capturedOrder.Sz)
			if err != nil {
				rt.Fatalf("SELL fill: failed to parse captured order size %q: %v", capturedOrder.Sz, err)
			}

			if !capturedQty.Equal(orderSize) {
				rt.Fatalf("PRESERVATION VIOLATED: SELL fill counter BUY quantity = %s, expected cfg.OrderSize = %s\n"+
					"  fillSz=%s (should be ignored for counter BUY)",
					capturedQty.String(), orderSize.String(), fillSz.String())
			}
		} else {
			// --- BUY fill with feeRate=0 path: counter SELL must use full fillSz ---
			// When feeRate=0, fillSz * (1 - 0) = fillSz, so the counter SELL uses
			// the full fill size (net = gross when fee is zero).
			handler := NewGridFillHandler(
				client,
				[]models.GridConfig{{
					Symbol:    "DOGE-USDT",
					OrderSize: orderSize,
					FeeRate:   decimal.Zero, // feeRate = 0
				}},
				map[string][]decimal.Decimal{"DOGE-USDT": levels},
				monitor.NewStructuredLogger(io.Discard),
			)

			// Fill at level[0] (1.00) — counter SELL at level[1] (1.50)
			handler.OnFill("DOGE-USDT", "buy", "1.00", fillSz.String(), "buy-fill-order", "filled")

			if !orderCaptured {
				rt.Fatalf("BUY fill (feeRate=0): counter SELL order was not placed")
			}

			// Verify counter-order is a SELL
			if capturedOrder.Side != "sell" {
				rt.Fatalf("BUY fill (feeRate=0): counter-order side should be 'sell', got %q", capturedOrder.Side)
			}

			// PRESERVATION: counter SELL quantity must equal fillSz (full fill size)
			// When feeRate=0, fillSz * (1 - 0) = fillSz — no fee deduction
			capturedQty, err := decimal.NewFromString(capturedOrder.Sz)
			if err != nil {
				rt.Fatalf("BUY fill (feeRate=0): failed to parse captured order size %q: %v", capturedOrder.Sz, err)
			}

			if !capturedQty.Equal(fillSz) {
				rt.Fatalf("PRESERVATION VIOLATED: BUY fill (feeRate=0) counter SELL quantity = %s, expected fillSz = %s\n"+
					"  cfg.OrderSize=%s, feeRate=0",
					capturedQty.String(), fillSz.String(), orderSize.String())
			}
		}
	})
}

// --- Test helpers ---

type preservationSellQtyRoundTripFunc func(*http.Request) (*http.Response, error)

func (f preservationSellQtyRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func preservationSellQtyResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func preservationSellQtyAPIClient(transport http.RoundTripper) *APIClient {
	client := NewAPIClient("http://127.0.0.1", &config.Credentials{
		APIKey:     fmt.Sprintf("synthetic-preservation-sell-qty-key-%d", time.Now().UnixNano()),
		SecretKey:  "synthetic-preservation-sell-qty-secret",
		Passphrase: "synthetic-preservation-sell-qty-passphrase",
	}, nil)
	client.httpClient.Transport = transport
	client.httpClient.Timeout = time.Second
	return client
}
