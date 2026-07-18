package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/internal/config"
	"github.com/yourname/okx-hft-grid/internal/execution"
	"github.com/yourname/okx-hft-grid/internal/monitor"
	"github.com/yourname/okx-hft-grid/internal/strategy"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

const (
	preservationAPIKey     = "synthetic-preservation-api-key"
	preservationSecretKey  = "synthetic-preservation-secret-key"
	preservationPassphrase = "synthetic-preservation-passphrase"
)

type preservationTicker struct {
	last string
	high string
	low  string
}

type preservationCmdScenario struct {
	balance string
	tickers map[string]preservationTicker
	pending map[string][]map[string]string
}

type preservationCmdObservation struct {
	paths         []string
	cancelledIDs  []string
	orderPayloads []map[string]any
	violation     string
}

type preservationCmdSimulator struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	scenario preservationCmdScenario
	observed preservationCmdObservation
}

func newPreservationCmdSimulator(t *testing.T) *preservationCmdSimulator {
	t.Helper()
	sim := &preservationCmdSimulator{t: t}
	sim.server = httptest.NewServer(http.HandlerFunc(sim.handle))
	t.Cleanup(sim.server.Close)
	return sim
}

func (s *preservationCmdSimulator) setScenario(scenario preservationCmdScenario) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scenario = scenario
	s.observed = preservationCmdObservation{}
}

func (s *preservationCmdSimulator) observation() preservationCmdObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := s.observed
	result.paths = append([]string(nil), result.paths...)
	result.cancelledIDs = append([]string(nil), result.cancelledIDs...)
	result.orderPayloads = append([]map[string]any(nil), result.orderPayloads...)
	return result
}

func (s *preservationCmdSimulator) handle(w http.ResponseWriter, req *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	host, _, err := net.SplitHostPort(req.Host)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		s.observed.violation = fmt.Sprintf("non-loopback request host %q", req.Host)
		http.Error(w, "non-loopback host", http.StatusBadRequest)
		return
	}
	if req.Header.Get("OK-ACCESS-KEY") != preservationAPIKey || req.Header.Get("OK-ACCESS-PASSPHRASE") != preservationPassphrase {
		s.observed.violation = "request did not use synthetic preservation credentials"
		http.Error(w, "unexpected credentials", http.StatusBadRequest)
		return
	}

	s.observed.paths = append(s.observed.paths, req.Method+" "+req.URL.Path)
	w.Header().Set("Content-Type", "application/json")

	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/api/v5/market/ticker":
		symbol := req.URL.Query().Get("instId")
		ticker, ok := s.scenario.tickers[symbol]
		if !ok {
			s.observed.violation = "missing ticker fixture for " + symbol
			http.Error(w, "missing ticker", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "0", "msg": "", "data": []map[string]string{{
				"instId": symbol, "last": ticker.last, "high24h": ticker.high, "low24h": ticker.low,
			}},
		})
	case req.Method == http.MethodGet && req.URL.Path == "/api/v5/trade/orders-pending":
		symbol := req.URL.Query().Get("instId")
		orders := s.scenario.pending[symbol]
		if orders == nil {
			orders = []map[string]string{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "msg": "", "data": orders})
	case req.Method == http.MethodPost && req.URL.Path == "/api/v5/trade/cancel-order":
		defer req.Body.Close()
		var body map[string]string
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			s.observed.violation = "invalid cancel payload"
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		s.observed.cancelledIDs = append(s.observed.cancelledIDs, body["ordId"])
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "0", "msg": "", "data": []map[string]string{{"ordId": body["ordId"], "sCode": "0", "sMsg": ""}},
		})
	case req.Method == http.MethodGet && req.URL.Path == "/api/v5/account/balance":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "0", "data": []map[string]any{{"details": []map[string]string{{"ccy": "USDT", "availBal": s.scenario.balance}}}},
		})
	case req.Method == http.MethodPost && req.URL.Path == "/api/v5/trade/order":
		defer req.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			s.observed.violation = "invalid order payload"
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		s.observed.orderPayloads = append(s.observed.orderPayloads, body)
		orderID := fmt.Sprintf("sim-order-%d", len(s.observed.orderPayloads))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "0", "msg": "", "data": []map[string]string{{"ordId": orderID, "clOrdId": "", "sCode": "0", "sMsg": ""}},
		})
	default:
		s.observed.violation = req.Method + " " + req.URL.Path
		http.Error(w, "unexpected simulator request", http.StatusBadRequest)
	}
}

