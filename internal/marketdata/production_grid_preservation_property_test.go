package marketdata

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirements 3.6**
//
// PRE-06 records exact valid ticker parsing and synchronous dispatch for
// DOGE/WIF, while keeping the existing five-second staleness rejection.
func TestProperty2_Preservation_PRE06_PublicTickerParseDispatchAndStaleness(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		symbols := []string{"DOGE-USDT", "WIF-USDT"}
		symbol := symbols[rapid.IntRange(0, len(symbols)-1).Draw(t, "symbol_index")]
		now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
		timestampOffset := rapid.IntRange(-4, 4).Draw(t, "fresh_timestamp_offset_seconds")
		sequence := int64(rapid.IntRange(1, 1_000_000).Draw(t, "sequence"))
		lastUnits := int64(rapid.IntRange(100_000, 5_000_000).Draw(t, "last_microunits"))
		spreadUnits := int64(rapid.IntRange(1, 1000).Draw(t, "spread_microunits"))
		scale := decimal.NewFromInt(1_000_000)
		last := decimal.NewFromInt(lastUnits).Div(scale)
		bid := decimal.NewFromInt(lastUnits - spreadUnits).Div(scale)
		ask := decimal.NewFromInt(lastUnits + spreadUnits).Div(scale)
		bidSize := decimal.NewFromInt(int64(rapid.IntRange(1, 100_000).Draw(t, "bid_size")))
		askSize := decimal.NewFromInt(int64(rapid.IntRange(1, 100_000).Draw(t, "ask_size")))
		volume := decimal.NewFromInt(int64(rapid.IntRange(1, 10_000_000).Draw(t, "volume")))
		freshTime := now.Add(time.Duration(timestampOffset) * time.Second)

		message := OKXTickerMessage{
			Arg: OKXTickerArg{Channel: "tickers", InstID: symbol},
			Data: []OKXTickerData{{
				InstID: symbol, Last: last.String(), BidPx: bid.String(), AskPx: ask.String(),
				BidSz: bidSize.String(), AskSz: askSize.String(), Vol24h: volume.String(),
				Ts: fmt.Sprintf("%d", freshTime.UnixMilli()), SeqId: sequence,
			}},
		}
		raw, err := json.Marshal(message)
		if err != nil {
			t.Fatalf("PRE-06 fixture marshal: %v", err)
		}
		var invalidCalls int
		parser := NewParser(func(gotSymbol, reason string) { invalidCalls++ })
		parser.timeNow = func() time.Time { return now }
		tick, err := parser.ParseAndValidate(raw)
		if err != nil {
			t.Fatalf("PRE-06 valid ticker rejected: %v", err)
		}
		if invalidCalls != 0 || parser.GetValidationFailureCount() != 0 {
			t.Fatalf("PRE-06 valid ticker emitted invalid signal: calls=%d failures=%d", invalidCalls, parser.GetValidationFailureCount())
		}
		if tick.Symbol != symbol || tick.Timestamp != freshTime.UnixMicro() || tick.SequenceId != sequence ||
			!tick.LastPrice.Equal(last) || !tick.BestBid.Equal(bid) || !tick.BestAsk.Equal(ask) ||
			!tick.BidSize.Equal(bidSize) || !tick.AskSize.Equal(askSize) || !tick.Volume24h.Equal(volume) {
			t.Fatalf("PRE-06 parsed ticker changed: got=%+v", tick)
		}

		dispatcher := NewDispatcher(DefaultDispatcherConfig())
		var received []models.MarketEvent
		dispatcher.RegisterMarketHandler(models.EventMarketData, func(event models.MarketEvent) {
			received = append(received, event)
		})
		event := models.MarketEvent{
			Symbol: symbol, Timestamp: time.UnixMicro(tick.Timestamp), LastPrice: tick.LastPrice,
			BestBid: tick.BestBid, BestAsk: tick.BestAsk, BidSize: tick.BidSize,
			AskSize: tick.AskSize, Volume24h: tick.Volume24h, SeqID: tick.SequenceId,
		}
		dispatcher.DispatchMarketEventSync(models.EventMarketData, event)
		if len(received) != 1 {
			t.Fatalf("PRE-06 dispatch count=%d, want 1", len(received))
		}
		got := received[0]
		if got.Symbol != symbol || got.SeqID != sequence || !got.Timestamp.Equal(freshTime) ||
			!got.LastPrice.Equal(last) || !got.BestBid.Equal(bid) || !got.BestAsk.Equal(ask) ||
			!got.BidSize.Equal(bidSize) || !got.AskSize.Equal(askSize) || !got.Volume24h.Equal(volume) {
			t.Fatalf("PRE-06 dispatched ticker changed: got=%+v", got)
		}

		staleMessage := message
		staleMessage.Data = append([]OKXTickerData(nil), message.Data...)
		staleMessage.Data[0].Ts = fmt.Sprintf("%d", now.Add(-6*time.Second).UnixMilli())
		staleRaw, err := json.Marshal(staleMessage)
		if err != nil {
			t.Fatalf("PRE-06 stale fixture marshal: %v", err)
		}
		var staleSymbol string
		staleParser := NewParser(func(gotSymbol, reason string) { staleSymbol = gotSymbol })
		staleParser.timeNow = func() time.Time { return now }
		if staleTick, staleErr := staleParser.ParseAndValidate(staleRaw); staleErr == nil || staleTick != nil {
			t.Fatalf("PRE-06 stale ticker accepted: tick=%+v err=%v", staleTick, staleErr)
		}
		if staleSymbol != symbol || staleParser.GetValidationFailureCount() != 1 {
			t.Fatalf("PRE-06 stale evidence changed: symbol=%s failures=%d", staleSymbol, staleParser.GetValidationFailureCount())
		}
	})
}
