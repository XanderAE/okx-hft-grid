package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"gopkg.in/yaml.v3"
)

// ValidationError holds multiple validation failure messages.
type ValidationError struct {
	Errors []string
}

// Error returns a formatted string of all validation errors.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed: %s", strings.Join(e.Errors, "; "))
}

// HasErrors returns true if there are any validation errors.
func (e *ValidationError) HasErrors() bool {
	return len(e.Errors) > 0
}

// Add appends a validation error message.
func (e *ValidationError) Add(msg string) {
	e.Errors = append(e.Errors, msg)
}

// SystemConfig holds the complete system configuration.
type SystemConfig struct {
	Symbols              []string                      `yaml:"symbols"`
	GridConfigs          []models.GridConfig           `yaml:"grid_configs"`
	MeanReversionConfigs []models.MeanReversionConfig  `yaml:"mean_reversion_configs"`
	RiskLimits           models.RiskLimits             `yaml:"risk_limits"`
	ExchangeMinOrderSize map[string]decimal.Decimal    `yaml:"exchange_min_order_size"`
	WebSocketURL         string                        `yaml:"websocket_url"`
	RESTURL              string                        `yaml:"rest_url"`
	ReconcileIntervalSec int                           `yaml:"reconcile_interval_sec"`
	PersistencePath      string                        `yaml:"persistence_path"`
	MetricsPort          int                           `yaml:"metrics_port"`
}

// LoadConfig reads a YAML configuration file and returns the parsed SystemConfig.
func LoadConfig(path string) (*SystemConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	var cfg SystemConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", path, err)
	}

	return &cfg, nil
}

// ValidateGridConfig validates a GridConfig and returns a detailed error if invalid.
func ValidateGridConfig(cfg *models.GridConfig, exchangeMinOrderSize decimal.Decimal) error {
	ve := &ValidationError{}

	if cfg.UpperPrice.LessThanOrEqual(cfg.LowerPrice) {
		ve.Add(fmt.Sprintf("upperPrice (%s) must be greater than lowerPrice (%s)",
			cfg.UpperPrice.String(), cfg.LowerPrice.String()))
	}

	if cfg.GridCount < 3 || cfg.GridCount > 500 {
		ve.Add(fmt.Sprintf("gridCount (%d) must be between 3 and 500 inclusive", cfg.GridCount))
	}

	if cfg.OrderSize.LessThan(exchangeMinOrderSize) {
		ve.Add(fmt.Sprintf("orderSize (%s) must be greater than or equal to exchange minimum (%s)",
			cfg.OrderSize.String(), exchangeMinOrderSize.String()))
	}

	if cfg.Drift != nil {
		if err := ValidateDriftConfig(cfg.Drift); err != nil {
			if driftVE, ok := err.(*ValidationError); ok {
				for _, msg := range driftVE.Errors {
					ve.Add(msg)
				}
			} else {
				ve.Add(err.Error())
			}
		}
	}

	if ve.HasErrors() {
		return ve
	}
	return nil
}

// ValidateDriftConfig validates a DriftConfig and returns a detailed error if invalid.
func ValidateDriftConfig(cfg *models.DriftConfig) error {
	ve := &ValidationError{}

	thresholdMin := decimal.NewFromFloat(0.01)
	thresholdMax := decimal.NewFromFloat(0.50)

	if cfg.DriftThreshold.LessThan(thresholdMin) || cfg.DriftThreshold.GreaterThan(thresholdMax) {
		ve.Add(fmt.Sprintf("driftThreshold (%s) must be between 0.01 and 0.50 inclusive",
			cfg.DriftThreshold.String()))
	}

	if cfg.DriftStep <= 0 {
		ve.Add(fmt.Sprintf("driftStep (%d) must be a positive integer", cfg.DriftStep))
	}

	minCooldown := 5 * time.Second
	if cfg.CooldownPeriod < minCooldown {
		ve.Add(fmt.Sprintf("cooldownPeriod (%s) must be at least 5s", cfg.CooldownPeriod.String()))
	}

	if cfg.MaxDrifts < 0 {
		ve.Add(fmt.Sprintf("maxDrifts (%d) must be non-negative", cfg.MaxDrifts))
	}

	if ve.HasErrors() {
		return ve
	}
	return nil
}

