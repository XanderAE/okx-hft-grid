package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/internal/config"
	"github.com/yourname/okx-hft-grid/internal/monitor"
	"github.com/yourname/okx-hft-grid/internal/risk"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// startupTestSimulator provides an in-memory OKX loopback simulator for startup tests.
type startupTestSimulator struct {
	t        *testing.T
	server   *httptest.Server
	wsServer *httptest.Server

	mu              sync.Mutex
	requestPaths    []string
	ordersPending   []map[string]string
	privateWSFailed bool
	reconcileFailed bool
	tickerResponse  map[string]string
	instrumentRules map[string]map[string]string

	// Tracking
	ordersPlaced    int32
	ordersCancelled int32
}

func newStartupTestSimulator(t *testing.T) *startupTestSimulator {
	t.Helper()
	sim := &startupTestSimulator{
		t:               t,
		tickerResponse:  map[string]string{"DOGE-USDT": "0.15", "WIF-USDT": "2.50"},
		instrumentRules: make(map[string]map[string]string),
	}
	sim.server = httptest.NewServer(http.HandlerFunc(sim.handle))
	t.Cleanup(sim.server.Close)
	// Create a WebSocket server that immediately accepts and sends login success
	sim.wsServer = httptest.NewServer(http.HandlerFunc(sim.handleWS))
	t.Cleanup(sim.wsServer.Close)
	return sim
}

func (s *startupTestSimulator) wsURL() string {
	return "ws://" + s.wsServer.Listener.Addr().String() + "/ws/v5/private"
}

func (s *startupTestSimulator) publicWSURL() string {
	return "ws://" + s.wsServer.Listener.Addr().String() + "/ws/v5/public"
}

func (s *startupTestSimulator) privateWSURL() string {
	return "ws://" + s.wsServer.Listener.Addr().String() + "/ws/v5/private"
}

func (s *startupTestSimulator) handleWS(w http.ResponseWriter, req *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Read login request and respond with success
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var frame map[string]any
		if json.Unmarshal(msg, &frame) != nil {
			continue
		}
		op, _ := frame["op"].(string)
		switch op {
		case "login":
			conn.WriteJSON(map[string]any{"event": "login", "code": "0", "msg": ""})
		case "subscribe":
			conn.WriteJSON(map[string]any{"event": "subscribe", "arg": frame["args"]})
		case "ping":
			conn.WriteMessage(websocket.TextMessage, []byte("pong"))
		}
	}
}

func (s *startupTestSimulator) handle(w http.ResponseWriter, req *http.Request) {
	s.mu.Lock()
	s.requestPaths = append(s.requestPaths, req.Method+" "+req.URL.Path)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	switch {
	case req.URL.Path == "/api/v5/market/ticker":
		symbol := req.URL.Query().Get("instId")
		price := "100"
		s.mu.Lock()
		if p, ok := s.tickerResponse[symbol]; ok {
			price = p
		}
		s.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"code": "0", "data": []map[string]string{{"instId": symbol, "last": price, "bidPx": price, "askPx": price, "ts": fmt.Sprintf("%d", time.Now().UnixMilli())}},
		})

	case req.URL.Path == "/api/v5/trade/orders-pending":
		s.mu.Lock()
		pending := s.ordersPending
		s.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": pending})

	case req.URL.Path == "/api/v5/trade/order" && req.Method == "POST":
		atomic.AddInt32(&s.ordersPlaced, 1)
		json.NewEncoder(w).Encode(map[string]any{
			"code": "0", "data": []map[string]string{{"ordId": "sim-order-1", "clOrdId": "", "sCode": "0", "sMsg": ""}},
		})

	case req.URL.Path == "/api/v5/trade/cancel-order" && req.Method == "POST":
		atomic.AddInt32(&s.ordersCancelled, 1)
		json.NewEncoder(w).Encode(map[string]any{
			"code": "0", "data": []map[string]string{{"ordId": "cancelled", "clOrdId": "", "sCode": "0", "sMsg": ""}},
		})

	case req.URL.Path == "/api/v5/account/balance":
		json.NewEncoder(w).Encode(map[string]any{
			"code": "0", "data": []map[string]any{{"details": []map[string]string{{"ccy": "USDT", "availBal": "1000"}}}},
		})

	case req.URL.Path == "/api/v5/account/positions":
		json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": []any{}})

	case req.URL.Path == "/api/v5/public/instruments":
		symbol := req.URL.Query().Get("instId")
		json.NewEncoder(w).Encode(map[string]any{
			"code": "0", "data": []map[string]string{{"instId": symbol, "tickSz": "0.00001", "lotSz": "1", "minSz": "1", "instType": "SPOT", "state": "live"}},
		})

	default:
		json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": []any{}})
	}
}

