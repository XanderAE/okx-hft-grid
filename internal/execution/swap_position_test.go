package execution

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// tolerance for float-rounding differences in decimal price comparisons.
var swapTol = decimal.NewFromFloat(0.01)

func assertClose(t *testing.T, got, want decimal.Decimal, msg string) {
	t.Helper()
	if got.Sub(want).Abs().LessThan(swapTol) {
		return
	}
	t.Fatalf("%s: got %s, want %s (diff %s exceeds tolerance %s)",
		msg, got.String(), want.String(), got.Sub(want).Abs().String(), swapTol.String())
}

// TestSwapPosition_LongCloseAboveEntry verifies a long close target sits above
// the entry price by the configured spread.
func TestSwapPosition_LongCloseAboveEntry(t *testing.T) {
	m := NewSwapPositionManager()
	entry := decimal.NewFromInt(64000)
	m.Open(SwapLong, entry, 1, 1)

	target, ok := m.CloseTarget(decimal.NewFromFloat(0.003), 0, entry)
	if !ok {
		t.Fatalf("CloseTarget returned ok=false with an open position")
	}
	if !target.GreaterThan(entry) {
		t.Fatalf("long close target %s should be greater than entry %s", target.String(), entry.String())
	}
	assertClose(t, target, decimal.NewFromFloat(64192), "long close target")
}

// TestSwapPosition_ShortCloseBelowEntry verifies a short close target sits below
// the entry price by the configured spread.
func TestSwapPosition_ShortCloseBelowEntry(t *testing.T) {
	m := NewSwapPositionManager()
	entry := decimal.NewFromInt(64000)
	m.Open(SwapShort, entry, 1, 1)

	target, ok := m.CloseTarget(decimal.NewFromFloat(0.003), 0, entry)
	if !ok {
		t.Fatalf("CloseTarget returned ok=false with an open position")
	}
	if !target.LessThan(entry) {
		t.Fatalf("short close target %s should be less than entry %s", target.String(), entry.String())
	}
	assertClose(t, target, decimal.NewFromFloat(63808), "short close target")
}

// TestSwapPosition_LongUnderwaterTightens verifies an underwater long clamps the
// close margin down to the fee floor.
func TestSwapPosition_LongUnderwaterTightens(t *testing.T) {
	m := NewSwapPositionManager()
	entry := decimal.NewFromInt(64000)
	m.Open(SwapLong, entry, 1, 1)

	// mark below entry => underwater => margin clamped to feeFloor 0.0005.
	target, ok := m.CloseTarget(decimal.NewFromFloat(0.003), 0, decimal.NewFromInt(63000))
	if !ok {
		t.Fatalf("CloseTarget returned ok=false with an open position")
	}
	assertClose(t, target, decimal.NewFromFloat(64032), "underwater long close target")
}

// TestSwapPosition_ShortUnderwaterTightens verifies an underwater short clamps
// the close margin down to the fee floor.
func TestSwapPosition_ShortUnderwaterTightens(t *testing.T) {
	m := NewSwapPositionManager()
	entry := decimal.NewFromInt(64000)
	m.Open(SwapShort, entry, 1, 1)

	// mark above entry => underwater for a short => margin clamped to feeFloor.
	target, ok := m.CloseTarget(decimal.NewFromFloat(0.003), 0, decimal.NewFromInt(65000))
	if !ok {
		t.Fatalf("CloseTarget returned ok=false with an open position")
	}
	assertClose(t, target, decimal.NewFromFloat(63968), "underwater short close target")
}

// TestSwapPosition_HardStopLong verifies the hard-stop trigger for a long.
func TestSwapPosition_HardStopLong(t *testing.T) {
	m := NewSwapPositionManager()
	entry := decimal.NewFromInt(64000)
	m.Open(SwapLong, entry, 1, 1)

	stopPct := decimal.NewFromFloat(0.015)

	// 2% below entry => 62720 => should stop.
	if !m.ShouldHardStop(decimal.NewFromInt(62720), stopPct) {
		t.Fatalf("expected hard stop at mark 62720 (2%% below), got false")
	}
	// 0.5% below entry => 63680 => should not stop.
	if m.ShouldHardStop(decimal.NewFromInt(63680), stopPct) {
		t.Fatalf("did not expect hard stop at mark 63680 (0.5%% below)")
	}
}

