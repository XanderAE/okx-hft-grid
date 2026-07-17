package strategy

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// --- Test helpers ---

func newGridConfig(id, symbol string) StrategyConfig {
	return StrategyConfig{
		StrategyID: id,
		Type:       "grid",
		Grid: &models.GridConfig{
			Symbol:     symbol,
			UpperPrice: decimal.NewFromInt(100),
			LowerPrice: decimal.NewFromInt(50),
			GridCount:  10,
			GridType:   models.GridTypeArithmetic,
			OrderSize:  decimal.NewFromFloat(0.1),
		},
	}
}

func newMeanReversionConfig(id, symbol string) StrategyConfig {
	return StrategyConfig{
		StrategyID: id,
		Type:       "mean_reversion",
		MeanReversion: &models.MeanReversionConfig{
			Symbol:         symbol,
			LookbackPeriod: 20,
			EntryThreshold: decimal.NewFromFloat(2.0),
			ExitThreshold:  decimal.NewFromFloat(0.5),
			MAType:         models.MATypeSMA,
			OrderSize:      decimal.NewFromFloat(0.5),
			MaxPosition:    decimal.NewFromInt(10),
			CooldownMs:     500,
		},
	}
}

func newScheduler() *Scheduler {
	return NewScheduler(nil, nil)
}

// --- LoadStrategy tests ---

func TestLoadStrategy_Grid(t *testing.T) {
	s := newScheduler()
	config := newGridConfig("grid-1", "BTC-USDT")

	err := s.LoadStrategy(config)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	instance := s.strategies["grid-1"]
	if instance == nil {
		t.Fatal("expected strategy instance to be created")
	}
	if instance.State != StrategyStateLoaded {
		t.Errorf("expected state LOADED, got %v", instance.State)
	}
	if instance.Symbol != "BTC-USDT" {
		t.Errorf("expected symbol BTC-USDT, got %v", instance.Symbol)
	}
}

func TestLoadStrategy_MeanReversion(t *testing.T) {
	s := newScheduler()
	config := newMeanReversionConfig("mr-1", "ETH-USDT")

	err := s.LoadStrategy(config)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	instance := s.strategies["mr-1"]
	if instance == nil {
		t.Fatal("expected strategy instance to be created")
	}
	if instance.Symbol != "ETH-USDT" {
		t.Errorf("expected symbol ETH-USDT, got %v", instance.Symbol)
	}
}

func TestLoadStrategy_DuplicateID(t *testing.T) {
	s := newScheduler()
	config := newGridConfig("grid-1", "BTC-USDT")

	_ = s.LoadStrategy(config)
	err := s.LoadStrategy(config)
	if err != ErrStrategyExists {
		t.Errorf("expected ErrStrategyExists, got %v", err)
	}
}

