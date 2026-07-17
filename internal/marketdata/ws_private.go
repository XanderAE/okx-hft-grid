package marketdata

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/yourname/okx-hft-grid/internal/config"
)

const (
	// PrivateWebSocketURL is the OKX private WebSocket endpoint.
	PrivateWebSocketURL = "wss://ws.okx.com:8443/ws/v5/private"

	// PrivateHeartbeatInterval is how often we send a ping to the private WS server.
	PrivateHeartbeatInterval = 25 * time.Second

	// PrivateHeartbeatTimeout is the maximum time to wait for a pong response on private WS.
	PrivateHeartbeatTimeout = 30 * time.Second

	// PrivateConnectTimeout is the maximum time to wait for a private WS connection.
	PrivateConnectTimeout = 10 * time.Second

	// PrivateInitialReconnectDelay is the starting delay for exponential backoff on private WS.
	PrivateInitialReconnectDelay = 1 * time.Second

	// PrivateMaxReconnectDelay is the maximum delay between reconnection attempts on private WS.
	PrivateMaxReconnectDelay = 60 * time.Second

	// PrivateMaxReconnectAttempts is the max reconnect attempts for private WS.
	PrivateMaxReconnectAttempts = 10
)

// FillCallback is the callback function invoked when a fill is received.
// Parameters: instId, side, fillPx, fillSz, ordId, state
type FillCallback func(instId, side, fillPx, fillSz, ordId, state string)

// PrivateWSClientConfig holds configuration for the private WebSocket client.
type PrivateWSClientConfig struct {
	URL                   string
	HeartbeatInterval     time.Duration
	HeartbeatTimeout      time.Duration
	ConnectTimeout        time.Duration
	InitialReconnectDelay time.Duration
	MaxReconnectDelay     time.Duration
	MaxReconnectAttempts  int
}

// DefaultPrivateWSClientConfig returns a PrivateWSClientConfig with default values.
func DefaultPrivateWSClientConfig() PrivateWSClientConfig {
	return PrivateWSClientConfig{
		URL:                   PrivateWebSocketURL,
		HeartbeatInterval:     PrivateHeartbeatInterval,
		HeartbeatTimeout:      PrivateHeartbeatTimeout,
		ConnectTimeout:        PrivateConnectTimeout,
		InitialReconnectDelay: PrivateInitialReconnectDelay,
		MaxReconnectDelay:     PrivateMaxReconnectDelay,
		MaxReconnectAttempts:  PrivateMaxReconnectAttempts,
	}
}

// OKXLoginArg represents the login argument for OKX private WebSocket authentication.
type OKXLoginArg struct {
	APIKey     string `json:"apiKey"`
	Passphrase string `json:"passphrase"`
	Timestamp  string `json:"timestamp"`
	Sign       string `json:"sign"`
}

// OKXLoginRequest represents the login request for OKX private WebSocket.
type OKXLoginRequest struct {
	Op   string        `json:"op"`
	Args []OKXLoginArg `json:"args"`
}

// OKXPrivateSubArg represents a subscription argument for OKX private channels.
type OKXPrivateSubArg struct {
	Channel  string `json:"channel"`
	InstType string `json:"instType"`
}

// OKXPrivateSubRequest represents a subscription request for OKX private channels.
type OKXPrivateSubRequest struct {
	Op   string             `json:"op"`
	Args []OKXPrivateSubArg `json:"args"`
}

// OKXOrderPush represents the structure of an order push from OKX private WS.
type OKXOrderPush struct {
	Arg  OKXPrivateSubArg  `json:"arg"`
	Data []OKXOrderPushData `json:"data"`
}

// OKXOrderPushData represents a single order data item in an OKX order push.
type OKXOrderPushData struct {
	InstID string `json:"instId"`
	OrdID  string `json:"ordId"`
	Side   string `json:"side"`
	FillPx string `json:"fillPx"`
	FillSz string `json:"fillSz"`
	AvgPx  string `json:"avgPx"`
	State  string `json:"state"`
	Px     string `json:"px"`
	Sz     string `json:"sz"`
}

// PrivateWSClient implements a WebSocket client for OKX private channels
// with authentication, heartbeat management, and automatic reconnection.
type PrivateWSClient struct {
	config      PrivateWSClientConfig
	credentials *config.Credentials

	// Connection state
	conn       *websocket.Conn
	connected  bool
	stateMu    sync.RWMutex
	lastPong   time.Time
	lastPongMu sync.RWMutex

	// Callbacks
	fillCallback FillCallback
	callbackMu   sync.RWMutex

	// Control
	done        chan struct{}
	reconnectCh chan struct{}
	writeMu     sync.Mutex
	wg          sync.WaitGroup

	// Reconnection state
	reconnectCount int
	reconnectMu    sync.Mutex
}

