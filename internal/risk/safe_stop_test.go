package risk

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

var testClock = func() time.Time {
	return time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
}

// --- TestSafeStopScope ---

func TestSafeStopScope_SymbolLocalFailureOnlyBlocksThatSymbol(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	// Enter DOGE Safe_Stop
	gate.EnterSymbolSafeStop("DOGE-USDT", ReasonCounterSellFailed, "counter sell timeout")

	// DOGE Risk_Increasing should be blocked
	decision := gate.Authorize("DOGE-USDT", RiskIncreasing)
	if decision.Allowed {
		t.Fatal("DOGE risk-increasing should be blocked when symbol safe-stopped")
	}
	if decision.Scope != models.SafeStopSymbol {
		t.Fatalf("expected symbol scope, got %v", decision.Scope)
	}

	// WIF Risk_Increasing should still be allowed (healthy peer)
	decision = gate.Authorize("WIF-USDT", RiskIncreasing)
	if !decision.Allowed {
		t.Fatalf("WIF risk-increasing should be allowed when only DOGE is safe-stopped: %s", decision.BlockReason)
	}
}

func TestSafeStopScope_RiskReducingAllowedDuringSymbolStop(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))
	gate.EnterSymbolSafeStop("DOGE-USDT", ReasonCounterSellFailed, "test")

	// Risk-reducing SELL should be allowed
	decision := gate.Authorize("DOGE-USDT", RiskReducing)
	if !decision.Allowed {
		t.Fatalf("risk-reducing should be allowed during symbol safe-stop: %s", decision.BlockReason)
	}

	// Reconciliation should be allowed
	decision = gate.Authorize("DOGE-USDT", Reconciliation)
	if !decision.Allowed {
		t.Fatalf("reconciliation should be allowed during symbol safe-stop: %s", decision.BlockReason)
	}

	// Confirmed BUY cancellation should be allowed
	decision = gate.Authorize("DOGE-USDT", ConfirmedBuyCancellation)
	if !decision.Allowed {
		t.Fatalf("confirmed buy cancellation should be allowed during symbol safe-stop: %s", decision.BlockReason)
	}
}

// --- TestOperationClassification ---

func TestOperationClassification_BuyIsAlwaysRiskIncreasing(t *testing.T) {
	class := ClassifyOperation(models.SideBuy, "initial")
	if class != RiskIncreasing {
		t.Fatalf("BUY should be risk-increasing, got %v", class)
	}

	class = ClassifyOperation(models.SideBuy, "counter-buy")
	if class != RiskIncreasing {
		t.Fatalf("counter-BUY should be risk-increasing, got %v", class)
	}

	class = ClassifyOperation(models.SideBuy, "replacement")
	if class != RiskIncreasing {
		t.Fatalf("replacement BUY should be risk-increasing, got %v", class)
	}
}

func TestOperationClassification_SellRiskReducingByPurpose(t *testing.T) {
	class := ClassifyOperation(models.SideSell, "counter-sell")
	if class != RiskReducing {
		t.Fatalf("counter-sell SELL should be risk-reducing, got %v", class)
	}

	class = ClassifyOperation(models.SideSell, "risk-reducing-sell")
	if class != RiskReducing {
		t.Fatalf("risk-reducing-sell should be risk-reducing, got %v", class)
	}

	class = ClassifyOperation(models.SideSell, "grid-sell")
	if class != RiskReducing {
		t.Fatalf("grid-sell should be risk-reducing, got %v", class)
	}
}

func TestOperationClassification_UnknownSellPurposeIsRiskIncreasing(t *testing.T) {
	class := ClassifyOperation(models.SideSell, "unknown-sell")
	if class != RiskIncreasing {
		t.Fatalf("unknown SELL purpose should default to risk-increasing, got %v", class)
	}
}

func TestOperationClassification_ReconciliationAndCancelPurpose(t *testing.T) {
	class := ClassifyOperation(models.SideBuy, "reconciliation")
	if class != Reconciliation {
		t.Fatalf("reconciliation purpose should be Reconciliation class, got %v", class)
	}

	class = ClassifyOperation(models.SideBuy, "confirmed-buy-cancel")
	if class != ConfirmedBuyCancellation {
		t.Fatalf("confirmed-buy-cancel should be ConfirmedBuyCancellation, got %v", class)
	}
}

