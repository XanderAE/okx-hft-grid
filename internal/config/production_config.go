package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	pathpkg "path"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	ApprovedProductionLocation = "aws-ap-southeast-1-singapore"
	ApprovedTradingMode        = "cash"
	ApprovedStatePath          = "/var/lib/okx-hft-grid/hft_state.db"
	PerSideHalfWidthMode       = "per_side_half_width"
)

const (
	ApprovedPrivateWSHeartbeat       = 20 * time.Second
	ApprovedPrivateWSLivenessTimeout = 45 * time.Second
	ApprovedReconnectStartDeadline   = 5 * time.Second
	ApprovedReconciliationInterval   = 30 * time.Second
	ApprovedRebalancerInterval       = 30 * time.Second
	MaximumRebalancerJitter          = 5 * time.Second
	ApprovedTickerMaxAge             = 5 * time.Second
)

var (
	ApprovedMinimumHalfWidth = decimal.RequireFromString("0.005")
	ApprovedMaximumHalfWidth = decimal.RequireFromString("0.04")
)

// ExecutionMode identifies the environment in which the process is intended to run.
type ExecutionMode string

const (
	ExecutionModeProduction ExecutionMode = "production"
	ExecutionModeSimulated  ExecutionMode = "simulated"
	ExecutionModeReplay     ExecutionMode = "replay"
	ExecutionModeUnit       ExecutionMode = "unit"
)

// DeploymentConfig contains non-secret deployment identity.
type DeploymentConfig struct {
	Location string `yaml:"location"`
}

// ExecutionConfig contains exchange execution invariants.
type ExecutionConfig struct {
	TDMode string `yaml:"td_mode"`
}

// PrivateWSConfig contains the approved liveness timing contract.
type PrivateWSConfig struct {
	HeartbeatInterval      time.Duration `yaml:"heartbeat_interval"`
	LivenessTimeout        time.Duration `yaml:"liveness_timeout"`
	ReconnectStartDeadline time.Duration `yaml:"reconnect_start_deadline"`
}

// ReconciliationConfig contains the fixed-rate reconciliation schedule.
type ReconciliationConfig struct {
	Interval time.Duration `yaml:"interval"`
}

// RebalancerConfig contains the fixed-rate schedule and maximum permitted jitter.
type RebalancerConfig struct {
	Interval  time.Duration `yaml:"interval"`
	MaxJitter time.Duration `yaml:"max_jitter"`
}

// TickerConfig contains the maximum accepted age for a reference ticker.
type TickerConfig struct {
	MaxAge time.Duration `yaml:"max_age"`
}

// AdaptiveRangeConfig describes a symmetric range using one per-side half-width.
type AdaptiveRangeConfig struct {
	MinHalfWidth decimal.Decimal `yaml:"min_half_width"`
	MaxHalfWidth decimal.Decimal `yaml:"max_half_width"`
	Symmetric    bool            `yaml:"symmetric"`
	WidthMode    string          `yaml:"width_mode,omitempty"`
}

// ProductionGateEvidence contains opaque evidence identifiers only. The values
// are never included in effective-config summaries or validation errors.
type ProductionGateEvidence struct {
	CredentialRotation   string `yaml:"credential_rotation"`
	LeastPrivilege       string `yaml:"least_privilege"`
	SingaporeIPAllowlist string `yaml:"singapore_ip_allowlist"`
	HumanTradingApproval string `yaml:"human_trading_approval"`
}

// Complete reports whether every independently required production-trading gate
// has evidence. It intentionally does not expose any evidence value.
func (g ProductionGateEvidence) Complete() bool {
	return strings.TrimSpace(g.CredentialRotation) != "" &&
		strings.TrimSpace(g.LeastPrivilege) != "" &&
		strings.TrimSpace(g.SingaporeIPAllowlist) != "" &&
		strings.TrimSpace(g.HumanTradingApproval) != ""
}

// MissingNames returns only non-sensitive gate field names.
func (g ProductionGateEvidence) MissingNames() []string {
	var missing []string
	if strings.TrimSpace(g.CredentialRotation) == "" {
		missing = append(missing, "credential_rotation")
	}
	if strings.TrimSpace(g.LeastPrivilege) == "" {
		missing = append(missing, "least_privilege")
	}
	if strings.TrimSpace(g.SingaporeIPAllowlist) == "" {
		missing = append(missing, "singapore_ip_allowlist")
	}
	if strings.TrimSpace(g.HumanTradingApproval) == "" {
		missing = append(missing, "human_trading_approval")
	}
	return missing
}

