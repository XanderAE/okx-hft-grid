package risk

import (
	"fmt"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// OperationRiskClass classifies an operation's risk profile for gate decisions.
type OperationRiskClass int

const (
	// RiskIncreasing covers spot BUY, counter BUY after SELL fill, initial/replacement BUY,
	// or any operation that cannot prove it reduces known exposure.
	RiskIncreasing OperationRiskClass = iota

	// RiskReducing covers SELL that is confirmed not to exceed known available spot position,
	// and confirmed cancellation of Bot_Owned BUY orders.
	RiskReducing

	// Reconciliation covers exchange queries, fill/order reconciliation, and status checks.
	Reconciliation

	// ConfirmedBuyCancellation covers cancel of Bot_Owned BUY orders that have been confirmed.
	ConfirmedBuyCancellation
)

// String returns a human-readable representation.
func (c OperationRiskClass) String() string {
	switch c {
	case RiskIncreasing:
		return "risk-increasing"
	case RiskReducing:
		return "risk-reducing"
	case Reconciliation:
		return "reconciliation"
	case ConfirmedBuyCancellation:
		return "confirmed-buy-cancellation"
	default:
		return "unknown"
	}
}

// GateDecision is the result of TradingGate.Authorize.
type GateDecision struct {
	Allowed     bool
	BlockReason string
	Scope       models.SafeStopScope
	ReasonCodes []string
}

// ReasonCode constants for typed Safe_Stop reasons.
const (
	ReasonCounterSellFailed  = "counter-sell-terminal-failure"
	ReasonInstrumentError    = "instrument-metadata-error"
	ReasonRebalanceTerminal  = "rebalance-terminal-failure"
	ReasonStaleTicker        = "stale-ticker"
	ReasonPersistenceFailure = "persistence-write-failure"
	ReasonPrivateWSUncertain = "private-ws-uncertain"
	ReasonAccountReconcile   = "account-reconciliation-uncertain"
	ReasonPortfolioRisk      = "portfolio-risk-uncertain"
	ReasonMigrationConflict  = "migration-conflict"
	ReasonUnknownEffect      = "unknown-exchange-effect"
)

// SymbolSafeStopState tracks Safe_Stop state for a single symbol.
type SymbolSafeStopState struct {
	Active        bool
	Reasons       map[string]SafeStopReason
	RecoveryEpoch uint64
}

// SafeStopReason is a single reason entry.
type SafeStopReason struct {
	Code          string
	Since         time.Time
	Details       string
	RequiresHuman bool // whether automatic recovery is prohibited
}

// SharedDependencyHealth tracks the health of shared dependencies.
type SharedDependencyHealth struct {
	PersistenceHealthy      bool
	PrivateWSHealthy        bool
	AccountReconcileHealthy bool
	PortfolioRiskHealthy    bool
}

// TradingGate implements the design's TradingGate interface:
//   - symbol-local failure defaults to rejecting only that symbol's Risk_Increasing
//   - shared dependency failure (persistence, Private_WS, account reconciliation,
//     portfolio risk) causes global rejection of all Risk_Increasing
//   - only confirmed BUY cancellation, reconciliation, and risk-reducing SELL
//     (not exceeding known position) are allowed while blocked
//   - composes with (does NOT replace) the existing EmergencyStopManager
type TradingGate struct {
	mu sync.RWMutex

	// Per-symbol Safe_Stop state
	symbolStates map[string]*SymbolSafeStopState

	// Global Safe_Stop reasons (shared dependency failures)
	globalReasons map[string]SafeStopReason

	// Global recovery epoch (incremented when global recovery succeeds)
	globalRecoveryEpoch uint64

	// Shared dependency health snapshot
	sharedHealth SharedDependencyHealth

	// Known positions per symbol - used to verify risk-reducing sells
	knownPositions map[string]decimal.Decimal

	// Composition with existing EmergencyStopManager
	emergencyStop *EmergencyStopManager

	// Clock for testing
	clock func() time.Time
}

// TradingGateOption configures the gate.
type TradingGateOption func(*TradingGate)

// WithTradingGateClock injects a fake clock.
func WithTradingGateClock(clock func() time.Time) TradingGateOption {
	return func(g *TradingGate) { g.clock = clock }
}

// WithEmergencyStop composes with the existing global emergency stop.
func WithEmergencyStop(esm *EmergencyStopManager) TradingGateOption {
	return func(g *TradingGate) { g.emergencyStop = esm }
}

// NewTradingGate creates a new TradingGate with optional configuration.
func NewTradingGate(opts ...TradingGateOption) *TradingGate {
	g := &TradingGate{
		symbolStates:   make(map[string]*SymbolSafeStopState),
		globalReasons:  make(map[string]SafeStopReason),
		knownPositions: make(map[string]decimal.Decimal),
		clock:          time.Now,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// Authorize checks whether the given operation is permitted for the symbol.
// It combines scoped health checks with the existing global emergency stop.
// Reconciliation and confirmed BUY cancellation are ALWAYS allowed regardless
// of any stop state (design requirement).
func (g *TradingGate) Authorize(symbol string, class OperationRiskClass) GateDecision {
	// Reconciliation and confirmed BUY cancellation are always allowed
	// (even during emergency stop / Safe_Stop) - this is the design requirement.
	switch class {
	case Reconciliation, ConfirmedBuyCancellation:
		return GateDecision{Allowed: true}
	}

	// Check existing emergency stop (composition, not replacement)
	if g.emergencyStop != nil && g.emergencyStop.IsEmergencyStopActive() {
		return GateDecision{
			Allowed:     false,
			BlockReason: "emergency stop active: " + g.emergencyStop.GetEmergencyStopReason(),
			Scope:       models.SafeStopGlobal,
			ReasonCodes: []string{"emergency-stop"},
		}
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	// Check global Safe_Stop (shared dependency failures)
	if len(g.globalReasons) > 0 {
		if class == RiskIncreasing {
			codes := make([]string, 0, len(g.globalReasons))
			for code := range g.globalReasons {
				codes = append(codes, code)
			}
			return GateDecision{
				Allowed:     false,
				BlockReason: fmt.Sprintf("global safe-stop: %d shared dependency failures", len(g.globalReasons)),
				Scope:       models.SafeStopGlobal,
				ReasonCodes: codes,
			}
		}
		// RiskReducing is allowed during global Safe_Stop
		// (confirmed risk-reducing SELL not exceeding known position)
		return GateDecision{Allowed: true}
	}

	// Check symbol-local Safe_Stop
	if symbol != "" {
		if state, ok := g.symbolStates[symbol]; ok && state.Active && len(state.Reasons) > 0 {
			if class == RiskIncreasing {
				codes := make([]string, 0, len(state.Reasons))
				for code := range state.Reasons {
					codes = append(codes, code)
				}
				return GateDecision{
					Allowed:     false,
					BlockReason: fmt.Sprintf("symbol safe-stop for %s: %d reasons", symbol, len(state.Reasons)),
					Scope:       models.SafeStopSymbol,
					ReasonCodes: codes,
				}
			}
			// RiskReducing is allowed during symbol Safe_Stop
			return GateDecision{Allowed: true}
		}
	}

	// Healthy path: pass through (existing risk checks apply separately)
	return GateDecision{Allowed: true}
}

// EnterSymbolSafeStop activates a Safe_Stop reason for one symbol.
// Only blocks that symbol's Risk_Increasing operations.
func (g *TradingGate) EnterSymbolSafeStop(symbol string, reasonCode string, details string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	state := g.getOrCreateSymbolState(symbol)
	state.Active = true
	state.Reasons[reasonCode] = SafeStopReason{
		Code:    reasonCode,
		Since:   g.clock(),
		Details: details,
	}
}

// EnterSymbolSafeStopRequireHuman activates a Safe_Stop reason that requires human
// confirmation to clear (unknown effect, migration conflict).
func (g *TradingGate) EnterSymbolSafeStopRequireHuman(symbol string, reasonCode string, details string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	state := g.getOrCreateSymbolState(symbol)
	state.Active = true
	state.Reasons[reasonCode] = SafeStopReason{
		Code:          reasonCode,
		Since:         g.clock(),
		Details:       details,
		RequiresHuman: true,
	}
}

// EnterGlobalSafeStop activates a global Safe_Stop reason (shared dependency failure).
// Blocks Risk_Increasing for ALL symbols.
func (g *TradingGate) EnterGlobalSafeStop(reasonCode string, details string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.globalReasons[reasonCode] = SafeStopReason{
		Code:    reasonCode,
		Since:   g.clock(),
		Details: details,
	}
}

// EnterGlobalSafeStopRequireHuman activates a global Safe_Stop requiring human confirmation.
func (g *TradingGate) EnterGlobalSafeStopRequireHuman(reasonCode string, details string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.globalReasons[reasonCode] = SafeStopReason{
		Code:          reasonCode,
		Since:         g.clock(),
		Details:       details,
		RequiresHuman: true,
	}
}

// ClearSymbolReason removes a specific reason from a symbol's Safe_Stop.
// Only succeeds if the reason does NOT require human confirmation, OR if
// humanConfirmed is true. Returns false if the reason requires human and
// humanConfirmed is false.
func (g *TradingGate) ClearSymbolReason(symbol string, reasonCode string, humanConfirmed bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	state, ok := g.symbolStates[symbol]
	if !ok {
		return true
	}

	reason, exists := state.Reasons[reasonCode]
	if !exists {
		return true
	}

	if reason.RequiresHuman && !humanConfirmed {
		return false
	}

	delete(state.Reasons, reasonCode)
	if len(state.Reasons) == 0 {
		state.Active = false
	}
	return true
}

// ClearGlobalReason removes a global Safe_Stop reason.
// Only succeeds if the reason does NOT require human confirmation, OR if
// humanConfirmed is true.
func (g *TradingGate) ClearGlobalReason(reasonCode string, humanConfirmed bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	reason, exists := g.globalReasons[reasonCode]
	if !exists {
		return true
	}

	if reason.RequiresHuman && !humanConfirmed {
		return false
	}

	delete(g.globalReasons, reasonCode)
	return true
}

// MarkReconciled records a successful reconciliation epoch for a symbol.
// This is part of the recovery condition: dependency health + reconciliation epoch.
func (g *TradingGate) MarkReconciled(symbol string, epoch uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	state := g.getOrCreateSymbolState(symbol)
	state.RecoveryEpoch = epoch
}

// MarkGlobalReconciled updates the global recovery epoch.
func (g *TradingGate) MarkGlobalReconciled(epoch uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.globalRecoveryEpoch = epoch
}

// RecoveryEpoch returns the current reconciliation epoch for a symbol.
func (g *TradingGate) RecoveryEpoch(symbol string) uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if state, ok := g.symbolStates[symbol]; ok {
		return state.RecoveryEpoch
	}
	return 0
}

// GlobalRecoveryEpoch returns the global recovery epoch.
func (g *TradingGate) GlobalRecoveryEpoch() uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.globalRecoveryEpoch
}

// TryRecoverSymbol attempts automatic recovery for a symbol. It succeeds only if:
// 1. No remaining reasons require human confirmation
// 2. The provided epoch is >= the symbol's recovery epoch (proving fresh reconciliation)
// 3. The shared dependencies are all healthy
// Returns false with reason if recovery cannot proceed automatically.
func (g *TradingGate) TryRecoverSymbol(symbol string, reconcileEpoch uint64) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Global must be clean first
	if len(g.globalReasons) > 0 {
		return false, "global safe-stop still active"
	}

	state, ok := g.symbolStates[symbol]
	if !ok || !state.Active {
		return true, "" // already healthy
	}

	// Check all remaining reasons
	for code, reason := range state.Reasons {
		if reason.RequiresHuman {
			return false, fmt.Sprintf("reason %q requires human confirmation", code)
		}
	}

	// Verify reconciliation epoch proves fresh state
	if reconcileEpoch < state.RecoveryEpoch {
		return false, fmt.Sprintf("reconciliation epoch %d < required %d", reconcileEpoch, state.RecoveryEpoch)
	}

	// Verify shared dependency health
	if !g.sharedHealth.PersistenceHealthy {
		return false, "persistence unhealthy"
	}
	if !g.sharedHealth.PrivateWSHealthy {
		return false, "private-ws unhealthy"
	}
	if !g.sharedHealth.AccountReconcileHealthy {
		return false, "account-reconciliation unhealthy"
	}
	if !g.sharedHealth.PortfolioRiskHealthy {
		return false, "portfolio-risk unhealthy"
	}

	// Clear all auto-recoverable reasons
	state.Reasons = make(map[string]SafeStopReason)
	state.Active = false
	state.RecoveryEpoch = reconcileEpoch
	return true, ""
}

// UpdateSharedHealth updates the shared dependency health snapshot.
// When a shared dependency is unhealthy, automatically enters global Safe_Stop.
// When it recovers, automatically clears the corresponding reason.
func (g *TradingGate) UpdateSharedHealth(health SharedDependencyHealth) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.sharedHealth = health

	// Synchronize global reasons with current health state.
	// Unhealthy → add reason (idempotent). Healthy → remove reason.
	g.syncHealthReason(!health.PersistenceHealthy, ReasonPersistenceFailure, "persistence write/commit uncertain")
	g.syncHealthReason(!health.PrivateWSHealthy, ReasonPrivateWSUncertain, "private-ws auth/liveness/subscription uncertain")
	g.syncHealthReason(!health.AccountReconcileHealthy, ReasonAccountReconcile, "account reconciliation/auth uncertain")
	g.syncHealthReason(!health.PortfolioRiskHealthy, ReasonPortfolioRisk, "portfolio/combined risk uncertain")
}

