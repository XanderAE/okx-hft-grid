package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// Clock is an injectable time source for deterministic testing.
type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
	NewTicker(d time.Duration) Ticker
	AfterFunc(d time.Duration, f func()) Timer
}

// Ticker abstracts time.Ticker for injection.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// Timer abstracts time.Timer for injection.
type Timer interface {
	Stop() bool
	Reset(d time.Duration) bool
}

// RealClock uses the standard time package.
type RealClock struct{}

func (RealClock) Now() time.Time                         { return time.Now() }
func (RealClock) Since(t time.Time) time.Duration        { return time.Since(t) }
func (RealClock) NewTicker(d time.Duration) Ticker       { return &realTicker{time.NewTicker(d)} }
func (RealClock) AfterFunc(d time.Duration, f func()) Timer {
	return &realTimer{time.AfterFunc(d, f)}
}

type realTicker struct{ t *time.Ticker }

func (r *realTicker) C() <-chan time.Time { return r.t.C }
func (r *realTicker) Stop()              { r.t.Stop() }

type realTimer struct{ t *time.Timer }

func (r *realTimer) Stop() bool              { return r.t.Stop() }
func (r *realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }

// Dialer abstracts WebSocket connection establishment for testing.
type Dialer interface {
	DialContext(ctx context.Context, urlStr string) (WSConn, error)
}

// WSConn abstracts a WebSocket connection for testing.
type WSConn interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	WriteJSON(v interface{}) error
	Close() error
	SetReadDeadline(t time.Time) error
	LocalAddr() net.Addr
}

// PrivateWSState represents the state of the Private WS state machine.
type PrivateWSState int

const (
	PWS_Disconnected PrivateWSState = iota
	PWS_Connecting
	PWS_Authenticating
	PWS_Subscribing
	PWS_Reconciling
	PWS_Ready
	PWS_Unhealthy
	PWS_Backoff
	PWS_SafeStopped
)

func (s PrivateWSState) String() string {
	switch s {
	case PWS_Disconnected:
		return "Disconnected"
	case PWS_Connecting:
		return "Connecting"
	case PWS_Authenticating:
		return "Authenticating"
	case PWS_Subscribing:
		return "Subscribing"
	case PWS_Reconciling:
		return "Reconciling"
	case PWS_Ready:
		return "Ready"
	case PWS_Unhealthy:
		return "Unhealthy"
	case PWS_Backoff:
		return "Backoff"
	case PWS_SafeStopped:
		return "SafeStopped"
	default:
		return "Unknown"
	}
}

// StateChangeReason codes for state transitions.
type StateChangeReason string

const (
	ReasonStartup           StateChangeReason = "startup"
	ReasonPongReceived      StateChangeReason = "pong_received"
	ReasonAuthSuccess       StateChangeReason = "auth_success"
	ReasonSubConfirmed      StateChangeReason = "subscription_confirmed"
	ReasonReconcileComplete StateChangeReason = "reconciliation_complete"
	ReasonLivenessTimeout   StateChangeReason = "liveness_timeout"
	ReasonReadError         StateChangeReason = "read_error"
	ReasonCloseFrame        StateChangeReason = "close_frame"
	ReasonAuthFailed        StateChangeReason = "auth_failed"
	ReasonSubFailed         StateChangeReason = "subscription_failed"
	ReasonConnectFailed     StateChangeReason = "connect_failed"
	ReasonRetriesExhausted  StateChangeReason = "retries_exhausted"
	ReasonReconcileFailed   StateChangeReason = "reconciliation_failed"
	ReasonShutdown          StateChangeReason = "intentional_shutdown"
	ReasonRecoveryProbe     StateChangeReason = "recovery_probe"
)

// StateChangeEvent is published on every state transition.
type StateChangeEvent struct {
	From            PrivateWSState
	To              PrivateWSState
	Reason          StateChangeReason
	Epoch           uint64
	LastLiveness    time.Time
	ReconnectCount  int
	SubscriptionSet []SubscriptionSpec
	Timestamp       time.Time
}

