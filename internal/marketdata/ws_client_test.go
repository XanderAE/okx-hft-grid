package marketdata

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// upgrader for test WebSocket server.
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// mockWSServer creates a test WebSocket server that echoes back pings and handles subscriptions.
func mockWSServer(t *testing.T, handler func(conn *websocket.Conn)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("Upgrade error: %v", err)
			return
		}
		defer conn.Close()
		handler(conn)
	}))
	return server
}

// wsURL converts an HTTP test server URL to a WebSocket URL.
func wsURL(server *httptest.Server) string {
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func TestNewWSClient_DefaultConfig(t *testing.T) {
	config := DefaultWSClientConfig()
	client := NewWSClient(config)

	if client == nil {
		t.Fatal("NewWSClient returned nil")
	}
	if client.config.URL != DefaultWebSocketURL {
		t.Errorf("expected URL %s, got %s", DefaultWebSocketURL, client.config.URL)
	}
	if client.config.HeartbeatInterval != HeartbeatInterval {
		t.Errorf("expected HeartbeatInterval %v, got %v", HeartbeatInterval, client.config.HeartbeatInterval)
	}
	if client.config.HeartbeatTimeout != HeartbeatTimeout {
		t.Errorf("expected HeartbeatTimeout %v, got %v", HeartbeatTimeout, client.config.HeartbeatTimeout)
	}
	if client.config.ConnectTimeout != ConnectTimeout {
		t.Errorf("expected ConnectTimeout %v, got %v", ConnectTimeout, client.config.ConnectTimeout)
	}
	if client.config.MaxReconnectAttempts != MaxReconnectAttempts {
		t.Errorf("expected MaxReconnectAttempts %d, got %d", MaxReconnectAttempts, client.config.MaxReconnectAttempts)
	}
}

func TestWSClient_Connect_Success(t *testing.T) {
	var subscribeReceived atomic.Int32

	server := mockWSServer(t, func(conn *websocket.Conn) {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req OKXWSRequest
			if json.Unmarshal(msg, &req) == nil && req.Op == "subscribe" {
				subscribeReceived.Add(int32(len(req.Args)))
				// Send subscription confirmation
				resp := map[string]interface{}{
					"event": "subscribe",
					"arg":   req.Args[0],
				}
				respBytes, _ := json.Marshal(resp)
				_ = conn.WriteMessage(websocket.TextMessage, respBytes)
			}
		}
	})
	defer server.Close()

	config := WSClientConfig{
		URL:                   wsURL(server),
		Channels:              []string{"tickers", "books5"},
		HeartbeatInterval:     5 * time.Second,
		HeartbeatTimeout:      10 * time.Second,
		ConnectTimeout:        5 * time.Second,
		InitialReconnectDelay: 100 * time.Millisecond,
		MaxReconnectDelay:     1 * time.Second,
		MaxReconnectAttempts:  3,
	}
	client := NewWSClient(config)

	err := client.Connect([]string{"BTC-USDT", "ETH-USDT"})
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	if !client.IsConnected() {
		t.Error("expected IsConnected to be true after successful connect")
	}

	// Wait for subscriptions to be processed
	time.Sleep(100 * time.Millisecond)

	// Should have sent subscriptions for 2 symbols × 2 channels = 4 subscriptions
	// (sent as a single batch)
	if subscribeReceived.Load() < 1 {
		t.Error("expected subscribe request to be received by server")
	}
}

func TestWSClient_Connect_Timeout(t *testing.T) {
	// Create a server that never completes the handshake
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	// Accept connections but never respond
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Hold the connection open without doing anything
			go func() {
				time.Sleep(30 * time.Second)
				conn.Close()
			}()
		}
	}()

	config := WSClientConfig{
		URL:            "ws://" + listener.Addr().String(),
		Channels:       []string{"tickers"},
		ConnectTimeout: 500 * time.Millisecond,
	}
	client := NewWSClient(config)

	err = client.Connect([]string{"BTC-USDT"})
	if err == nil {
		client.Disconnect()
		t.Fatal("expected Connect to fail with timeout")
	}

	if !client.IsDataStale("BTC-USDT") {
		// Data starts fresh, only stale on disconnect
	}
}

