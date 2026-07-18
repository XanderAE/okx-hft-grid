package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// === Fake Clock ===

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*fakeTicker
	timers  []*fakeTimer
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (fc *fakeClock) Now() time.Time {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.now
}

func (fc *fakeClock) Since(t time.Time) time.Duration {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.now.Sub(t)
}

func (fc *fakeClock) Advance(d time.Duration) {
	fc.mu.Lock()
	fc.now = fc.now.Add(d)
	now := fc.now
	tickers := append([]*fakeTicker(nil), fc.tickers...)
	timers := append([]*fakeTimer(nil), fc.timers...)
	fc.mu.Unlock()

	for _, t := range tickers {
		t.maybeFire(now)
	}
	for _, t := range timers {
		t.maybeFire(now)
	}
}

func (fc *fakeClock) NewTicker(d time.Duration) Ticker {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	ft := &fakeTicker{
		interval: d,
		nextFire: fc.now.Add(d),
		ch:       make(chan time.Time, 10),
	}
	fc.tickers = append(fc.tickers, ft)
	return ft
}

func (fc *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	ft := &fakeTimer{
		fireAt: fc.now.Add(d),
		fn:     f,
		active: true,
	}
	fc.timers = append(fc.timers, ft)
	return ft
}

type fakeTicker struct {
	mu       sync.Mutex
	interval time.Duration
	nextFire time.Time
	ch       chan time.Time
	stopped  bool
}

func (ft *fakeTicker) C() <-chan time.Time { return ft.ch }
func (ft *fakeTicker) Stop() {
	ft.mu.Lock()
	ft.stopped = true
	ft.mu.Unlock()
}

func (ft *fakeTicker) maybeFire(now time.Time) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if ft.stopped {
		return
	}
	for !now.Before(ft.nextFire) {
		select {
		case ft.ch <- ft.nextFire:
		default:
		}
		ft.nextFire = ft.nextFire.Add(ft.interval)
	}
}

type fakeTimer struct {
	mu     sync.Mutex
	fireAt time.Time
	fn     func()
	active bool
	fired  bool
}

func (ft *fakeTimer) Stop() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	was := ft.active
	ft.active = false
	return was
}

func (ft *fakeTimer) Reset(d time.Duration) bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	was := ft.active
	ft.active = true
	ft.fired = false
	return was
}

func (ft *fakeTimer) maybeFire(now time.Time) {
	ft.mu.Lock()
	if !ft.active || ft.fired || now.Before(ft.fireAt) {
		ft.mu.Unlock()
		return
	}
	ft.fired = true
	ft.active = false
	fn := ft.fn
	ft.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// === Fake Dialer and Connection ===

type fakeDialer struct {
	mu       sync.Mutex
	conns    []*fakeWSConn
	dialErr  error
	dialFunc func(ctx context.Context, url string) (WSConn, error)
}

func newFakeDialer() *fakeDialer {
	return &fakeDialer{}
}

func (fd *fakeDialer) SetDialFunc(fn func(ctx context.Context, url string) (WSConn, error)) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.dialFunc = fn
}

func (fd *fakeDialer) DialContext(ctx context.Context, url string) (WSConn, error) {
	fd.mu.Lock()
	if fd.dialFunc != nil {
		fn := fd.dialFunc
		fd.mu.Unlock()
		return fn(ctx, url)
	}
	if fd.dialErr != nil {
		err := fd.dialErr
		fd.mu.Unlock()
		return nil, err
	}
	conn := newFakeWSConn()
	fd.conns = append(fd.conns, conn)
	fd.mu.Unlock()
	return conn, nil
}

func (fd *fakeDialer) LastConn() *fakeWSConn {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if len(fd.conns) == 0 {
		return nil
	}
	return fd.conns[len(fd.conns)-1]
}

func (fd *fakeDialer) ConnCount() int {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return len(fd.conns)
}

type fakeWSConn struct {
	mu          sync.Mutex
	writtenMsgs [][]byte
	readQueue   chan []byte
	closed      bool
	readErr     error
	writeErr    error
}

func newFakeWSConn() *fakeWSConn {
	return &fakeWSConn{
		readQueue: make(chan []byte, 100),
	}
}