func TestLoadStrategy_EmptyID(t *testing.T) {
	s := newScheduler()
	config := StrategyConfig{
		StrategyID: "",
		Type:       "grid",
		Grid:       &models.GridConfig{Symbol: "BTC-USDT"},
	}

	err := s.LoadStrategy(config)
	if err != ErrInvalidConfig {
		t.Errorf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestLoadStrategy_InvalidType(t *testing.T) {
	s := newScheduler()
	config := StrategyConfig{
		StrategyID: "x",
		Type:       "unknown",
	}

	err := s.LoadStrategy(config)
	if err != ErrInvalidConfig {
		t.Errorf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestLoadStrategy_GridWithNilConfig(t *testing.T) {
	s := newScheduler()
	config := StrategyConfig{
		StrategyID: "x",
		Type:       "grid",
		Grid:       nil,
	}

	err := s.LoadStrategy(config)
	if err != ErrInvalidConfig {
		t.Errorf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestLoadStrategy_MeanReversionWithNilConfig(t *testing.T) {
	s := newScheduler()
	config := StrategyConfig{
		StrategyID: "x",
		Type:       "mean_reversion",
		MeanReversion: nil,
	}

	err := s.LoadStrategy(config)
	if err != ErrInvalidConfig {
		t.Errorf("expected ErrInvalidConfig, got %v", err)
	}
}

// --- StartStrategy tests ---

func TestStartStrategy_Success(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))

	err := s.StartStrategy("grid-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	instance := s.strategies["grid-1"]
	if instance.State != StrategyStateActive {
		t.Errorf("expected state ACTIVE, got %v", instance.State)
	}
}

func TestStartStrategy_NotFound(t *testing.T) {
	s := newScheduler()

	err := s.StartStrategy("nonexistent")
	if err != ErrStrategyNotFound {
		t.Errorf("expected ErrStrategyNotFound, got %v", err)
	}
}

func TestStartStrategy_AlreadyActive(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))
	_ = s.StartStrategy("grid-1")

	err := s.StartStrategy("grid-1")
	if err != ErrStrategyAlreadyActive {
		t.Errorf("expected ErrStrategyAlreadyActive, got %v", err)
	}
}

// --- StopStrategy tests ---

func TestStopStrategy_Success(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))
	_ = s.StartStrategy("grid-1")

	err := s.StopStrategy("grid-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	instance := s.strategies["grid-1"]
	if instance.State != StrategyStateStopped {
		t.Errorf("expected state STOPPED, got %v", instance.State)
	}
}

func TestStopStrategy_NotFound(t *testing.T) {
	s := newScheduler()

	err := s.StopStrategy("nonexistent")
	if err != ErrStrategyNotFound {
		t.Errorf("expected ErrStrategyNotFound, got %v", err)
	}
}

func TestStopStrategy_AlreadyStopped(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))
	_ = s.StartStrategy("grid-1")
	_ = s.StopStrategy("grid-1")

	err := s.StopStrategy("grid-1")
	if err != ErrStrategyStopped {
		t.Errorf("expected ErrStrategyStopped, got %v", err)
	}
}

// --- OnMarketUpdate tests ---

func TestOnMarketUpdate_RoutesToActiveStrategy(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))
	_ = s.StartStrategy("grid-1")

	tick := &models.TickData{
		Symbol:    "BTC-USDT",
		LastPrice: decimal.NewFromInt(60), // Below midpoint (75), should generate buy signal
		BestBid:   decimal.NewFromInt(59),
		BestAsk:   decimal.NewFromInt(61),
		Timestamp: time.Now().UnixMicro(),
	}

	s.OnMarketUpdate("BTC-USDT", tick)

	instance := s.strategies["grid-1"]
	if instance.LastSignal != models.SignalDirectionBuy {
		t.Errorf("expected buy signal, got %v", instance.LastSignal)
	}
}

func TestOnMarketUpdate_IgnoresInactiveStrategy(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))
	// Not started

	tick := &models.TickData{
		Symbol:    "BTC-USDT",
		LastPrice: decimal.NewFromInt(60),
		BestBid:   decimal.NewFromInt(59),
		BestAsk:   decimal.NewFromInt(61),
		Timestamp: time.Now().UnixMicro(),
	}

	s.OnMarketUpdate("BTC-USDT", tick)

	instance := s.strategies["grid-1"]
	if instance.LastSignal != models.SignalDirectionNone {
		t.Errorf("expected no signal for inactive strategy, got %v", instance.LastSignal)
	}
}

func TestOnMarketUpdate_IgnoresDifferentSymbol(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))
	_ = s.StartStrategy("grid-1")

	tick := &models.TickData{
		Symbol:    "ETH-USDT",
		LastPrice: decimal.NewFromInt(60),
		BestBid:   decimal.NewFromInt(59),
		BestAsk:   decimal.NewFromInt(61),
		Timestamp: time.Now().UnixMicro(),
	}

	s.OnMarketUpdate("ETH-USDT", tick)

	instance := s.strategies["grid-1"]
	if instance.LastSignal != models.SignalDirectionNone {
		t.Errorf("expected no signal for different symbol, got %v", instance.LastSignal)
	}
}

func TestOnMarketUpdate_NilTick(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))
	_ = s.StartStrategy("grid-1")

	// Should not panic
	s.OnMarketUpdate("BTC-USDT", nil)
}

