package monitor

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// sensitiveKeyDenylist contains field names that must never appear in log output.
// Any key (case-insensitive) matching these patterns is replaced with a redacted marker.
var sensitiveKeyDenylist = []string{
	"authorization",
	"api_key",
	"api-key",
	"apikey",
	"secret_key",
	"secret-key",
	"secretkey",
	"secret",
	"passphrase",
	"password",
	"signature",
	"access_key",
	"access-key",
	"accesskey",
	"env_dump",
	"env-dump",
	"envdump",
}

// isSensitiveKey checks if a key name matches the denylist (case-insensitive).
func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, denied := range sensitiveKeyDenylist {
		if lower == denied || strings.Contains(lower, denied) {
			return true
		}
	}
	return false
}

const redactedKeyValue = "[REDACTED]"

// TradeAction represents a single trading action log entry with all required fields.
type TradeAction struct {
	Timestamp  time.Time         `json:"-"`
	ActionType string            `json:"action_type"`
	Instrument string            `json:"instrument"`
	Quantity   string            `json:"quantity"`
	Price      string            `json:"price"`
	OrderID    string            `json:"order_id"`
	Result     string            `json:"result"`
	Extra      map[string]string `json:"extra,omitempty"`
}

// logEntry is the internal JSON structure written to the output.
type logEntry struct {
	Timestamp  string            `json:"timestamp"`
	Level      string            `json:"level"`
	Message    string            `json:"msg,omitempty"`
	ActionType string            `json:"action_type,omitempty"`
	Instrument string            `json:"instrument,omitempty"`
	Quantity   string            `json:"quantity,omitempty"`
	Price      string            `json:"price,omitempty"`
	OrderID    string            `json:"order_id,omitempty"`
	Result     string            `json:"result,omitempty"`
	Extra      map[string]string `json:"extra,omitempty"`
}

// StructuredLogger outputs structured JSON logs to the configured writer.
// All string values are passed through a sanitizer before output to prevent
// credential leakage in logs.
type StructuredLogger struct {
	output    io.Writer
	sanitizer func(string) string
	mu        sync.Mutex
}

// NewStructuredLogger creates a new StructuredLogger that writes JSON logs
// to the provided output writer. By default, output goes to os.Stdout.
func NewStructuredLogger(output io.Writer) *StructuredLogger {
	if output == nil {
		output = os.Stdout
	}
	return &StructuredLogger{
		output:    output,
		sanitizer: func(s string) string { return s }, // no-op by default
	}
}

// SetSanitizer sets the sanitization function for log output.
// Integrate with config.SanitizeLog to strip credentials from all logged values.
func (l *StructuredLogger) SetSanitizer(fn func(string) string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if fn != nil {
		l.sanitizer = fn
	}
}

// LogTradeAction outputs a structured JSON trade action log entry.
// The entry includes timestamp with millisecond precision, action type, instrument,
// quantity, price, order ID, and result as required by Requirement 11.4.
// Keys matching the sensitive denylist have their values replaced with [REDACTED].
func (l *StructuredLogger) LogTradeAction(action TradeAction) {
	ts := action.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	l.mu.Lock()
	sanitize := l.sanitizer
	l.mu.Unlock()

	extra := make(map[string]string, len(action.Extra))
	for k, v := range action.Extra {
		sanitizedKey := sanitize(k)
		if isSensitiveKey(k) {
			extra[sanitizedKey] = redactedKeyValue
		} else {
			extra[sanitizedKey] = sanitize(v)
		}
	}

	entry := logEntry{
		Timestamp:  ts.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Level:      "INFO",
		ActionType: sanitize(action.ActionType),
		Instrument: sanitize(action.Instrument),
		Quantity:   sanitize(action.Quantity),
		Price:      sanitize(action.Price),
		OrderID:    sanitize(action.OrderID),
		Result:     sanitize(action.Result),
	}
	if len(extra) > 0 {
		entry.Extra = extra
	}

	l.writeJSON(entry)
}

// LogInfo outputs an informational structured log entry.
func (l *StructuredLogger) LogInfo(msg string, fields map[string]string) {
	l.logLevel("INFO", msg, fields)
}

// LogWarn outputs a warning structured log entry.
func (l *StructuredLogger) LogWarn(msg string, fields map[string]string) {
	l.logLevel("WARN", msg, fields)
}

// LogError outputs an error structured log entry.
func (l *StructuredLogger) LogError(msg string, fields map[string]string) {
	l.logLevel("ERROR", msg, fields)
}

// logLevel handles generic log entries at the specified level.
// Keys matching the sensitive denylist have their values replaced with [REDACTED].
func (l *StructuredLogger) logLevel(level, msg string, fields map[string]string) {
	l.mu.Lock()
	sanitize := l.sanitizer
	l.mu.Unlock()

	extra := make(map[string]string, len(fields))
	for k, v := range fields {
		sanitizedKey := sanitize(k)
		if isSensitiveKey(k) {
			extra[sanitizedKey] = redactedKeyValue
		} else {
			extra[sanitizedKey] = sanitize(v)
		}
	}

	entry := logEntry{
		Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Level:     level,
		Message:   sanitize(msg),
	}
	if len(extra) > 0 {
		entry.Extra = extra
	}

	l.writeJSON(entry)
}

// writeJSON encodes the entry as a single JSON line and writes it to the output.
func (l *StructuredLogger) writeJSON(entry logEntry) {
	data, err := json.Marshal(entry)
	if err != nil {
		// If marshaling fails, write a fallback error line
		data = []byte(`{"level":"ERROR","msg":"failed to marshal log entry"}`)
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.output.Write(data)
}
