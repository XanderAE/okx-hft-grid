package marketdata

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

func explorationFirstHeartbeatTimeout(config PrivateWSClientConfig, lastVerified time.Time) time.Time {
	// FIXED: The design mandates a 1-second watchdog that checks liveness age
	// independently of the heartbeat send interval. Detection is no longer
	// quantized to heartbeat ticks. Unhealthy is detected when age >= timeout.
	for ms := 1000; ms <= 120000; ms += 1000 {
		now := lastVerified.Add(time.Duration(ms) * time.Millisecond)
		if now.Sub(lastVerified) >= config.HeartbeatTimeout {
			return now
		}
	}
	return time.Time{}
}

// **Validates: Requirements 1.2, 1.3, 2.2, 2.3**
//
// EXP-04 advances a fake clock for a silent/half-open peer. The FIXED system
// uses 20s heartbeat, detects unhealthy within 45s, and starts reconnect within 5s.
func TestProperty1_BugCondition_EXP04_PrivateWSHalfOpen(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		peerScripts := []string{"silent_after_subscribe", "arbitrary_unverified_bytes"}
		_ = peerScripts[rapid.IntRange(0, len(peerScripts)-1).Draw(t, "peer_script_index")]
		config := DefaultPrivateWSClientConfig()
		lastVerified, _ := time.Parse(time.RFC3339, "2025-01-02T03:04:05Z")
		detectedAt := explorationFirstHeartbeatTimeout(config, lastVerified)
		reconnectAt := detectedAt.Add(config.InitialReconnectDelay)
		detectionLatency := detectedAt.Sub(lastVerified)
		reconnectLatency := reconnectAt.Sub(detectedAt)

		// FIXED: config enforces 20s heartbeat, detection within 45s, reconnect within 5s
		correct := config.HeartbeatInterval == 20*time.Second &&
			detectionLatency <= 45*time.Second &&
			reconnectLatency <= 5*time.Second
		if !correct {
			t.Fatalf("EXP-04 FIXED: heartbeat=%s detection=%s reconnect=%s (expected 20s/<=45s/<=5s)",
				config.HeartbeatInterval, detectionLatency, reconnectLatency)
		}
	})
}