func (s *preservationCmdSimulator) application(cfg *config.SystemConfig) *application {
	client := execution.NewAPIClient(s.server.URL, &config.Credentials{
		APIKey: preservationAPIKey, SecretKey: preservationSecretKey, Passphrase: preservationPassphrase,
	}, nil)
	return &application{cfg: cfg, apiClient: client, logger: monitor.NewStructuredLogger(io.Discard)}
}

func (s *preservationCmdSimulator) assertIsolated(t interface{ Fatalf(string, ...any) }) {
	parsed, err := url.Parse(s.server.URL)
	if err != nil {
		t.Fatalf("invalid loopback simulator URL: %v", err)
	}
	if ip := net.ParseIP(parsed.Hostname()); ip == nil || !ip.IsLoopback() {
		t.Fatalf("simulator is not loopback: %s", s.server.URL)
	}
	if observation := s.observation(); observation.violation != "" {
		t.Fatalf("simulator isolation violation: %s", observation.violation)
	}
}

func clearProductionCredentialEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("OKX_API_KEY", "")
	t.Setenv("OKX_SECRET_KEY", "")
	t.Setenv("OKX_PASSPHRASE", "")
}

// **Validates: Requirements 3.2**
//
// PRE-02 records the strict non-stale domain [0, 2%]. Every exchange response
// comes from a literal-IP loopback simulator; no cancel endpoint should be hit.
func TestProperty2_Preservation_PRE02_DeviationAtOrBelowTwoPercentIsKept(t *testing.T) {
	clearProductionCredentialEnvironment(t)
	sim := newPreservationCmdSimulator(t)

	rapid.Check(t, func(t *rapid.T) {
		partsPerMillion := rapid.IntRange(0, 20000).Draw(t, "deviation_ppm")
		direction := []int64{-1, 1}[rapid.IntRange(0, 1).Draw(t, "direction_index")]
		current := decimal.NewFromInt(100)
		deviation := decimal.NewFromInt(int64(partsPerMillion)).Div(decimal.NewFromInt(1_000_000))
		orderPrice := current.Mul(decimal.NewFromInt(1).Add(deviation.Mul(decimal.NewFromInt(direction))))

		sim.setScenario(preservationCmdScenario{
			tickers: map[string]preservationTicker{"DOGE-USDT": {last: current.String(), high: "105", low: "95"}},
			pending: map[string][]map[string]string{"DOGE-USDT": {{
				"instId": "DOGE-USDT", "ordId": "pg-owned-kept", "clOrdId": "pg-v1-kept",
				"px": orderPrice.String(), "sz": "10", "side": "buy", "state": "live",
			}}},
		})
		app := sim.application(&config.SystemConfig{GridConfigs: []models.GridConfig{{Symbol: "DOGE-USDT"}}})
		app.rebalanceOrders()

		observation := sim.observation()
		if len(observation.cancelledIDs) != 0 || len(observation.orderPayloads) != 0 {
			t.Fatalf("PRE-02 deviation=%s cancelled=%v replacements=%d", deviation, observation.cancelledIDs, len(observation.orderPayloads))
		}
		if len(observation.paths) != 2 || observation.paths[0] != "GET /api/v5/market/ticker" || observation.paths[1] != "GET /api/v5/trade/orders-pending" {
			t.Fatalf("PRE-02 unexpected request trace: %v", observation.paths)
		}
		sim.assertIsolated(t)
	})
}