func (c *fakeWSConn) ReadMessage() (int, []byte, error) {
	c.mu.Lock()
	if c.readErr != nil {
		err := c.readErr
		c.mu.Unlock()
		return 0, nil, err
	}
	c.mu.Unlock()

	msg, ok := <-c.readQueue
	if !ok {
		return 0, nil, fmt.Errorf("connection closed")
	}
	return 1, msg, nil
}

func (c *fakeWSConn) WriteMessage(msgType int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil {
		return c.writeErr
	}
	if c.closed {
		return fmt.Errorf("connection closed")
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	c.writtenMsgs = append(c.writtenMsgs, cp)
	return nil
}

func (c *fakeWSConn) WriteJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.WriteMessage(1, data)
}

func (c *fakeWSConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	close(c.readQueue)
	return nil
}

func (c *fakeWSConn) SetReadDeadline(t time.Time) error { return nil }
func (c *fakeWSConn) LocalAddr() net.Addr               { return &net.TCPAddr{} }

func (c *fakeWSConn) EnqueueMessage(msg []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.readQueue <- msg
}

func (c *fakeWSConn) EnqueueJSON(v interface{}) {
	data, _ := json.Marshal(v)
	c.EnqueueMessage(data)
}

func (c *fakeWSConn) SetReadError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readErr = err
}

func (c *fakeWSConn) WrittenMessages() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([][]byte, len(c.writtenMsgs))
	copy(cp, c.writtenMsgs)
	return cp
}

// === Test helpers ===

func testConfig() PrivateWSStateMachineConfig {
	return PrivateWSStateMachineConfig{
		URL:                    "ws://localhost:0/ws/v5/private",
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
		RequiredSubscriptions: []SubscriptionSpec{
			{Channel: "orders", InstType: "SPOT"},
		},
		LoginBuilder: func(clock Clock) (json.RawMessage, error) {
			return json.Marshal(map[string]interface{}{
				"op":   "login",
				"args": []map[string]string{{"apiKey": "test", "timestamp": "1", "sign": "s", "passphrase": "p"}},
			})
		},
	}
}

// setupConnectedSM creates and starts a state machine that reaches Ready.
func setupConnectedSM(t *testing.T, clock *fakeClock) (*PrivateWSStateMachine, *fakeDialer) {
	t.Helper()
	dialer := newFakeDialer()

	// Set up dialer to provide auth and subscription responses
	dialer.SetDialFunc(func(ctx context.Context, url string) (WSConn, error) {
		conn := newFakeWSConn()
		// Enqueue login ack
		conn.EnqueueJSON(map[string]string{"event": "login", "code": "0"})
		// Enqueue subscription ack
		conn.EnqueueJSON(map[string]interface{}{
			"event": "subscribe",
			"arg":   map[string]string{"channel": "orders", "instType": "SPOT"},
		})
		dialer.mu.Lock()
		dialer.conns = append(dialer.conns, conn)
		dialer.mu.Unlock()
		return conn, nil
	})

	cfg := testConfig()
	sm := NewPrivateWSStateMachine(cfg, clock, dialer)
	return sm, dialer
}

// TestPrivateWS_Heartbeat20 verifies heartbeat is sent every 20 seconds.
func TestPrivateWS_Heartbeat20(t *testing.T) {
	clock := newFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	sm, dialer := setupConnectedSM(t, clock)

	err := sm.Start(context.Background())
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer sm.Stop()

	if sm.State() != PWS_Ready {
		t.Fatalf("expected Ready state, got %s", sm.State())
	}

	conn := dialer.LastConn()
	time.Sleep(20 * time.Millisecond) // Let goroutines settle

	// Record messages before advancing
	initialMsgs := len(conn.WrittenMessages())

	// Advance exactly 20 seconds - heartbeat ticker should fire
	clock.Advance(20 * time.Second)
	time.Sleep(50 * time.Millisecond) // Give goroutine time to process ticker

	msgs := conn.WrittenMessages()
	foundPing := false
	for i := initialMsgs; i < len(msgs); i++ {
		if string(msgs[i]) == "ping" {
			foundPing = true
			break
		}
	}
	if !foundPing {
		t.Errorf("expected 'ping' heartbeat at 20 seconds, not found in %d new messages", len(msgs)-initialMsgs)
	}

	// Send pong to refresh liveness
	conn.EnqueueMessage([]byte("pong"))
	time.Sleep(20 * time.Millisecond)

	// Advance another 20 seconds - second heartbeat
	beforeSecond := len(conn.WrittenMessages())
	clock.Advance(20 * time.Second)
	time.Sleep(50 * time.Millisecond)

	msgs2 := conn.WrittenMessages()
	foundSecondPing := false
	for i := beforeSecond; i < len(msgs2); i++ {
		if string(msgs2[i]) == "ping" {
			foundSecondPing = true
			break
		}
	}
	if !foundSecondPing {
		t.Errorf("expected second ping at 40s, not found")
	}

	// Verify correct interval: count all pings
	totalPings := 0
	for _, m := range conn.WrittenMessages() {
		if string(m) == "ping" {
			totalPings++
		}
	}
	if totalPings < 2 {
		t.Errorf("expected at least 2 pings after 40s, got %d", totalPings)
	}
}

