package monitor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// --- TestRequiredMetrics ---

func TestRequiredMetrics(t *testing.T) {
	port := getFreePort()
	ms := NewMetricsServer(port)
	if err := ms.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer ms.Stop()
	time.Sleep(100 * time.Millisecond)

	// Exercise all production grid metrics
	ms.SetServiceUp(true)
	ms.SetHealthState("healthy")
	ms.SetStateDirectoryWritable(true)
	ms.SetPrivateWSStateMetric("ready")
	ms.SetPrivateWSLivenessAge(3.5)
	ms.IncrementPrivateWSReconnect()
	ms.SetPrivateWSSubscriptionReady(true)
	ms.SetReconciliationLastSuccess(float64(time.Now().Unix()))
	ms.SetReconciliationLag(12.5)
	ms.RecordReconciliationDuration(2.1)
	ms.RecordReconciliationOutcome("DOGE-USDT", "success")
	ms.IncrementReconciliationChecked(15)
	ms.IncrementReconciliationDiffs(2)
	ms.IncrementReconciliationCompensated(1)
	ms.IncrementFillDuplicatesSuppressed()
	ms.IncrementFillGaps()
	ms.SetOutboxBacklog(3)
	ms.IncrementOutboxLeaseRecovery()
	ms.RecordCounterSellInitiationLatency(1.2)
	ms.RecordCounterSellOutcome("DOGE-USDT", "confirmed")
	ms.SetRebalancerLastRun(float64(time.Now().Unix()))
	ms.RecordRebalancerJitter(0.5)
	ms.SetRebalancerRefAge(2.3)
	ms.SetRebalancerStaleCount(1)
	ms.RecordRebalancerOutcome("WIF-USDT", "replaced")
	ms.SetSafeStopActive("symbol", "counter-sell-terminal-failure", true)
	ms.IncrementAlertAttempts()
	ms.RecordAlertDeliveryOutcome("telegram", "success")

	body := getMetricsBodyObs(t, port)

	// All required metrics must appear in output
	requiredMetrics := []string{
		"grid_service_up",
		"grid_health_state",
		"grid_state_directory_writable",
		"grid_private_ws_state",
		"grid_private_ws_liveness_age_seconds",
		"grid_private_ws_reconnect_total",
		"grid_private_ws_subscription_ready",
		"grid_reconciliation_last_success_timestamp",
		"grid_reconciliation_lag_seconds",
		"grid_reconciliation_duration_seconds",
		"grid_reconciliation_outcome_total",
		"grid_reconciliation_checked_total",
		"grid_reconciliation_diffs_total",
		"grid_reconciliation_compensated_total",
		"grid_fill_duplicates_suppressed_total",
		"grid_fill_gaps_total",
		"grid_outbox_backlog",
		"grid_outbox_lease_recovery_total",
		"grid_counter_sell_initiation_latency_seconds",
		"grid_counter_sell_outcome_total",
		"grid_rebalancer_last_run_timestamp",
		"grid_rebalancer_jitter_seconds",
		"grid_rebalancer_reference_age_seconds",
		"grid_rebalancer_stale_orders",
		"grid_rebalancer_outcome_total",
		"grid_safe_stop_active",
		"grid_alert_attempts_total",
		"grid_alert_delivery_outcome_total",
	}

	for _, metric := range requiredMetrics {
		if !strings.Contains(body, metric) {
			t.Errorf("required metric %q missing from /metrics output", metric)
		}
	}

	// Verify bounded labels are used (symbol, scope, reason_code, outcome, state)
	// and order/fill IDs are NOT labels
	if strings.Contains(body, `order_id="`) || strings.Contains(body, `fill_id="`) {
		t.Error("order/fill ID found as a Prometheus label - these must not be used as labels")
	}

	// Verify bounded label values appear correctly
	if !strings.Contains(body, `symbol="DOGE-USDT"`) {
		t.Error("bounded symbol label 'DOGE-USDT' not found")
	}
	if !strings.Contains(body, `scope="symbol"`) {
		t.Error("bounded scope label not found")
	}
	if !strings.Contains(body, `reason_code="counter-sell-terminal-failure"`) {
		t.Error("bounded reason_code label not found")
	}
}

// --- TestStructuredSanitization ---

