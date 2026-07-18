package ratelimiter

import (
	"fmt"
	"sync"
	"time"
)

// bucket holds the state of a single token bucket for one endpoint.
type bucket struct {
	mu             sync.Mutex
	tokens         float64
	maxTokens      int
	refillRate     int
	refillInterval time.Duration
	lastRefill     time.Time
}

// refill adds tokens based on elapsed time since last refill.
func (b *bucket) refill(now time.Time) {
	elapsed := now.Sub(b.lastRefill)
	if elapsed <= 0 {
		return
	}

	// Calculate how many tokens to add based on elapsed time and refill rate.
	// refillRate tokens per refillInterval.
	tokensToAdd := float64(b.refillRate) * (float64(elapsed) / float64(b.refillInterval))
	b.tokens += tokensToAdd
	if b.tokens > float64(b.maxTokens) {
		b.tokens = float64(b.maxTokens)
	}
	b.lastRefill = now
}

// tryConsume attempts to consume one token. Returns true if successful.
func (b *bucket) tryConsume(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refill(now)
	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true
	}
	return false
}

// timeUntilNextToken returns the duration until the next token becomes available.
func (b *bucket) timeUntilNextToken(now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refill(now)
	if b.tokens >= 1.0 {
		return 0
	}

	// Calculate time needed to generate (1 - current tokens) more tokens.
	deficit := 1.0 - b.tokens
	// Time = deficit / (refillRate / refillInterval)
	timeNeeded := time.Duration(float64(b.refillInterval) * deficit / float64(b.refillRate))
	return timeNeeded
}

// TokenBucket implements the RateLimiter interface using per-endpoint token buckets.
// Each endpoint has its own independent bucket with configurable limits.
type TokenBucket struct {
	buckets   map[string]*bucket
	mu        sync.RWMutex // protects the buckets map
	nowFunc   func() time.Time
	sleepFunc func(time.Duration)
}

// NewTokenBucketLimiter creates a new TokenBucket rate limiter with the given endpoint configurations.
// Each endpoint gets its own independent token bucket initialized to full capacity.
func NewTokenBucketLimiter(configs []EndpointConfig) *TokenBucket {
	tb := &TokenBucket{
		buckets:   make(map[string]*bucket, len(configs)),
		nowFunc:   time.Now,
		sleepFunc: time.Sleep,
	}

	for _, cfg := range configs {
		tb.buckets[cfg.Endpoint] = &bucket{
			tokens:         float64(cfg.MaxTokens),
			maxTokens:      cfg.MaxTokens,
			refillRate:     cfg.RefillRate,
			refillInterval: cfg.RefillInterval,
			lastRefill:     time.Now(),
		}
	}

	return tb
}

// TryAcquire attempts to acquire a token for the given endpoint.
// It blocks for up to 5 seconds waiting for a token to become available.
// Returns nil on success, or an error with the endpoint name and next available time if timeout.
func (tb *TokenBucket) TryAcquire(endpoint string) error {
	tb.mu.RLock()
	b, exists := tb.buckets[endpoint]
	tb.mu.RUnlock()

	if !exists {
		return fmt.Errorf("rate limiter: unknown endpoint %q", endpoint)
	}

	const maxWait = 5 * time.Second
	deadline := tb.now().Add(maxWait)

	for {
		now := tb.now()
		if now.After(deadline) {
			nextAvail := b.timeUntilNextToken(now)
			return fmt.Errorf(
				"rate limiter: timeout waiting for token on endpoint %q, next available in %v",
				endpoint, nextAvail,
			)
		}

		if b.tryConsume(now) {
			return nil
		}

		// Calculate how long to sleep before a token might be available.
		waitTime := b.timeUntilNextToken(now)
		if waitTime <= 0 {
			waitTime = time.Millisecond
		}

		// Cap the sleep to not exceed the deadline.
		remaining := deadline.Sub(tb.now())
		if remaining <= 0 {
			nextAvail := b.timeUntilNextToken(tb.now())
			return fmt.Errorf(
				"rate limiter: timeout waiting for token on endpoint %q, next available in %v",
				endpoint, nextAvail,
			)
		}
		if waitTime > remaining {
			waitTime = remaining
		}

		tb.sleepFunc(waitTime)
	}
}

// GetNextAvailableTime returns the duration until the next token becomes available
// for the given endpoint. Returns 0 if a token is currently available.
// Returns -1 if the endpoint is unknown.
func (tb *TokenBucket) GetNextAvailableTime(endpoint string) time.Duration {
	tb.mu.RLock()
	b, exists := tb.buckets[endpoint]
	tb.mu.RUnlock()

	if !exists {
		return -1
	}

	return b.timeUntilNextToken(tb.now())
}

// now returns the current time, using the nowFunc for testability.
func (tb *TokenBucket) now() time.Time {
	if tb.nowFunc != nil {
		return tb.nowFunc()
	}
	return time.Now()
}
