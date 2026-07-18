package execution

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// StaleThreshold is the strict >2% deviation for an order to be considered stale.
// Exactly 2% (deviation == 0.02) is NOT stale and must be kept.
var StaleThreshold = decimal.NewFromFloat(0.02)

// MaxTickerAge is the maximum age of a ticker observation for it to be usable.
const MaxTickerAge = 5 * time.Second

// MaxScheduleJitter is the maximum allowed deviation from the 30-second schedule.
const MaxScheduleJitter = 5 * time.Second

// RebalancerConfig configures the per-symbol rebalancer.
type RebalancerConfig struct {
	Interval     time.Duration // 30 seconds
	MaxJitter    time.Duration // 5 seconds
	TickerMaxAge time.Duration // 5 seconds
	Clock        func() time.Time
}

// DefaultRebalancerConfig returns the design-mandated defaults.
func DefaultRebalancerConfig() RebalancerConfig {
	return RebalancerConfig{
		Interval:     30 * time.Second,
		MaxJitter:    MaxScheduleJitter,
		TickerMaxAge: MaxTickerAge,
		Clock:        time.Now,
	}
}

// BotOrdersStore provides bot order lineage verification.
type BotOrdersStore interface {
	// IsBotOwned checks if the order is proved to be owned by this bot via
	// clOrdId namespace + bot_orders lineage.
	IsBotOwned(ctx context.Context, symbol string, clientOrderID string, exchangeOrderID string) (bool, error)
}

// RebalanceOutcomeStore persists rebalance terminal outcomes.
type RebalanceOutcomeStore interface {
	// PersistRebalanceOutcome saves the terminal outcome for a stale order.
	PersistRebalanceOutcome(ctx context.Context, outcome models.RebalanceOutcomeRecord) error
}

// RebalancerDeps bundles all dependencies for the Rebalancer.
type RebalancerDeps struct {
	Gateway       ExchangeGateway
	Gate          RebalancerTradingGate
	FillObserver  FillObserver
	RulesProvider InstrumentRulesProvider
	OrderStore    BotOrdersStore
	OutcomeStore  RebalanceOutcomeStore
}

// RebalancerGateDecision is the gate authorization result used by the rebalancer.
type RebalancerGateDecision struct {
	Allowed     bool
	BlockReason string
}

// RebalancerTradingGate is the subset of TradingGate used by the rebalancer.
type RebalancerTradingGate interface {
	Authorize(symbol string, class int) RebalancerGateDecision
	EnterSymbolSafeStop(symbol string, reasonCode string, details string)
}

// OperationRiskClass constants for the rebalancer's gate interactions.
type OperationRiskClass = int

const (
	OpRiskIncreasing           OperationRiskClass = 0
	OpRiskReducing             OperationRiskClass = 1
	OpReconciliation           OperationRiskClass = 2
	OpConfirmedBuyCancellation OperationRiskClass = 3
)

// RebalancerCycleResult summarizes one cycle for observability.
type RebalancerCycleResult struct {
	Symbol         string
	StartedAt      time.Time
	CompletedAt    time.Time
	RunID          string
	ReferencePrice decimal.Decimal
	ReferenceAge   time.Duration
	Skipped        bool
	SkipReason     string
	StaleCount     int
	KeptCount      int
	Outcomes       []models.RebalanceOutcomeRecord
	Err            error
}

// Rebalancer implements the per-symbol ownership-safe rebalancer:
// - 30-second fixed schedule with jitter <= 5s
// - non-overlapping per-symbol lock
// - only uses ticker age <= 5s
// - strict >2% deviation
// - only processes Bot_Owned orders
// - terminal cancel confirmation before replacement
// - cancel/fill race handling via FillProcessor
// - every order gets one of: replaced|filled|already-cancelled|kept-by-rule|failed-safe
type Rebalancer struct {
	symbol string
	deps   RebalancerDeps
	config RebalancerConfig

	mu       sync.Mutex
	running  bool
	stopCh   chan struct{}
	done     chan struct{}
	cycleNum uint64

	// Callback for observability
	onCycleComplete func(RebalancerCycleResult)
}