// syncHealthReason adds or removes a global reason based on the unhealthy flag.
func (g *TradingGate) syncHealthReason(unhealthy bool, code string, details string) {
	if unhealthy {
		if _, exists := g.globalReasons[code]; !exists {
			g.globalReasons[code] = SafeStopReason{
				Code:    code,
				Since:   g.clock(),
				Details: details,
			}
		}
	} else {
		delete(g.globalReasons, code)
	}
}

// UpdateKnownPosition sets the known spot position for a symbol. Used to
// verify risk-reducing SELL does not exceed known position.
func (g *TradingGate) UpdateKnownPosition(symbol string, qty decimal.Decimal) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.knownPositions[symbol] = qty
}

// KnownPosition returns the tracked known position for a symbol.
func (g *TradingGate) KnownPosition(symbol string) decimal.Decimal {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if pos, ok := g.knownPositions[symbol]; ok {
		return pos
	}
	return decimal.Zero
}

// IsSymbolSafeStopped returns whether a symbol has active Safe_Stop reasons.
func (g *TradingGate) IsSymbolSafeStopped(symbol string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if state, ok := g.symbolStates[symbol]; ok {
		return state.Active && len(state.Reasons) > 0
	}
	return false
}

// IsGlobalSafeStopped returns whether global Safe_Stop is active.
func (g *TradingGate) IsGlobalSafeStopped() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.globalReasons) > 0
}

