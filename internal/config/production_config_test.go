package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

func validProductionConfigForTest() *SystemConfig {
	return &SystemConfig{
		Symbols:              []string{"DOGE-USDT", "WIF-USDT"},
		MeanReversionConfigs: []models.MeanReversionConfig{},
		WebSocketURL:         "wss://ws.okx.com:8443/ws/v5/public",
		PrivateWebSocketURL:  "wss://ws.okx.com:8443/ws/v5/private",
		RESTURL:              "https://www.okx.com",
		ReconcileIntervalSec: 30,
		PersistencePath:      ApprovedStatePath,
		Deployment:           DeploymentConfig{Location: ApprovedProductionLocation},
		Execution:            ExecutionConfig{TDMode: ApprovedTradingMode},
		PrivateWS: PrivateWSConfig{
			HeartbeatInterval:      ApprovedPrivateWSHeartbeat,
			LivenessTimeout:        ApprovedPrivateWSLivenessTimeout,
			ReconnectStartDeadline: ApprovedReconnectStartDeadline,
		},
		Reconciliation: ReconciliationConfig{Interval: ApprovedReconciliationInterval},
		Rebalancer: RebalancerConfig{
			Interval:  ApprovedRebalancerInterval,
			MaxJitter: MaximumRebalancerJitter,
		},
		Ticker: TickerConfig{MaxAge: ApprovedTickerMaxAge},
		AdaptiveRange: AdaptiveRangeConfig{
			MinHalfWidth: ApprovedMinimumHalfWidth,
			MaxHalfWidth: ApprovedMaximumHalfWidth,
			Symmetric:    true,
			WidthMode:    PerSideHalfWidthMode,
		},
		ExecutionMode:  ExecutionModeProduction,
		TradingEnabled: false,
	}
}

func completeProductionGatesForTest() ProductionGateEvidence {
	return ProductionGateEvidence{
		CredentialRotation:   "rotation-evidence-id",
		LeastPrivilege:       "least-privilege-evidence-id",
		SingaporeIPAllowlist: "singapore-ip-evidence-id",
		HumanTradingApproval: "human-approval-evidence-id",
	}
}

func TestStrictProductionConfig(t *testing.T) {
	if err := ValidateProductionConfig(validProductionConfigForTest()); err != nil {
		t.Fatalf("valid production config rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*SystemConfig)
		field  string
	}{
		{"cross mode", func(c *SystemConfig) { c.Execution.TDMode = "cross" }, "execution.td_mode"},
		{"non-Singapore location", func(c *SystemConfig) { c.Deployment.Location = "aws-ap-northeast-1-tokyo" }, "deployment.location"},
		{"relative state path", func(c *SystemConfig) { c.PersistencePath = "data/hft_state.db" }, "persistence_path"},
		{"unapproved symbol", func(c *SystemConfig) { c.Symbols = []string{"DOGE-USDT", "PEPE-USDT"} }, "symbols"},
		{"production mean reversion", func(c *SystemConfig) { c.MeanReversionConfigs = []models.MeanReversionConfig{{Symbol: "DOGE-USDT"}} }, "mean_reversion_configs"},
		{"production trading without gates", func(c *SystemConfig) { c.TradingEnabled = true }, "gate evidence"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validProductionConfigForTest()
			test.mutate(cfg)
			err := ValidateProductionConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("expected %q validation error, got %v", test.field, err)
			}
		})
	}

	approved := validProductionConfigForTest()
	approved.TradingEnabled = true
	approved.ProductionGates = completeProductionGatesForTest()
	if err := ValidateProductionConfig(approved); err != nil {
		t.Fatalf("fully gated production trading config rejected: %v", err)
	}
}

func TestStrictProductionConfigRejectsUnknownFields(t *testing.T) {
	profilePath := filepath.Join("..", "..", "deploy", "config.example.yaml")
	contents, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read versioned profile: %v", err)
	}
	contents = append(contents, []byte("\nunknown_production_switch: true\n")...)
	path := filepath.Join(t.TempDir(), "unknown.yaml")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write strict profile: %v", err)
	}
	_, err = LoadProductionConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown_production_switch") {
		t.Fatalf("unknown field was not rejected precisely: %v", err)
	}
}

func TestApprovedTiming(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SystemConfig)
		field  string
	}{
		{"heartbeat 25", func(c *SystemConfig) { c.PrivateWS.HeartbeatInterval = 25 * time.Second }, "heartbeat_interval"},
		{"liveness 86400", func(c *SystemConfig) { c.PrivateWS.LivenessTimeout = 86400 * time.Second }, "liveness_timeout"},
		{"reconnect late", func(c *SystemConfig) { c.PrivateWS.ReconnectStartDeadline = 6 * time.Second }, "reconnect_start_deadline"},
		{"reconcile 60", func(c *SystemConfig) { c.Reconciliation.Interval = 60 * time.Second }, "reconciliation.interval"},
		{"legacy reconcile 60", func(c *SystemConfig) { c.ReconcileIntervalSec = 60 }, "reconcile_interval_sec"},
		{"rebalance 60", func(c *SystemConfig) { c.Rebalancer.Interval = 60 * time.Second }, "rebalancer.interval"},
		{"rebalance jitter too large", func(c *SystemConfig) { c.Rebalancer.MaxJitter = 6 * time.Second }, "rebalancer.max_jitter"},
		{"ticker stale", func(c *SystemConfig) { c.Ticker.MaxAge = 6 * time.Second }, "ticker.max_age"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validProductionConfigForTest()
			test.mutate(cfg)
			err := ValidateProductionConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("expected %s error, got %v", test.field, err)
			}
		})
	}
}

