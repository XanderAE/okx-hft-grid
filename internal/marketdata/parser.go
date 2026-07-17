package marketdata

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// maxPrice is the upper bound for price validation: 99,999,999.99
var maxPrice = decimal.NewFromFloat(99999999.99)

// timestampToleranceSec is the maximum allowed difference between tick timestamp
// and server time (±5 seconds).
const timestampToleranceSec = 5

// ValidationError describes why a tick data failed validation.
type ValidationError struct {
	Symbol string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed for %s: %s", e.Symbol, e.Reason)
}

// OKXTickerMessage represents the raw JSON structure from OKX ticker channel.
type OKXTickerMessage struct {
	Arg  OKXTickerArg    `json:"arg"`
	Data []OKXTickerData `json:"data"`
}

// OKXTickerArg represents the channel/instId subscription info.
type OKXTickerArg struct {
	Channel string `json:"channel"`
	InstID  string `json:"instId"`
}

// OKXTickerData represents a single ticker data entry from OKX.
type OKXTickerData struct {
	InstID    string `json:"instId"`
	Last      string `json:"last"`
	BidPx     string `json:"bidPx"`
	AskPx     string `json:"askPx"`
	BidSz     string `json:"bidSz"`
	AskSz     string `json:"askSz"`
	Vol24h    string `json:"vol24h"`
	Ts        string `json:"ts"`
	SeqId     int64  `json:"seqId"`
}

// DataInvalidCallback is a function called when tick data fails validation.
type DataInvalidCallback func(symbol string, reason string)

// Parser handles parsing and validation of OKX market data messages.
type Parser struct {
	// lastSequenceIds tracks the last seen sequenceId per symbol for ordering checks.
	lastSequenceIds sync.Map // map[string]int64

	// validationFailures counts the total number of validation failures.
	validationFailures atomic.Int64

	// onDataInvalid is called when validation fails (emits DATA_INVALID event).
	onDataInvalid DataInvalidCallback

	// timeNow is a function returning the current time (injectable for testing).
	timeNow func() time.Time
}

// NewParser creates a new Parser with default settings.
func NewParser(onDataInvalid DataInvalidCallback) *Parser {
	return &Parser{
		onDataInvalid: onDataInvalid,
		timeNow:       time.Now,
	}
}

// GetValidationFailureCount returns the current number of validation failures.
func (p *Parser) GetValidationFailureCount() int64 {
	return p.validationFailures.Load()
}

// ResetValidationFailureCount resets the failure counter to zero.
func (p *Parser) ResetValidationFailureCount() {
	p.validationFailures.Store(0)
}

// ParseAndValidate parses raw OKX JSON message bytes into validated TickData.
// Returns nil and an error if parsing or validation fails.
// On validation failure, emits a DATA_INVALID event and increments the failure counter.
func (p *Parser) ParseAndValidate(data []byte) (*models.TickData, error) {
	var msg OKXTickerMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		p.recordFailure("", "json parse error: "+err.Error())
		return nil, fmt.Errorf("json parse error: %w", err)
	}

	if len(msg.Data) == 0 {
		p.recordFailure("", "empty data array")
		return nil, &ValidationError{Symbol: "", Reason: "empty data array"}
	}

	// Process the first data entry (OKX sends one ticker per message typically).
	raw := msg.Data[0]
	symbol := raw.InstID
	if symbol == "" {
		symbol = msg.Arg.InstID
	}

	// Parse decimal fields.
	lastPrice, err := decimal.NewFromString(raw.Last)
	if err != nil {
		p.recordFailure(symbol, "invalid lastPrice format")
		return nil, &ValidationError{Symbol: symbol, Reason: "invalid lastPrice format"}
	}

	bestBid, err := decimal.NewFromString(raw.BidPx)
	if err != nil {
		p.recordFailure(symbol, "invalid bestBid format")
		return nil, &ValidationError{Symbol: symbol, Reason: "invalid bestBid format"}
	}

	bestAsk, err := decimal.NewFromString(raw.AskPx)
	if err != nil {
		p.recordFailure(symbol, "invalid bestAsk format")
		return nil, &ValidationError{Symbol: symbol, Reason: "invalid bestAsk format"}
	}

	bidSize, err := decimal.NewFromString(raw.BidSz)
	if err != nil {
		p.recordFailure(symbol, "invalid bidSize format")
		return nil, &ValidationError{Symbol: symbol, Reason: "invalid bidSize format"}
	}

	askSize, err := decimal.NewFromString(raw.AskSz)
	if err != nil {
		p.recordFailure(symbol, "invalid askSize format")
		return nil, &ValidationError{Symbol: symbol, Reason: "invalid askSize format"}
	}

	vol24h, err := decimal.NewFromString(raw.Vol24h)
	if err != nil {
		// Volume is not critical for validation, default to zero.
		vol24h = decimal.Zero
	}

	// Parse timestamp (OKX sends milliseconds as a string).
	var tsMs int64
	if _, err := fmt.Sscanf(raw.Ts, "%d", &tsMs); err != nil {
		p.recordFailure(symbol, "invalid timestamp format")
		return nil, &ValidationError{Symbol: symbol, Reason: "invalid timestamp format"}
	}

	// Convert ms to microseconds for TickData.Timestamp.
	tsMicro := tsMs * 1000

	// Build TickData.
	tick := &models.TickData{
		Symbol:     symbol,
		Timestamp:  tsMicro,
		LastPrice:  lastPrice,
		BestBid:    bestBid,
		BestAsk:    bestAsk,
		BidSize:    bidSize,
		AskSize:    askSize,
		Volume24h:  vol24h,
		SequenceId: raw.SeqId,
	}

	// Run validation.
	if err := p.validate(tick); err != nil {
		return nil, err
	}

	return tick, nil
}

