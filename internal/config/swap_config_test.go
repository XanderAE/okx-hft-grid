package config

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestValidateSwapConfig_Valid(t *testing.T) {
	cfg := &SwapConfig{
		Instrument:   "BTC-USDT-SWAP",
		TdMode:       "isolated",
		MaxLeverage:  3.0,
		Spread:       decimal.NewFromFloat(0.003),
		StopLoss:     decimal.NewFromFloat(0.02),
		MaxHoldHours: 12,
		NotionalUSDT: decimal.NewFromFloat(50),
	}
	if err := ValidateSwapConfig(cfg); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
}

func TestValidateSwapConfig_BadInstrument(t *testing.T) {
	cfg := &SwapConfig{Instrument: "ETH-USDT-SWAP", TdMode: "isolated", MaxLeverage: 2, Spread: decimal.NewFromFloat(0.003), StopLoss: decimal.NewFromFloat(0.02), NotionalUSDT: decimal.NewFromFloat(50)}
	if err := ValidateSwapConfig(cfg); err == nil {
		t.Fatal("expected error for wrong instrument")
	}
}

func TestValidateSwapConfig_LeverageTooHigh(t *testing.T) {
	cfg := &SwapConfig{Instrument: "BTC-USDT-SWAP", TdMode: "isolated", MaxLeverage: 4.0, Spread: decimal.NewFromFloat(0.003), StopLoss: decimal.NewFromFloat(0.02), NotionalUSDT: decimal.NewFromFloat(50)}
	if err := ValidateSwapConfig(cfg); err == nil {
		t.Fatal("expected error for leverage > 3")
	}
}

func TestValidateSwapConfig_SpotRejected(t *testing.T) {
	cfg := &SwapConfig{Instrument: "BTC-USDT-SWAP", TdMode: "cash", MaxLeverage: 2, Spread: decimal.NewFromFloat(0.003), StopLoss: decimal.NewFromFloat(0.02), NotionalUSDT: decimal.NewFromFloat(50)}
	if err := ValidateSwapConfig(cfg); err == nil {
		t.Fatal("expected error for td_mode=cash")
	}
}

func TestValidateSwapConfig_SpreadTooSmall(t *testing.T) {
	cfg := &SwapConfig{Instrument: "BTC-USDT-SWAP", TdMode: "isolated", MaxLeverage: 2, Spread: decimal.NewFromFloat(0.0001), StopLoss: decimal.NewFromFloat(0.02), NotionalUSDT: decimal.NewFromFloat(50)}
	if err := ValidateSwapConfig(cfg); err == nil {
		t.Fatal("expected error for spread too small")
	}
}

func TestValidateSwapConfig_NilConfig(t *testing.T) {
	if err := ValidateSwapConfig(nil); err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestValidateSwapConfig_ZeroLeverage(t *testing.T) {
	cfg := &SwapConfig{Instrument: "BTC-USDT-SWAP", TdMode: "isolated", MaxLeverage: 0, Spread: decimal.NewFromFloat(0.003), StopLoss: decimal.NewFromFloat(0.02), NotionalUSDT: decimal.NewFromFloat(50)}
	if err := ValidateSwapConfig(cfg); err == nil {
		t.Fatal("expected error for zero leverage")
	}
}

func TestValidateSwapConfig_ZeroStopLoss(t *testing.T) {
	cfg := &SwapConfig{Instrument: "BTC-USDT-SWAP", TdMode: "isolated", MaxLeverage: 2, Spread: decimal.NewFromFloat(0.003), StopLoss: decimal.Zero, NotionalUSDT: decimal.NewFromFloat(50)}
	if err := ValidateSwapConfig(cfg); err == nil {
		t.Fatal("expected error for zero stop_loss")
	}
}

func TestValidateSwapConfig_ZeroNotional(t *testing.T) {
	cfg := &SwapConfig{Instrument: "BTC-USDT-SWAP", TdMode: "isolated", MaxLeverage: 2, Spread: decimal.NewFromFloat(0.003), StopLoss: decimal.NewFromFloat(0.02), NotionalUSDT: decimal.Zero}
	if err := ValidateSwapConfig(cfg); err == nil {
		t.Fatal("expected error for zero notional_usdt")
	}
}

func TestValidateSwapConfig_DefaultMaxHoldHours(t *testing.T) {
	cfg := &SwapConfig{
		Instrument:   "BTC-USDT-SWAP",
		TdMode:       "isolated",
		MaxLeverage:  2.0,
		Spread:       decimal.NewFromFloat(0.003),
		StopLoss:     decimal.NewFromFloat(0.02),
		MaxHoldHours: 0,
		NotionalUSDT: decimal.NewFromFloat(50),
	}
	if err := ValidateSwapConfig(cfg); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
	if cfg.MaxHoldHours != 12 {
		t.Fatalf("expected MaxHoldHours defaulted to 12, got %d", cfg.MaxHoldHours)
	}
}