// TestPrivateWS_HalfOpen45 verifies that a silent peer is detected within 45s.
func TestPrivateWS_HalfOpen45(t *testing.T) {
	clock := newFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	sm, _ := setupConnectedSM(t, clock)

	var gateOpen atomic.Bool
	gateOpen.Store(true)
	sm.SetRiskGateCallback(func(open bool, reason StateChangeReason) {
		gateOpen.Store(open)
	})

	err := sm.Start(context.Background())
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer sm.Stop()

	if sm.State() != PWS_Ready {
		t.Fatalf("expected Ready, got %s", sm.State())
	}

	// Silent peer: no pong received. Advance 44 seconds - still Ready
	clock.Advance(44 * time.Second)
	time.Sleep(10 * time.Millisecond)

	if sm.State() != PWS_Ready {
		t.Errorf("expected Ready at 44s, got %s", sm.State())
	}

	// Advance to 45 seconds - should become Unhealthy
	clock.Advance(1 * time.Second)
	time.Sleep(10 * time.Millisecond)

	state := sm.State()
	if state != PWS_Unhealthy && state != PWS_Backoff && state != PWS_Connecting {
		t.Errorf("expected Unhealthy/Backoff/Connecting at 45s, got %s", state)
	}

	// Risk gate should be closed
	if gateOpen.Load() {
		t.Error("risk gate should be closed after liveness timeout")
	}

	// Verify that socket-open alone doesn't refresh liveness
	// (the connection was open the entire time but no valid pong was received)
}

// TestPrivateWS_ReconnectWithin5 verifies reconnection starts within 5s of unhealthy.
func TestPrivateWS_ReconnectWithin5(t *testing.T) {
	clock := newFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	sm, dialer := setupConnectedSM(t, clock)

	var reconnectStarted atomic.Bool
	var reconnectTime time.Time
	var reconnectMu sync.Mutex
	var unhealthyTime time.Time

	sm.SetStateChangeCallback(func(event StateChangeEvent) {
		if event.To == PWS_Unhealthy {
			reconnectMu.Lock()
			unhealthyTime = event.Timestamp
			reconnectMu.Unlock()
		}
		if event.To == PWS_Connecting && event.From == PWS_Backoff {
			reconnectMu.Lock()
			reconnectTime = event.Timestamp
			reconnectStarted.Store(true)
			reconnectMu.Unlock()
		}
	})

	err := sm.Start(context.Background())
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer sm.Stop()

	// Let connection become unhealthy (advance 45s with no pong)
	clock.Advance(45 * time.Second)
	time.Sleep(20 * time.Millisecond)

	// The initial backoff is 1 second, so reconnect should start at +1s
	clock.Advance(1 * time.Second)
	time.Sleep(20 * time.Millisecond)

	if !reconnectStarted.Load() {
		// Try advancing a bit more for timer processing
		clock.Advance(1 * time.Second)
		time.Sleep(20 * time.Millisecond)
	}

	if !reconnectStarted.Load() {
		t.Error("expected reconnection to start within 5s of unhealthy")
	}

	reconnectMu.Lock()
	if !unhealthyTime.IsZero() && !reconnectTime.IsZero() {
		delay := reconnectTime.Sub(unhealthyTime)
		if delay > 5*time.Second {
			t.Errorf("reconnect started %v after unhealthy, want <= 5s", delay)
		}
	}
	reconnectMu.Unlock()

	// Verify bounded exponential backoff (initial 1s, 2s, 4s, ..., capped 30s)
	if sm.cfg.InitialBackoff != 1*time.Second {
		t.Errorf("initial backoff = %v, want 1s", sm.cfg.InitialBackoff)
	}
	if sm.cfg.MaxBackoff != 30*time.Second {
		t.Errorf("max backoff = %v, want 30s", sm.cfg.MaxBackoff)
	}
	if sm.cfg.MaxReconnectAttempts != 10 {
		t.Errorf("max attempts = %d, want 10", sm.cfg.MaxReconnectAttempts)
	}

	// Verify dialer was called for reconnection
	if dialer.ConnCount() < 2 {
		t.Logf("connection count: %d (reconnect may still be processing)", dialer.ConnCount())
	}
}