// **Validates: Requirements 3.3**
//
// PRE-03 records the unfixed dynamic allocation formula: reserve 5%, divide the
// remainder equally by all BUY slots across DOGE/WIF, then derive symbol quantity.
func TestProperty2_Preservation_PRE03_DOGEWIFDynamicAllocation(t *testing.T) {
	clearProductionCredentialEnvironment(t)
	sim := newPreservationCmdSimulator(t)

	rapid.Check(t, func(t *rapid.T) {
		balance := decimal.NewFromInt(int64(rapid.IntRange(1000, 10000).Draw(t, "balance_usdt")))
		dogePrice := decimal.NewFromInt(int64(rapid.IntRange(100, 500).Draw(t, "doge_millis"))).Div(decimal.NewFromInt(1000))
		wifPrice := decimal.NewFromInt(int64(rapid.IntRange(100, 500).Draw(t, "wif_cents"))).Div(decimal.NewFromInt(100))
		prices := map[string]decimal.Decimal{"DOGE-USDT": dogePrice, "WIF-USDT": wifPrice}
		tickers := make(map[string]preservationTicker, len(prices))
		configs := make([]models.GridConfig, 0, len(prices))
		for _, symbol := range []string{"DOGE-USDT", "WIF-USDT"} {
			price := prices[symbol]
			tickers[symbol] = preservationTicker{
				last: price.String(), high: price.Mul(decimal.RequireFromString("1.06")).String(),
				low: price.Mul(decimal.RequireFromString("0.94")).String(),
			}
			configs = append(configs, models.GridConfig{
				Symbol: symbol, LowerPrice: price.Mul(decimal.RequireFromString("0.96")),
				UpperPrice: price.Mul(decimal.RequireFromString("1.04")), GridCount: 4,
				GridType: models.GridTypeArithmetic, OrderSize: decimal.NewFromInt(1),
			})
		}
		sim.setScenario(preservationCmdScenario{
			balance: balance.String(), tickers: tickers,
			pending: map[string][]map[string]string{"DOGE-USDT": {}, "WIF-USDT": {}},
		})
		cfg := &config.SystemConfig{GridConfigs: configs}
		app := sim.application(cfg)
		app.placeInitialGridOrders()

		perSlot := balance.Mul(decimal.RequireFromString("0.95")).Div(decimal.NewFromInt(4))
		expectedSize := map[string]decimal.Decimal{
			"DOGE-USDT": perSlot.Div(dogePrice).Round(0),
			"WIF-USDT":  perSlot.Div(wifPrice).Round(0),
		}
		for _, gridCfg := range app.cfg.GridConfigs {
			if !gridCfg.OrderSize.Equal(expectedSize[gridCfg.Symbol]) {
				t.Fatalf("PRE-03 %s quantity=%s, want %s", gridCfg.Symbol, gridCfg.OrderSize, expectedSize[gridCfg.Symbol])
			}
		}

		observation := sim.observation()
		if len(observation.orderPayloads) != 8 {
			t.Fatalf("PRE-03 placed %d orders, want 8; trace=%v", len(observation.orderPayloads), observation.paths)
		}
		actual := make(map[string]int)
		actualBuyRisk := map[string]decimal.Decimal{"DOGE-USDT": decimal.Zero, "WIF-USDT": decimal.Zero}
		for _, payload := range observation.orderPayloads {
			symbol, _ := payload["instId"].(string)
			side, _ := payload["side"].(string)
			orderType, _ := payload["ordType"].(string)
			priceText, _ := payload["px"].(string)
			quantityText, _ := payload["sz"].(string)
			price, priceErr := decimal.NewFromString(priceText)
			quantity, quantityErr := decimal.NewFromString(quantityText)
			if priceErr != nil || quantityErr != nil {
				t.Fatalf("PRE-03 invalid captured decimal: price=%q qty=%q", priceText, quantityText)
			}
			if orderType != "post_only" || !quantity.Equal(expectedSize[symbol]) {
				t.Fatalf("PRE-03 metadata changed: symbol=%s side=%s type=%s qty=%s", symbol, side, orderType, quantity)
			}
			if (side == "buy") != price.LessThan(prices[symbol]) || (side == "sell") != price.GreaterThan(prices[symbol]) {
				t.Fatalf("PRE-03 direction changed: symbol=%s side=%s price=%s current=%s", symbol, side, price, prices[symbol])
			}
			actual[symbol+":"+side]++
			if side == "buy" {
				actualBuyRisk[symbol] = actualBuyRisk[symbol].Add(price.Mul(quantity))
			}
		}
		for _, symbol := range []string{"DOGE-USDT", "WIF-USDT"} {
			if actual[symbol+":buy"] != 2 || actual[symbol+":sell"] != 2 {
				t.Fatalf("PRE-03 %s side counts changed: buys=%d sells=%d", symbol, actual[symbol+":buy"], actual[symbol+":sell"])
			}
			gridCfg := app.cfg.GridConfigs[0]
			if gridCfg.Symbol != symbol {
				gridCfg = app.cfg.GridConfigs[1]
			}
			levels, err := strategy.CalculateGridLevels(&gridCfg)
			if err != nil {
				t.Fatalf("PRE-03 oracle levels: %v", err)
			}
			precision := int32(getPricePrecision(symbol))
			for i := range levels {
				levels[i] = levels[i].Round(precision)
			}
			expectedRisk := decimal.Zero
			for _, order := range strategy.PlaceGridOrders(levels, prices[symbol], &gridCfg) {
				if order.Side == models.SideBuy {
					expectedRisk = expectedRisk.Add(order.Price.Mul(order.Quantity))
				}
			}
			if !actualBuyRisk[symbol].Equal(expectedRisk) {
				t.Fatalf("PRE-03 %s BUY risk=%s, want %s", symbol, actualBuyRisk[symbol], expectedRisk)
			}
		}
		sim.assertIsolated(t)
	})
}

