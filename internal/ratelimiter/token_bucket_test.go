package ratelimiter

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewTokenBucketLimiter(t *testing.T) {
	configs := []EndpointConfig{
		{Endpoint: "trade/order", MaxTokens: 20, RefillRate: 20, RefillInterval: 2 * time.Second},
		{Endpoint: "market/ticker", MaxTokens: 10, RefillRate: 10, RefillInterval: 2 * time.Second},
	}

	tb := NewTokenBucketLimiter(configs)

	if len(tb.buckets) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(tb.buckets))
	}

	b1, ok := tb.buckets["trade/order"]
	if !ok {
		t.Fatal("expected bucket for trade/order")
	}
	if b1.maxTokens != 20 {
		t.Errorf("expected maxTokens=20, got %d", b1.maxTokens)
	}
	if b1.tokens != 20.0 {
		t.Errorf("expected initial tokens=20.0, got %f", b1.tokens)
	}

	b2, ok := tb.buckets["market/ticker"]
	if !ok {
		t.Fatal("expected bucket for market/ticker")
	}
	if b2.maxTokens != 10 {
		t.Errorf("expected maxTokens=10, got %d", b2.maxTokens)
	}
}

func TestTryAcquire_Success(t *testing.T) {
	configs := []EndpointConfig{
		{Endpoint: "trade/order", MaxTokens: 20, RefillRate: 20, RefillInterval: 2 * time.Second},
	}
	tb := NewTokenBucketLimiter(configs)

	// Should succeed immediately since bucket starts full.
	err := tb.TryAcquire("trade/order")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestTryAcquire_UnknownEndpoint(t *testing.T) {
	configs := []EndpointConfig{
		{Endpoint: "trade/order", MaxTokens: 20, RefillRate: 20, RefillInterval: 2 * time.Second},
	}
	tb := NewTokenBucketLimiter(configs)

	err := tb.TryAcquire("unknown/endpoint")
	if err == nil {
		t.Fatal("expected error for unknown endpoint")
	}
	if !strings.Contains(err.Error(), "unknown endpoint") {
		t.Errorf("expected error mentioning unknown endpoint, got: %v", err)
	}
}

func TestTryAcquire_ConsumesTokens(t *testing.T) {
	configs := []EndpointConfig{
		{Endpoint: "trade/order", MaxTokens: 5, RefillRate: 5, RefillInterval: 2 * time.Second},
	}
	tb := NewTokenBucketLimiter(configs)

	// Consume all 5 tokens.
	for i := 0; i < 5; i++ {
		err := tb.TryAcquire("trade/order")
		if err != nil {
			t.Fatalf("iteration %d: expected no error, got: %v", i, err)
		}
	}

	// Verify the bucket is now empty.
	b := tb.buckets["trade/order"]
	b.mu.Lock()
	tokens := b.tokens
	b.mu.Unlock()
	if tokens > 0.1 { // Allow small floating point imprecision
		t.Errorf("expected tokens near 0, got %f", tokens)
	}
}

func TestTryAcquire_BlocksAndRefills(t *testing.T) {
	configs := []EndpointConfig{
		// High refill rate so test doesn't take long: 10 tokens per 100ms.
		{Endpoint: "fast", MaxTokens: 2, RefillRate: 10, RefillInterval: 100 * time.Millisecond},
	}
	tb := NewTokenBucketLimiter(configs)

	// Consume all tokens.
	for i := 0; i < 2; i++ {
		_ = tb.TryAcquire("fast")
	}

	// Next acquire should block briefly and then succeed after refill.
	start := time.Now()
	err := tb.TryAcquire("fast")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected no error after waiting, got: %v", err)
	}
	// Should have waited at least a few milliseconds for refill.
	if elapsed < 5*time.Millisecond {
		t.Errorf("expected some wait time, got %v", elapsed)
	}
	// Should not have waited more than 1 second.
	if elapsed > 1*time.Second {
		t.Errorf("waited too long: %v", elapsed)
	}
}

func TestTryAcquire_Timeout(t *testing.T) {
	configs := []EndpointConfig{
		// Very slow refill: 1 token per 100 seconds.
		{Endpoint: "slow", MaxTokens: 1, RefillRate: 1, RefillInterval: 100 * time.Second},
	}
	tb := NewTokenBucketLimiter(configs)

	// Use a fake clock that advances on each call to simulate time passing
	// past the deadline without real sleeps.
	start := time.Now()
	var callCount atomic.Int64
	tb.nowFunc = func() time.Time {
		// Each call advances the fake clock by 1 second, so after 6 calls
		// the deadline (5s from start) will have been exceeded.
		n := callCount.Add(1)
		return start.Add(time.Duration(n) * time.Second)
	}

	// Consume the single token (this will use call 1 for deadline + call 2 for now check).
	_ = tb.TryAcquire("slow")

	// Reset call count so the second TryAcquire starts fresh with time advancing.
	callCount.Store(0)
	tb.nowFunc = func() time.Time {
		// First call sets deadline at start+0+5s = start+5s.
		// Subsequent calls advance past that deadline.
		n := callCount.Add(1)
		if n == 1 {
			// Deadline calculation: now = start, deadline = start+5s
			return start
		}
		// After first call, jump time to past the deadline
		return start.Add(6 * time.Second)
	}

	err := tb.TryAcquire("slow")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected error mentioning timeout, got: %v", err)
	}
	if !strings.Contains(err.Error(), "slow") {
		t.Errorf("expected error mentioning endpoint name, got: %v", err)
	}
}

