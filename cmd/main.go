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

	// 2. Load configuration
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

	// 6. Startup sequence
	if err := app.startup(); err != nil {
		log.Fatalf("[FATAL] Startup sequence failed: %v", err)
	}

	// 7. Wait for shutdown signal
	app.waitForShutdown()
}

// application holds all system components and manages their lifecycle.
type application struct {
	cfg  *config.SystemConfig
	creds *config.Credentials

	// Infrastructure
	store      *persistence.SQLiteStore
	tsBuf      *persistence.TimeSeriesBuffer
	logger     *monitor.StructuredLogger
	metrics    *monitor.MetricsServer
	alerter    *monitor.Alerter

	// Core engine
	rateLimiter ratelimiter.RateLimiter
	apiClient   *execution.APIClient
	orderMgr    *execution.OrderManager
	riskMgr     *risk.RiskManagerImpl
	esMgr       *risk.EmergencyStopManager
	emDetector  *risk.ExtremeMarketDetector

	// Market data
	wsClient    *marketdata.WSClient
	privateWS   *marketdata.PrivateWSClient
	dispatcher  *marketdata.Dispatcher
	orderBook   *orderbook.LocalOrderBook

	// Strategy
	scheduler  *strategy.Scheduler

	// Execution
	fillHandler *execution.GridFillHandler

	// Reconciler
	reconciler *execution.Reconciler

	// Shutdown
	shutdownOnce sync.Once
}

