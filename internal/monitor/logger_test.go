package monitor

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewStructuredLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)
	if logger == nil {
		t.Fatal("NewStructuredLogger returned nil")
	}
	if logger.output != &buf {
		t.Error("output writer not set correctly")
	}
}

func TestNewStructuredLoggerNilOutput(t *testing.T) {
	logger := NewStructuredLogger(nil)
	if logger == nil {
		t.Fatal("NewStructuredLogger returned nil for nil output")
	}
	// Should not panic when logging
	logger.LogInfo("test", nil)
}

func TestLogTradeAction_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)

	ts := time.Date(2024, 6, 15, 10, 30, 45, 123000000, time.UTC)
	action := TradeAction{
		Timestamp:  ts,
		ActionType: "PLACE_ORDER",
		Instrument: "BTC-USDT",
		Quantity:   "0.5",
		Price:      "65000.00",
		OrderID:    "ord-12345",
		Result:     "SUCCESS",
	}

	logger.LogTradeAction(action)

	// Parse the JSON output
	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw: %s", err, buf.String())
	}

	// Verify required fields
	if entry["timestamp"] != "2024-06-15T10:30:45.123Z" {
		t.Errorf("timestamp = %v, want 2024-06-15T10:30:45.123Z", entry["timestamp"])
	}
	if entry["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", entry["level"])
	}
	if entry["action_type"] != "PLACE_ORDER" {
		t.Errorf("action_type = %v, want PLACE_ORDER", entry["action_type"])
	}
	if entry["instrument"] != "BTC-USDT" {
		t.Errorf("instrument = %v, want BTC-USDT", entry["instrument"])
	}
	if entry["quantity"] != "0.5" {
		t.Errorf("quantity = %v, want 0.5", entry["quantity"])
	}
	if entry["price"] != "65000.00" {
		t.Errorf("price = %v, want 65000.00", entry["price"])
	}
	if entry["order_id"] != "ord-12345" {
		t.Errorf("order_id = %v, want ord-12345", entry["order_id"])
	}
	if entry["result"] != "SUCCESS" {
		t.Errorf("result = %v, want SUCCESS", entry["result"])
	}
}

func TestLogTradeAction_MillisecondPrecision(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)

	ts := time.Date(2024, 1, 1, 0, 0, 0, 789000000, time.UTC)
	action := TradeAction{
		Timestamp:  ts,
		ActionType: "CANCEL",
		Instrument: "ETH-USDT",
		Quantity:   "1.0",
		Price:      "3200.50",
		OrderID:    "ord-999",
		Result:     "CANCELLED",
	}

	logger.LogTradeAction(action)

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	tsStr, ok := entry["timestamp"].(string)
	if !ok {
		t.Fatal("timestamp is not a string")
	}
	// Verify millisecond precision - should contain .789
	if !strings.Contains(tsStr, ".789") {
		t.Errorf("timestamp %q does not have millisecond precision .789", tsStr)
	}
}

func TestLogTradeAction_ExtraFields(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)

	action := TradeAction{
		Timestamp:  time.Now().UTC(),
		ActionType: "FILL",
		Instrument: "SOL-USDT",
		Quantity:   "10",
		Price:      "150.25",
		OrderID:    "ord-abc",
		Result:     "FILLED",
		Extra: map[string]string{
			"fee":  "0.01",
			"side": "BUY",
		},
	}

	logger.LogTradeAction(action)

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	extra, ok := entry["extra"].(map[string]interface{})
	if !ok {
		t.Fatal("extra field is not a map")
	}
	if extra["fee"] != "0.01" {
		t.Errorf("extra.fee = %v, want 0.01", extra["fee"])
	}
	if extra["side"] != "BUY" {
		t.Errorf("extra.side = %v, want BUY", extra["side"])
	}
}

func TestLogTradeAction_DefaultTimestamp(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)

	before := time.Now().UTC()
	action := TradeAction{
		ActionType: "PLACE_ORDER",
		Instrument: "BTC-USDT",
		Quantity:   "1",
		Price:      "50000",
		OrderID:    "ord-1",
		Result:     "OK",
	}
	logger.LogTradeAction(action)
	after := time.Now().UTC()

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	tsStr := entry["timestamp"].(string)
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z07:00", tsStr)
	if err != nil {
		t.Fatalf("cannot parse timestamp %q: %v", tsStr, err)
	}

	if parsed.Before(before.Truncate(time.Millisecond)) || parsed.After(after.Add(time.Millisecond)) {
		t.Errorf("timestamp %v is not between %v and %v", parsed, before, after)
	}
}