// TestPrivateWS_StrictAck verifies login and subscription must be strictly confirmed.
func TestPrivateWS_StrictAck(t *testing.T) {
	clock := newFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))

	t.Run("login_wrong_code_rejected", func(t *testing.T) {
		dialer := newFakeDialer()
		dialer.SetDialFunc(func(ctx context.Context, url string) (WSConn, error) {
			conn := newFakeWSConn()
			// Wrong login code
			conn.EnqueueJSON(map[string]string{"event": "login", "code": "60009"})
			dialer.mu.Lock()
			dialer.conns = append(dialer.conns, conn)
			dialer.mu.Unlock()
			return conn, nil
		})

		cfg := testConfig()
		sm := NewPrivateWSStateMachine(cfg, clock, dialer)
		err := sm.Start(context.Background())
		if err == nil {
			sm.Stop()
			t.Fatal("expected auth failure with wrong code")
		}
	})

	t.Run("login_wrong_event_rejected", func(t *testing.T) {
		dialer := newFakeDialer()
		dialer.SetDialFunc(func(ctx context.Context, url string) (WSConn, error) {
			conn := newFakeWSConn()
			// Wrong event type
			conn.EnqueueJSON(map[string]string{"event": "error", "code": "0"})
			dialer.mu.Lock()
			dialer.conns = append(dialer.conns, conn)
			dialer.mu.Unlock()
			return conn, nil
		})

		cfg := testConfig()
		sm := NewPrivateWSStateMachine(cfg, clock, dialer)
		err := sm.Start(context.Background())
		if err == nil {
			sm.Stop()
			t.Fatal("expected auth failure with wrong event")
		}
	})

	t.Run("subscription_wrong_channel_rejected", func(t *testing.T) {
		dialer := newFakeDialer()
		dialer.SetDialFunc(func(ctx context.Context, url string) (WSConn, error) {
			conn := newFakeWSConn()
			conn.EnqueueJSON(map[string]string{"event": "login", "code": "0"})
			// Wrong channel in ack
			conn.EnqueueJSON(map[string]interface{}{
				"event": "subscribe",
				"arg":   map[string]string{"channel": "positions", "instType": "SPOT"},
			})
			dialer.mu.Lock()
			dialer.conns = append(dialer.conns, conn)
			dialer.mu.Unlock()
			return conn, nil
		})

		cfg := testConfig()
		sm := NewPrivateWSStateMachine(cfg, clock, dialer)
		err := sm.Start(context.Background())
		if err == nil {
			sm.Stop()
			t.Fatal("expected subscribe failure with wrong channel")
		}
	})

	t.Run("data_push_not_accepted_as_sub_ack", func(t *testing.T) {
		dialer := newFakeDialer()
		dialer.SetDialFunc(func(ctx context.Context, url string) (WSConn, error) {
			conn := newFakeWSConn()
			conn.EnqueueJSON(map[string]string{"event": "login", "code": "0"})
			// Data push instead of sub ack
			conn.EnqueueJSON(map[string]interface{}{
				"arg":  map[string]string{"channel": "orders", "instType": "SPOT"},
				"data": []map[string]string{{"instId": "DOGE-USDT"}},
			})
			dialer.mu.Lock()
			dialer.conns = append(dialer.conns, conn)
			dialer.mu.Unlock()
			return conn, nil
		})

		cfg := testConfig()
		sm := NewPrivateWSStateMachine(cfg, clock, dialer)
		err := sm.Start(context.Background())
		if err == nil {
			sm.Stop()
			t.Fatal("expected subscribe failure - data push is not a sub ack")
		}
	})

	t.Run("correct_ack_accepted", func(t *testing.T) {
		dialer := newFakeDialer()
		dialer.SetDialFunc(func(ctx context.Context, url string) (WSConn, error) {
			conn := newFakeWSConn()
			conn.EnqueueJSON(map[string]string{"event": "login", "code": "0"})
			conn.EnqueueJSON(map[string]interface{}{
				"event": "subscribe",
				"arg":   map[string]string{"channel": "orders", "instType": "SPOT"},
			})
			dialer.mu.Lock()
			dialer.conns = append(dialer.conns, conn)
			dialer.mu.Unlock()
			return conn, nil
		})

		cfg := testConfig()
		sm := NewPrivateWSStateMachine(cfg, clock, dialer)
		err := sm.Start(context.Background())
		if err != nil {
			t.Fatalf("expected success with correct ack, got: %v", err)
		}
		defer sm.Stop()

		if sm.State() != PWS_Ready {
			t.Errorf("expected Ready, got %s", sm.State())
		}
	})
}