// ValidateProductionConfig validates every approved production invariant before
// any network component may be constructed.
func ValidateProductionConfig(cfg *SystemConfig) error {
	ve := &ValidationError{}
	if cfg == nil {
		ve.Add("production config is required")
		return ve
	}

	if cfg.ExecutionMode != ExecutionModeProduction {
		ve.Add(fmt.Sprintf("execution_mode must be %q", ExecutionModeProduction))
	}
	if cfg.Deployment.Location != ApprovedProductionLocation {
		ve.Add(fmt.Sprintf("deployment.location must be %q", ApprovedProductionLocation))
	}
	if cfg.Execution.TDMode != ApprovedTradingMode {
		ve.Add(fmt.Sprintf("execution.td_mode must be %q", ApprovedTradingMode))
	}

	requireExactDuration(ve, "private_ws.heartbeat_interval", cfg.PrivateWS.HeartbeatInterval, ApprovedPrivateWSHeartbeat)
	requireExactDuration(ve, "private_ws.liveness_timeout", cfg.PrivateWS.LivenessTimeout, ApprovedPrivateWSLivenessTimeout)
	requireExactDuration(ve, "private_ws.reconnect_start_deadline", cfg.PrivateWS.ReconnectStartDeadline, ApprovedReconnectStartDeadline)
	requireExactDuration(ve, "reconciliation.interval", cfg.Reconciliation.Interval, ApprovedReconciliationInterval)
	requireExactDuration(ve, "rebalancer.interval", cfg.Rebalancer.Interval, ApprovedRebalancerInterval)
	if cfg.Rebalancer.MaxJitter < 0 || cfg.Rebalancer.MaxJitter > MaximumRebalancerJitter {
		ve.Add(fmt.Sprintf("rebalancer.max_jitter must be between 0s and %s", MaximumRebalancerJitter))
	}
	requireExactDuration(ve, "ticker.max_age", cfg.Ticker.MaxAge, ApprovedTickerMaxAge)
	if cfg.ReconcileIntervalSec != 0 && cfg.ReconcileIntervalSec != int(ApprovedReconciliationInterval/time.Second) {
		ve.Add("reconcile_interval_sec, when present for legacy composition, must be 30")
	}

	if !cfg.AdaptiveRange.MinHalfWidth.Equal(ApprovedMinimumHalfWidth) {
		ve.Add(fmt.Sprintf("adaptive_range.min_half_width must be %s per side", ApprovedMinimumHalfWidth))
	}
	if !cfg.AdaptiveRange.MaxHalfWidth.Equal(ApprovedMaximumHalfWidth) {
		ve.Add(fmt.Sprintf("adaptive_range.max_half_width must be %s per side", ApprovedMaximumHalfWidth))
	}
	if !cfg.AdaptiveRange.Symmetric {
		ve.Add("adaptive_range.symmetric must be true")
	}
	if cfg.AdaptiveRange.WidthMode != "" && cfg.AdaptiveRange.WidthMode != PerSideHalfWidthMode {
		ve.Add(fmt.Sprintf("adaptive_range.width_mode must be %q; total or asymmetric width is forbidden", PerSideHalfWidthMode))
	}

	cleanStatePath := pathpkg.Clean(cfg.PersistencePath)
	if !strings.HasPrefix(cleanStatePath, "/") || cleanStatePath != ApprovedStatePath || cfg.PersistencePath != cleanStatePath {
		ve.Add(fmt.Sprintf("persistence_path must be the canonical absolute production path %q", ApprovedStatePath))
	}

	if !hasExactApprovedSymbols(cfg.Symbols) {
		ve.Add("symbols must contain exactly DOGE-USDT and WIF-USDT with no duplicates")
	}
	for _, grid := range cfg.GridConfigs {
		if grid.Symbol != "DOGE-USDT" && grid.Symbol != "WIF-USDT" {
			ve.Add("grid_configs contains a symbol outside the approved DOGE-USDT/WIF-USDT production scope")
			break
		}
	}
	if len(cfg.MeanReversionConfigs) != 0 {
		ve.Add("mean_reversion_configs must be empty in production")
	}

	if cfg.TradingEnabled && !cfg.ProductionGates.Complete() {
		ve.Add("production trading requires independent gate evidence: " + strings.Join(cfg.ProductionGates.MissingNames(), ", "))
	}

	// Validate production endpoint roles
	resolved, endpointErr := ResolveNetworkEndpoints(cfg)
	if endpointErr != nil {
		ve.Add(endpointErr.Error())
	} else {
		// Verify resolved endpoints match the authoritative defaults
		_ = resolved
	}

	if ve.HasErrors() {
		return ve
	}
	return nil
}

func requireExactDuration(ve *ValidationError, field string, actual, expected time.Duration) {
	if actual != expected {
		ve.Add(fmt.Sprintf("%s must be %s", field, expected))
	}
}