func TestSymmetricHalfWidth(t *testing.T) {
	valid := validProductionConfigForTest()
	if err := ValidateProductionConfig(valid); err != nil {
		t.Fatalf("approved symmetric half-width rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*SystemConfig)
	}{
		{"minimum below 1.5 percent", func(c *SystemConfig) { c.AdaptiveRange.MinHalfWidth = decimal.RequireFromString("0.0149") }},
		{"maximum above 4 percent", func(c *SystemConfig) { c.AdaptiveRange.MaxHalfWidth = decimal.RequireFromString("0.0401") }},
		{"asymmetric", func(c *SystemConfig) { c.AdaptiveRange.Symmetric = false }},
		{"total width", func(c *SystemConfig) { c.AdaptiveRange.WidthMode = "total_width" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validProductionConfigForTest()
			test.mutate(cfg)
			if err := ValidateProductionConfig(cfg); err == nil {
				t.Fatal("invalid width interpretation was accepted")
			}
		})
	}
}

func TestNoSedDependency(t *testing.T) {
	profilePath := filepath.Join("..", "..", "deploy", "config.example.yaml")
	cfg, err := LoadProductionConfig(profilePath)
	if err != nil {
		t.Fatalf("versioned production profile must load without runtime mutation: %v", err)
	}
	if cfg.Execution.TDMode != "cash" || cfg.ReconcileIntervalSec != 30 ||
		cfg.PrivateWS.HeartbeatInterval != 20*time.Second ||
		cfg.PrivateWS.LivenessTimeout != 45*time.Second ||
		cfg.PersistencePath != ApprovedStatePath {
		t.Fatalf("versioned profile does not contain approved effective values: %+v", cfg)
	}
	contents, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read versioned profile: %v", err)
	}
	lower := strings.ToLower(string(contents))
	for _, legacy := range []string{"td_mode: \"cross\"", "reconcile_interval_sec: 60", "86400s", "persistence_path: \"data/", "- \"pepe-usdt\""} {
		if strings.Contains(lower, legacy) {
			t.Fatalf("versioned profile retains forbidden runtime mutation target %q", legacy)
		}
	}
}

func TestEffectiveConfigSanitized(t *testing.T) {
	cfg := validProductionConfigForTest()
	cfg.TradingEnabled = true
	cfg.ProductionGates = ProductionGateEvidence{
		CredentialRotation:   "rotation-sensitive-evidence",
		LeastPrivilege:       "permission-sensitive-evidence",
		SingaporeIPAllowlist: "ip-sensitive-evidence",
		HumanTradingApproval: "human-sensitive-evidence",
	}
	cfg.PrivateWebSocketURL = DefaultPrivateWebSocketURL

	// Use valid production endpoints to pass endpoint validation, then test
	// that the summary still omits gate evidence values.
	summary, err := EffectiveConfigSanitized(cfg)
	if err != nil {
		t.Fatalf("build effective config summary: %v", err)
	}
	for _, secret := range []string{
		"rotation-sensitive-evidence", "permission-sensitive-evidence", "ip-sensitive-evidence", "human-sensitive-evidence",
	} {
		if strings.Contains(summary, secret) {
			t.Fatalf("effective config summary leaked %q: %s", secret, summary)
		}
	}
	for _, expected := range []string{ApprovedProductionLocation, "cash", "20s", "45s", "30s", ApprovedStatePath, "DOGE-USDT", "WIF-USDT", "https://www.okx.com", "wss://ws.okx.com:8443"} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("effective config summary missing approved field %q: %s", expected, summary)
		}
	}

	// For URL secret-leakage testing, use non-production mode to bypass
	// production endpoint validation (which correctly rejects userinfo/query).
	nonProdCfg := *cfg
	nonProdCfg.ExecutionMode = ExecutionModeUnit
	nonProdCfg.RESTURL = "https://api-user:api-password@www.okx.com/private?token=query-secret"
	nonProdCfg.WebSocketURL = "wss://ws-user:ws-password@ws.okx.com:8443/ws/v5/public?signature=secret"
	nonProdCfg.PrivateWebSocketURL = "wss://priv-key:priv-secret@ws.okx.com:8443/ws/v5/private?auth=hidden"

	summary2, err := EffectiveConfigSanitized(&nonProdCfg)
	if err != nil {
		t.Fatalf("non-production sanitized summary: %v", err)
	}
	for _, secret := range []string{
		"api-user", "api-password", "query-secret", "ws-user", "ws-password", "signature=secret",
		"priv-key", "priv-secret", "auth=hidden",
	} {
		if strings.Contains(summary2, secret) {
			t.Fatalf("effective config summary leaked %q: %s", secret, summary2)
		}
	}
}