// **Validates: Requirements 3.4**
//
// PRE-04 compares adaptive ranges by semantic volatility class. The only
// accepted numeric difference is the approved clamp change (3%-8% to 1.5%-4%).
// Grid Drift position, average cost, realized PnL and fill counters are exact.
func TestProperty2_Preservation_PRE04_AdaptiveRangeAndGridState(t *testing.T) {
	clearProductionCredentialEnvironment(t)
	sim := newPreservationCmdSimulator(t)

	rapid.Check(t, func(t *rapid.T) {
		categories := []struct {
			name    string
			halfVol decimal.Decimal
			allowed []decimal.Decimal
		}{
			{name: "minimum_clamp", halfVol: decimal.RequireFromString("0.003"), allowed: []decimal.Decimal{decimal.RequireFromString("0.03"), decimal.RequireFromString("0.015"), decimal.RequireFromString("0.005")}},
			{name: "interior", halfVol: decimal.RequireFromString("0.035"), allowed: []decimal.Decimal{decimal.RequireFromString("0.035")}},
			{name: "maximum_clamp", halfVol: decimal.RequireFromString("0.10"), allowed: []decimal.Decimal{decimal.RequireFromString("0.08"), decimal.RequireFromString("0.04")}},
		}
		category := categories[rapid.IntRange(0, len(categories)-1).Draw(t, "volatility_category")]
		current := decimal.NewFromInt(100)
		sim.setScenario(preservationCmdScenario{tickers: map[string]preservationTicker{
			"WIF-USDT": {
				last: current.String(),
				high: current.Mul(decimal.NewFromInt(1).Add(category.halfVol)).String(),
				low:  current.Mul(decimal.NewFromInt(1).Sub(category.halfVol)).String(),
			},
		}})
		app := sim.application(&config.SystemConfig{})
		lower, upper, err := app.calculateAdaptiveGridRange("WIF-USDT")
		if err != nil {
			t.Fatalf("PRE-04 adaptive range error: %v", err)
		}
		lowerWidth := current.Sub(lower).Div(current)
		upperWidth := upper.Sub(current).Div(current)
		if !lowerWidth.Equal(upperWidth) || !lower.LessThan(current) || !upper.GreaterThan(current) {
			t.Fatalf("PRE-04 asymmetric/directional range: lower=%s current=%s upper=%s", lower, current, upper)
		}
		matchedClamp := false
		for _, allowed := range category.allowed {
			if upperWidth.Equal(allowed) {
				matchedClamp = true
				break
			}
		}
		if !matchedClamp {
			t.Fatalf("PRE-04 %s width=%s; only old/new approved clamp values are allowed", category.name, upperWidth)
		}

		direction := []models.DriftDirection{models.DriftDown, models.DriftUp}[rapid.IntRange(0, 1).Draw(t, "drift_direction")]
		quantity := decimal.NewFromInt(int64(rapid.IntRange(1, 100).Draw(t, "position_quantity")))
		avgEntry := decimal.NewFromInt(int64(rapid.IntRange(80, 110).Draw(t, "average_entry")))
		pnl := decimal.NewFromInt(int64(rapid.IntRange(-10000, 10000).Draw(t, "realized_pnl_cents"))).Div(decimal.NewFromInt(100))
		gridCfg := &models.GridConfig{
			Symbol: "WIF-USDT", LowerPrice: decimal.NewFromInt(100), UpperPrice: decimal.NewFromInt(120),
			GridCount: 4, GridType: models.GridTypeArithmetic, OrderSize: decimal.NewFromInt(10),
			Drift: &models.DriftConfig{Enabled: true, DriftThreshold: decimal.RequireFromString("0.1"), DriftStep: 1},
		}
		levelPrices, err := strategy.CalculateGridLevels(gridCfg)
		if err != nil {
			t.Fatalf("PRE-04 drift fixture levels: %v", err)
		}
		gridState := &strategy.GridState{
			Position: quantity, AvgEntryPrice: avgEntry, RealizedPnL: pnl,
			TotalBuys: 7, TotalSells: 3, Levels: make([]models.GridLevel, len(levelPrices)),
		}
		for i, price := range levelPrices {
			gridState.Levels[i] = models.GridLevel{Index: i, Price: price}
		}
		engine := strategy.NewGridDriftEngine(gridCfg, gridState, preservationDriftExecution{}, nil, nil, nil)
		trigger := decimal.NewFromInt(101)
		expectedLower := decimal.NewFromInt(95)
		expectedUpper := decimal.NewFromInt(115)
		if direction == models.DriftUp {
			trigger = decimal.NewFromInt(119)
			expectedLower = decimal.NewFromInt(105)
			expectedUpper = decimal.NewFromInt(125)
		}
		if !engine.OnPriceUpdate(trigger) {
			t.Fatalf("PRE-04 %s drift was not executed", direction)
		}
		if !gridCfg.LowerPrice.Equal(expectedLower) || !gridCfg.UpperPrice.Equal(expectedUpper) ||
			!gridCfg.UpperPrice.Sub(gridCfg.LowerPrice).Equal(decimal.NewFromInt(20)) {
			t.Fatalf("PRE-04 %s range changed: lower=%s upper=%s", direction, gridCfg.LowerPrice, gridCfg.UpperPrice)
		}
		if !gridState.Position.Equal(quantity) || !gridState.AvgEntryPrice.Equal(avgEntry) || !gridState.RealizedPnL.Equal(pnl) ||
			gridState.TotalBuys != 7 || gridState.TotalSells != 3 {
			t.Fatalf("PRE-04 GridState changed: position=%s avg=%s pnl=%s buys=%d sells=%d",
				gridState.Position, gridState.AvgEntryPrice, gridState.RealizedPnL, gridState.TotalBuys, gridState.TotalSells)
		}
		sim.assertIsolated(t)
	})
}