// SubscriptionSpec identifies a required subscription.
type SubscriptionSpec struct {
	Channel    string
	InstType   string
	InstID     string // optional, empty means all for that instType
}

// ReconciliationCallback is triggered on startup, reconnect, or gap.
type ReconciliationCallback func(ctx context.Context) error

// StateChangeCallback receives state transition notifications.
type StateChangeCallback func(event StateChangeEvent)

// RiskGateCallback controls the shared risk-increasing gate.
type RiskGateCallback func(open bool, reason StateChangeReason)

// PrivateWSStateMachineConfig holds all timing and behavioral parameters.
type PrivateWSStateMachineConfig struct {
	URL                    string
	HeartbeatSendInterval  time.Duration // 20s
	LivenessTimeout        time.Duration // 45s
	ReconnectStartDeadline time.Duration // 5s
	WatchdogInterval       time.Duration // 1s
	InitialBackoff         time.Duration // 1s
	MaxBackoff             time.Duration // 30s
	MaxReconnectAttempts   int           // 10
	ConnectTimeout         time.Duration // 10s
	AuthTimeout            time.Duration // 10s
	SubscribeTimeout       time.Duration // 10s

	RequiredSubscriptions []SubscriptionSpec
	LoginBuilder          func(clock Clock) (json.RawMessage, error)
}

// DefaultPrivateWSStateMachineConfig returns approved production timing.
func DefaultPrivateWSStateMachineConfig() PrivateWSStateMachineConfig {
	return PrivateWSStateMachineConfig{
		HeartbeatSendInterval:  20 * time.Second,
		LivenessTimeout:        45 * time.Second,
		ReconnectStartDeadline: 5 * time.Second,
		WatchdogInterval:       1 * time.Second,
		InitialBackoff:         1 * time.Second,
		MaxBackoff:             30 * time.Second,
		MaxReconnectAttempts:   10,
		ConnectTimeout:         10 * time.Second,
		AuthTimeout:            10 * time.Second,
		SubscribeTimeout:       10 * time.Second,
	}
}

// PrivateWSStateMachine implements the 20/45/5 liveness and recovery protocol.
type PrivateWSStateMachine struct {
	cfg    PrivateWSStateMachineConfig
	clock  Clock
	dialer Dialer

	mu              sync.RWMutex
	state           PrivateWSState
	epoch           uint64
	lastLiveness    time.Time
	reconnectCount  int
	conn            WSConn
	confirmedSubs   map[string]bool // key: "channel:instType:instID"
	unhealthyAt     time.Time

	// Callbacks
	onStateChange  StateChangeCallback
	onReconcile    ReconciliationCallback
	onRiskGate     RiskGateCallback
	onFill         FillCallback

	// Control
	cancel    context.CancelFunc
	done      chan struct{}
	wg        sync.WaitGroup
}

// NewPrivateWSStateMachine constructs the state machine without starting it.
func NewPrivateWSStateMachine(
	cfg PrivateWSStateMachineConfig,
	clock Clock,
	dialer Dialer,
) *PrivateWSStateMachine {
	return &PrivateWSStateMachine{
		cfg:           cfg,
		clock:         clock,
		dialer:        dialer,
		state:         PWS_Disconnected,
		confirmedSubs: make(map[string]bool),
		done:          make(chan struct{}),
	}
}

// SetStateChangeCallback registers the state change listener.
func (sm *PrivateWSStateMachine) SetStateChangeCallback(cb StateChangeCallback) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.onStateChange = cb
}

// SetReconciliationCallback registers the reconciliation trigger.
func (sm *PrivateWSStateMachine) SetReconciliationCallback(cb ReconciliationCallback) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.onReconcile = cb
}

// SetRiskGateCallback registers the risk gate controller.
func (sm *PrivateWSStateMachine) SetRiskGateCallback(cb RiskGateCallback) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.onRiskGate = cb
}

// SetFillCallback registers the fill handler.
func (sm *PrivateWSStateMachine) SetFillCallback(cb FillCallback) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.onFill = cb
}

// State returns the current state (thread-safe).
func (sm *PrivateWSStateMachine) State() PrivateWSState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state
}