func TestOnMarketUpdate_SellSignal(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))
	_ = s.StartStrategy("grid-1")

	tick := &models.TickData{
		Symbol:    "BTC-USDT",
		LastPrice: decimal.NewFromInt(90), // Above midpoint (75), should generate sell signal
		BestBid:   decimal.NewFromInt(89),
		BestAsk:   decimal.NewFromInt(91),
		Timestamp: time.Now().UnixMicro(),
	}

	s.OnMarketUpdate("BTC-USDT", tick)

	instance := s.strategies["grid-1"]
	if instance.LastSignal != models.SignalDirectionSell {
		t.Errorf("expected sell signal, got %v", instance.LastSignal)
	}
}

// --- OnOrderFill tests ---

func TestOnOrderFill_UpdatesPnL(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))
	_ = s.StartStrategy("grid-1")

	fill := models.FillEvent{
		OrderID:    "order-1",
		Symbol:     "BTC-USDT",
		Side:       models.SideSell,
		Price:      decimal.NewFromInt(100),
		Quantity:   decimal.NewFromFloat(0.1),
		Fee:        decimal.NewFromFloat(0.01),
		StrategyID: "grid-1",
		Timestamp:  time.Now(),
	}

	s.OnOrderFill(fill)

	instance := s.strategies["grid-1"]
	// Sell: realized = price*qty - fee = 100*0.1 - 0.01 = 9.99
	expected := decimal.NewFromFloat(9.99)
	if !instance.PnL.RealizedPnL.Equal(expected) {
		t.Errorf("expected realized PnL %v, got %v", expected, instance.PnL.RealizedPnL)
	}
	if instance.PnL.TotalTrades != 1 {
		t.Errorf("expected 1 trade, got %d", instance.PnL.TotalTrades)
	}
	if instance.PnL.WinCount != 1 {
		t.Errorf("expected 1 win, got %d", instance.PnL.WinCount)
	}
}

func TestOnOrderFill_BuySide(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))
	_ = s.StartStrategy("grid-1")

	fill := models.FillEvent{
		OrderID:    "order-1",
		Symbol:     "BTC-USDT",
		Side:       models.SideBuy,
		Price:      decimal.NewFromInt(100),
		Quantity:   decimal.NewFromFloat(0.1),
		Fee:        decimal.NewFromFloat(0.01),
		StrategyID: "grid-1",
		Timestamp:  time.Now(),
	}

	s.OnOrderFill(fill)

	instance := s.strategies["grid-1"]
	// Buy: realized = -(price*qty + fee) = -(10 + 0.01) = -10.01
	expected := decimal.NewFromFloat(-10.01)
	if !instance.PnL.RealizedPnL.Equal(expected) {
		t.Errorf("expected realized PnL %v, got %v", expected, instance.PnL.RealizedPnL)
	}
}

func TestOnOrderFill_UnknownStrategy(t *testing.T) {
	s := newScheduler()

	fill := models.FillEvent{
		OrderID:    "order-1",
		Symbol:     "BTC-USDT",
		Side:       models.SideSell,
		Price:      decimal.NewFromInt(100),
		Quantity:   decimal.NewFromFloat(0.1),
		Fee:        decimal.NewFromFloat(0.01),
		StrategyID: "unknown",
		Timestamp:  time.Now(),
	}

	// Should not panic
	s.OnOrderFill(fill)
}

// --- GetActiveStrategies tests ---

func TestGetActiveStrategies(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))
	_ = s.LoadStrategy(newMeanReversionConfig("mr-1", "ETH-USDT"))
	_ = s.StartStrategy("grid-1")

	statuses := s.GetActiveStrategies()
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}

	activeCount := 0
	for _, st := range statuses {
		if st.IsActive {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Errorf("expected 1 active strategy, got %d", activeCount)
	}
}

func TestGetActiveStrategies_Empty(t *testing.T) {
	s := newScheduler()

	statuses := s.GetActiveStrategies()
	if len(statuses) != 0 {
		t.Errorf("expected 0 statuses, got %d", len(statuses))
	}
}

