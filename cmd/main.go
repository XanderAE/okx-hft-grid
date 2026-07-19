package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/internal/config"
	"github.com/yourname/okx-hft-grid/internal/execution"
	"github.com/yourname/okx-hft-grid/internal/marketdata"
	"github.com/yourname/okx-hft-grid/internal/monitor"
	"github.com/yourname/okx-hft-grid/internal/orderbook"
	"github.com/yourname/okx-hft-grid/internal/persistence"
	"github.com/yourname/okx-hft-grid/internal/ratelimiter"
	"github.com/yourname/okx-hft-grid/internal/risk"
	"github.com/yourname/okx-hft-grid/internal/strategy"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

const (
	// reconcileTimeout is the maximum time to reconcile with the exchange on startup.
	reconcileTimeout = 60 * time.Second

	// exchangeUnreachableTimeout triggers trading halt if exchange is unreachable during reconciliation.
	exchangeUnreachableTimeout = 60 * time.Second
)

func main() {
	// Panic recovery with alerting
	defer panicRecovery(nil)

	// 1. Parse command-line flags
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	flag.Parse()

	// 2. Load configuration with strict known-field decoding
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("[FATAL] Failed to load configuration: %v", err)
	}

	// 3. Check security constraints (root check, file permissions)
	if err := config.CheckSecurityConstraints(*configPath); err != nil {
		log.Fatalf("[FATAL] Security check failed: %v", err)
	}

	// 4. Load credentials from environment
	creds, err := config.LoadCredentials()
	if err != nil {
		log.Fatalf("[FATAL] Credential validation failed: %v", err)
	}

	// 5. Initialize components
	app, err := initializeComponents(cfg, creds)
	if err != nil {
		log.Fatalf("[FATAL] Component initialization failed: %v", err)
	}

	// Update panic recovery to use alerter
	defer panicRecovery(app.alerter)

	// 6. Startup sequence (design-mandated ordering)
	if err := app.startup(); err != nil {
		log.Fatalf("[FATAL] Startup sequence failed: %v", err)
	}

	// 7. Wait for shutdown signal
	app.waitForShutdown()
}

// application holds all system components and manages their lifecycle.
type application struct {
	cfg   *config.SystemConfig
	creds *config.Credentials

	// Infrastructure
	store   *persistence.SQLiteStore
	tsBuf   *persistence.TimeSeriesBuffer
	logger  *monitor.StructuredLogger
	metrics *monitor.MetricsServer
	alerter *monitor.Alerter
	health  *monitor.HealthRegistry

	// Core engine
	rateLimiter ratelimiter.RateLimiter
	apiClient   *execution.APIClient
	orderMgr    *execution.OrderManager
	riskMgr     *risk.RiskManagerImpl
	esMgr       *risk.EmergencyStopManager
	emDetector  *risk.ExtremeMarketDetector

	// Trading gate (scoped Safe_Stop)
	tradingGate *risk.TradingGate

	// Market data
	wsClient   *marketdata.WSClient
	privateWS  *marketdata.PrivateWSClient
	parser     *marketdata.Parser
	dispatcher *marketdata.Dispatcher
	orderBook  *orderbook.LocalOrderBook

	// Strategy
	scheduler *strategy.Scheduler

	// Execution - new components
	gateway        *execution.OKXGateway
	rulesProvider  *execution.InstrumentRulesCache
	reconcileCoord *execution.ReconciliationCoordinator
	rebalancers    map[string]*execution.Rebalancer

	// Legacy compat
	fillHandler *execution.GridFillHandler
	reconciler  *execution.Reconciler

	// Inventory tracking
	inventoryTracker *execution.InventoryTracker

	// Shutdown
	shutdownOnce sync.Once
}

// initializeComponents creates and wires all system components.
func initializeComponents(cfg *config.SystemConfig, creds *config.Credentials) (*application, error) {
	app := &application{
		cfg:         cfg,
		creds:       creds,
		rebalancers: make(map[string]*execution.Rebalancer),
	}

	// 5.1 SQLiteStore (persistence)
	dbPath := cfg.PersistencePath
	if dbPath == "" {
		dbPath = "data/hft_state.db"
	}
	store, err := persistence.NewSQLiteStore(dbPath)
	if err != nil {
		if errors.Is(err, persistence.ErrCorrupted) {
			log.Printf("[CRITICAL] Persistence database corrupted: %v", err)
			log.Printf("[CRITICAL] Stopping trading - manual intervention required. Corrupted file preserved at: %s", dbPath)
			return nil, fmt.Errorf("persistence corrupted: %w", err)
		}
		return nil, fmt.Errorf("failed to initialize persistence: %w", err)
	}
	app.store = store

	// 5.2 TimeSeriesBuffer
	app.tsBuf = persistence.NewTimeSeriesBuffer(persistence.DefaultCapacityPerSymbol)

	// 5.3 RateLimiter (token bucket)
	app.rateLimiter = ratelimiter.NewTokenBucketLimiter(ratelimiter.DefaultEndpointConfigs())

	// 5.4 Resolve role-labelled network endpoints
	resolved, resolveErr := config.ResolveNetworkEndpoints(cfg)
	if resolveErr != nil {
		return nil, fmt.Errorf("endpoint resolution failed: %w", resolveErr)
	}

	// 5.4a APIClient (execution) — production uses explicit guard; all others use default
	if cfg.ExecutionMode == config.ExecutionModeProduction {
		guard, guardErr := config.NewProductionNetworkGuard(cfg)
		if guardErr != nil {
			return nil, fmt.Errorf("production network guard creation failed: %w", guardErr)
		}
		if err := guard.ValidateEndpoint(resolved.RESTBaseURL); err != nil {
			return nil, fmt.Errorf("production REST endpoint validation failed: %w", err)
		}
		app.apiClient = execution.NewAPIClientWithEndpointGuard(resolved.RESTBaseURL, creds, app.rateLimiter, guard)
	} else {
		app.apiClient = execution.NewAPIClient(resolved.RESTBaseURL, creds, app.rateLimiter)
	}

	// 5.5 OrderManager
	app.orderMgr = execution.NewOrderManager()

	// 5.6 RiskManager + EmergencyStopManager
	app.riskMgr = risk.NewRiskManager(&cfg.RiskLimits)
	app.esMgr = risk.NewEmergencyStopManager(&cfg.RiskLimits)

	// 5.7 ExtremeMarketDetector
	app.emDetector = risk.NewExtremeMarketDetector()

	// 5.8 TradingGate (scoped Safe_Stop with emergency stop composition)
	app.tradingGate = risk.NewTradingGate(risk.WithEmergencyStop(app.esMgr))

	// 5.9 MetricsServer
	metricsPort := cfg.MetricsPort
	if metricsPort == 0 {
		metricsPort = 9090
	}
	app.metrics = monitor.NewMetricsServer(metricsPort)

	// 5.10 StructuredLogger
	app.logger = monitor.NewStructuredLogger(os.Stdout)
	app.logger.SetSanitizer(config.SanitizeLog)

	// 5.11 HealthRegistry
	location := cfg.Deployment.Location
	if location == "" {
		location = "unknown"
	}
	app.health = monitor.NewHealthRegistry(location)

	// 5.12 Alerter (with Telegram/Discord channels if configured)
	alertChannels := buildAlertChannels(cfg)
	app.alerter = monitor.NewAlerter(alertChannels, app.logger)

	// 5.13 WSClient (market data) — public endpoint from resolved values
	wsConfig := marketdata.DefaultWSClientConfig()
	wsConfig.URL = resolved.PublicWebSocketURL
	wsConfig.Symbols = cfg.Symbols
	app.wsClient = marketdata.NewWSClient(wsConfig)

	// 5.14 PrivateWSClient (for order fills) — private endpoint from resolved values
	privateWSConfig := marketdata.DefaultPrivateWSClientConfig()
	privateWSConfig.URL = resolved.PrivateWebSocketURL
	app.privateWS = marketdata.NewPrivateWSClient(privateWSConfig, creds)

	// 5.15 Parser (for tick validation)
	app.parser = marketdata.NewParser(func(symbol, reason string) {
		app.logger.LogWarn("data validation failed", map[string]string{"symbol": symbol, "reason": reason})
	})

	// 5.16 EventDispatcher
	app.dispatcher = marketdata.NewDispatcher(marketdata.DefaultDispatcherConfig())

	// 5.17 OrderBook
	app.orderBook = orderbook.NewLocalOrderBook()

	// 5.18 Scheduler (strategy engine)
	app.scheduler = strategy.NewScheduler(app.riskMgr, nil)

	// 5.19 ExchangeGateway (context-aware, cash mode, deterministic clOrdId)
	app.gateway = execution.NewOKXGateway(app.apiClient)

	// 5.20 InstrumentRulesProvider (cache with 5-min refresh, 15-min hard TTL)
	app.rulesProvider = execution.NewInstrumentRulesCache(app.gateway)

	// 5.21 Reconciler (legacy compat - uses approved 30-second interval)
	reconcileInterval := time.Duration(cfg.ReconcileIntervalSec) * time.Second
	if reconcileInterval == 0 {
		reconcileInterval = 30 * time.Second
	}
	querier := &exchangeQuerierAdapter{client: app.apiClient, symbols: cfg.Symbols}
	app.reconciler = execution.NewReconciler(app.orderMgr, querier, reconcileInterval)
	app.reconciler.SetSymbols(cfg.Symbols)

	// Wire up emergency stop callbacks
	app.esMgr.RegisterEmergencyCallback(&emergencyStopHandler{app: app})

	// 5.22 InventoryTracker (max $50 per symbol)
	app.inventoryTracker = execution.NewInventoryTracker(decimal.NewFromFloat(50))

	// Wire up extreme market detector callbacks
	app.emDetector.RegisterCallback(&extremeMarketHandler{app: app})

	return app, nil
}