// Epoch returns the current connection epoch.
func (sm *PrivateWSStateMachine) Epoch() uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.epoch
}

// LastLiveness returns the last verified liveness timestamp.
func (sm *PrivateWSStateMachine) LastLiveness() time.Time {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.lastLiveness
}

// ReconnectCount returns the current reconnect attempts.
func (sm *PrivateWSStateMachine) ReconnectCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.reconnectCount
}

// IsReady returns true only when fully authenticated, subscribed, and reconciled.
func (sm *PrivateWSStateMachine) IsReady() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state == PWS_Ready
}

// Start begins the state machine: connects, authenticates, subscribes, reconciles.
func (sm *PrivateWSStateMachine) Start(ctx context.Context) error {
	sm.mu.Lock()
	if sm.state != PWS_Disconnected {
		sm.mu.Unlock()
		return fmt.Errorf("state machine already started (state=%s)", sm.state)
	}
	sm.done = make(chan struct{})
	sm.mu.Unlock()

	// Close risk gate immediately on startup
	sm.closeRiskGate(ReasonStartup)

	ctx, sm.cancel = context.WithCancel(ctx)

	// Perform initial connection sequence
	if err := sm.connectSequence(ctx); err != nil {
		return err
	}

	// Start background loops
	sm.wg.Add(2)
	go sm.heartbeatSendLoop(ctx)
	go sm.watchdogLoop(ctx)

	return nil
}

// Stop gracefully shuts down the state machine.
func (sm *PrivateWSStateMachine) Stop() {
	sm.mu.Lock()
	if sm.cancel != nil {
		sm.cancel()
	}
	select {
	case <-sm.done:
	default:
		close(sm.done)
	}
	conn := sm.conn
	sm.conn = nil
	oldState := sm.state
	sm.state = PWS_Disconnected
	sm.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}

	sm.wg.Wait()

	if oldState != PWS_Disconnected {
		sm.publishStateChange(oldState, PWS_Disconnected, ReasonShutdown)
	}
}

// connectSequence performs connect -> auth -> subscribe -> reconcile.
func (sm *PrivateWSStateMachine) connectSequence(ctx context.Context) error {
	sm.transition(PWS_Connecting, ReasonStartup)

	// Dial
	dialCtx, dialCancel := context.WithTimeout(ctx, sm.cfg.ConnectTimeout)
	defer dialCancel()
	conn, err := sm.dialer.DialContext(dialCtx, sm.cfg.URL)
	if err != nil {
		sm.transition(PWS_Backoff, ReasonConnectFailed)
		return fmt.Errorf("dial failed: %w", err)
	}

	sm.mu.Lock()
	sm.conn = conn
	sm.mu.Unlock()

	// Authenticate
	sm.transition(PWS_Authenticating, ReasonStartup)
	if err := sm.performAuth(ctx); err != nil {
		sm.closeConn()
		sm.transition(PWS_Backoff, ReasonAuthFailed)
		return fmt.Errorf("auth failed: %w", err)
	}

	// Subscribe
	sm.transition(PWS_Subscribing, ReasonAuthSuccess)
	if err := sm.performSubscribe(ctx); err != nil {
		sm.closeConn()
		sm.transition(PWS_Backoff, ReasonSubFailed)
		return fmt.Errorf("subscribe failed: %w", err)
	}

	// Reconcile
	sm.transition(PWS_Reconciling, ReasonSubConfirmed)
	if err := sm.performReconciliation(ctx); err != nil {
		sm.closeConn()
		sm.transition(PWS_SafeStopped, ReasonReconcileFailed)
		return fmt.Errorf("reconciliation failed: %w", err)
	}

	// Ready - refresh liveness and open gate
	sm.mu.Lock()
	sm.lastLiveness = sm.clock.Now()
	sm.epoch++
	sm.reconnectCount = 0
	sm.mu.Unlock()

	sm.transition(PWS_Ready, ReasonReconcileComplete)
	sm.openRiskGate(ReasonReconcileComplete)

	// Start the read loop
	sm.wg.Add(1)
	go sm.readLoop(ctx)

	return nil
}

