package monitor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockChannel is a test implementation of AlertChannel.
type mockChannel struct {
	name      string
	sendErr   error
	sent      []Alert
	sendCount int
	mu        sync.Mutex
	failUntil int // fail for the first N calls
}

func newMockChannel(name string) *mockChannel {
	return &mockChannel{name: name}
}

func (m *mockChannel) Send(alert Alert) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendCount++
	if m.sendCount <= m.failUntil {
		return m.sendErr
	}
	if m.sendErr != nil && m.failUntil == 0 {
		return m.sendErr
	}
	m.sent = append(m.sent, alert)
	return nil
}

func (m *mockChannel) Name() string {
	return m.name
}

func (m *mockChannel) getSent() []Alert {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]Alert, len(m.sent))
	copy(result, m.sent)
	return result
}

func (m *mockChannel) getSendCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sendCount
}

// --- Alerter Tests ---

func TestNewAlerter(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)
	ch := newMockChannel("test")

	alerter := NewAlerter([]AlertChannel{ch}, logger)
	if alerter == nil {
		t.Fatal("NewAlerter returned nil")
	}
	if alerter.retries != 3 {
		t.Errorf("expected retries=3, got %d", alerter.retries)
	}
	if alerter.retryInterval != 10*time.Second {
		t.Errorf("expected retryInterval=10s, got %v", alerter.retryInterval)
	}
}

func TestSendAlert_Success(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)
	ch := newMockChannel("test")

	alerter := NewAlerter([]AlertChannel{ch}, logger)

	alert := Alert{
		Level:   CRITICAL,
		Message: "Emergency stop triggered",
		Extra: map[string]string{
			"reason": "daily loss exceeded",
		},
	}

	err := alerter.SendAlert(alert)
	if err != nil {
		t.Fatalf("SendAlert() error: %v", err)
	}

	sent := ch.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 sent alert, got %d", len(sent))
	}
	if sent[0].Level != CRITICAL {
		t.Errorf("expected CRITICAL level, got %v", sent[0].Level)
	}
	if sent[0].Message != "Emergency stop triggered" {
		t.Errorf("expected message 'Emergency stop triggered', got %q", sent[0].Message)
	}
}

func TestSendAlert_MultipleChannels(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)
	ch1 := newMockChannel("telegram")
	ch2 := newMockChannel("discord")

	alerter := NewAlerter([]AlertChannel{ch1, ch2}, logger)

	err := alerter.SendAlert(Alert{
		Level:   WARNING,
		Message: "reconciliation discrepancy",
	})
	if err != nil {
		t.Fatalf("SendAlert() error: %v", err)
	}

	if len(ch1.getSent()) != 1 {
		t.Errorf("channel 1 expected 1 alert, got %d", len(ch1.getSent()))
	}
	if len(ch2.getSent()) != 1 {
		t.Errorf("channel 2 expected 1 alert, got %d", len(ch2.getSent()))
	}
}

func TestSendAlert_RetryOnFailure(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)
	ch := newMockChannel("test")
	ch.sendErr = fmt.Errorf("connection refused")
	ch.failUntil = 2 // fail first 2 attempts, succeed on 3rd

	alerter := NewAlerter([]AlertChannel{ch}, logger)
	// Override retry interval to make test fast
	alerter.retryInterval = 1 * time.Millisecond

	err := alerter.SendAlert(Alert{
		Level:   CRITICAL,
		Message: "test retry",
	})
	if err != nil {
		t.Fatalf("SendAlert() should succeed after retry, got error: %v", err)
	}

	if ch.getSendCount() != 3 {
		t.Errorf("expected 3 send attempts (2 fail + 1 success), got %d", ch.getSendCount())
	}
	if len(ch.getSent()) != 1 {
		t.Errorf("expected 1 successfully sent alert, got %d", len(ch.getSent()))
	}
}

func TestSendAlert_AllRetriesFail_LogsCritical(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)
	ch := newMockChannel("test-channel")
	ch.sendErr = fmt.Errorf("network unreachable")
	// failUntil=0 means always fail when sendErr is set

	alerter := NewAlerter([]AlertChannel{ch}, logger)
	alerter.retryInterval = 1 * time.Millisecond

	err := alerter.SendAlert(Alert{
		Level:   CRITICAL,
		Message: "emergency",
	})
	if err == nil {
		t.Fatal("expected error when all channels fail")
	}

	// Should have attempted 4 times (1 initial + 3 retries)
	if ch.getSendCount() != 4 {
		t.Errorf("expected 4 send attempts (1 + 3 retries), got %d", ch.getSendCount())
	}

	// Check that a CRITICAL log was written locally
	output := buf.String()
	if !strings.Contains(output, "CRITICAL") {
		t.Errorf("expected CRITICAL in log output, got:\n%s", output)
	}
	if !strings.Contains(output, "test-channel") {
		t.Errorf("expected channel name in log output, got:\n%s", output)
	}
}