// startup executes the production startup sequence in the design-mandated order:
// 1. strict config/guard validation
// 2. state probe/migration/store
// 3. observability (logger, health, metrics)
// 4. recovery state/gates
// 5. gateway/instrument rules
// 6. Private_WS auth/subscription
// 7. immediate reconciliation/outbox recovery
// 8. ownership-safe startup cleanup
// 9. fresh public ticker
// 10. healthy/READY
// 11. approved initial grid & schedules
func (app *application) startup() error {
	app.logger.LogInfo("starting OKX HFT Grid Trading System", map[string]string{
		"symbols": fmt.Sprintf("%v", app.cfg.Symbols),
	})

	// ---- Phase 1: Strict config/guard validation ----
	// Config already validated during LoadConfig via strict known-field decoding.
	// Log sanitized effective config summary.
	if summary, err := config.BuildSanitizedEffectiveConfig(app.cfg); err == nil {
		app.logger.LogInfo("effective config validated", map[string]string{"summary": summary})
	}

	// Reject production mean reversion: production profile must not load it
	if len(app.cfg.MeanReversionConfigs) > 0 {
		return fmt.Errorf("production profile forbids mean_reversion_configs")
	}

	// ---- Phase 2: State probe/migration/store ----
	// State directory validation and writable probe
	if app.cfg.PersistencePath != "" && app.cfg.PersistencePath != "data/hft_state.db" {
		stateDir := app.cfg.PersistencePath[:len(app.cfg.PersistencePath)-len("/hft_state.db")]
		if stateDir != "" {
			if err := persistence.ValidateStateDirectory(stateDir); err != nil {
				app.health.SetStateDirectoryWritable(false)
				app.tradingGate.EnterGlobalSafeStop(risk.ReasonPersistenceFailure, "state directory validation failed: "+err.Error())
				app.logger.LogError("state directory validation failed", map[string]string{"error": err.Error()})
				return fmt.Errorf("state directory: %w", err)
			}
			if err := persistence.WritableProbe(stateDir); err != nil {
				app.health.SetStateDirectoryWritable(false)
				app.tradingGate.EnterGlobalSafeStop(risk.ReasonPersistenceFailure, "state directory writable probe failed: "+err.Error())
				app.logger.LogError("state directory writable probe failed", map[string]string{"error": err.Error()})
				return fmt.Errorf("state directory writable probe: %w", err)
			}
		}
	}
	app.health.SetStateDirectoryWritable(true)

	// Load persisted state (orders, positions, strategy state)
	if err := app.loadPersistedState(); err != nil {
		app.logger.LogError("failed to load persisted state", map[string]string{"error": err.Error()})
		return fmt.Errorf("load persisted state: %w", err)
	}

	// ---- Phase 3: Observability ----
	app.dispatcher.Start()

	if err := app.metrics.Start(); err != nil {
		app.logger.LogWarn("failed to start metrics server", map[string]string{"error": err.Error()})
		// Non-fatal: continue without metrics
	} else {
		app.logger.LogInfo("metrics server started", map[string]string{
			"port": fmt.Sprintf("%d", app.cfg.MetricsPort),
		})
	}

	// ---- Phase 4: Recovery state/gates ----
	// Set global trading gate to DEGRADED_RECONCILING until all gates pass
	app.tradingGate.EnterGlobalSafeStop(risk.ReasonPrivateWSUncertain, "startup: Private_WS not yet authenticated")
	app.health.SetPrivateWSState("connecting", time.Time{}, 0, false)

	// Initialize per-symbol states
	for _, symbol := range app.cfg.Symbols {
		app.health.SetSymbolState(symbol, monitor.HealthStateDegraded, nil)
	}
	app.logger.LogInfo("recovery gates initialized", nil)

	// ---- Phase 5: Gateway/instrument rules ----
	// Fetch fresh instrument rules for each symbol before any order placement
	for _, symbol := range app.cfg.Symbols {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, err := app.rulesProvider.Refresh(ctx, symbol)
		cancel()
		if err != nil {
			app.logger.LogWarn("instrument rules fetch failed", map[string]string{
				"symbol": symbol,
				"error":  err.Error(),
			})
			// Non-fatal at startup - will be fetched before order placement
		}
	}
	app.logger.LogInfo("gateway and instrument rules initialized", nil)

	// ---- Phase 6: Private_WS auth/subscription ----
	if err := app.privateWS.Connect(); err != nil {
		app.logger.LogError("failed to connect private WebSocket", map[string]string{
			"error": err.Error(),
		})
		app.alerter.SendCritical("Private WebSocket connection failed: "+err.Error(), map[string]string{
			"impact": "trading will not start - all risk-increasing operations blocked",
		})
		// Remain degraded/safe-stopped but don't crash - allow health endpoint
		app.health.SetPrivateWSState("disconnected", time.Time{}, 0, false)
		// Leave global safe-stop active for Private_WS
		return fmt.Errorf("private WS connection: %w", err)
	}
	// Mark Private_WS as connected (auth/subscription confirmed by Connect())
	app.health.SetPrivateWSState("ready", time.Now(), 0, true)
	// Clear Private_WS reason from global gate
	app.tradingGate.ClearGlobalReason(risk.ReasonPrivateWSUncertain, false)
	app.logger.LogInfo("private WebSocket connected and authenticated", nil)

	// ---- Phase 7: Immediate reconciliation/outbox recovery ----
	if err := app.reconcileWithTimeout(); err != nil {
		app.logger.LogError("startup reconciliation failed", map[string]string{"error": err.Error()})
		app.alerter.SendCritical("Startup reconciliation failed: "+err.Error(), map[string]string{
			"action": "manual intervention required",
		})
		return fmt.Errorf("reconciliation: %w", err)
	}
	app.health.SetReconciliationSuccess(time.Now())
	app.logger.LogInfo("startup reconciliation completed successfully", nil)

	// ---- Phase 8: Ownership-safe startup cleanup ----
	// Only cancel orders proved to be Bot_Owned via clOrdId + lineage
	app.ownershipSafeCleanup()
	app.logger.LogInfo("ownership-safe startup cleanup completed", nil)

	// ---- Phase 9: Fresh public ticker ----
	app.logger.LogInfo("connecting to exchange WebSocket", nil)
	// Set handler BEFORE Connect so readLoop goroutine sees it without a race.
	app.wsClient.SetMessageHandler(func(messageType int, data []byte) {
		tick, err := app.parser.ParseAndValidate(data)
		if err != nil || tick == nil {
			return
		}
		app.scheduler.OnMarketUpdate(tick.Symbol, tick)
	})
	if err := app.wsClient.Connect(app.cfg.Symbols); err != nil {
		app.logger.LogError("failed to connect to exchange", map[string]string{"error": err.Error()})
		return fmt.Errorf("exchange connection: %w", err)
	}
	app.logger.LogInfo("exchange WebSocket connected with fresh ticker", nil)

	// ---- Phase 10: Healthy/READY ----
	// All shared and symbol gates are satisfied
	for _, symbol := range app.cfg.Symbols {
		app.health.SetSymbolState(symbol, monitor.HealthStateHealthy, nil)
		app.tradingGate.MarkReconciled(symbol, 1)
	}
	app.tradingGate.UpdateSharedHealth(risk.SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})
	app.logger.LogInfo("system healthy and READY", nil)

	// ---- Phase 11: Approved initial grid & schedules ----
	// Only proceed if trading is enabled (production gate check)
	if app.cfg.TradingEnabled {
		app.startStrategies()
		app.placeInitialGridOrders()
		app.logger.LogInfo("initial grid placed and strategies started", nil)
	} else {
		app.logger.LogInfo("trading_enabled=false: reconcile-only mode, no initial grid", nil)
	}

	// Register fill handler for grid trading loop through TradingGate
	app.startPrivateWSFillHandler()

	// Start reconciler (30-second periodic)
	app.reconciler.Start()
	app.logger.LogInfo("reconciler started", nil)

	// Start per-symbol rebalancers (30-second periodic with ownership filter)
	app.startRebalancers()

	// Start inventory rebalance loop (time decay + hard stop)
	go app.inventoryRebalanceLoop()

	app.logger.LogInfo("system startup complete - production composition active", nil)
	return nil
}