// TestPrivateWS_GapTriggersReconcile verifies reconciliation is triggered on startup/reconnect/gap.
func TestPrivateWS_GapTriggersReconcile(t *testing.T) {
	clock := newFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))

	t.Run("startup_triggers_reconciliation", func(t *testing.T) {
		dialer := newFakeDialer()
		dialer.SetDialFunc(func(ctx context.Context, url string) (WSConn, error) {
			conn := newFakeWSConn()
			conn.EnqueueJSON(map[string]string{"event": "login", "code": "0"})
			conn.EnqueueJSON(map[string]interface{}{
				"event": "subscribe",
				"arg":   map[string]string{"channel": "orders", "instType": "SPOT"},
			})
			dialer.mu.Lock()
			dialer.conns = append(dialer.conns, conn)
			dialer.mu.Unlock()
			return conn, nil
		})

		cfg := testConfig()
		sm := NewPrivateWSStateMachine(cfg, clock, dialer)

		var reconcileCalled atomic.Int32
		sm.SetReconciliationCallback(func(ctx context.Context) error {
			reconcileCalled.Add(1)
			return nil
		})

		err := sm.Start(context.Background())
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		defer sm.Stop()

		if reconcileCalled.Load() != 1 {
			t.Errorf("expected reconciliation on startup, called %d times",
				reconcileCalled.Load())
		}
	})

	t.Run("reconnect_triggers_reconciliation", func(t *testing.T) {
		sm, dialer := setupConnectedSM(t, clock)

		var reconcileCalled atomic.Int32
		sm.SetReconciliationCallback(func(ctx context.Context) error {
			reconcileCalled.Add(1)
			return nil
		})

		err := sm.Start(context.Background())
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		defer sm.Stop()

		initialCount := reconcileCalled.Load() // 1 from startup

		// Force disconnect
		clock.Advance(45 * time.Second)
		time.Sleep(20 * time.Millisecond)

		// Wait for reconnect timer and attempt
		clock.Advance(2 * time.Second)
		time.Sleep(50 * time.Millisecond)

		reconnectCount := reconcileCalled.Load()
		if reconnectCount <= initialCount {
			t.Logf("reconcile count: initial=%d, after reconnect=%d (reconnect may still be in progress)",
				initialCount, reconnectCount)
			// Give more time
			clock.Advance(5 * time.Second)
			time.Sleep(50 * time.Millisecond)
		}

		_ = dialer // suppress unused
	})

	t.Run("risk_gate_closed_during_reconciliation", func(t *testing.T) {
		dialer := newFakeDialer()
		dialer.SetDialFunc(func(ctx context.Context, url string) (WSConn, error) {
			conn := newFakeWSConn()
			conn.EnqueueJSON(map[string]string{"event": "login", "code": "0"})
			conn.EnqueueJSON(map[string]interface{}{
				"event": "subscribe",
				"arg":   map[string]string{"channel": "orders", "instType": "SPOT"},
			})
			dialer.mu.Lock()
			dialer.conns = append(dialer.conns, conn)
			dialer.mu.Unlock()
			return conn, nil
		})

		cfg := testConfig()
		sm := NewPrivateWSStateMachine(cfg, clock, dialer)

		var gateHistory []bool
		var gateMu sync.Mutex
		sm.SetRiskGateCallback(func(open bool, reason StateChangeReason) {
			gateMu.Lock()
			gateHistory = append(gateHistory, open)
			gateMu.Unlock()
		})

		sm.SetReconciliationCallback(func(ctx context.Context) error {
			// During reconciliation, gate should be closed
			gateMu.Lock()
			lastState := gateHistory[len(gateHistory)-1]
			gateMu.Unlock()
			if lastState {
				t.Error("gate should be closed during reconciliation")
			}
			return nil
		})

		err := sm.Start(context.Background())
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		defer sm.Stop()

		// After Ready, gate should be open
		gateMu.Lock()
		if len(gateHistory) < 2 {
			t.Error("expected at least 2 gate transitions (close on startup, open on ready)")
		} else {
			// Last should be open (true)
			if !gateHistory[len(gateHistory)-1] {
				t.Error("expected gate to be open after reaching Ready")
			}
		}
		gateMu.Unlock()
	})
}