// --- GetStrategyPnL tests ---

func TestGetStrategyPnL_Success(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))

	report, err := s.GetStrategyPnL("grid-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if report.StrategyID != "grid-1" {
		t.Errorf("expected strategy ID grid-1, got %v", report.StrategyID)
	}
	if !report.RealizedPnL.IsZero() {
		t.Errorf("expected zero PnL, got %v", report.RealizedPnL)
	}
}

func TestGetStrategyPnL_NotFound(t *testing.T) {
	s := newScheduler()

	_, err := s.GetStrategyPnL("nonexistent")
	if err != ErrStrategyNotFound {
		t.Errorf("expected ErrStrategyNotFound, got %v", err)
	}
}

func TestGetStrategyPnL_WithTrades(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))
	_ = s.StartStrategy("grid-1")

	// Simulate fills
	s.OnOrderFill(models.FillEvent{
		Side:       models.SideSell,
		Price:      decimal.NewFromInt(100),
		Quantity:   decimal.NewFromFloat(1.0),
		Fee:        decimal.NewFromFloat(0.1),
		StrategyID: "grid-1",
		Timestamp:  time.Now(),
	})
	s.OnOrderFill(models.FillEvent{
		Side:       models.SideSell,
		Price:      decimal.NewFromInt(50),
		Quantity:   decimal.NewFromFloat(1.0),
		Fee:        decimal.NewFromFloat(0.1),
		StrategyID: "grid-1",
		Timestamp:  time.Now(),
	})

	report, err := s.GetStrategyPnL("grid-1")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if report.TotalTrades != 2 {
		t.Errorf("expected 2 trades, got %d", report.TotalTrades)
	}
	if report.WinRate.IsZero() {
		t.Error("expected non-zero win rate")
	}
}

// --- StopAll tests ---

func TestStopAll(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))
	_ = s.LoadStrategy(newMeanReversionConfig("mr-1", "ETH-USDT"))
	_ = s.StartStrategy("grid-1")
	_ = s.StartStrategy("mr-1")

	s.StopAll()

	for id, instance := range s.strategies {
		if instance.State != StrategyStateStopped {
			t.Errorf("expected strategy %s to be stopped, got %v", id, instance.State)
		}
	}
}

func TestStopAll_AlreadyStopped(t *testing.T) {
	s := newScheduler()
	_ = s.LoadStrategy(newGridConfig("grid-1", "BTC-USDT"))
	// Never started - StopAll should not panic

	s.StopAll()

	instance := s.strategies["grid-1"]
	// Loaded state should remain since it was never active
	if instance.State != StrategyStateLoaded {
		t.Errorf("expected state LOADED (never activated), got %v", instance.State)
	}
}

// --- Interface compliance ---

func TestSchedulerImplementsStrategyEngine(t *testing.T) {
	var _ StrategyEngine = (*Scheduler)(nil)
}

// --- PnLTracker WinRate tests ---

func TestPnLTracker_WinRate_Zero(t *testing.T) {
	tracker := PnLTracker{}
	if !tracker.WinRate().IsZero() {
		t.Errorf("expected zero win rate with no trades, got %v", tracker.WinRate())
	}
}

func TestPnLTracker_WinRate_Calculated(t *testing.T) {
	tracker := PnLTracker{
		TotalTrades: 10,
		WinCount:    7,
	}
	expected := decimal.NewFromInt(70)
	if !tracker.WinRate().Equal(expected) {
		t.Errorf("expected win rate 70, got %v", tracker.WinRate())
	}
}

// --- StrategyState String tests ---

func TestStrategyState_String(t *testing.T) {
	tests := []struct {
		state    StrategyState
		expected string
	}{
		{StrategyStateLoaded, "LOADED"},
		{StrategyStateActive, "ACTIVE"},
		{StrategyStateStopped, "STOPPED"},
		{StrategyState(99), "UNKNOWN"},
	}
	for _, tt := range tests {
		if tt.state.String() != tt.expected {
			t.Errorf("expected %q, got %q", tt.expected, tt.state.String())
		}
	}
}
