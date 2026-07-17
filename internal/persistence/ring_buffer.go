package persistence

import (
	"sort"
	"sync"

	"github.com/shopspring/decimal"
)

// DefaultCapacityPerSymbol is the default number of records per instrument.
const DefaultCapacityPerSymbol = 1_000_000

// TimeSeriesRecord represents a single time-series data point.
type TimeSeriesRecord struct {
	Timestamp int64
	Price     decimal.Decimal
	Volume    decimal.Decimal
	Symbol    string
}

// ringBuffer is a fixed-capacity circular buffer for TimeSeriesRecord entries.
type ringBuffer struct {
	data  []TimeSeriesRecord
	head  int // next write position
	count int // current number of elements
	cap   int
}

// newRingBuffer creates a ring buffer with the given capacity.
func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{
		data: make([]TimeSeriesRecord, capacity),
		cap:  capacity,
	}
}

// append adds a record to the ring buffer, overwriting the oldest if full.
func (rb *ringBuffer) append(record TimeSeriesRecord) {
	rb.data[rb.head] = record
	rb.head = (rb.head + 1) % rb.cap
	if rb.count < rb.cap {
		rb.count++
	}
}

// queryByTimeRange returns all records with Timestamp in [startTime, endTime].
// Results are returned in chronological order.
func (rb *ringBuffer) queryByTimeRange(startTime, endTime int64) []TimeSeriesRecord {
	if rb.count == 0 {
		return nil
	}

	var results []TimeSeriesRecord

	// Calculate the start index of the oldest element
	start := 0
	if rb.count == rb.cap {
		start = rb.head // oldest element is at head when full
	}

	for i := 0; i < rb.count; i++ {
		idx := (start + i) % rb.cap
		rec := rb.data[idx]
		if rec.Timestamp >= startTime && rec.Timestamp <= endTime {
			results = append(results, rec)
		}
	}

	return results
}

// TimeSeriesBuffer manages per-symbol ring buffers for time-series data storage.
// It is thread-safe for concurrent access.
type TimeSeriesBuffer struct {
	mu               sync.RWMutex
	buffers          map[string]*ringBuffer
	capacityPerSymbol int
}

// NewTimeSeriesBuffer creates a new TimeSeriesBuffer with the specified capacity per symbol.
// If capacityPerSymbol <= 0, DefaultCapacityPerSymbol is used.
func NewTimeSeriesBuffer(capacityPerSymbol int) *TimeSeriesBuffer {
	if capacityPerSymbol <= 0 {
		capacityPerSymbol = DefaultCapacityPerSymbol
	}
	return &TimeSeriesBuffer{
		buffers:          make(map[string]*ringBuffer),
		capacityPerSymbol: capacityPerSymbol,
	}
}

// Append adds a record to the ring buffer for the record's symbol.
// If the buffer for that symbol is full, the oldest record is overwritten.
func (tsb *TimeSeriesBuffer) Append(record TimeSeriesRecord) {
	tsb.mu.Lock()
	defer tsb.mu.Unlock()

	rb, ok := tsb.buffers[record.Symbol]
	if !ok {
		rb = newRingBuffer(tsb.capacityPerSymbol)
		tsb.buffers[record.Symbol] = rb
	}
	rb.append(record)
}

// QueryByTimeRange returns all records for the given symbol with Timestamp in [startTime, endTime].
// Results are sorted in chronological order (ascending timestamp).
// Returns nil if the symbol has no data.
func (tsb *TimeSeriesBuffer) QueryByTimeRange(symbol string, startTime, endTime int64) []TimeSeriesRecord {
	tsb.mu.RLock()
	defer tsb.mu.RUnlock()

	rb, ok := tsb.buffers[symbol]
	if !ok {
		return nil
	}

	results := rb.queryByTimeRange(startTime, endTime)

	// Sort results by timestamp for guaranteed chronological order
	sort.Slice(results, func(i, j int) bool {
		return results[i].Timestamp < results[j].Timestamp
	})

	return results
}

// Count returns the current number of records stored for the given symbol.
// Returns 0 if the symbol has no data.
func (tsb *TimeSeriesBuffer) Count(symbol string) int {
	tsb.mu.RLock()
	defer tsb.mu.RUnlock()

	rb, ok := tsb.buffers[symbol]
	if !ok {
		return 0
	}
	return rb.count
}

// Capacity returns the configured capacity per symbol.
func (tsb *TimeSeriesBuffer) Capacity() int {
	return tsb.capacityPerSymbol
}
