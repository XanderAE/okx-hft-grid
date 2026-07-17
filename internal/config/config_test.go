package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

func TestValidateGridConfig_Valid(t *testing.T) {
	cfg := &models.GridConfig{
		UpperPrice: decimal.NewFromFloat(100.0),
		LowerPrice: decimal.NewFromFloat(50.0),
		GridCount:  10,
		OrderSize:  decimal.NewFromFloat(1.0),
	}
	minOrder := decimal.NewFromFloat(0.5)

	err := ValidateGridConfig(cfg, minOrder)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateGridConfig_UpperPriceLessThanLowerPrice(t *testing.T) {
	cfg := &models.GridConfig{
		UpperPrice: decimal.NewFromFloat(50.0),
		LowerPrice: decimal.NewFromFloat(100.0),
		GridCount:  10,
		OrderSize:  decimal.NewFromFloat(1.0),
	}
	minOrder := decimal.NewFromFloat(0.5)

	err := ValidateGridConfig(cfg, minOrder)
	if err == nil {
		t.Fatal("expected error for upperPrice < lowerPrice")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatal("expected ValidationError type")
	}
	if len(ve.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(ve.Errors), ve.Errors)
	}
}

func TestValidateGridConfig_EqualPrices(t *testing.T) {
	cfg := &models.GridConfig{
		UpperPrice: decimal.NewFromFloat(100.0),
		LowerPrice: decimal.NewFromFloat(100.0),
		GridCount:  10,
		OrderSize:  decimal.NewFromFloat(1.0),
	}
	minOrder := decimal.NewFromFloat(0.5)

	err := ValidateGridConfig(cfg, minOrder)
	if err == nil {
		t.Fatal("expected error for upperPrice == lowerPrice")
	}
}

func TestValidateGridConfig_GridCountTooLow(t *testing.T) {
	cfg := &models.GridConfig{
		UpperPrice: decimal.NewFromFloat(100.0),
		LowerPrice: decimal.NewFromFloat(50.0),
		GridCount:  2,
		OrderSize:  decimal.NewFromFloat(1.0),
	}
	minOrder := decimal.NewFromFloat(0.5)

	err := ValidateGridConfig(cfg, minOrder)
	if err == nil {
		t.Fatal("expected error for gridCount < 3")
	}
}

func TestValidateGridConfig_GridCountTooHigh(t *testing.T) {
	cfg := &models.GridConfig{
		UpperPrice: decimal.NewFromFloat(100.0),
		LowerPrice: decimal.NewFromFloat(50.0),
		GridCount:  501,
		OrderSize:  decimal.NewFromFloat(1.0),
	}
	minOrder := decimal.NewFromFloat(0.5)

	err := ValidateGridConfig(cfg, minOrder)
	if err == nil {
		t.Fatal("expected error for gridCount > 500")
	}
}

func TestValidateGridConfig_GridCountBoundary(t *testing.T) {
	minOrder := decimal.NewFromFloat(0.5)

	// gridCount = 3 should be valid
	cfg3 := &models.GridConfig{
		UpperPrice: decimal.NewFromFloat(100.0),
		LowerPrice: decimal.NewFromFloat(50.0),
		GridCount:  3,
		OrderSize:  decimal.NewFromFloat(1.0),
	}
	if err := ValidateGridConfig(cfg3, minOrder); err != nil {
		t.Fatalf("gridCount=3 should be valid, got: %v", err)
	}

	// gridCount = 500 should be valid
	cfg500 := &models.GridConfig{
		UpperPrice: decimal.NewFromFloat(100.0),
		LowerPrice: decimal.NewFromFloat(50.0),
		GridCount:  500,
		OrderSize:  decimal.NewFromFloat(1.0),
	}
	if err := ValidateGridConfig(cfg500, minOrder); err != nil {
		t.Fatalf("gridCount=500 should be valid, got: %v", err)
	}
}

func TestValidateGridConfig_OrderSizeBelowMinimum(t *testing.T) {
	cfg := &models.GridConfig{
		UpperPrice: decimal.NewFromFloat(100.0),
		LowerPrice: decimal.NewFromFloat(50.0),
		GridCount:  10,
		OrderSize:  decimal.NewFromFloat(0.001),
	}
	minOrder := decimal.NewFromFloat(0.01)

	err := ValidateGridConfig(cfg, minOrder)
	if err == nil {
		t.Fatal("expected error for orderSize < exchange minimum")
	}
}

func TestValidateGridConfig_OrderSizeEqualMinimum(t *testing.T) {
	cfg := &models.GridConfig{
		UpperPrice: decimal.NewFromFloat(100.0),
		LowerPrice: decimal.NewFromFloat(50.0),
		GridCount:  10,
		OrderSize:  decimal.NewFromFloat(0.01),
	}
	minOrder := decimal.NewFromFloat(0.01)

	err := ValidateGridConfig(cfg, minOrder)
	if err != nil {
		t.Fatalf("orderSize == exchange minimum should be valid, got: %v", err)
	}
}

