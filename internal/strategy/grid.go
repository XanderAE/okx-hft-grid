package strategy

import (
	"errors"
	"math"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

var (
	ErrInvalidGridCount  = errors.New("grid count must be at least 1")
	ErrInvalidPriceRange = errors.New("upper price must be greater than lower price")
	ErrPriceNotPositive  = errors.New("prices must be positive")
	ErrUnknownGridType   = errors.New("unknown grid type")
)

// CalculateGridLevels computes the price levels for a grid trading strategy.
// It returns exactly gridCount+1 levels in strictly ascending order.
// For arithmetic grids: levels[i] = lowerPrice + i * step, where step = (upper - lower) / gridCount.
// For geometric grids: levels[i] = lowerPrice * ratio^i, where ratio = (upper/lower)^(1/gridCount).
func CalculateGridLevels(config *models.GridConfig) ([]decimal.Decimal, error) {
	if config.GridCount < 1 {
		return nil, ErrInvalidGridCount
	}
	if !config.LowerPrice.IsPositive() || !config.UpperPrice.IsPositive() {
		return nil, ErrPriceNotPositive
	}
	if config.UpperPrice.LessThanOrEqual(config.LowerPrice) {
		return nil, ErrInvalidPriceRange
	}

	count := config.GridCount
	levels := make([]decimal.Decimal, 0, count+1)

	switch config.GridType {
	case models.GridTypeArithmetic:
		levels = calculateArithmeticLevels(config.LowerPrice, config.UpperPrice, count)
	case models.GridTypeGeometric:
		levels = calculateGeometricLevels(config.LowerPrice, config.UpperPrice, count)
	default:
		return nil, ErrUnknownGridType
	}

	// Validate output
	if err := validateLevels(levels, config.LowerPrice, config.UpperPrice, count); err != nil {
		return nil, err
	}

	return levels, nil
}

// calculateArithmeticLevels generates grid levels with equal price intervals.
// step = (upperPrice - lowerPrice) / gridCount
// levels[i] = lowerPrice + i * step
func calculateArithmeticLevels(lower, upper decimal.Decimal, count int) []decimal.Decimal {
	gridCount := decimal.NewFromInt(int64(count))
	step := upper.Sub(lower).Div(gridCount)

	levels := make([]decimal.Decimal, 0, count+1)
	for i := 0; i <= count; i++ {
		level := lower.Add(step.Mul(decimal.NewFromInt(int64(i))))
		levels = append(levels, level)
	}

	// Ensure last level exactly equals upper price to avoid floating-point drift
	levels[count] = upper

	return levels
}

// calculateGeometricLevels generates grid levels with equal price ratios.
// ratio = (upperPrice / lowerPrice) ^ (1 / gridCount)
// levels[i] = lowerPrice * ratio^i
func calculateGeometricLevels(lower, upper decimal.Decimal, count int) []decimal.Decimal {
	// Use float64 for the exponentiation since decimal doesn't natively support fractional powers.
	// The precision of float64 is sufficient for price calculations here.
	lowerF := lower.InexactFloat64()
	upperF := upper.InexactFloat64()

	ratio := math.Pow(upperF/lowerF, 1.0/float64(count))

	levels := make([]decimal.Decimal, 0, count+1)
	for i := 0; i <= count; i++ {
		var level decimal.Decimal
		if i == 0 {
			level = lower
		} else if i == count {
			level = upper
		} else {
			// levels[i] = lowerPrice * ratio^i
			multiplier := math.Pow(ratio, float64(i))
			level = decimal.NewFromFloat(lowerF * multiplier)
		}
		levels = append(levels, level)
	}

	return levels
}

// validateLevels checks that grid levels meet all invariants:
// 1. Exactly gridCount+1 levels
// 2. Strictly ascending
// 3. First level ≈ lowerPrice (within 0.01% tolerance)
// 4. Last level ≈ upperPrice (tolerance 1e-9 × priceRange)
func validateLevels(levels []decimal.Decimal, lower, upper decimal.Decimal, count int) error {
	// Check count
	if len(levels) != count+1 {
		return errors.New("incorrect number of grid levels generated")
	}

	// Check strictly ascending
	for i := 1; i < len(levels); i++ {
		if levels[i].LessThanOrEqual(levels[i-1]) {
			return errors.New("grid levels are not strictly ascending")
		}
	}

	// Check first level ≈ lower (tolerance 0.01% of lower price)
	lowerTolerance := lower.Mul(decimal.NewFromFloat(0.0001))
	if levels[0].Sub(lower).Abs().GreaterThan(lowerTolerance) {
		return errors.New("first grid level does not equal lower price")
	}

	// Check last level ≈ upper (tolerance 1e-9 × range)
	priceRange := upper.Sub(lower)
	tolerance := priceRange.Mul(decimal.NewFromFloat(1e-9))
	diff := levels[len(levels)-1].Sub(upper).Abs()
	if diff.GreaterThan(tolerance) {
		return errors.New("last grid level deviates from upper price beyond tolerance")
	}

	return nil
}