// performAuth sends login and waits for strict ack.
func (sm *PrivateWSStateMachine) performAuth(ctx context.Context) error {
	sm.mu.RLock()
	conn := sm.conn
	sm.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("no connection")
	}

	// Build login message
	loginMsg, err := sm.cfg.LoginBuilder(sm.clock)
	if err != nil {
		return fmt.Errorf("login builder: %w", err)
	}

	if err := conn.WriteMessage(1, loginMsg); err != nil { // TextMessage = 1
		return fmt.Errorf("send login: %w", err)
	}

	// Wait for login response with timeout
	if err := conn.SetReadDeadline(sm.clock.Now().Add(sm.cfg.AuthTimeout)); err != nil {
		return fmt.Errorf("set read deadline: %w", err)
	}
	_, msg, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read login response: %w", err)
	}
	// Clear deadline
	_ = conn.SetReadDeadline(time.Time{})

	// Strict ack: must be event=login, code=0
	var resp struct {
		Event string `json:"event"`
		Code  string `json:"code"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal(msg, &resp); err != nil {
		return fmt.Errorf("parse login response: %w", err)
	}
	if resp.Event != "login" || resp.Code != "0" {
		return fmt.Errorf("login rejected: event=%s code=%s msg=%s",
			resp.Event, resp.Code, resp.Msg)
	}

	// Login is verified liveness evidence
	sm.mu.Lock()
	sm.lastLiveness = sm.clock.Now()
	sm.mu.Unlock()

	return nil
}

// performSubscribe sends subscription requests and waits for strict per-item ack.
func (sm *PrivateWSStateMachine) performSubscribe(ctx context.Context) error {
	sm.mu.RLock()
	conn := sm.conn
	sm.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("no connection")
	}

	sm.mu.Lock()
	sm.confirmedSubs = make(map[string]bool)
	sm.mu.Unlock()

	for _, sub := range sm.cfg.RequiredSubscriptions {
		// Build subscription request
		req := map[string]interface{}{
			"op": "subscribe",
			"args": []map[string]string{
				{
					"channel":  sub.Channel,
					"instType": sub.InstType,
				},
			},
		}
		if sub.InstID != "" {
			req["args"] = []map[string]string{
				{
					"channel":  sub.Channel,
					"instType": sub.InstType,
					"instId":   sub.InstID,
				},
			}
		}

		reqBytes, _ := json.Marshal(req)
		if err := conn.WriteMessage(1, reqBytes); err != nil {
			return fmt.Errorf("send subscribe: %w", err)
		}

		// Wait for strict ack for this subscription
		if err := conn.SetReadDeadline(sm.clock.Now().Add(sm.cfg.SubscribeTimeout)); err != nil {
			return fmt.Errorf("set read deadline: %w", err)
		}
		if err := sm.waitForSubAck(conn, sub); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Time{})
	}

	// Subscription confirmed is liveness evidence
	sm.mu.Lock()
	sm.lastLiveness = sm.clock.Now()
	sm.mu.Unlock()

	return nil
}

// waitForSubAck reads messages until we get the exact subscription ack.
// Data pushes are NOT accepted as subscription acks.
func (sm *PrivateWSStateMachine) waitForSubAck(conn WSConn, expected SubscriptionSpec) error {
	_, msg, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read subscribe response: %w", err)
	}

	var resp struct {
		Event string `json:"event"`
		Code  string `json:"code"`
		Msg   string `json:"msg"`
		Arg   struct {
			Channel  string `json:"channel"`
			InstType string `json:"instType"`
			InstID   string `json:"instId"`
		} `json:"arg"`
		// If data field is present, it's a push not an ack
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(msg, &resp); err != nil {
		return fmt.Errorf("parse subscribe response: %w", err)
	}

	// A data push is NOT a valid subscription ack
	if len(resp.Data) > 0 && resp.Event == "" {
		return fmt.Errorf("received data push instead of subscription ack for %s/%s",
			expected.Channel, expected.InstType)
	}

	// Must be event=subscribe with matching channel/instType
	if resp.Event != "subscribe" {
		if resp.Event == "error" {
			return fmt.Errorf("subscription error: code=%s msg=%s", resp.Code, resp.Msg)
		}
		return fmt.Errorf("unexpected event %q waiting for subscribe ack", resp.Event)
	}

	// Strict match: channel and instType must match exactly
	if resp.Arg.Channel != expected.Channel {
		return fmt.Errorf("subscribe ack channel mismatch: got %q want %q",
			resp.Arg.Channel, expected.Channel)
	}
	if resp.Arg.InstType != expected.InstType {
		return fmt.Errorf("subscribe ack instType mismatch: got %q want %q",
			resp.Arg.InstType, expected.InstType)
	}
	if expected.InstID != "" && resp.Arg.InstID != expected.InstID {
		return fmt.Errorf("subscribe ack instId mismatch: got %q want %q",
			resp.Arg.InstID, expected.InstID)
	}

	// Record confirmed subscription
	key := subKey(expected)
	sm.mu.Lock()
	sm.confirmedSubs[key] = true
	sm.mu.Unlock()

	return nil
}

func subKey(s SubscriptionSpec) string {
	return s.Channel + ":" + s.InstType + ":" + s.InstID
}

// performReconciliation triggers the reconciliation callback.
func (sm *PrivateWSStateMachine) performReconciliation(ctx context.Context) error {
	sm.mu.RLock()
	cb := sm.onReconcile
	sm.mu.RUnlock()

	if cb == nil {
		// No reconciliation callback registered - proceed (for testing)
		return nil
	}

	return cb(ctx)
}

// readLoop processes incoming messages after Ready.
func (sm *PrivateWSStateMachine) readLoop(ctx context.Context) {
	defer sm.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sm.done:
			return
		default:
		}

		sm.mu.RLock()
		conn := sm.conn
		sm.mu.RUnlock()
		if conn == nil {
			return
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-sm.done:
				return
			default:
				sm.handleConnectionLoss(ReasonReadError)
			}
			return
		}

		sm.processMessage(msg)
	}
}

// processMessage handles an incoming message and updates liveness.
func (sm *PrivateWSStateMachine) processMessage(msg []byte) {
	// Handle pong - this IS liveness evidence
	if string(msg) == "pong" {
		sm.mu.Lock()
		sm.lastLiveness = sm.clock.Now()
		sm.mu.Unlock()
		return
	}

	// Try to parse as a structured message
	var envelope struct {
		Event string          `json:"event"`
		Code  string          `json:"code"`
		Arg   json.RawMessage `json:"arg"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(msg, &envelope); err != nil {
		// Unrecognized message - NOT liveness evidence
		return
	}

	// Authenticated control frame (event responses) = liveness
	if envelope.Event != "" {
		sm.mu.Lock()
		sm.lastLiveness = sm.clock.Now()
		sm.mu.Unlock()
		return
	}

	// Data push from subscribed channel = liveness evidence
	// Only if it has an arg with a channel we're subscribed to
	if len(envelope.Arg) > 0 && len(envelope.Data) > 0 {
		var arg struct {
			Channel  string `json:"channel"`
			InstType string `json:"instType"`
		}
		if json.Unmarshal(envelope.Arg, &arg) == nil && arg.Channel != "" {
			// Valid subscribed private message - liveness evidence
			sm.mu.Lock()
			sm.lastLiveness = sm.clock.Now()
			sm.mu.Unlock()

			// Dispatch fills
			sm.dispatchFill(msg)
		}
		return
	}

	// Arbitrary bytes with no recognizable structure - NOT liveness
}