func TestValidateGridConfig_MultipleErrors(t *testing.T) {
	cfg := &models.GridConfig{
		UpperPrice: decimal.NewFromFloat(50.0),
		LowerPrice: decimal.NewFromFloat(100.0),
		GridCount:  1,
		OrderSize:  decimal.NewFromFloat(0.001),
	}
	minOrder := decimal.NewFromFloat(0.01)

	err := ValidateGridConfig(cfg, minOrder)
	if err == nil {
		t.Fatal("expected error")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatal("expected ValidationError type")
	}
	if len(ve.Errors) != 3 {
		t.Fatalf("expected 3 errors, got %d: %v", len(ve.Errors), ve.Errors)
	}
}

func TestValidateMeanReversionConfig_Valid(t *testing.T) {
	cfg := &models.MeanReversionConfig{
		EntryThreshold: decimal.NewFromFloat(2.0),
		ExitThreshold:  decimal.NewFromFloat(0.5),
		LookbackPeriod: 100,
	}

	err := ValidateMeanReversionConfig(cfg)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateMeanReversionConfig_EntryNotGreaterThanExit(t *testing.T) {
	cfg := &models.MeanReversionConfig{
		EntryThreshold: decimal.NewFromFloat(0.5),
		ExitThreshold:  decimal.NewFromFloat(1.0),
		LookbackPeriod: 100,
	}

	err := ValidateMeanReversionConfig(cfg)
	if err == nil {
		t.Fatal("expected error for entry <= exit")
	}
}

func TestValidateMeanReversionConfig_EntryEqualExit(t *testing.T) {
	cfg := &models.MeanReversionConfig{
		EntryThreshold: decimal.NewFromFloat(1.5),
		ExitThreshold:  decimal.NewFromFloat(1.5),
		LookbackPeriod: 100,
	}

	err := ValidateMeanReversionConfig(cfg)
	if err == nil {
		t.Fatal("expected error for entry == exit")
	}
}

func TestValidateMeanReversionConfig_EntryBelowMin(t *testing.T) {
	cfg := &models.MeanReversionConfig{
		EntryThreshold: decimal.NewFromFloat(0.5),
		ExitThreshold:  decimal.NewFromFloat(0.2),
		LookbackPeriod: 100,
	}

	err := ValidateMeanReversionConfig(cfg)
	if err == nil {
		t.Fatal("expected error for entry < 1.0")
	}
}

func TestValidateMeanReversionConfig_EntryAboveMax(t *testing.T) {
	cfg := &models.MeanReversionConfig{
		EntryThreshold: decimal.NewFromFloat(6.0),
		ExitThreshold:  decimal.NewFromFloat(0.5),
		LookbackPeriod: 100,
	}

	err := ValidateMeanReversionConfig(cfg)
	if err == nil {
		t.Fatal("expected error for entry > 5.0")
	}
}

func TestValidateMeanReversionConfig_ExitBelowMin(t *testing.T) {
	cfg := &models.MeanReversionConfig{
		EntryThreshold: decimal.NewFromFloat(2.0),
		ExitThreshold:  decimal.NewFromFloat(0.05),
		LookbackPeriod: 100,
	}

	err := ValidateMeanReversionConfig(cfg)
	if err == nil {
		t.Fatal("expected error for exit < 0.1")
	}
}

func TestValidateMeanReversionConfig_ExitAboveMax(t *testing.T) {
	cfg := &models.MeanReversionConfig{
		EntryThreshold: decimal.NewFromFloat(3.0),
		ExitThreshold:  decimal.NewFromFloat(2.5),
		LookbackPeriod: 100,
	}

	err := ValidateMeanReversionConfig(cfg)
	if err == nil {
		t.Fatal("expected error for exit > 2.0")
	}
}

func TestValidateMeanReversionConfig_LookbackTooLow(t *testing.T) {
	cfg := &models.MeanReversionConfig{
		EntryThreshold: decimal.NewFromFloat(2.0),
		ExitThreshold:  decimal.NewFromFloat(0.5),
		LookbackPeriod: 5,
	}

	err := ValidateMeanReversionConfig(cfg)
	if err == nil {
		t.Fatal("expected error for lookback < 10")
	}
}

func TestValidateMeanReversionConfig_LookbackTooHigh(t *testing.T) {
	cfg := &models.MeanReversionConfig{
		EntryThreshold: decimal.NewFromFloat(2.0),
		ExitThreshold:  decimal.NewFromFloat(0.5),
		LookbackPeriod: 600,
	}

	err := ValidateMeanReversionConfig(cfg)
	if err == nil {
		t.Fatal("expected error for lookback > 500")
	}
}

func TestValidateMeanReversionConfig_LookbackBoundary(t *testing.T) {
	// lookback = 10 should be valid
	cfg10 := &models.MeanReversionConfig{
		EntryThreshold: decimal.NewFromFloat(2.0),
		ExitThreshold:  decimal.NewFromFloat(0.5),
		LookbackPeriod: 10,
	}
	if err := ValidateMeanReversionConfig(cfg10); err != nil {
		t.Fatalf("lookback=10 should be valid, got: %v", err)
	}

	// lookback = 500 should be valid
	cfg500 := &models.MeanReversionConfig{
		EntryThreshold: decimal.NewFromFloat(2.0),
		ExitThreshold:  decimal.NewFromFloat(0.5),
		LookbackPeriod: 500,
	}
	if err := ValidateMeanReversionConfig(cfg500); err != nil {
		t.Fatalf("lookback=500 should be valid, got: %v", err)
	}
}

func TestValidateMeanReversionConfig_MultipleErrors(t *testing.T) {
	cfg := &models.MeanReversionConfig{
		EntryThreshold: decimal.NewFromFloat(0.05),
		ExitThreshold:  decimal.NewFromFloat(3.0),
		LookbackPeriod: 2,
	}

	err := ValidateMeanReversionConfig(cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatal("expected ValidationError type")
	}
	// entry <= exit, entry < 1.0, exit > 2.0, lookback < 10
	if len(ve.Errors) < 3 {
		t.Fatalf("expected at least 3 errors, got %d: %v", len(ve.Errors), ve.Errors)
	}
}

func TestValidateRiskLimits_Valid(t *testing.T) {
	limits := &models.RiskLimits{
		MaxPositionPerSymbol: decimal.NewFromFloat(10000),
		MaxTotalPosition:     decimal.NewFromFloat(50000),
		MaxDailyLoss:         decimal.NewFromFloat(1000),
		MaxStrategyLoss:      decimal.NewFromFloat(500),
		MaxOrdersPerSecond:   10,
		MaxOpenOrders:        50,
		MinSpreadBps:         5,
		EmergencyStopLoss:    decimal.NewFromFloat(5000),
	}

	err := ValidateRiskLimits(limits)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateRiskLimits_ZeroDecimalFields(t *testing.T) {
	limits := &models.RiskLimits{
		MaxPositionPerSymbol: decimal.Zero,
		MaxTotalPosition:     decimal.Zero,
		MaxDailyLoss:         decimal.Zero,
		MaxStrategyLoss:      decimal.Zero,
		MaxOrdersPerSecond:   10,
		MaxOpenOrders:        50,
		MinSpreadBps:         5,
		EmergencyStopLoss:    decimal.Zero,
	}

	err := ValidateRiskLimits(limits)
	if err == nil {
		t.Fatal("expected error for zero-valued fields")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatal("expected ValidationError type")
	}
	if len(ve.Errors) != 5 {
		t.Fatalf("expected 5 errors, got %d: %v", len(ve.Errors), ve.Errors)
	}
}

func TestValidateRiskLimits_NegativeDecimalFields(t *testing.T) {
	limits := &models.RiskLimits{
		MaxPositionPerSymbol: decimal.NewFromFloat(-100),
		MaxTotalPosition:     decimal.NewFromFloat(-500),
		MaxDailyLoss:         decimal.NewFromFloat(-50),
		MaxStrategyLoss:      decimal.NewFromFloat(-25),
		MaxOrdersPerSecond:   10,
		MaxOpenOrders:        50,
		MinSpreadBps:         5,
		EmergencyStopLoss:    decimal.NewFromFloat(-1000),
	}

	err := ValidateRiskLimits(limits)
	if err == nil {
		t.Fatal("expected error for negative fields")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatal("expected ValidationError type")
	}
	if len(ve.Errors) != 5 {
		t.Fatalf("expected 5 errors, got %d: %v", len(ve.Errors), ve.Errors)
	}
}

func TestValidateRiskLimits_ZeroIntFields(t *testing.T) {
	limits := &models.RiskLimits{
		MaxPositionPerSymbol: decimal.NewFromFloat(10000),
		MaxTotalPosition:     decimal.NewFromFloat(50000),
		MaxDailyLoss:         decimal.NewFromFloat(1000),
		MaxStrategyLoss:      decimal.NewFromFloat(500),
		MaxOrdersPerSecond:   0,
		MaxOpenOrders:        0,
		MinSpreadBps:         0,
		EmergencyStopLoss:    decimal.NewFromFloat(5000),
	}

	err := ValidateRiskLimits(limits)
	if err == nil {
		t.Fatal("expected error for zero int fields")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatal("expected ValidationError type")
	}
	if len(ve.Errors) != 3 {
		t.Fatalf("expected 3 errors, got %d: %v", len(ve.Errors), ve.Errors)
	}
}

func TestValidateRiskLimits_NegativeIntFields(t *testing.T) {
	limits := &models.RiskLimits{
		MaxPositionPerSymbol: decimal.NewFromFloat(10000),
		MaxTotalPosition:     decimal.NewFromFloat(50000),
		MaxDailyLoss:         decimal.NewFromFloat(1000),
		MaxStrategyLoss:      decimal.NewFromFloat(500),
		MaxOrdersPerSecond:   -1,
		MaxOpenOrders:        -5,
		MinSpreadBps:         -2,
		EmergencyStopLoss:    decimal.NewFromFloat(5000),
	}

	err := ValidateRiskLimits(limits)
	if err == nil {
		t.Fatal("expected error for negative int fields")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatal("expected ValidationError type")
	}
	if len(ve.Errors) != 3 {
		t.Fatalf("expected 3 errors, got %d: %v", len(ve.Errors), ve.Errors)
	}
}

func TestLoadConfig_ValidYAML(t *testing.T) {
	yamlContent := `
symbols:
  - "BTC-USDT"
  - "ETH-USDT"
websocket_url: "wss://ws.okx.com:8443/ws/v5/public"
rest_url: "https://www.okx.com"
reconcile_interval_sec: 60
persistence_path: "/data/state.db"
metrics_port: 9090
risk_limits:
  max_position_per_symbol: "10000"
  max_total_position: "50000"
  max_daily_loss: "1000"
  max_strategy_loss: "500"
  max_orders_per_second: 10
  max_open_orders: 50
  min_spread_bps: 5
  emergency_stop_loss: "5000"
grid_configs:
  - symbol: "BTC-USDT"
    upper_price: "50000"
    lower_price: "40000"
    grid_count: 20
    grid_type: 0
    order_size: "0.01"
    max_position: "1"
mean_reversion_configs:
  - symbol: "ETH-USDT"
    lookback_period: 100
    entry_threshold: "2.0"
    exit_threshold: "0.5"
    ma_type: 1
    order_size: "0.1"
    max_position: "10"
    cooldown_ms: 5000
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if len(cfg.Symbols) != 2 {
		t.Fatalf("expected 2 symbols, got %d", len(cfg.Symbols))
	}
	if cfg.Symbols[0] != "BTC-USDT" {
		t.Fatalf("expected BTC-USDT, got %s", cfg.Symbols[0])
	}
	if cfg.WebSocketURL != "wss://ws.okx.com:8443/ws/v5/public" {
		t.Fatalf("unexpected websocket_url: %s", cfg.WebSocketURL)
	}
	if cfg.MetricsPort != 9090 {
		t.Fatalf("expected metrics_port 9090, got %d", cfg.MetricsPort)
	}
	if len(cfg.GridConfigs) != 1 {
		t.Fatalf("expected 1 grid config, got %d", len(cfg.GridConfigs))
	}
	if cfg.GridConfigs[0].GridCount != 20 {
		t.Fatalf("expected grid_count 20, got %d", cfg.GridConfigs[0].GridCount)
	}
	if len(cfg.MeanReversionConfigs) != 1 {
		t.Fatalf("expected 1 mean reversion config, got %d", len(cfg.MeanReversionConfigs))
	}
	if cfg.MeanReversionConfigs[0].LookbackPeriod != 100 {
		t.Fatalf("expected lookback_period 100, got %d", cfg.MeanReversionConfigs[0].LookbackPeriod)
	}
	if !cfg.RiskLimits.MaxDailyLoss.Equal(decimal.NewFromFloat(1000)) {
		t.Fatalf("expected max_daily_loss 1000, got %s", cfg.RiskLimits.MaxDailyLoss.String())
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "bad.yaml")
	if err := os.WriteFile(cfgPath, []byte("invalid: [yaml: content"), 0600); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestValidationError_ErrorString(t *testing.T) {
	ve := &ValidationError{}
	ve.Add("field1 is invalid")
	ve.Add("field2 is out of range")

	errStr := ve.Error()
	if errStr != "validation failed: field1 is invalid; field2 is out of range" {
		t.Fatalf("unexpected error string: %s", errStr)
	}
}

func TestValidationError_HasErrors(t *testing.T) {
	ve := &ValidationError{}
	if ve.HasErrors() {
		t.Fatal("expected no errors initially")
	}
	ve.Add("some error")
	if !ve.HasErrors() {
		t.Fatal("expected errors after adding one")
	}
}
