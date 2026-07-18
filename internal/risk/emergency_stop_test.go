package risk

import (
	"sync"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// mockEmergencyCallback is a test double for EmergencyStopCallback.
type mockEmergencyCallback struct {
	mu                     sync.Mutex
	cancelAllOrdersCalls   int
	stopAllStrategiesCalls int
	sendCriticalAlertCalls int
	lastAlertReason        string
}

func (m *mockEmergencyCallback) CancelAllOrders() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelAllOrdersCalls++
	return nil
}

func (m *mockEmergencyCallback) StopAllStrategies() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopAllStrategiesCalls++
	return nil
}

func (m *mockEmergencyCallback) SendCriticalAlert(reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendCriticalAlertCalls++
	m.lastAlertReason = reason
	return nil
}

func TestEmergencyStop_TriggerOnLossThreshold(t *testing.T) {
	limits := &models.RiskLimits{
		EmergencyStopLoss: decimal.NewFromFloat(1000), // trigger at -1000
	}
	esm := NewEmergencyStopManager(limits)
	cb := &mockEmergencyCallback{}
	esm.RegisterEmergencyCallback(cb)

	// PnL at -500 should NOT trigger
	esm.UpdatePnL(decimal.NewFromFloat(-500))
	if esm.IsEmergencyStopActive() {
		t.Fatal("emergency stop should not be active at -500 with threshold 1000")
	}

	// PnL at -999.99 should NOT trigger
	esm.UpdatePnL(decimal.NewFromFloat(-999.99))
	if esm.IsEmergencyStopActive() {
		t.Fatal("emergency stop should not be active at -999.99 with threshold 1000")
	}

	// PnL at -1000.01 should trigger
	esm.UpdatePnL(decimal.NewFromFloat(-1000.01))
	if !esm.IsEmergencyStopActive() {
		t.Fatal("emergency stop should be active at -1000.01 with threshold 1000")
	}

	// Verify callbacks were called
	cb.mu.Lock()
	if cb.cancelAllOrdersCalls != 1 {
		t.Errorf("expected 1 CancelAllOrders call, got %d", cb.cancelAllOrdersCalls)
	}
	if cb.stopAllStrategiesCalls != 1 {
		t.Errorf("expected 1 StopAllStrategies call, got %d", cb.stopAllStrategiesCalls)
	}
	if cb.sendCriticalAlertCalls != 1 {
		t.Errorf("expected 1 SendCriticalAlert call, got %d", cb.sendCriticalAlertCalls)
	}
	if cb.lastAlertReason == "" {
		t.Error("expected non-empty alert reason")
	}
	cb.mu.Unlock()
}

func TestEmergencyStop_ExactThresholdDoesNotTrigger(t *testing.T) {
	limits := &models.RiskLimits{
		EmergencyStopLoss: decimal.NewFromFloat(1000),
	}
	esm := NewEmergencyStopManager(limits)
	cb := &mockEmergencyCallback{}
	esm.RegisterEmergencyCallback(cb)

	// PnL at exactly -1000 should NOT trigger (must be less than -threshold)
	esm.UpdatePnL(decimal.NewFromFloat(-1000))
	if esm.IsEmergencyStopActive() {
		t.Fatal("emergency stop should not be active at exactly -1000 (requires < -1000)")
	}
}

func TestEmergencyStop_AllOperationsRejectedDuringStop(t *testing.T) {
	limits := &models.RiskLimits{
		EmergencyStopLoss: decimal.NewFromFloat(100),
	}
	esm := NewEmergencyStopManager(limits)

	// Trigger emergency stop
	esm.EmergencyStop("test trigger")

	// CheckEmergencyStop should return rejection
	decision := esm.CheckEmergencyStop()
	if decision == nil {
		t.Fatal("expected non-nil decision during emergency stop")
	}
	if decision.Approved {
		t.Fatal("expected decision to be rejected during emergency stop")
	}
	if len(decision.Reasons) == 0 {
		t.Fatal("expected at least one rejection reason")
	}

	foundEmergencyReason := false
	for _, reason := range decision.Reasons {
		if reason != "" {
			foundEmergencyReason = true
		}
	}
	if !foundEmergencyReason {
		t.Fatal("expected emergency stop reason in rejection")
	}
}

func TestEmergencyStop_ResumeClearsState(t *testing.T) {
	limits := &models.RiskLimits{
		EmergencyStopLoss: decimal.NewFromFloat(100),
	}
	esm := NewEmergencyStopManager(limits)

	// Trigger emergency stop
	esm.EmergencyStop("test trigger")
	if !esm.IsEmergencyStopActive() {
		t.Fatal("expected emergency stop to be active")
	}

	// Resume should clear the state
	err := esm.ResumeFromEmergencyStop()
	if err != nil {
		t.Fatalf("unexpected error resuming: %v", err)
	}
	if esm.IsEmergencyStopActive() {
		t.Fatal("expected emergency stop to be inactive after resume")
	}

	// CheckEmergencyStop should return nil (no rejection)
	decision := esm.CheckEmergencyStop()
	if decision != nil {
		t.Fatal("expected nil decision after resume (no rejection)")
	}

	// Reason should be cleared
	if esm.GetEmergencyStopReason() != "" {
		t.Fatalf("expected empty reason after resume, got %q", esm.GetEmergencyStopReason())
	}
}

func TestEmergencyStop_ResumeWhenNotActiveReturnsError(t *testing.T) {
	limits := &models.RiskLimits{
		EmergencyStopLoss: decimal.NewFromFloat(100),
	}
	esm := NewEmergencyStopManager(limits)

	err := esm.ResumeFromEmergencyStop()
	if err == nil {
		t.Fatal("expected error when resuming while not active")
	}
}

