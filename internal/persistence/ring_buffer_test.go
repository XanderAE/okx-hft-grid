package persistence

import (
	"sync"
	"testing"

	"github.com/shopspring/decimal"
)

func makeRecord(symbol string, ts int64, price, volume float64) TimeSeriesRecord {
	return TimeSeriesRecord{
		Timestamp: ts,
		Price:     decimal.NewFromFloat(price),
		Volume:    decimal.NewFromFloat(volume),
		Symbol:    symbol,
	}
}

func TestNewTimeSeriesBuffer_DefaultCapacity(t *testing.T) {
	tsb := NewTimeSeriesBuffer(0)
	if tsb.Capacity() != DefaultCapacityPerSymbol {
		t.Errorf("expected default capacity %d, got %d", DefaultCapacityPerSymbol, tsb.Capacity())
	}
}

func TestNewTimeSeriesBuffer_NegativeCapacity(t *testing.T) {
	tsb := NewTimeSeriesBuffer(-5)
	if tsb.Capacity() != DefaultCapacityPerSymbol {
		t.Errorf("expected default capacity %d, got %d", DefaultCapacityPerSymbol, tsb.Capacity())
	}
}

func TestNewTimeSeriesBuffer_CustomCapacity(t *testing.T) {
	tsb := NewTimeSeriesBuffer(500)
	if tsb.Capacity() != 500 {
		t.Errorf("expected capacity 500, got %d", tsb.Capacity())
	}
}

func TestAppend_SingleRecord(t *testing.T) {
	tsb := NewTimeSeriesBuffer(10)
	rec := makeRecord("BTC-USDT", 1000, 50000.0, 1.5)
	tsb.Append(rec)

	if tsb.Count("BTC-USDT") != 1 {
		t.Errorf("expected count 1, got %d", tsb.Count("BTC-USDT"))
	}
}

func TestAppend_MultipleSymbols(t *testing.T) {
	tsb := NewTimeSeriesBuffer(10)

	tsb.Append(makeRecord("BTC-USDT", 1000, 50000.0, 1.5))
	tsb.Append(makeRecord("ETH-USDT", 1001, 3000.0, 10.0))
	tsb.Append(makeRecord("BTC-USDT", 1002, 50100.0, 2.0))

	if tsb.Count("BTC-USDT") != 2 {
		t.Errorf("expected BTC count 2, got %d", tsb.Count("BTC-USDT"))
	}
	if tsb.Count("ETH-USDT") != 1 {
		t.Errorf("expected ETH count 1, got %d", tsb.Count("ETH-USDT"))
	}
}

func TestAppend_OverwriteOldest(t *testing.T) {
	tsb := NewTimeSeriesBuffer(3)

	// Fill the buffer
	tsb.Append(makeRecord("BTC-USDT", 1, 100.0, 1.0))
	tsb.Append(makeRecord("BTC-USDT", 2, 200.0, 2.0))
	tsb.Append(makeRecord("BTC-USDT", 3, 300.0, 3.0))

	if tsb.Count("BTC-USDT") != 3 {
		t.Errorf("expected count 3, got %d", tsb.Count("BTC-USDT"))
	}

	// Overflow: should overwrite ts=1
	tsb.Append(makeRecord("BTC-USDT", 4, 400.0, 4.0))

	if tsb.Count("BTC-USDT") != 3 {
		t.Errorf("expected count still 3 after overflow, got %d", tsb.Count("BTC-USDT"))
	}

	// ts=1 should no longer be queryable
	results := tsb.QueryByTimeRange("BTC-USDT", 1, 1)
	if len(results) != 0 {
		t.Errorf("expected ts=1 to be overwritten, but got %d results", len(results))
	}

	// ts=2,3,4 should all be present
	results = tsb.QueryByTimeRange("BTC-USDT", 2, 4)
	if len(results) != 3 {
		t.Errorf("expected 3 results for ts=2..4, got %d", len(results))
	}
}

func TestQueryByTimeRange_Empty(t *testing.T) {
	tsb := NewTimeSeriesBuffer(10)

	results := tsb.QueryByTimeRange("BTC-USDT", 0, 9999)
	if results != nil {
		t.Errorf("expected nil for unknown symbol, got %v", results)
	}
}

func TestQueryByTimeRange_ExactMatch(t *testing.T) {
	tsb := NewTimeSeriesBuffer(10)
	tsb.Append(makeRecord("BTC-USDT", 100, 50000.0, 1.0))
	tsb.Append(makeRecord("BTC-USDT", 200, 51000.0, 2.0))
	tsb.Append(makeRecord("BTC-USDT", 300, 52000.0, 3.0))

	results := tsb.QueryByTimeRange("BTC-USDT", 200, 200)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Timestamp != 200 {
		t.Errorf("expected timestamp 200, got %d", results[0].Timestamp)
	}
}

