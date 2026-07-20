package config

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// SwapConfig holds configuration for the perpetual swap market-making strategy.
type SwapConfig struct {
	Instrument    string          `yaml:"instrument"`     // e.g. "BTC-USDT-SWAP"
	TdMode        string          `yaml:"td_mode"`        // must be "isolated"
	MaxLeverage   float64         `yaml:"max_leverage"`   // <= 3.0
	Spread        decimal.Decimal `yaml:"spread"`         // e.g. 0.003 (0.3%)
	StopLoss      decimal.Decimal `yaml:"stop_loss"`      // e.g. 0.02 (2%)
	MaxHoldHours  int             `yaml:"max_hold_hours"` // e.g. 12
	NotionalUSDT  decimal.Decimal `yaml:"notional_usdt"`  // per-trade notional in USDT
	AvoidFunding  bool            `yaml:"avoid_funding"`  // close before funding if unprofitable
	ConfThreshold float64         `yaml:"conf_threshold"` // direction confidence threshold to enter
}

// ValidateSwapConfig validates the swap configuration for production.
func ValidateSwapConfig(cfg *SwapConfig) error {
	if cfg == nil {
		return fmt.Errorf("swap config is required")
	}
	if cfg.Instrument != "BTC-USDT-SWAP" {
		return fmt.Errorf("swap instrument must be BTC-USDT-SWAP, got %q", cfg.Instrument)
	}
	if cfg.TdMode != "isolated" {
		return fmt.Errorf("swap td_mode must be isolated, got %q", cfg.TdMode)
	}
	if cfg.MaxLeverage <= 0 || cfg.MaxLeverage > 3.0 {
		return fmt.Errorf("swap max_leverage must be in (0, 3.0], got %v", cfg.MaxLeverage)
	}
	minSpread := decimal.NewFromFloat(0.0005)
	if cfg.Spread.LessThan(minSpread) {
		return fmt.Errorf("swap spread must be >= 0.05%%, got %s", cfg.Spread.String())
	}
	if !cfg.StopLoss.IsPositive() {
		return fmt.Errorf("swap stop_loss must be positive, got %s", cfg.StopLoss.String())
	}
	if !cfg.NotionalUSDT.IsPositive() {
		return fmt.Errorf("swap notional_usdt must be positive, got %s", cfg.NotionalUSDT.String())
	}
	if cfg.MaxHoldHours <= 0 {
		cfg.MaxHoldHours = 12
	}
	return nil
}
