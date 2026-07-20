package execution

import (
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

type SwapSide int

const (
	SwapFlat SwapSide = iota
	SwapLong
	SwapShort
)

func (s SwapSide) String() string {
	switch s {
	case SwapLong:
		return "long"
	case SwapShort:
		return "short"
	default:
		return "flat"
	}
}

type SwapPositionEntry struct {
	Side      SwapSide
	Entry     decimal.Decimal
	Contracts int
	Leverage  float64
	OpenedAt  time.Time
}

type SwapPositionManager struct {
	mu       sync.RWMutex
	pos      *SwapPositionEntry
	feeFloor decimal.Decimal
}

func NewSwapPositionManager() *SwapPositionManager {
	return &SwapPositionManager{feeFloor: decimal.NewFromFloat(0.0005)}
}

func (m *SwapPositionManager) Open(side SwapSide, entry decimal.Decimal, contracts int, leverage float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pos = &SwapPositionEntry{Side: side, Entry: entry, Contracts: contracts, Leverage: leverage, OpenedAt: time.Now()}
}

func (m *SwapPositionManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pos = nil
}

func (m *SwapPositionManager) Get() *SwapPositionEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.pos == nil {
		return nil
	}
	cp := *m.pos
	return &cp
}

func (m *SwapPositionManager) HasPosition() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pos != nil
}

func (m *SwapPositionManager) CloseTarget(spread decimal.Decimal, elapsed time.Duration, mark decimal.Decimal) (decimal.Decimal, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.pos == nil {
		return decimal.Zero, false
	}
	margin := m.decayedMarginLocked(spread, elapsed)
	underwater := false
	if m.pos.Side == SwapLong && mark.LessThan(m.pos.Entry) {
		underwater = true
	}
	if m.pos.Side == SwapShort && mark.GreaterThan(m.pos.Entry) {
		underwater = true
	}
	if underwater && margin.GreaterThan(m.feeFloor) {
		margin = m.feeFloor
	}
	one := decimal.NewFromInt(1)
	if m.pos.Side == SwapLong {
		return m.pos.Entry.Mul(one.Add(margin)), true
	}
	return m.pos.Entry.Mul(one.Sub(margin)), true
}

func (m *SwapPositionManager) decayedMarginLocked(spread decimal.Decimal, elapsed time.Duration) decimal.Decimal {
	var margin decimal.Decimal
	switch {
	case elapsed < 1*time.Hour:
		margin = spread
	case elapsed < 6*time.Hour:
		margin = spread.Mul(decimal.NewFromFloat(0.8))
	default:
		margin = spread.Mul(decimal.NewFromFloat(0.67))
	}
	if margin.LessThan(m.feeFloor) {
		margin = m.feeFloor
	}
	return margin
}

func (m *SwapPositionManager) ShouldHardStop(mark, stopPct decimal.Decimal) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.pos == nil || !mark.IsPositive() || !m.pos.Entry.IsPositive() {
		return false
	}
	var lossFrac decimal.Decimal
	if m.pos.Side == SwapLong {
		lossFrac = m.pos.Entry.Sub(mark).Div(m.pos.Entry)
	} else {
		lossFrac = mark.Sub(m.pos.Entry).Div(m.pos.Entry)
	}
	return lossFrac.GreaterThanOrEqual(stopPct)
}

func (m *SwapPositionManager) ShouldForceClose(maxHold time.Duration) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.pos == nil {
		return false
	}
	return time.Since(m.pos.OpenedAt) > maxHold
}