func TestSendAlert_PartialFailure_StillSucceeds(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)
	chFail := newMockChannel("failing")
	chFail.sendErr = fmt.Errorf("always fail")
	chOK := newMockChannel("working")

	alerter := NewAlerter([]AlertChannel{chFail, chOK}, logger)
	alerter.retryInterval = 1 * time.Millisecond

	err := alerter.SendAlert(Alert{
		Level:   WARNING,
		Message: "partial test",
	})
	// Should succeed because at least one channel worked
	if err != nil {
		t.Fatalf("expected no error when at least one channel succeeds, got: %v", err)
	}

	if len(chOK.getSent()) != 1 {
		t.Errorf("working channel should have 1 alert, got %d", len(chOK.getSent()))
	}

	// Failing channel should still have logged a CRITICAL
	output := buf.String()
	if !strings.Contains(output, "CRITICAL") {
		t.Errorf("expected CRITICAL in log for failed channel, got:\n%s", output)
	}
}

func TestSendAlert_NoChannels(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)

	alerter := NewAlerter([]AlertChannel{}, logger)

	err := alerter.SendAlert(Alert{
		Level:   INFO,
		Message: "no channels",
	})
	if err == nil {
		t.Fatal("expected error when no channels configured")
	}
}

func TestSendAlert_SetsTimestamp(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)
	ch := newMockChannel("test")

	alerter := NewAlerter([]AlertChannel{ch}, logger)

	before := time.Now().UTC()
	err := alerter.SendAlert(Alert{
		Level:   INFO,
		Message: "timestamp test",
	})
	after := time.Now().UTC()

	if err != nil {
		t.Fatalf("SendAlert() error: %v", err)
	}

	sent := ch.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(sent))
	}

	ts := sent[0].Timestamp
	if ts.Before(before) || ts.After(after) {
		t.Errorf("timestamp %v is not between %v and %v", ts, before, after)
	}
}

func TestSendCritical(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)
	ch := newMockChannel("test")

	alerter := NewAlerter([]AlertChannel{ch}, logger)

	err := alerter.SendCritical("system halted", map[string]string{
		"reason":    "daily loss exceeded",
		"positions": "BTC-USDT: -500",
	})
	if err != nil {
		t.Fatalf("SendCritical() error: %v", err)
	}

	sent := ch.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(sent))
	}
	if sent[0].Level != CRITICAL {
		t.Errorf("expected CRITICAL level, got %v", sent[0].Level)
	}
	if sent[0].Message != "system halted" {
		t.Errorf("expected message 'system halted', got %q", sent[0].Message)
	}
	if sent[0].Extra["reason"] != "daily loss exceeded" {
		t.Errorf("expected reason in extra, got %v", sent[0].Extra)
	}
}

func TestSendWarning(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)
	ch := newMockChannel("test")

	alerter := NewAlerter([]AlertChannel{ch}, logger)

	err := alerter.SendWarning("reconciliation mismatch", map[string]string{
		"symbol":    "ETH-USDT",
		"local_qty": "10",
		"exch_qty":  "8",
	})
	if err != nil {
		t.Fatalf("SendWarning() error: %v", err)
	}

	sent := ch.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(sent))
	}
	if sent[0].Level != WARNING {
		t.Errorf("expected WARNING level, got %v", sent[0].Level)
	}
	if sent[0].Message != "reconciliation mismatch" {
		t.Errorf("expected message 'reconciliation mismatch', got %q", sent[0].Message)
	}
}

// --- AlertLevel Tests ---

func TestAlertLevel_String(t *testing.T) {
	tests := []struct {
		level    AlertLevel
		expected string
	}{
		{INFO, "INFO"},
		{WARNING, "WARNING"},
		{CRITICAL, "CRITICAL"},
		{AlertLevel(99), "UNKNOWN"},
	}

	for _, tc := range tests {
		if got := tc.level.String(); got != tc.expected {
			t.Errorf("AlertLevel(%d).String() = %q, want %q", tc.level, got, tc.expected)
		}
	}
}

// --- TelegramChannel Tests ---