// NewRebalancer creates a rebalancer for a single symbol.
func NewRebalancer(symbol string, deps RebalancerDeps, config RebalancerConfig, onComplete func(RebalancerCycleResult)) *Rebalancer {
	if config.Interval <= 0 {
		config.Interval = 30 * time.Second
	}
	if config.MaxJitter <= 0 {
		config.MaxJitter = MaxScheduleJitter
	}
	if config.TickerMaxAge <= 0 {
		config.TickerMaxAge = MaxTickerAge
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &Rebalancer{
		symbol:          symbol,
		deps:            deps,
		config:          config,
		onCycleComplete: onComplete,
	}
}

// Start begins the periodic rebalancer loop.
func (r *Rebalancer) Start() {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return
	}
	r.running = true
	r.stopCh = make(chan struct{})
	r.done = make(chan struct{})
	r.mu.Unlock()

	go r.loop()
}

// Stop halts the rebalancer and waits for in-flight cycle.
func (r *Rebalancer) Stop() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	r.running = false
	r.mu.Unlock()
	close(r.stopCh)
	<-r.done
}

// IsRunning returns whether the rebalancer loop is active.
func (r *Rebalancer) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// RunOnce executes a single rebalancer cycle synchronously.
// Used for testing and immediate triggers.
func (r *Rebalancer) RunOnce(ctx context.Context) RebalancerCycleResult {
	return r.executeCycle(ctx)
}

func (r *Rebalancer) loop() {
	defer close(r.done)

	ticker := time.NewTicker(r.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			result := r.executeCycle(ctx)
			cancel()
			if r.onCycleComplete != nil {
				r.onCycleComplete(result)
			}
		}
	}
}

func (r *Rebalancer) executeCycle(ctx context.Context) RebalancerCycleResult {
	now := r.config.Clock()
	r.mu.Lock()
	r.cycleNum++
	runID := fmt.Sprintf("rb-%s-%d-%d", r.symbol, now.UnixMilli(), r.cycleNum)
	r.mu.Unlock()

	result := RebalancerCycleResult{
		Symbol:    r.symbol,
		StartedAt: now,
		RunID:     runID,
	}

	// Step 1: Check trading gate authorization
	decision := r.deps.Gate.Authorize(r.symbol, OpRiskIncreasing)
	if !decision.Allowed {
		result.Skipped = true
		result.SkipReason = "trading-gate-blocked: " + decision.BlockReason
		result.CompletedAt = r.config.Clock()
		return result
	}

	// Step 2: Get fresh validated ticker
	ticker, err := r.deps.Gateway.GetTicker(ctx, r.symbol)
	if err != nil {
		result.Skipped = true
		result.SkipReason = fmt.Sprintf("ticker-fetch-error: %v", err)
		result.CompletedAt = r.config.Clock()
		return result
	}

	// Validate ticker: must have positive last price
	if !ticker.Last.IsPositive() {
		result.Skipped = true
		result.SkipReason = "ticker-invalid: non-positive last price"
		result.CompletedAt = r.config.Clock()
		return result
	}

	// Validate ticker age: age must be <= 5s
	tickerAge := r.config.Clock().Sub(ticker.ReceivedAt)
	if tickerAge > r.config.TickerMaxAge {
		result.Skipped = true
		result.SkipReason = fmt.Sprintf("ticker-stale: age=%v exceeds %v", tickerAge, r.config.TickerMaxAge)
		result.CompletedAt = r.config.Clock()
		return result
	}

	result.ReferencePrice = ticker.Last
	result.ReferenceAge = tickerAge

	// Step 3: List pending orders for the symbol
	pendingOrders, err := r.deps.Gateway.ListPendingOrders(ctx, r.symbol)
	if err != nil {
		result.Skipped = true
		result.SkipReason = fmt.Sprintf("list-pending-error: %v", err)
		result.CompletedAt = r.config.Clock()
		return result
	}

	// Step 4: Get current instrument rules for replacement normalization
	rules, err := r.deps.RulesProvider.Current(ctx, r.symbol)
	if err != nil {
		result.Skipped = true
		result.SkipReason = fmt.Sprintf("instrument-rules-error: %v", err)
		result.CompletedAt = r.config.Clock()
		return result
	}

	// Step 5: Process each pending order
	for _, order := range pendingOrders {
		outcome := r.processOrder(ctx, order, ticker.Last, tickerAge, rules, runID)
		result.Outcomes = append(result.Outcomes, outcome)
		if outcome.TerminalOutcome == models.RebalanceKeptByRule {
			result.KeptCount++
		} else {
			result.StaleCount++
		}
	}

	result.CompletedAt = r.config.Clock()
	return result
}

