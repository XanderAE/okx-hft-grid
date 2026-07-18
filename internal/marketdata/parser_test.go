package marketdata

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// makeValidOKXMessage creates a valid OKX ticker JSON for testing.
func makeValidOKXMessage(t *testing.T, nowMs int64) []byte {
	t.Helper()
	msg := OKXTickerMessage{
		Arg: OKXTickerArg{
			Channel: "tickers",
			InstID:  "BTC-USDT",
		},
		Data: []OKXTickerData{
			{
				InstID: "BTC-USDT",
				Last:   "50000.50",
				BidPx:  "50000.00",
				AskPx:  "50001.00",
				BidSz:  "1.5",
				AskSz:  "2.0",
				Vol24h: "10000",
				Ts:     fmt.Sprintf("%d", nowMs),
				SeqId:  100,
			},
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal message: %v", err)
	}
	return data
}

func TestParseAndValidate_ValidMessage(t *testing.T) {
	now := time.Now()
	nowMs := now.UnixMilli()

	var invalidCalled bool
	parser := NewParser(func(symbol, reason string) {
		invalidCalled = true
	})
	parser.timeNow = func() time.Time { return now }

	data := makeValidOKXMessage(t, nowMs)
	tick, err := parser.ParseAndValidate(data)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if tick == nil {
		t.Fatal("expected tick, got nil")
	}
	if tick.Symbol != "BTC-USDT" {
		t.Errorf("expected symbol BTC-USDT, got %s", tick.Symbol)
	}
	if !tick.LastPrice.Equal(decimal.NewFromFloat(50000.50)) {
		t.Errorf("unexpected lastPrice: %s", tick.LastPrice.String())
	}
	if !tick.BestBid.Equal(decimal.NewFromFloat(50000.00)) {
		t.Errorf("unexpected bestBid: %s", tick.BestBid.String())
	}
	if !tick.BestAsk.Equal(decimal.NewFromFloat(50001.00)) {
		t.Errorf("unexpected bestAsk: %s", tick.BestAsk.String())
	}
	if tick.SequenceId != 100 {
		t.Errorf("unexpected sequenceId: %d", tick.SequenceId)
	}
	if invalidCalled {
		t.Error("DATA_INVALID callback should not have been called")
	}
	if parser.GetValidationFailureCount() != 0 {
		t.Errorf("expected 0 failures, got %d", parser.GetValidationFailureCount())
	}
}

func TestParseAndValidate_InvalidJSON(t *testing.T) {
	var invalidCalled bool
	parser := NewParser(func(symbol, reason string) {
		invalidCalled = true
	})

	_, err := parser.ParseAndValidate([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !invalidCalled {
		t.Error("expected DATA_INVALID callback for invalid JSON")
	}
	if parser.GetValidationFailureCount() != 1 {
		t.Errorf("expected 1 failure, got %d", parser.GetValidationFailureCount())
	}
}

func TestParseAndValidate_NegativePrice(t *testing.T) {
	now := time.Now()
	parser := NewParser(func(symbol, reason string) {})
	parser.timeNow = func() time.Time { return now }

	msg := OKXTickerMessage{
		Arg: OKXTickerArg{Channel: "tickers", InstID: "DOGE-USDT"},
		Data: []OKXTickerData{
			{
				InstID: "DOGE-USDT",
				Last:   "-1.0",
				BidPx:  "0.05",
				AskPx:  "0.06",
				BidSz:  "100",
				AskSz:  "200",
				Vol24h: "5000",
				Ts:     fmt.Sprintf("%d", now.UnixMilli()),
				SeqId:  1,
			},
		},
	}
	data, _ := json.Marshal(msg)

	_, err := parser.ParseAndValidate(data)
	if err == nil {
		t.Fatal("expected error for negative lastPrice")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected ValidationError, got %T", err)
	}
	if ve.Symbol != "DOGE-USDT" {
		t.Errorf("expected symbol DOGE-USDT, got %s", ve.Symbol)
	}
}

func TestParseAndValidate_PriceExceedsMax(t *testing.T) {
	now := time.Now()
	parser := NewParser(func(symbol, reason string) {})
	parser.timeNow = func() time.Time { return now }

	msg := OKXTickerMessage{
		Arg: OKXTickerArg{Channel: "tickers", InstID: "BTC-USDT"},
		Data: []OKXTickerData{
			{
				InstID: "BTC-USDT",
				Last:   "100000000.00", // > 99,999,999.99
				BidPx:  "50000.00",
				AskPx:  "50001.00",
				BidSz:  "1.0",
				AskSz:  "1.0",
				Vol24h: "5000",
				Ts:     fmt.Sprintf("%d", now.UnixMilli()),
				SeqId:  1,
			},
		},
	}
	data, _ := json.Marshal(msg)

	_, err := parser.ParseAndValidate(data)
	if err == nil {
		t.Fatal("expected error for price exceeding max")
	}
}

func TestParseAndValidate_CrossedBook(t *testing.T) {
	now := time.Now()
	parser := NewParser(func(symbol, reason string) {})
	parser.timeNow = func() time.Time { return now }

	msg := OKXTickerMessage{
		Arg: OKXTickerArg{Channel: "tickers", InstID: "ETH-USDT"},
		Data: []OKXTickerData{
			{
				InstID: "ETH-USDT",
				Last:   "3000.00",
				BidPx:  "3001.00", // bid >= ask → crossed
				AskPx:  "3000.50",
				BidSz:  "1.0",
				AskSz:  "1.0",
				Vol24h: "5000",
				Ts:     fmt.Sprintf("%d", now.UnixMilli()),
				SeqId:  1,
			},
		},
	}
	data, _ := json.Marshal(msg)

	_, err := parser.ParseAndValidate(data)
	if err == nil {
		t.Fatal("expected error for crossed book")
	}
}

func TestParseAndValidate_TimestampTooOld(t *testing.T) {
	now := time.Now()
	parser := NewParser(func(symbol, reason string) {})
	parser.timeNow = func() time.Time { return now }

	oldTs := now.Add(-10 * time.Second).UnixMilli()
	msg := OKXTickerMessage{
		Arg: OKXTickerArg{Channel: "tickers", InstID: "SOL-USDT"},
		Data: []OKXTickerData{
			{
				InstID: "SOL-USDT",
				Last:   "100.00",
				BidPx:  "99.90",
				AskPx:  "100.10",
				BidSz:  "10",
				AskSz:  "10",
				Vol24h: "50000",
				Ts:     fmt.Sprintf("%d", oldTs),
				SeqId:  1,
			},
		},
	}
	data, _ := json.Marshal(msg)

	_, err := parser.ParseAndValidate(data)
	if err == nil {
		t.Fatal("expected error for old timestamp")
	}
}

func TestParseAndValidate_TimestampInFuture(t *testing.T) {
	now := time.Now()
	parser := NewParser(func(symbol, reason string) {})
	parser.timeNow = func() time.Time { return now }

	futureTs := now.Add(10 * time.Second).UnixMilli()
	msg := OKXTickerMessage{
		Arg: OKXTickerArg{Channel: "tickers", InstID: "SOL-USDT"},
		Data: []OKXTickerData{
			{
				InstID: "SOL-USDT",
				Last:   "100.00",
				BidPx:  "99.90",
				AskPx:  "100.10",
				BidSz:  "10",
				AskSz:  "10",
				Vol24h: "50000",
				Ts:     fmt.Sprintf("%d", futureTs),
				SeqId:  1,
			},
		},
	}
	data, _ := json.Marshal(msg)

	_, err := parser.ParseAndValidate(data)
	if err == nil {
		t.Fatal("expected error for future timestamp")
	}
}

func TestParseAndValidate_SequenceIdNotIncreasing(t *testing.T) {
	now := time.Now()
	parser := NewParser(func(symbol, reason string) {})
	parser.timeNow = func() time.Time { return now }

	makeMsg := func(seqId int64) []byte {
		msg := OKXTickerMessage{
			Arg: OKXTickerArg{Channel: "tickers", InstID: "DOGE-USDT"},
			Data: []OKXTickerData{
				{
					InstID: "DOGE-USDT",
					Last:   "0.10",
					BidPx:  "0.09",
					AskPx:  "0.11",
					BidSz:  "1000",
					AskSz:  "1000",
					Vol24h: "100000",
					Ts:     fmt.Sprintf("%d", now.UnixMilli()),
					SeqId:  seqId,
				},
			},
		}
		data, _ := json.Marshal(msg)
		return data
	}

	// First message should pass.
	_, err := parser.ParseAndValidate(makeMsg(10))
	if err != nil {
		t.Fatalf("first message should pass, got: %v", err)
	}

	// Same seqId should fail.
	_, err = parser.ParseAndValidate(makeMsg(10))
	if err == nil {
		t.Fatal("expected error for same sequenceId")
	}

	// Lower seqId should fail.
	_, err = parser.ParseAndValidate(makeMsg(5))
	if err == nil {
		t.Fatal("expected error for lower sequenceId")
	}

	// Higher seqId should pass.
	_, err = parser.ParseAndValidate(makeMsg(11))
	if err != nil {
		t.Fatalf("higher sequenceId should pass, got: %v", err)
	}
}

func TestParseAndValidate_SequenceIdPerSymbol(t *testing.T) {
	now := time.Now()
	parser := NewParser(func(symbol, reason string) {})
	parser.timeNow = func() time.Time { return now }

	makeMsg := func(symbol string, seqId int64) []byte {
		msg := OKXTickerMessage{
			Arg: OKXTickerArg{Channel: "tickers", InstID: symbol},
			Data: []OKXTickerData{
				{
					InstID: symbol,
					Last:   "100.00",
					BidPx:  "99.90",
					AskPx:  "100.10",
					BidSz:  "10",
					AskSz:  "10",
					Vol24h: "50000",
					Ts:     fmt.Sprintf("%d", now.UnixMilli()),
					SeqId:  seqId,
				},
			},
		}
		data, _ := json.Marshal(msg)
		return data
	}

	// Different symbols should have independent sequence tracking.
	_, err := parser.ParseAndValidate(makeMsg("BTC-USDT", 100))
	if err != nil {
		t.Fatalf("BTC seqId=100 should pass: %v", err)
	}

	_, err = parser.ParseAndValidate(makeMsg("ETH-USDT", 50))
	if err != nil {
		t.Fatalf("ETH seqId=50 should pass (different symbol): %v", err)
	}

	// BTC with lower seqId should fail.
	_, err = parser.ParseAndValidate(makeMsg("BTC-USDT", 99))
	if err == nil {
		t.Fatal("BTC seqId=99 should fail (lower than 100)")
	}

	// ETH with higher seqId should pass.
	_, err = parser.ParseAndValidate(makeMsg("ETH-USDT", 51))
	if err != nil {
		t.Fatalf("ETH seqId=51 should pass: %v", err)
	}
}

func TestParseAndValidate_FailureCounter(t *testing.T) {
	now := time.Now()
	parser := NewParser(func(symbol, reason string) {})
	parser.timeNow = func() time.Time { return now }

	// Generate multiple failures.
	for i := 0; i < 5; i++ {
		parser.ParseAndValidate([]byte("bad json"))
	}

	if parser.GetValidationFailureCount() != 5 {
		t.Errorf("expected 5 failures, got %d", parser.GetValidationFailureCount())
	}

	parser.ResetValidationFailureCount()
	if parser.GetValidationFailureCount() != 0 {
		t.Errorf("expected 0 after reset, got %d", parser.GetValidationFailureCount())
	}
}

func TestParseAndValidate_DataInvalidCallbackCalled(t *testing.T) {
	now := time.Now()
	var callbackSymbol, callbackReason string
	parser := NewParser(func(symbol, reason string) {
		callbackSymbol = symbol
		callbackReason = reason
	})
	parser.timeNow = func() time.Time { return now }

	msg := OKXTickerMessage{
		Arg: OKXTickerArg{Channel: "tickers", InstID: "XRP-USDT"},
		Data: []OKXTickerData{
			{
				InstID: "XRP-USDT",
				Last:   "0",
				BidPx:  "0.50",
				AskPx:  "0.51",
				BidSz:  "100",
				AskSz:  "100",
				Vol24h: "10000",
				Ts:     fmt.Sprintf("%d", now.UnixMilli()),
				SeqId:  1,
			},
		},
	}
	data, _ := json.Marshal(msg)

	_, err := parser.ParseAndValidate(data)
	if err == nil {
		t.Fatal("expected error for zero lastPrice")
	}
	if callbackSymbol != "XRP-USDT" {
		t.Errorf("expected callback symbol XRP-USDT, got %s", callbackSymbol)
	}
	if callbackReason == "" {
		t.Error("expected non-empty callback reason")
	}
}

func TestParseTickDirect_ValidTick(t *testing.T) {
	now := time.Now()
	parser := NewParser(nil)
	parser.timeNow = func() time.Time { return now }

	tick := &models.TickData{
		Symbol:     "BTC-USDT",
		Timestamp:  now.UnixMicro(),
		LastPrice:  decimal.NewFromFloat(50000.0),
		BestBid:    decimal.NewFromFloat(49999.0),
		BestAsk:    decimal.NewFromFloat(50001.0),
		BidSize:    decimal.NewFromFloat(1.0),
		AskSize:    decimal.NewFromFloat(1.0),
		Volume24h:  decimal.NewFromFloat(10000.0),
		SequenceId: 1,
	}

	err := parser.ParseTickDirect(tick)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestParseAndValidate_EmptyData(t *testing.T) {
	parser := NewParser(func(symbol, reason string) {})

	msg := OKXTickerMessage{
		Arg:  OKXTickerArg{Channel: "tickers", InstID: "BTC-USDT"},
		Data: []OKXTickerData{},
	}
	data, _ := json.Marshal(msg)

	_, err := parser.ParseAndValidate(data)
	if err == nil {
		t.Fatal("expected error for empty data array")
	}
}

func TestResetSequenceId(t *testing.T) {
	now := time.Now()
	parser := NewParser(func(symbol, reason string) {})
	parser.timeNow = func() time.Time { return now }

	makeMsg := func(symbol string, seqId int64) []byte {
		msg := OKXTickerMessage{
			Arg: OKXTickerArg{Channel: "tickers", InstID: symbol},
			Data: []OKXTickerData{
				{
					InstID: symbol,
					Last:   "100.00",
					BidPx:  "99.90",
					AskPx:  "100.10",
					BidSz:  "10",
					AskSz:  "10",
					Vol24h: "50000",
					Ts:     fmt.Sprintf("%d", now.UnixMilli()),
					SeqId:  seqId,
				},
			},
		}
		data, _ := json.Marshal(msg)
		return data
	}

	_, err := parser.ParseAndValidate(makeMsg("BTC-USDT", 100))
	if err != nil {
		t.Fatalf("initial message should pass: %v", err)
	}

	// After reset, lower seqId should pass.
	parser.ResetSequenceId("BTC-USDT")
	_, err = parser.ParseAndValidate(makeMsg("BTC-USDT", 1))
	if err != nil {
		t.Fatalf("after reset, lower seqId should pass: %v", err)
	}
}