// NewPrivateWSClient creates a new private WebSocket client for OKX.
func NewPrivateWSClient(cfg PrivateWSClientConfig, creds *config.Credentials) *PrivateWSClient {
	if cfg.URL == "" {
		cfg.URL = PrivateWebSocketURL
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = PrivateHeartbeatInterval
	}
	if cfg.HeartbeatTimeout == 0 {
		cfg.HeartbeatTimeout = PrivateHeartbeatTimeout
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = PrivateConnectTimeout
	}
	if cfg.InitialReconnectDelay == 0 {
		cfg.InitialReconnectDelay = PrivateInitialReconnectDelay
	}
	if cfg.MaxReconnectDelay == 0 {
		cfg.MaxReconnectDelay = PrivateMaxReconnectDelay
	}
	if cfg.MaxReconnectAttempts == 0 {
		cfg.MaxReconnectAttempts = PrivateMaxReconnectAttempts
	}

	return &PrivateWSClient{
		config:      cfg,
		credentials: creds,
		done:        make(chan struct{}),
		reconnectCh: make(chan struct{}, 1),
	}
}

// SetFillCallback registers the callback that is invoked on order fills.
func (pw *PrivateWSClient) SetFillCallback(cb FillCallback) {
	pw.callbackMu.Lock()
	defer pw.callbackMu.Unlock()
	pw.fillCallback = cb
}

// Connect establishes the private WebSocket connection, authenticates, and subscribes to orders.
func (pw *PrivateWSClient) Connect() error {
	pw.stateMu.Lock()
	if pw.connected {
		pw.stateMu.Unlock()
		return nil
	}
	pw.stateMu.Unlock()

	if err := pw.connect(); err != nil {
		return fmt.Errorf("failed to connect private WS: %w", err)
	}

	if err := pw.authenticate(); err != nil {
		pw.closeConn()
		return fmt.Errorf("failed to authenticate private WS: %w", err)
	}

	if err := pw.subscribeOrders(); err != nil {
		pw.closeConn()
		return fmt.Errorf("failed to subscribe to orders channel: %w", err)
	}

	pw.stateMu.Lock()
	pw.connected = true
	pw.stateMu.Unlock()

	// Start background goroutines
	pw.wg.Add(3)
	go pw.readLoop()
	go pw.heartbeatLoop()
	go pw.reconnectLoop()

	log.Printf("[PrivateWS] Connected and authenticated successfully")
	return nil
}

// Disconnect gracefully closes the private WebSocket connection.
func (pw *PrivateWSClient) Disconnect() error {
	pw.stateMu.Lock()
	if !pw.connected {
		pw.stateMu.Unlock()
		return nil
	}
	pw.stateMu.Unlock()

	close(pw.done)
	pw.closeConn()

	pw.wg.Wait()

	pw.stateMu.Lock()
	pw.connected = false
	pw.stateMu.Unlock()

	log.Printf("[PrivateWS] Disconnected")
	return nil
}

// IsConnected returns whether the private WS is currently connected.
func (pw *PrivateWSClient) IsConnected() bool {
	pw.stateMu.RLock()
	defer pw.stateMu.RUnlock()
	return pw.connected
}

// --- Internal methods ---

// connect establishes the actual WebSocket connection.
func (pw *PrivateWSClient) connect() error {
	u, err := url.Parse(pw.config.URL)
	if err != nil {
		return fmt.Errorf("invalid WebSocket URL: %w", err)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: pw.config.ConnectTimeout,
	}

	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("WebSocket dial failed: %w", err)
	}

	conn.SetPongHandler(func(appData string) error {
		pw.lastPongMu.Lock()
		pw.lastPong = time.Now()
		pw.lastPongMu.Unlock()
		return nil
	})

	pw.conn = conn
	pw.lastPongMu.Lock()
	pw.lastPong = time.Now()
	pw.lastPongMu.Unlock()

	return nil
}

// closeConn safely closes the WebSocket connection.
func (pw *PrivateWSClient) closeConn() {
	pw.writeMu.Lock()
	defer pw.writeMu.Unlock()
	if pw.conn != nil {
		_ = pw.conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
		_ = pw.conn.Close()
		pw.conn = nil
	}
}

