package config

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirements 3.9, 5.10, 5.11, 9.5, 9.6, 9.7**

// --- GridConfig Property Tests ---

// TestProperty_ValidGridConfig_AcceptedByValidator tests that for any valid GridConfig
// (upperPrice > lowerPrice, gridCount in [3,500], orderSize >= minOrder),
// ValidateGridConfig returns nil.
func TestProperty_ValidGridConfig_AcceptedByValidator(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a valid GridConfig
		lowerPrice := rapid.Float64Range(0.0001, 99_999_998.0).Draw(t, "lowerPrice")
		upperPrice := rapid.Float64Range(lowerPrice+0.0001, 99_999_999.99).Draw(t, "upperPrice")
		gridCount := rapid.IntRange(3, 500).Draw(t, "gridCount")
		minOrderFloat := rapid.Float64Range(0.0001, 1000.0).Draw(t, "minOrder")
		// orderSize >= minOrder
		orderSize := rapid.Float64Range(minOrderFloat, minOrderFloat*10.0).Draw(t, "orderSize")

		cfg := &models.GridConfig{
			UpperPrice: decimal.NewFromFloat(upperPrice),
			LowerPrice: decimal.NewFromFloat(lowerPrice),
			GridCount:  gridCount,
			OrderSize:  decimal.NewFromFloat(orderSize),
		}
		minOrder := decimal.NewFromFloat(minOrderFloat)

		err := ValidateGridConfig(cfg, minOrder)
		if err != nil {
			t.Fatalf("expected nil error for valid GridConfig, got: %v\n"+
				"lowerPrice=%f, upperPrice=%f, gridCount=%d, orderSize=%f, minOrder=%f",
				err, lowerPrice, upperPrice, gridCount, orderSize, minOrderFloat)
		}
	})
}

// TestProperty_InvalidGridConfig_UpperLessOrEqualLower tests that when upperPrice <= lowerPrice,
// ValidateGridConfig returns an error.
func TestProperty_InvalidGridConfig_UpperLessOrEqualLower(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		price := rapid.Float64Range(0.01, 99_999_999.0).Draw(t, "price")
		// Generate upperPrice <= lowerPrice
		choice := rapid.IntRange(0, 1).Draw(t, "choice")
		var upperPrice, lowerPrice float64
		if choice == 0 {
			// Equal prices
			upperPrice = price
			lowerPrice = price
		} else {
			// Upper < lower
			upperPrice = price
			lowerPrice = rapid.Float64Range(price+0.0001, 99_999_999.99).Draw(t, "lowerPrice")
		}

		gridCount := rapid.IntRange(3, 500).Draw(t, "gridCount")
		minOrderFloat := rapid.Float64Range(0.0001, 100.0).Draw(t, "minOrder")
		orderSize := rapid.Float64Range(minOrderFloat, minOrderFloat*10.0).Draw(t, "orderSize")

		cfg := &models.GridConfig{
			UpperPrice: decimal.NewFromFloat(upperPrice),
			LowerPrice: decimal.NewFromFloat(lowerPrice),
			GridCount:  gridCount,
			OrderSize:  decimal.NewFromFloat(orderSize),
		}
		minOrder := decimal.NewFromFloat(minOrderFloat)

		err := ValidateGridConfig(cfg, minOrder)
		if err == nil {
			t.Fatalf("expected error for upperPrice (%f) <= lowerPrice (%f), got nil",
				upperPrice, lowerPrice)
		}
		// Verify error message mentions the specific field
		errMsg := err.Error()
		if !strings.Contains(errMsg, "upperPrice") && !strings.Contains(errMsg, "lowerPrice") {
			t.Fatalf("error message should mention 'upperPrice' or 'lowerPrice', got: %s", errMsg)
		}
	})
}

