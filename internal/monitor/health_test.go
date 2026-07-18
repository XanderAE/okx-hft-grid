package monitor

import (
	"encoding/json"
	"testing"
	"time"
)

func TestHealthStates_HealthyWhenAllGreen(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	h := NewHealthRegistry("aws-ap-southeast-1-singapore", WithHealthClock(func() time.Time { return now }))
	h.SetStateDirectoryWritable(true)
	h.SetPrivateWSState("ready", now.Add(-5*time.Second), 0, true)
	h.SetReconciliationSuccess(now.Add(-10 * time.Second))
	h.SetSymbolState("DOGE-USDT", HealthStateHealthy, nil)
	h.SetSymbolState("WIF-USDT", HealthStateHealthy, nil)

	if got := h.State(); got != HealthStateHealthy {
		t.Fatalf("expected %q, got %q", HealthStateHealthy, got)
	}

	snap := h.Snapshot()
	if snap.State != HealthStateHealthy {
		t.Fatalf("snapshot state = %q, want %q", snap.State, HealthStateHealthy)
	}
	if snap.Location != "aws-ap-southeast-1-singapore" {
		t.Fatalf("location = %q, want aws-ap-southeast-1-singapore", snap.Location)
	}
	if !snap.StateDirectoryWritable {
		t.Fatal("state_directory_writable should be true")
	}
	if snap.PrivateWS.State != "ready" {
		t.Fatalf("private_ws.state = %q, want ready", snap.PrivateWS.State)
	}
	if snap.PrivateWS.Reconnects != 0 {
		t.Fatalf("private_ws.reconnects = %d, want 0", snap.PrivateWS.Reconnects)
	}
}

func TestHealthStates_DegradedWhenReconciliationLag(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	h := NewHealthRegistry("aws-ap-southeast-1-singapore", WithHealthClock(func() time.Time { return now }))
	h.SetStateDirectoryWritable(true)
	h.SetPrivateWSState("ready", now.Add(-5*time.Second), 0, true)
	// Last reconciliation was 35 seconds ago (> 30s interval)
	h.SetReconciliationSuccess(now.Add(-35 * time.Second))
	h.SetSymbolState("DOGE-USDT", HealthStateHealthy, nil)

	if got := h.State(); got != HealthStateDegraded {
		t.Fatalf("expected %q due to reconciliation lag, got %q", HealthStateDegraded, got)
	}
}

func TestHealthStates_DegradedWhenPrivateWSNotReady(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	h := NewHealthRegistry("aws-ap-southeast-1-singapore", WithHealthClock(func() time.Time { return now }))
	h.SetStateDirectoryWritable(true)
	h.SetPrivateWSState("reconnecting", now.Add(-20*time.Second), 2, false)
	h.SetReconciliationSuccess(now.Add(-10 * time.Second))

	if got := h.State(); got != HealthStateDegraded {
		t.Fatalf("expected %q due to private_ws not ready, got %q", HealthStateDegraded, got)
	}
}

func TestHealthStates_SafeStoppedWhenStateDirectoryNotWritable(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	h := NewHealthRegistry("aws-ap-southeast-1-singapore", WithHealthClock(func() time.Time { return now }))
	h.SetStateDirectoryWritable(false)
	h.SetPrivateWSState("ready", now.Add(-5*time.Second), 0, true)
	h.SetReconciliationSuccess(now.Add(-10 * time.Second))

	if got := h.State(); got != HealthStateSafeStopped {
		t.Fatalf("expected %q due to non-writable state dir, got %q", HealthStateSafeStopped, got)
	}
}

func TestHealthStates_SafeStoppedWhenSymbolSafeStopped(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	h := NewHealthRegistry("aws-ap-southeast-1-singapore", WithHealthClock(func() time.Time { return now }))
	h.SetStateDirectoryWritable(true)
	h.SetPrivateWSState("ready", now.Add(-5*time.Second), 0, true)
	h.SetReconciliationSuccess(now.Add(-10 * time.Second))
	h.SetSymbolState("WIF-USDT", HealthStateSafeStopped, []string{"counter-sell-terminal-failure"})

	if got := h.State(); got != HealthStateSafeStopped {
		t.Fatalf("expected %q due to symbol safe-stop, got %q", HealthStateSafeStopped, got)
	}
}

func TestHealthStates_SafeStoppedWhenScopeActive(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	h := NewHealthRegistry("aws-ap-southeast-1-singapore", WithHealthClock(func() time.Time { return now }))
	h.SetStateDirectoryWritable(true)
	h.SetPrivateWSState("ready", now.Add(-5*time.Second), 0, true)
	h.SetReconciliationSuccess(now.Add(-10 * time.Second))
	h.SetSafeStopScope("global", []string{"persistence-write-failure"})

	if got := h.State(); got != HealthStateSafeStopped {
		t.Fatalf("expected %q due to scope safe-stop, got %q", HealthStateSafeStopped, got)
	}
}

func TestHealthStates_DegradedWhenNeverReconciled(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	h := NewHealthRegistry("aws-ap-southeast-1-singapore", WithHealthClock(func() time.Time { return now }))
	h.SetStateDirectoryWritable(true)
	h.SetPrivateWSState("ready", now.Add(-5*time.Second), 0, true)
	// Never reconciled

	if got := h.State(); got != HealthStateDegraded {
		t.Fatalf("expected %q (never reconciled, service expected), got %q", HealthStateDegraded, got)
	}
}

func TestHealthStates_JSONOutput(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 0, 30, 0, time.UTC)
	h := NewHealthRegistry("aws-ap-southeast-1-singapore", WithHealthClock(func() time.Time { return now }))
	h.SetStateDirectoryWritable(true)
	h.SetPrivateWSState("ready", now.Add(-2*time.Second), 1, true)
	h.SetReconciliationSuccess(now.Add(-15 * time.Second))
	h.SetSymbolState("DOGE-USDT", HealthStateHealthy, nil)
	h.SetSymbolState("WIF-USDT", HealthStateHealthy, nil)

	data, err := h.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	var snap HealthSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if snap.State != HealthStateHealthy {
		t.Fatalf("state = %q, want healthy", snap.State)
	}
	if snap.PrivateWS.LastLivenessAgeMs != 2000 {
		t.Fatalf("last_liveness_age_ms = %d, want 2000", snap.PrivateWS.LastLivenessAgeMs)
	}
	if snap.Reconciliation.LagMs != 15000 {
		t.Fatalf("reconciliation lag_ms = %d, want 15000", snap.Reconciliation.LagMs)
	}
}

func TestHealthStates_FixedOutputStrings(t *testing.T) {
	// The three fixed output strings must be exactly these
	if HealthStateHealthy != "healthy" {
		t.Fatalf("HealthStateHealthy = %q, want 'healthy'", HealthStateHealthy)
	}
	if HealthStateDegraded != "degraded/reconciling" {
		t.Fatalf("HealthStateDegraded = %q, want 'degraded/reconciling'", HealthStateDegraded)
	}
	if HealthStateSafeStopped != "safe-stopped" {
		t.Fatalf("HealthStateSafeStopped = %q, want 'safe-stopped'", HealthStateSafeStopped)
	}
}