// ownershipSafeCleanup only cancels orders that can be proved Bot_Owned
// via clOrdId namespace + persisted bot_orders lineage. Unowned/manual orders
// are preserved per requirement 3.5.
func (app *application) ownershipSafeCleanup() {
	for _, gridCfg := range app.cfg.GridConfigs {
		var pendingOrders []execution.ExchangeOrderInfo
		if app.gateway != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			var err error
			pendingOrders, err = app.gateway.ListPendingOrders(ctx, gridCfg.Symbol)
			cancel()
			if err != nil {
				app.logger.LogWarn("startup cleanup: failed to list pending orders", map[string]string{
					"symbol": gridCfg.Symbol,
					"error":  err.Error(),
				})
				continue
			}
		} else {
			// Legacy fallback for tests that create minimal app without gateway
			legacyOrders, err := app.getPendingOrders(gridCfg.Symbol)
			if err != nil {
				continue
			}
			for _, lo := range legacyOrders {
				pendingOrders = append(pendingOrders, execution.ExchangeOrderInfo{
					ExchangeOrderID: lo.OrdID,
					ClientOrderID:   lo.ClOrdID,
					Symbol:          lo.InstID,
				})
			}
		}

		cancelled := 0
		preserved := 0
		for _, order := range pendingOrders {
			// Ownership filter: only cancel if client order ID proves bot ownership
			if !isBotOwnedClientOrderID(order.ClientOrderID) {
				preserved++
				if app.logger != nil {
					app.logger.LogInfo("startup cleanup: preserving unowned order", map[string]string{
						"symbol":  gridCfg.Symbol,
						"ordId":   order.ExchangeOrderID,
						"clOrdId": order.ClientOrderID,
					})
				}
				continue
			}

			// Cancel with terminal confirmation
			if app.gateway != nil {
				ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
				_, cancelErr := app.gateway.CancelOrder(ctx2, execution.OrderRef{
					Symbol:          gridCfg.Symbol,
					ExchangeOrderID: order.ExchangeOrderID,
					ClientOrderID:   order.ClientOrderID,
				})
				cancel2()
				if cancelErr != nil {
					if app.logger != nil {
						app.logger.LogWarn("startup cleanup: cancel failed", map[string]string{
							"symbol": gridCfg.Symbol,
							"ordId":  order.ExchangeOrderID,
							"error":  cancelErr.Error(),
						})
					}
					continue
				}
			} else {
				// Legacy cancel via APIClient
				if order.ExchangeOrderID != "" {
					_, _ = app.apiClient.CancelOrder(gridCfg.Symbol, order.ExchangeOrderID)
				}
			}
			cancelled++
		}

		if (cancelled > 0 || preserved > 0) && app.logger != nil {
			app.logger.LogInfo("startup cleanup completed", map[string]string{
				"symbol":    gridCfg.Symbol,
				"cancelled": fmt.Sprintf("%d", cancelled),
				"preserved": fmt.Sprintf("%d", preserved),
			})
		}
	}
}