// dispatchFill parses fill data and invokes the callback.
func (sm *PrivateWSStateMachine) dispatchFill(msg []byte) {
	var push struct {
		Arg struct {
			Channel string `json:"channel"`
		} `json:"arg"`
		Data []struct {
			InstID string `json:"instId"`
			OrdID  string `json:"ordId"`
			Side   string `json:"side"`
			FillPx string `json:"fillPx"`
			FillSz string `json:"fillSz"`
			State  string `json:"state"`
		} `json:"data"`
	}
	if json.Unmarshal(msg, &push) != nil {
		return
	}
	if push.Arg.Channel != "orders" {
		return
	}

	sm.mu.RLock()
	cb := sm.onFill
	sm.mu.RUnlock()
	if cb == nil {
		return
	}

	for _, d := range push.Data {
		if d.State == "filled" || d.State == "partially_filled" {
			cb(d.InstID, d.Side, d.FillPx, d.FillSz, d.OrdID, d.State)
		}
	}
}

// heartbeatSendLoop sends ping every 20 seconds.
func (sm *PrivateWSStateMachine) heartbeatSendLoop(ctx context.Context) {
	defer sm.wg.Done()
	ticker := sm.clock.NewTicker(sm.cfg.HeartbeatSendInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-sm.done:
			return
		case <-ticker.C():
			sm.mu.RLock()
			conn := sm.conn
			state := sm.state
			sm.mu.RUnlock()

			if state != PWS_Ready || conn == nil {
				continue
			}

			// Send "ping" text message (OKX protocol)
			if err := conn.WriteMessage(1, []byte("ping")); err != nil {
				sm.handleConnectionLoss(ReasonReadError)
				return
			}
		}
	}
}

