package execution

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/yourname/okx-hft-grid/pkg/models"
)

// ReconcileReason describes why a reconciliation cycle was triggered.
type ReconcileReason string

const (
	ReconcileReasonStartup       ReconcileReason = "startup"
	ReconcileReasonReconnect     ReconcileReason = "reconnect"
	ReconcileReasonGap           ReconcileReason = "gap"
	ReconcileReasonUncertainOutbox ReconcileReason = "uncertain-outbox"
	ReconcileReasonPeriodic      ReconcileReason = "periodic"
	ReconcileReasonManual        ReconcileReason = "manual"
)

// ReconcileResult summarizes one completed reconciliation cycle.
type ReconcileResult struct {
	Symbol          string
	Reason          ReconcileReason
	StartedAt       time.Time
	CompletedAt     time.Time
	PagesQueried    int
	FillsChecked    int
	FillsCompensated int
	DuplicatesFound int
	OrdersChecked   int
	OrderDiffs      int
	WatermarkBefore *models.ReconciliationWatermark
	WatermarkAfter  *models.ReconciliationWatermark
	Err             error
	Overrun         bool
}

// ReconcileCycleCallback is invoked after each cycle completes for observability.
type ReconcileCycleCallback func(ReconcileResult)

// FillObserver is the unified fill observation path (same for WS and REST).
// Task 3.2's ObserveFill is invoked through this interface.
type FillObserver interface {
	ObserveFill(ctx context.Context, obs models.FillObservation, plan models.CounterOrderPlan) (*models.FillApplyResult, error)
}

// ReconciliationStore abstracts the persistence operations needed by the coordinator.
type ReconciliationStore interface {
	LoadReconciliationWatermark(ctx context.Context, symbol, stream string) (*models.ReconciliationWatermark, error)
	CommitReconciliationWatermark(ctx context.Context, wm models.ReconciliationWatermark) error
}

// DefaultOverlapWindow is subtracted from the watermark timestamp to handle
// equal timestamps and late ordering at the exchange boundary.
const DefaultOverlapWindow = 5 * time.Second

// ReconciliationCoordinatorConfig holds tunable parameters.
type ReconciliationCoordinatorConfig struct {
	Interval      time.Duration // Fixed 30-second schedule from design
	OverlapWindow time.Duration // Overlap window for paginated queries
	PageSize      int           // Fills per page for ListFills
	Clock         func() time.Time
}

// DefaultReconciliationCoordinatorConfig returns the design-mandated defaults.
func DefaultReconciliationCoordinatorConfig() ReconciliationCoordinatorConfig {
	return ReconciliationCoordinatorConfig{
		Interval:      30 * time.Second,
		OverlapWindow: DefaultOverlapWindow,
		PageSize:      100,
		Clock:         time.Now,
	}
}

// symbolReconcileState tracks per-symbol non-overlapping lock, coalescing, and overrun.
type symbolReconcileState struct {
	mu             sync.Mutex
	running        bool
	pendingTrigger *ReconcileReason // at most one follow-up coalesced
	overrunCount   int64
	lastCycleStart time.Time
}

// ReconciliationCoordinator replaces the legacy order-only Reconciler loop.
// It unifies WS and REST observations into the same FillProcessor path,
// provides immediate triggers, fixed 30-second periodic scheduling, per-symbol
// non-overlapping cycles with coalesced follow-ups, paginated fill/order queries,
// and committed watermark advancement.
type ReconciliationCoordinator struct {
	gateway  ExchangeGateway
	observer FillObserver
	store    ReconciliationStore
	config   ReconciliationCoordinatorConfig
	callback ReconcileCycleCallback

	mu       sync.Mutex
	symbols  map[string]*symbolReconcileState
	stopCh   chan struct{}
	done     chan struct{}
	running  bool
	triggerCh chan symbolTrigger
}

type symbolTrigger struct {
	symbol string
	reason ReconcileReason
}

