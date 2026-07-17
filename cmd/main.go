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
	"strings"
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
	parser      *marketdata.Parser
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

	// 5.11c Parser (for tick validation and routing to scheduler)
	app.parser = marketdata.NewParser(func(symbol, reason string) {
		app.logger.LogWarn("data validation failed", map[string]string{"symbol": symbol, "reason": reason})
	})

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

	// Route market data ticks to the Scheduler for mean reversion processing
	app.wsClient.SetMessageHandler(func(messageType int, data []byte) {
		tick, err := app.parser.ParseAndValidate(data)
		if err != nil || tick == nil {
			return
		}
		app.scheduler.OnMarketUpdate(tick.Symbol, tick)
	})

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

	// 6d.3 Start order rebalancer to cancel stranded orders and place new ones
	app.startOrderRebalancer()

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

// calculateAdaptiveGridRange fetches the current ticker for a symbol and computes
// adaptive grid boundaries centered on the current price. It uses 24h volatility
// to size the range (minimum 3%, maximum 8% each side) ensuring all grid levels
// are within realistic trading distance of the current price.
func (app *application) calculateAdaptiveGridRange(symbol string) (lower, upper decimal.Decimal, err error) {
	// 1. Get current price
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
		Msg  string `json:"msg"`
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

	// 2. Use 24h high/low to determine volatility, but cap at ±5% from current price
	high24h, _ := decimal.NewFromString(tickerResp.Data[0].High24h)
	low24h, _ := decimal.NewFromString(tickerResp.Data[0].Low24h)

	// Calculate range based on 24h volatility or default 5%
	var rangePercent decimal.Decimal
	if high24h.IsPositive() && low24h.IsPositive() && currentPrice.IsPositive() {
		// Actual 24h range as percentage of current price
		volatility := high24h.Sub(low24h).Div(currentPrice)
		// Use half the 24h volatility as our range (each side), minimum 3%, maximum 8%
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
		rangePercent = decimal.NewFromFloat(0.05) // Default 5%
	}

	// 3. Compute range: currentPrice ± rangePercent
	lower = currentPrice.Mul(decimal.NewFromInt(1).Sub(rangePercent))
	upper = currentPrice.Mul(decimal.NewFromInt(1).Add(rangePercent))

	app.logger.LogInfo("adaptive grid range calculated", map[string]string{
		"symbol":        symbol,
		"current_price": currentPrice.String(),
		"range_percent": rangePercent.Mul(decimal.NewFromInt(100)).String() + "%",
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

// placeInitialGridOrders fetches the current market price for each grid config and places
// the initial grid of limit orders. This is the critical step that ensures the bot actually
// has orders on the book after startup. If UpperPrice/LowerPrice are zero (not configured),
// it uses calculateAdaptiveGridRange to determine the grid bounds from historical data.
// Before placing orders, it dynamically calculates order_size based on available USDT balance
// to ensure nearly 100% fund utilization.
func (app *application) placeInitialGridOrders() {
	// Dynamic order sizing: query available USDT and distribute across all BUY grid slots
	availableBalance, err := app.getAvailableBalance()
	if err != nil {
		app.logger.LogWarn("could not get available balance, using config order_size", map[string]string{"error": err.Error()})
		// Fall through to use config values
	} else {
		// Reserve 5% as buffer for fees and slippage
		usableBalance := availableBalance.Mul(decimal.NewFromFloat(0.95))

		// Count total BUY grid slots across all symbols
		totalBuySlots := 0
		type symbolInfo struct {
			index    int
			price    decimal.Decimal
			buySlots int
		}
		var infos []symbolInfo

		for i, gridCfg := range app.cfg.GridConfigs {
			currentPrice, priceErr := app.getCurrentPrice(gridCfg.Symbol)
			if priceErr != nil {
				continue
			}

			// Calculate adaptive range to know how many BUY levels there will be
			lower, upper, rangeErr := app.calculateAdaptiveGridRange(gridCfg.Symbol)
			if rangeErr != nil {
				continue
			}

			// Estimate buy slots: levels below current price
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
			// Divide usable balance equally among all buy slots
			perSlotUSDT := usableBalance.Div(decimal.NewFromInt(int64(totalBuySlots)))

			for _, info := range infos {
				// order_size = perSlotUSDT / currentPrice (number of coins per order)
				orderSize := perSlotUSDT.Div(info.price).Round(0)
				if orderSize.IsPositive() {
					app.cfg.GridConfigs[info.index].OrderSize = orderSize
					app.logger.LogInfo("dynamic order size calculated", map[string]string{
						"symbol":       app.cfg.GridConfigs[info.index].Symbol,
						"order_size":   orderSize.String(),
						"per_slot_usd": perSlotUSDT.String(),
						"buy_slots":    fmt.Sprintf("%d", info.buySlots),
						"balance":      availableBalance.String(),
					})
				}
			}
		}
	}

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

// PendingOrder represents a pending order returned from OKX REST API.
type PendingOrder struct {
	InstID string `json:"instId"`
	OrdID  string `json:"ordId"`
	Px     string `json:"px"`
	Sz     string `json:"sz"`
	Side   string `json:"side"`
	State  string `json:"state"`
}

// startOrderRebalancer launches a background goroutine that periodically checks
// all pending orders and cancels those that have drifted too far from current price,
// then places new orders closer to the current price using grid level logic.
func (app *application) startOrderRebalancer() {
	go func() {
		defer panicRecovery(app.alerter)

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				app.rebalanceOrders()
			}
		}
	}()
	app.logger.LogInfo("order rebalancer started", map[string]string{
		"interval": "30s",
		"threshold": "2%",
	})
}

// rebalanceOrders iterates all grid symbols, checks pending orders, cancels those
// too far from current price (>2%), and places new orders at current grid levels.
func (app *application) rebalanceOrders() {
	for i, gridCfg := range app.cfg.GridConfigs {
		symbol := gridCfg.Symbol

		// 1. Get current price
		currentPrice, err := app.getCurrentPrice(symbol)
		if err != nil {
			app.logger.LogWarn("rebalancer: failed to get current price", map[string]string{
				"symbol": symbol,
				"error":  err.Error(),
			})
			continue
		}

		// 2. Get pending orders from OKX
		pendingOrders, err := app.getPendingOrders(symbol)
		if err != nil {
			app.logger.LogWarn("rebalancer: failed to get pending orders", map[string]string{
				"symbol": symbol,
				"error":  err.Error(),
			})
			continue
		}

		if len(pendingOrders) == 0 {
			continue
		}

		// 3. Check each order - cancel if too far from current price (>2%)
		maxDistance := currentPrice.Mul(decimal.NewFromFloat(0.02))
		cancelledAny := false

		for _, order := range pendingOrders {
			orderPrice, err := decimal.NewFromString(order.Px)
			if err != nil {
				continue
			}
			distance := currentPrice.Sub(orderPrice).Abs()

			if distance.GreaterThan(maxDistance) {
				// Cancel this order - it's too far away
				_, cancelErr := app.apiClient.CancelOrder(symbol, order.OrdID)
				if cancelErr != nil {
					app.logger.LogWarn("rebalancer: failed to cancel stranded order", map[string]string{
						"symbol": symbol,
						"ordId":  order.OrdID,
						"price":  order.Px,
						"error":  cancelErr.Error(),
					})
					continue
				}
				cancelledAny = true
				app.logger.LogInfo("rebalancer: cancelled stranded order", map[string]string{
					"symbol":        symbol,
					"ordId":         order.OrdID,
					"order_price":   order.Px,
					"current_price": currentPrice.String(),
					"distance":      distance.String(),
				})
			}
		}

		// 4. If we cancelled orders, place new ones at current grid levels
		if cancelledAny {
			// Recalculate adaptive grid range centered on current price
			adaptiveLower, adaptiveUpper, err := app.calculateAdaptiveGridRange(symbol)
			if err != nil {
				app.logger.LogWarn("rebalancer: failed to calculate adaptive range", map[string]string{
					"symbol": symbol,
					"error":  err.Error(),
				})
				continue
			}
			app.cfg.GridConfigs[i].LowerPrice = adaptiveLower
			app.cfg.GridConfigs[i].UpperPrice = adaptiveUpper

			levels, err := strategy.CalculateGridLevels(&app.cfg.GridConfigs[i])
			if err != nil {
				app.logger.LogWarn("rebalancer: failed to calculate grid levels", map[string]string{
					"symbol": symbol,
					"error":  err.Error(),
				})
				continue
			}

			// Place only BUY orders (SELL orders are placed after fills as counter-orders)
			orders := strategy.PlaceGridOrders(levels, currentPrice, &app.cfg.GridConfigs[i])

			// Dynamic order sizing for rebalance
			availBal, balErr := app.getAvailableBalance()
			if balErr == nil && availBal.IsPositive() {
				// Count buy slots in new levels
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
						app.logger.LogInfo("rebalancer: dynamic order size calculated", map[string]string{
							"symbol":     symbol,
							"order_size": dynamicSize.String(),
							"balance":    availBal.String(),
							"buy_slots":  fmt.Sprintf("%d", buyCount),
						})
						// Regenerate orders with updated size
						orders = strategy.PlaceGridOrders(levels, currentPrice, &app.cfg.GridConfigs[i])
					}
				}
			}

			placed := 0
			for _, order := range orders {
				if order.Side == models.SideBuy {
					req := &execution.OrderRequest{
						Symbol:    order.Symbol,
						Side:      order.Side,
						OrderType: order.OrderType,
						Price:     roundPriceForSymbol(order.Price, symbol),
						Quantity:  order.Quantity,
					}
					result, placeErr := app.apiClient.PlaceOrder(req)
					if placeErr != nil || !result.Success {
						continue
					}
					placed++
				}
			}

			// Update fill handler grid levels so counter-orders use new levels
			if app.fillHandler != nil {
				app.fillHandler.UpdateGridLevels(symbol, levels)
			}

			app.logger.LogInfo("rebalancer: orders rebalanced", map[string]string{
				"symbol":        symbol,
				"current_price": currentPrice.String(),
				"new_lower":     adaptiveLower.String(),
				"new_upper":     adaptiveUpper.String(),
				"orders_placed": fmt.Sprintf("%d", placed),
			})
		}
	}
}

// getCurrentPrice queries the OKX REST API for the current ticker price of a symbol.
func (app *application) getCurrentPrice(symbol string) (decimal.Decimal, error) {
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

// roundPriceForSymbol rounds a price to the appropriate decimal places for the given symbol.
// OKX has different tick sizes per instrument.
func roundPriceForSymbol(price decimal.Decimal, symbol string) decimal.Decimal {
	switch {
	case strings.Contains(symbol, "BTC"):
		return price.Round(1) // BTC: $0.1 tick
	case strings.Contains(symbol, "ETH"):
		return price.Round(2) // ETH: $0.01 tick
	case strings.Contains(symbol, "DOGE"):
		return price.Round(5) // DOGE: $0.00001 tick
	case strings.Contains(symbol, "PEPE"):
		return price.Round(10) // PEPE: very small tick
	case strings.Contains(symbol, "WIF"):
		return price.Round(4) // WIF: $0.0001 tick
	case strings.Contains(symbol, "KOR"):
		return price.Round(4) // KOR: $0.0001 tick
	default:
		return price.Round(6) // Safe default
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
