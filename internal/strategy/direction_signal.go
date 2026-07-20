package strategy

import "github.com/shopspring/decimal"

type Direction int

const (
	Flat Direction = iota
	Long
	Short
)

func (d Direction) String() string {
	switch d {
	case Long:
		return "long"
	case Short:
		return "short"
	default:
		return "flat"
	}
}

// DirectionSignal maintains short/long EMAs over a price series and emits
// a directional signal with a confidence score. Pure and deterministic.
type DirectionSignal struct {
	shortWindow int
	longWindow  int
	shortEMA    decimal.Decimal
	longEMA     decimal.Decimal
	prevShort   decimal.Decimal
	count       int
	prices      []decimal.Decimal // recent prices for ATR/confidence
	maxPrices   int
}

func NewDirectionSignal(shortWindow, longWindow int) *DirectionSignal {
	if shortWindow < 2 {
		shortWindow = 5
	}
	if longWindow <= shortWindow {
		longWindow = shortWindow * 4
	}
	return &DirectionSignal{
		shortWindow: shortWindow,
		longWindow:  longWindow,
		maxPrices:   longWindow * 2,
	}
}

// Update ingests one new price observation.
func (d *DirectionSignal) Update(price decimal.Decimal) {
	if !price.IsPositive() {
		return
	}
	d.count++
	d.prices = append(d.prices, price)
	if len(d.prices) > d.maxPrices {
		d.prices = d.prices[len(d.prices)-d.maxPrices:]
	}
	kShort := decimal.NewFromFloat(2.0 / float64(d.shortWindow+1))
	kLong := decimal.NewFromFloat(2.0 / float64(d.longWindow+1))
	if d.count == 1 {
		d.shortEMA = price
		d.longEMA = price
		d.prevShort = price
		return
	}
	d.prevShort = d.shortEMA
	one := decimal.NewFromInt(1)
	d.shortEMA = price.Mul(kShort).Add(d.shortEMA.Mul(one.Sub(kShort)))
	d.longEMA = price.Mul(kLong).Add(d.longEMA.Mul(one.Sub(kLong)))
}

// Evaluate returns the current direction and a confidence in [0,1].
func (d *DirectionSignal) Evaluate() (Direction, float64) {
	// Warmup: need at least longWindow samples
	if d.count < d.longWindow {
		return Flat, 0
	}
	gap := d.shortEMA.Sub(d.longEMA)
	slope := d.shortEMA.Sub(d.prevShort)

	// Confidence: |gap| relative to recent price scale, clamped [0,1]
	conf := 0.0
	if len(d.prices) > 0 {
		scale := d.longEMA.Abs()
		if scale.IsPositive() {
			c, _ := gap.Abs().Div(scale).Float64()
			conf = c * 50.0 // scale small relative gaps into a usable range
			if conf > 1.0 {
				conf = 1.0
			}
			if conf < 0.0 {
				conf = 0.0
			}
		}
	}

	if gap.IsPositive() && slope.IsPositive() {
		return Long, conf
	}
	if gap.IsNegative() && slope.IsNegative() {
		return Short, conf
	}
	return Flat, conf
}