// --- TestSharedFailureGlobal ---

func TestSharedFailureGlobal_PersistenceFailureBlocksBothSymbols(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	// Start healthy
	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})

	// Persistence becomes unhealthy
	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      false,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})

	// Both symbols should be blocked for risk-increasing
	for _, sym := range []string{"DOGE-USDT", "WIF-USDT"} {
		decision := gate.Authorize(sym, RiskIncreasing)
		if decision.Allowed {
			t.Fatalf("%s risk-increasing should be blocked on persistence failure", sym)
		}
		if decision.Scope != models.SafeStopGlobal {
			t.Fatalf("expected global scope for %s, got %v", sym, decision.Scope)
		}
	}

	// But risk-reducing should still work
	decision := gate.Authorize("DOGE-USDT", RiskReducing)
	if !decision.Allowed {
		t.Fatalf("risk-reducing should be allowed during global safe-stop: %s", decision.BlockReason)
	}
}

func TestSharedFailureGlobal_PrivateWSBlocksBothSymbols(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})

	// Private_WS becomes unhealthy
	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        false,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})

	decision := gate.Authorize("DOGE-USDT", RiskIncreasing)
	if decision.Allowed {
		t.Fatal("DOGE risk-increasing should be blocked on private-ws failure")
	}
	decision = gate.Authorize("WIF-USDT", RiskIncreasing)
	if decision.Allowed {
		t.Fatal("WIF risk-increasing should be blocked on private-ws failure")
	}
}

func TestSharedFailureGlobal_AccountReconcileBlocksBoth(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})

	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: false,
		PortfolioRiskHealthy:    true,
	})

	for _, sym := range []string{"DOGE-USDT", "WIF-USDT"} {
		decision := gate.Authorize(sym, RiskIncreasing)
		if decision.Allowed {
			t.Fatalf("%s should be blocked on account-reconcile failure", sym)
		}
	}
}

func TestSharedFailureGlobal_RecoveryAutoClears(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	// Go unhealthy
	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        false,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})
	if !gate.IsGlobalSafeStopped() {
		t.Fatal("should be globally safe-stopped")
	}

	// Recover
	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})
	if gate.IsGlobalSafeStopped() {
		t.Fatal("should no longer be globally safe-stopped after recovery")
	}

	// Both symbols should be allowed again
	for _, sym := range []string{"DOGE-USDT", "WIF-USDT"} {
		decision := gate.Authorize(sym, RiskIncreasing)
		if !decision.Allowed {
			t.Fatalf("%s should be allowed after shared recovery: %s", sym, decision.BlockReason)
		}
	}
}

// --- TestSymbolIsolation ---

func TestSymbolIsolation_DOGEFailureDoesNotStopWIF(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	// Ensure shared dependencies are healthy
	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})

	// DOGE has multiple local failures
	gate.EnterSymbolSafeStop("DOGE-USDT", ReasonCounterSellFailed, "test")
	gate.EnterSymbolSafeStop("DOGE-USDT", ReasonRebalanceTerminal, "test")

	// DOGE blocked
	decision := gate.Authorize("DOGE-USDT", RiskIncreasing)
	if decision.Allowed {
		t.Fatal("DOGE should be blocked")
	}

	// WIF unaffected
	decision = gate.Authorize("WIF-USDT", RiskIncreasing)
	if !decision.Allowed {
		t.Fatalf("WIF should not be affected by DOGE failure: %s", decision.BlockReason)
	}
}

func TestSymbolIsolation_WIFFailureDoesNotStopDOGE(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})

	gate.EnterSymbolSafeStop("WIF-USDT", ReasonInstrumentError, "stale rules")

	// WIF blocked
	decision := gate.Authorize("WIF-USDT", RiskIncreasing)
	if decision.Allowed {
		t.Fatal("WIF should be blocked")
	}

	// DOGE unaffected
	decision = gate.Authorize("DOGE-USDT", RiskIncreasing)
	if !decision.Allowed {
		t.Fatalf("DOGE should not be affected by WIF failure: %s", decision.BlockReason)
	}
}