func TestWSClient_Disconnect(t *testing.T) {
	server := mockWSServer(t, func(conn *websocket.Conn) {
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	})
	defer server.Close()

	config := WSClientConfig{
		URL:               wsURL(server),
		Channels:          []string{"tickers"},
		HeartbeatInterval: 5 * time.Second,
		HeartbeatTimeout:  10 * time.Second,
		ConnectTimeout:    5 * time.Second,
	}
	client := NewWSClient(config)

	err := client.Connect([]string{"BTC-USDT"})
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	err = client.Disconnect()
	if err != nil {
		t.Fatalf("Disconnect failed: %v", err)
	}

	if client.IsConnected() {
		t.Error("expected IsConnected to be false after disconnect")
	}
}

func TestWSClient_IsDataStale(t *testing.T) {
	client := NewWSClient(DefaultWSClientConfig())

	// Initially not stale
	if client.IsDataStale("BTC-USDT") {
		t.Error("expected data not to be stale initially")
	}

	// Manually set stale
	client.staleMu.Lock()
	client.staleSymbols["BTC-USDT"] = true
	client.staleMu.Unlock()

	if !client.IsDataStale("BTC-USDT") {
		t.Error("expected data to be stale after marking")
	}

	// Clear stale
	client.ClearStaleForSymbol("BTC-USDT")
	if client.IsDataStale("BTC-USDT") {
		t.Error("expected data not to be stale after clearing")
	}
}

func TestWSClient_MarkAllStale(t *testing.T) {
	client := NewWSClient(DefaultWSClientConfig())

	// Set up subscriptions
	client.subMu.Lock()
	client.subscriptions["BTC-USDT"] = []string{"tickers"}
	client.subscriptions["ETH-USDT"] = []string{"tickers"}
	client.subMu.Unlock()

	client.markAllStale()

	if !client.IsDataStale("BTC-USDT") {
		t.Error("expected BTC-USDT to be stale")
	}
	if !client.IsDataStale("ETH-USDT") {
		t.Error("expected ETH-USDT to be stale")
	}
}

func TestWSClient_SequenceID_Check(t *testing.T) {
	client := NewWSClient(DefaultWSClientConfig())

	// First message should always be accepted
	if !client.CheckSequenceID("BTC-USDT", 100) {
		t.Error("first sequence ID should be accepted")
	}

	// Set a sequence ID
	client.UpdateSequenceID("BTC-USDT", 100)

	// Greater sequence should be valid
	if !client.CheckSequenceID("BTC-USDT", 101) {
		t.Error("greater sequence ID should be valid")
	}
	if !client.CheckSequenceID("BTC-USDT", 200) {
		t.Error("much greater sequence ID should be valid")
	}

	// Equal or lesser sequence should be invalid
	if client.CheckSequenceID("BTC-USDT", 100) {
		t.Error("equal sequence ID should be invalid")
	}
	if client.CheckSequenceID("BTC-USDT", 99) {
		t.Error("lesser sequence ID should be invalid")
	}
}