func TestGetNextAvailableTime_TokenAvailable(t *testing.T) {
	configs := []EndpointConfig{
		{Endpoint: "trade/order", MaxTokens: 20, RefillRate: 20, RefillInterval: 2 * time.Second},
	}
	tb := NewTokenBucketLimiter(configs)

	d := tb.GetNextAvailableTime("trade/order")
	if d != 0 {
		t.Errorf("expected 0 when tokens available, got %v", d)
	}
}

func TestGetNextAvailableTime_NoTokenAvailable(t *testing.T) {
	configs := []EndpointConfig{
		{Endpoint: "trade/order", MaxTokens: 2, RefillRate: 20, RefillInterval: 2 * time.Second},
	}
	tb := NewTokenBucketLimiter(configs)

	// Consume all tokens.
	_ = tb.TryAcquire("trade/order")
	_ = tb.TryAcquire("trade/order")

	d := tb.GetNextAvailableTime("trade/order")
	if d <= 0 {
		t.Errorf("expected positive duration when no tokens available, got %v", d)
	}
	// For 20 tokens per 2 seconds, one token takes 100ms.
	if d > 200*time.Millisecond {
		t.Errorf("expected wait time around 100ms, got %v", d)
	}
}

func TestGetNextAvailableTime_UnknownEndpoint(t *testing.T) {
	configs := []EndpointConfig{
		{Endpoint: "trade/order", MaxTokens: 20, RefillRate: 20, RefillInterval: 2 * time.Second},
	}
	tb := NewTokenBucketLimiter(configs)

	d := tb.GetNextAvailableTime("unknown")
	if d != -1 {
		t.Errorf("expected -1 for unknown endpoint, got %v", d)
	}
}

func TestTokenBucket_SeparateBuckets(t *testing.T) {
	configs := []EndpointConfig{
		{Endpoint: "trade/order", MaxTokens: 2, RefillRate: 20, RefillInterval: 2 * time.Second},
		{Endpoint: "market/ticker", MaxTokens: 2, RefillRate: 20, RefillInterval: 2 * time.Second},
	}
	tb := NewTokenBucketLimiter(configs)

	// Consume all tokens from trade/order.
	_ = tb.TryAcquire("trade/order")
	_ = tb.TryAcquire("trade/order")

	// market/ticker should still have tokens.
	err := tb.TryAcquire("market/ticker")
	if err != nil {
		t.Fatalf("market/ticker should still have tokens, got: %v", err)
	}

	// trade/order should be depleted.
	d := tb.GetNextAvailableTime("trade/order")
	if d <= 0 {
		t.Error("trade/order should have no tokens available")
	}
}

func TestTokenBucket_DoesNotExceedMaxTokens(t *testing.T) {
	configs := []EndpointConfig{
		{Endpoint: "trade/order", MaxTokens: 5, RefillRate: 100, RefillInterval: 100 * time.Millisecond},
	}
	tb := NewTokenBucketLimiter(configs)

	// Wait for refill to overshoot if not capped.
	time.Sleep(200 * time.Millisecond)

	b := tb.buckets["trade/order"]
	b.mu.Lock()
	b.refill(time.Now())
	tokens := b.tokens
	b.mu.Unlock()

	if tokens > float64(5) {
		t.Errorf("tokens should not exceed maxTokens (5), got %f", tokens)
	}
}

func TestTokenBucket_ThreadSafety(t *testing.T) {
	configs := []EndpointConfig{
		{Endpoint: "trade/order", MaxTokens: 100, RefillRate: 100, RefillInterval: 100 * time.Millisecond},
	}
	tb := NewTokenBucketLimiter(configs)

	var wg sync.WaitGroup
	var successCount atomic.Int64

	// Launch 50 concurrent goroutines all trying to acquire tokens.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := tb.TryAcquire("trade/order")
			if err == nil {
				successCount.Add(1)
			}
		}()
	}

	wg.Wait()

	// All 50 should succeed since we have 100 tokens.
	if successCount.Load() != 50 {
		t.Errorf("expected all 50 acquires to succeed, got %d", successCount.Load())
	}
}

func TestTokenBucket_RateLimitWithin2SecondWindow(t *testing.T) {
	configs := []EndpointConfig{
		{Endpoint: "trade/order", MaxTokens: 20, RefillRate: 20, RefillInterval: 2 * time.Second},
	}
	tb := NewTokenBucketLimiter(configs)

	// Consume all 20 tokens instantly.
	for i := 0; i < 20; i++ {
		err := tb.TryAcquire("trade/order")
		if err != nil {
			t.Fatalf("acquire %d failed: %v", i, err)
		}
	}

	// The 21st request should have to wait since we're within the 2-second window.
	d := tb.GetNextAvailableTime("trade/order")
	if d <= 0 {
		t.Error("expected to wait for next token after consuming all 20 within the window")
	}
}

func TestTokenBucket_ImplementsRateLimiterInterface(t *testing.T) {
	configs := DefaultEndpointConfigs()
	var _ RateLimiter = NewTokenBucketLimiter(configs)
}