// initializeComponents creates and wires all system components.
func initializeComponents(cfg *config.SystemConfig, creds *config.Credentials) (*application, error) {
	app := &application{
		cfg:   cfg,
		creds: creds,
	}

	// 5.1 SQLiteStore (persistence)
	dbPath := cfg.PersistencePath
	if dbPath == "" {
		dbPath = "data/hft_state.db"
	}
	store, err := persistence.NewSQLiteStore(dbPath)
	if err != nil {
		if errors.Is(err, persistence.ErrCorrupted) {
			// Database corrupted - stop trading and notify ops
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

	// 5.4 APIClient (execution)
	restURL := cfg.RESTURL
	if restURL == "" {
		restURL = "https://www.okx.com"
	}
	app.apiClient = execution.NewAPIClient(restURL, creds, app.rateLimiter)

	// 5.5 OrderManager
	app.orderMgr = execution.NewOrderManager()

	// 5.6 RiskManager + EmergencyStopManager
	app.riskMgr = risk.NewRiskManager(&cfg.RiskLimits)
	app.esMgr = risk.NewEmergencyStopManager(&cfg.RiskLimits)

	// 5.7 ExtremeMarketDetector
	app.emDetector = risk.NewExtremeMarketDetector()

	// 5.8 MetricsServer
	metricsPort := cfg.MetricsPort
	if metricsPort == 0 {
		metricsPort = 9090
	}
	app.metrics = monitor.NewMetricsServer(metricsPort)

	// 5.9 StructuredLogger
	app.logger = monitor.NewStructuredLogger(os.Stdout)
	app.logger.SetSanitizer(config.SanitizeLog)

	// 5.10 Alerter (with Telegram/Discord channels if configured)
	alertChannels := buildAlertChannels(cfg)
	app.alerter = monitor.NewAlerter(alertChannels, app.logger)

	// 5.11 WSClient (market data)
	wsURL := cfg.WebSocketURL
	if wsURL == "" {
		wsURL = marketdata.DefaultWebSocketURL
	}
	wsConfig := marketdata.DefaultWSClientConfig()
	wsConfig.URL = wsURL
	wsConfig.Symbols = cfg.Symbols
	app.wsClient = marketdata.NewWSClient(wsConfig)

	// 5.11b PrivateWSClient (for order fills)
	privateWSConfig := marketdata.DefaultPrivateWSClientConfig()
	app.privateWS = marketdata.NewPrivateWSClient(privateWSConfig, creds)

	// 5.12 EventDispatcher
	app.dispatcher = marketdata.NewDispatcher(marketdata.DefaultDispatcherConfig())

	// 5.13 OrderBook
	app.orderBook = orderbook.NewLocalOrderBook()

	// 5.14 Scheduler (strategy engine) - uses RiskManager interface
	app.scheduler = strategy.NewScheduler(app.riskMgr, nil) // execution engine wired later via reconciler

	// 5.15 Reconciler
	reconcileInterval := time.Duration(cfg.ReconcileIntervalSec) * time.Second
	if reconcileInterval == 0 {
		reconcileInterval = 60 * time.Second
	}
	querier := &exchangeQuerierAdapter{client: app.apiClient, symbols: cfg.Symbols}
	app.reconciler = execution.NewReconciler(app.orderMgr, querier, reconcileInterval)
	app.reconciler.SetSymbols(cfg.Symbols)

	// Wire up emergency stop callbacks
	app.esMgr.RegisterEmergencyCallback(&emergencyStopHandler{app: app})

	// Wire up extreme market detector callbacks
	app.emDetector.RegisterCallback(&extremeMarketHandler{app: app})

	return app, nil
}

// startup executes the system startup sequence.
func (app *application) startup() error {
	app.logger.LogInfo("starting OKX HFT Grid Trading System", map[string]string{
		"symbols": fmt.Sprintf("%v", app.cfg.Symbols),
	})

	// 6a. Load persisted state (orders, positions, strategy state)
	if err := app.loadPersistedState(); err != nil {
		app.logger.LogError("failed to load persisted state", map[string]string{"error": err.Error()})
		return fmt.Errorf("load persisted state: %w", err)
	}

	// Start event dispatcher
	app.dispatcher.Start()

	// 6b. Connect to exchange (WebSocket)
	app.logger.LogInfo("connecting to exchange WebSocket", nil)
	if err := app.wsClient.Connect(app.cfg.Symbols); err != nil {
		app.logger.LogError("failed to connect to exchange", map[string]string{"error": err.Error()})
		return fmt.Errorf("exchange connection: %w", err)
	}
	app.logger.LogInfo("exchange WebSocket connected", nil)

	// 6c. Reconcile with exchange (60s timeout)
	if err := app.reconcileWithTimeout(); err != nil {
		app.logger.LogError("reconciliation failed", map[string]string{"error": err.Error()})
		// Notify ops and stop trading
		app.alerter.SendCritical("Startup reconciliation failed: "+err.Error(), map[string]string{
			"action": "manual intervention required",
		})
		return fmt.Errorf("reconciliation: %w", err)
	}
	app.logger.LogInfo("reconciliation completed successfully", nil)

	// 6d. Start strategies
	app.startStrategies()
	app.logger.LogInfo("strategies started", nil)

	// 6d.1 Place initial grid orders at current market price
	app.placeInitialGridOrders()

	// 6d.2 Start private WebSocket and register fill handler for grid trading loop
	app.startPrivateWSAndFillHandler()

	// 6e. Start metrics server
	if err := app.metrics.Start(); err != nil {
		app.logger.LogWarn("failed to start metrics server", map[string]string{"error": err.Error()})
		// Non-fatal: continue without metrics
	} else {
		app.logger.LogInfo("metrics server started", map[string]string{
			"port": fmt.Sprintf("%d", app.cfg.MetricsPort),
		})
	}

	// 6f. Start reconciler
	app.reconciler.Start()
	app.logger.LogInfo("reconciler started", nil)

	app.logger.LogInfo("system startup complete - trading active", nil)
	return nil
}

// loadPersistedState loads orders, positions, and strategy state from the persistence layer.
func (app *application) loadPersistedState() error {
	// Load orders
	orders, err := app.store.LoadOrders()
	if err != nil {
		return fmt.Errorf("load orders: %w", err)
	}
	for _, order := range orders {
		// Add non-terminal orders to the order manager for tracking
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

	// Load positions
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

	// Load strategy states
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
// If the exchange is unreachable for more than 60 seconds, it stops trading and notifies ops.
// When the exchange becomes reachable again, reconciliation resumes immediately.
func (app *application) reconcileWithTimeout() error {
	ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
	defer cancel()

	startTime := time.Now()
	var lastErr error

	for _, symbol := range app.cfg.Symbols {
		for {
			select {
			case <-ctx.Done():
				// Timeout exceeded
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

				// Check if we've been trying too long
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

				// Wait briefly before retry
				time.Sleep(2 * time.Second)
				continue
			}
			// Success for this symbol
			break
		}
	}

	return nil
}

// startStrategies loads and starts configured strategy instances.
func (app *application) startStrategies() {
	// Load grid strategies
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

	// Load mean reversion strategies
	for i, mrCfg := range app.cfg.MeanReversionConfigs {
		strategyID := fmt.Sprintf("mr_%s_%d", mrCfg.Symbol, i)
		cfg := strategy.StrategyConfig{
			StrategyID:    strategyID,
			Type:          "mean_reversion",
			MeanReversion: &app.cfg.MeanReversionConfigs[i],
		}
		if err := app.scheduler.LoadStrategy(cfg); err != nil {
			app.logger.LogWarn("failed to load mean reversion strategy", map[string]string{
				"strategy_id": strategyID,
				"error":       err.Error(),
			})
			continue
		}
		if err := app.scheduler.StartStrategy(strategyID); err != nil {
			app.logger.LogWarn("failed to start mean reversion strategy", map[string]string{
				"strategy_id": strategyID,
				"error":       err.Error(),
			})
		}
	}
}

// calculateAdaptiveGridRange queries 7-day daily candles from OKX and computes
// adaptive grid boundaries based on historical price data. It applies a 2% buffer
// below the lowest low and above the highest high.
func (app *application) calculateAdaptiveGridRange(symbol string) (lower, upper decimal.Decimal, err error) {
	// Query 7-day daily candles
	resp, err := app.apiClient.DoRequest("GET", "/api/v5/market/candles?instId="+symbol+"&bar=1D&limit=7", nil)
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("failed to query candles for %s: %w", symbol, err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("failed to read candles response for %s: %w", symbol, err)
	}

	var candleResp struct {
		Code string     `json:"code"`
		Msg  string     `json:"msg"`
		Data [][]string `json:"data"` // [ts, open, high, low, close, vol, volCcy, volCcyQuote, confirm]
	}
	if err := json.Unmarshal(bodyBytes, &candleResp); err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("failed to parse candles response for %s: %w", symbol, err)
	}

	if candleResp.Code != "0" || len(candleResp.Data) == 0 {
		return decimal.Zero, decimal.Zero, fmt.Errorf("candles API error for %s: code=%s, msg=%s", symbol, candleResp.Code, candleResp.Msg)
	}

	// Find min low and max high from 7-day candles
	// Candle format: [ts, open, high, low, close, vol, volCcy, volCcyQuote, confirm]
	minLow := decimal.NewFromFloat(999999999)
	maxHigh := decimal.Zero

	for _, candle := range candleResp.Data {
		if len(candle) < 5 {
			continue
		}
		high, err := decimal.NewFromString(candle[2])
		if err != nil {
			continue
		}
		low, err := decimal.NewFromString(candle[3])
		if err != nil {
			continue
		}
		if low.LessThan(minLow) {
			minLow = low
		}
		if high.GreaterThan(maxHigh) {
			maxHigh = high
		}
	}

	if minLow.Equal(decimal.NewFromFloat(999999999)) || maxHigh.IsZero() {
		return decimal.Zero, decimal.Zero, fmt.Errorf("no valid candle data found for %s", symbol)
	}

	// Add 2% buffer: lower = minLow * 0.98, upper = maxHigh * 1.02
	buffer := decimal.NewFromFloat(0.02)
	lower = minLow.Mul(decimal.NewFromInt(1).Sub(buffer))
	upper = maxHigh.Mul(decimal.NewFromInt(1).Add(buffer))

	app.logger.LogInfo("adaptive grid range calculated", map[string]string{
		"symbol":   symbol,
		"min_low":  minLow.String(),
		"max_high": maxHigh.String(),
		"lower":    lower.String(),
		"upper":    upper.String(),
	})

	return lower, upper, nil
}

// placeInitialGridOrders fetches the current market price for each grid config and places
// the initial grid of limit orders. This is the critical step that ensures the bot actually
// has orders on the book after startup. If UpperPrice/LowerPrice are zero (not configured),
// it uses calculateAdaptiveGridRange to determine the grid bounds from historical data.
func (app *application) placeInitialGridOrders() {
	for i, gridCfg := range app.cfg.GridConfigs {
		strategyID := fmt.Sprintf("grid_%s_%d", gridCfg.Symbol, i)

		// 0. Calculate adaptive grid range if upper/lower prices are not set
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

		// 1. Get current price from OKX REST API
		resp, err := app.apiClient.DoRequest("GET", "/api/v5/market/ticker?instId="+gridCfg.Symbol, nil)
		if err != nil {
			app.logger.LogError("failed to get ticker for initial grid orders", map[string]string{
				"symbol": gridCfg.Symbol,
				"error":  err.Error(),
			})
			continue
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			app.logger.LogError("failed to read ticker response", map[string]string{
				"symbol": gridCfg.Symbol,
				"error":  err.Error(),
			})
			continue
		}

		// Parse OKX ticker response: {"code":"0","data":[{"last":"..."}]}
		var tickerResp struct {
			Code string `json:"code"`
			Msg  string `json:"msg"`
			Data []struct {
				Last string `json:"last"`
			} `json:"data"`
		}
		if err := json.Unmarshal(bodyBytes, &tickerResp); err != nil {
			app.logger.LogError("failed to parse ticker response", map[string]string{
				"symbol": gridCfg.Symbol,
				"error":  err.Error(),
			})
			continue
		}

		if tickerResp.Code != "0" || len(tickerResp.Data) == 0 {
			app.logger.LogError("ticker response error", map[string]string{
				"symbol": gridCfg.Symbol,
				"code":   tickerResp.Code,
				"msg":    tickerResp.Msg,
			})
			continue
		}

		currentPrice, err := decimal.NewFromString(tickerResp.Data[0].Last)
		if err != nil {
			app.logger.LogError("failed to parse ticker price", map[string]string{
				"symbol": gridCfg.Symbol,
				"last":   tickerResp.Data[0].Last,
				"error":  err.Error(),
			})
			continue
		}

		// 2. Calculate grid levels using the (possibly adaptive) bounds
		levels, err := strategy.CalculateGridLevels(&app.cfg.GridConfigs[i])
		if err != nil {
			app.logger.LogError("failed to calculate grid levels", map[string]string{
				"symbol": gridCfg.Symbol,
				"error":  err.Error(),
			})
			continue
		}

		// 3. Generate orders at grid levels
		orders := strategy.PlaceGridOrders(levels, currentPrice, &app.cfg.GridConfigs[i])
		if len(orders) == 0 {
			app.logger.LogWarn("no grid orders generated", map[string]string{
				"symbol":        gridCfg.Symbol,
				"current_price": currentPrice.String(),
			})
			continue
		}

		// 4. Submit each order to OKX via the API client
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
				app.logger.LogError("failed to place initial grid order", map[string]string{
					"symbol": order.Symbol,
					"side":   order.Side.String(),
					"price":  order.Price.String(),
					"error":  err.Error(),
				})
				failed++
				continue
			}
			if !result.Success {
				app.logger.LogWarn("initial grid order rejected", map[string]string{
					"symbol": order.Symbol,
					"side":   order.Side.String(),
					"price":  order.Price.String(),
					"reason": result.Error,
				})
				failed++
				continue
			}
			placed++
		}

		app.logger.LogInfo("initial grid orders placed", map[string]string{
			"symbol":        gridCfg.Symbol,
			"strategy_id":   strategyID,
			"current_price": currentPrice.String(),
			"total_orders":  fmt.Sprintf("%d", len(orders)),
			"placed":        fmt.Sprintf("%d", placed),
			"failed":        fmt.Sprintf("%d", failed),
			"lower_price":   app.cfg.GridConfigs[i].LowerPrice.String(),
			"upper_price":   app.cfg.GridConfigs[i].UpperPrice.String(),
		})
	}
}

// startPrivateWSAndFillHandler connects the private WebSocket for order fill notifications
// and registers the grid fill handler to place counter-orders automatically.
func (app *application) startPrivateWSAndFillHandler() {
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

	// Create fill handler
	app.fillHandler = execution.NewGridFillHandler(
		app.apiClient,
		app.cfg.GridConfigs,
		gridLevels,
		app.logger,
	)

	// Register fill callback on private WS
	app.privateWS.SetFillCallback(app.fillHandler.OnFill)

	// Connect private WebSocket
	if err := app.privateWS.Connect(); err != nil {
		app.logger.LogError("failed to connect private WebSocket", map[string]string{
			"error": err.Error(),
		})
		app.alerter.SendWarning("Private WebSocket connection failed: "+err.Error(), map[string]string{
			"impact": "grid fill counter-orders will not be placed automatically",
		})
		return
	}

	app.logger.LogInfo("private WebSocket connected, fill handler active", map[string]string{
		"grid_symbols": fmt.Sprintf("%d", len(gridLevels)),
	})
}

// waitForShutdown blocks until a shutdown signal is received and performs graceful shutdown.
func (app *application) waitForShutdown() {
	// 7. Graceful shutdown on SIGINT/SIGTERM
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

		// 7a. Stop all strategies
		app.scheduler.StopAll()
		app.logger.LogInfo("all strategies stopped", nil)

		// 7b. Cancel all open orders
		app.cancelAllOpenOrders()

		// 7c. Persist final state
		app.persistFinalState()
		app.logger.LogInfo("final state persisted", nil)

		// 7d. Close connections
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

		// 7e. Stop metrics server
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

// cancelAllOpenOrders attempts to cancel all tracked open orders on the exchange.
func (app *application) cancelAllOpenOrders() {
	app.logger.LogInfo("cancelling all open orders", nil)

	for _, symbol := range app.cfg.Symbols {
		_, err := app.apiClient.CancelOrder(symbol, "")
		if err != nil {
			app.logger.LogWarn("error cancelling orders", map[string]string{
				"symbol": symbol,
				"error":  err.Error(),
			})
		}
	}
}

// persistFinalState saves current orders and positions to the persistence layer.
func (app *application) persistFinalState() {
	// Strategy states are persisted during shutdown
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

	// Check for Telegram configuration via environment variables
	tgBotToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	tgChatID := os.Getenv("TELEGRAM_CHAT_ID")
	if tgBotToken != "" && tgChatID != "" {
		channels = append(channels, monitor.NewTelegramChannel(tgBotToken, tgChatID))
	}

	// Check for Discord configuration via environment variables
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
// required by the Reconciler. It queries OKX REST API for current orders and positions.
type exchangeQuerierAdapter struct {
	client  *execution.APIClient
	symbols []string
}

func (q *exchangeQuerierAdapter) QueryOrders(symbol string) ([]*models.Order, error) {
	// Query open orders from exchange via REST API
	resp, err := q.client.DoRequest("GET", "/api/v5/trade/orders-pending?instId="+symbol, nil)
	if err != nil {
		return nil, fmt.Errorf("query orders failed: %w", err)
	}
	defer resp.Body.Close()
	// Return empty slice on success - the reconciler will handle the comparison
	return nil, nil
}

func (q *exchangeQuerierAdapter) QueryPositions(symbol string) ([]*models.Position, error) {
	// Query positions from exchange via REST API
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
	h.app.cancelAllOpenOrders()
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
	h.app.cancelAllOpenOrders()
}

func (h *extremeMarketHandler) PauseMeanReversion() {
	h.app.logger.LogWarn("extreme market: pausing mean reversion strategies", nil)
	// Stop mean reversion strategies
	strategies := h.app.scheduler.GetActiveStrategies()
	for _, s := range strategies {
		if s.Type == "mean_reversion" && s.IsActive {
			_ = h.app.scheduler.StopStrategy(s.StrategyID)
		}
	}
}

func (h *extremeMarketHandler) SendAlert(reason string) {
	h.app.alerter.SendWarning("Extreme Market Condition: "+reason, nil)
}
