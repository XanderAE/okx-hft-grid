package marketdata

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

const (
	// DefaultWebSocketURL is the OKX public WebSocket endpoint.
	DefaultWebSocketURL = "wss://ws.okx.com:8443/ws/v5/public"

	// HeartbeatInterval is how often we send a ping to the server.
	HeartbeatInterval = 25 * time.Second

	// HeartbeatTimeout is the maximum time to wait for a pong response.
	HeartbeatTimeout = 30 * time.Second

	// ConnectTimeout is the maximum time to wait for a WebSocket connection.
	ConnectTimeout = 10 * time.Second

	// InitialReconnectDelay is the starting delay for exponential backoff.
	InitialReconnectDelay = 1 * time.Second

	// MaxReconnectDelay is the maximum delay between reconnection attempts.
	MaxReconnectDelay = 60 * time.Second

	// MaxReconnectAttempts is the maximum number of reconnect attempts within 60 seconds.
	MaxReconnectAttempts = 5
)

// ConnectionState represents the current state of the WebSocket connection.
type ConnectionState int

const (
	StateDisconnected ConnectionState = iota
	StateConnecting
	StateConnected
	StateReconnecting
)

// WSClientConfig holds configuration for the WebSocket client.
type WSClientConfig struct {
	URL                   string
	Symbols               []string
	Channels              []string // e.g. ["tickers", "books5", "trades"]
	HeartbeatInterval     time.Duration
	HeartbeatTimeout      time.Duration
	ConnectTimeout        time.Duration
	InitialReconnectDelay time.Duration
	MaxReconnectDelay     time.Duration
	MaxReconnectAttempts  int
}

// DefaultWSClientConfig returns a WSClientConfig with default values.
func DefaultWSClientConfig() WSClientConfig {
	return WSClientConfig{
		URL:                   DefaultWebSocketURL,
		Channels:              []string{"tickers", "books5", "trades"},
		HeartbeatInterval:     HeartbeatInterval,
		HeartbeatTimeout:      HeartbeatTimeout,
		ConnectTimeout:        ConnectTimeout,
		InitialReconnectDelay: InitialReconnectDelay,
		MaxReconnectDelay:     MaxReconnectDelay,
		MaxReconnectAttempts:  MaxReconnectAttempts,
	}
}

// OKXSubscription represents an OKX WebSocket subscription argument.
type OKXSubscription struct {
	Channel string `json:"channel"`
	InstID  string `json:"instId"`
}

// OKXWSRequest represents a WebSocket request to OKX.
type OKXWSRequest struct {
	Op   string            `json:"op"`
	Args []OKXSubscription `json:"args"`
}