func TestQueryByTimeRange_RangeSubset(t *testing.T) {
	tsb := NewTimeSeriesBuffer(10)
	for i := int64(1); i <= 10; i++ {
		tsb.Append(makeRecord("BTC-USDT", i*100, float64(i)*1000, float64(i)))
	}

	results := tsb.QueryByTimeRange("BTC-USDT", 300, 700)
	if len(results) != 5 {
		t.Errorf("expected 5 results for ts=300..700, got %d", len(results))
	}
}

func TestQueryByTimeRange_ChronologicalOrder(t *testing.T) {
	tsb := NewTimeSeriesBuffer(10)
	tsb.Append(makeRecord("BTC-USDT", 500, 50000.0, 1.0))
	tsb.Append(makeRecord("BTC-USDT", 100, 49000.0, 2.0))
	tsb.Append(makeRecord("BTC-USDT", 300, 49500.0, 1.5))

	results := tsb.QueryByTimeRange("BTC-USDT", 0, 1000)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i := 1; i < len(results); i++ {
		if results[i].Timestamp < results[i-1].Timestamp {
			t.Errorf("results not in chronological order at index %d: %d < %d",
				i, results[i].Timestamp, results[i-1].Timestamp)
		}
	}
}

func TestQueryByTimeRange_NoMatch(t *testing.T) {
	tsb := NewTimeSeriesBuffer(10)
	tsb.Append(makeRecord("BTC-USDT", 100, 50000.0, 1.0))
	tsb.Append(makeRecord("BTC-USDT", 200, 51000.0, 2.0))

	results := tsb.QueryByTimeRange("BTC-USDT", 300, 500)
	if len(results) != 0 {
		t.Errorf("expected 0 results for out-of-range query, got %d", len(results))
	}
}

func TestQueryByTimeRange_AfterOverwrite(t *testing.T) {
	tsb := NewTimeSeriesBuffer(5)

	// Insert 8 records (capacity=5 → first 3 are overwritten)
	for i := int64(1); i <= 8; i++ {
		tsb.Append(makeRecord("BTC-USDT", i*10, float64(i)*100, 1.0))
	}

	// Records with ts 10, 20, 30 should be gone
	results := tsb.QueryByTimeRange("BTC-USDT", 10, 30)
	if len(results) != 0 {
		t.Errorf("expected 0 overwritten records, got %d", len(results))
	}

	// Records with ts 40..80 should still be present
	results = tsb.QueryByTimeRange("BTC-USDT", 40, 80)
	if len(results) != 5 {
		t.Errorf("expected 5 remaining records, got %d", len(results))
	}
}

func TestCount_UnknownSymbol(t *testing.T) {
	tsb := NewTimeSeriesBuffer(10)
	if tsb.Count("UNKNOWN") != 0 {
		t.Errorf("expected 0 for unknown symbol, got %d", tsb.Count("UNKNOWN"))
	}
}

func TestCount_CapBound(t *testing.T) {
	tsb := NewTimeSeriesBuffer(5)
	for i := int64(0); i < 100; i++ {
		tsb.Append(makeRecord("BTC-USDT", i, float64(i), 1.0))
	}
	if tsb.Count("BTC-USDT") != 5 {
		t.Errorf("expected count capped at 5, got %d", tsb.Count("BTC-USDT"))
	}
}

func TestConcurrentAccess(t *testing.T) {
	tsb := NewTimeSeriesBuffer(1000)
	var wg sync.WaitGroup

	// Concurrent writers
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				ts := int64(goroutineID*1000 + i)
				tsb.Append(makeRecord("BTC-USDT", ts, float64(ts), 1.0))
			}
		}(g)
	}

	// Concurrent readers
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				tsb.QueryByTimeRange("BTC-USDT", 0, 99999)
				tsb.Count("BTC-USDT")
			}
		}()
	}

	wg.Wait()

	// After all writes, we should have exactly 1000 records (10 goroutines × 100 records each)
	count := tsb.Count("BTC-USDT")
	if count != 1000 {
		t.Errorf("expected 1000 records after concurrent writes, got %d", count)
	}
}

func TestCapacity(t *testing.T) {
	tsb := NewTimeSeriesBuffer(42)
	if tsb.Capacity() != 42 {
		t.Errorf("expected capacity 42, got %d", tsb.Capacity())
	}
}