func TestLogInfo_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)

	logger.LogInfo("system started", map[string]string{"version": "1.0.0"})

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	if entry["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", entry["level"])
	}
	if entry["msg"] != "system started" {
		t.Errorf("msg = %v, want 'system started'", entry["msg"])
	}
	extra := entry["extra"].(map[string]interface{})
	if extra["version"] != "1.0.0" {
		t.Errorf("extra.version = %v, want 1.0.0", extra["version"])
	}
}

func TestLogWarn_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)

	logger.LogWarn("high latency detected", map[string]string{"latency_ms": "5"})

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	if entry["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", entry["level"])
	}
	if entry["msg"] != "high latency detected" {
		t.Errorf("msg = %v, want 'high latency detected'", entry["msg"])
	}
}

func TestLogError_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)

	logger.LogError("connection failed", map[string]string{"reason": "timeout"})

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	if entry["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", entry["level"])
	}
	if entry["msg"] != "connection failed" {
		t.Errorf("msg = %v, want 'connection failed'", entry["msg"])
	}
}

func TestLogWithNilFields(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)

	logger.LogInfo("simple message", nil)

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	if entry["msg"] != "simple message" {
		t.Errorf("msg = %v, want 'simple message'", entry["msg"])
	}
	// extra should not be present when nil fields
	if _, exists := entry["extra"]; exists {
		t.Error("extra field should not be present when fields is nil")
	}
}

func TestSanitizer_TradeAction(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)

	// Set up a sanitizer that replaces "secret123" with "********"
	logger.SetSanitizer(func(s string) string {
		return strings.ReplaceAll(s, "secret123", "********")
	})

	action := TradeAction{
		Timestamp:  time.Now().UTC(),
		ActionType: "PLACE_ORDER",
		Instrument: "BTC-USDT",
		Quantity:   "1",
		Price:      "50000",
		OrderID:    "ord-secret123-abc",
		Result:     "SUCCESS",
		Extra: map[string]string{
			"api_key": "secret123",
		},
	}

	logger.LogTradeAction(action)

	output := buf.String()
	if strings.Contains(output, "secret123") {
		t.Errorf("output contains unsanitized value 'secret123': %s", output)
	}

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	if entry["order_id"] != "ord-********-abc" {
		t.Errorf("order_id = %v, want ord-********-abc", entry["order_id"])
	}
	extra := entry["extra"].(map[string]interface{})
	if extra["api_key"] != "********" {
		t.Errorf("extra.api_key = %v, want ********", extra["api_key"])
	}
}

func TestSanitizer_LogMessages(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)

	logger.SetSanitizer(func(s string) string {
		return strings.ReplaceAll(s, "my-api-key-value", "********")
	})

	logger.LogWarn("failed with key my-api-key-value", map[string]string{
		"key": "my-api-key-value",
	})

	output := buf.String()
	if strings.Contains(output, "my-api-key-value") {
		t.Errorf("output contains unsanitized credential: %s", output)
	}

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	if !strings.Contains(entry["msg"].(string), "********") {
		t.Errorf("message not sanitized: %s", entry["msg"])
	}
}

func TestSetSanitizer_NilIsIgnored(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)

	// Set a sanitizer first
	logger.SetSanitizer(func(s string) string {
		return strings.ReplaceAll(s, "secret", "********")
	})

	// Setting nil should not replace the existing sanitizer
	logger.SetSanitizer(nil)

	logger.LogInfo("secret message", nil)

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	// Sanitizer should still be active (nil didn't replace it)
	if entry["msg"] != "******** message" {
		t.Errorf("msg = %v, want '******** message' (sanitizer should still be active)", entry["msg"])
	}
}

func TestOutputIsOneLinePerEntry(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)

	logger.LogInfo("first", nil)
	logger.LogInfo("second", nil)
	logger.LogInfo("third", nil)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d: %v", len(lines), lines)
	}

	// Each line should be valid JSON
	for i, line := range lines {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i, err)
		}
	}
}

func TestTimestampISO8601Format(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)

	logger.LogInfo("test", nil)

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	tsStr := entry["timestamp"].(string)

	// Verify ISO 8601 format with millisecond precision
	_, err := time.Parse("2006-01-02T15:04:05.000Z07:00", tsStr)
	if err != nil {
		t.Errorf("timestamp %q is not valid ISO 8601 with ms precision: %v", tsStr, err)
	}
}
