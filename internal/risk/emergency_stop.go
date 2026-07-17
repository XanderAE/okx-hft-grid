package risk

import (
	"errors"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// EmergencyStopCallback defines the interface for handlers invoked when emergency stop triggers.
type EmergencyStopCallback interface {
	// CancelAllOrders cancels all open orders across all symbols.
	CancelAllOrders() error
	// StopAllStrategies halts all running strategies.
	StopAllStrategies() error
	// SendCriticalAlert sends a CRITICAL alert with the given reason.
	SendCriticalAlert(reason string) error
}

// EmergencyStopManager implements the emergency stop mechanism for the risk manager.
// It tracks daily PnL, triggers emergency stop when loss exceeds threshold,
// and rejects all trading operations while active.
type EmergencyStopManager struct {
	mu sync.RWMutex

	emergencyStopActive bool
	emergencyStopReason string
	emergencyStopTime   time.Time

	dailyPnL          decimal.Decimal
	emergencyStopLoss decimal.Decimal // positive threshold; triggers when dailyPnL < -emergencyStopLoss

	callbacks []EmergencyStopCallback

	riskLimits *models.RiskLimits
}

// NewEmergencyStopManager creates a new EmergencyStopManager with the given risk limits.
func NewEmergencyStopManager(limits *models.RiskLimits) *EmergencyStopManager {
	esm := &EmergencyStopManager{
		dailyPnL:  decimal.Zero,
		callbacks: make([]EmergencyStopCallback, 0),
	}
	if limits != nil {
		esm.emergencyStopLoss = limits.EmergencyStopLoss
		esm.riskLimits = limits
	}
	return esm
}

// RegisterEmergencyCallback registers a handler that will be invoked when emergency stop triggers.
func (esm *EmergencyStopManager) RegisterEmergencyCallback(cb EmergencyStopCallback) {
	esm.mu.Lock()
	defer esm.mu.Unlock()
	esm.callbacks = append(esm.callbacks, cb)
}

// EmergencyStop triggers the emergency stop mechanism.
// It sets the active flag, records the reason, and invokes all registered callbacks
// (cancel orders, stop strategies, send critical alert).
// All subsequent CheckOrder/CheckBatchOrders will return rejected with "emergency stop active".
func (esm *EmergencyStopManager) EmergencyStop(reason string) {
	esm.mu.Lock()
	if esm.emergencyStopActive {
		esm.mu.Unlock()
		return // already active, no-op
	}
	esm.emergencyStopActive = true
	esm.emergencyStopReason = reason
	esm.emergencyStopTime = time.Now()
	callbacks := make([]EmergencyStopCallback, len(esm.callbacks))
	copy(callbacks, esm.callbacks)
	esm.mu.Unlock()

	// Invoke all registered callbacks (cancel orders, stop strategies, send alerts)
	for _, cb := range callbacks {
		cb.CancelAllOrders()
		cb.StopAllStrategies()
		cb.SendCriticalAlert(reason)
	}
}

// ResumeFromEmergencyStop clears the emergency stop state after manual confirmation.
// Returns an error if emergency stop is not currently active.
func (esm *EmergencyStopManager) ResumeFromEmergencyStop() error {
	esm.mu.Lock()
	defer esm.mu.Unlock()
	if !esm.emergencyStopActive {
		return errors.New("emergency stop is not active")
	}
	esm.emergencyStopActive = false
	esm.emergencyStopReason = ""
	return nil
}

// IsEmergencyStopActive returns whether emergency stop is currently engaged.
func (esm *EmergencyStopManager) IsEmergencyStopActive() bool {
	esm.mu.RLock()
	defer esm.mu.RUnlock()
	return esm.emergencyStopActive
}

// GetEmergencyStopReason returns the reason for the current emergency stop, or empty string if not active.
func (esm *EmergencyStopManager) GetEmergencyStopReason() string {
	esm.mu.RLock()
	defer esm.mu.RUnlock()
	return esm.emergencyStopReason
}

// UpdatePnL updates the daily PnL and triggers emergency stop if the loss exceeds the threshold.
// The emergencyStopLoss threshold is a positive value; emergency stop triggers when dailyPnL < -emergencyStopLoss.
func (esm *EmergencyStopManager) UpdatePnL(pnl decimal.Decimal) {
	esm.mu.Lock()
	esm.dailyPnL = pnl

	// Check if daily PnL has breached the emergency stop loss threshold
	if !esm.emergencyStopActive && esm.emergencyStopLoss.IsPositive() {
		negativeThreshold := esm.emergencyStopLoss.Neg() // -emergencyStopLoss
		if esm.dailyPnL.LessThan(negativeThreshold) {
			// Release lock before triggering (EmergencyStop acquires it)
			esm.mu.Unlock()
			esm.EmergencyStop("daily PnL " + esm.dailyPnL.String() + " breached emergency stop loss threshold -" + esm.emergencyStopLoss.String())
			return
		}
	}
	esm.mu.Unlock()
}

// CheckEmergencyStop checks if emergency stop is active and returns a RiskDecision.
// If active, returns rejected with reason. If not active, returns nil (caller should continue checks).
func (esm *EmergencyStopManager) CheckEmergencyStop() *RiskDecision {
	esm.mu.RLock()
	defer esm.mu.RUnlock()
	if esm.emergencyStopActive {
		return &RiskDecision{
			Approved: false,
			Reasons:  []string{"emergency stop active: " + esm.emergencyStopReason},
		}
	}
	return nil
}

// SetEmergencyStopLoss updates the emergency stop loss threshold.
func (esm *EmergencyStopManager) SetEmergencyStopLoss(threshold decimal.Decimal) {
	esm.mu.Lock()
	defer esm.mu.Unlock()
	esm.emergencyStopLoss = threshold
}

// GetDailyPnL returns the current daily PnL.
func (esm *EmergencyStopManager) GetDailyPnL() decimal.Decimal {
	esm.mu.RLock()
	defer esm.mu.RUnlock()
	return esm.dailyPnL
}