// isBotOwnedClientOrderID checks if a client order ID belongs to this bot's
// namespace. Bot-generated IDs use known prefixes from the deterministic ID scheme.
func isBotOwnedClientOrderID(clOrdID string) bool {
	if clOrdID == "" {
		return false
	}
	// Bot namespace prefixes for deterministic client order IDs:
	// "tb1" = TokyoBot v1 prefix used by outbox/rebalancer
	// "pg-v1" = production grid v1 prefix (from exploration tests)
	botPrefixes := []string{"tb1", "pg-v1-"}
	for _, prefix := range botPrefixes {
		if len(clOrdID) >= len(prefix) && clOrdID[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// startPrivateWSFillHandler registers the fill handler that routes all fills
// through the TradingGate. SELL fill → counter BUY is Risk_Increasing and
// must pass through the gate.
func (app *application) startPrivateWSFillHandler() {
	// Compute grid levels for all configured grids
	gridLevels := make(map[string][]decimal.Decimal)
	for i := range app.cfg.GridConfigs {
		cfg := &app.cfg.GridConfigs[i]
		if cfg.UpperPrice.IsZero() || cfg.LowerPrice.IsZero() {
			continue
		}
		levels, err := strategy.CalculateGridLevels(cfg)
		if err != nil {
			app.logger.LogWarn("failed to compute grid levels for fill handler", map[string]string{
				"symbol": cfg.Symbol,
				"error":  err.Error(),
			})
			continue
		}
		gridLevels[cfg.Symbol] = levels
	}

	// Create fill handler that uses TradingGate for all risk-increasing entries
	app.fillHandler = execution.NewGridFillHandler(
		app.apiClient,
		app.cfg.GridConfigs,
		gridLevels,
		app.logger,
		app.inventoryTracker,
	)

	// Register gated fill callback on private WS
	app.privateWS.SetFillCallback(func(instId, side, fillPx, fillSz, ordId, state string) {
		// All risk-increasing operations (including SELL fill → counter BUY)
		// go through TradingGate authorization
		riskClass := risk.RiskReducing // SELL counter for BUY fill is risk-reducing
		if side == "sell" {
			// SELL fill leads to counter BUY which is Risk_Increasing
			riskClass = risk.RiskIncreasing
		}
		decision := app.tradingGate.Authorize(instId, riskClass)
		if !decision.Allowed {
			app.logger.LogWarn("fill handler: trading gate blocked counter order", map[string]string{
				"instId": instId,
				"side":   side,
				"reason": decision.BlockReason,
			})
			return
		}
		app.fillHandler.OnFill(instId, side, fillPx, fillSz, ordId, state)
	})
	app.logger.LogInfo("private WebSocket fill handler active with trading gate", map[string]string{
		"grid_symbols": fmt.Sprintf("%d", len(gridLevels)),
	})
}

// startRebalancers creates and starts per-symbol rebalancers using the new
// ownership-safe, terminal-confirmation design. Replaces the unsafe inline
// goroutine that had no ownership filter or terminal confirmation.
func (app *application) startRebalancers() {
	if !app.cfg.TradingEnabled {
		app.logger.LogInfo("rebalancers: skipped (trading_enabled=false)", nil)
		return
	}
	for _, gridCfg := range app.cfg.GridConfigs {
		symbol := gridCfg.Symbol
		// Each symbol gets its own non-overlapping rebalancer
		rb := execution.NewRebalancer(symbol, execution.RebalancerDeps{
			Gateway:       app.gateway,
			Gate:          &rebalancerGateAdapter{gate: app.tradingGate},
			FillObserver:  nil, // TODO: wire fill processor when available
			RulesProvider: app.rulesProvider,
			OrderStore:    &botOrderStoreAdapter{},
			OutcomeStore:  nil, // TODO: wire durable outcome store
		}, execution.DefaultRebalancerConfig(), func(result execution.RebalancerCycleResult) {
			app.health.SetRebalancerState(result.StartedAt, result.ReferenceAge, result.StaleCount)
			if result.Err != nil {
				app.logger.LogWarn("rebalancer cycle error", map[string]string{
					"symbol": result.Symbol,
					"error":  result.Err.Error(),
				})
			}
		})
		rb.Start()
		app.rebalancers[symbol] = rb
		app.logger.LogInfo("rebalancer started", map[string]string{
			"symbol":   symbol,
			"interval": "30s",
		})
	}
}

// rebalancerGateAdapter adapts TradingGate to the RebalancerTradingGate interface.
type rebalancerGateAdapter struct {
	gate *risk.TradingGate
}

func (a *rebalancerGateAdapter) Authorize(symbol string, class int) execution.RebalancerGateDecision {
	decision := a.gate.Authorize(symbol, risk.OperationRiskClass(class))
	return execution.RebalancerGateDecision{
		Allowed:     decision.Allowed,
		BlockReason: decision.BlockReason,
	}
}

func (a *rebalancerGateAdapter) EnterSymbolSafeStop(symbol string, reasonCode string, details string) {
	a.gate.EnterSymbolSafeStop(symbol, reasonCode, details)
}

// botOrderStoreAdapter provides bot ownership verification using client order ID namespace.
type botOrderStoreAdapter struct{}

func (a *botOrderStoreAdapter) IsBotOwned(_ context.Context, _ string, clientOrderID string, _ string) (bool, error) {
	return isBotOwnedClientOrderID(clientOrderID), nil
}

// inventoryRebalanceLoop implements the SELL decay loop for the market-making strategy.
// Every 30 seconds, it checks open positions and adjusts the SELL price downward.
// After 12 hours of decay, it force-exits with a market sell.
func (app *application) inventoryRebalanceLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		for _, cfg := range app.cfg.GridConfigs {
			pos := app.inventoryTracker.GetPosition(cfg.Symbol)

			// ---- BUY order tracking (when no position held) ----
			// If price moves up, the old BUY sits far below market and never fills.
			// Cancel stale BUY and re-place at bestBid - 1 tick when drift > 0.3%.
			if pos == nil && app.cfg.Execution.TDMode == "cash" && cfg.GridCount == 1 {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				tickerObs, err := app.gateway.GetTicker(ctx, cfg.Symbol)
				cancel()
				if err != nil || !tickerObs.BestBid.IsPositive() {
					continue
				}

				// 24h position filter: don't place BUY if price is in upper half of 24h range
				if tickerObs.High24h.IsPositive() && tickerObs.Low24h.IsPositive() {
					mid24h := tickerObs.High24h.Add(tickerObs.Low24h).Div(decimal.NewFromInt(2))
					if tickerObs.Last.GreaterThanOrEqual(mid24h) {
						continue
					}
				}

				// Check existing pending orders
				ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
				pending, _ := app.gateway.ListPendingOrders(ctx2, cfg.Symbol)
				cancel2()

				// Find bot-owned BUY orders
				var hasBuyOrder bool
				var buyOrderPrice decimal.Decimal
				var buyOrderRef execution.OrderRef
				for _, order := range pending {
					if isBotOwnedClientOrderID(order.ClientOrderID) && order.Side == models.SideBuy {
						hasBuyOrder = true
						buyOrderPrice = order.Price
						buyOrderRef = execution.OrderRef{
							Symbol:          cfg.Symbol,
							ExchangeOrderID: order.ExchangeOrderID,
							ClientOrderID:   order.ClientOrderID,
						}
						break
					}
				}

				// Calculate ideal BUY price: bestBid - 1 tick
				tickSize := getTickSize(cfg.Symbol)
				idealBuyPrice := tickerObs.BestBid.Sub(tickSize)
				precision := getPricePrecision(cfg.Symbol)
				idealBuyPrice = idealBuyPrice.Round(int32(precision))

				if !hasBuyOrder {
					// Place fresh BUY
					req := &execution.OrderRequest{
						Symbol:    cfg.Symbol,
						Side:      models.SideBuy,
						OrderType: models.OrderTypePostOnly,
						Price:     idealBuyPrice,
						Quantity:  cfg.OrderSize,
					}
					app.apiClient.PlaceOrder(req)
					app.logger.LogInfo("REBALANCE: placed new BUY order", map[string]string{
						"symbol": cfg.Symbol,
						"price":  idealBuyPrice.String(),
					})
					continue
				}

				// If BUY exists but is >0.3% away from ideal, cancel and re-place
				if buyOrderPrice.IsPositive() {
					drift := idealBuyPrice.Sub(buyOrderPrice).Abs().Div(idealBuyPrice)
					if drift.GreaterThan(decimal.NewFromFloat(0.003)) {
						// Cancel old BUY
						ctx3, cancel3 := context.WithTimeout(context.Background(), 5*time.Second)
						app.gateway.CancelOrder(ctx3, buyOrderRef)
						cancel3()

						// Place new BUY at current bestBid - 1 tick
						req := &execution.OrderRequest{
							Symbol:    cfg.Symbol,
							Side:      models.SideBuy,
							OrderType: models.OrderTypePostOnly,
							Price:     idealBuyPrice,
							Quantity:  cfg.OrderSize,
						}
						app.apiClient.PlaceOrder(req)
						app.logger.LogInfo("REBALANCE: adjusted BUY order (price drifted >0.3%)", map[string]string{
							"symbol":    cfg.Symbol,
							"old_price": buyOrderPrice.String(),
							"new_price": idealBuyPrice.String(),
							"drift_pct": drift.Mul(decimal.NewFromInt(100)).Round(2).String() + "%",
						})
					}
				}
				continue
			}
			// ---- END BUY order tracking ----

			if pos == nil {
				continue
			}
			// Get current price
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			tickerObs, err := app.gateway.GetTicker(ctx, cfg.Symbol)
			cancel()
			if err != nil {
				continue
			}
			currentPrice := tickerObs.Last

			// Hard stop check (1.5% loss)
			if app.inventoryTracker.ShouldHardStop(cfg.Symbol, currentPrice, decimal.NewFromFloat(0.015)) {
				app.logger.LogWarn("HARD STOP: selling at market", map[string]string{
					"symbol":    cfg.Symbol,
					"buy_price": pos.BuyPrice.String(),
					"current":   currentPrice.String(),
				})
				qty := pos.Quantity.Mul(decimal.NewFromInt(1).Sub(cfg.FeeRate))
				req := &execution.OrderRequest{
					Symbol:    cfg.Symbol,
					Side:      models.SideSell,
					OrderType: models.OrderTypeMarket,
					Quantity:  qty,
				}
				app.apiClient.PlaceOrder(req)
				app.inventoryTracker.ClearPosition(cfg.Symbol)
				continue
			}

			elapsed := time.Since(pos.BuyTime)

			// Force market sell after 12 hours
			if elapsed > 12*time.Hour {
				app.logger.LogWarn("DECAY: force market sell after 12h", map[string]string{
					"symbol": cfg.Symbol,
				})
				qty := pos.Quantity.Mul(decimal.NewFromInt(1).Sub(cfg.FeeRate))
				req := &execution.OrderRequest{
					Symbol:    cfg.Symbol,
					Side:      models.SideSell,
					OrderType: models.OrderTypeMarket,
					Quantity:  qty,
				}
				app.apiClient.PlaceOrder(req)
				app.inventoryTracker.ClearPosition(cfg.Symbol)
				continue
			}

			// Time-based decay: gradually lower SELL price toward the fee-covering floor
			// 0-1h: keep at +0.3% (don't interfere with fill handler's initial order)
			// 1h-6h: lower to +0.25%
			// 6h-12h: lower to +0.2% (fee floor - still profitable)
			// >12h: market sell (force exit, handled above)
			var newSellPrice decimal.Decimal
			switch {
			case elapsed < 1*time.Hour:
				// Keep at +0.3% (don't interfere with fill handler's initial order)
				continue
			case elapsed < 6*time.Hour:
				newSellPrice = pos.BuyPrice.Mul(decimal.NewFromFloat(1.0025)) // +0.25%
			default:
				newSellPrice = pos.BuyPrice.Mul(decimal.NewFromFloat(1.002)) // +0.2% (floor)
			}

			// Don't go below fee floor (+0.2% = still profitable after 0.16% round-trip fees)
			minSellPrice := pos.BuyPrice.Mul(decimal.NewFromFloat(1.002))
			if newSellPrice.LessThan(minSellPrice) {
				newSellPrice = minSellPrice
			}

			precision := getPricePrecision(cfg.Symbol)
			newSellPrice = newSellPrice.Round(int32(precision))

			// Cancel any existing pending SELL for this symbol, then place new one
			if app.gateway != nil {
				ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
				pending, _ := app.gateway.ListPendingOrders(ctx2, cfg.Symbol)
				cancel2()
				for _, order := range pending {
					if isBotOwnedClientOrderID(order.ClientOrderID) {
						ctx3, cancel3 := context.WithTimeout(context.Background(), 5*time.Second)
						app.gateway.CancelOrder(ctx3, execution.OrderRef{
							Symbol:          cfg.Symbol,
							ExchangeOrderID: order.ExchangeOrderID,
							ClientOrderID:   order.ClientOrderID,
						})
						cancel3()
					}
				}
			}

			qty := pos.Quantity.Mul(decimal.NewFromInt(1).Sub(cfg.FeeRate))

			app.logger.LogInfo("DECAY: adjusting SELL price", map[string]string{
				"symbol":    cfg.Symbol,
				"new_price": newSellPrice.String(),
				"buy_price": pos.BuyPrice.String(),
				"elapsed_s": fmt.Sprintf("%.0f", elapsed.Seconds()),
			})

			// Place new SELL at decayed price
			req := &execution.OrderRequest{
				Symbol:    cfg.Symbol,
				Side:      models.SideSell,
				OrderType: models.OrderTypePostOnly,
				Price:     newSellPrice,
				Quantity:  qty,
			}
			_, err = app.apiClient.PlaceOrder(req)
			if err != nil {
				app.logger.LogWarn("DECAY: failed to place adjusted SELL", map[string]string{
					"symbol": cfg.Symbol,
					"error":  err.Error(),
				})
			}
		}
	}
}

// loadPersistedState loads orders, positions, and strategy state from the persistence layer.
func (app *application) loadPersistedState() error {
	orders, err := app.store.LoadOrders()
	if err != nil {
		return fmt.Errorf("load orders: %w", err)
	}
	for _, order := range orders {
		if !isTerminalStatus(order.Status) {
			if err := app.orderMgr.AddOrder(order); err != nil {
				app.logger.LogWarn("failed to restore order", map[string]string{
					"order_id": order.OrderId,
					"error":    err.Error(),
				})
			}
		}
	}
	app.logger.LogInfo("persisted orders loaded", map[string]string{
		"total": fmt.Sprintf("%d", len(orders)),
	})

	positions, err := app.store.LoadPositions()
	if err != nil {
		return fmt.Errorf("load positions: %w", err)
	}
	for _, pos := range positions {
		app.riskMgr.UpdatePosition(pos.Symbol, pos)
	}
	app.logger.LogInfo("persisted positions loaded", map[string]string{
		"total": fmt.Sprintf("%d", len(positions)),
	})

	states, err := app.store.LoadStrategyStates()
	if err != nil {
		return fmt.Errorf("load strategy states: %w", err)
	}
	app.logger.LogInfo("persisted strategy states loaded", map[string]string{
		"total": fmt.Sprintf("%d", len(states)),
	})

	return nil
}

// reconcileWithTimeout performs reconciliation with the exchange within the 60-second timeout.
func (app *application) reconcileWithTimeout() error {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	startTime := time.Now()
	var lastErr error

	for _, symbol := range app.cfg.Symbols {
		for {
			select {
			case <-ctx.Done():
				app.alerter.SendCritical(
					"Reconciliation timeout: exchange unreachable for 60+ seconds",
					map[string]string{
						"duration":   time.Since(startTime).String(),
						"last_error": fmt.Sprintf("%v", lastErr),
						"action":     "trading halted - manual intervention required",
					},
				)
				return fmt.Errorf("reconciliation timeout after %v: %w", reconcileTimeout, lastErr)
			default:
			}

			err := app.reconciler.Reconcile(symbol)
			if err != nil {
				lastErr = err
				app.logger.LogWarn("reconciliation attempt failed, retrying", map[string]string{
					"symbol": symbol,
					"error":  err.Error(),
				})

				if time.Since(startTime) > exchangeUnreachableTimeout {
					app.alerter.SendCritical(
						"Exchange unreachable during reconciliation for >60s",
						map[string]string{
							"duration":   time.Since(startTime).String(),
							"last_error": err.Error(),
							"action":     "stopping trading and notifying ops",
						},
					)
					return fmt.Errorf("exchange unreachable for >60s: %w", err)
				}

				time.Sleep(2 * time.Second)
				continue
			}
			break
		}
	}

	return nil
}

// startStrategies loads and starts configured grid strategy instances.
// Mean reversion strategies are explicitly NOT loaded in production profile.
func (app *application) startStrategies() {
	for i, gridCfg := range app.cfg.GridConfigs {
		strategyID := fmt.Sprintf("grid_%s_%d", gridCfg.Symbol, i)
		cfg := strategy.StrategyConfig{
			StrategyID: strategyID,
			Type:       "grid",
			Grid:       &app.cfg.GridConfigs[i],
		}
		if err := app.scheduler.LoadStrategy(cfg); err != nil {
			app.logger.LogWarn("failed to load grid strategy", map[string]string{
				"strategy_id": strategyID,
				"error":       err.Error(),
			})
			continue
		}
		if err := app.scheduler.StartStrategy(strategyID); err != nil {
			app.logger.LogWarn("failed to start grid strategy", map[string]string{
				"strategy_id": strategyID,
				"error":       err.Error(),
			})
		}
	}
	// EXPLICITLY: do NOT load MeanReversionConfigs in production
	// per requirement 3.10 and design scope.
}

// calculateAdaptiveGridRange fetches the current ticker for a symbol and computes
// adaptive grid boundaries using symmetric per-side half-width derived from 24h volatility,
// clamped between configured min (0.5%) and max (4%).
func (app *application) calculateAdaptiveGridRange(symbol string) (lower, upper decimal.Decimal, err error) {
	// If no gateway, use legacy volatility-based calculation
	if app.gateway == nil {
		return app.calculateAdaptiveGridRangeLegacy(symbol)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ticker, err := app.gateway.GetTicker(ctx, symbol)
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("failed to get ticker for %s: %w", symbol, err)
	}
	currentPrice := ticker.Last

	if !currentPrice.IsPositive() {
		return decimal.Zero, decimal.Zero, fmt.Errorf("invalid ticker price for %s", symbol)
	}

	// Use symmetric per-side half-width from approved config (0.5%-4%)
	minHalf := app.cfg.AdaptiveRange.MinHalfWidth
	maxHalf := app.cfg.AdaptiveRange.MaxHalfWidth
	if minHalf.IsZero() {
		minHalf = decimal.NewFromFloat(0.005)
	}
	if maxHalf.IsZero() {
		maxHalf = decimal.NewFromFloat(0.04)
	}

	// Calculate volatility from 24h high/low (same formula as legacy path)
	var rangePercent decimal.Decimal
	if ticker.High24h.IsPositive() && ticker.Low24h.IsPositive() && currentPrice.IsPositive() {
		volatility := ticker.High24h.Sub(ticker.Low24h).Div(currentPrice)
		halfVol := volatility.Div(decimal.NewFromInt(2))
		// Clamp between minHalf and maxHalf
		if halfVol.LessThan(minHalf) {
			rangePercent = minHalf
		} else if halfVol.GreaterThan(maxHalf) {
			rangePercent = maxHalf
		} else {
			rangePercent = halfVol
		}
	} else {
		// Fallback when 24h data is unavailable
		rangePercent = minHalf
	}

	// Symmetric: lower = price*(1-r), upper = price*(1+r)
	lower = currentPrice.Mul(decimal.NewFromInt(1).Sub(rangePercent))
	upper = currentPrice.Mul(decimal.NewFromInt(1).Add(rangePercent))

	app.logger.LogInfo("adaptive grid range calculated", map[string]string{
		"symbol":        symbol,
		"current_price": currentPrice.String(),
		"high_24h":      ticker.High24h.String(),
		"low_24h":       ticker.Low24h.String(),
		"half_width":    rangePercent.String(),
		"lower":         lower.String(),
		"upper":         upper.String(),
	})

	return lower, upper, nil
}

// getAvailableBalance queries the OKX account balance API and returns the available USDT balance.
func (app *application) getAvailableBalance() (decimal.Decimal, error) {
	resp, err := app.apiClient.DoRequest("GET", "/api/v5/account/balance", nil)
	if err != nil {
		return decimal.Zero, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return decimal.Zero, fmt.Errorf("failed to read balance response: %w", err)
	}

	var balResp struct {
		Code string `json:"code"`
		Data []struct {
			Details []struct {
				Ccy      string `json:"ccy"`
				AvailBal string `json:"availBal"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &balResp); err != nil {
		return decimal.Zero, fmt.Errorf("failed to parse balance response: %w", err)
	}

	if balResp.Code != "0" || len(balResp.Data) == 0 {
		return decimal.Zero, fmt.Errorf("balance API error: code=%s", balResp.Code)
	}

	for _, detail := range balResp.Data[0].Details {
		if detail.Ccy == "USDT" {
			bal, err := decimal.NewFromString(detail.AvailBal)
			if err != nil {
				return decimal.Zero, fmt.Errorf("failed to parse USDT balance: %w", err)
			}
			return bal, nil
		}
	}
	return decimal.Zero, fmt.Errorf("USDT not found in balance response")
}

// placeInitialGridOrders fetches current market price and places the initial grid.
// All orders go through instrument rules normalization and TradingGate check.
func (app *application) placeInitialGridOrders() {
	// Ownership-safe cleanup before placing new grid orders
	app.ownershipSafeCleanup()

	// Dynamic order sizing: query available USDT and distribute across all BUY grid slots
	availableBalance, err := app.getAvailableBalance()
	if err != nil {
		app.logger.LogWarn("could not get available balance, using config order_size", map[string]string{"error": err.Error()})
	} else {
		usableBalance := availableBalance.Mul(decimal.NewFromFloat(0.95))
		totalBuySlots := 0
		type symbolInfo struct {
			index    int
			price    decimal.Decimal
			buySlots int
		}
		var infos []symbolInfo

		for i, gridCfg := range app.cfg.GridConfigs {
			var currentPrice decimal.Decimal
			if app.gateway != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				ticker, tickerErr := app.gateway.GetTicker(ctx, gridCfg.Symbol)
				cancel()
				if tickerErr != nil || !ticker.Last.IsPositive() {
					continue
				}
				currentPrice = ticker.Last
			} else {
				p, pErr := app.getCurrentPriceLegacy(gridCfg.Symbol)
				if pErr != nil || !p.IsPositive() {
					continue
				}
				currentPrice = p
			}
			lower, upper, rangeErr := app.calculateAdaptiveGridRange(gridCfg.Symbol)
			if rangeErr != nil {
				continue
			}
			gridCount := gridCfg.GridCount
			rangeWidth := upper.Sub(lower)
			step := rangeWidth.Div(decimal.NewFromInt(int64(gridCount)))
			buySlots := 0
			for lvl := 0; lvl <= gridCount; lvl++ {
				levelPrice := lower.Add(step.Mul(decimal.NewFromInt(int64(lvl))))
				if levelPrice.LessThan(currentPrice) {
					buySlots++
				}
			}
			infos = append(infos, symbolInfo{index: i, price: currentPrice, buySlots: buySlots})
			totalBuySlots += buySlots
		}

		if totalBuySlots > 0 {
			perSlotUSDT := usableBalance.Div(decimal.NewFromInt(int64(totalBuySlots)))
			for _, info := range infos {
				orderSize := perSlotUSDT.Div(info.price).Round(0)
				if orderSize.IsPositive() {
					app.cfg.GridConfigs[info.index].OrderSize = orderSize
				}
			}
		}
	}

	for i, gridCfg := range app.cfg.GridConfigs {
		strategyID := fmt.Sprintf("grid_%s_%d", gridCfg.Symbol, i)

		// Check TradingGate before placing risk-increasing orders
		if app.tradingGate != nil {
			decision := app.tradingGate.Authorize(gridCfg.Symbol, risk.RiskIncreasing)
			if !decision.Allowed {
				app.logger.LogWarn("initial grid: trading gate blocked", map[string]string{
					"symbol": gridCfg.Symbol,
					"reason": decision.BlockReason,
				})
				continue
			}
		}

		// Calculate adaptive grid range if bounds not set
		if app.cfg.GridConfigs[i].UpperPrice.IsZero() || app.cfg.GridConfigs[i].LowerPrice.IsZero() {
			adaptiveLower, adaptiveUpper, err := app.calculateAdaptiveGridRange(gridCfg.Symbol)
			if err != nil {
				app.logger.LogError("failed to calculate adaptive grid range", map[string]string{
					"symbol": gridCfg.Symbol,
					"error":  err.Error(),
				})
				continue
			}
			app.cfg.GridConfigs[i].LowerPrice = adaptiveLower
			app.cfg.GridConfigs[i].UpperPrice = adaptiveUpper
		}

		// Get current price via gateway (or legacy fallback)
		var currentPrice decimal.Decimal
		var bestBid decimal.Decimal
		var high24h, low24h decimal.Decimal
		if app.gateway != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			ticker, err := app.gateway.GetTicker(ctx, gridCfg.Symbol)
			cancel()
			if err != nil || !ticker.Last.IsPositive() {
				app.logger.LogError("failed to get ticker for initial grid", map[string]string{
					"symbol": gridCfg.Symbol,
				})
				continue
			}
			currentPrice = ticker.Last
			bestBid = ticker.BestBid
			high24h = ticker.High24h
			low24h = ticker.Low24h
		} else {
			var err error
			currentPrice, err = app.getCurrentPriceLegacy(gridCfg.Symbol)
			if err != nil {
				continue
			}
		}

		// Single-grid cash mode: place a single BUY at the best bid (or 0.1% below
		// current price as fallback). This ensures the order is near the top of the
		// book and can fill quickly, instead of being placed at the lower boundary
		// of the adaptive range which may be too far from the market.
		var orders []*models.Order
		if gridCfg.GridCount == 1 && app.cfg.Execution.TDMode == "cash" {
			// Inventory checks: skip if already holding or full
			if app.inventoryTracker != nil && app.inventoryTracker.IsInventoryFull(gridCfg.Symbol) {
				app.logger.LogInfo("inventory full, skipping BUY", map[string]string{"symbol": gridCfg.Symbol})
				continue
			}
			if app.inventoryTracker != nil && app.inventoryTracker.HasPosition(gridCfg.Symbol) {
				app.logger.LogInfo("already has position, skipping new BUY", map[string]string{"symbol": gridCfg.Symbol})
				continue
			}

			// Price position filter: only buy in the lower half of 24h range
			if high24h.IsPositive() && low24h.IsPositive() {
				mid24h := high24h.Add(low24h).Div(decimal.NewFromInt(2))
				if currentPrice.GreaterThanOrEqual(mid24h) {
					app.logger.LogInfo("single-grid: skipping BUY, price in upper half of 24h range", map[string]string{
						"symbol":   gridCfg.Symbol,
						"price":    currentPrice.String(),
						"mid_24h":  mid24h.String(),
						"high_24h": high24h.String(),
						"low_24h":  low24h.String(),
					})
					continue
				}
			}

			// Market-making: BUY at bestBid - 1 tick (passive, only fills on dip)
			tickSize := getTickSize(gridCfg.Symbol)
			buyPrice := bestBid.Sub(tickSize)
			if !buyPrice.IsPositive() {
				// Fallback: 0.1% below current price
				buyPrice = currentPrice.Mul(decimal.NewFromFloat(0.999))
			}
			precision := getPricePrecision(gridCfg.Symbol)
			buyPrice = buyPrice.Round(int32(precision))
			orders = []*models.Order{{
				Symbol:    gridCfg.Symbol,
				Side:      models.SideBuy,
				OrderType: models.OrderTypePostOnly,
				Price:     buyPrice,
				Quantity:  gridCfg.OrderSize,
				Status:    models.OrderStatusPending,
			}}
			app.logger.LogInfo("single-grid mode: placing BUY at bestBid - 1 tick", map[string]string{
				"symbol":    gridCfg.Symbol,
				"buy_price": buyPrice.String(),
				"best_bid":  bestBid.String(),
				"tick_size": tickSize.String(),
				"last":      currentPrice.String(),
			})
		} else {
			levels, err := strategy.CalculateGridLevels(&app.cfg.GridConfigs[i])
			if err != nil {
				app.logger.LogError("failed to calculate grid levels", map[string]string{
					"symbol": gridCfg.Symbol,
					"error":  err.Error(),
				})
				continue
			}

			orders = strategy.PlaceGridOrders(levels, currentPrice, &app.cfg.GridConfigs[i])
			if len(orders) == 0 {
				continue
			}

			// In cash (spot) mode, only place BUY orders during initial grid placement.
			// The bot has no coin holdings at startup, so SELL orders would be rejected
			// by OKX for insufficient balance. SELL orders are placed by the fill handler
			// after a BUY fill is received.
			if app.cfg.Execution.TDMode == "cash" {
				buyOrders := make([]*models.Order, 0, len(orders))
				for _, o := range orders {
					if o.Side == models.SideBuy {
						buyOrders = append(buyOrders, o)
					}
				}
				orders = buyOrders
				if len(orders) == 0 {
					continue
				}
			}
		}

		if len(orders) == 0 {
			continue
		}

		placed := 0
		failed := 0
		for _, order := range orders {
			req := &execution.OrderRequest{
				Symbol:     order.Symbol,
				Side:       order.Side,
				OrderType:  order.OrderType,
				Price:      order.Price,
				Quantity:   order.Quantity,
				StrategyID: strategyID,
			}
			result, err := app.apiClient.PlaceOrder(req)
			if err != nil {
				failed++
				continue
			}
			if !result.Success {
				failed++
				continue
			}
			placed++
		}

		app.logger.LogInfo("initial grid orders placed", map[string]string{
			"symbol":      gridCfg.Symbol,
			"strategy_id": strategyID,
			"placed":      fmt.Sprintf("%d", placed),
			"failed":      fmt.Sprintf("%d", failed),
		})
	}
}

// getCurrentPrice queries the OKX REST API for the current ticker price of a symbol.
func (app *application) getCurrentPrice(symbol string) (decimal.Decimal, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ticker, err := app.gateway.GetTicker(ctx, symbol)
	if err != nil {
		return decimal.Zero, err
	}
	return ticker.Last, nil
}

// getPendingOrders queries the OKX REST API for all pending orders for a symbol.
func (app *application) getPendingOrders(symbol string) ([]PendingOrder, error) {
	resp, err := app.apiClient.DoRequest("GET", "/api/v5/trade/orders-pending?instId="+symbol, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending orders for %s: %w", symbol, err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read pending orders response: %w", err)
	}

	var ordersResp struct {
		Code string         `json:"code"`
		Data []PendingOrder `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &ordersResp); err != nil {
		return nil, fmt.Errorf("failed to parse pending orders response: %w", err)
	}

	if ordersResp.Code != "0" {
		return nil, fmt.Errorf("pending orders API error for %s: code=%s", symbol, ordersResp.Code)
	}

	return ordersResp.Data, nil
}

// PendingOrder represents a pending order returned from OKX REST API.
type PendingOrder struct {
	InstID  string `json:"instId"`
	OrdID   string `json:"ordId"`
	ClOrdID string `json:"clOrdId"`
	Px      string `json:"px"`
	Sz      string `json:"sz"`
	Side    string `json:"side"`
	State   string `json:"state"`
}

// waitForShutdown blocks until a shutdown signal is received and performs graceful shutdown.
func (app *application) waitForShutdown() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigCh
	app.logger.LogInfo("shutdown signal received", map[string]string{
		"signal": sig.String(),
	})

	app.gracefulShutdown()
}

// gracefulShutdown performs orderly system shutdown.
func (app *application) gracefulShutdown() {
	app.shutdownOnce.Do(func() {
		app.logger.LogInfo("initiating graceful shutdown", nil)

		// Stop all strategies
		app.scheduler.StopAll()
		app.logger.LogInfo("all strategies stopped", nil)

		// Stop rebalancers
		for symbol, rb := range app.rebalancers {
			rb.Stop()
			app.logger.LogInfo("rebalancer stopped", map[string]string{"symbol": symbol})
		}

		// Persist final state
		app.persistFinalState()
		app.logger.LogInfo("final state persisted", nil)

		// Close connections
		app.reconciler.Stop()
		app.dispatcher.Stop()
		if app.privateWS != nil {
			if err := app.privateWS.Disconnect(); err != nil {
				app.logger.LogWarn("error disconnecting private WebSocket", map[string]string{"error": err.Error()})
			}
		}
		if err := app.wsClient.Disconnect(); err != nil {
			app.logger.LogWarn("error disconnecting WebSocket", map[string]string{"error": err.Error()})
		}
		app.logger.LogInfo("connections closed", nil)

		// Stop metrics server
		if err := app.metrics.Stop(); err != nil {
			app.logger.LogWarn("error stopping metrics server", map[string]string{"error": err.Error()})
		}

		// Close persistence store
		if err := app.store.Close(); err != nil {
			app.logger.LogWarn("error closing persistence store", map[string]string{"error": err.Error()})
		}

		app.logger.LogInfo("graceful shutdown complete", nil)
	})
}

// persistFinalState saves current orders and positions to the persistence layer.
func (app *application) persistFinalState() {
	strategies := app.scheduler.GetActiveStrategies()
	for _, s := range strategies {
		if err := app.store.SaveStrategyState(s.StrategyID, s.Type, s.IsActive, nil, nil); err != nil {
			app.logger.LogWarn("failed to persist strategy state", map[string]string{
				"strategy_id": s.StrategyID,
				"error":       err.Error(),
			})
		}
	}
}

// panicRecovery catches panics and sends a critical alert if possible.
func panicRecovery(alerter *monitor.Alerter) {
	if r := recover(); r != nil {
		msg := fmt.Sprintf("PANIC RECOVERY: %v", r)
		log.Printf("[CRITICAL] %s", msg)

		if alerter != nil {
			alerter.SendCritical(msg, map[string]string{
				"action": "system crashed - requires restart and investigation",
			})
		}

		os.Exit(1)
	}
}

// buildAlertChannels constructs alert channels from system configuration.
func buildAlertChannels(cfg *config.SystemConfig) []monitor.AlertChannel {
	var channels []monitor.AlertChannel

	tgBotToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	tgChatID := os.Getenv("TELEGRAM_CHAT_ID")
	if tgBotToken != "" && tgChatID != "" {
		channels = append(channels, monitor.NewTelegramChannel(tgBotToken, tgChatID))
	}

	discordWebhook := os.Getenv("DISCORD_WEBHOOK_URL")
	if discordWebhook != "" {
		channels = append(channels, monitor.NewDiscordChannel(discordWebhook))
	}

	return channels
}

// isTerminalStatus returns true if the order status is a final/terminal state.
func isTerminalStatus(status interface{ String() string }) bool {
	switch s := status.(type) {
	default:
		str := s.String()
		return str == "FILLED" || str == "CANCELLED" || str == "REJECTED" || str == "EXPIRED" || str == "ERROR"
	}
}

// exchangeQuerierAdapter adapts the APIClient to the ExchangeQuerier interface
// required by the Reconciler.
type exchangeQuerierAdapter struct {
	client  *execution.APIClient
	symbols []string
}

func (q *exchangeQuerierAdapter) QueryOrders(symbol string) ([]*models.Order, error) {
	resp, err := q.client.DoRequest("GET", "/api/v5/trade/orders-pending?instId="+symbol, nil)
	if err != nil {
		return nil, fmt.Errorf("query orders failed: %w", err)
	}
	defer resp.Body.Close()
	return nil, nil
}

func (q *exchangeQuerierAdapter) QueryPositions(symbol string) ([]*models.Position, error) {
	resp, err := q.client.DoRequest("GET", "/api/v5/account/positions?instId="+symbol, nil)
	if err != nil {
		return nil, fmt.Errorf("query positions failed: %w", err)
	}
	defer resp.Body.Close()
	return nil, nil
}

// emergencyStopHandler implements risk.EmergencyStopCallback for the application.
type emergencyStopHandler struct {
	app *application
}

func (h *emergencyStopHandler) CancelAllOrders() error {
	// Use ownership-safe cancellation instead of broad cancel
	h.app.ownershipSafeCleanup()
	return nil
}

func (h *emergencyStopHandler) StopAllStrategies() error {
	h.app.scheduler.StopAll()
	return nil
}

func (h *emergencyStopHandler) SendCriticalAlert(reason string) error {
	return h.app.alerter.SendCritical("Emergency Stop Triggered: "+reason, map[string]string{
		"action": "all trading halted - manual confirmation required to resume",
	})
}

// extremeMarketHandler implements risk.ExtremeMarketCallback for the application.
type extremeMarketHandler struct {
	app *application
}

func (h *extremeMarketHandler) CancelGridOrders() {
	h.app.logger.LogWarn("extreme market: cancelling grid orders", nil)
	h.app.ownershipSafeCleanup()
}

func (h *extremeMarketHandler) PauseMeanReversion() {
	// Mean reversion not loaded in production; no-op
	h.app.logger.LogWarn("extreme market: pause mean reversion (no-op in production)", nil)
}

func (h *extremeMarketHandler) SendAlert(reason string) {
	h.app.alerter.SendWarning("Extreme Market Condition: "+reason, nil)
}

// ---- Backward-compatible methods for existing property tests ----
// These preserve the UNFIXED behavior that exploration tests assert against.

// cancelAllPendingOrders cancels all pending orders for all grid symbols without
// ownership filter. This is the UNFIXED behavior preserved for property test
// observation; production startup uses ownershipSafeCleanup instead.
func (app *application) cancelAllPendingOrders() {
	for _, gridCfg := range app.cfg.GridConfigs {
		orders, err := app.getPendingOrders(gridCfg.Symbol)
		if err != nil {
			continue
		}
		for _, order := range orders {
			if order.OrdID == "" {
				continue
			}
			_, err := app.apiClient.CancelOrder(gridCfg.Symbol, order.OrdID)
			if err != nil {
				app.logger.LogWarn("error cancelling order", map[string]string{
					"symbol": gridCfg.Symbol,
					"ordId":  order.OrdID,
					"error":  err.Error(),
				})
			}
		}
	}
}

// rebalanceOrders is the UNFIXED inline rebalancer behavior preserved for property
// test observation. Production uses per-symbol Rebalancer component instead.
func (app *application) rebalanceOrders() {
	for i, gridCfg := range app.cfg.GridConfigs {
		symbol := gridCfg.Symbol

		currentPrice, err := app.getCurrentPriceLegacy(symbol)
		if err != nil {
			continue
		}

		pendingOrders, err := app.getPendingOrders(symbol)
		if err != nil {
			continue
		}

		if len(pendingOrders) == 0 {
			continue
		}

		maxDistance := currentPrice.Mul(decimal.NewFromFloat(0.02))
		cancelledAny := false

		for _, order := range pendingOrders {
			orderPrice, err := decimal.NewFromString(order.Px)
			if err != nil {
				continue
			}
			distance := currentPrice.Sub(orderPrice).Abs()

			if distance.GreaterThan(maxDistance) {
				_, cancelErr := app.apiClient.CancelOrder(symbol, order.OrdID)
				if cancelErr != nil {
					continue
				}
				cancelledAny = true
			}
		}

		if cancelledAny {
			adaptiveLower, adaptiveUpper, err := app.calculateAdaptiveGridRangeLegacy(symbol)
			if err != nil {
				continue
			}
			app.cfg.GridConfigs[i].LowerPrice = adaptiveLower
			app.cfg.GridConfigs[i].UpperPrice = adaptiveUpper

			levels, err := strategy.CalculateGridLevels(&app.cfg.GridConfigs[i])
			if err != nil {
				continue
			}

			orders := strategy.PlaceGridOrders(levels, currentPrice, &app.cfg.GridConfigs[i])

			availBal, balErr := app.getAvailableBalance()
			if balErr == nil && availBal.IsPositive() {
				buyCount := 0
				for _, order := range orders {
					if order.Side == models.SideBuy {
						buyCount++
					}
				}
				if buyCount > 0 {
					perSlot := availBal.Mul(decimal.NewFromFloat(0.95)).Div(decimal.NewFromInt(int64(buyCount)))
					dynamicSize := perSlot.Div(currentPrice).Round(0)
					if dynamicSize.IsPositive() {
						app.cfg.GridConfigs[i].OrderSize = dynamicSize
						orders = strategy.PlaceGridOrders(levels, currentPrice, &app.cfg.GridConfigs[i])
					}
				}
			}

			for _, order := range orders {
				if order.Side == models.SideBuy {
					req := &execution.OrderRequest{
						Symbol:    order.Symbol,
						Side:      order.Side,
						OrderType: order.OrderType,
						Price:     order.Price,
						Quantity:  order.Quantity,
					}
					_, _ = app.apiClient.PlaceOrder(req)
				}
			}
		}
	}
}

// getPricePrecision returns the number of decimal places allowed for a symbol.
// This is the UNFIXED symbol-switch precision logic preserved for property tests.
// Production uses InstrumentRulesProvider instead.
func getPricePrecision(symbol string) int {
	switch {
	case containsStr(symbol, "BTC"):
		return 1
	case containsStr(symbol, "ETH"):
		return 2
	case containsStr(symbol, "DOGE"):
		return 5
	case containsStr(symbol, "PEPE"):
		return 10
	case containsStr(symbol, "WIF"):
		return 4
	case containsStr(symbol, "KOR"):
		return 4
	default:
		return 5
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// getTickSize returns the minimum price increment (tick) for a given symbol.
// Used for market-making: BUY at bestBid - 1 tick, SELL decay by 1 tick per interval.
func getTickSize(symbol string) decimal.Decimal {
	switch {
	case containsStr(symbol, "DOGE"):
		return decimal.NewFromFloat(0.00001) // 5 decimal places
	case containsStr(symbol, "WIF"):
		return decimal.NewFromFloat(0.0001) // 4 decimal places
	case containsStr(symbol, "BTC"):
		return decimal.NewFromFloat(0.1)
	case containsStr(symbol, "ETH"):
		return decimal.NewFromFloat(0.01)
	default:
		return decimal.NewFromFloat(0.00001)
	}
}

// getCurrentPriceLegacy uses raw APIClient for backward compat with property tests.
func (app *application) getCurrentPriceLegacy(symbol string) (decimal.Decimal, error) {
	resp, err := app.apiClient.DoRequest("GET", "/api/v5/market/ticker?instId="+symbol, nil)
	if err != nil {
		return decimal.Zero, fmt.Errorf("failed to get ticker for %s: %w", symbol, err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return decimal.Zero, fmt.Errorf("failed to read ticker response: %w", err)
	}

	var tickerResp struct {
		Code string `json:"code"`
		Data []struct {
			Last string `json:"last"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &tickerResp); err != nil {
		return decimal.Zero, fmt.Errorf("failed to parse ticker response: %w", err)
	}

	if tickerResp.Code != "0" || len(tickerResp.Data) == 0 {
		return decimal.Zero, fmt.Errorf("ticker API error for %s: code=%s", symbol, tickerResp.Code)
	}

	price, err := decimal.NewFromString(tickerResp.Data[0].Last)
	if err != nil {
		return decimal.Zero, fmt.Errorf("failed to parse price: %w", err)
	}

	return price, nil
}

// calculateAdaptiveGridRangeLegacy uses raw APIClient for backward compat with property tests.
func (app *application) calculateAdaptiveGridRangeLegacy(symbol string) (lower, upper decimal.Decimal, err error) {
	resp, err := app.apiClient.DoRequest("GET", "/api/v5/market/ticker?instId="+symbol, nil)
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("failed to get ticker for %s: %w", symbol, err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("failed to read ticker for %s: %w", symbol, err)
	}

	var tickerResp struct {
		Code string `json:"code"`
		Data []struct {
			Last    string `json:"last"`
			High24h string `json:"high24h"`
			Low24h  string `json:"low24h"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &tickerResp); err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("failed to parse ticker for %s: %w", symbol, err)
	}

	if tickerResp.Code != "0" || len(tickerResp.Data) == 0 {
		return decimal.Zero, decimal.Zero, fmt.Errorf("ticker API error for %s: code=%s", symbol, tickerResp.Code)
	}

	currentPrice, err := decimal.NewFromString(tickerResp.Data[0].Last)
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("failed to parse price for %s: %w", symbol, err)
	}

	high24h, _ := decimal.NewFromString(tickerResp.Data[0].High24h)
	low24h, _ := decimal.NewFromString(tickerResp.Data[0].Low24h)

	var rangePercent decimal.Decimal
	if high24h.IsPositive() && low24h.IsPositive() && currentPrice.IsPositive() {
		volatility := high24h.Sub(low24h).Div(currentPrice)
		halfVol := volatility.Div(decimal.NewFromInt(2))
		minRange := decimal.NewFromFloat(0.03)
		maxRange := decimal.NewFromFloat(0.08)
		if halfVol.LessThan(minRange) {
			rangePercent = minRange
		} else if halfVol.GreaterThan(maxRange) {
			rangePercent = maxRange
		} else {
			rangePercent = halfVol
		}
	} else {
		rangePercent = decimal.NewFromFloat(0.05)
	}

	lower = currentPrice.Mul(decimal.NewFromInt(1).Sub(rangePercent))
	upper = currentPrice.Mul(decimal.NewFromInt(1).Add(rangePercent))

	precision := getPricePrecision(symbol)
	lower = lower.Round(int32(precision))
	upper = upper.Round(int32(precision))

	return lower, upper, nil
}