// TestSwapPosition_HardStopShort verifies the hard-stop trigger for a short.
func TestSwapPosition_HardStopShort(t *testing.T) {
	m := NewSwapPositionManager()
	entry := decimal.NewFromInt(64000)
	m.Open(SwapShort, entry, 1, 1)

	stopPct := decimal.NewFromFloat(0.015)

	// 2% above entry => 65280 => should stop.
	if !m.ShouldHardStop(decimal.NewFromInt(65280), stopPct) {
		t.Fatalf("expected hard stop at mark 65280 (2%% above), got false")
	}
}

// TestSwapPosition_ForceClose verifies a freshly opened position is not force
// closed within its max-hold window.
func TestSwapPosition_ForceClose(t *testing.T) {
	m := NewSwapPositionManager()
	m.Open(SwapLong, decimal.NewFromInt(64000), 1, 1)

	if m.ShouldForceClose(1 * time.Hour) {
		t.Fatalf("did not expect force close immediately after opening with maxHold=1h")
	}
}

// TestSwapPosition_TimeDecay verifies the close margin decays as the position
// ages, using a non-underwater mark so decay applies.
func TestSwapPosition_TimeDecay(t *testing.T) {
	m := NewSwapPositionManager()
	entry := decimal.NewFromInt(64000)
	m.Open(SwapLong, entry, 1, 1)

	spread := decimal.NewFromFloat(0.01)

	// elapsed=0 => full margin 0.01 => 64640.
	t0, ok := m.CloseTarget(spread, 0, entry)
	if !ok {
		t.Fatalf("CloseTarget ok=false at elapsed=0")
	}
	assertClose(t, t0, decimal.NewFromFloat(64640), "time decay elapsed=0")

	// elapsed=3h => margin 0.008 => 64512.
	t3, ok := m.CloseTarget(spread, 3*time.Hour, entry)
	if !ok {
		t.Fatalf("CloseTarget ok=false at elapsed=3h")
	}
	assertClose(t, t3, decimal.NewFromFloat(64512), "time decay elapsed=3h")

	// elapsed=8h => margin 0.0067 => 64428.8.
	t8, ok := m.CloseTarget(spread, 8*time.Hour, entry)
	if !ok {
		t.Fatalf("CloseTarget ok=false at elapsed=8h")
	}
	assertClose(t, t8, decimal.NewFromFloat(64428.8), "time decay elapsed=8h")
}

// **Validates: Requirements 1.2**
//
// Property — Long close margin never drops below the fee floor.
//
// For any spread in [0.0005, 0.02] and any elapsed window in [0, 24h], a long
// position closed at a non-underwater mark must produce a close target whose
// implied margin is at least the fee floor (0.0005).
func TestProperty_SwapCloseMargin_AboveFeeFloor(t *testing.T) {
	feeFloor := decimal.NewFromFloat(0.0005)
	epsilon := decimal.NewFromFloat(1e-9)

	rapid.Check(t, func(rt *rapid.T) {
		spreadF := rapid.Float64Range(0.0005, 0.02).Draw(rt, "spread")
		hours := rapid.IntRange(0, 24).Draw(rt, "elapsedHours")

		m := NewSwapPositionManager()
		entry := decimal.NewFromInt(64000)
		m.Open(SwapLong, entry, 1, 1)

		spread := decimal.NewFromFloat(spreadF)
		elapsed := time.Duration(hours) * time.Hour

		// mark == entry => not underwater, so time decay governs the margin.
		target, ok := m.CloseTarget(spread, elapsed, entry)
		if !ok {
			rt.Fatalf("CloseTarget returned ok=false with an open position")
		}

		margin := target.Sub(entry).Div(entry)
		if margin.LessThan(feeFloor.Sub(epsilon)) {
			rt.Fatalf("margin %s dropped below fee floor %s (spread=%s, elapsedHours=%d, target=%s)",
				margin.String(), feeFloor.String(), spread.String(), hours, target.String())
		}
	})
}