// TestProperty_InvalidGridConfig_GridCountOutOfRange tests that when gridCount < 3 or > 500,
// ValidateGridConfig returns an error.
func TestProperty_InvalidGridConfig_GridCountOutOfRange(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		lowerPrice := rapid.Float64Range(0.01, 1000.0).Draw(t, "lowerPrice")
		upperPrice := rapid.Float64Range(lowerPrice+0.01, 99_999_999.99).Draw(t, "upperPrice")
		minOrderFloat := rapid.Float64Range(0.0001, 100.0).Draw(t, "minOrder")
		orderSize := rapid.Float64Range(minOrderFloat, minOrderFloat*10.0).Draw(t, "orderSize")

		// Generate invalid gridCount: either < 3 or > 500
		choice := rapid.IntRange(0, 1).Draw(t, "choice")
		var gridCount int
		if choice == 0 {
			gridCount = rapid.IntRange(-100, 2).Draw(t, "gridCountLow")
		} else {
			gridCount = rapid.IntRange(501, 10000).Draw(t, "gridCountHigh")
		}

		cfg := &models.GridConfig{
			UpperPrice: decimal.NewFromFloat(upperPrice),
			LowerPrice: decimal.NewFromFloat(lowerPrice),
			GridCount:  gridCount,
			OrderSize:  decimal.NewFromFloat(orderSize),
		}
		minOrder := decimal.NewFromFloat(minOrderFloat)

		err := ValidateGridConfig(cfg, minOrder)
		if err == nil {
			t.Fatalf("expected error for gridCount=%d (out of [3,500]), got nil", gridCount)
		}
		// Verify error message mentions the specific field
		errMsg := err.Error()
		if !strings.Contains(errMsg, "gridCount") {
			t.Fatalf("error message should mention 'gridCount', got: %s", errMsg)
		}
	})
}

// TestProperty_InvalidGridConfig_OrderSizeBelowMinimum tests that when orderSize < minOrder,
// ValidateGridConfig returns an error.
func TestProperty_InvalidGridConfig_OrderSizeBelowMinimum(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		lowerPrice := rapid.Float64Range(0.01, 1000.0).Draw(t, "lowerPrice")
		upperPrice := rapid.Float64Range(lowerPrice+0.01, 99_999_999.99).Draw(t, "upperPrice")
		gridCount := rapid.IntRange(3, 500).Draw(t, "gridCount")

		minOrderFloat := rapid.Float64Range(0.01, 1000.0).Draw(t, "minOrder")
		// orderSize < minOrder
		orderSize := rapid.Float64Range(0.0001, minOrderFloat-0.0001).Draw(t, "orderSize")

		cfg := &models.GridConfig{
			UpperPrice: decimal.NewFromFloat(upperPrice),
			LowerPrice: decimal.NewFromFloat(lowerPrice),
			GridCount:  gridCount,
			OrderSize:  decimal.NewFromFloat(orderSize),
		}
		minOrder := decimal.NewFromFloat(minOrderFloat)

		err := ValidateGridConfig(cfg, minOrder)
		if err == nil {
			t.Fatalf("expected error for orderSize (%f) < minOrder (%f), got nil",
				orderSize, minOrderFloat)
		}
		// Verify error message mentions the specific field
		errMsg := err.Error()
		if !strings.Contains(errMsg, "orderSize") {
			t.Fatalf("error message should mention 'orderSize', got: %s", errMsg)
		}
	})
}

// --- MeanReversionConfig Property Tests ---

// TestProperty_ValidMeanReversionConfig_AcceptedByValidator tests that for any valid
// MeanReversionConfig (entry > exit, entry in [1.0,5.0], exit in [0.1,2.0], lookback in [10,500]),
// ValidateMeanReversionConfig returns nil.
func TestProperty_ValidMeanReversionConfig_AcceptedByValidator(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate valid exit first, then entry > exit
		exitThreshold := rapid.Float64Range(0.1, 2.0).Draw(t, "exitThreshold")
		// entry must be > exit AND in [1.0, 5.0]
		entryMin := exitThreshold + 0.0001
		if entryMin < 1.0 {
			entryMin = 1.0
		}
		// Ensure we have a valid range for entry (entryMin must be <= 5.0)
		if entryMin > 5.0 {
			// This can happen if exitThreshold is close to 5.0, which isn't possible
			// since exitThreshold max is 2.0, so entryMin max is ~2.0001
			t.Skip("impossible range")
		}
		entryThreshold := rapid.Float64Range(entryMin, 5.0).Draw(t, "entryThreshold")
		lookbackPeriod := rapid.IntRange(10, 500).Draw(t, "lookbackPeriod")

		cfg := &models.MeanReversionConfig{
			EntryThreshold: decimal.NewFromFloat(entryThreshold),
			ExitThreshold:  decimal.NewFromFloat(exitThreshold),
			LookbackPeriod: lookbackPeriod,
		}

		err := ValidateMeanReversionConfig(cfg)
		if err != nil {
			t.Fatalf("expected nil error for valid MeanReversionConfig, got: %v\n"+
				"entryThreshold=%f, exitThreshold=%f, lookbackPeriod=%d",
				err, entryThreshold, exitThreshold, lookbackPeriod)
		}
	})
}