func TestSymbolIsolation_InterleavedOperationsStayScoped(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})

	// Interleaved operations: DOGE fails, WIF still healthy
	gate.EnterSymbolSafeStop("DOGE-USDT", ReasonCounterSellFailed, "test")

	// Simulate interleaved access pattern
	if gate.Authorize("WIF-USDT", RiskIncreasing).Allowed != true {
		t.Fatal("WIF should still be allowed")
	}
	if gate.Authorize("DOGE-USDT", RiskIncreasing).Allowed != false {
		t.Fatal("DOGE should be blocked")
	}
	if gate.Authorize("WIF-USDT", RiskReducing).Allowed != true {
		t.Fatal("WIF risk-reducing should be allowed")
	}
	if gate.Authorize("DOGE-USDT", RiskReducing).Allowed != true {
		t.Fatal("DOGE risk-reducing should be allowed even during safe-stop")
	}
	if gate.Authorize("WIF-USDT", Reconciliation).Allowed != true {
		t.Fatal("WIF reconciliation should be allowed")
	}
	if gate.Authorize("DOGE-USDT", Reconciliation).Allowed != true {
		t.Fatal("DOGE reconciliation should be allowed even during safe-stop")
	}
}

// --- TestRecoveryEpoch ---

func TestRecoveryEpoch_RequiresHealthAndReconciliation(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	// Start with all healthy
	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})

	// Enter symbol safe-stop and set a recovery epoch requirement
	gate.EnterSymbolSafeStop("DOGE-USDT", ReasonCounterSellFailed, "test")
	gate.MarkReconciled("DOGE-USDT", 5) // require at least epoch 5

	// Try recover with stale epoch - should fail
	ok, reason := gate.TryRecoverSymbol("DOGE-USDT", 3)
	if ok {
		t.Fatal("recovery should fail with stale epoch")
	}
	if reason == "" {
		t.Fatal("should have a reason for failure")
	}

	// Try recover with fresh epoch - should succeed
	ok, _ = gate.TryRecoverSymbol("DOGE-USDT", 5)
	if !ok {
		t.Fatal("recovery should succeed with matching epoch and healthy dependencies")
	}

	// Symbol should now be healthy
	if gate.IsSymbolSafeStopped("DOGE-USDT") {
		t.Fatal("DOGE should be healthy after successful recovery")
	}
}

func TestRecoveryEpoch_FailsWhenSharedUnhealthy(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	// Shared unhealthy
	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      false,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})

	gate.EnterSymbolSafeStop("DOGE-USDT", ReasonCounterSellFailed, "test")

	// Recovery should fail because persistence is unhealthy
	ok, reason := gate.TryRecoverSymbol("DOGE-USDT", 100)
	if ok {
		t.Fatal("recovery should fail when shared dependency unhealthy")
	}
	if reason == "" {
		t.Fatal("should explain why recovery failed")
	}
}

func TestRecoveryEpoch_HumanRequiredBlocksAutoRecovery(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})

	// Enter with human-required reason
	gate.EnterSymbolSafeStopRequireHuman("DOGE-USDT", ReasonUnknownEffect, "unknown exchange effect")

	// Auto recovery should fail
	ok, reason := gate.TryRecoverSymbol("DOGE-USDT", 100)
	if ok {
		t.Fatal("auto recovery should be blocked when human confirmation required")
	}
	if reason == "" {
		t.Fatal("should explain human confirmation needed")
	}

	// Manual clear should work
	cleared := gate.ClearSymbolReason("DOGE-USDT", ReasonUnknownEffect, true)
	if !cleared {
		t.Fatal("manual clear with humanConfirmed=true should succeed")
	}

	if gate.IsSymbolSafeStopped("DOGE-USDT") {
		t.Fatal("should be healthy after manual clear")
	}
}

func TestRecoveryEpoch_ManualClearDeniedWithoutConfirmation(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	gate.EnterSymbolSafeStopRequireHuman("DOGE-USDT", ReasonMigrationConflict, "conflict")

	// Try without human confirmation
	cleared := gate.ClearSymbolReason("DOGE-USDT", ReasonMigrationConflict, false)
	if cleared {
		t.Fatal("should not clear human-required reason without confirmation")
	}

	if !gate.IsSymbolSafeStopped("DOGE-USDT") {
		t.Fatal("should still be safe-stopped")
	}
}

