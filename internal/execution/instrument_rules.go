package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

const (
	// DefaultInstrumentRulesTTL is the time-to-live for cached instrument rules.
	DefaultInstrumentRulesTTL = 5 * time.Minute

	// DefaultInstrumentRulesHardTTL is the hard maximum age beyond which rules
	// are considered expired and orders are blocked.
	DefaultInstrumentRulesHardTTL = 15 * time.Minute
)

// InstrumentRulesProvider fetches, caches and refreshes OKX instrument
// metadata. Expired rules block new orders until refreshed.
type InstrumentRulesProvider interface {
	// Current returns the cached rules for symbol if still valid.
	Current(ctx context.Context, symbol string) (models.InstrumentRules, error)

	// Refresh fetches fresh rules from the gateway and updates the cache.
	Refresh(ctx context.Context, symbol string) (models.InstrumentRules, error)
}

// InstrumentRulesCache implements InstrumentRulesProvider with an in-memory
// cache and a gateway dependency for fetching fresh data.
type InstrumentRulesCache struct {
	gateway ExchangeGateway
	clock   func() time.Time
	ttl     time.Duration
	hardTTL time.Duration

	mu    sync.RWMutex
	cache map[string]models.InstrumentRules
}

// InstrumentRulesCacheOption configures the rules cache.
type InstrumentRulesCacheOption func(*InstrumentRulesCache)

// WithInstrumentRulesTTL sets the soft TTL for cached rules.
func WithInstrumentRulesTTL(ttl time.Duration) InstrumentRulesCacheOption {
	return func(c *InstrumentRulesCache) { c.ttl = ttl }
}

// WithInstrumentRulesHardTTL sets the hard expiry for cached rules.
func WithInstrumentRulesHardTTL(ttl time.Duration) InstrumentRulesCacheOption {
	return func(c *InstrumentRulesCache) { c.hardTTL = ttl }
}

// WithInstrumentRulesClock injects a fake clock for testing.
func WithInstrumentRulesClock(clock func() time.Time) InstrumentRulesCacheOption {
	return func(c *InstrumentRulesCache) { c.clock = clock }
}