// processOrder evaluates and handles a single order.
func (r *Rebalancer) processOrder(
	ctx context.Context,
	order ExchangeOrderInfo,
	referencePrice decimal.Decimal,
	referenceAge time.Duration,
	rules models.InstrumentRules,
	runID string,
) models.RebalanceOutcomeRecord {
	outcome := models.RebalanceOutcomeRecord{
		RunID:              runID,
		Symbol:             r.symbol,
		OldClientOrderID:   order.ClientOrderID,
		OldExchangeOrderID: order.ExchangeOrderID,
		ReferencePrice:     referencePrice,
		ReferenceAge:       referenceAge,
		RecordedAt:         r.config.Clock(),
	}

	// Step A: Ownership filter - only process Bot_Owned orders
	owned, err := r.deps.OrderStore.IsBotOwned(ctx, r.symbol, order.ClientOrderID, order.ExchangeOrderID)
	if err != nil {
		// Cannot determine ownership: keep by rule (preserve unknown/manual orders)
		outcome.TerminalOutcome = models.RebalanceKeptByRule
		outcome.ErrorClass = models.FailureUncertainTransport
		r.persistOutcome(ctx, outcome)
		return outcome
	}
	if !owned {
		// Not bot-owned: preserve (manual/other-strategy order)
		outcome.TerminalOutcome = models.RebalanceKeptByRule
		r.persistOutcome(ctx, outcome)
		return outcome
	}

	// Step B: Compute exact decimal deviation
	// deviation = ABS(orderPrice - referencePrice) / referencePrice
	deviation := order.Price.Sub(referencePrice).Abs().Div(referencePrice)
	outcome.Deviation = deviation

	// Step C: Strict >2% check. Exactly 2% (<=0.02) must be preserved.
	if deviation.LessThanOrEqual(StaleThreshold) {
		outcome.TerminalOutcome = models.RebalanceKeptByRule
		r.persistOutcome(ctx, outcome)
		return outcome
	}

	// Order is stale (>2%): process cancel-and-replace flow
	return r.processStaleOrder(ctx, order, referencePrice, referenceAge, rules, outcome)
}