// TestProperty_InvalidMeanReversionConfig_EntryNotGreaterThanExit tests that when
// entryThreshold <= exitThreshold, ValidateMeanReversionConfig returns an error.
func TestProperty_InvalidMeanReversionConfig_EntryNotGreaterThanExit(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Both in valid ranges but entry <= exit
		exitThreshold := rapid.Float64Range(1.0, 2.0).Draw(t, "exitThreshold")
		// entry <= exit, but still in [1.0, 5.0]
		entryThreshold := rapid.Float64Range(1.0, exitThreshold).Draw(t, "entryThreshold")
		lookbackPeriod := rapid.IntRange(10, 500).Draw(t, "lookbackPeriod")

		cfg := &models.MeanReversionConfig{
			EntryThreshold: decimal.NewFromFloat(entryThreshold),
			ExitThreshold:  decimal.NewFromFloat(exitThreshold),
			LookbackPeriod: lookbackPeriod,
		}

		err := ValidateMeanReversionConfig(cfg)
		if err == nil {
			t.Fatalf("expected error for entryThreshold (%f) <= exitThreshold (%f), got nil",
				entryThreshold, exitThreshold)
		}
		// Verify error message mentions the specific field
		errMsg := err.Error()
		if !strings.Contains(errMsg, "entryThreshold") && !strings.Contains(errMsg, "exitThreshold") {
			t.Fatalf("error message should mention 'entryThreshold' or 'exitThreshold', got: %s", errMsg)
		}
	})
}

// TestProperty_InvalidMeanReversionConfig_EntryOutOfBounds tests that when entryThreshold
// is outside [1.0, 5.0], ValidateMeanReversionConfig returns an error.
func TestProperty_InvalidMeanReversionConfig_EntryOutOfBounds(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		exitThreshold := rapid.Float64Range(0.1, 0.9).Draw(t, "exitThreshold")
		lookbackPeriod := rapid.IntRange(10, 500).Draw(t, "lookbackPeriod")

		// Generate entry outside [1.0, 5.0]
		choice := rapid.IntRange(0, 1).Draw(t, "choice")
		var entryThreshold float64
		if choice == 0 {
			// Below 1.0 (but > exit to isolate this check)
			entryThreshold = rapid.Float64Range(exitThreshold+0.0001, 0.9999).Draw(t, "entryLow")
		} else {
			// Above 5.0
			entryThreshold = rapid.Float64Range(5.0001, 100.0).Draw(t, "entryHigh")
		}

		cfg := &models.MeanReversionConfig{
			EntryThreshold: decimal.NewFromFloat(entryThreshold),
			ExitThreshold:  decimal.NewFromFloat(exitThreshold),
			LookbackPeriod: lookbackPeriod,
		}

		err := ValidateMeanReversionConfig(cfg)
		if err == nil {
			t.Fatalf("expected error for entryThreshold=%f outside [1.0, 5.0], got nil",
				entryThreshold)
		}
		// Verify error message mentions the specific field
		errMsg := err.Error()
		if !strings.Contains(errMsg, "entryThreshold") {
			t.Fatalf("error message should mention 'entryThreshold', got: %s", errMsg)
		}
	})
}

// TestProperty_InvalidMeanReversionConfig_ExitOutOfBounds tests that when exitThreshold
// is outside [0.1, 2.0], ValidateMeanReversionConfig returns an error.
func TestProperty_InvalidMeanReversionConfig_ExitOutOfBounds(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		lookbackPeriod := rapid.IntRange(10, 500).Draw(t, "lookbackPeriod")

		// Generate exit outside [0.1, 2.0]
		choice := rapid.IntRange(0, 1).Draw(t, "choice")
		var exitThreshold float64
		var entryThreshold float64
		if choice == 0 {
			// Below 0.1
			exitThreshold = rapid.Float64Range(0.001, 0.0999).Draw(t, "exitLow")
			entryThreshold = rapid.Float64Range(1.0, 5.0).Draw(t, "entry")
		} else {
			// Above 2.0
			exitThreshold = rapid.Float64Range(2.0001, 10.0).Draw(t, "exitHigh")
			entryThreshold = rapid.Float64Range(exitThreshold+0.0001, exitThreshold+5.0).Draw(t, "entry")
		}

		cfg := &models.MeanReversionConfig{
			EntryThreshold: decimal.NewFromFloat(entryThreshold),
			ExitThreshold:  decimal.NewFromFloat(exitThreshold),
			LookbackPeriod: lookbackPeriod,
		}

		err := ValidateMeanReversionConfig(cfg)
		if err == nil {
			t.Fatalf("expected error for exitThreshold=%f outside [0.1, 2.0], got nil",
				exitThreshold)
		}
		// Verify error message mentions the specific field
		errMsg := err.Error()
		if !strings.Contains(errMsg, "exitThreshold") {
			t.Fatalf("error message should mention 'exitThreshold', got: %s", errMsg)
		}
	})
}

