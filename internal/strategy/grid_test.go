package strategy

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

func TestGridArithmeticBasic(t *testing.T) {
	config := &models.GridConfig{
		LowerPrice: decimal.NewFromInt(100),
		UpperPrice: decimal.NewFromInt(200),
		GridCount:  10,
		GridType:   models.GridTypeArithmetic,
	}

	levels, err := CalculateGridLevels(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have gridCount+1 = 11 levels
	if len(levels) != 11 {
		t.Fatalf("expected 11 levels, got %d", len(levels))
	}

	// First level = lower price
	if !levels[0].Equal(decimal.NewFromInt(100)) {
		t.Errorf("first level = %s, want 100", levels[0])
	}

	// Last level = upper price
	if !levels[10].Equal(decimal.NewFromInt(200)) {
		t.Errorf("last level = %s, want 200", levels[10])
	}

	// Check equal spacing: step = (200-100)/10 = 10
	expectedStep := decimal.NewFromInt(10)
	for i := 1; i < len(levels); i++ {
		diff := levels[i].Sub(levels[i-1])
		if !diff.Equal(expectedStep) {
			t.Errorf("interval [%d]-[%d] = %s, want %s", i-1, i, diff, expectedStep)
		}
	}
}

func TestGridArithmeticFractional(t *testing.T) {
	config := &models.GridConfig{
		LowerPrice: decimal.NewFromFloat(0.001),
		UpperPrice: decimal.NewFromFloat(0.010),
		GridCount:  3,
		GridType:   models.GridTypeArithmetic,
	}

	levels, err := CalculateGridLevels(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(levels) != 4 {
		t.Fatalf("expected 4 levels, got %d", len(levels))
	}

	// step = (0.010 - 0.001) / 3 = 0.003
	expectedStep := decimal.NewFromFloat(0.003)
	for i := 1; i < len(levels); i++ {
		diff := levels[i].Sub(levels[i-1])
		if !diff.Equal(expectedStep) {
			t.Errorf("interval [%d]-[%d] = %s, want %s", i-1, i, diff, expectedStep)
		}
	}
}

func TestGridGeometricBasic(t *testing.T) {
	config := &models.GridConfig{
		LowerPrice: decimal.NewFromInt(100),
		UpperPrice: decimal.NewFromInt(400),
		GridCount:  4,
		GridType:   models.GridTypeGeometric,
	}

	levels, err := CalculateGridLevels(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have gridCount+1 = 5 levels
	if len(levels) != 5 {
		t.Fatalf("expected 5 levels, got %d", len(levels))
	}

	// First = lower, last = upper
	if !levels[0].Equal(decimal.NewFromInt(100)) {
		t.Errorf("first level = %s, want 100", levels[0])
	}
	if !levels[4].Equal(decimal.NewFromInt(400)) {
		t.Errorf("last level = %s, want 400", levels[4])
	}

	// ratio = (400/100)^(1/4) = 4^0.25 = sqrt(2) ≈ 1.41421
	// Check that all ratios are approximately equal
	tolerance := decimal.NewFromFloat(1e-8)
	firstRatio := levels[1].Div(levels[0])
	for i := 2; i < len(levels); i++ {
		ratio := levels[i].Div(levels[i-1])
		diff := ratio.Sub(firstRatio).Abs()
		if diff.GreaterThan(tolerance) {
			t.Errorf("ratio [%d]/[%d] = %s, want ≈ %s (diff %s)", i, i-1, ratio, firstRatio, diff)
		}
	}
}

func TestGridGeometricStrictlyAscending(t *testing.T) {
	config := &models.GridConfig{
		LowerPrice: decimal.NewFromFloat(0.0001),
		UpperPrice: decimal.NewFromFloat(1.0),
		GridCount:  20,
		GridType:   models.GridTypeGeometric,
	}

	levels, err := CalculateGridLevels(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(levels) != 21 {
		t.Fatalf("expected 21 levels, got %d", len(levels))
	}

	for i := 1; i < len(levels); i++ {
		if levels[i].LessThanOrEqual(levels[i-1]) {
			t.Errorf("levels not strictly ascending at index %d: %s <= %s", i, levels[i], levels[i-1])
		}
	}
}

func TestGridInvalidInputs(t *testing.T) {
	tests := []struct {
		name   string
		config *models.GridConfig
		errMsg string
	}{
		{
			name: "grid count zero",
			config: &models.GridConfig{
				LowerPrice: decimal.NewFromInt(100),
				UpperPrice: decimal.NewFromInt(200),
				GridCount:  0,
				GridType:   models.GridTypeArithmetic,
			},
			errMsg: "grid count must be at least 1",
		},
		{
			name: "negative grid count",
			config: &models.GridConfig{
				LowerPrice: decimal.NewFromInt(100),
				UpperPrice: decimal.NewFromInt(200),
				GridCount:  -5,
				GridType:   models.GridTypeArithmetic,
			},
			errMsg: "grid count must be at least 1",
		},
		{
			name: "upper equals lower",
			config: &models.GridConfig{
				LowerPrice: decimal.NewFromInt(100),
				UpperPrice: decimal.NewFromInt(100),
				GridCount:  5,
				GridType:   models.GridTypeArithmetic,
			},
			errMsg: "upper price must be greater than lower price",
		},
		{
			name: "upper less than lower",
			config: &models.GridConfig{
				LowerPrice: decimal.NewFromInt(200),
				UpperPrice: decimal.NewFromInt(100),
				GridCount:  5,
				GridType:   models.GridTypeArithmetic,
			},
			errMsg: "upper price must be greater than lower price",
		},
		{
			name: "lower price zero",
			config: &models.GridConfig{
				LowerPrice: decimal.NewFromInt(0),
				UpperPrice: decimal.NewFromInt(100),
				GridCount:  5,
				GridType:   models.GridTypeGeometric,
			},
			errMsg: "prices must be positive",
		},
		{
			name: "negative lower price",
			config: &models.GridConfig{
				LowerPrice: decimal.NewFromInt(-10),
				UpperPrice: decimal.NewFromInt(100),
				GridCount:  5,
				GridType:   models.GridTypeArithmetic,
			},
			errMsg: "prices must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CalculateGridLevels(tt.config)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if err.Error() != tt.errMsg {
				t.Errorf("error = %q, want %q", err.Error(), tt.errMsg)
			}
		})
	}
}

func TestGridSingleLevel(t *testing.T) {
	// gridCount = 1 means 2 levels: lower and upper
	config := &models.GridConfig{
		LowerPrice: decimal.NewFromFloat(50.5),
		UpperPrice: decimal.NewFromFloat(100.5),
		GridCount:  1,
		GridType:   models.GridTypeArithmetic,
	}

	levels, err := CalculateGridLevels(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(levels) != 2 {
		t.Fatalf("expected 2 levels, got %d", len(levels))
	}

	if !levels[0].Equal(decimal.NewFromFloat(50.5)) {
		t.Errorf("first level = %s, want 50.5", levels[0])
	}
	if !levels[1].Equal(decimal.NewFromFloat(100.5)) {
		t.Errorf("last level = %s, want 100.5", levels[1])
	}
}

func TestGridGeometricSingleLevel(t *testing.T) {
	config := &models.GridConfig{
		LowerPrice: decimal.NewFromFloat(10.0),
		UpperPrice: decimal.NewFromFloat(1000.0),
		GridCount:  1,
		GridType:   models.GridTypeGeometric,
	}

	levels, err := CalculateGridLevels(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(levels) != 2 {
		t.Fatalf("expected 2 levels, got %d", len(levels))
	}

	if !levels[0].Equal(decimal.NewFromFloat(10.0)) {
		t.Errorf("first level = %s, want 10", levels[0])
	}
	if !levels[1].Equal(decimal.NewFromFloat(1000.0)) {
		t.Errorf("last level = %s, want 1000", levels[1])
	}
}

func TestGridArithmeticLargeCount(t *testing.T) {
	config := &models.GridConfig{
		LowerPrice: decimal.NewFromFloat(1.0),
		UpperPrice: decimal.NewFromFloat(2.0),
		GridCount:  500,
		GridType:   models.GridTypeArithmetic,
	}

	levels, err := CalculateGridLevels(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(levels) != 501 {
		t.Fatalf("expected 501 levels, got %d", len(levels))
	}

	// Verify strictly ascending
	for i := 1; i < len(levels); i++ {
		if levels[i].LessThanOrEqual(levels[i-1]) {
			t.Fatalf("not strictly ascending at index %d", i)
		}
	}

	// Last level = upper
	if !levels[500].Equal(decimal.NewFromFloat(2.0)) {
		t.Errorf("last level = %s, want 2.0", levels[500])
	}
}

func TestGridGeometricLargeCount(t *testing.T) {
	config := &models.GridConfig{
		LowerPrice: decimal.NewFromFloat(0.001),
		UpperPrice: decimal.NewFromFloat(100.0),
		GridCount:  100,
		GridType:   models.GridTypeGeometric,
	}

	levels, err := CalculateGridLevels(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(levels) != 101 {
		t.Fatalf("expected 101 levels, got %d", len(levels))
	}

	// Verify strictly ascending
	for i := 1; i < len(levels); i++ {
		if levels[i].LessThanOrEqual(levels[i-1]) {
			t.Fatalf("not strictly ascending at index %d: %s <= %s", i, levels[i], levels[i-1])
		}
	}

	// First = lower, last = upper
	if !levels[0].Equal(decimal.NewFromFloat(0.001)) {
		t.Errorf("first level = %s, want 0.001", levels[0])
	}
	if !levels[100].Equal(decimal.NewFromFloat(100.0)) {
		t.Errorf("last level = %s, want 100.0", levels[100])
	}
}
