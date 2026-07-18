package models

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	CounterOrderInitiationSLO = 5 * time.Second
	CounterOrderTerminalSLO   = 15 * time.Second
)

var (
	ErrInvalidFillIdentity = errors.New("models: invalid unique fill identity")
	ErrInvalidCumulative   = errors.New("models: invalid cumulative fill quantity")
)

type FillSource string

const (
	FillSourcePrivateWS      FillSource = "private-ws"
	FillSourceReconciliation FillSource = "rest-reconciliation"
	FillSourceReplay         FillSource = "replay"
)

type FillEligibility string

const (
	FillEligible   FillEligibility = "eligible"
	FillIneligible FillEligibility = "ineligible"
)

type IntentStatus string

const (
	IntentPending             IntentStatus = "pending"
	IntentDispatching         IntentStatus = "dispatching"
	IntentUncertain           IntentStatus = "uncertain"
	IntentConfirmed           IntentStatus = "confirmed"
	IntentSafeFailureTerminal IntentStatus = "safe-failure-terminal"
)

type OutboxStatus string

const (
	OutboxPending   OutboxStatus = "pending"
	OutboxLeased    OutboxStatus = "leased"
	OutboxUncertain OutboxStatus = "uncertain"
	OutboxCompleted OutboxStatus = "completed"
	OutboxFailed    OutboxStatus = "failed"
)

type FailureClass string

const (
	FailureNone                 FailureClass = ""
	FailureUncertainTransport   FailureClass = "uncertain-transport"
	FailureRetryableTransient   FailureClass = "retryable-transient"
	FailureDefinitiveReject     FailureClass = "definitive-business-reject"
	FailureAuthAccountUncertain FailureClass = "auth-account-uncertain"
	FailureUnknownTerminal      FailureClass = "unknown-terminal"
	FailureInitiationDeadline   FailureClass = "initiation-deadline-exceeded"
	FailureCriticalCommit       FailureClass = "critical-commit-uncertain"
	FailurePersistenceWrite     FailureClass = "persistence-write-failed"
)

type SafeStopScope string

const (
	SafeStopSymbol SafeStopScope = "symbol"
	SafeStopGlobal SafeStopScope = "global"
)

type RebalanceTerminalOutcome string

const (
	RebalanceReplaced         RebalanceTerminalOutcome = "replaced"
	RebalanceFilled           RebalanceTerminalOutcome = "filled"
	RebalanceAlreadyCancelled RebalanceTerminalOutcome = "already-cancelled"
	RebalanceKeptByRule       RebalanceTerminalOutcome = "kept-by-rule"
	RebalanceFailedSafe       RebalanceTerminalOutcome = "failed-safe"
)

// FillObservation contains exchange facts only. Decimal values remain base-10
// throughout the durable path; no float conversion is used for identity or PnL.
type FillObservation struct {
	Symbol             string
	ClientOrderID      string
	ExchangeOrderID    string
	ExchangeFillID     string
	Side               Side
	CumulativeQuantity decimal.Decimal
	FillPrice          decimal.Decimal
	Fee                decimal.Decimal
	Source             FillSource
	ExchangeTimestamp  time.Time
	ObservedAt         time.Time
	StrategyID         string
	GridLevel          int
}

// CounterOrderPlan is computed before entering the database transaction. The
// quantity is deliberately absent: the store derives it from the committed
// cumulative watermark, preventing stale callers from over-covering a fill.
type CounterOrderPlan struct {
	Eligibility      FillEligibility
	Price            decimal.Decimal
	Purpose          string
	IneligibleReason string
}

type FillLedgerRecord struct {
	FillKey            string
	Symbol             string
	ExchangeOrderID    string
	ExchangeFillID     string
	Side               Side
	CumulativeQuantity decimal.Decimal
	DeltaQuantity      decimal.Decimal
	FillPrice          decimal.Decimal
	Fee                decimal.Decimal
	Source             FillSource
	ExchangeTimestamp  time.Time
	ObservedAt         time.Time
	Eligibility        FillEligibility
	IneligibleReason   string
}

type OrderFillWatermark struct {
	Symbol                      string
	ExchangeOrderID             string
	ProcessedCumulativeQuantity decimal.Decimal
	UpdatedAt                   time.Time
}

type GridStateRecord struct {
	Symbol        string
	StrategyID    string
	Position      decimal.Decimal
	AvgEntryPrice decimal.Decimal
	RealizedPnL   decimal.Decimal
	TotalBuys     int64
	TotalSells    int64
	UpdatedAt     time.Time
}

type CounterOrderIntent struct {
	IntentID                   string
	FillKey                    string
	Symbol                     string
	Side                       Side
	Price                      decimal.Decimal
	Quantity                   decimal.Decimal
	Purpose                    string
	DeterministicClientOrderID string
	ObservedAt                 time.Time
	InitiationDeadline         time.Time
	TerminalDeadline           time.Time
	InitiatedAt                time.Time
	TerminalAt                 time.Time
	Status                     IntentStatus
	Attempts                   int
	ExchangeOrderID            string
	FinalErrorClass            FailureClass
	FinalSCode                 string
	FinalSMsg                  string
}

