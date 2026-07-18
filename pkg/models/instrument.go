package models

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrInstrumentRulesExpired = errors.New("models: instrument rules have expired")
	ErrInstrumentRulesInvalid = errors.New("models: instrument rules are invalid")
)

// InstrumentRules represents the OKX instrument metadata required for
// base-10 decimal order normalization. All values are stored as decimal
// strings to avoid binary float precision errors.
type InstrumentRules struct {
	Symbol      string          `json:"symbol"`
	TickSize    decimal.Decimal `json:"tickSz"`      // price step
	LotSize     decimal.Decimal `json:"lotSz"`       // quantity step
	MinSize     decimal.Decimal `json:"minSz"`       // minimum order quantity
	MinNotional decimal.Decimal `json:"minNotional"` // minimum price*qty (zero if not applicable)
	FetchedAt   time.Time       `json:"fetchedAt"`
	ExpiresAt   time.Time       `json:"expiresAt"`
}

// Valid checks that all required fields are positive and the rules have not
// expired. The caller provides "now" to support fake-clock injection.
func (r InstrumentRules) Valid(now time.Time) error {
	if r.Symbol == "" {
		return ErrInstrumentRulesInvalid
	}
	if !r.TickSize.IsPositive() || !r.LotSize.IsPositive() || !r.MinSize.IsPositive() {
		return ErrInstrumentRulesInvalid
	}
	if now.After(r.ExpiresAt) {
		return ErrInstrumentRulesExpired
	}
	return nil
}

// FloorToMultiple returns the largest value <= v that is an exact integer
// multiple of step. Used for BUY price and quantity normalization.
func FloorToMultiple(v, step decimal.Decimal) decimal.Decimal {
	if !step.IsPositive() {
		return v
	}
	return v.Div(step).Floor().Mul(step)
}

// CeilToMultiple returns the smallest value >= v that is an exact integer
// multiple of step. Used for SELL price normalization.
func CeilToMultiple(v, step decimal.Decimal) decimal.Decimal {
	if !step.IsPositive() {
		return v
	}
	return v.Div(step).Ceil().Mul(step)
}

// IsExactMultiple checks whether v is an exact integer multiple of step.
func IsExactMultiple(v, step decimal.Decimal) bool {
	if !step.IsPositive() {
		return false
	}
	return v.Mod(step).IsZero()
}