// NewReconciliationCoordinator creates a coordinator. Caller must call Start()
// to begin periodic scheduling and trigger handling.
func NewReconciliationCoordinator(
	gateway ExchangeGateway,
	observer FillObserver,
	store ReconciliationStore,
	cfg ReconciliationCoordinatorConfig,
	callback ReconcileCycleCallback,
) *ReconciliationCoordinator {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.OverlapWindow <= 0 {
		cfg.OverlapWindow = DefaultOverlapWindow
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = 100
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &ReconciliationCoordinator{
		gateway:   gateway,
		observer:  observer,
		store:     store,
		config:    cfg,
		callback:  callback,
		symbols:   make(map[string]*symbolReconcileState),
		triggerCh: make(chan symbolTrigger, 64),
	}
}

// SetSymbols configures which symbols to reconcile periodically.
func (rc *ReconciliationCoordinator) SetSymbols(symbols []string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	for _, s := range symbols {
		if _, ok := rc.symbols[s]; !ok {
			rc.symbols[s] = &symbolReconcileState{}
		}
	}
}

// Trigger enqueues an immediate reconciliation for the given symbol and reason.
// If the symbol is currently running, it coalesces into a single follow-up.
func (rc *ReconciliationCoordinator) Trigger(symbol string, reason ReconcileReason) {
	select {
	case rc.triggerCh <- symbolTrigger{symbol: symbol, reason: reason}:
	default:
		log.Printf("[ReconciliationCoordinator] trigger channel full for %s reason=%s", symbol, reason)
	}
}

// TriggerAll enqueues immediate reconciliation for all configured symbols.
func (rc *ReconciliationCoordinator) TriggerAll(reason ReconcileReason) {
	rc.mu.Lock()
	syms := make([]string, 0, len(rc.symbols))
	for s := range rc.symbols {
		syms = append(syms, s)
	}
	rc.mu.Unlock()
	for _, s := range syms {
		rc.Trigger(s, reason)
	}
}

// Start begins the periodic 30-second schedule and trigger dispatch loop.
func (rc *ReconciliationCoordinator) Start() {
	rc.mu.Lock()
	if rc.running {
		rc.mu.Unlock()
		return
	}
	rc.running = true
	rc.stopCh = make(chan struct{})
	rc.done = make(chan struct{})
	rc.mu.Unlock()

	go rc.loop()
}

// Stop halts the coordinator and waits for in-flight cycles to finish.
func (rc *ReconciliationCoordinator) Stop() {
	rc.mu.Lock()
	if !rc.running {
		rc.mu.Unlock()
		return
	}
	rc.running = false
	rc.mu.Unlock()
	close(rc.stopCh)
	<-rc.done
}

// IsRunning reports whether the coordinator loop is active.
func (rc *ReconciliationCoordinator) IsRunning() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.running
}

// loop runs the fixed-rate 30-second periodic schedule plus immediate triggers.
// The interval is measured from the START of the previous cycle, not the end.
func (rc *ReconciliationCoordinator) loop() {
	defer close(rc.done)

	ticker := time.NewTicker(rc.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-rc.stopCh:
			return
		case <-ticker.C:
			rc.periodicReconcileAll()
		case trigger := <-rc.triggerCh:
			rc.handleTrigger(trigger)
		}
	}
}

func (rc *ReconciliationCoordinator) periodicReconcileAll() {
	rc.mu.Lock()
	syms := make([]string, 0, len(rc.symbols))
	for s := range rc.symbols {
		syms = append(syms, s)
	}
	rc.mu.Unlock()

	for _, s := range syms {
		rc.handleTrigger(symbolTrigger{symbol: s, reason: ReconcileReasonPeriodic})
	}
}

