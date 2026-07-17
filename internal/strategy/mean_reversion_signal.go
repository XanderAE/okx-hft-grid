package strategy

import (
	"math"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// CalculateStdDev computes the population standard deviation of the given prices
// relative to the provided mean.
// stdDev = sqrt( sum((price - mean)^2) / n )
// Returns zero if prices is empty.
func CalculateStdDev(prices []decimal.Decimal, mean decimal.Decimal) decimal.Decimal {
	n := len(prices)
	if n == 0 {
		return decimal.Zero
	}

	sumSquares := decimal.Zero
	for _, p := range prices {
		diff := p.Sub(mean)
		sumSquares = sumSquares.Add(diff.Mul(diff))
	}

	variance := sumSquares.Div(decimal.NewFromInt(int64(n)))

	// Newton's method for square root with decimal precision
	if variance.IsZero() {
		return decimal.Zero
	}

	// Use float approximation as initial guess, then refine
	varianceFloat, _ := variance.Float64()
	if varianceFloat <= 0 {
		return decimal.Zero
	}

	// Newton's method: x_{n+1} = (x_n + variance/x_n) / 2
	two := decimal.NewFromInt(2)
	x := decimal.NewFromFloat(math.Sqrt(varianceFloat))

	// Iterate Newton's method for precision
	for i := 0; i < 20; i++ {
		if x.IsZero() {
			return decimal.Zero
		}
		xNew := x.Add(variance.Div(x)).Div(two)
		if xNew.Equal(x) {
			break
		}
		x = xNew
	}

	return x
}

// CalculateZScore computes Z_Score = (currentPrice - mean) / stdDev.
// Returns (zscore, valid). If stdDev is zero, returns (zero, false).
func CalculateZScore(currentPrice, mean, stdDev decimal.Decimal) (decimal.Decimal, bool) {
	if stdDev.IsZero() {
		return decimal.Zero, false
	}
	zscore := currentPrice.Sub(mean).Div(stdDev)
	return zscore, true
}

// SignalGeneratorConfig holds the configuration for signal generation.
type SignalGeneratorConfig struct {
	EntryThreshold decimal.Decimal
	ExitThreshold  decimal.Decimal
	CooldownMs     int
	LookbackPeriod int
	MAType         models.MAType
}

// SignalGenerator generates mean reversion trading signals based on Z-Score calculations.
type SignalGenerator struct {
	config     SignalGeneratorConfig
	calculator *MeanReversionCalculator

	lastSignal     models.SignalDirection
	lastSignalTime time.Time
}

// NewSignalGenerator creates a new SignalGenerator with the given configuration.
// CooldownMs is clamped to [100, 60000].
func NewSignalGenerator(config SignalGeneratorConfig) *SignalGenerator {
	if config.CooldownMs < 100 {
		config.CooldownMs = 100
	}
	if config.CooldownMs > 60000 {
		config.CooldownMs = 60000
	}

	calc, _ := NewMeanReversionCalculator(config.LookbackPeriod)

	return &SignalGenerator{
		config:     config,
		calculator: calc,
		lastSignal: models.SignalDirectionNone,
	}
}

// AddDataPoint adds a price-volume data point to the internal buffer.
func (sg *SignalGenerator) AddDataPoint(price, volume decimal.Decimal) {
	sg.calculator.AddDataPoint(price, volume)
}

// GenerateSignal evaluates the current price and returns a signal direction.
// It enforces lookback period requirements, stdDev=0 suppression, cooldown, and Z-Score thresholds.
func (sg *SignalGenerator) GenerateSignal(currentPrice decimal.Decimal, now time.Time) models.SignalDirection {
	// Suppress signal if not enough data
	if !sg.calculator.HasEnoughData() {
		return sg.lastSignal
	}

	// Calculate the mean based on configured MA type
	mean := sg.getMean()

	// Get all prices from the buffer to calculate stdDev
	data := sg.calculator.buffer.GetAll()
	prices := make([]decimal.Decimal, len(data))
	for i, d := range data {
		prices[i] = d.Price
	}

	stdDev := CalculateStdDev(prices, mean)

	// If stdDev is zero, suppress signal generation (retain previous signal)
	zscore, valid := CalculateZScore(currentPrice, mean, stdDev)
	if !valid {
		return sg.lastSignal
	}

	// Determine the raw signal from Z-Score
	var newSignal models.SignalDirection
	negEntryThreshold := sg.config.EntryThreshold.Neg()

	if zscore.LessThan(negEntryThreshold) {
		newSignal = models.SignalDirectionBuy
	} else if zscore.GreaterThan(sg.config.EntryThreshold) {
		newSignal = models.SignalDirectionSell
	} else if zscore.Abs().LessThan(sg.config.ExitThreshold) {
		newSignal = models.SignalDirectionClose
	} else {
		// In the dead zone between exitThreshold and entryThreshold, retain previous signal
		return sg.lastSignal
	}

	// If signal hasn't changed, no cooldown check needed
	if newSignal == sg.lastSignal {
		return sg.lastSignal
	}

	// Enforce cooldown: no new signal within cooldownMs of last signal change
	cooldownDuration := time.Duration(sg.config.CooldownMs) * time.Millisecond
	if !sg.lastSignalTime.IsZero() && now.Sub(sg.lastSignalTime) < cooldownDuration {
		return sg.lastSignal
	}

	// Update signal state
	sg.lastSignal = newSignal
	sg.lastSignalTime = now

	return newSignal
}

// getMean returns the moving average based on the configured MA type.
func (sg *SignalGenerator) getMean() decimal.Decimal {
	switch sg.config.MAType {
	case models.MATypeEMA:
		return sg.calculator.GetEMA()
	case models.MATypeVWAP:
		return sg.calculator.GetVWAP()
	default:
		return sg.calculator.GetSMA()
	}
}

// LastSignal returns the most recent signal direction.
func (sg *SignalGenerator) LastSignal() models.SignalDirection {
	return sg.lastSignal
}

// LastSignalTime returns the time of the most recent signal change.
func (sg *SignalGenerator) LastSignalTime() time.Time {
	return sg.lastSignalTime
}