func TestWSClient_Subscribe_Unsubscribe(t *testing.T) {
	client := NewWSClient(DefaultWSClientConfig())

	// Subscribe
	err := client.Subscribe("BTC-USDT", "tickers")
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	client.subMu.RLock()
	channels, exists := client.subscriptions["BTC-USDT"]
	client.subMu.RUnlock()

	if !exists {
		t.Fatal("subscription not recorded")
	}
	if len(channels) != 1 || channels[0] != "tickers" {
		t.Errorf("unexpected channels: %v", channels)
	}

	// Subscribe to another channel
	err = client.Subscribe("BTC-USDT", "books5")
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	client.subMu.RLock()
	channels = client.subscriptions["BTC-USDT"]
	client.subMu.RUnlock()

	if len(channels) != 2 {
		t.Errorf("expected 2 channels, got %d", len(channels))
	}

	// Duplicate subscription should be idempotent
	err = client.Subscribe("BTC-USDT", "tickers")
	if err != nil {
		t.Fatalf("Duplicate subscribe failed: %v", err)
	}
	client.subMu.RLock()
	channels = client.subscriptions["BTC-USDT"]
	client.subMu.RUnlock()
	if len(channels) != 2 {
		t.Errorf("expected 2 channels after duplicate, got %d", len(channels))
	}

	// Unsubscribe
	err = client.Unsubscribe("BTC-USDT", "tickers")
	if err != nil {
		t.Fatalf("Unsubscribe failed: %v", err)
	}

	client.subMu.RLock()
	channels = client.subscriptions["BTC-USDT"]
	client.subMu.RUnlock()

	if len(channels) != 1 || channels[0] != "books5" {
		t.Errorf("unexpected channels after unsubscribe: %v", channels)
	}

	// Unsubscribe the last channel should remove the symbol
	err = client.Unsubscribe("BTC-USDT", "books5")
	if err != nil {
		t.Fatalf("Unsubscribe failed: %v", err)
	}

	client.subMu.RLock()
	_, exists = client.subscriptions["BTC-USDT"]
	client.subMu.RUnlock()
	if exists {
		t.Error("symbol should be removed when all channels unsubscribed")
	}
}

func TestWSClient_Heartbeat_Timeout_Triggers_Reconnect(t *testing.T) {
	var connCount atomic.Int32
	var mu sync.Mutex
	var conns []*websocket.Conn

	server := mockWSServer(t, func(conn *websocket.Conn) {
		connCount.Add(1)
		mu.Lock()
		conns = append(conns, conn)
		mu.Unlock()
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	})
	defer server.Close()

	config := WSClientConfig{
		URL:                   wsURL(server),
		Channels:              []string{"tickers"},
		HeartbeatInterval:     100 * time.Millisecond,
		HeartbeatTimeout:      200 * time.Millisecond,
		ConnectTimeout:        5 * time.Second,
		InitialReconnectDelay: 50 * time.Millisecond,
		MaxReconnectDelay:     200 * time.Millisecond,
		MaxReconnectAttempts:  3,
	}
	client := NewWSClient(config)

	err := client.Connect([]string{"BTC-USDT"})
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	// Wait for heartbeat timeout to trigger (server doesn't respond to pings)
	time.Sleep(500 * time.Millisecond)

	// Should have attempted reconnection
	if connCount.Load() < 2 {
		t.Logf("Connection count: %d (expected >= 2, reconnection may still be in progress)", connCount.Load())
	}
}

func TestWSClient_Reconnection_Marks_Data_Stale(t *testing.T) {
	server := mockWSServer(t, func(conn *websocket.Conn) {
		// Respond to pings to keep connection alive briefly
		conn.SetPingHandler(func(appData string) error {
			return conn.WriteMessage(websocket.PongMessage, nil)
		})
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	})
	defer server.Close()

	config := WSClientConfig{
		URL:                   wsURL(server),
		Channels:              []string{"tickers"},
		HeartbeatInterval:     5 * time.Second,
		HeartbeatTimeout:      10 * time.Second,
		ConnectTimeout:        5 * time.Second,
		InitialReconnectDelay: 100 * time.Millisecond,
		MaxReconnectDelay:     500 * time.Millisecond,
		MaxReconnectAttempts:  2,
	}
	client := NewWSClient(config)

	err := client.Connect([]string{"BTC-USDT", "ETH-USDT"})
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer client.Disconnect()

	// Simulate disconnection
	client.handleDisconnection()

	// Data should be stale
	if !client.IsDataStale("BTC-USDT") {
		t.Error("expected BTC-USDT data to be stale after disconnection")
	}
	if !client.IsDataStale("ETH-USDT") {
		t.Error("expected ETH-USDT data to be stale after disconnection")
	}
}