// ParseTickDirect parses a pre-constructed TickData (e.g., from tests or internal use)
// and applies validation only. Useful when the raw JSON parsing is already done.
func (p *Parser) ParseTickDirect(tick *models.TickData) error {
	return p.validate(tick)
}

// validate applies all validation rules to a TickData.
func (p *Parser) validate(tick *models.TickData) error {
	// Rule 1: lastPrice > 0 and <= 99,999,999.99
	if !tick.LastPrice.IsPositive() || tick.LastPrice.GreaterThan(maxPrice) {
		reason := fmt.Sprintf("lastPrice out of range: %s", tick.LastPrice.String())
		p.recordFailure(tick.Symbol, reason)
		return &ValidationError{Symbol: tick.Symbol, Reason: reason}
	}

	// Rule 2: bestBid > 0 and <= 99,999,999.99
	if !tick.BestBid.IsPositive() || tick.BestBid.GreaterThan(maxPrice) {
		reason := fmt.Sprintf("bestBid out of range: %s", tick.BestBid.String())
		p.recordFailure(tick.Symbol, reason)
		return &ValidationError{Symbol: tick.Symbol, Reason: reason}
	}

	// Rule 3: bestAsk > 0 and <= 99,999,999.99
	if !tick.BestAsk.IsPositive() || tick.BestAsk.GreaterThan(maxPrice) {
		reason := fmt.Sprintf("bestAsk out of range: %s", tick.BestAsk.String())
		p.recordFailure(tick.Symbol, reason)
		return &ValidationError{Symbol: tick.Symbol, Reason: reason}
	}

	// Rule 4: bestBid < bestAsk (no crossed book)
	if tick.BestBid.GreaterThanOrEqual(tick.BestAsk) {
		reason := fmt.Sprintf("crossed book: bestBid=%s >= bestAsk=%s", tick.BestBid.String(), tick.BestAsk.String())
		p.recordFailure(tick.Symbol, reason)
		return &ValidationError{Symbol: tick.Symbol, Reason: reason}
	}

	// Rule 5: timestamp within ±5 seconds of server time
	now := p.timeNow()
	tickTime := time.UnixMicro(tick.Timestamp)
	diff := now.Sub(tickTime)
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Duration(timestampToleranceSec)*time.Second {
		reason := fmt.Sprintf("timestamp out of range: diff=%s (tick=%d, now=%d)",
			diff.String(), tick.Timestamp, now.UnixMicro())
		p.recordFailure(tick.Symbol, reason)
		return &ValidationError{Symbol: tick.Symbol, Reason: reason}
	}

	// Rule 6: sequenceId must be strictly greater than previous for this symbol
	if tick.Symbol != "" {
		prevRaw, loaded := p.lastSequenceIds.Load(tick.Symbol)
		if loaded {
			prev := prevRaw.(int64)
			if tick.SequenceId <= prev {
				reason := fmt.Sprintf("sequence out of order: current=%d, previous=%d", tick.SequenceId, prev)
				p.recordFailure(tick.Symbol, reason)
				return &ValidationError{Symbol: tick.Symbol, Reason: reason}
			}
		}
		p.lastSequenceIds.Store(tick.Symbol, tick.SequenceId)
	}

	return nil
}

// recordFailure increments the failure counter and invokes the DATA_INVALID callback.
func (p *Parser) recordFailure(symbol, reason string) {
	p.validationFailures.Add(1)
	if p.onDataInvalid != nil {
		p.onDataInvalid(symbol, reason)
	}
}

// ResetSequenceId removes the stored sequence ID for a symbol (e.g., on reconnection).
func (p *Parser) ResetSequenceId(symbol string) {
	p.lastSequenceIds.Delete(symbol)
}

// ResetAllSequenceIds clears all stored sequence IDs.
func (p *Parser) ResetAllSequenceIds() {
	p.lastSequenceIds.Range(func(key, value interface{}) bool {
		p.lastSequenceIds.Delete(key)
		return true
	})
}
