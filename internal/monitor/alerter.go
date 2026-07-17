package monitor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// AlertLevel represents the severity of an alert.
type AlertLevel int

const (
	// INFO is for informational alerts.
	INFO AlertLevel = iota
	// WARNING is for alerts that require attention.
	WARNING
	// CRITICAL is for alerts that require immediate action.
	CRITICAL
)

// String returns the string representation of an AlertLevel.
func (l AlertLevel) String() string {
	switch l {
	case INFO:
		return "INFO"
	case WARNING:
		return "WARNING"
	case CRITICAL:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

// Alert represents a notification to be sent through alert channels.
type Alert struct {
	Level     AlertLevel
	Message   string
	Timestamp time.Time
	Extra     map[string]string
}

// AlertChannel defines the interface for alert delivery channels.
type AlertChannel interface {
	// Send delivers an alert through the channel.
	Send(alert Alert) error
	// Name returns the channel identifier.
	Name() string
}

// TelegramChannel sends alerts via the Telegram Bot API.
type TelegramChannel struct {
	botToken   string
	chatID     string
	httpClient *http.Client
	apiBaseURL string
}

// NewTelegramChannel creates a new TelegramChannel with the given bot token and chat ID.
func NewTelegramChannel(botToken, chatID string) *TelegramChannel {
	return &TelegramChannel{
		botToken: botToken,
		chatID:   chatID,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		apiBaseURL: "https://api.telegram.org",
	}
}

// Send delivers an alert via Telegram Bot API sendMessage endpoint.
func (tc *TelegramChannel) Send(alert Alert) error {
	url := fmt.Sprintf("%s/bot%s/sendMessage", tc.apiBaseURL, tc.botToken)

	text := formatAlertMessage(alert)

	payload := map[string]string{
		"chat_id":    tc.chatID,
		"text":       text,
		"parse_mode": "HTML",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram: failed to marshal payload: %w", err)
	}

	resp, err := tc.httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: unexpected status code %d", resp.StatusCode)
	}

	return nil
}

// Name returns the channel name.
func (tc *TelegramChannel) Name() string {
	return "telegram"
}

// DiscordChannel sends alerts via a Discord webhook.
type DiscordChannel struct {
	webhookURL string
	httpClient *http.Client
}

// NewDiscordChannel creates a new DiscordChannel with the given webhook URL.
func NewDiscordChannel(webhookURL string) *DiscordChannel {
	return &DiscordChannel{
		webhookURL: webhookURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Send delivers an alert via Discord webhook.
func (dc *DiscordChannel) Send(alert Alert) error {
	text := formatAlertMessage(alert)

	payload := map[string]string{
		"content": text,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("discord: failed to marshal payload: %w", err)
	}

	resp, err := dc.httpClient.Post(dc.webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("discord: request failed: %w", err)
	}
	defer resp.Body.Close()

	// Discord returns 204 No Content on success
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("discord: unexpected status code %d", resp.StatusCode)
	}

	return nil
}

// Name returns the channel name.
func (dc *DiscordChannel) Name() string {
	return "discord"
}

// Alerter manages alert delivery across multiple channels with retry logic.
type Alerter struct {
	channels      []AlertChannel
	retries       int
	retryInterval time.Duration
	logger        *StructuredLogger
	mu            sync.Mutex
}

// NewAlerter creates a new Alerter with the given channels and logger.
// Default retry configuration: 3 retries with 10s interval.
func NewAlerter(channels []AlertChannel, logger *StructuredLogger) *Alerter {
	return &Alerter{
		channels:      channels,
		retries:       3,
		retryInterval: 10 * time.Second,
		logger:        logger,
	}
}

// SendAlert sends an alert to all configured channels.
// For each channel, retries up to 3 times with 10s interval on failure.
// If all retries fail for a channel, logs a CRITICAL message locally.
// Returns an error if ALL channels fail to deliver the alert.
func (a *Alerter) SendAlert(alert Alert) error {
	if alert.Timestamp.IsZero() {
		alert.Timestamp = time.Now().UTC()
	}

	a.mu.Lock()
	channels := make([]AlertChannel, len(a.channels))
	copy(channels, a.channels)
	retries := a.retries
	retryInterval := a.retryInterval
	a.mu.Unlock()

	if len(channels) == 0 {
		if a.logger != nil {
			a.logger.LogError("alert: no channels configured", map[string]string{
				"level":   alert.Level.String(),
				"message": alert.Message,
			})
		}
		return fmt.Errorf("alerter: no channels configured")
	}

	var (
		atLeastOneSuccess bool
		lastErr           error
	)

	for _, ch := range channels {
		err := a.sendWithRetry(ch, alert, retries, retryInterval)
		if err != nil {
			lastErr = err
			// All retries failed for this channel - log CRITICAL locally
			if a.logger != nil {
				a.logger.LogError(
					fmt.Sprintf("CRITICAL: alert delivery failed after %d retries on channel %s", retries, ch.Name()),
					map[string]string{
						"channel":     ch.Name(),
						"alert_level": alert.Level.String(),
						"alert_msg":   alert.Message,
						"error":       err.Error(),
					},
				)
			}
		} else {
			atLeastOneSuccess = true
		}
	}

	if !atLeastOneSuccess {
		return fmt.Errorf("alerter: all channels failed: %w", lastErr)
	}

	return nil
}

// sendWithRetry attempts to send an alert through a channel with retries.
func (a *Alerter) sendWithRetry(ch AlertChannel, alert Alert, retries int, interval time.Duration) error {
	var lastErr error

	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			time.Sleep(interval)
		}

		err := ch.Send(alert)
		if err == nil {
			return nil
		}
		lastErr = err
	}

	return lastErr
}

// SendCritical is a shortcut to send a CRITICAL-level alert.
func (a *Alerter) SendCritical(message string, extra map[string]string) error {
	alert := Alert{
		Level:     CRITICAL,
		Message:   message,
		Timestamp: time.Now().UTC(),
		Extra:     extra,
	}
	return a.SendAlert(alert)
}

// SendWarning is a shortcut to send a WARNING-level alert.
func (a *Alerter) SendWarning(message string, extra map[string]string) error {
	alert := Alert{
		Level:     WARNING,
		Message:   message,
		Timestamp: time.Now().UTC(),
		Extra:     extra,
	}
	return a.SendAlert(alert)
}

// formatAlertMessage formats an alert into a human-readable message.
func formatAlertMessage(alert Alert) string {
	var buf bytes.Buffer

	buf.WriteString(fmt.Sprintf("[%s] %s\n", alert.Level.String(), alert.Message))
	buf.WriteString(fmt.Sprintf("Time: %s\n", alert.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z")))

	if len(alert.Extra) > 0 {
		buf.WriteString("Details:\n")
		for k, v := range alert.Extra {
			buf.WriteString(fmt.Sprintf("  %s: %s\n", k, v))
		}
	}

	return buf.String()
}
