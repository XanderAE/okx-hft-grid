package strategy

import (
	"errors"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/internal/execution"
	"github.com/yourname/okx-hft-grid/internal/risk"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

var (
	ErrStrategyNotFound      = errors.New("strategy not found")
	ErrStrategyExists        = errors.New("strategy already exists")
	ErrStrategyAlreadyActive = errors.New("strategy is already active")
	ErrStrategyStopped       = errors.New("strategy is already stopped")
	ErrInvalidConfig         = errors.New("invalid strategy config")
)

// StrategyState represents the current state of a strategy instance.
type StrategyState int

const (
	StrategyStateLoaded  StrategyState = iota // Loaded but not active
	StrategyStateActive                       // Running and processing events
	StrategyStateStopped                      // Stopped, no longer processing
)

// String returns the string representation of StrategyState.
func (s StrategyState) String() string {
	switch s {
	case StrategyStateLoaded:
		return "LOADED"
	case StrategyStateActive:
		return "ACTIVE"
	case StrategyStateStopped:
		return "STOPPED"
	default:
		return "UNKNOWN"
	}
}

// PnLTracker tracks realized and unrealized PnL for a strategy.
type PnLTracker struct {
	RealizedPnL   decimal.Decimal
	UnrealizedPnL decimal.Decimal
	TotalTrades   int
	WinCount      int
}

// WinRate returns the win rate as a decimal percentage.
func (p *PnLTracker) WinRate() decimal.Decimal {
	if p.TotalTrades == 0 {
		return decimal.Zero
	}
	return decimal.NewFromInt(int64(p.WinCount)).
		Div(decimal.NewFromInt(int64(p.TotalTrades))).
		Mul(decimal.NewFromInt(100))
}

// StrategyInstance holds the runtime state of a single strategy instance.
type StrategyInstance struct {
	Config      StrategyConfig
	State       StrategyState
	PnL         PnLTracker
	LastSignal  models.SignalDirection
	LastUpdate  time.Time
	Symbol      string
	DriftEngine *GridDriftEngine
	SignalGen   *SignalGenerator
}

// Scheduler implements the StrategyEngine interface and manages strategy lifecycle.
type Scheduler struct {
	mu              sync.RWMutex
	strategies      map[string]*StrategyInstance
	riskManager     risk.RiskManager
	executionEngine execution.OrderExecutionEngine
}

// NewScheduler creates a new Scheduler with the given risk manager and execution engine references.
func NewScheduler(riskManager risk.RiskManager, executionEngine execution.OrderExecutionEngine) *Scheduler {
	return &Scheduler{
		strategies:      make(map[string]*StrategyInstance),
		riskManager:     riskManager,
		executionEngine: executionEngine,
	}
}

// LoadStrategy creates a strategy instance from config and stores it in the scheduler.
func (s *Scheduler) LoadStrategy(config StrategyConfig) error {
	if config.StrategyID == "" {
		return ErrInvalidConfig
	}
	if config.Type != "grid" && config.Type != "mean_reversion" {
		return ErrInvalidConfig
	}
	if config.Type == "grid" && config.Grid == nil {
		return ErrInvalidConfig
	}
	if config.Type == "mean_reversion" && config.MeanReversion == nil {
		return ErrInvalidConfig
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.strategies[config.StrategyID]; exists {
		return ErrStrategyExists
	}

	symbol := ""
	if config.Grid != nil {
		symbol = config.Grid.Symbol
	} else if config.MeanReversion != nil {
		symbol = config.MeanReversion.Symbol
	}

	instance := &StrategyInstance{
		Config:     config,
		State:      StrategyStateLoaded,
		PnL:        PnLTracker{},
		LastSignal: models.SignalDirectionNone,
		LastUpdate: time.Now(),
		Symbol:     symbol,
	}

	// Initialize DriftEngine if grid drift is configured and enabled
	if config.Grid != nil && config.Grid.Drift != nil && config.Grid.Drift.Enabled {
		instance.DriftEngine = NewGridDriftEngine(
			config.Grid,
			nil, // GridState will be set when strategy starts processing
			s.executionEngine,
			nil, // logger injected separately
			nil, // metrics injected separately
			nil, // alerter injected separately
		)
	}

	// Initialize SignalGenerator for mean reversion strategies
	if config.Type == "mean_reversion" && config.MeanReversion != nil {
		cfg := config.MeanReversion
		instance.SignalGen = NewSignalGenerator(SignalGeneratorConfig{
			EntryThreshold: cfg.EntryThreshold,
			ExitThreshold:  cfg.ExitThreshold,
			CooldownMs:     cfg.CooldownMs,
			LookbackPeriod: cfg.LookbackPeriod,
			MAType:         cfg.MAType,
		})
	}

	s.strategies[config.StrategyID] = instance
	return nil
}

// StartStrategy activates a loaded strategy by its ID.
func (s *Scheduler) StartStrategy(strategyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	instance, exists := s.strategies[strategyID]
	if !exists {
		return ErrStrategyNotFound
	}

	if instance.State == StrategyStateActive {
		return ErrStrategyAlreadyActive
	}

	instance.State = StrategyStateActive
	instance.LastUpdate = time.Now()
	return nil
}

// StopStrategy halts an active strategy by its ID.
func (s *Scheduler) StopStrategy(strategyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	instance, exists := s.strategies[strategyID]
	if !exists {
		return ErrStrategyNotFound
	}

	if instance.State == StrategyStateStopped {
		return ErrStrategyStopped
	}

	instance.State = StrategyStateStopped
	instance.LastUpdate = time.Now()
	return nil
}

// OnMarketUpdate receives a market data update and routes it to relevant active strategies.
func (s *Scheduler) OnMarketUpdate(symbol string, tick *models.TickData) {
	if tick == nil {
		return
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, instance := range s.strategies {
		if instance.State != StrategyStateActive {
			continue
		}
		if instance.Symbol != symbol {
			continue
		}
		// Route the market update to the strategy and generate a signal
		signal := s.processMarketUpdate(instance, tick)
		if signal != models.SignalDirectionNone {
			instance.LastSignal = signal
			instance.LastUpdate = time.Now()
			// Submit signal to risk manager for approval
			s.submitSignalForApproval(instance, signal, tick)
		}
	}
}

// processMarketUpdate processes a market update for a specific strategy instance.
// Returns the signal direction generated by the strategy.
func (s *Scheduler) processMarketUpdate(instance *StrategyInstance, tick *models.TickData) models.SignalDirection {
	switch instance.Config.Type {
	case "grid":
		return s.processGridUpdate(instance, tick)
	case "mean_reversion":
		return s.processMeanReversionUpdate(instance, tick)
	default:
		return models.SignalDirectionNone
	}
}

// processGridUpdate processes a market update for a grid strategy.
// Grid strategies generate buy signals when price is near lower levels and sell signals when near upper levels.
func (s *Scheduler) processGridUpdate(instance *StrategyInstance, tick *models.TickData) models.SignalDirection {
	if instance.Config.Grid == nil {
		return models.SignalDirectionNone
	}

	// Invoke drift engine before grid signal logic
	if instance.DriftEngine != nil {
		instance.DriftEngine.OnPriceUpdate(tick.LastPrice)
	}

	config := instance.Config.Grid
	price := tick.LastPrice

	// Determine signal direction based on price relative to grid range
	midPrice := config.LowerPrice.Add(config.UpperPrice).Div(decimal.NewFromInt(2))

	if price.LessThan(midPrice) {
		return models.SignalDirectionBuy
	} else if price.GreaterThan(midPrice) {
		return models.SignalDirectionSell
	}

	return models.SignalDirectionNone
}

// processMeanReversionUpdate processes a market update for a mean reversion strategy.
func (s *Scheduler) processMeanReversionUpdate(instance *StrategyInstance, tick *models.TickData) models.SignalDirection {
	if instance.Config.MeanReversion == nil {
		return models.SignalDirectionNone
	}

	// Initialize signal generator if not yet created
	if instance.SignalGen == nil {
		cfg := instance.Config.MeanReversion
		instance.SignalGen = NewSignalGenerator(SignalGeneratorConfig{
			EntryThreshold: cfg.EntryThreshold,
			ExitThreshold:  cfg.ExitThreshold,
			CooldownMs:     cfg.CooldownMs,
			LookbackPeriod: cfg.LookbackPeriod,
			MAType:         cfg.MAType,
		})
	}

	// Feed data point
	instance.SignalGen.AddDataPoint(tick.LastPrice, tick.Volume24h)

	// Generate signal
	signal := instance.SignalGen.GenerateSignal(tick.LastPrice, time.Now())
	return signal
}

// submitSignalForApproval submits a strategy signal to the risk manager for approval.
func (s *Scheduler) submitSignalForApproval(instance *StrategyInstance, signal models.SignalDirection, tick *models.TickData) {
	if s.riskManager == nil || s.executionEngine == nil {
		return
	}

	var side models.Side
	switch signal {
	case models.SignalDirectionBuy:
		side = models.SideBuy
	case models.SignalDirectionSell:
		side = models.SideSell
	default:
		return
	}

	orderSize := decimal.Zero
	if instance.Config.Grid != nil {
		orderSize = instance.Config.Grid.OrderSize
	} else if instance.Config.MeanReversion != nil {
		orderSize = instance.Config.MeanReversion.OrderSize
	}

	riskReq := &risk.OrderRequest{
		Symbol:     instance.Symbol,
		Side:       side,
		OrderType:  models.OrderTypeLimit,
		Price:      tick.LastPrice,
		Quantity:   orderSize,
		StrategyID: instance.Config.StrategyID,
	}

	decision := s.riskManager.CheckOrder(riskReq)
	if decision != nil && decision.Approved {
		execReq := &execution.OrderRequest{
			Symbol:     instance.Symbol,
			Side:       side,
			OrderType:  models.OrderTypeLimit,
			Price:      tick.LastPrice,
			Quantity:   orderSize,
			StrategyID: instance.Config.StrategyID,
			GridLevel:  -1,
		}
		_, _ = s.executionEngine.PlaceOrder(execReq)
	}
}

// OnOrderFill receives a fill event and routes it to the owning strategy.
func (s *Scheduler) OnOrderFill(fill models.FillEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	instance, exists := s.strategies[fill.StrategyID]
	if !exists {
		return
	}

	// Update PnL tracker
	fillValue := fill.Price.Mul(fill.Quantity)
	fee := fill.Fee

	if fill.Side == models.SideSell {
		// Realized PnL from a sell (simplified: assumes profit)
		instance.PnL.RealizedPnL = instance.PnL.RealizedPnL.Add(fillValue.Sub(fee))
		instance.PnL.TotalTrades++
		if fillValue.Sub(fee).IsPositive() {
			instance.PnL.WinCount++
		}
	} else {
		// Buy side reduces realized PnL (cost)
		instance.PnL.RealizedPnL = instance.PnL.RealizedPnL.Sub(fillValue.Add(fee))
		instance.PnL.TotalTrades++
	}

	instance.LastUpdate = time.Now()
}

// GetActiveStrategies returns the status of all loaded strategies.
func (s *Scheduler) GetActiveStrategies() []StrategyStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	statuses := make([]StrategyStatus, 0, len(s.strategies))
	for id, instance := range s.strategies {
		status := StrategyStatus{
			StrategyID:    id,
			Symbol:        instance.Symbol,
			Type:          instance.Config.Type,
			IsActive:      instance.State == StrategyStateActive,
			RealizedPnL:   instance.PnL.RealizedPnL,
			UnrealizedPnL: instance.PnL.UnrealizedPnL,
		}
		statuses = append(statuses, status)
	}

	return statuses
}

// GetStrategyPnL returns the PnL report for a specific strategy.
func (s *Scheduler) GetStrategyPnL(strategyID string) (*PnLReport, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	instance, exists := s.strategies[strategyID]
	if !exists {
		return nil, ErrStrategyNotFound
	}

	report := &PnLReport{
		StrategyID:    strategyID,
		RealizedPnL:   instance.PnL.RealizedPnL,
		UnrealizedPnL: instance.PnL.UnrealizedPnL,
		TotalTrades:   instance.PnL.TotalTrades,
		WinRate:       instance.PnL.WinRate(),
	}

	return report, nil
}

// StopAll halts all active strategies. Used during emergency stop.
func (s *Scheduler) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, instance := range s.strategies {
		if instance.State == StrategyStateActive {
			instance.State = StrategyStateStopped
			instance.LastUpdate = time.Now()
		}
	}
}

// Compile-time assertion that Scheduler implements StrategyEngine.
var _ StrategyEngine = (*Scheduler)(nil)