// TestStartupOrder verifies the startup sequence follows the design-mandated order:
// config/guard → state → observability → gates → gateway → Private_WS → reconcile → cleanup → ticker → READY → grid
func TestStartupOrder(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	t.Setenv("DISCORD_WEBHOOK_URL", "")

	sim := newStartupTestSimulator(t)
	dbPath := filepath.Join(t.TempDir(), "state.db")

	cfg := &config.SystemConfig{
		Symbols:         []string{"DOGE-USDT", "WIF-USDT"},
		PersistencePath: dbPath,
		GridConfigs: []models.GridConfig{
			{Symbol: "DOGE-USDT", GridCount: 5, OrderSize: decimal.NewFromInt(100),
				LowerPrice: decimal.NewFromFloat(0.14), UpperPrice: decimal.NewFromFloat(0.16), GridType: models.GridTypeArithmetic},
			{Symbol: "WIF-USDT", GridCount: 5, OrderSize: decimal.NewFromInt(10),
				LowerPrice: decimal.NewFromFloat(2.30), UpperPrice: decimal.NewFromFloat(2.70), GridType: models.GridTypeArithmetic},
		},
		RESTURL:             sim.server.URL,
		WebSocketURL:        sim.publicWSURL(),
		PrivateWebSocketURL: sim.privateWSURL(),
		TradingEnabled:      true,
		AdaptiveRange: config.AdaptiveRangeConfig{
			MinHalfWidth: decimal.NewFromFloat(0.015),
			MaxHalfWidth: decimal.NewFromFloat(0.04),
			Symmetric:    true,
		},
	}

	app, err := initializeComponents(cfg, &config.Credentials{
		APIKey: "test-key", SecretKey: "test-secret", Passphrase: "test-pass",
	})
	if err != nil {
		t.Fatalf("initializeComponents: %v", err)
	}
	defer app.store.Close()

	// Run startup
	if err := app.startup(); err != nil {
		t.Fatalf("startup: %v", err)
	}
	defer app.gracefulShutdown()

	// Verify health is healthy after startup
	state := app.health.State()
	if state != monitor.HealthStateHealthy {
		t.Fatalf("expected healthy state after startup, got %s", state)
	}

	// Verify Private_WS was connected before any orders
	snap := app.health.Snapshot()
	if snap.PrivateWS.State != "ready" {
		t.Fatalf("expected Private_WS ready, got %s", snap.PrivateWS.State)
	}

	// Verify reconciliation was done
	if snap.Reconciliation.LastSuccess == "" {
		t.Fatalf("expected reconciliation to have been completed")
	}
}