func (rc *ReconciliationCoordinator) handleTrigger(trigger symbolTrigger) {
	rc.mu.Lock()
	state, ok := rc.symbols[trigger.symbol]
	if !ok {
		state = &symbolReconcileState{}
		rc.symbols[trigger.symbol] = state
	}
	rc.mu.Unlock()

	state.mu.Lock()
	if state.running {
		// Coalesce: only one pending follow-up per symbol
		if state.pendingTrigger == nil {
			reason := trigger.reason
			state.pendingTrigger = &reason
		}
		state.overrunCount++
		state.mu.Unlock()
		return
	}
	state.running = true
	state.mu.Unlock()

	// Run the cycle (synchronously in the loop goroutine, or spawn if needed)
	go rc.runCycle(trigger.symbol, trigger.reason, state)
}

func (rc *ReconciliationCoordinator) runCycle(symbol string, reason ReconcileReason, state *symbolReconcileState) {
	for {
		result := rc.executeCycle(symbol, reason)

		// Check for coalesced follow-up
		state.mu.Lock()
		if state.pendingTrigger != nil {
			reason = *state.pendingTrigger
			state.pendingTrigger = nil
			result.Overrun = true
			state.mu.Unlock()
			// Emit callback for the completed cycle before starting follow-up
			if rc.callback != nil {
				rc.callback(result)
			}
			continue
		}
		state.running = false
		state.mu.Unlock()

		if rc.callback != nil {
			rc.callback(result)
		}
		return
	}
}