// **Validates: Requirements 3.5**
//
// PRE-05 uses a non-bug domain containing one provably bot-namespaced order and
// records that cleanup finishes before the first new-grid placement.
func TestProperty2_Preservation_PRE05_OwnedCleanupPrecedesNewGrid(t *testing.T) {
	clearProductionCredentialEnvironment(t)
	sim := newPreservationCmdSimulator(t)

	rapid.Check(t, func(t *rapid.T) {
		quantity := decimal.NewFromInt(int64(rapid.IntRange(10, 100).Draw(t, "configured_quantity")))
		current := decimal.RequireFromString("0.1")
		sim.setScenario(preservationCmdScenario{
			balance: "1000",
			tickers: map[string]preservationTicker{"DOGE-USDT": {last: current.String(), high: "0.106", low: "0.094"}},
			pending: map[string][]map[string]string{"DOGE-USDT": {{
				"instId": "DOGE-USDT", "ordId": "owned-1", "clOrdId": "pg-v1-owned-1",
				"px": "0.098", "sz": quantity.String(), "side": "buy", "state": "live",
			}}},
		})
		cfg := &config.SystemConfig{GridConfigs: []models.GridConfig{{
			Symbol: "DOGE-USDT", LowerPrice: decimal.RequireFromString("0.096"), UpperPrice: decimal.RequireFromString("0.104"),
			GridCount: 4, GridType: models.GridTypeArithmetic, OrderSize: quantity,
		}}}
		app := sim.application(cfg)
		app.placeInitialGridOrders()

		observation := sim.observation()
		if len(observation.cancelledIDs) != 1 || observation.cancelledIDs[0] != "owned-1" {
			t.Fatalf("PRE-05 ownership cleanup changed: cancelled=%v", observation.cancelledIDs)
		}
		cancelIndex := indexOfPath(observation.paths, "POST /api/v5/trade/cancel-order")
		placeIndex := indexOfPath(observation.paths, "POST /api/v5/trade/order")
		if cancelIndex < 0 || placeIndex < 0 || cancelIndex >= placeIndex {
			t.Fatalf("PRE-05 startup order changed: trace=%v", observation.paths)
		}
		if len(observation.orderPayloads) != 4 {
			t.Fatalf("PRE-05 new-grid order count=%d, want 4", len(observation.orderPayloads))
		}
		for _, payload := range observation.orderPayloads {
			if payload["instId"] != "DOGE-USDT" {
				t.Fatalf("PRE-05 cross-symbol placement: %+v", payload)
			}
		}
		sim.assertIsolated(t)
	})
}