// authenticate sends the login request to OKX private WebSocket.
// Sign = Base64(HMAC-SHA256(secretKey, timestamp+'GET'+'/users/self/verify'))
func (pw *PrivateWSClient) authenticate() error {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	message := timestamp + "GET" + "/users/self/verify"

	mac := hmac.New(sha256.New, []byte(pw.credentials.SecretKey))
	mac.Write([]byte(message))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	loginReq := OKXLoginRequest{
		Op: "login",
		Args: []OKXLoginArg{
			{
				APIKey:     pw.credentials.APIKey,
				Passphrase: pw.credentials.Passphrase,
				Timestamp:  timestamp,
				Sign:       sign,
			},
		},
	}

	if err := pw.writeJSON(loginReq); err != nil {
		return fmt.Errorf("failed to send login request: %w", err)
	}

	// Wait for login response (with timeout)
	pw.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, msg, err := pw.conn.ReadMessage()
	pw.conn.SetReadDeadline(time.Time{}) // Clear deadline
	if err != nil {
		return fmt.Errorf("failed to read login response: %w", err)
	}

	var resp struct {
		Event string `json:"event"`
		Code  string `json:"code"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal(msg, &resp); err != nil {
		return fmt.Errorf("failed to parse login response: %w", err)
	}

	if resp.Event != "login" || resp.Code != "0" {
		return fmt.Errorf("login failed: event=%s, code=%s, msg=%s", resp.Event, resp.Code, resp.Msg)
	}

	log.Printf("[PrivateWS] Authentication successful")
	return nil
}

// subscribeOrders subscribes to the "orders" channel for SPOT instruments.
func (pw *PrivateWSClient) subscribeOrders() error {
	subReq := OKXPrivateSubRequest{
		Op: "subscribe",
		Args: []OKXPrivateSubArg{
			{
				Channel:  "orders",
				InstType: "SPOT",
			},
		},
	}

	if err := pw.writeJSON(subReq); err != nil {
		return fmt.Errorf("failed to send subscribe request: %w", err)
	}

	// Wait for subscription confirmation
	pw.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, msg, err := pw.conn.ReadMessage()
	pw.conn.SetReadDeadline(time.Time{})
	if err != nil {
		return fmt.Errorf("failed to read subscribe response: %w", err)
	}

	var resp struct {
		Event string `json:"event"`
		Code  string `json:"code"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal(msg, &resp); err != nil {
		// It might be a data push already, not a subscribe confirmation - that's ok
		log.Printf("[PrivateWS] Subscribe response parse info: %v (might be data push)", err)
		return nil
	}

	if resp.Event == "error" {
		return fmt.Errorf("subscribe failed: code=%s, msg=%s", resp.Code, resp.Msg)
	}

	log.Printf("[PrivateWS] Subscribed to orders channel")
	return nil
}

// writeJSON writes a JSON message to the WebSocket connection (thread-safe).
func (pw *PrivateWSClient) writeJSON(v interface{}) error {
	pw.writeMu.Lock()
	defer pw.writeMu.Unlock()
	if pw.conn == nil {
		return fmt.Errorf("connection is nil")
	}
	return pw.conn.WriteJSON(v)
}

// readLoop continuously reads messages from the private WebSocket.
func (pw *PrivateWSClient) readLoop() {
	defer pw.wg.Done()
	for {
		select {
		case <-pw.done:
			return
		default:
		}

		if pw.conn == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		_, message, err := pw.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("[PrivateWS] WebSocket closed normally")
				return
			}
			select {
			case <-pw.done:
				return
			default:
				log.Printf("[PrivateWS] Read error: %v", err)
				pw.handleDisconnection()
			}
			return
		}

		// Handle OKX "ping" text message
		if string(message) == "ping" {
			pw.writeMu.Lock()
			if pw.conn != nil {
				_ = pw.conn.WriteMessage(websocket.TextMessage, []byte("pong"))
			}
			pw.writeMu.Unlock()
			continue
		}

		// Handle OKX "pong" text response to our "ping" text message
		if string(message) == "pong" {
			pw.lastPongMu.Lock()
			pw.lastPong = time.Now()
			pw.lastPongMu.Unlock()
			continue
		}

		// Parse and handle order pushes
		pw.handleMessage(message)
	}
}

// handleMessage parses incoming private WS messages and dispatches fill callbacks.
func (pw *PrivateWSClient) handleMessage(data []byte) {
	var push OKXOrderPush
	if err := json.Unmarshal(data, &push); err != nil {
		// Not a data push (could be event response), ignore
		return
	}

	// Check if this is an orders channel push
	if push.Arg.Channel != "orders" {
		return
	}

	pw.callbackMu.RLock()
	cb := pw.fillCallback
	pw.callbackMu.RUnlock()

	if cb == nil {
		return
	}

	for _, orderData := range push.Data {
		// Only trigger callback for fills
		if orderData.State == "filled" || orderData.State == "partially_filled" {
			cb(orderData.InstID, orderData.Side, orderData.FillPx, orderData.FillSz, orderData.OrdID, orderData.State)
		}
	}
}