func TestRecoveryEpoch_GlobalBlocksSymbolRecovery(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})

	// Enter global and symbol stop
	gate.EnterGlobalSafeStop(ReasonPersistenceFailure, "db write failed")
	gate.EnterSymbolSafeStop("DOGE-USDT", ReasonCounterSellFailed, "test")

	// Symbol recovery blocked because global is active
	ok, reason := gate.TryRecoverSymbol("DOGE-USDT", 100)
	if ok {
		t.Fatal("symbol recovery should be blocked when global safe-stop active")
	}
	if reason == "" {
		t.Fatal("should explain global blocker")
	}
}

// --- TestEmergencyStopComposition ---

func TestEmergencyStopComposition_EmergencyStopBlocksEverything(t *testing.T) {
	limits := &models.RiskLimits{EmergencyStopLoss: decimal.NewFromFloat(1000)}
	esm := NewEmergencyStopManager(limits)
	gate := NewTradingGate(WithTradingGateClock(testClock), WithEmergencyStop(esm))

	// Trigger legacy emergency stop
	esm.EmergencyStop("portfolio PnL breach")

	// All operations blocked (including risk-reducing!) because emergency stop
	// is the nuclear option
	decision := gate.Authorize("DOGE-USDT", RiskIncreasing)
	if decision.Allowed {
		t.Fatal("risk-increasing should be blocked by emergency stop")
	}
	if decision.Scope != models.SafeStopGlobal {
		t.Fatalf("expected global scope, got %v", decision.Scope)
	}

	// Reconciliation still allowed (always allowed)
	decision = gate.Authorize("DOGE-USDT", Reconciliation)
	if !decision.Allowed {
		t.Fatal("reconciliation should always be allowed even during emergency stop")
	}

	// Confirmed BUY cancellation still allowed
	decision = gate.Authorize("DOGE-USDT", ConfirmedBuyCancellation)
	if !decision.Allowed {
		t.Fatal("confirmed buy cancellation should always be allowed")
	}
}

func TestEmergencyStopComposition_ScopeDoesNotReplaceEmergency(t *testing.T) {
	limits := &models.RiskLimits{EmergencyStopLoss: decimal.NewFromFloat(1000)}
	esm := NewEmergencyStopManager(limits)
	gate := NewTradingGate(WithTradingGateClock(testClock), WithEmergencyStop(esm))

	// Without emergency: gate allows
	decision := gate.Authorize("DOGE-USDT", RiskIncreasing)
	if !decision.Allowed {
		t.Fatal("should be allowed when no stops active")
	}

	// Enter scoped safe-stop: only DOGE blocked
	gate.EnterSymbolSafeStop("DOGE-USDT", ReasonCounterSellFailed, "test")
	decision = gate.Authorize("WIF-USDT", RiskIncreasing)
	if !decision.Allowed {
		t.Fatal("WIF should be allowed with only DOGE scoped stop")
	}

	// Now trigger emergency: both blocked regardless of scoped state
	esm.EmergencyStop("test emergency")
	decision = gate.Authorize("WIF-USDT", RiskIncreasing)
	if decision.Allowed {
		t.Fatal("WIF should be blocked by emergency stop")
	}
	decision = gate.Authorize("DOGE-USDT", RiskIncreasing)
	if decision.Allowed {
		t.Fatal("DOGE should be blocked by emergency stop (not just scoped)")
	}
}

// --- Additional edge cases ---

func TestSafeStopScope_MultipleReasonsClearOneByOne(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})

	gate.EnterSymbolSafeStop("DOGE-USDT", ReasonCounterSellFailed, "a")
	gate.EnterSymbolSafeStop("DOGE-USDT", ReasonStaleTicker, "b")

	if !gate.IsSymbolSafeStopped("DOGE-USDT") {
		t.Fatal("should be safe-stopped with two reasons")
	}

	// Clear one reason
	gate.ClearSymbolReason("DOGE-USDT", ReasonCounterSellFailed, false)
	if !gate.IsSymbolSafeStopped("DOGE-USDT") {
		t.Fatal("should still be safe-stopped with one reason remaining")
	}

	// Verify still blocked
	decision := gate.Authorize("DOGE-USDT", RiskIncreasing)
	if decision.Allowed {
		t.Fatal("should still be blocked with one remaining reason")
	}

	// Clear last reason
	gate.ClearSymbolReason("DOGE-USDT", ReasonStaleTicker, false)
	if gate.IsSymbolSafeStopped("DOGE-USDT") {
		t.Fatal("should not be safe-stopped after all reasons cleared")
	}

	// Now allowed
	decision = gate.Authorize("DOGE-USDT", RiskIncreasing)
	if !decision.Allowed {
		t.Fatalf("should be allowed after all reasons cleared: %s", decision.BlockReason)
	}
}

