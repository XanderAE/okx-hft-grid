package strategy

import (
	"errors"

	"github.com/shopspring/decimal"
)

// PriceVolume represents a price-volume pair stored in the ring buffer.
type PriceVolume struct {
	Price  decimal.Decimal
	Volume decimal.Decimal
}

// RingBuffer is a fixed-capacity circular buffer for storing price/volume history.
type RingBuffer struct {
	data     []PriceVolume
	capacity int
	head     int // next write position
	count    int // current number of elements
}

// NewRingBuffer creates a new RingBuffer with the given capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &RingBuffer{
		data:     make([]PriceVolume, capacity),
		capacity: capacity,
		head:     0,
		count:    0,
	}
}

// Push adds a price-volume pair to the ring buffer, overwriting the oldest entry if full.
func (rb *RingBuffer) Push(pv PriceVolume) {
	rb.data[rb.head] = pv
	rb.head = (rb.head + 1) % rb.capacity
	if rb.count < rb.capacity {
		rb.count++
	}
}

// Len returns the number of elements currently in the buffer.
func (rb *RingBuffer) Len() int {
	return rb.count
}

// Cap returns the capacity of the buffer.
func (rb *RingBuffer) Cap() int {
	return rb.capacity
}

// GetAll returns all elements in the buffer in insertion order (oldest first).
func (rb *RingBuffer) GetAll() []PriceVolume {
	if rb.count == 0 {
		return nil
	}
	result := make([]PriceVolume, rb.count)
	start := (rb.head - rb.count + rb.capacity) % rb.capacity
	for i := 0; i < rb.count; i++ {
		idx := (start + i) % rb.capacity
		result[i] = rb.data[idx]
	}
	return result
}

// GetLast returns the last n elements in insertion order (oldest first among the n).
// If n > current count, returns all available elements.
func (rb *RingBuffer) GetLast(n int) []PriceVolume {
	if n <= 0 {
		return nil
	}
	if n > rb.count {
		n = rb.count
	}
	result := make([]PriceVolume, n)
	start := (rb.head - n + rb.capacity) % rb.capacity
	for i := 0; i < n; i++ {
		idx := (start + i) % rb.capacity
		result[i] = rb.data[idx]
	}
	return result
}

// CalculateSMA computes the Simple Moving Average of the given prices.
// SMA = sum(prices) / length
// Returns zero if prices is empty.
func CalculateSMA(prices []decimal.Decimal) decimal.Decimal {
	if len(prices) == 0 {
		return decimal.Zero
	}
	sum := decimal.Zero
	for _, p := range prices {
		sum = sum.Add(p)
	}
	return sum.Div(decimal.NewFromInt(int64(len(prices))))
}

// CalculateEMA computes the Exponential Moving Average of the given prices
// with alpha = 2 / (lookbackPeriod + 1).
// The result is clamped to [MIN(prices), MAX(prices)].
// Returns zero if prices is empty or lookbackPeriod <= 0.
func CalculateEMA(prices []decimal.Decimal, lookbackPeriod int) decimal.Decimal {
	if len(prices) == 0 || lookbackPeriod <= 0 {
		return decimal.Zero
	}

	alpha := decimal.NewFromInt(2).Div(decimal.NewFromInt(int64(lookbackPeriod + 1)))
	oneMinusAlpha := decimal.NewFromInt(1).Sub(alpha)

	// Initialize EMA with first price
	ema := prices[0]

	// Find min and max for clamping
	minPrice := prices[0]
	maxPrice := prices[0]
	for _, p := range prices[1:] {
		if p.LessThan(minPrice) {
			minPrice = p
		}
		if p.GreaterThan(maxPrice) {
			maxPrice = p
		}
	}

	// Calculate EMA iteratively
	for i := 1; i < len(prices); i++ {
		// EMA = alpha * price + (1 - alpha) * prevEMA
		ema = alpha.Mul(prices[i]).Add(oneMinusAlpha.Mul(ema))
	}

	// Clamp result to [min, max]
	if ema.LessThan(minPrice) {
		ema = minPrice
	}
	if ema.GreaterThan(maxPrice) {
		ema = maxPrice
	}

	return ema
}

// CalculateVWAP computes the Volume-Weighted Average Price.
// VWAP = sum(price * volume) / sum(volume)
// Returns zero if inputs are empty, have mismatched lengths, or total volume is zero.
func CalculateVWAP(prices, volumes []decimal.Decimal) decimal.Decimal {
	if len(prices) == 0 || len(volumes) == 0 || len(prices) != len(volumes) {
		return decimal.Zero
	}

	sumPriceVolume := decimal.Zero
	sumVolume := decimal.Zero

	for i := range prices {
		sumPriceVolume = sumPriceVolume.Add(prices[i].Mul(volumes[i]))
		sumVolume = sumVolume.Add(volumes[i])
	}

	if sumVolume.IsZero() {
		return decimal.Zero
	}

	return sumPriceVolume.Div(sumVolume)
}

// MeanReversionCalculator wraps a ring buffer and provides moving average calculations.
type MeanReversionCalculator struct {
	buffer         *RingBuffer
	lookbackPeriod int
}

// NewMeanReversionCalculator creates a new calculator with the specified lookback period.
// The ring buffer capacity is set to the lookback period.
func NewMeanReversionCalculator(lookbackPeriod int) (*MeanReversionCalculator, error) {
	if lookbackPeriod <= 0 {
		return nil, errors.New("lookbackPeriod must be positive")
	}
	return &MeanReversionCalculator{
		buffer:         NewRingBuffer(lookbackPeriod),
		lookbackPeriod: lookbackPeriod,
	}, nil
}

// AddDataPoint adds a new price-volume data point to the calculator's history.
func (c *MeanReversionCalculator) AddDataPoint(price, volume decimal.Decimal) {
	c.buffer.Push(PriceVolume{Price: price, Volume: volume})
}

// DataCount returns the number of data points currently stored.
func (c *MeanReversionCalculator) DataCount() int {
	return c.buffer.Len()
}

// HasEnoughData returns true if the buffer has at least lookbackPeriod data points.
func (c *MeanReversionCalculator) HasEnoughData() bool {
	return c.buffer.Len() >= c.lookbackPeriod
}

// GetSMA calculates the Simple Moving Average over the stored price history.
func (c *MeanReversionCalculator) GetSMA() decimal.Decimal {
	data := c.buffer.GetAll()
	if len(data) == 0 {
		return decimal.Zero
	}
	prices := make([]decimal.Decimal, len(data))
	for i, d := range data {
		prices[i] = d.Price
	}
	return CalculateSMA(prices)
}

// GetEMA calculates the Exponential Moving Average over the stored price history.
func (c *MeanReversionCalculator) GetEMA() decimal.Decimal {
	data := c.buffer.GetAll()
	if len(data) == 0 {
		return decimal.Zero
	}
	prices := make([]decimal.Decimal, len(data))
	for i, d := range data {
		prices[i] = d.Price
	}
	return CalculateEMA(prices, c.lookbackPeriod)
}

// GetVWAP calculates the Volume-Weighted Average Price over the stored history.
func (c *MeanReversionCalculator) GetVWAP() decimal.Decimal {
	data := c.buffer.GetAll()
	if len(data) == 0 {
		return decimal.Zero
	}
	prices := make([]decimal.Decimal, len(data))
	volumes := make([]decimal.Decimal, len(data))
	for i, d := range data {
		prices[i] = d.Price
		volumes[i] = d.Volume
	}
	return CalculateVWAP(prices, volumes)
}
