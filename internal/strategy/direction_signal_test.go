package strategy

import (
	"testing"

	"github.com/shopspring/decimal"
)

const (
	testShortWindow = 5
	testLongWindow  = 20
)

func feed(d *DirectionSignal, prices []decimal.Decimal) {
	for _, p := range prices {
		d.Update(p)
	}
}

// Uptrend: steadily increasing prices after warmup should yield Long.
func TestDirectionSignal_Uptrend_Long(t *testing.T) {
	d := NewDirectionSignal(testShortWindow, testLongWindow)
	base := 64000.0
	for i := 0; i < 45; i++ {
		d.Update(decimal.NewFromFloat(base + float64(i)*10.0))
	}
	dir, conf := d.Evaluate()
	if dir != Long {
		t.Fatalf("expected Long on sustained uptrend, got %s", dir)
	}
	if conf < 0 || conf > 1 {
		t.Fatalf("confidence out of range: %v", conf)
	}
}

// Downtrend: steadily decreasing prices after warmup should yield Short.
func TestDirectionSignal_Downtrend_Short(t *testing.T) {
	d := NewDirectionSignal(testShortWindow, testLongWindow)
	base := 64000.0
	for i := 0; i < 45; i++ {
		d.Update(decimal.NewFromFloat(base - float64(i)*10.0))
	}
	dir, conf := d.Evaluate()
	if dir != Short {
		t.Fatalf("expected Short on sustained downtrend, got %s", dir)
	}
	if conf < 0 || conf > 1 {
		t.Fatalf("confidence out of range: %v", conf)
	}
}

// Warmup: fewer than longWindow samples should yield Flat with zero confidence.
func TestDirectionSignal_Warmup_Flat(t *testing.T) {
	d := NewDirectionSignal(testShortWindow, testLongWindow)
	base := 64000.0
	// Feed fewer than longWindow samples.
	for i := 0; i < testLongWindow-1; i++ {
		d.Update(decimal.NewFromFloat(base + float64(i)*10.0))
	}
	dir, conf := d.Evaluate()
	if dir != Flat {
		t.Fatalf("expected Flat during warmup, got %s", dir)
	}
	if conf != 0 {
		t.Fatalf("expected zero confidence during warmup, got %v", conf)
	}
}

// Choppy/flat prices should yield Flat.
func TestDirectionSignal_Flat_Flat(t *testing.T) {
	d := NewDirectionSignal(testShortWindow, testLongWindow)
	price := decimal.NewFromFloat(64000.0)
	for i := 0; i < 45; i++ {
		d.Update(price)
	}
	dir, _ := d.Evaluate()
	if dir != Flat {
		t.Fatalf("expected Flat on constant prices, got %s", dir)
	}
}

// Determinism: identical input sequences must produce identical outputs.
func TestDirectionSignal_Determinism(t *testing.T) {
	prices := make([]decimal.Decimal, 0, 60)
	base := 64000.0
	// A mixed sequence: up then down.
	for i := 0; i < 30; i++ {
		prices = append(prices, decimal.NewFromFloat(base+float64(i)*7.5))
	}
	for i := 0; i < 30; i++ {
		prices = append(prices, decimal.NewFromFloat(base+225.0-float64(i)*7.5))
	}

	d1 := NewDirectionSignal(testShortWindow, testLongWindow)
	d2 := NewDirectionSignal(testShortWindow, testLongWindow)
	feed(d1, prices)
	feed(d2, prices)

	dir1, conf1 := d1.Evaluate()
	dir2, conf2 := d2.Evaluate()
	if dir1 != dir2 {
		t.Fatalf("determinism violated: dir %s vs %s", dir1, dir2)
	}
	if conf1 != conf2 {
		t.Fatalf("determinism violated: conf %v vs %v", conf1, conf2)
	}
}

// Non-positive prices should be ignored (defensive).
func TestDirectionSignal_IgnoresNonPositive(t *testing.T) {
	d := NewDirectionSignal(testShortWindow, testLongWindow)
	d.Update(decimal.Zero)
	d.Update(decimal.NewFromInt(-1))
	if d.count != 0 {
		t.Fatalf("expected non-positive prices to be ignored, count=%d", d.count)
	}
	dir, conf := d.Evaluate()
	if dir != Flat || conf != 0 {
		t.Fatalf("expected Flat/0 after only non-positive inputs, got %s/%v", dir, conf)
	}
}