func TestStructuredSanitization(t *testing.T) {
	// The logger's key denylist must redact values for sensitive field names
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)

	// Test with denylist keys - values must be [REDACTED] regardless of content
	sensitiveKeys := []string{
		"authorization",
		"api_key",
		"secret_key",
		"passphrase",
		"signature",
		"secret",
		"env_dump",
	}

	for _, key := range sensitiveKeys {
		buf.Reset()
		logger.LogInfo("test operation", map[string]string{
			key:      "some-plaintext-value-12345",
			"symbol": "DOGE-USDT",
		})

		output := buf.String()
		if strings.Contains(output, "some-plaintext-value-12345") {
			t.Errorf("sensitive key %q: value leaked through denylist: %s", key, output)
		}

		var entry map[string]interface{}
		if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
			t.Fatalf("key %q: invalid JSON: %v", key, err)
		}

		extra := entry["extra"].(map[string]interface{})
		if extra[key] != "[REDACTED]" {
			t.Errorf("key %q: expected [REDACTED], got %v", key, extra[key])
		}
		// Non-sensitive fields must be preserved
		if extra["symbol"] != "DOGE-USDT" {
			t.Errorf("key %q: non-sensitive field 'symbol' was corrupted: %v", key, extra["symbol"])
		}
	}

	// Verify case-insensitive matching
	buf.Reset()
	logger.LogInfo("test", map[string]string{
		"Authorization": "Bearer token123",
		"API_KEY":       "my-key",
	})
	output := buf.String()
	if strings.Contains(output, "Bearer token123") || strings.Contains(output, "my-key") {
		t.Errorf("case-insensitive denylist failed: %s", output)
	}

	// Verify trade action also applies denylist
	buf.Reset()
	logger.LogTradeAction(TradeAction{
		Timestamp:  time.Now().UTC(),
		ActionType: "COUNTER_SELL",
		Instrument: "DOGE-USDT",
		Quantity:   "100",
		Price:      "0.45",
		OrderID:    "sim-order-1",
		Result:     "confirmed",
		Extra: map[string]string{
			"signature":  "hmac-sha256-value",
			"passphrase": "my-secret-pass",
			"symbol":     "DOGE-USDT",
		},
	})
	output = buf.String()
	if strings.Contains(output, "hmac-sha256-value") || strings.Contains(output, "my-secret-pass") {
		t.Errorf("trade action denylist failed: %s", output)
	}
	if !strings.Contains(output, "DOGE-USDT") {
		t.Error("non-sensitive field lost in trade action")
	}
}

// --- TestJournaldFirstAlert ---

func TestJournaldFirstAlert(t *testing.T) {
	// Alert flow: synchronously write alert_raised to journal BEFORE external delivery
	// Regardless of external success or failure, journal evidence must exist.

	for _, tc := range []struct {
		name         string
		channelFail  bool
		hasChannels  bool
	}{
		{"external_success", false, true},
		{"external_failure", true, true},
		{"no_channels_configured", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var journal bytes.Buffer
			logger := NewStructuredLogger(&journal)

			var channels []AlertChannel
			if tc.hasChannels {
				ch := &testAlertChannel{fail: tc.channelFail}
				channels = []AlertChannel{ch}
			}

			alerter := NewAlerter(channels, logger)
			alerter.retries = 0
			alerter.retryInterval = 0

			_ = alerter.SendAlert(Alert{
				Level:     CRITICAL,
				Message:   "Counter_SELL terminal failure",
				Timestamp: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
				Extra:     map[string]string{"symbol": "WIF-USDT"},
			})

			output := journal.String()

			// Mandatory: alert_raised must ALWAYS exist in journal
			if !strings.Contains(output, "alert_raised") {
				t.Fatalf("journal evidence 'alert_raised' missing - journald-first requirement violated.\nOutput: %s", output)
			}

			// alert_delivery outcome must be recorded
			if !strings.Contains(output, "alert_delivery") {
				t.Fatalf("alert_delivery log missing.\nOutput: %s", output)
			}

			// Verify the content is sanitized (no raw credential patterns)
			if strings.Contains(output, "api_key=") || strings.Contains(output, "secret_key=") {
				t.Fatalf("unsanitized credential in alert journal output: %s", output)
			}
		})
	}
}

// --- TestExternalAlertFailure ---