func TestSafeStopScope_SnapshotReflectsState(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        false,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})
	gate.EnterSymbolSafeStop("DOGE-USDT", ReasonCounterSellFailed, "test detail")

	snap := gate.Snapshot()

	if !snap.GlobalSafeStopped {
		t.Fatal("snapshot should show global safe-stopped")
	}
	if _, ok := snap.GlobalReasons[ReasonPrivateWSUncertain]; !ok {
		t.Fatal("snapshot should contain private-ws reason")
	}
	if ss, ok := snap.SymbolStates["DOGE-USDT"]; !ok || !ss.Active {
		t.Fatal("snapshot should show DOGE as active safe-stopped")
	}
	if !snap.SharedHealth.PersistenceHealthy {
		t.Fatal("snapshot should reflect persistence healthy")
	}
	if snap.SharedHealth.PrivateWSHealthy {
		t.Fatal("snapshot should reflect private-ws unhealthy")
	}
}

func TestSafeStopScope_KnownPositionTracking(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	gate.UpdateKnownPosition("DOGE-USDT", decimal.NewFromFloat(1000))
	gate.UpdateKnownPosition("WIF-USDT", decimal.NewFromFloat(500))

	if !gate.KnownPosition("DOGE-USDT").Equal(decimal.NewFromFloat(1000)) {
		t.Fatal("DOGE position should be 1000")
	}
	if !gate.KnownPosition("WIF-USDT").Equal(decimal.NewFromFloat(500)) {
		t.Fatal("WIF position should be 500")
	}
	if !gate.KnownPosition("BTC-USDT").IsZero() {
		t.Fatal("unknown symbol position should be zero")
	}
}

func TestSafeStopScope_ConcurrentAccess(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	gate.UpdateSharedHealth(SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})

	// Run concurrent operations
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			sym := "DOGE-USDT"
			if id%2 == 0 {
				sym = "WIF-USDT"
			}
			gate.Authorize(sym, RiskIncreasing)
			gate.Authorize(sym, RiskReducing)
			gate.Authorize(sym, Reconciliation)
			gate.EnterSymbolSafeStop(sym, ReasonStaleTicker, "concurrent test")
			gate.ClearSymbolReason(sym, ReasonStaleTicker, false)
			gate.IsSymbolSafeStopped(sym)
			gate.Snapshot()
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestSafeStopScope_GlobalReasonCodes(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	gate.EnterGlobalSafeStop(ReasonPersistenceFailure, "db fail")
	gate.EnterGlobalSafeStop(ReasonPrivateWSUncertain, "ws fail")

	reasons := gate.GlobalReasons()
	if len(reasons) != 2 {
		t.Fatalf("expected 2 global reasons, got %d", len(reasons))
	}

	// Clear one
	gate.ClearGlobalReason(ReasonPersistenceFailure, false)
	reasons = gate.GlobalReasons()
	if len(reasons) != 1 {
		t.Fatalf("expected 1 global reason after clear, got %d", len(reasons))
	}
}

func TestSafeStopScope_GlobalHumanRequired(t *testing.T) {
	gate := NewTradingGate(WithTradingGateClock(testClock))

	gate.EnterGlobalSafeStopRequireHuman(ReasonMigrationConflict, "conflict")

	// Cannot clear without human
	cleared := gate.ClearGlobalReason(ReasonMigrationConflict, false)
	if cleared {
		t.Fatal("should not clear human-required global reason without confirmation")
	}

	// Can clear with human
	cleared = gate.ClearGlobalReason(ReasonMigrationConflict, true)
	if !cleared {
		t.Fatal("should clear with human confirmation")
	}

	if gate.IsGlobalSafeStopped() {
		t.Fatal("should not be globally stopped after human clear")
	}
}