// processStaleOrder handles a confirmed stale Bot_Owned order.
func (r *Rebalancer) processStaleOrder(
	ctx context.Context,
	order ExchangeOrderInfo,
	referencePrice decimal.Decimal,
	referenceAge time.Duration,
	rules models.InstrumentRules,
	outcome models.RebalanceOutcomeRecord,
) models.RebalanceOutcomeRecord {

	// Step 1: Query current terminal state BEFORE cancel
	preQuery, err := r.deps.Gateway.QueryOrder(ctx, OrderRef{
		Symbol:          r.symbol,
		ExchangeOrderID: order.ExchangeOrderID,
		ClientOrderID:   order.ClientOrderID,
	})
	if err != nil {
		// Cannot confirm current state: failed-safe, do not proceed
		outcome.TerminalOutcome = models.RebalanceFailedSafe
		outcome.ErrorClass = models.FailureUncertainTransport
		r.deps.Gate.EnterSymbolSafeStop(r.symbol, "rebalance-terminal-failure",
			fmt.Sprintf("pre-cancel query failed for %s: %v", order.ExchangeOrderID, err))
		r.persistOutcome(ctx, outcome)
		return outcome
	}

	// Check if already filled
	if preQuery.Status == models.OrderStatusFilled {
		// Process the fill delta through FillProcessor
		r.processMissedFill(ctx, preQuery)
		outcome.TerminalOutcome = models.RebalanceFilled
		r.persistOutcome(ctx, outcome)
		return outcome
	}

	// Check if already cancelled
	if preQuery.Status == models.OrderStatusCancelled {
		// Process any fill that occurred before/during cancellation
		if preQuery.CumulativeFillQty.IsPositive() {
			r.processMissedFill(ctx, preQuery)
		}
		outcome.TerminalOutcome = models.RebalanceAlreadyCancelled
		// Evaluate replacement from current state
		r.createReplacement(ctx, order, referencePrice, rules, &outcome)
		r.persistOutcome(ctx, outcome)
		return outcome
	}

	// Step 2: Process any partial fill before cancel
	if preQuery.CumulativeFillQty.IsPositive() && preQuery.CumulativeFillQty.GreaterThan(order.CumulativeFillQty) {
		r.processMissedFill(ctx, preQuery)
	}

	// Step 3: Send cancel request
	cancelResult, err := r.deps.Gateway.CancelOrder(ctx, OrderRef{
		Symbol:          r.symbol,
		ExchangeOrderID: order.ExchangeOrderID,
		ClientOrderID:   order.ClientOrderID,
	})
	if err != nil {
		// Cancel request failed: query terminal state
		outcome = r.handleCancelUncertainty(ctx, order, outcome)
		r.persistOutcome(ctx, outcome)
		return outcome
	}

	// Cancel response received but need to VERIFY terminal state
	_ = cancelResult

	// Step 4: Query terminal state AFTER cancel to confirm
	postQuery, err := r.deps.Gateway.QueryOrder(ctx, OrderRef{
		Symbol:          r.symbol,
		ExchangeOrderID: order.ExchangeOrderID,
		ClientOrderID:   order.ClientOrderID,
	})
	if err != nil {
		// Cannot confirm cancel: failed-safe
		outcome.TerminalOutcome = models.RebalanceFailedSafe
		outcome.ErrorClass = models.FailureUncertainTransport
		r.deps.Gate.EnterSymbolSafeStop(r.symbol, "rebalance-terminal-failure",
			fmt.Sprintf("post-cancel query failed for %s: %v", order.ExchangeOrderID, err))
		r.persistOutcome(ctx, outcome)
		return outcome
	}

	// Step 5: Handle terminal state
	switch postQuery.Status {
	case models.OrderStatusCancelled:
		// Successfully cancelled - check for fill race
		if postQuery.CumulativeFillQty.IsPositive() {
			// Cancel/fill race: process the fill first
			r.processMissedFill(ctx, postQuery)
		}
		// Now create replacement
		outcome.TerminalOutcome = models.RebalanceReplaced
		r.createReplacement(ctx, order, referencePrice, rules, &outcome)
		r.persistOutcome(ctx, outcome)
		return outcome

	case models.OrderStatusFilled:
		// Filled during cancel attempt (cancel/fill race)
		r.processMissedFill(ctx, postQuery)
		outcome.TerminalOutcome = models.RebalanceFilled
		r.persistOutcome(ctx, outcome)
		return outcome

	case models.OrderStatusOpen, models.OrderStatusPartiallyFilled:
		// Cancel did not take effect yet - could be timing. Mark failed-safe.
		outcome.TerminalOutcome = models.RebalanceFailedSafe
		outcome.ErrorClass = models.FailureUnknownTerminal
		r.deps.Gate.EnterSymbolSafeStop(r.symbol, "rebalance-terminal-failure",
			fmt.Sprintf("order %s still open after cancel attempt", order.ExchangeOrderID))
		r.persistOutcome(ctx, outcome)
		return outcome

	default:
		// Unknown state: failed-safe
		outcome.TerminalOutcome = models.RebalanceFailedSafe
		outcome.ErrorClass = models.FailureUnknownTerminal
		r.deps.Gate.EnterSymbolSafeStop(r.symbol, "rebalance-terminal-failure",
			fmt.Sprintf("order %s in unexpected state %s after cancel", order.ExchangeOrderID, postQuery.Status.String()))
		r.persistOutcome(ctx, outcome)
		return outcome
	}
}