func TestExternalAlertFailure(t *testing.T) {
	// External delivery failure must NOT suppress or remove journal evidence.
	var journal bytes.Buffer
	logger := NewStructuredLogger(&journal)

	failCh := &testAlertChannel{fail: true, name: "telegram-test"}
	alerter := NewAlerter([]AlertChannel{failCh}, logger)
	alerter.retries = 2
	alerter.retryInterval = time.Millisecond

	err := alerter.SendAlert(Alert{
		Level:   CRITICAL,
		Message: "persistence write failure",
		Extra:   map[string]string{"symbol": "DOGE-USDT"},
	})

	// Error returned (all channels failed)
	if err == nil {
		t.Fatal("expected error when all channels fail")
	}

	output := journal.String()

	// Journal evidence must still exist
	if !strings.Contains(output, "alert_raised") {
		t.Fatalf("journal alert_raised evidence suppressed by external failure: %s", output)
	}

	// Failure delivery record must exist
	if !strings.Contains(output, "alert_delivery") {
		t.Fatalf("alert_delivery failure record missing: %s", output)
	}
	if !strings.Contains(output, "failure") {
		t.Fatalf("delivery failure result not recorded: %s", output)
	}

	// Channel name must appear for traceability
	if !strings.Contains(output, "telegram-test") {
		t.Fatalf("channel name not in delivery record: %s", output)
	}
}

// --- TestFillToTerminalCorrelation ---

func TestFillToTerminalCorrelation(t *testing.T) {
	// Verify that health/log/metrics can correlate a BUY fill to a unique
	// Counter_SELL or safe terminal. We test the observability path only.

	var journal bytes.Buffer
	logger := NewStructuredLogger(&journal)

	// Simulate the fill → Counter_SELL lifecycle through structured logs
	correlationID := "fill-corr-abc123"
	symbol := "DOGE-USDT"

	// 1. Fill observed
	logger.LogInfo("fill_observed", map[string]string{
		"symbol":         symbol,
		"correlation_id": correlationID,
		"side":           "BUY",
		"delta_qty":      "100",
		"source":         "ws",
	})

	// 2. Counter_SELL intent created
	logger.LogInfo("counter_sell_intent_created", map[string]string{
		"symbol":         symbol,
		"correlation_id": correlationID,
		"quantity":       "100",
		"price":         "0.45",
	})

	// 3. Counter_SELL confirmed
	logger.LogInfo("counter_sell_confirmed", map[string]string{
		"symbol":         symbol,
		"correlation_id": correlationID,
		"outcome":        "confirmed",
	})

	output := journal.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	// Verify correlation chain exists
	fillObserved := false
	intentCreated := false
	confirmed := false

	for _, line := range lines {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid JSON line: %v", err)
		}
		msg, _ := entry["msg"].(string)
		extra, _ := entry["extra"].(map[string]interface{})
		if extra == nil {
			continue
		}
		cid, _ := extra["correlation_id"].(string)
		if cid != correlationID {
			continue
		}
		switch msg {
		case "fill_observed":
			fillObserved = true
			if extra["symbol"] != symbol || extra["side"] != "BUY" {
				t.Fatalf("fill_observed missing required fields: %v", extra)
			}
		case "counter_sell_intent_created":
			intentCreated = true
		case "counter_sell_confirmed":
			confirmed = true
			if extra["outcome"] != "confirmed" {
				t.Fatalf("counter_sell_confirmed missing outcome: %v", extra)
			}
		}
	}

	if !fillObserved {
		t.Fatal("fill_observed event not found in correlation chain")
	}
	if !intentCreated {
		t.Fatal("counter_sell_intent_created not found in correlation chain")
	}
	if !confirmed {
		t.Fatal("counter_sell_confirmed not found in correlation chain")
	}

	// Also test the safe-failure path
	journal.Reset()
	logger.LogInfo("fill_observed", map[string]string{
		"symbol":         symbol,
		"correlation_id": "fill-corr-xyz999",
		"side":           "BUY",
		"delta_qty":      "50",
		"source":         "rest",
	})
	logger.LogInfo("counter_sell_safe_failed", map[string]string{
		"symbol":         symbol,
		"correlation_id": "fill-corr-xyz999",
		"outcome":        "safe_failed",
		"error_class":    "definitive-business-reject",
	})

	output = journal.String()
	if !strings.Contains(output, "fill-corr-xyz999") {
		t.Fatal("safe-failure correlation chain missing")
	}
	if !strings.Contains(output, "safe_failed") {
		t.Fatal("safe_failed outcome not traceable")
	}
}

// --- helper types ---

type testAlertChannel struct {
	fail bool
	name string
}

func (c *testAlertChannel) Send(Alert) error {
	if c.fail {
		return errors.New("simulated delivery failure")
	}
	return nil
}

func (c *testAlertChannel) Name() string {
	if c.name != "" {
		return c.name
	}
	return "test-channel"
}

func getMetricsBodyObs(t *testing.T, port int) string {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/metrics", port))
	if err != nil {
		t.Fatalf("GET /metrics error: %v", err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	return string(b)
}