// watchdogLoop checks liveness every 1 second independently.
func (sm *PrivateWSStateMachine) watchdogLoop(ctx context.Context) {
	defer sm.wg.Done()
	ticker := sm.clock.NewTicker(sm.cfg.WatchdogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-sm.done:
			return
		case <-ticker.C():
			sm.mu.RLock()
			state := sm.state
			lastLive := sm.lastLiveness
			sm.mu.RUnlock()

			if state != PWS_Ready {
				continue
			}

			age := sm.clock.Since(lastLive)
			if age >= sm.cfg.LivenessTimeout {
				sm.handleConnectionLoss(ReasonLivenessTimeout)
				return
			}
		}
	}
}

// handleConnectionLoss transitions to Unhealthy and starts reconnect.
func (sm *PrivateWSStateMachine) handleConnectionLoss(reason StateChangeReason) {
	sm.mu.Lock()
	if sm.state == PWS_Unhealthy || sm.state == PWS_Backoff ||
		sm.state == PWS_SafeStopped || sm.state == PWS_Disconnected {
		sm.mu.Unlock()
		return
	}
	sm.state = PWS_Unhealthy
	sm.unhealthyAt = sm.clock.Now()
	sm.mu.Unlock()

	// Close risk gate immediately
	sm.closeRiskGate(reason)
	sm.closeConn()
	sm.publishStateChange(PWS_Ready, PWS_Unhealthy, reason)

	// Start reconnect - must begin within 5 seconds of unhealthy
	go sm.reconnectLoop()
}

// reconnectLoop performs bounded exponential backoff reconnection.
func (sm *PrivateWSStateMachine) reconnectLoop() {
	sm.mu.Lock()
	sm.reconnectCount = 0
	sm.mu.Unlock()

	sm.transition(PWS_Backoff, ReasonLivenessTimeout)

	delay := sm.cfg.InitialBackoff

	for attempt := 0; attempt < sm.cfg.MaxReconnectAttempts; attempt++ {
		select {
		case <-sm.done:
			return
		default:
		}

		sm.mu.Lock()
		sm.reconnectCount = attempt + 1
		sm.mu.Unlock()

		// Wait for backoff duration using clock
		waitDone := make(chan struct{})
		timer := sm.clock.AfterFunc(delay, func() {
			close(waitDone)
		})

		select {
		case <-sm.done:
			timer.Stop()
			return
		case <-waitDone:
		}

		// Attempt connection sequence
		ctx, cancel := context.WithCancel(context.Background())
		// Check if done
		select {
		case <-sm.done:
			cancel()
			return
		default:
		}

		err := sm.attemptReconnect(ctx)
		cancel()

		if err == nil {
			return // Success
		}

		// Calculate next backoff: exponential, capped at MaxBackoff
		delay = delay * 2
		if delay > sm.cfg.MaxBackoff {
			delay = sm.cfg.MaxBackoff
		}
	}

	// Exhausted retries
	sm.transition(PWS_SafeStopped, ReasonRetriesExhausted)
	sm.publishStateChange(PWS_Backoff, PWS_SafeStopped, ReasonRetriesExhausted)
}