// NewInstrumentRulesCache creates a new provider backed by the given gateway.
func NewInstrumentRulesCache(gateway ExchangeGateway, opts ...InstrumentRulesCacheOption) *InstrumentRulesCache {
	c := &InstrumentRulesCache{
		gateway: gateway,
		clock:   time.Now,
		ttl:     DefaultInstrumentRulesTTL,
		hardTTL: DefaultInstrumentRulesHardTTL,
		cache:   make(map[string]models.InstrumentRules),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Current returns cached rules if not expired. Returns ErrInstrumentRulesExpired
// if the hard TTL has been exceeded.
func (c *InstrumentRulesCache) Current(ctx context.Context, symbol string) (models.InstrumentRules, error) {
	c.mu.RLock()
	rules, ok := c.cache[symbol]
	c.mu.RUnlock()

	if !ok {
		return c.Refresh(ctx, symbol)
	}

	now := c.clock()
	if err := rules.Valid(now); err != nil {
		return c.Refresh(ctx, symbol)
	}

	return rules, nil
}

// Refresh fetches fresh rules from the exchange and updates the cache.
func (c *InstrumentRulesCache) Refresh(ctx context.Context, symbol string) (models.InstrumentRules, error) {
	rules, err := c.gateway.GetInstrumentRules(ctx, symbol)
	if err != nil {
		return models.InstrumentRules{}, fmt.Errorf("instrument rules refresh failed for %s: %w", symbol, err)
	}

	now := c.clock()
	rules.FetchedAt = now
	rules.ExpiresAt = now.Add(c.hardTTL)

	if err := rules.Valid(now); err != nil {
		return models.InstrumentRules{}, fmt.Errorf("fetched instrument rules invalid for %s: %w", symbol, err)
	}

	c.mu.Lock()
	c.cache[symbol] = rules
	c.mu.Unlock()

	return rules, nil
}

// NormalizedOrderRequest is an order request that has passed instrument
// rule validation and decimal normalization.
type NormalizedOrderRequest struct {
	Symbol    string
	Side      models.Side
	OrderType models.OrderType
	Price     decimal.Decimal
	Quantity  decimal.Decimal
	ClOrdID   string // Deterministic client order ID from caller
	Purpose   string // counter-sell, initial, replacement, etc.
}

// NormalizationResult describes the outcome of order normalization.
type NormalizationResult struct {
	Normalized *NormalizedOrderRequest
	NoSend     bool
	Reason     string
}

// NormalizeOrder applies instrument rules to an order candidate:
// - BUY price: floor to tickSz multiple
// - SELL price: ceil to tickSz multiple
// - Quantity: floor to lotSz multiple
// Then re-validates minSz, minNotional, and ensures the price/qty are exact
// multiples. Returns a no-send result if the normalized order cannot satisfy
// all checks.
func NormalizeOrder(candidate NormalizedOrderRequest, rules models.InstrumentRules, now time.Time) NormalizationResult {
	if err := rules.Valid(now); err != nil {
		return NormalizationResult{NoSend: true, Reason: fmt.Sprintf("instrument rules invalid: %v", err)}
	}

	if candidate.Symbol != rules.Symbol {
		return NormalizationResult{NoSend: true, Reason: fmt.Sprintf("symbol mismatch: order=%s rules=%s", candidate.Symbol, rules.Symbol)}
	}

	// Normalize price based on side
	var price decimal.Decimal
	switch candidate.Side {
	case models.SideBuy:
		price = models.FloorToMultiple(candidate.Price, rules.TickSize)
	case models.SideSell:
		price = models.CeilToMultiple(candidate.Price, rules.TickSize)
	default:
		return NormalizationResult{NoSend: true, Reason: "invalid order side"}
	}

	// Normalize quantity: always floor to lotSz
	qty := models.FloorToMultiple(candidate.Quantity, rules.LotSize)

	// Validate price is exact multiple
	if !models.IsExactMultiple(price, rules.TickSize) {
		return NormalizationResult{NoSend: true, Reason: "price is not exact tickSz multiple after normalization"}
	}

	// Validate quantity is exact multiple
	if !models.IsExactMultiple(qty, rules.LotSize) {
		return NormalizationResult{NoSend: true, Reason: "quantity is not exact lotSz multiple after normalization"}
	}

	// Check minimum quantity
	if qty.LessThan(rules.MinSize) {
		return NormalizationResult{NoSend: true, Reason: fmt.Sprintf("quantity %s below minSz %s", qty, rules.MinSize)}
	}

	// Check positive price
	if !price.IsPositive() {
		return NormalizationResult{NoSend: true, Reason: "normalized price is not positive"}
	}

	// Check positive quantity
	if !qty.IsPositive() {
		return NormalizationResult{NoSend: true, Reason: "normalized quantity is not positive"}
	}

	// Check minimum notional if applicable
	if rules.MinNotional.IsPositive() {
		notional := price.Mul(qty)
		if notional.LessThan(rules.MinNotional) {
			return NormalizationResult{NoSend: true, Reason: fmt.Sprintf("notional %s below minNotional %s", notional, rules.MinNotional)}
		}
	}

	normalized := candidate
	normalized.Price = price
	normalized.Quantity = qty
	return NormalizationResult{Normalized: &normalized}
}

// OKXInstrumentData represents one item from the /api/v5/public/instruments response.
type OKXInstrumentData struct {
	InstID      string `json:"instId"`
	TickSz      string `json:"tickSz"`
	LotSz       string `json:"lotSz"`
	MinSz       string `json:"minSz"`
	InstType    string `json:"instType"`
	State       string `json:"state"`
}

// ParseInstrumentRules converts raw OKX instrument data into validated rules.
func ParseInstrumentRules(data OKXInstrumentData, now time.Time, hardTTL time.Duration) (models.InstrumentRules, error) {
	tickSz, err := decimal.NewFromString(data.TickSz)
	if err != nil || !tickSz.IsPositive() {
		return models.InstrumentRules{}, fmt.Errorf("invalid tickSz %q for %s", data.TickSz, data.InstID)
	}

	lotSz, err := decimal.NewFromString(data.LotSz)
	if err != nil || !lotSz.IsPositive() {
		return models.InstrumentRules{}, fmt.Errorf("invalid lotSz %q for %s", data.LotSz, data.InstID)
	}

	minSz, err := decimal.NewFromString(data.MinSz)
	if err != nil || !minSz.IsPositive() {
		return models.InstrumentRules{}, fmt.Errorf("invalid minSz %q for %s", data.MinSz, data.InstID)
	}

	return models.InstrumentRules{
		Symbol:      data.InstID,
		TickSize:    tickSz,
		LotSize:     lotSz,
		MinSize:     minSz,
		MinNotional: decimal.Zero, // OKX does not always provide this; set to zero when absent
		FetchedAt:   now,
		ExpiresAt:   now.Add(hardTTL),
	}, nil
}

// parseInstrumentsResponse parses the full OKX /api/v5/public/instruments response.
func parseInstrumentsResponse(body io.Reader) ([]OKXInstrumentData, error) {
	var resp struct {
		Code string              `json:"code"`
		Msg  string              `json:"msg"`
		Data []OKXInstrumentData `json:"data"`
	}
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("failed to decode instruments response: %w", err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("instruments API error: code=%s msg=%s", resp.Code, resp.Msg)
	}
	return resp.Data, nil
}