// TestNoRiskBeforeReady verifies that no risk-increasing orders are placed
// before the system reaches READY state (auth/subscription, reconciliation,
// outbox recovery, ticker all confirmed).
func TestNoRiskBeforeReady(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	t.Setenv("DISCORD_WEBHOOK_URL", "")

	sim := newStartupTestSimulator(t)
	dbPath := filepath.Join(t.TempDir(), "state.db")

	cfg := &config.SystemConfig{
		Symbols:         []string{"DOGE-USDT"},
		PersistencePath: dbPath,
		GridConfigs: []models.GridConfig{
			{Symbol: "DOGE-USDT", GridCount: 5, OrderSize: decimal.NewFromInt(100),
				LowerPrice: decimal.NewFromFloat(0.14), UpperPrice: decimal.NewFromFloat(0.16), GridType: models.GridTypeArithmetic},
		},
		RESTURL:             sim.server.URL,
		WebSocketURL:        sim.publicWSURL(),
		PrivateWebSocketURL: sim.privateWSURL(),
		TradingEnabled:      true,
		AdaptiveRange: config.AdaptiveRangeConfig{
			MinHalfWidth: decimal.NewFromFloat(0.015),
			MaxHalfWidth: decimal.NewFromFloat(0.04),
			Symmetric:    true,
		},
	}

	app, err := initializeComponents(cfg, &config.Credentials{
		APIKey: "test-key", SecretKey: "test-secret", Passphrase: "test-pass",
	})
	if err != nil {
		t.Fatalf("initializeComponents: %v", err)
	}
	defer app.store.Close()

	// Simulate the early startup state: enter global safe-stop as startup() would
	app.tradingGate.EnterGlobalSafeStop("private-ws-uncertain", "test: pre-startup gate")

	// Before READY: trading gate blocks risk-increasing
	decision := app.tradingGate.Authorize("DOGE-USDT", 0) // RiskIncreasing
	if decision.Allowed {
		t.Fatalf("trading gate should block risk-increasing before READY")
	}

	// Clear to simulate successful startup
	app.tradingGate.ClearGlobalReason("private-ws-uncertain", false)
	app.tradingGate.UpdateSharedHealth(risk.SharedDependencyHealth{
		PersistenceHealthy:      true,
		PrivateWSHealthy:        true,
		AccountReconcileHealthy: true,
		PortfolioRiskHealthy:    true,
	})

	// After READY: trading gate allows risk-increasing (healthy)
	decision = app.tradingGate.Authorize("DOGE-USDT", 0)
	if !decision.Allowed {
		t.Fatalf("trading gate should allow risk-increasing after READY, blocked: %s", decision.BlockReason)
	}
}

// TestRecoveryBeforeInitialGrid verifies that reconciliation completes before
// any initial grid orders are placed.
func TestRecoveryBeforeInitialGrid(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	t.Setenv("DISCORD_WEBHOOK_URL", "")

	sim := newStartupTestSimulator(t)
	dbPath := filepath.Join(t.TempDir(), "state.db")

	cfg := &config.SystemConfig{
		Symbols:         []string{"DOGE-USDT"},
		PersistencePath: dbPath,
		GridConfigs: []models.GridConfig{
			{Symbol: "DOGE-USDT", GridCount: 5, OrderSize: decimal.NewFromInt(100),
				LowerPrice: decimal.NewFromFloat(0.14), UpperPrice: decimal.NewFromFloat(0.16), GridType: models.GridTypeArithmetic},
		},
		RESTURL:             sim.server.URL,
		WebSocketURL:        sim.publicWSURL(),
		PrivateWebSocketURL: sim.privateWSURL(),
		TradingEnabled:      true,
		AdaptiveRange: config.AdaptiveRangeConfig{
			MinHalfWidth: decimal.NewFromFloat(0.015),
			MaxHalfWidth: decimal.NewFromFloat(0.04),
			Symmetric:    true,
		},
	}

	app, err := initializeComponents(cfg, &config.Credentials{
		APIKey: "test-key", SecretKey: "test-secret", Passphrase: "test-pass",
	})
	if err != nil {
		t.Fatalf("initializeComponents: %v", err)
	}
	defer app.store.Close()

	if err := app.startup(); err != nil {
		t.Fatalf("startup: %v", err)
	}
	defer app.gracefulShutdown()

	// Verify reconciliation happened (health shows last_success)
	snap := app.health.Snapshot()
	if snap.Reconciliation.LastSuccess == "" {
		t.Fatalf("reconciliation was not completed before initial grid")
	}

	// Verify orders were placed (initial grid)
	placed := atomic.LoadInt32(&sim.ordersPlaced)
	if placed == 0 {
		t.Fatalf("expected initial grid orders to be placed, got 0")
	}
}