// TestProperty_InvalidMeanReversionConfig_LookbackOutOfRange tests that when lookbackPeriod
// is outside [10, 500], ValidateMeanReversionConfig returns an error.
func TestProperty_InvalidMeanReversionConfig_LookbackOutOfRange(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		entryThreshold := rapid.Float64Range(1.5, 5.0).Draw(t, "entry")
		exitThreshold := rapid.Float64Range(0.1, entryThreshold-0.01).Draw(t, "exit")
		// Clamp exit to valid range for isolation
		if exitThreshold > 2.0 {
			exitThreshold = 1.5
		}

		// Generate invalid lookback: either < 10 or > 500
		choice := rapid.IntRange(0, 1).Draw(t, "choice")
		var lookbackPeriod int
		if choice == 0 {
			lookbackPeriod = rapid.IntRange(-100, 9).Draw(t, "lookbackLow")
		} else {
			lookbackPeriod = rapid.IntRange(501, 10000).Draw(t, "lookbackHigh")
		}

		cfg := &models.MeanReversionConfig{
			EntryThreshold: decimal.NewFromFloat(entryThreshold),
			ExitThreshold:  decimal.NewFromFloat(exitThreshold),
			LookbackPeriod: lookbackPeriod,
		}

		err := ValidateMeanReversionConfig(cfg)
		if err == nil {
			t.Fatalf("expected error for lookbackPeriod=%d outside [10, 500], got nil",
				lookbackPeriod)
		}
		// Verify error message mentions the specific field
		errMsg := err.Error()
		if !strings.Contains(errMsg, "lookbackPeriod") {
			t.Fatalf("error message should mention 'lookbackPeriod', got: %s", errMsg)
		}
	})
}

// --- RiskLimits Property Tests ---

// TestProperty_ValidRiskLimits_AcceptedByValidator tests that for any valid RiskLimits
// (all values positive), ValidateRiskLimits returns nil.
func TestProperty_ValidRiskLimits_AcceptedByValidator(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(rapid.Float64Range(0.01, 1_000_000.0).Draw(t, "maxPosSym")),
			MaxTotalPosition:     decimal.NewFromFloat(rapid.Float64Range(0.01, 10_000_000.0).Draw(t, "maxTotalPos")),
			MaxDailyLoss:         decimal.NewFromFloat(rapid.Float64Range(0.01, 1_000_000.0).Draw(t, "maxDailyLoss")),
			MaxStrategyLoss:      decimal.NewFromFloat(rapid.Float64Range(0.01, 1_000_000.0).Draw(t, "maxStratLoss")),
			MaxOrdersPerSecond:   rapid.IntRange(1, 1000).Draw(t, "maxOrdersSec"),
			MaxOpenOrders:        rapid.IntRange(1, 10000).Draw(t, "maxOpenOrders"),
			MinSpreadBps:         rapid.IntRange(1, 1000).Draw(t, "minSpreadBps"),
			EmergencyStopLoss:    decimal.NewFromFloat(rapid.Float64Range(0.01, 10_000_000.0).Draw(t, "emergencyStop")),
		}

		err := ValidateRiskLimits(limits)
		if err != nil {
			t.Fatalf("expected nil error for valid RiskLimits, got: %v", err)
		}
	})
}