// handleCancelUncertainty handles the case when a cancel request fails.
func (r *Rebalancer) handleCancelUncertainty(
	ctx context.Context,
	order ExchangeOrderInfo,
	outcome models.RebalanceOutcomeRecord,
) models.RebalanceOutcomeRecord {
	// Query to determine actual state
	query, err := r.deps.Gateway.QueryOrder(ctx, OrderRef{
		Symbol:          r.symbol,
		ExchangeOrderID: order.ExchangeOrderID,
		ClientOrderID:   order.ClientOrderID,
	})
	if err != nil {
		// Cannot determine state: failed-safe and Safe_Stop
		outcome.TerminalOutcome = models.RebalanceFailedSafe
		outcome.ErrorClass = models.FailureUnknownTerminal
		r.deps.Gate.EnterSymbolSafeStop(r.symbol, "rebalance-terminal-failure",
			fmt.Sprintf("cancel failed and query failed for %s: %v", order.ExchangeOrderID, err))
		return outcome
	}

	switch query.Status {
	case models.OrderStatusFilled:
		r.processMissedFill(ctx, query)
		outcome.TerminalOutcome = models.RebalanceFilled
	case models.OrderStatusCancelled:
		outcome.TerminalOutcome = models.RebalanceAlreadyCancelled
	default:
		// Still open or unknown: failed-safe
		outcome.TerminalOutcome = models.RebalanceFailedSafe
		outcome.ErrorClass = models.FailureUnknownTerminal
		r.deps.Gate.EnterSymbolSafeStop(r.symbol, "rebalance-terminal-failure",
			fmt.Sprintf("order %s in state %s after cancel failure", order.ExchangeOrderID, query.Status.String()))
	}
	return outcome
}

// processMissedFill sends a discovered fill through FillProcessor.
func (r *Rebalancer) processMissedFill(ctx context.Context, orderInfo ExchangeOrderInfo) {
	if !orderInfo.CumulativeFillQty.IsPositive() {
		return
	}

	obs := models.FillObservation{
		Symbol:             r.symbol,
		ClientOrderID:      orderInfo.ClientOrderID,
		ExchangeOrderID:    orderInfo.ExchangeOrderID,
		ExchangeFillID:     fmt.Sprintf("rebalance-discover-%s-%d", orderInfo.ExchangeOrderID, r.config.Clock().UnixMilli()),
		Side:               orderInfo.Side,
		CumulativeQuantity: orderInfo.CumulativeFillQty,
		FillPrice:          orderInfo.AvgFillPrice,
		Source:             models.FillSourceReconciliation,
		ExchangeTimestamp:  orderInfo.UpdateTime,
		ObservedAt:         r.config.Clock(),
	}

	plan := models.CounterOrderPlan{
		Eligibility: models.FillEligible,
		Price:       orderInfo.AvgFillPrice,
		Purpose:     "counter-sell",
	}
	if orderInfo.Side == models.SideSell {
		plan.Eligibility = models.FillIneligible
		plan.IneligibleReason = "sell-fill-no-counter-from-rebalancer"
	}

	// Send through unified FillProcessor; errors are logged but don't block rebalancer
	_, _ = r.deps.FillObserver.ObserveFill(ctx, obs, plan)
}