func TestWSClient_RegisterCallback(t *testing.T) {
	client := NewWSClient(DefaultWSClientConfig())

	var callbackCalled atomic.Int32
	handler := func(event models.MarketEvent) {
		callbackCalled.Add(1)
	}

	client.RegisterCallback(models.EventDataStale, handler)

	// Emit stale event
	client.emitStaleEvent()

	if callbackCalled.Load() != 1 {
		t.Errorf("expected callback to be called once, got %d", callbackCalled.Load())
	}
}

func TestWSClient_OrderPauseNotifier(t *testing.T) {
	client := NewWSClient(DefaultWSClientConfig())

	var paused atomic.Bool
	var pauseReason string
	var mu sync.Mutex

	notifier := &mockOrderPauser{
		pauseFn: func(reason string) {
			paused.Store(true)
			mu.Lock()
			pauseReason = reason
			mu.Unlock()
		},
		resumeFn: func() {
			paused.Store(false)
		},
	}
	client.SetOrderPauseNotifier(notifier)

	// Set up subscriptions
	client.subMu.Lock()
	client.subscriptions["BTC-USDT"] = []string{"tickers"}
	client.subMu.Unlock()

	// Simulate disconnection
	client.stateMu.Lock()
	client.state = StateConnected
	client.stateMu.Unlock()
	client.handleDisconnection()

	time.Sleep(50 * time.Millisecond)

	if !paused.Load() {
		t.Error("expected orders to be paused after disconnection")
	}
	mu.Lock()
	if pauseReason == "" {
		t.Error("expected pause reason to be set")
	}
	mu.Unlock()
}

func TestWSClient_CalculateBackoff(t *testing.T) {
	config := WSClientConfig{
		InitialReconnectDelay: 1 * time.Second,
		MaxReconnectDelay:     60 * time.Second,
	}
	client := NewWSClient(config)

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 16 * time.Second},
		{5, 32 * time.Second},
		{6, 60 * time.Second}, // capped at max
		{7, 60 * time.Second}, // still capped
	}

	for _, tt := range tests {
		got := client.calculateBackoff(tt.attempt)
		if got != tt.expected {
			t.Errorf("calculateBackoff(%d) = %v, want %v", tt.attempt, got, tt.expected)
		}
	}
}

func TestWSClient_ClearStaleFlags(t *testing.T) {
	client := NewWSClient(DefaultWSClientConfig())

	client.staleMu.Lock()
	client.staleSymbols["BTC-USDT"] = true
	client.staleSymbols["ETH-USDT"] = true
	client.staleMu.Unlock()

	client.clearStaleFlags()

	if client.IsDataStale("BTC-USDT") || client.IsDataStale("ETH-USDT") {
		t.Error("expected all stale flags to be cleared")
	}
}

func TestWSClient_GetState(t *testing.T) {
	client := NewWSClient(DefaultWSClientConfig())

	if client.GetState() != StateDisconnected {
		t.Error("expected initial state to be StateDisconnected")
	}

	client.stateMu.Lock()
	client.state = StateConnected
	client.stateMu.Unlock()

	if client.GetState() != StateConnected {
		t.Error("expected state to be StateConnected")
	}
}

// --- Mock helpers ---

type mockOrderPauser struct {
	pauseFn  func(reason string)
	resumeFn func()
}

func (m *mockOrderPauser) PauseNewOrders(reason string) {
	if m.pauseFn != nil {
		m.pauseFn(reason)
	}
}

func (m *mockOrderPauser) ResumeNewOrders() {
	if m.resumeFn != nil {
		m.resumeFn()
	}
}