// ValidateMeanReversionConfig validates a MeanReversionConfig and returns a detailed error if invalid.
func ValidateMeanReversionConfig(cfg *models.MeanReversionConfig) error {
	ve := &ValidationError{}

	entryMin := decimal.NewFromFloat(1.0)
	entryMax := decimal.NewFromFloat(5.0)
	exitMin := decimal.NewFromFloat(0.1)
	exitMax := decimal.NewFromFloat(2.0)

	if cfg.EntryThreshold.LessThanOrEqual(cfg.ExitThreshold) {
		ve.Add(fmt.Sprintf("entryThreshold (%s) must be greater than exitThreshold (%s)",
			cfg.EntryThreshold.String(), cfg.ExitThreshold.String()))
	}

	if cfg.EntryThreshold.LessThan(entryMin) || cfg.EntryThreshold.GreaterThan(entryMax) {
		ve.Add(fmt.Sprintf("entryThreshold (%s) must be within [1.0, 5.0]",
			cfg.EntryThreshold.String()))
	}

	if cfg.ExitThreshold.LessThan(exitMin) || cfg.ExitThreshold.GreaterThan(exitMax) {
		ve.Add(fmt.Sprintf("exitThreshold (%s) must be within [0.1, 2.0]",
			cfg.ExitThreshold.String()))
	}

	if cfg.LookbackPeriod < 10 || cfg.LookbackPeriod > 500 {
		ve.Add(fmt.Sprintf("lookbackPeriod (%d) must be between 10 and 500 inclusive", cfg.LookbackPeriod))
	}

	if ve.HasErrors() {
		return ve
	}
	return nil
}

// ValidateRiskLimits validates a RiskLimits and returns a detailed error if invalid.
func ValidateRiskLimits(limits *models.RiskLimits) error {
	ve := &ValidationError{}

	zero := decimal.Zero

	if limits.MaxPositionPerSymbol.LessThanOrEqual(zero) {
		ve.Add(fmt.Sprintf("maxPositionPerSymbol (%s) must be positive",
			limits.MaxPositionPerSymbol.String()))
	}

	if limits.MaxTotalPosition.LessThanOrEqual(zero) {
		ve.Add(fmt.Sprintf("maxTotalPosition (%s) must be positive",
			limits.MaxTotalPosition.String()))
	}

	if limits.MaxDailyLoss.LessThanOrEqual(zero) {
		ve.Add(fmt.Sprintf("maxDailyLoss (%s) must be positive",
			limits.MaxDailyLoss.String()))
	}

	if limits.MaxStrategyLoss.LessThanOrEqual(zero) {
		ve.Add(fmt.Sprintf("maxStrategyLoss (%s) must be positive",
			limits.MaxStrategyLoss.String()))
	}

	if limits.MaxOrdersPerSecond <= 0 {
		ve.Add(fmt.Sprintf("maxOrdersPerSecond (%d) must be positive", limits.MaxOrdersPerSecond))
	}

	if limits.MaxOpenOrders <= 0 {
		ve.Add(fmt.Sprintf("maxOpenOrders (%d) must be positive", limits.MaxOpenOrders))
	}

	if limits.MinSpreadBps <= 0 {
		ve.Add(fmt.Sprintf("minSpreadBps (%d) must be positive", limits.MinSpreadBps))
	}

	if limits.EmergencyStopLoss.LessThanOrEqual(zero) {
		ve.Add(fmt.Sprintf("emergencyStopLoss (%s) must be positive",
			limits.EmergencyStopLoss.String()))
	}

	if ve.HasErrors() {
		return ve
	}
	return nil
}