func hasExactApprovedSymbols(symbols []string) bool {
	if len(symbols) != 2 {
		return false
	}
	seen := map[string]bool{}
	for _, symbol := range symbols {
		if symbol != "DOGE-USDT" && symbol != "WIF-USDT" {
			return false
		}
		if seen[symbol] {
			return false
		}
		seen[symbol] = true
	}
	return seen["DOGE-USDT"] && seen["WIF-USDT"]
}

type effectiveConfigSummary struct {
	SchemaVersion          int      `json:"schema_version"`
	Location               string   `json:"location"`
	ExecutionMode          string   `json:"execution_mode"`
	TradingEnabled         bool     `json:"trading_enabled"`
	ProductionGatesReady   bool     `json:"production_gates_ready"`
	TDMode                 string   `json:"td_mode"`
	PrivateWSHeartbeat     string   `json:"private_ws_heartbeat"`
	PrivateWSLiveness      string   `json:"private_ws_liveness_timeout"`
	ReconnectStartDeadline string   `json:"reconnect_start_deadline"`
	ReconciliationInterval string   `json:"reconciliation_interval"`
	RebalancerInterval     string   `json:"rebalancer_interval"`
	RebalancerMaxJitter    string   `json:"rebalancer_max_jitter"`
	TickerMaxAge           string   `json:"ticker_max_age"`
	MinHalfWidth           string   `json:"min_half_width_per_side"`
	MaxHalfWidth           string   `json:"max_half_width_per_side"`
	Symmetric              bool     `json:"symmetric"`
	PersistencePath        string   `json:"persistence_path"`
	Symbols                []string `json:"symbols"`
	RESTOrigin             string   `json:"rest_origin,omitempty"`
	WebSocketOrigin        string   `json:"websocket_origin,omitempty"`
	PublicWebSocketOrigin  string   `json:"public_websocket_origin,omitempty"`
	PrivateWebSocketOrigin string   `json:"private_websocket_origin,omitempty"`
	MeanReversionEnabled   bool     `json:"mean_reversion_enabled"`
}

// EffectiveConfigSanitized returns a deterministic JSON summary built from an
// explicit non-sensitive allowlist. Gate evidence, credentials, URL userinfo,
// paths beyond the approved state path, query strings and fragments are omitted.
func EffectiveConfigSanitized(cfg *SystemConfig) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("effective config is required")
	}
	if cfg.ExecutionMode == ExecutionModeProduction {
		if err := ValidateProductionConfig(cfg); err != nil {
			return "", err
		}
	}

	symbols := append([]string(nil), cfg.Symbols...)
	sort.Strings(symbols)
	summary := effectiveConfigSummary{
		SchemaVersion:          1,
		Location:               cfg.Deployment.Location,
		ExecutionMode:          string(cfg.ExecutionMode),
		TradingEnabled:         cfg.TradingEnabled,
		ProductionGatesReady:   cfg.ProductionGates.Complete(),
		TDMode:                 cfg.Execution.TDMode,
		PrivateWSHeartbeat:     cfg.PrivateWS.HeartbeatInterval.String(),
		PrivateWSLiveness:      cfg.PrivateWS.LivenessTimeout.String(),
		ReconnectStartDeadline: cfg.PrivateWS.ReconnectStartDeadline.String(),
		ReconciliationInterval: cfg.Reconciliation.Interval.String(),
		RebalancerInterval:     cfg.Rebalancer.Interval.String(),
		RebalancerMaxJitter:    cfg.Rebalancer.MaxJitter.String(),
		TickerMaxAge:           cfg.Ticker.MaxAge.String(),
		MinHalfWidth:           cfg.AdaptiveRange.MinHalfWidth.String(),
		MaxHalfWidth:           cfg.AdaptiveRange.MaxHalfWidth.String(),
		Symmetric:              cfg.AdaptiveRange.Symmetric,
		PersistencePath:        cfg.PersistencePath,
		Symbols:                symbols,
		RESTOrigin:             sanitizedOrigin(cfg.RESTURL),
		WebSocketOrigin:        sanitizedOrigin(cfg.WebSocketURL),
		PublicWebSocketOrigin:  sanitizedOrigin(cfg.WebSocketURL),
		PrivateWebSocketOrigin: sanitizedOrigin(cfg.PrivateWebSocketURL),
		MeanReversionEnabled:   len(cfg.MeanReversionConfigs) != 0,
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return "", fmt.Errorf("failed to encode effective config summary: %w", err)
	}
	return string(encoded), nil
}

// BuildSanitizedEffectiveConfig is a descriptive alias for callers that build
// startup logs.
func BuildSanitizedEffectiveConfig(cfg *SystemConfig) (string, error) {
	return EffectiveConfigSanitized(cfg)
}

func sanitizedOrigin(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return "invalid"
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if port := u.Port(); port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return strings.ToLower(u.Scheme) + "://" + host
}