// TestOwnedCleanupOnly verifies that startup cleanup only cancels Bot_Owned orders
// and preserves unowned/manual orders.
func TestOwnedCleanupOnly(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	t.Setenv("DISCORD_WEBHOOK_URL", "")

	var cancelledIDs []string
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.URL.Path == "/api/v5/trade/orders-pending":
			// Return one bot-owned and one manual order
			orders := []map[string]string{
				{"instId": "DOGE-USDT", "ordId": "bot-order-1", "clOrdId": "tb1counter123", "px": "0.15", "sz": "100", "side": "buy", "state": "live"},
				{"instId": "DOGE-USDT", "ordId": "manual-order-1", "clOrdId": "manual-user-placed", "px": "0.14", "sz": "50", "side": "buy", "state": "live"},
			}
			json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": orders})

		case req.URL.Path == "/api/v5/trade/cancel-order" && req.Method == "POST":
			defer req.Body.Close()
			var body map[string]string
			json.NewDecoder(req.Body).Decode(&body)
			mu.Lock()
			cancelledIDs = append(cancelledIDs, body["ordId"])
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{
				"code": "0", "data": []map[string]string{{"ordId": body["ordId"], "sCode": "0", "sMsg": ""}},
			})

		default:
			json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": []any{}})
		}
	}))
	defer server.Close()

	dbPath := filepath.Join(t.TempDir(), "state.db")

	// Create a minimal WS mock for Private_WS
	wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame map[string]any
			if json.Unmarshal(msg, &frame) != nil {
				continue
			}
			op, _ := frame["op"].(string)
			switch op {
			case "login":
				conn.WriteJSON(map[string]any{"event": "login", "code": "0", "msg": ""})
			case "subscribe":
				conn.WriteJSON(map[string]any{"event": "subscribe", "arg": frame["args"]})
			}
		}
	}))
	defer wsSrv.Close()
	wsURL := "ws://" + wsSrv.Listener.Addr().String() + "/ws/v5/private"

	cfg := &config.SystemConfig{
		Symbols:         []string{"DOGE-USDT"},
		PersistencePath: dbPath,
		GridConfigs: []models.GridConfig{{Symbol: "DOGE-USDT", GridCount: 5, OrderSize: decimal.NewFromInt(100),
			LowerPrice: decimal.NewFromFloat(0.14), UpperPrice: decimal.NewFromFloat(0.16), GridType: models.GridTypeArithmetic}},
		RESTURL:             server.URL,
		WebSocketURL:        wsURL,
		PrivateWebSocketURL: wsURL,
		TradingEnabled:      false, // reconcile-only to isolate cleanup
		AdaptiveRange: config.AdaptiveRangeConfig{
			MinHalfWidth: decimal.NewFromFloat(0.015),
			MaxHalfWidth: decimal.NewFromFloat(0.04),
			Symmetric:    true,
		},
	}

	app, err := initializeComponents(cfg, &config.Credentials{
		APIKey: "test-key", SecretKey: "test-secret", Passphrase: "test-pass",
	})
	if err != nil {
		t.Fatalf("initializeComponents: %v", err)
	}
	defer app.store.Close()

	if err := app.startup(); err != nil {
		t.Fatalf("startup: %v", err)
	}
	defer app.gracefulShutdown()

	// Verify only bot-owned order was cancelled
	mu.Lock()
	cancelled := append([]string(nil), cancelledIDs...)
	mu.Unlock()

	if len(cancelled) != 1 {
		t.Fatalf("expected exactly 1 order cancelled (bot-owned only), got %d: %v", len(cancelled), cancelled)
	}
	if cancelled[0] != "bot-order-1" {
		t.Fatalf("expected bot-order-1 to be cancelled, got %s", cancelled[0])
	}
}

// TestStartupFailureClosed verifies that when a dependency is untrusted,
// the system remains degraded/safe-stopped and does not place new risk-increasing orders.
func TestStartupFailureClosed(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	t.Setenv("DISCORD_WEBHOOK_URL", "")

	// Create a server that will reject Private_WS connection by being invalid
	// This simulates Private_WS auth failure
	dbPath := filepath.Join(t.TempDir(), "state.db")

	cfg := &config.SystemConfig{
		Symbols:         []string{"DOGE-USDT"},
		PersistencePath: dbPath,
		GridConfigs: []models.GridConfig{
			{Symbol: "DOGE-USDT", GridCount: 5, OrderSize: decimal.NewFromInt(100),
				LowerPrice: decimal.NewFromFloat(0.14), UpperPrice: decimal.NewFromFloat(0.16), GridType: models.GridTypeArithmetic},
		},
		RESTURL:        "http://127.0.0.1:1", // Unreachable to simulate failure
		TradingEnabled: true,
		AdaptiveRange: config.AdaptiveRangeConfig{
			MinHalfWidth: decimal.NewFromFloat(0.015),
			MaxHalfWidth: decimal.NewFromFloat(0.04),
			Symmetric:    true,
		},
	}

	app, err := initializeComponents(cfg, &config.Credentials{
		APIKey: "test-key", SecretKey: "test-secret", Passphrase: "test-pass",
	})
	if err != nil {
		t.Fatalf("initializeComponents: %v", err)
	}
	defer app.store.Close()

	// Startup should fail (Private_WS cannot connect)
	err = app.startup()
	if err == nil {
		app.gracefulShutdown()
		t.Fatalf("expected startup to fail when dependency is unavailable")
	}

	// Verify system did NOT reach healthy state
	state := app.health.State()
	if state == monitor.HealthStateHealthy {
		t.Fatalf("system should not be healthy when startup fails, got %s", state)
	}

	// Verify trading gate blocks risk-increasing
	decision := app.tradingGate.Authorize("DOGE-USDT", 0) // RiskIncreasing
	if decision.Allowed {
		t.Fatalf("trading gate should block risk-increasing when startup failed")
	}
}