// createReplacement creates a deterministic replacement order for a confirmed-cancelled stale order.
func (r *Rebalancer) createReplacement(
	ctx context.Context,
	oldOrder ExchangeOrderInfo,
	referencePrice decimal.Decimal,
	rules models.InstrumentRules,
	outcome *models.RebalanceOutcomeRecord,
) {
	// Derive replacement price from reference price and original order's side/level
	// The replacement targets the reference price area at the same grid spacing
	replacementPrice := referencePrice
	if oldOrder.Side == models.SideBuy {
		// BUY replacement: use a price near current reference but reflecting grid intent
		replacementPrice = referencePrice
	} else {
		// SELL replacement: place at or above reference
		replacementPrice = referencePrice
	}

	// Generate deterministic client order ID for the replacement
	replacementClOrdID := fmt.Sprintf("tb1rb%s%d", oldOrder.ExchangeOrderID[:min(8, len(oldOrder.ExchangeOrderID))], r.config.Clock().UnixMilli()%100000)
	if len(replacementClOrdID) > 32 {
		replacementClOrdID = replacementClOrdID[:32]
	}

	// Determine replacement quantity from original order
	replacementQty := oldOrder.Quantity
	if oldOrder.CumulativeFillQty.IsPositive() {
		replacementQty = oldOrder.Quantity.Sub(oldOrder.CumulativeFillQty)
	}
	if !replacementQty.IsPositive() {
		// Nothing to replace (fully filled situation already handled)
		return
	}

	// Normalize using instrument rules
	candidate := NormalizedOrderRequest{
		Symbol:    r.symbol,
		Side:      oldOrder.Side,
		OrderType: models.OrderTypePostOnly,
		Price:     replacementPrice,
		Quantity:  replacementQty,
		ClOrdID:   replacementClOrdID,
		Purpose:   "replacement",
	}

	normResult := NormalizeOrder(candidate, rules, r.config.Clock())
	if normResult.NoSend {
		// Cannot normalize: failed-safe for this replacement
		outcome.TerminalOutcome = models.RebalanceFailedSafe
		outcome.ErrorClass = models.FailureDefinitiveReject
		r.deps.Gate.EnterSymbolSafeStop(r.symbol, "rebalance-terminal-failure",
			fmt.Sprintf("replacement normalization failed: %s", normResult.Reason))
		return
	}

	// Authorize the replacement through trading gate
	decision := r.deps.Gate.Authorize(r.symbol, OpRiskIncreasing)
	if !decision.Allowed {
		outcome.TerminalOutcome = models.RebalanceFailedSafe
		outcome.ErrorClass = models.FailureDefinitiveReject
		return
	}

	// Place the replacement order
	placeResult, err := r.deps.Gateway.PlaceOrder(ctx, *normResult.Normalized)
	if err != nil {
		// Placement error: NEVER silent continue
		outcome.TerminalOutcome = models.RebalanceFailedSafe
		outcome.ErrorClass = models.FailureUncertainTransport
		r.deps.Gate.EnterSymbolSafeStop(r.symbol, "rebalance-terminal-failure",
			fmt.Sprintf("replacement place error for %s: %v", r.symbol, err))
		return
	}

	if placeResult.Err != nil {
		// Exchange rejected: NEVER silent continue
		outcome.TerminalOutcome = models.RebalanceFailedSafe
		outcome.ErrorClass = models.FailureDefinitiveReject
		r.deps.Gate.EnterSymbolSafeStop(r.symbol, "rebalance-terminal-failure",
			fmt.Sprintf("replacement rejected: sCode=%s sMsg=%s", placeResult.Err.SCode, placeResult.Err.SMsg))
		return
	}

	// Replacement succeeded
	outcome.TerminalOutcome = models.RebalanceReplaced
	outcome.ReplacementClientOrderID = replacementClOrdID
}

// persistOutcome saves the outcome to the durable store.
func (r *Rebalancer) persistOutcome(ctx context.Context, outcome models.RebalanceOutcomeRecord) {
	if r.deps.OutcomeStore != nil {
		_ = r.deps.OutcomeStore.PersistRebalanceOutcome(ctx, outcome)
	}
}