// OKXWSResponse represents a WebSocket response from OKX.
type OKXWSResponse struct {
	Event string          `json:"event,omitempty"`
	Code  string          `json:"code,omitempty"`
	Msg   string          `json:"msg,omitempty"`
	Arg   json.RawMessage `json:"arg,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// OrderPauseNotifier is an interface for components that need to be notified
// about order generation pause/resume state.
type OrderPauseNotifier interface {
	PauseNewOrders(reason string)
	ResumeNewOrders()
}

// SnapshotRequester is an interface for components that can request full order book snapshots.
type SnapshotRequester interface {
	RequestFullSnapshot(symbol string)
}

// WSClient implements a WebSocket client for OKX market data with automatic
// reconnection, heartbeat management, and channel subscription.
type WSClient struct {
	config WSClientConfig

	// Connection state
	conn            *websocket.Conn
	state           ConnectionState
	stateMu         sync.RWMutex
	lastPong        time.Time
	lastPongMu      sync.RWMutex

	// Data staleness
	staleSymbols    map[string]bool
	staleMu         sync.RWMutex

	// Subscriptions
	subscriptions   map[string][]string // symbol -> channels
	subMu           sync.RWMutex

	// Sequence tracking
	lastSeqID       map[string]int64 // symbol -> last sequenceId
	seqMu           sync.RWMutex

	// Callbacks
	callbacks       map[models.EventType][]models.MarketEventCallback
	callbackMu      sync.RWMutex

	// External notifiers
	orderPauser     OrderPauseNotifier
	snapshotReq     SnapshotRequester

	// Message handler (for raw messages, useful for parser integration)
	msgHandler      func(messageType int, data []byte)

	// Control
	done            chan struct{}
	reconnectCh     chan struct{}
	writeMu         sync.Mutex
	wg              sync.WaitGroup

	// Reconnection state
	reconnectCount  int
	reconnectMu     sync.Mutex
}

// NewWSClient creates a new WebSocket client with the given configuration.
func NewWSClient(config WSClientConfig) *WSClient {
	if config.URL == "" {
		config.URL = DefaultWebSocketURL
	}
	if config.HeartbeatInterval == 0 {
		config.HeartbeatInterval = HeartbeatInterval
	}
	if config.HeartbeatTimeout == 0 {
		config.HeartbeatTimeout = HeartbeatTimeout
	}
	if config.ConnectTimeout == 0 {
		config.ConnectTimeout = ConnectTimeout
	}
	if config.InitialReconnectDelay == 0 {
		config.InitialReconnectDelay = InitialReconnectDelay
	}
	if config.MaxReconnectDelay == 0 {
		config.MaxReconnectDelay = MaxReconnectDelay
	}
	if config.MaxReconnectAttempts == 0 {
		config.MaxReconnectAttempts = MaxReconnectAttempts
	}
	if len(config.Channels) == 0 {
		config.Channels = []string{"tickers", "books5", "trades"}
	}

	return &WSClient{
		config:        config,
		state:         StateDisconnected,
		staleSymbols:  make(map[string]bool),
		subscriptions: make(map[string][]string),
		lastSeqID:     make(map[string]int64),
		callbacks:     make(map[models.EventType][]models.MarketEventCallback),
		done:          make(chan struct{}),
		reconnectCh:   make(chan struct{}, 1),
	}
}

// SetOrderPauseNotifier sets the component that will be notified to pause/resume orders.
func (ws *WSClient) SetOrderPauseNotifier(notifier OrderPauseNotifier) {
	ws.orderPauser = notifier
}

// SetSnapshotRequester sets the component that handles full snapshot requests.
func (ws *WSClient) SetSnapshotRequester(requester SnapshotRequester) {
	ws.snapshotReq = requester
}

// SetMessageHandler sets a callback for raw WebSocket messages (for parser integration).
func (ws *WSClient) SetMessageHandler(handler func(messageType int, data []byte)) {
	ws.msgHandler = handler
}

// Connect establishes the WebSocket connection and subscribes to configured channels.
func (ws *WSClient) Connect(symbols []string) error {
	ws.stateMu.Lock()
	if ws.state == StateConnected || ws.state == StateConnecting {
		ws.stateMu.Unlock()
		return nil
	}
	ws.state = StateConnecting
	ws.stateMu.Unlock()

	// Store symbols for subscription
	ws.subMu.Lock()
	for _, symbol := range symbols {
		ws.subscriptions[symbol] = ws.config.Channels
	}
	ws.subMu.Unlock()

	// Also store in config for reconnection
	ws.config.Symbols = symbols

	if err := ws.connect(); err != nil {
		ws.stateMu.Lock()
		ws.state = StateDisconnected
		ws.stateMu.Unlock()
		return fmt.Errorf("failed to connect: %w", err)
	}

	// Subscribe to channels for all symbols
	if err := ws.subscribeAll(); err != nil {
		ws.closeConn()
		ws.stateMu.Lock()
		ws.state = StateDisconnected
		ws.stateMu.Unlock()
		return fmt.Errorf("failed to subscribe: %w", err)
	}

	ws.stateMu.Lock()
	ws.state = StateConnected
	ws.stateMu.Unlock()

	// Clear stale flags after successful connection
	ws.clearStaleFlags()

	// Start background goroutines
	ws.wg.Add(3)
	go ws.readLoop()
	go ws.heartbeatLoop()
	go ws.reconnectLoop()

	return nil
}

// Disconnect gracefully closes the WebSocket connection and stops all goroutines.
func (ws *WSClient) Disconnect() error {
	ws.stateMu.Lock()
	if ws.state == StateDisconnected {
		ws.stateMu.Unlock()
		return nil
	}
	ws.stateMu.Unlock()

	close(ws.done)
	ws.closeConn()

	ws.wg.Wait()

	ws.stateMu.Lock()
	ws.state = StateDisconnected
	ws.stateMu.Unlock()

	return nil
}

// Subscribe adds a subscription for a specific symbol and channel.
func (ws *WSClient) Subscribe(symbol string, channel string) error {
	ws.subMu.Lock()
	channels, exists := ws.subscriptions[symbol]
	if !exists {
		ws.subscriptions[symbol] = []string{channel}
	} else {
		// Check if channel already subscribed
		for _, ch := range channels {
			if ch == channel {
				ws.subMu.Unlock()
				return nil
			}
		}
		ws.subscriptions[symbol] = append(channels, channel)
	}
	ws.subMu.Unlock()

	// If connected, send subscription immediately
	if ws.IsConnected() {
		return ws.sendSubscribe([]OKXSubscription{{Channel: channel, InstID: symbol}})
	}
	return nil
}

// Unsubscribe removes a subscription for a specific symbol and channel.
func (ws *WSClient) Unsubscribe(symbol string, channel string) error {
	ws.subMu.Lock()
	channels, exists := ws.subscriptions[symbol]
	if !exists {
		ws.subMu.Unlock()
		return nil
	}
	// Remove channel from list
	for i, ch := range channels {
		if ch == channel {
			ws.subscriptions[symbol] = append(channels[:i], channels[i+1:]...)
			break
		}
	}
	if len(ws.subscriptions[symbol]) == 0 {
		delete(ws.subscriptions, symbol)
	}
	ws.subMu.Unlock()

	// If connected, send unsubscribe
	if ws.IsConnected() {
		return ws.sendUnsubscribe([]OKXSubscription{{Channel: channel, InstID: symbol}})
	}
	return nil
}

// IsConnected returns whether the WebSocket connection is currently active.
func (ws *WSClient) IsConnected() bool {
	ws.stateMu.RLock()
	defer ws.stateMu.RUnlock()
	return ws.state == StateConnected
}

// IsDataStale returns whether market data for the given symbol is stale.
func (ws *WSClient) IsDataStale(symbol string) bool {
	ws.staleMu.RLock()
	defer ws.staleMu.RUnlock()
	return ws.staleSymbols[symbol]
}

// GetState returns the current connection state.
func (ws *WSClient) GetState() ConnectionState {
	ws.stateMu.RLock()
	defer ws.stateMu.RUnlock()
	return ws.state
}

// RegisterCallback registers an event callback handler for a specific event type.
func (ws *WSClient) RegisterCallback(eventType models.EventType, handler models.MarketEventCallback) {
	ws.callbackMu.Lock()
	defer ws.callbackMu.Unlock()
	ws.callbacks[eventType] = append(ws.callbacks[eventType], handler)
}

// GetLastSequenceID returns the last processed sequence ID for a symbol.
func (ws *WSClient) GetLastSequenceID(symbol string) int64 {
	ws.seqMu.RLock()
	defer ws.seqMu.RUnlock()
	return ws.lastSeqID[symbol]
}

// UpdateSequenceID updates the last sequence ID for a symbol (called by parser).
func (ws *WSClient) UpdateSequenceID(symbol string, seqID int64) {
	ws.seqMu.Lock()
	defer ws.seqMu.Unlock()
	ws.lastSeqID[symbol] = seqID
}

// CheckSequenceID verifies sequence continuity. Returns true if the sequence is valid.
func (ws *WSClient) CheckSequenceID(symbol string, newSeqID int64) bool {
	ws.seqMu.RLock()
	lastID, exists := ws.lastSeqID[symbol]
	ws.seqMu.RUnlock()

	if !exists {
		// First message for this symbol - accept it
		return true
	}

	// sequenceId must be strictly greater than previous
	return newSeqID > lastID
}

// --- Internal connection methods ---

// connect establishes the actual WebSocket connection with timeout.
func (ws *WSClient) connect() error {
	u, err := url.Parse(ws.config.URL)
	if err != nil {
		return fmt.Errorf("invalid WebSocket URL: %w", err)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: ws.config.ConnectTimeout,
	}

	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		log.Printf("[MarketData] WebSocket connection failed: %v", err)
		return fmt.Errorf("WebSocket dial failed (timeout %v): %w", ws.config.ConnectTimeout, err)
	}

	// Set up pong handler for heartbeat
	conn.SetPongHandler(func(appData string) error {
		ws.lastPongMu.Lock()
		ws.lastPong = time.Now()
		ws.lastPongMu.Unlock()
		return nil
	})

	ws.conn = conn
	ws.lastPongMu.Lock()
	ws.lastPong = time.Now()
	ws.lastPongMu.Unlock()

	log.Printf("[MarketData] WebSocket connected to %s", ws.config.URL)
	return nil
}

// closeConn safely closes the WebSocket connection.
func (ws *WSClient) closeConn() {
	ws.writeMu.Lock()
	defer ws.writeMu.Unlock()
	if ws.conn != nil {
		_ = ws.conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
		_ = ws.conn.Close()
		ws.conn = nil
	}
}

// subscribeAll sends subscription requests for all configured symbol/channel pairs.
func (ws *WSClient) subscribeAll() error {
	ws.subMu.RLock()
	var subs []OKXSubscription
	for symbol, channels := range ws.subscriptions {
		for _, channel := range channels {
			subs = append(subs, OKXSubscription{
				Channel: channel,
				InstID:  symbol,
			})
		}
	}
	ws.subMu.RUnlock()

	if len(subs) == 0 {
		return nil
	}

	return ws.sendSubscribe(subs)
}

// sendSubscribe sends a subscribe request to OKX.
func (ws *WSClient) sendSubscribe(subs []OKXSubscription) error {
	req := OKXWSRequest{
		Op:   "subscribe",
		Args: subs,
	}
	return ws.writeJSON(req)
}

// sendUnsubscribe sends an unsubscribe request to OKX.
func (ws *WSClient) sendUnsubscribe(subs []OKXSubscription) error {
	req := OKXWSRequest{
		Op:   "unsubscribe",
		Args: subs,
	}
	return ws.writeJSON(req)
}

// writeJSON writes a JSON message to the WebSocket connection (thread-safe).
func (ws *WSClient) writeJSON(v interface{}) error {
	ws.writeMu.Lock()
	defer ws.writeMu.Unlock()
	if ws.conn == nil {
		return fmt.Errorf("connection is nil")
	}
	return ws.conn.WriteJSON(v)
}

// --- Background goroutines ---

// readLoop continuously reads messages from the WebSocket connection.
func (ws *WSClient) readLoop() {
	defer ws.wg.Done()
	for {
		select {
		case <-ws.done:
			return
		default:
		}

		if ws.conn == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		messageType, message, err := ws.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("[MarketData] WebSocket closed normally")
				return
			}
			// Connection error - trigger reconnection
			select {
			case <-ws.done:
				return
			default:
				log.Printf("[MarketData] WebSocket read error: %v", err)
				ws.handleDisconnection()
			}
			return
		}

		// Forward raw message to handler if set
		if ws.msgHandler != nil {
			ws.msgHandler(messageType, message)
		}

		// Handle ping from OKX (they may send "ping" text)
		if string(message) == "ping" {
			ws.writeMu.Lock()
			if ws.conn != nil {
				_ = ws.conn.WriteMessage(websocket.TextMessage, []byte("pong"))
			}
			ws.writeMu.Unlock()
			continue
		}
	}
}

// heartbeatLoop sends periodic pings and checks for pong responses.
func (ws *WSClient) heartbeatLoop() {
	defer ws.wg.Done()
	ticker := time.NewTicker(ws.config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ws.done:
			return
		case <-ticker.C:
			if !ws.IsConnected() {
				continue
			}

			// Check if we've received a pong within the timeout period
			ws.lastPongMu.RLock()
			lastPong := ws.lastPong
			ws.lastPongMu.RUnlock()

			if time.Since(lastPong) > ws.config.HeartbeatTimeout {
				log.Printf("[MarketData] Heartbeat timeout: no pong received for %v", time.Since(lastPong))
				ws.handleDisconnection()
				return
			}

			// Send ping
			ws.writeMu.Lock()
			if ws.conn != nil {
				err := ws.conn.WriteMessage(websocket.PingMessage, nil)
				if err != nil {
					ws.writeMu.Unlock()
					log.Printf("[MarketData] Failed to send ping: %v", err)
					ws.handleDisconnection()
					return
				}
			}
			ws.writeMu.Unlock()
		}
	}
}

// reconnectLoop handles reconnection attempts with exponential backoff.
func (ws *WSClient) reconnectLoop() {
	defer ws.wg.Done()
	for {
		select {
		case <-ws.done:
			return
		case <-ws.reconnectCh:
			ws.performReconnect()
		}
	}
}

// handleDisconnection is called when a disconnection is detected.
func (ws *WSClient) handleDisconnection() {
	ws.stateMu.Lock()
	if ws.state == StateReconnecting || ws.state == StateDisconnected {
		ws.stateMu.Unlock()
		return
	}
	ws.state = StateReconnecting
	ws.stateMu.Unlock()

	log.Printf("[MarketData] Connection lost, marking data as STALE and pausing new orders")

	// Mark all data as STALE
	ws.markAllStale()

	// Pause new order generation
	if ws.orderPauser != nil {
		ws.orderPauser.PauseNewOrders("WebSocket disconnected")
	}

	// Emit DataStale event
	ws.emitStaleEvent()

	// Close existing connection
	ws.closeConn()

	// Trigger reconnection
	select {
	case ws.reconnectCh <- struct{}{}:
	default:
		// reconnect already pending
	}
}

// performReconnect attempts to reconnect with exponential backoff.
func (ws *WSClient) performReconnect() {
	ws.reconnectMu.Lock()
	ws.reconnectCount = 0
	ws.reconnectMu.Unlock()

	delay := ws.config.InitialReconnectDelay

	for {
		select {
		case <-ws.done:
			return
		default:
		}

		ws.reconnectMu.Lock()
		attempt := ws.reconnectCount
		ws.reconnectCount++
		ws.reconnectMu.Unlock()

		if attempt >= ws.config.MaxReconnectAttempts {
			log.Printf("[MarketData] Max reconnection attempts (%d) exhausted", ws.config.MaxReconnectAttempts)
			ws.stateMu.Lock()
			ws.state = StateDisconnected
			ws.stateMu.Unlock()
			return
		}

		log.Printf("[MarketData] Reconnection attempt %d/%d (delay: %v)", attempt+1, ws.config.MaxReconnectAttempts, delay)

		// Wait before attempting
		select {
		case <-ws.done:
			return
		case <-time.After(delay):
		}

		// Try to connect
		err := ws.connect()
		if err != nil {
			log.Printf("[MarketData] Reconnection attempt %d failed: %v", attempt+1, err)
			// Exponential backoff
			delay = ws.calculateBackoff(attempt + 1)
			continue
		}

		// Connection succeeded - resubscribe
		if err := ws.subscribeAll(); err != nil {
			log.Printf("[MarketData] Resubscription failed: %v", err)
			ws.closeConn()
			delay = ws.calculateBackoff(attempt + 1)
			continue
		}

		// Reconnection successful
		log.Printf("[MarketData] Reconnection successful after %d attempts", attempt+1)
		ws.onReconnectSuccess()
		return
	}
}

// onReconnectSuccess handles post-reconnection tasks.
func (ws *WSClient) onReconnectSuccess() {
	ws.stateMu.Lock()
	ws.state = StateConnected
	ws.stateMu.Unlock()

	// Request full order book snapshots for all subscribed symbols
	if ws.snapshotReq != nil {
		ws.subMu.RLock()
		for symbol := range ws.subscriptions {
			ws.snapshotReq.RequestFullSnapshot(symbol)
		}
		ws.subMu.RUnlock()
	}

	// Reset sequence IDs to force verification on next message
	ws.seqMu.Lock()
	ws.lastSeqID = make(map[string]int64)
	ws.seqMu.Unlock()

	// Clear stale flags
	ws.clearStaleFlags()

	// Resume order generation
	if ws.orderPauser != nil {
		ws.orderPauser.ResumeNewOrders()
	}

	// Restart read and heartbeat loops
	ws.wg.Add(2)
	go ws.readLoop()
	go ws.heartbeatLoop()
}

// calculateBackoff computes the exponential backoff delay for a given attempt.
func (ws *WSClient) calculateBackoff(attempt int) time.Duration {
	// Exponential backoff: initialDelay * 2^attempt, capped at maxDelay
	backoff := float64(ws.config.InitialReconnectDelay) * math.Pow(2, float64(attempt))
	if backoff > float64(ws.config.MaxReconnectDelay) {
		backoff = float64(ws.config.MaxReconnectDelay)
	}
	return time.Duration(backoff)
}

// --- State management helpers ---

// markAllStale marks all subscribed symbols' data as stale.
func (ws *WSClient) markAllStale() {
	ws.staleMu.Lock()
	defer ws.staleMu.Unlock()
	ws.subMu.RLock()
	defer ws.subMu.RUnlock()

	for symbol := range ws.subscriptions {
		ws.staleSymbols[symbol] = true
	}
}

// clearStaleFlags clears all stale data flags.
func (ws *WSClient) clearStaleFlags() {
	ws.staleMu.Lock()
	defer ws.staleMu.Unlock()
	ws.staleSymbols = make(map[string]bool)
}

// ClearStaleForSymbol clears the stale flag for a specific symbol (called after fresh data arrives).
func (ws *WSClient) ClearStaleForSymbol(symbol string) {
	ws.staleMu.Lock()
	defer ws.staleMu.Unlock()
	delete(ws.staleSymbols, symbol)
}

// emitStaleEvent emits a DataStale event to all registered callbacks.
func (ws *WSClient) emitStaleEvent() {
	ws.callbackMu.RLock()
	handlers := ws.callbacks[models.EventDataStale]
	ws.callbackMu.RUnlock()

	event := models.MarketEvent{
		Timestamp: time.Now(),
	}

	for _, handler := range handlers {
		handler(event)
	}
}

// GetReconnectCount returns the current number of reconnect attempts.
func (ws *WSClient) GetReconnectCount() int {
	ws.reconnectMu.Lock()
	defer ws.reconnectMu.Unlock()
	return ws.reconnectCount
}
