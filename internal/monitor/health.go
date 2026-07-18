package monitor

import (
	"encoding/json"
	"sync"
	"time"
)

// HealthState is the fixed output enum for machine health.
type HealthState string

const (
	HealthStateHealthy     HealthState = "healthy"
	HealthStateDegraded    HealthState = "degraded/reconciling"
	HealthStateSafeStopped HealthState = "safe-stopped"
)

// PrivateWSHealthSnapshot contains Private_WS liveness information.
type PrivateWSHealthSnapshot struct {
	State                  string `json:"state"`
	LastLivenessAgeMs      int64  `json:"last_liveness_age_ms"`
	Reconnects             int    `json:"reconnects"`
	SubscriptionsConfirmed bool   `json:"subscriptions_confirmed"`
}

// ReconciliationHealthSnapshot contains reconciliation lag information.
type ReconciliationHealthSnapshot struct {
	LastSuccess string `json:"last_success"`
	LagMs       int64  `json:"lag_ms"`
}

// SymbolHealthSnapshot contains per-symbol health status.
type SymbolHealthSnapshot struct {
	State           HealthState `json:"state"`
	SafeStopReasons []string    `json:"safe_stop_reasons"`
}

// HealthSnapshot is the complete machine-readable health output.
type HealthSnapshot struct {
	State                HealthState                        `json:"state"`
	Location             string                             `json:"location"`
	ServiceExpected      bool                               `json:"service_expected"`
	StateDirectoryWritable bool                             `json:"state_directory_writable"`
	PrivateWS            PrivateWSHealthSnapshot            `json:"private_ws"`
	Reconciliation       ReconciliationHealthSnapshot       `json:"reconciliation"`
	Symbols              map[string]SymbolHealthSnapshot    `json:"symbols"`
}

// HealthRegistry aggregates dependency and symbol states to produce the fixed
// health output: healthy | degraded/reconciling | safe-stopped.
//
// No credential-derived value appears in the health snapshot. Order/fill IDs
// are counts/correlation hashes, not full payload dumps.
type HealthRegistry struct {
	mu sync.RWMutex

	location             string
	serviceExpected      bool
	stateDirectoryWritable bool

	// Private_WS state
	privateWSState                  string
	privateWSLastLiveness           time.Time
	privateWSReconnects             int
	privateWSSubscriptionsConfirmed bool

	// Reconciliation state
	reconciliationLastSuccess time.Time
	reconciliationInterval    time.Duration

	// Per-symbol health
	symbolStates map[string]SymbolHealthSnapshot

	// Counters for fill/outbox observability
	fillDuplicatesSuppressed int64
	fillGaps                 int64
	outboxBacklog            int

	// Counter_SELL tracking
	counterSellConfirmed   int64
	counterSellRejected    int64
	counterSellUnconfirmed int64
	counterSellSafeFailed  int64

	// Rebalancer tracking
	rebalancerLastRun    time.Time
	rebalancerRefAge     time.Duration
	rebalancerStaleCount int

	// Safe_Stop state
	safeStopScopes map[string][]string // scope -> reason codes

	// Clock injection for testing
	clock func() time.Time
}

// HealthRegistryOption configures the HealthRegistry.
type HealthRegistryOption func(*HealthRegistry)

// WithHealthClock injects a test clock.
func WithHealthClock(clock func() time.Time) HealthRegistryOption {
	return func(h *HealthRegistry) { h.clock = clock }
}

// NewHealthRegistry creates a HealthRegistry with the given location.
func NewHealthRegistry(location string, opts ...HealthRegistryOption) *HealthRegistry {
	h := &HealthRegistry{
		location:               location,
		serviceExpected:        true,
		symbolStates:           make(map[string]SymbolHealthSnapshot),
		safeStopScopes:         make(map[string][]string),
		reconciliationInterval: 30 * time.Second,
		clock:                  time.Now,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// SetStateDirectoryWritable updates state directory health.
func (h *HealthRegistry) SetStateDirectoryWritable(writable bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stateDirectoryWritable = writable
}

// SetPrivateWSState updates Private_WS connection state.
func (h *HealthRegistry) SetPrivateWSState(state string, lastLiveness time.Time, reconnects int, subsConfirmed bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.privateWSState = state
	h.privateWSLastLiveness = lastLiveness
	h.privateWSReconnects = reconnects
	h.privateWSSubscriptionsConfirmed = subsConfirmed
}

// SetReconciliationSuccess records a successful reconciliation cycle.
func (h *HealthRegistry) SetReconciliationSuccess(at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reconciliationLastSuccess = at
}

// SetSymbolState updates per-symbol health.
func (h *HealthRegistry) SetSymbolState(symbol string, state HealthState, reasons []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.symbolStates[symbol] = SymbolHealthSnapshot{
		State:           state,
		SafeStopReasons: reasons,
	}
}

// SetSafeStopScope records safe stop reasons per scope.
func (h *HealthRegistry) SetSafeStopScope(scope string, reasons []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(reasons) == 0 {
		delete(h.safeStopScopes, scope)
	} else {
		h.safeStopScopes[scope] = reasons
	}
}

// IncrementFillDuplicates records a suppressed duplicate fill.
func (h *HealthRegistry) IncrementFillDuplicates() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fillDuplicatesSuppressed++
}

// IncrementFillGaps records a detected fill gap.
func (h *HealthRegistry) IncrementFillGaps() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fillGaps++
}