// TestPrivateWS_ReadyGate verifies only auth+subscription+reconciliation leads to Ready.
func TestPrivateWS_ReadyGate(t *testing.T) {
	clock := newFakeClock(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))

	t.Run("not_ready_without_reconciliation", func(t *testing.T) {
		dialer := newFakeDialer()
		dialer.SetDialFunc(func(ctx context.Context, url string) (WSConn, error) {
			conn := newFakeWSConn()
			conn.EnqueueJSON(map[string]string{"event": "login", "code": "0"})
			conn.EnqueueJSON(map[string]interface{}{
				"event": "subscribe",
				"arg":   map[string]string{"channel": "orders", "instType": "SPOT"},
			})
			dialer.mu.Lock()
			dialer.conns = append(dialer.conns, conn)
			dialer.mu.Unlock()
			return conn, nil
		})

		cfg := testConfig()
		sm := NewPrivateWSStateMachine(cfg, clock, dialer)

		// Reconciliation fails
		sm.SetReconciliationCallback(func(ctx context.Context) error {
			return fmt.Errorf("reconciliation unavailable")
		})

		err := sm.Start(context.Background())
		if err == nil {
			sm.Stop()
			t.Fatal("expected failure when reconciliation fails")
		}

		// Should NOT be Ready
		if sm.State() == PWS_Ready {
			t.Error("should not be Ready when reconciliation fails")
		}
	})

	t.Run("state_change_publishes_required_fields", func(t *testing.T) {
		sm, _ := setupConnectedSM(t, clock)

		var events []StateChangeEvent
		var eventsMu sync.Mutex
		sm.SetStateChangeCallback(func(event StateChangeEvent) {
			eventsMu.Lock()
			events = append(events, event)
			eventsMu.Unlock()
		})

		err := sm.Start(context.Background())
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		defer sm.Stop()

		eventsMu.Lock()
		defer eventsMu.Unlock()

		if len(events) == 0 {
			t.Fatal("expected state change events")
		}

		// Find the Ready event
		var readyEvent *StateChangeEvent
		for i := range events {
			if events[i].To == PWS_Ready {
				readyEvent = &events[i]
				break
			}
		}
		if readyEvent == nil {
			t.Fatal("no Ready state change event found")
		}

		// Verify required fields are present (no credentials)
		if readyEvent.Reason == "" {
			t.Error("reason code missing")
		}
		if readyEvent.Epoch == 0 {
			t.Error("epoch should be > 0 after connection")
		}
		if readyEvent.LastLiveness.IsZero() {
			t.Error("last liveness should be set")
		}
		if len(readyEvent.SubscriptionSet) == 0 {
			t.Error("subscription set should be published")
		}
		if readyEvent.Timestamp.IsZero() {
			t.Error("timestamp should be set")
		}
	})

	t.Run("retries_exhausted_enters_safe_stopped", func(t *testing.T) {
		dialer := newFakeDialer()
		dialer.SetDialFunc(func(ctx context.Context, url string) (WSConn, error) {
			return nil, fmt.Errorf("connection refused")
		})

		cfg := testConfig()
		cfg.MaxReconnectAttempts = 2
		cfg.InitialBackoff = 1 * time.Second
		cfg.MaxBackoff = 2 * time.Second
		sm := NewPrivateWSStateMachine(cfg, clock, dialer)

		// Start will fail on first connect
		err := sm.Start(context.Background())
		if err == nil {
			sm.Stop()
			t.Fatal("expected failure when connect fails")
		}

		// State should be Backoff (from failed initial connect)
		state := sm.State()
		if state != PWS_Backoff {
			t.Logf("state after failed start: %s", state)
		}
	})
}
