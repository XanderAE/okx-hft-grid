package monitor

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/yourname/okx-hft-grid/internal/config"
	"pgregory.net/rapid"
)

// **Validates: Requirements 3.11**
//
// PRE-11 records controlled environment loading plus sanitization across info,
// warning, error, trade-action and local alert-fallback output. Inputs are
// synthetic credential-shaped values; side/symbol/quantity fields remain exact.
func TestProperty2_Preservation_PRE11_ControlledCredentialsAndSanitizedObservability(t *testing.T) {
	const (
		controlledAPIKey     = "synthetic-controlled-api-key"
		controlledSecretKey  = "synthetic-controlled-secret-key"
		controlledPassphrase = "synthetic-controlled-passphrase"
	)
	t.Setenv("OKX_API_KEY", controlledAPIKey)
	t.Setenv("OKX_SECRET_KEY", controlledSecretKey)
	t.Setenv("OKX_PASSPHRASE", controlledPassphrase)
	credentials, err := config.LoadCredentials()
	if err != nil {
		t.Fatalf("PRE-11 controlled credential load: %v", err)
	}
	if credentials.APIKey != controlledAPIKey || credentials.SecretKey != controlledSecretKey || credentials.Passphrase != controlledPassphrase {
		t.Fatalf("PRE-11 controlled credential source changed")
	}

	rapid.Check(t, func(t *rapid.T) {
		secretShapes := []string{
			"0123456789abcdef0123456789abcdef",
			"abcdefabcdefabcdefabcdefabcdefabcdefabcd",
			"11112222333344445555666677778888",
		}
		secret := secretShapes[rapid.IntRange(0, len(secretShapes)-1).Draw(t, "secret_shape_index")]
		symbols := []string{"DOGE-USDT", "WIF-USDT"}
		symbol := symbols[rapid.IntRange(0, len(symbols)-1).Draw(t, "symbol_index")]

		var output bytes.Buffer
		logger := NewStructuredLogger(&output)
		logger.SetSanitizer(config.SanitizeLog)
		logger.LogInfo("credential source loaded api_key="+secret, map[string]string{
			"symbol": symbol, "side": "BUY", "quantity": "10",
		})
		logger.LogWarn("recovery warning", map[string]string{
			"token": "token=" + secret, "symbol": symbol,
		})
		logger.LogError("request rejected secret_key="+secret, map[string]string{
			"symbol": symbol, "authorization": "token=" + secret,
		})
		logger.LogTradeAction(TradeAction{
			Timestamp:  time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
			ActionType: "COUNTER_ORDER", Instrument: symbol, Quantity: "10", Price: "2",
			OrderID: "sim-order-" + secret, Result: "SIMULATED",
			Extra: map[string]string{"side": "BUY", "passphrase": "passphrase=" + secret},
		})
		alerter := NewAlerter(nil, logger)
		if alertErr := alerter.SendAlert(Alert{
			Level: CRITICAL, Message: "simulated alert passphrase=" + secret,
			Timestamp: time.Date(2025, 1, 2, 3, 4, 6, 0, time.UTC),
			Extra:     map[string]string{"symbol": symbol},
		}); alertErr == nil {
			t.Fatalf("PRE-11 no-channel fallback unexpectedly reported delivery")
		}

		text := output.String()
		if strings.Contains(text, secret) {
			t.Fatalf("PRE-11 raw secret-shaped value leaked: %s", text)
		}
		if !strings.Contains(text, "********") {
			t.Fatalf("PRE-11 sanitization mask missing: %s", text)
		}
		for _, preserved := range []string{symbol, "BUY", "10"} {
			if !strings.Contains(text, preserved) {
				t.Fatalf("PRE-11 non-sensitive field %q was lost: %s", preserved, text)
			}
		}
	})
}