// SymbolReasons returns the active reason codes for a symbol.
func (g *TradingGate) SymbolReasons(symbol string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	state, ok := g.symbolStates[symbol]
	if !ok {
		return nil
	}
	codes := make([]string, 0, len(state.Reasons))
	for code := range state.Reasons {
		codes = append(codes, code)
	}
	return codes
}

// GlobalReasons returns the active global Safe_Stop reason codes.
func (g *TradingGate) GlobalReasons() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	codes := make([]string, 0, len(g.globalReasons))
	for code := range g.globalReasons {
		codes = append(codes, code)
	}
	return codes
}

// Snapshot returns a full gate state snapshot for observability.
func (g *TradingGate) Snapshot() GateSnapshot {
	g.mu.RLock()
	defer g.mu.RUnlock()

	snap := GateSnapshot{
		GlobalSafeStopped:   len(g.globalReasons) > 0,
		GlobalReasons:       make(map[string]SafeStopReason, len(g.globalReasons)),
		GlobalRecoveryEpoch: g.globalRecoveryEpoch,
		SymbolStates:        make(map[string]SymbolGateSnapshot, len(g.symbolStates)),
		SharedHealth:        g.sharedHealth,
	}

	for code, r := range g.globalReasons {
		snap.GlobalReasons[code] = r
	}

	for sym, state := range g.symbolStates {
		ss := SymbolGateSnapshot{
			Active:        state.Active,
			RecoveryEpoch: state.RecoveryEpoch,
			Reasons:       make(map[string]SafeStopReason, len(state.Reasons)),
		}
		for code, r := range state.Reasons {
			ss.Reasons[code] = r
		}
		snap.SymbolStates[sym] = ss
	}

	return snap
}