// heartbeatLoop sends periodic pings and checks for pong responses.
func (pw *PrivateWSClient) heartbeatLoop() {
	defer pw.wg.Done()
	ticker := time.NewTicker(pw.config.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pw.done:
			return
		case <-ticker.C:
			if !pw.IsConnected() {
				continue
			}

			pw.lastPongMu.RLock()
			lastPong := pw.lastPong
			pw.lastPongMu.RUnlock()

			if time.Since(lastPong) > pw.config.HeartbeatTimeout {
				log.Printf("[PrivateWS] Heartbeat timeout: no pong for %v", time.Since(lastPong))
				pw.handleDisconnection()
				return
			}

			// Send ping as text message (OKX private WS expects "ping" text)
			pw.writeMu.Lock()
			if pw.conn != nil {
				err := pw.conn.WriteMessage(websocket.TextMessage, []byte("ping"))
				if err != nil {
					pw.writeMu.Unlock()
					log.Printf("[PrivateWS] Failed to send ping: %v", err)
					pw.handleDisconnection()
					return
				}
			}
			pw.writeMu.Unlock()
		}
	}
}

// reconnectLoop handles reconnection attempts with exponential backoff.
func (pw *PrivateWSClient) reconnectLoop() {
	defer pw.wg.Done()
	for {
		select {
		case <-pw.done:
			return
		case <-pw.reconnectCh:
			pw.performReconnect()
		}
	}
}

// handleDisconnection is called when a disconnection is detected.
func (pw *PrivateWSClient) handleDisconnection() {
	pw.stateMu.Lock()
	if !pw.connected {
		pw.stateMu.Unlock()
		return
	}
	pw.connected = false
	pw.stateMu.Unlock()

	log.Printf("[PrivateWS] Connection lost, initiating reconnection")
	pw.closeConn()

	select {
	case pw.reconnectCh <- struct{}{}:
	default:
	}
}

// performReconnect attempts to reconnect with exponential backoff.
func (pw *PrivateWSClient) performReconnect() {
	pw.reconnectMu.Lock()
	pw.reconnectCount = 0
	pw.reconnectMu.Unlock()

	delay := pw.config.InitialReconnectDelay

	for {
		select {
		case <-pw.done:
			return
		default:
		}

		pw.reconnectMu.Lock()
		attempt := pw.reconnectCount
		pw.reconnectCount++
		pw.reconnectMu.Unlock()

		if attempt >= pw.config.MaxReconnectAttempts {
			log.Printf("[PrivateWS] Max reconnection attempts (%d) exhausted", pw.config.MaxReconnectAttempts)
			return
		}

		log.Printf("[PrivateWS] Reconnection attempt %d/%d (delay: %v)", attempt+1, pw.config.MaxReconnectAttempts, delay)

		select {
		case <-pw.done:
			return
		case <-time.After(delay):
		}

		// Try to connect
		if err := pw.connect(); err != nil {
			log.Printf("[PrivateWS] Reconnection attempt %d failed (connect): %v", attempt+1, err)
			delay = pw.calculateBackoff(attempt + 1)
			continue
		}

		// Authenticate
		if err := pw.authenticate(); err != nil {
			log.Printf("[PrivateWS] Reconnection attempt %d failed (auth): %v", attempt+1, err)
			pw.closeConn()
			delay = pw.calculateBackoff(attempt + 1)
			continue
		}

		// Subscribe
		if err := pw.subscribeOrders(); err != nil {
			log.Printf("[PrivateWS] Reconnection attempt %d failed (subscribe): %v", attempt+1, err)
			pw.closeConn()
			delay = pw.calculateBackoff(attempt + 1)
			continue
		}

		// Success
		log.Printf("[PrivateWS] Reconnection successful after %d attempts", attempt+1)
		pw.stateMu.Lock()
		pw.connected = true
		pw.stateMu.Unlock()

		// Restart read and heartbeat loops
		pw.wg.Add(2)
		go pw.readLoop()
		go pw.heartbeatLoop()
		return
	}
}

// calculateBackoff computes exponential backoff delay.
func (pw *PrivateWSClient) calculateBackoff(attempt int) time.Duration {
	backoff := float64(pw.config.InitialReconnectDelay) * math.Pow(2, float64(attempt))
	if backoff > float64(pw.config.MaxReconnectDelay) {
		backoff = float64(pw.config.MaxReconnectDelay)
	}
	return time.Duration(backoff)
}