type OutboxRecord struct {
	OutboxID       string
	IntentID       string
	Status         OutboxStatus
	LeaseOwner     string
	LeaseUntil     time.Time
	NextAttemptAt  time.Time
	AttemptCount   int
	LastErrorClass FailureClass
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type BotOrderLineage struct {
	ClientOrderID     string
	ExchangeOrderID   string
	Symbol            string
	StrategyID        string
	Purpose           string
	ParentOrderID     string
	IntentID          string
	Side              Side
	State             OrderStatus
	CumulativeFillQty decimal.Decimal
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type ReconciliationWatermark struct {
	Symbol      string
	Stream      string
	ExchangeAt  time.Time
	StableID    string
	CompletedAt time.Time
}

type SafeStopRecord struct {
	Scope         SafeStopScope
	Symbol        string
	ReasonCode    string
	Active        bool
	Since         time.Time
	RecoveryEpoch uint64
	Details       string
	UpdatedAt     time.Time
}

type RebalanceOutcomeRecord struct {
	RunID                    string
	Symbol                   string
	OldClientOrderID         string
	OldExchangeOrderID       string
	ReferencePrice           decimal.Decimal
	ReferenceAge             time.Duration
	Deviation                decimal.Decimal
	TerminalOutcome          RebalanceTerminalOutcome
	ReplacementClientOrderID string
	ErrorClass               FailureClass
	RecordedAt               time.Time
}

type FillApplyResult struct {
	Duplicate  bool
	OutOfOrder bool
	Delta      decimal.Decimal
	Ledger     *FillLedgerRecord
	GridState  *GridStateRecord
	Intent     *CounterOrderIntent
	Outbox     *OutboxRecord
}

type OrderEffect struct {
	Found           bool
	ExchangeOrderID string
	ClientOrderID   string
	Status          OrderStatus
	SCode           string
	SMsg            string
}

func (e OrderEffect) ExchangeConfirmed() bool {
	if !e.Found {
		return false
	}
	switch e.Status {
	case OrderStatusSubmitted, OrderStatusOpen, OrderStatusPartiallyFilled, OrderStatusFilled:
		return true
	default:
		return false
	}
}

// CumulativeDelta returns only a newly observed base-10 increment. Equal or
// lower observations are duplicates/out-of-order and return isNew=false.
func CumulativeDelta(previous, observed decimal.Decimal) (delta decimal.Decimal, isNew bool, err error) {
	if previous.IsNegative() || !observed.IsPositive() {
		return decimal.Zero, false, ErrInvalidCumulative
	}
	if observed.LessThanOrEqual(previous) {
		return decimal.Zero, false, nil
	}
	return observed.Sub(previous), true, nil
}

// UniqueFillKey creates the canonical Unique_Fill identity from normalized
// exchange facts. Length prefixes make the preimage delimiter-safe; the
// versioned SHA-256 key keeps storage and logs bounded.
func UniqueFillKey(symbol, exchangeOrderID, exchangeFillID string, cumulative decimal.Decimal) (string, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	exchangeOrderID = strings.TrimSpace(exchangeOrderID)
	exchangeFillID = strings.TrimSpace(exchangeFillID)
	if symbol == "" || exchangeOrderID == "" || exchangeFillID == "" || !cumulative.IsPositive() {
		return "", ErrInvalidFillIdentity
	}
	preimage := canonicalFields("unique-fill-v1", symbol, exchangeOrderID, exchangeFillID, cumulative.String())
	digest := sha256.Sum256([]byte(preimage))
	return "uf1_" + hex.EncodeToString(digest[:]), nil
}

func CanonicalUniqueFillKey(observation FillObservation) (string, error) {
	return UniqueFillKey(observation.Symbol, observation.ExchangeOrderID, observation.ExchangeFillID, observation.CumulativeQuantity)
}

func CounterIntentID(fillKey, symbol, purpose string) (string, error) {
	if strings.TrimSpace(fillKey) == "" || strings.TrimSpace(symbol) == "" || strings.TrimSpace(purpose) == "" {
		return "", errors.New("models: invalid counter intent identity")
	}
	digest := sha256.Sum256([]byte(canonicalFields("counter-intent-v1", fillKey, strings.ToUpper(strings.TrimSpace(symbol)), purpose)))
	return "ci1_" + hex.EncodeToString(digest[:]), nil
}

// DeterministicClientOrderID returns an OKX-compatible alphanumeric ID no
// longer than 32 characters. Every retry of an intent receives the same ID.
func DeterministicClientOrderID(intentID, symbol, purpose string) (string, error) {
	if strings.TrimSpace(intentID) == "" || strings.TrimSpace(symbol) == "" || strings.TrimSpace(purpose) == "" {
		return "", errors.New("models: invalid deterministic client order identity")
	}
	digest := sha256.Sum256([]byte(canonicalFields("client-order-v1", intentID, strings.ToUpper(strings.TrimSpace(symbol)), purpose)))
	return "tb1" + hex.EncodeToString(digest[:])[:29], nil
}

func canonicalFields(fields ...string) string {
	var builder strings.Builder
	for _, field := range fields {
		builder.WriteString(strconv.Itoa(len(field)))
		builder.WriteByte(':')
		builder.WriteString(field)
	}
	return builder.String()
}

func ValidateFillObservation(observation FillObservation) error {
	if _, err := CanonicalUniqueFillKey(observation); err != nil {
		return err
	}
	if observation.Side != SideBuy && observation.Side != SideSell {
		return fmt.Errorf("models: invalid fill side %d", observation.Side)
	}
	if !observation.FillPrice.IsPositive() {
		return errors.New("models: fill price must be positive")
	}
	if observation.ObservedAt.IsZero() {
		return errors.New("models: observed_at is required")
	}
	switch observation.Source {
	case FillSourcePrivateWS, FillSourceReconciliation, FillSourceReplay:
		return nil
	default:
		return errors.New("models: valid fill source is required")
	}
}