// GateSnapshot is an immutable view of the gate state for observability.
type GateSnapshot struct {
	GlobalSafeStopped   bool
	GlobalReasons       map[string]SafeStopReason
	GlobalRecoveryEpoch uint64
	SymbolStates        map[string]SymbolGateSnapshot
	SharedHealth        SharedDependencyHealth
}

// SymbolGateSnapshot is a per-symbol view.
type SymbolGateSnapshot struct {
	Active        bool
	RecoveryEpoch uint64
	Reasons       map[string]SafeStopReason
}

func (g *TradingGate) getOrCreateSymbolState(symbol string) *SymbolSafeStopState {
	state, ok := g.symbolStates[symbol]
	if !ok {
		state = &SymbolSafeStopState{
			Reasons: make(map[string]SafeStopReason),
		}
		g.symbolStates[symbol] = state
	}
	return state
}

// ClassifyOperation determines the OperationRiskClass for a given order request.
// This is the centralized classification per the design:
// - BUY is always RiskIncreasing
// - SELL is RiskReducing only if qty <= known position (caller verifies)
// - Unknown operations default to RiskIncreasing (safe default)
func ClassifyOperation(side models.Side, purpose string) OperationRiskClass {
	switch purpose {
	case "reconciliation", "query", "status-check":
		return Reconciliation
	case "confirmed-buy-cancel":
		return ConfirmedBuyCancellation
	}

	switch side {
	case models.SideBuy:
		return RiskIncreasing
	case models.SideSell:
		// SELL is risk-reducing IF the caller has verified qty <= known position.
		// The gate trusts the caller classification when purpose indicates reduction.
		switch purpose {
		case "counter-sell", "risk-reducing-sell", "grid-sell":
			return RiskReducing
		default:
			// Unknown SELL purpose: treat as risk-increasing by default
			return RiskIncreasing
		}
	default:
		return RiskIncreasing
	}
}