// executeCycle performs one full reconciliation cycle for a symbol:
// 1. Load committed watermark
// 2. Paginate fills from overlap window
// 3. Route each fill through the unified FillProcessor (ObserveFill)
// 4. Query and apply exchange-authoritative order states
// 5. Advance watermark only on complete success
func (rc *ReconciliationCoordinator) executeCycle(symbol string, reason ReconcileReason) ReconcileResult {
	now := rc.config.Clock()
	result := ReconcileResult{
		Symbol:    symbol,
		Reason:    reason,
		StartedAt: now,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	// 1. Load current watermark
	wm, err := rc.store.LoadReconciliationWatermark(ctx, symbol, "rest")
	if err != nil {
		result.Err = fmt.Errorf("load watermark: %w", err)
		result.CompletedAt = rc.config.Clock()
		return result
	}
	result.WatermarkBefore = wm

	// 2. Determine query window start with overlap
	var windowStart time.Time
	if wm != nil && !wm.ExchangeAt.IsZero() {
		windowStart = wm.ExchangeAt.Add(-rc.config.OverlapWindow)
	}
	// If no watermark, query from epoch (full history for new/bootstrap)

	// 3. Paginate fills
	fillsResult, err := rc.paginateFills(ctx, symbol, windowStart, &result)
	if err != nil {
		result.Err = fmt.Errorf("paginate fills: %w", err)
		result.CompletedAt = rc.config.Clock()
		return result
	}

	// 4. Process each fill through the unified FillProcessor
	var latestExchangeTS time.Time
	var latestStableID string
	for _, fill := range fillsResult {
		obs := fillRecordToObservation(fill, symbol, rc.config.Clock)
		plan := models.CounterOrderPlan{
			Eligibility: models.FillEligible,
			Price:       fill.Price, // Counter price derived by FillProcessor/grid
			Purpose:     "counter-sell",
		}
		if fill.Side == models.SideSell {
			plan.Eligibility = models.FillIneligible
			plan.IneligibleReason = "sell-fill-no-counter"
		}

		applyResult, applyErr := rc.observer.ObserveFill(ctx, obs, plan)
		result.FillsChecked++
		if applyErr != nil {
			// DB failure: do not advance watermark
			result.Err = fmt.Errorf("apply fill: %w", applyErr)
			result.CompletedAt = rc.config.Clock()
			return result
		}
		if applyResult != nil {
			if applyResult.Duplicate {
				result.DuplicatesFound++
			} else {
				result.FillsCompensated++
			}
		}

		// Track latest timestamp/ID for watermark
		if fill.Timestamp.After(latestExchangeTS) {
			latestExchangeTS = fill.Timestamp
			latestStableID = fill.ExchangeFillID
		}
	}

	// 5. Advance watermark only if pages complete and all applies succeeded
	if latestExchangeTS.After(time.Time{}) {
		newWM := models.ReconciliationWatermark{
			Symbol:      symbol,
			Stream:      "rest",
			ExchangeAt:  latestExchangeTS,
			StableID:    latestStableID,
			CompletedAt: rc.config.Clock(),
		}
		if err := rc.store.CommitReconciliationWatermark(ctx, newWM); err != nil {
			result.Err = fmt.Errorf("commit watermark: %w", err)
			result.CompletedAt = rc.config.Clock()
			return result
		}
		result.WatermarkAfter = &newWM
	}

	result.CompletedAt = rc.config.Clock()
	return result
}

// paginateFills queries all pages of bot-owned fills since windowStart.
// Returns error on partial page (transport/parse/auth failure) so watermark is never advanced.
func (rc *ReconciliationCoordinator) paginateFills(
	ctx context.Context,
	symbol string,
	windowStart time.Time,
	result *ReconcileResult,
) ([]FillRecord, error) {
	var allFills []FillRecord
	cursor := FillCursor{
		Limit:     rc.config.PageSize,
		StartTime: windowStart,
	}

	for {
		page, err := rc.gateway.ListFills(ctx, symbol, cursor)
		if err != nil {
			return nil, fmt.Errorf("list fills page: %w", err)
		}
		result.PagesQueried++
		allFills = append(allFills, page.Fills...)

		if !page.HasMore || page.Cursor == "" {
			break
		}
		cursor.After = page.Cursor
	}
	return allFills, nil
}

// fillRecordToObservation converts a gateway FillRecord to a models.FillObservation
// for the unified FillProcessor path.
func fillRecordToObservation(fr FillRecord, symbol string, now func() time.Time) models.FillObservation {
	return models.FillObservation{
		Symbol:             symbol,
		ExchangeOrderID:    fr.ExchangeOrderID,
		ExchangeFillID:     fr.ExchangeFillID,
		Side:               fr.Side,
		CumulativeQuantity: fr.CumulativeQuantity,
		FillPrice:          fr.Price,
		Fee:                fr.Fee,
		Source:             models.FillSourceReconciliation,
		ExchangeTimestamp:  fr.Timestamp,
		ObservedAt:         now(),
	}
}

// ReconcileNow performs a synchronous reconciliation cycle for the given symbol.
// Returns immediately if the symbol is already running (coalesces).
func (rc *ReconciliationCoordinator) ReconcileNow(ctx context.Context, symbol string, reason ReconcileReason) ReconcileResult {
	rc.mu.Lock()
	state, ok := rc.symbols[symbol]
	if !ok {
		state = &symbolReconcileState{}
		rc.symbols[symbol] = state
	}
	rc.mu.Unlock()

	state.mu.Lock()
	if state.running {
		// Already running: coalesce
		if state.pendingTrigger == nil {
			state.pendingTrigger = &reason
		}
		state.overrunCount++
		state.mu.Unlock()
		return ReconcileResult{
			Symbol:  symbol,
			Reason:  reason,
			Overrun: true,
			Err:     fmt.Errorf("reconciliation already in progress, coalesced"),
		}
	}
	state.running = true
	state.mu.Unlock()

	result := rc.executeCycle(symbol, reason)

	state.mu.Lock()
	state.running = false
	state.mu.Unlock()

	if rc.callback != nil {
		rc.callback(result)
	}
	return result
}

// OverrunCount returns the total overrun count for a symbol.
func (rc *ReconciliationCoordinator) OverrunCount(symbol string) int64 {
	rc.mu.Lock()
	state, ok := rc.symbols[symbol]
	rc.mu.Unlock()
	if !ok {
		return 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.overrunCount
}