// SetOutboxBacklog records outbox pending count.
func (h *HealthRegistry) SetOutboxBacklog(count int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.outboxBacklog = count
}

// RecordCounterSellOutcome records one Counter_SELL terminal outcome.
func (h *HealthRegistry) RecordCounterSellOutcome(outcome string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch outcome {
	case "confirmed":
		h.counterSellConfirmed++
	case "rejected":
		h.counterSellRejected++
	case "unconfirmed":
		h.counterSellUnconfirmed++
	case "safe_failed":
		h.counterSellSafeFailed++
	}
}

// SetRebalancerState updates rebalancer observability fields.
func (h *HealthRegistry) SetRebalancerState(lastRun time.Time, refAge time.Duration, staleCount int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rebalancerLastRun = lastRun
	h.rebalancerRefAge = refAge
	h.rebalancerStaleCount = staleCount
}

// State computes the aggregate health state.
func (h *HealthRegistry) State() HealthState {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.computeState()
}

// computeState derives the top-level state (must hold at least RLock).
func (h *HealthRegistry) computeState() HealthState {
	// Any active safe-stop scope → safe-stopped
	if len(h.safeStopScopes) > 0 {
		return HealthStateSafeStopped
	}

	// Check for symbol-level safe-stops
	for _, sym := range h.symbolStates {
		if sym.State == HealthStateSafeStopped {
			return HealthStateSafeStopped
		}
	}

	// State directory not writable → safe-stopped
	if !h.stateDirectoryWritable {
		return HealthStateSafeStopped
	}

	// Private_WS not ready → degraded
	if h.privateWSState != "ready" {
		return HealthStateDegraded
	}

	// Reconciliation lag exceeds interval → degraded
	now := h.clock()
	if !h.reconciliationLastSuccess.IsZero() {
		lag := now.Sub(h.reconciliationLastSuccess)
		if lag > h.reconciliationInterval {
			return HealthStateDegraded
		}
	} else if h.serviceExpected {
		// Never reconciled but service expected → degraded
		return HealthStateDegraded
	}

	// Check for degraded symbols
	for _, sym := range h.symbolStates {
		if sym.State == HealthStateDegraded {
			return HealthStateDegraded
		}
	}

	return HealthStateHealthy
}

// Snapshot returns the full machine-readable health output.
func (h *HealthRegistry) Snapshot() HealthSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()

	now := h.clock()

	var livenessAge int64
	if !h.privateWSLastLiveness.IsZero() {
		livenessAge = now.Sub(h.privateWSLastLiveness).Milliseconds()
	}

	var reconcileLag int64
	var lastSuccessStr string
	if !h.reconciliationLastSuccess.IsZero() {
		reconcileLag = now.Sub(h.reconciliationLastSuccess).Milliseconds()
		lastSuccessStr = h.reconciliationLastSuccess.UTC().Format(time.RFC3339Nano)
	}

	symbols := make(map[string]SymbolHealthSnapshot, len(h.symbolStates))
	for k, v := range h.symbolStates {
		reasons := make([]string, len(v.SafeStopReasons))
		copy(reasons, v.SafeStopReasons)
		symbols[k] = SymbolHealthSnapshot{
			State:           v.State,
			SafeStopReasons: reasons,
		}
	}

	return HealthSnapshot{
		State:                  h.computeState(),
		Location:               h.location,
		ServiceExpected:        h.serviceExpected,
		StateDirectoryWritable: h.stateDirectoryWritable,
		PrivateWS: PrivateWSHealthSnapshot{
			State:                  h.privateWSState,
			LastLivenessAgeMs:      livenessAge,
			Reconnects:             h.privateWSReconnects,
			SubscriptionsConfirmed: h.privateWSSubscriptionsConfirmed,
		},
		Reconciliation: ReconciliationHealthSnapshot{
			LastSuccess: lastSuccessStr,
			LagMs:       reconcileLag,
		},
		Symbols: symbols,
	}
}

// JSON returns the health snapshot as JSON bytes.
func (h *HealthRegistry) JSON() ([]byte, error) {
	snap := h.Snapshot()
	return json.Marshal(snap)
}