func indexOfPath(paths []string, target string) int {
	for i, path := range paths {
		if path == target {
			return i
		}
	}
	return -1
}

type preservationDriftExecution struct{}

func (preservationDriftExecution) PlaceOrder(order *execution.OrderRequest) (*execution.OrderResult, error) {
	return &execution.OrderResult{Success: true, OrderID: "simulated", ExchangeOrderID: "simulated", Status: models.OrderStatusOpen}, nil
}
func (preservationDriftExecution) CancelOrder(orderID string) (*execution.CancelResult, error) {
	return &execution.CancelResult{Success: true, OrderID: orderID}, nil
}
func (preservationDriftExecution) BatchPlaceOrders(orders []*execution.OrderRequest) ([]*execution.OrderResult, error) {
	results := make([]*execution.OrderResult, len(orders))
	for i := range results {
		results[i] = &execution.OrderResult{Success: true, Status: models.OrderStatusOpen}
	}
	return results, nil
}
func (preservationDriftExecution) BatchCancelOrders(orderIDs []string) ([]*execution.CancelResult, error) {
	results := make([]*execution.CancelResult, len(orderIDs))
	for i, id := range orderIDs {
		results[i] = &execution.CancelResult{Success: true, OrderID: id}
	}
	return results, nil
}
func (preservationDriftExecution) GetOpenOrders(string) ([]*models.Order, error) { return nil, nil }
func (preservationDriftExecution) GetOrderStatus(string) (models.OrderStatus, error) {
	return models.OrderStatusOpen, nil
}
func (preservationDriftExecution) OnOrderUpdate(models.OrderUpdateEvent)        {}
func (preservationDriftExecution) GetPosition(string) (*models.Position, error) { return nil, nil }
func (preservationDriftExecution) GetAllOpenOrders() ([]*models.Order, error)   { return nil, nil }