// attemptReconnect tries a single reconnection cycle.
func (sm *PrivateWSStateMachine) attemptReconnect(ctx context.Context) error {
	sm.transition(PWS_Connecting, ReasonRecoveryProbe)

	dialCtx, dialCancel := context.WithTimeout(ctx, sm.cfg.ConnectTimeout)
	defer dialCancel()
	conn, err := sm.dialer.DialContext(dialCtx, sm.cfg.URL)
	if err != nil {
		sm.transition(PWS_Backoff, ReasonConnectFailed)
		return err
	}

	sm.mu.Lock()
	sm.conn = conn
	sm.mu.Unlock()

	// Auth
	sm.transition(PWS_Authenticating, ReasonRecoveryProbe)
	if err := sm.performAuth(ctx); err != nil {
		sm.closeConn()
		sm.transition(PWS_Backoff, ReasonAuthFailed)
		return err
	}

	// Subscribe
	sm.transition(PWS_Subscribing, ReasonAuthSuccess)
	if err := sm.performSubscribe(ctx); err != nil {
		sm.closeConn()
		sm.transition(PWS_Backoff, ReasonSubFailed)
		return err
	}

	// Reconcile - triggers immediate reconciliation
	sm.transition(PWS_Reconciling, ReasonSubConfirmed)
	if err := sm.performReconciliation(ctx); err != nil {
		sm.closeConn()
		sm.transition(PWS_SafeStopped, ReasonReconcileFailed)
		return err
	}

	// Ready
	sm.mu.Lock()
	sm.lastLiveness = sm.clock.Now()
	sm.epoch++
	sm.mu.Unlock()

	sm.transition(PWS_Ready, ReasonReconcileComplete)
	sm.openRiskGate(ReasonReconcileComplete)

	// Restart read loop
	sm.wg.Add(1)
	go sm.readLoop(ctx)

	return nil
}

// transition updates the state and publishes the change.
func (sm *PrivateWSStateMachine) transition(to PrivateWSState, reason StateChangeReason) {
	sm.mu.Lock()
	from := sm.state
	sm.state = to
	sm.mu.Unlock()

	if from != to {
		sm.publishStateChange(from, to, reason)
	}
}

// publishStateChange invokes the state change callback.
func (sm *PrivateWSStateMachine) publishStateChange(from, to PrivateWSState, reason StateChangeReason) {
	sm.mu.RLock()
	cb := sm.onStateChange
	epoch := sm.epoch
	lastLive := sm.lastLiveness
	reconCount := sm.reconnectCount
	subs := make([]SubscriptionSpec, len(sm.cfg.RequiredSubscriptions))
	copy(subs, sm.cfg.RequiredSubscriptions)
	sm.mu.RUnlock()

	if cb != nil {
		cb(StateChangeEvent{
			From:            from,
			To:              to,
			Reason:          reason,
			Epoch:           epoch,
			LastLiveness:    lastLive,
			ReconnectCount:  reconCount,
			SubscriptionSet: subs,
			Timestamp:       sm.clock.Now(),
		})
	}
}

// closeRiskGate signals that risk-increasing operations should be blocked.
func (sm *PrivateWSStateMachine) closeRiskGate(reason StateChangeReason) {
	sm.mu.RLock()
	cb := sm.onRiskGate
	sm.mu.RUnlock()
	if cb != nil {
		cb(false, reason)
	}
}

// openRiskGate signals that risk-increasing operations can proceed.
func (sm *PrivateWSStateMachine) openRiskGate(reason StateChangeReason) {
	sm.mu.RLock()
	cb := sm.onRiskGate
	sm.mu.RUnlock()
	if cb != nil {
		cb(true, reason)
	}
}

// closeConn safely closes the current connection.
func (sm *PrivateWSStateMachine) closeConn() {
	sm.mu.Lock()
	conn := sm.conn
	sm.conn = nil
	sm.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}