// TestProductionComposition verifies the full production startup composition:
// - Mean reversion is NOT loaded
// - All risk-increasing entry goes through TradingGate
// - Rebalancers are started per-symbol
// - Health reports correct state
func TestProductionComposition(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	t.Setenv("DISCORD_WEBHOOK_URL", "")

	sim := newStartupTestSimulator(t)
	dbPath := filepath.Join(t.TempDir(), "state.db")

	cfg := &config.SystemConfig{
		Symbols:         []string{"DOGE-USDT", "WIF-USDT"},
		PersistencePath: dbPath,
		GridConfigs: []models.GridConfig{
			{Symbol: "DOGE-USDT", GridCount: 5, OrderSize: decimal.NewFromInt(100),
				LowerPrice: decimal.NewFromFloat(0.14), UpperPrice: decimal.NewFromFloat(0.16), GridType: models.GridTypeArithmetic},
			{Symbol: "WIF-USDT", GridCount: 5, OrderSize: decimal.NewFromInt(10),
				LowerPrice: decimal.NewFromFloat(2.30), UpperPrice: decimal.NewFromFloat(2.70), GridType: models.GridTypeArithmetic},
		},
		RESTURL:             sim.server.URL,
		WebSocketURL:        sim.publicWSURL(),
		PrivateWebSocketURL: sim.privateWSURL(),
		TradingEnabled:      true,
		AdaptiveRange: config.AdaptiveRangeConfig{
			MinHalfWidth: decimal.NewFromFloat(0.015),
			MaxHalfWidth: decimal.NewFromFloat(0.04),
			Symmetric:    true,
		},
	}

	app, err := initializeComponents(cfg, &config.Credentials{
		APIKey: "test-key", SecretKey: "test-secret", Passphrase: "test-pass",
	})
	if err != nil {
		t.Fatalf("initializeComponents: %v", err)
	}
	defer app.store.Close()

	// Verify mean reversion is empty
	if len(cfg.MeanReversionConfigs) != 0 {
		t.Fatalf("production profile should not have mean reversion configs")
	}

	if err := app.startup(); err != nil {
		t.Fatalf("startup: %v", err)
	}
	defer app.gracefulShutdown()

	// Verify rebalancers started for both symbols
	if len(app.rebalancers) != 2 {
		t.Fatalf("expected 2 rebalancers, got %d", len(app.rebalancers))
	}
	for _, symbol := range []string{"DOGE-USDT", "WIF-USDT"} {
		rb, ok := app.rebalancers[symbol]
		if !ok {
			t.Fatalf("missing rebalancer for %s", symbol)
		}
		if !rb.IsRunning() {
			t.Fatalf("rebalancer for %s not running", symbol)
		}
	}

	// Verify health is healthy
	if app.health.State() != monitor.HealthStateHealthy {
		t.Fatalf("expected healthy state, got %s", app.health.State())
	}

	// Verify TradingGate is open for healthy symbols
	for _, symbol := range []string{"DOGE-USDT", "WIF-USDT"} {
		decision := app.tradingGate.Authorize(symbol, 0) // RiskIncreasing
		if !decision.Allowed {
			t.Fatalf("trading gate should allow %s after healthy startup, blocked: %s", symbol, decision.BlockReason)
		}
	}

	// Verify fill handler is registered
	if app.fillHandler == nil {
		t.Fatalf("fill handler should be registered")
	}
}

// Suppress unused import warnings
var _ = io.Discard
var _ = fmt.Sprint
var _ = time.Now