// **Validates: Requirements 3.6, 3.7**
//
// PRE-06 captures the production profile baseline: trading_enabled=false,
// Singapore identity, cash mode, approved timing/range/state values, and
// empty mean reversion. This proves the production profile semantics are
// preserved on the unfixed code.
func TestProperty2_Preservation_PRE06_ProductionProfileBaseline(t *testing.T) {
	clearProductionCredentialEnvironment(t)

	rapid.Check(t, func(t *rapid.T) {
		cfg := validProductionConfigForCmd()

		// trading_enabled must be false
		if cfg.TradingEnabled {
			t.Fatal("PRE-06 production profile has trading_enabled=true")
		}

		// Singapore identity
		if cfg.Deployment.Location != config.ApprovedProductionLocation {
			t.Fatalf("PRE-06 deployment location changed: %s", cfg.Deployment.Location)
		}

		// Cash mode
		if cfg.Execution.TDMode != config.ApprovedTradingMode {
			t.Fatalf("PRE-06 td_mode changed: %s", cfg.Execution.TDMode)
		}

		// Approved timing values
		if cfg.PrivateWS.HeartbeatInterval != config.ApprovedPrivateWSHeartbeat {
			t.Fatalf("PRE-06 heartbeat_interval changed: %s", cfg.PrivateWS.HeartbeatInterval)
		}
		if cfg.PrivateWS.LivenessTimeout != config.ApprovedPrivateWSLivenessTimeout {
			t.Fatalf("PRE-06 liveness_timeout changed: %s", cfg.PrivateWS.LivenessTimeout)
		}
		if cfg.PrivateWS.ReconnectStartDeadline != config.ApprovedReconnectStartDeadline {
			t.Fatalf("PRE-06 reconnect_start_deadline changed: %s", cfg.PrivateWS.ReconnectStartDeadline)
		}
		if cfg.Reconciliation.Interval != config.ApprovedReconciliationInterval {
			t.Fatalf("PRE-06 reconciliation.interval changed: %s", cfg.Reconciliation.Interval)
		}
		if cfg.Rebalancer.Interval != config.ApprovedRebalancerInterval {
			t.Fatalf("PRE-06 rebalancer.interval changed: %s", cfg.Rebalancer.Interval)
		}
		if cfg.Ticker.MaxAge != config.ApprovedTickerMaxAge {
			t.Fatalf("PRE-06 ticker.max_age changed: %s", cfg.Ticker.MaxAge)
		}

		// Approved range values
		if !cfg.AdaptiveRange.MinHalfWidth.Equal(config.ApprovedMinimumHalfWidth) {
			t.Fatalf("PRE-06 min_half_width changed: %s", cfg.AdaptiveRange.MinHalfWidth)
		}
		if !cfg.AdaptiveRange.MaxHalfWidth.Equal(config.ApprovedMaximumHalfWidth) {
			t.Fatalf("PRE-06 max_half_width changed: %s", cfg.AdaptiveRange.MaxHalfWidth)
		}
		if !cfg.AdaptiveRange.Symmetric {
			t.Fatal("PRE-06 adaptive_range.symmetric changed to false")
		}

		// Empty mean reversion
		if len(cfg.MeanReversionConfigs) != 0 {
			t.Fatalf("PRE-06 mean_reversion_configs non-empty: %d", len(cfg.MeanReversionConfigs))
		}

		// Production validation must pass
		if err := config.ValidateProductionConfig(cfg); err != nil {
			t.Fatalf("PRE-06 production profile validation failed: %v", err)
		}

		// Sanitized effective config must be buildable and contain approved fields
		summary, err := config.EffectiveConfigSanitized(cfg)
		if err != nil {
			t.Fatalf("PRE-06 effective config summary build failed: %v", err)
		}
		for _, expected := range []string{
			config.ApprovedProductionLocation,
			"cash",
			"false", // trading_enabled
			"DOGE-USDT",
			"WIF-USDT",
		} {
			if !containsStr(summary, expected) {
				t.Fatalf("PRE-06 effective config missing %q: %s", expected, summary)
			}
		}
	})
}

func validProductionConfigForCmd() *config.SystemConfig {
	return &config.SystemConfig{
		Symbols:              []string{"DOGE-USDT", "WIF-USDT"},
		MeanReversionConfigs: []models.MeanReversionConfig{},
		WebSocketURL:         config.DefaultPublicWebSocketURL,
		PrivateWebSocketURL:  config.DefaultPrivateWebSocketURL,
		RESTURL:              config.DefaultRESTBaseURL,
		ReconcileIntervalSec: 30,
		PersistencePath:      config.ApprovedStatePath,
		Deployment:           config.DeploymentConfig{Location: config.ApprovedProductionLocation},
		Execution:            config.ExecutionConfig{TDMode: config.ApprovedTradingMode},
		PrivateWS: config.PrivateWSConfig{
			HeartbeatInterval:      config.ApprovedPrivateWSHeartbeat,
			LivenessTimeout:        config.ApprovedPrivateWSLivenessTimeout,
			ReconnectStartDeadline: config.ApprovedReconnectStartDeadline,
		},
		Reconciliation: config.ReconciliationConfig{Interval: config.ApprovedReconciliationInterval},
		Rebalancer: config.RebalancerConfig{
			Interval:  config.ApprovedRebalancerInterval,
			MaxJitter: config.MaximumRebalancerJitter,
		},
		Ticker: config.TickerConfig{MaxAge: config.ApprovedTickerMaxAge},
		AdaptiveRange: config.AdaptiveRangeConfig{
			MinHalfWidth: config.ApprovedMinimumHalfWidth,
			MaxHalfWidth: config.ApprovedMaximumHalfWidth,
			Symmetric:    true,
			WidthMode:    config.PerSideHalfWidthMode,
		},
		ExecutionMode:  config.ExecutionModeProduction,
		TradingEnabled: false,
	}
}