func TestEmergencyStop_DoubleTriggersNoOp(t *testing.T) {
	limits := &models.RiskLimits{
		EmergencyStopLoss: decimal.NewFromFloat(100),
	}
	esm := NewEmergencyStopManager(limits)
	cb := &mockEmergencyCallback{}
	esm.RegisterEmergencyCallback(cb)

	// Trigger first time
	esm.EmergencyStop("first trigger")
	// Trigger second time - should be no-op
	esm.EmergencyStop("second trigger")

	cb.mu.Lock()
	if cb.cancelAllOrdersCalls != 1 {
		t.Errorf("expected 1 CancelAllOrders call (no duplicate), got %d", cb.cancelAllOrdersCalls)
	}
	cb.mu.Unlock()

	// Reason should be from first trigger
	if esm.GetEmergencyStopReason() != "first trigger" {
		t.Errorf("expected reason 'first trigger', got %q", esm.GetEmergencyStopReason())
	}
}

func TestEmergencyStop_MultipleCallbacks(t *testing.T) {
	limits := &models.RiskLimits{
		EmergencyStopLoss: decimal.NewFromFloat(100),
	}
	esm := NewEmergencyStopManager(limits)
	cb1 := &mockEmergencyCallback{}
	cb2 := &mockEmergencyCallback{}
	esm.RegisterEmergencyCallback(cb1)
	esm.RegisterEmergencyCallback(cb2)

	esm.EmergencyStop("multi callback test")

	cb1.mu.Lock()
	if cb1.cancelAllOrdersCalls != 1 {
		t.Errorf("cb1: expected 1 CancelAllOrders call, got %d", cb1.cancelAllOrdersCalls)
	}
	if cb1.stopAllStrategiesCalls != 1 {
		t.Errorf("cb1: expected 1 StopAllStrategies call, got %d", cb1.stopAllStrategiesCalls)
	}
	if cb1.sendCriticalAlertCalls != 1 {
		t.Errorf("cb1: expected 1 SendCriticalAlert call, got %d", cb1.sendCriticalAlertCalls)
	}
	cb1.mu.Unlock()

	cb2.mu.Lock()
	if cb2.cancelAllOrdersCalls != 1 {
		t.Errorf("cb2: expected 1 CancelAllOrders call, got %d", cb2.cancelAllOrdersCalls)
	}
	if cb2.stopAllStrategiesCalls != 1 {
		t.Errorf("cb2: expected 1 StopAllStrategies call, got %d", cb2.stopAllStrategiesCalls)
	}
	if cb2.sendCriticalAlertCalls != 1 {
		t.Errorf("cb2: expected 1 SendCriticalAlert call, got %d", cb2.sendCriticalAlertCalls)
	}
	cb2.mu.Unlock()
}

func TestEmergencyStop_PnLAutoTrigger(t *testing.T) {
	limits := &models.RiskLimits{
		EmergencyStopLoss: decimal.NewFromFloat(500),
	}
	esm := NewEmergencyStopManager(limits)
	cb := &mockEmergencyCallback{}
	esm.RegisterEmergencyCallback(cb)

	// Positive PnL should not trigger
	esm.UpdatePnL(decimal.NewFromFloat(100))
	if esm.IsEmergencyStopActive() {
		t.Fatal("positive PnL should not trigger emergency stop")
	}

	// Loss within threshold should not trigger
	esm.UpdatePnL(decimal.NewFromFloat(-499))
	if esm.IsEmergencyStopActive() {
		t.Fatal("loss within threshold should not trigger emergency stop")
	}

	// Loss exceeding threshold should trigger
	esm.UpdatePnL(decimal.NewFromFloat(-501))
	if !esm.IsEmergencyStopActive() {
		t.Fatal("loss exceeding threshold should trigger emergency stop")
	}

	cb.mu.Lock()
	if cb.cancelAllOrdersCalls != 1 {
		t.Errorf("expected 1 CancelAllOrders call, got %d", cb.cancelAllOrdersCalls)
	}
	cb.mu.Unlock()
}

func TestEmergencyStop_ZeroThresholdNeverTriggers(t *testing.T) {
	limits := &models.RiskLimits{
		EmergencyStopLoss: decimal.Zero,
	}
	esm := NewEmergencyStopManager(limits)
	cb := &mockEmergencyCallback{}
	esm.RegisterEmergencyCallback(cb)

	// Even large loss should not trigger if threshold is zero (non-positive)
	esm.UpdatePnL(decimal.NewFromFloat(-99999))
	if esm.IsEmergencyStopActive() {
		t.Fatal("zero threshold should never auto-trigger emergency stop")
	}
}

func TestEmergencyStop_ContinuesRejectingAfterPnLImproves(t *testing.T) {
	limits := &models.RiskLimits{
		EmergencyStopLoss: decimal.NewFromFloat(100),
	}
	esm := NewEmergencyStopManager(limits)

	// Trigger via PnL loss
	esm.UpdatePnL(decimal.NewFromFloat(-200))
	if !esm.IsEmergencyStopActive() {
		t.Fatal("should be active after breach")
	}

	// Even if PnL improves, emergency stop stays active
	esm.UpdatePnL(decimal.NewFromFloat(50))
	if !esm.IsEmergencyStopActive() {
		t.Fatal("emergency stop should remain active even if PnL improves (requires manual confirmation)")
	}

	// Only manual resume can clear it
	decision := esm.CheckEmergencyStop()
	if decision == nil || decision.Approved {
		t.Fatal("should still reject during active emergency stop")
	}
}