func TestTelegramChannel_Send_Success(t *testing.T) {
	var receivedBody map[string]string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/botTEST_TOKEN/sendMessage") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Errorf("failed to decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	tc := NewTelegramChannel("TEST_TOKEN", "12345")
	tc.apiBaseURL = server.URL

	alert := Alert{
		Level:     CRITICAL,
		Message:   "test alert",
		Timestamp: time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC),
		Extra:     map[string]string{"key": "value"},
	}

	err := tc.Send(alert)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	if receivedBody["chat_id"] != "12345" {
		t.Errorf("expected chat_id=12345, got %q", receivedBody["chat_id"])
	}
	if !strings.Contains(receivedBody["text"], "CRITICAL") {
		t.Errorf("expected CRITICAL in text, got %q", receivedBody["text"])
	}
	if !strings.Contains(receivedBody["text"], "test alert") {
		t.Errorf("expected 'test alert' in text, got %q", receivedBody["text"])
	}
}

func TestTelegramChannel_Send_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tc := NewTelegramChannel("TOKEN", "CHAT")
	tc.apiBaseURL = server.URL

	err := tc.Send(Alert{Level: INFO, Message: "test"})
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
	if !strings.Contains(err.Error(), "unexpected status code 500") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestTelegramChannel_Name(t *testing.T) {
	tc := NewTelegramChannel("token", "chat")
	if tc.Name() != "telegram" {
		t.Errorf("expected name 'telegram', got %q", tc.Name())
	}
}

// --- DiscordChannel Tests ---

func TestDiscordChannel_Send_Success(t *testing.T) {
	var receivedBody map[string]string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Errorf("failed to decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	dc := NewDiscordChannel(server.URL)

	alert := Alert{
		Level:     WARNING,
		Message:   "reconciliation issue",
		Timestamp: time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC),
	}

	err := dc.Send(alert)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	if !strings.Contains(receivedBody["content"], "WARNING") {
		t.Errorf("expected WARNING in content, got %q", receivedBody["content"])
	}
	if !strings.Contains(receivedBody["content"], "reconciliation issue") {
		t.Errorf("expected message in content, got %q", receivedBody["content"])
	}
}

func TestDiscordChannel_Send_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	dc := NewDiscordChannel(server.URL)

	err := dc.Send(Alert{Level: INFO, Message: "test"})
	if err == nil {
		t.Fatal("expected error on 400 response")
	}
	if !strings.Contains(err.Error(), "unexpected status code 400") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestDiscordChannel_Name(t *testing.T) {
	dc := NewDiscordChannel("https://discord.com/api/webhooks/xxx")
	if dc.Name() != "discord" {
		t.Errorf("expected name 'discord', got %q", dc.Name())
	}
}

// --- formatAlertMessage Tests ---

func TestFormatAlertMessage_Basic(t *testing.T) {
	alert := Alert{
		Level:     CRITICAL,
		Message:   "Emergency stop",
		Timestamp: time.Date(2024, 6, 15, 10, 30, 45, 123000000, time.UTC),
	}

	msg := formatAlertMessage(alert)

	if !strings.Contains(msg, "[CRITICAL]") {
		t.Errorf("expected [CRITICAL] in message, got: %s", msg)
	}
	if !strings.Contains(msg, "Emergency stop") {
		t.Errorf("expected 'Emergency stop' in message, got: %s", msg)
	}
	if !strings.Contains(msg, "2024-06-15T10:30:45.123Z") {
		t.Errorf("expected timestamp in message, got: %s", msg)
	}
}

func TestFormatAlertMessage_WithExtra(t *testing.T) {
	alert := Alert{
		Level:     WARNING,
		Message:   "test",
		Timestamp: time.Now().UTC(),
		Extra: map[string]string{
			"symbol": "BTC-USDT",
			"reason": "position mismatch",
		},
	}

	msg := formatAlertMessage(alert)

	if !strings.Contains(msg, "Details:") {
		t.Errorf("expected 'Details:' in message, got: %s", msg)
	}
	if !strings.Contains(msg, "symbol: BTC-USDT") {
		t.Errorf("expected 'symbol: BTC-USDT' in message, got: %s", msg)
	}
	if !strings.Contains(msg, "reason: position mismatch") {
		t.Errorf("expected 'reason: position mismatch' in message, got: %s", msg)
	}
}

// --- Integration-style Test: Retry behavior timing ---

func TestSendAlert_RetryInterval(t *testing.T) {
	var buf bytes.Buffer
	logger := NewStructuredLogger(&buf)
	ch := newMockChannel("timing")
	ch.sendErr = fmt.Errorf("timeout")
	ch.failUntil = 1 // fail first attempt, succeed second

	alerter := NewAlerter([]AlertChannel{ch}, logger)
	alerter.retryInterval = 50 * time.Millisecond

	start := time.Now()
	err := alerter.SendAlert(Alert{Level: INFO, Message: "timing test"})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}

	// Should have waited at least one retry interval
	if elapsed < 40*time.Millisecond {
		t.Errorf("expected at least ~50ms delay for retry, got %v", elapsed)
	}
}