// TestProperty_InvalidRiskLimits_NonPositiveDecimalField tests that when any decimal field
// is zero or negative, ValidateRiskLimits returns an error.
func TestProperty_InvalidRiskLimits_NonPositiveDecimalField(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Start with a valid config, then corrupt one decimal field
		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(rapid.Float64Range(0.01, 1_000_000.0).Draw(t, "maxPosSym")),
			MaxTotalPosition:     decimal.NewFromFloat(rapid.Float64Range(0.01, 10_000_000.0).Draw(t, "maxTotalPos")),
			MaxDailyLoss:         decimal.NewFromFloat(rapid.Float64Range(0.01, 1_000_000.0).Draw(t, "maxDailyLoss")),
			MaxStrategyLoss:      decimal.NewFromFloat(rapid.Float64Range(0.01, 1_000_000.0).Draw(t, "maxStratLoss")),
			MaxOrdersPerSecond:   rapid.IntRange(1, 1000).Draw(t, "maxOrdersSec"),
			MaxOpenOrders:        rapid.IntRange(1, 10000).Draw(t, "maxOpenOrders"),
			MinSpreadBps:         rapid.IntRange(1, 1000).Draw(t, "minSpreadBps"),
			EmergencyStopLoss:    decimal.NewFromFloat(rapid.Float64Range(0.01, 10_000_000.0).Draw(t, "emergencyStop")),
		}

		// Pick which decimal field to corrupt
		fieldIdx := rapid.IntRange(0, 4).Draw(t, "fieldIdx")
		// Generate non-positive value
		badValue := decimal.NewFromFloat(rapid.Float64Range(-1_000_000.0, 0.0).Draw(t, "badValue"))

		switch fieldIdx {
		case 0:
			limits.MaxPositionPerSymbol = badValue
		case 1:
			limits.MaxTotalPosition = badValue
		case 2:
			limits.MaxDailyLoss = badValue
		case 3:
			limits.MaxStrategyLoss = badValue
		case 4:
			limits.EmergencyStopLoss = badValue
		}

		err := ValidateRiskLimits(limits)
		if err == nil {
			t.Fatalf("expected error for non-positive decimal field (index=%d, value=%s), got nil",
				fieldIdx, badValue.String())
		}
		// Verify error message mentions the corrupted field
		errMsg := err.Error()
		fieldNames := []string{"maxPositionPerSymbol", "maxTotalPosition", "maxDailyLoss", "maxStrategyLoss", "emergencyStopLoss"}
		if !strings.Contains(errMsg, fieldNames[fieldIdx]) {
			t.Fatalf("error message should mention '%s', got: %s", fieldNames[fieldIdx], errMsg)
		}
	})
}

// TestProperty_InvalidRiskLimits_NonPositiveIntField tests that when any integer field
// is zero or negative, ValidateRiskLimits returns an error.
func TestProperty_InvalidRiskLimits_NonPositiveIntField(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Start with a valid config, then corrupt one int field
		limits := &models.RiskLimits{
			MaxPositionPerSymbol: decimal.NewFromFloat(rapid.Float64Range(0.01, 1_000_000.0).Draw(t, "maxPosSym")),
			MaxTotalPosition:     decimal.NewFromFloat(rapid.Float64Range(0.01, 10_000_000.0).Draw(t, "maxTotalPos")),
			MaxDailyLoss:         decimal.NewFromFloat(rapid.Float64Range(0.01, 1_000_000.0).Draw(t, "maxDailyLoss")),
			MaxStrategyLoss:      decimal.NewFromFloat(rapid.Float64Range(0.01, 1_000_000.0).Draw(t, "maxStratLoss")),
			MaxOrdersPerSecond:   rapid.IntRange(1, 1000).Draw(t, "maxOrdersSec"),
			MaxOpenOrders:        rapid.IntRange(1, 10000).Draw(t, "maxOpenOrders"),
			MinSpreadBps:         rapid.IntRange(1, 1000).Draw(t, "minSpreadBps"),
			EmergencyStopLoss:    decimal.NewFromFloat(rapid.Float64Range(0.01, 10_000_000.0).Draw(t, "emergencyStop")),
		}

		// Pick which int field to corrupt
		fieldIdx := rapid.IntRange(0, 2).Draw(t, "fieldIdx")
		// Generate non-positive int value
		badValue := rapid.IntRange(-1000, 0).Draw(t, "badValue")

		switch fieldIdx {
		case 0:
			limits.MaxOrdersPerSecond = badValue
		case 1:
			limits.MaxOpenOrders = badValue
		case 2:
			limits.MinSpreadBps = badValue
		}

		err := ValidateRiskLimits(limits)
		if err == nil {
			t.Fatalf("expected error for non-positive int field (index=%d, value=%d), got nil",
				fieldIdx, badValue)
		}
		// Verify error message mentions the corrupted field
		errMsg := err.Error()
		fieldNames := []string{"maxOrdersPerSecond", "maxOpenOrders", "minSpreadBps"}
		if !strings.Contains(errMsg, fieldNames[fieldIdx]) {
			t.Fatalf("error message should mention '%s', got: %s", fieldNames[fieldIdx], errMsg)
		}
	})
}
