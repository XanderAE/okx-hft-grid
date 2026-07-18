package ratelimiter

import (
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// **Validates: Requirements 8.1, 8.2, 8.3, 8.5, 8.6**

// TestProperty_RateLimitInvariant_NoEndpointExceedsLimitIn2SecondWindow tests that
// for any sequence of API requests, no endpoint receives more requests than its
// configured limit within any 2-second measurement window.
func TestProperty_RateLimitInvariant_NoEndpointExceedsLimitIn2SecondWindow(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate arbitrary configuration.
		maxTokens := rapid.IntRange(2, 30).Draw(t, "maxTokens")
		refillRate := rapid.IntRange(1, 30).Draw(t, "refillRate")
		refillIntervalMs := rapid.IntRange(500, 3000).Draw(t, "refillIntervalMs")
		refillInterval := time.Duration(refillIntervalMs) * time.Millisecond

		cfg := EndpointConfig{
			Endpoint:       "test/endpoint",
			MaxTokens:      maxTokens,
			RefillRate:     refillRate,
			RefillInterval: refillInterval,
		}

		startTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		var mu sync.Mutex
		currentTime := startTime

		tb := NewTokenBucketLimiter([]EndpointConfig{cfg})
		tb.nowFunc = func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return currentTime
		}
		tb.buckets["test/endpoint"].lastRefill = startTime

		// Generate a random request sequence with random time steps.
		numRequests := rapid.IntRange(20, 100).Draw(t, "numRequests")
		type acquireResult struct {
			time    time.Time
			success bool
		}
		results := make([]acquireResult, 0, numRequests)

		for i := 0; i < numRequests; i++ {
			// Advance time by a random step (0 to 500ms).
			stepMs := rapid.IntRange(0, 500).Draw(t, "stepMs")
			mu.Lock()
			currentTime = currentTime.Add(time.Duration(stepMs) * time.Millisecond)
			now := currentTime
			mu.Unlock()

			b := tb.buckets["test/endpoint"]
			success := b.tryConsume(now)
			results = append(results, acquireResult{time: now, success: success})
		}

		// Verify: in any 2-second sliding window, successful acquires do not
		// exceed maxTokens + tokens refilled in that window.
		windowDuration := 2 * time.Second
		for i := range results {
			if !results[i].success {
				continue
			}
			windowStart := results[i].time
			windowEnd := windowStart.Add(windowDuration)
			successInWindow := 0
			for j := i; j < len(results); j++ {
				if results[j].time.After(windowEnd) {
					break
				}
				if results[j].success {
					successInWindow++
				}
			}

			// Max possible in a 2-second window: initial bucket + refilled tokens.
			tokensRefilled := float64(refillRate) * (float64(windowDuration) / float64(refillInterval))
			maxPossible := float64(maxTokens) + tokensRefilled

			if float64(successInWindow) > maxPossible+1.0 {
				t.Fatalf("endpoint exceeded rate limit in 2s window: got %d successful requests, "+
					"max possible is %f (maxTokens=%d, refillRate=%d, refillInterval=%dms)",
					successInWindow, maxPossible, maxTokens, refillRate, refillIntervalMs)
			}
		}
	})
}

// TestProperty_RateLimitInvariant_SeparateBucketsPerEndpoint tests that separate
// token buckets are maintained for each distinct API endpoint. Requests to one
// endpoint do not affect another endpoint's token availability.
func TestProperty_RateLimitInvariant_SeparateBucketsPerEndpoint(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate configs for two distinct endpoints.
		maxTokens1 := rapid.IntRange(1, 30).Draw(t, "maxTokens1")
		maxTokens2 := rapid.IntRange(1, 30).Draw(t, "maxTokens2")
		refillRate1 := rapid.IntRange(1, 30).Draw(t, "refillRate1")
		refillRate2 := rapid.IntRange(1, 30).Draw(t, "refillRate2")

		cfg1 := EndpointConfig{
			Endpoint:       "trade/order",
			MaxTokens:      maxTokens1,
			RefillRate:     refillRate1,
			RefillInterval: 2 * time.Second,
		}
		cfg2 := EndpointConfig{
			Endpoint:       "market/ticker",
			MaxTokens:      maxTokens2,
			RefillRate:     refillRate2,
			RefillInterval: 2 * time.Second,
		}

		frozenTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		tb := NewTokenBucketLimiter([]EndpointConfig{cfg1, cfg2})
		tb.nowFunc = func() time.Time { return frozenTime }
		tb.buckets["trade/order"].lastRefill = frozenTime
		tb.buckets["market/ticker"].lastRefill = frozenTime

		// Drain all tokens from endpoint 1.
		for i := 0; i < maxTokens1; i++ {
			b := tb.buckets["trade/order"]
			b.mu.Lock()
			b.tokens -= 1.0
			b.mu.Unlock()
		}

		// Endpoint 2 should still have all its tokens unchanged.
		b2 := tb.buckets["market/ticker"]
		b2.mu.Lock()
		b2.refill(frozenTime)
		tokensInB2 := b2.tokens
		b2.mu.Unlock()

		if tokensInB2 != float64(maxTokens2) {
			t.Fatalf("draining trade/order affected market/ticker: expected %d tokens, got %f",
				maxTokens2, tokensInB2)
		}

		// Endpoint 1 should be drained.
		b1 := tb.buckets["trade/order"]
		b1.mu.Lock()
		b1.refill(frozenTime)
		tokensInB1 := b1.tokens
		b1.mu.Unlock()

		if tokensInB1 > 0.01 {
			t.Fatalf("trade/order should be drained, but has %f tokens", tokensInB1)
		}

		// Now consume tokens from endpoint 2 - should succeed independently.
		consumeCount := rapid.IntRange(1, maxTokens2).Draw(t, "consumeFromSecond")
		for i := 0; i < consumeCount; i++ {
			b2.mu.Lock()
			b2.refill(frozenTime)
			if b2.tokens < 1.0 {
				b2.mu.Unlock()
				t.Fatalf("market/ticker should have tokens for consume %d/%d", i+1, consumeCount)
			}
			b2.tokens -= 1.0
			b2.mu.Unlock()
		}

		// Verify endpoint 1 is still drained (unchanged by endpoint 2 consumption).
		b1.mu.Lock()
		b1.refill(frozenTime)
		tokensInB1After := b1.tokens
		b1.mu.Unlock()

		if tokensInB1After > 0.01 {
			t.Fatalf("consuming from market/ticker should not affect trade/order: got %f tokens",
				tokensInB1After)
		}
	})
}

// TestProperty_RateLimitInvariant_BlockingTimeoutDoesNotExceed5Seconds tests that
// when a token is unavailable, blocking does not exceed 5 seconds before returning
// a timeout error.
func TestProperty_RateLimitInvariant_BlockingTimeoutDoesNotExceed5Seconds(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Use a very slow refill rate to ensure timeout occurs.
		maxTokens := rapid.IntRange(1, 5).Draw(t, "maxTokens")
		// Refill so slow that 5s is not enough for a new token.
		refillRate := 1
		refillInterval := time.Duration(rapid.IntRange(30, 120).Draw(t, "refillIntervalSec")) * time.Second

		cfg := EndpointConfig{
			Endpoint:       "test/slow",
			MaxTokens:      maxTokens,
			RefillRate:     refillRate,
			RefillInterval: refillInterval,
		}

		// Use a simulated clock that advances automatically.
		var mu sync.Mutex
		startTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		currentTime := startTime

		tb := NewTokenBucketLimiter([]EndpointConfig{cfg})
		tb.nowFunc = func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			// Auto-advance time by 100ms each call to simulate passage.
			currentTime = currentTime.Add(100 * time.Millisecond)
			return currentTime
		}
		tb.sleepFunc = func(d time.Duration) {} // no-op: simulated clock advances via nowFunc
		tb.buckets["test/slow"].lastRefill = startTime

		// Drain all tokens.
		b := tb.buckets["test/slow"]
		b.mu.Lock()
		b.tokens = 0
		b.mu.Unlock()

		// TryAcquire should timeout. We measure the simulated time elapsed.
		mu.Lock()
		timeBeforeAcquire := currentTime
		mu.Unlock()

		err := tb.TryAcquire("test/slow")
		if err == nil {
			t.Fatal("expected timeout error when refill is too slow, but got nil")
		}

		mu.Lock()
		timeAfterAcquire := currentTime
		mu.Unlock()

		elapsed := timeAfterAcquire.Sub(timeBeforeAcquire)
		// The simulated elapsed time should be at most ~5 seconds + some tolerance
		// for the last loop iteration.
		maxAllowed := 6 * time.Second
		if elapsed > maxAllowed {
			t.Fatalf("blocking exceeded 5 seconds (simulated): elapsed %v, max allowed %v",
				elapsed, maxAllowed)
		}
	})
}

// TestProperty_RateLimitInvariant_RefillBehaviorCorrectness tests that the token
// bucket refill behavior is correct: tokens are added proportional to elapsed time
// and never exceed max capacity.
func TestProperty_RateLimitInvariant_RefillBehaviorCorrectness(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxTokens := rapid.IntRange(5, 50).Draw(t, "maxTokens")
		refillRate := rapid.IntRange(1, 50).Draw(t, "refillRate")
		refillIntervalMs := rapid.IntRange(100, 5000).Draw(t, "refillIntervalMs")
		refillInterval := time.Duration(refillIntervalMs) * time.Millisecond
		initialTokens := rapid.Float64Range(0, float64(maxTokens)).Draw(t, "initialTokens")
		elapsedMs := rapid.IntRange(1, 10000).Draw(t, "elapsedMs")
		elapsed := time.Duration(elapsedMs) * time.Millisecond

		cfg := EndpointConfig{
			Endpoint:       "test/refill",
			MaxTokens:      maxTokens,
			RefillRate:     refillRate,
			RefillInterval: refillInterval,
		}

		startTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		tb := NewTokenBucketLimiter([]EndpointConfig{cfg})
		b := tb.buckets["test/refill"]

		// Set initial state.
		b.mu.Lock()
		b.tokens = initialTokens
		b.lastRefill = startTime
		b.mu.Unlock()

		// Refill after elapsed time.
		futureTime := startTime.Add(elapsed)
		b.mu.Lock()
		b.refill(futureTime)
		tokensAfter := b.tokens
		b.mu.Unlock()

		// Property 1: Tokens never exceed maxTokens.
		if tokensAfter > float64(maxTokens)+0.001 {
			t.Fatalf("tokens (%f) exceed maxTokens (%d) after refill",
				tokensAfter, maxTokens)
		}

		// Property 2: Tokens are non-negative.
		if tokensAfter < 0 {
			t.Fatalf("tokens (%f) are negative after refill", tokensAfter)
		}

		// Property 3: Tokens should be >= initial tokens (refill only adds).
		if tokensAfter < initialTokens-0.001 {
			t.Fatalf("tokens decreased after refill: was %f, now %f",
				initialTokens, tokensAfter)
		}

		// Property 4: Verify the expected number of tokens added.
		expectedAdded := float64(refillRate) * (float64(elapsed) / float64(refillInterval))
		expectedTotal := initialTokens + expectedAdded
		if expectedTotal > float64(maxTokens) {
			expectedTotal = float64(maxTokens)
		}

		tolerance := 0.001
		if tokensAfter < expectedTotal-tolerance || tokensAfter > expectedTotal+tolerance {
			t.Fatalf("refill calculation incorrect: initial=%f, elapsed=%v, "+
				"expected=%f, got=%f (refillRate=%d, refillInterval=%v)",
				initialTokens, elapsed, expectedTotal, tokensAfter, refillRate, refillInterval)
		}
	})
}

// TestProperty_RateLimitInvariant_VariousConfigsRequestPatterns tests with various
// configurations of limits and request patterns to ensure the rate limiter is correct
// across the configuration space. Generates multiple endpoints with different configs
// and interleaved request patterns.
func TestProperty_RateLimitInvariant_VariousConfigsRequestPatterns(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate 2-4 endpoints with different configurations.
		numEndpoints := rapid.IntRange(2, 4).Draw(t, "numEndpoints")
		configs := make([]EndpointConfig, numEndpoints)
		for i := 0; i < numEndpoints; i++ {
			configs[i] = EndpointConfig{
				Endpoint:       rapid.StringMatching(`[a-z]+/[a-z]+`).Draw(t, "endpoint"),
				MaxTokens:      rapid.IntRange(5, 25).Draw(t, "maxTokens"),
				RefillRate:     rapid.IntRange(5, 25).Draw(t, "refillRate"),
				RefillInterval: time.Duration(rapid.IntRange(1000, 3000).Draw(t, "refillMs")) * time.Millisecond,
			}
		}

		// Ensure unique endpoint names.
		seen := make(map[string]bool)
		for i := range configs {
			for seen[configs[i].Endpoint] {
				configs[i].Endpoint = configs[i].Endpoint + "x"
			}
			seen[configs[i].Endpoint] = true
		}

		startTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		var mu sync.Mutex
		currentTime := startTime

		tb := NewTokenBucketLimiter(configs)
		tb.nowFunc = func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return currentTime
		}
		for _, cfg := range configs {
			tb.buckets[cfg.Endpoint].lastRefill = startTime
		}

		// Track successful acquires per endpoint with timestamps.
		type acquireRecord struct {
			endpoint string
			time     time.Time
		}
		var records []acquireRecord

		// Generate interleaved requests across endpoints.
		numRequests := rapid.IntRange(30, 80).Draw(t, "numRequests")
		for i := 0; i < numRequests; i++ {
			// Pick a random endpoint.
			epIdx := rapid.IntRange(0, numEndpoints-1).Draw(t, "epIdx")
			endpoint := configs[epIdx].Endpoint

			// Advance time by a random step.
			stepMs := rapid.IntRange(0, 300).Draw(t, "stepMs")
			mu.Lock()
			currentTime = currentTime.Add(time.Duration(stepMs) * time.Millisecond)
			now := currentTime
			mu.Unlock()

			b := tb.buckets[endpoint]
			if b.tryConsume(now) {
				records = append(records, acquireRecord{endpoint: endpoint, time: now})
			}
		}

		// Verify each endpoint independently: no endpoint exceeds its limit
		// in any 2-second window.
		windowDuration := 2 * time.Second
		for _, cfg := range configs {
			// Filter records for this endpoint.
			var epRecords []acquireRecord
			for _, r := range records {
				if r.endpoint == cfg.Endpoint {
					epRecords = append(epRecords, r)
				}
			}

			tokensRefilled := float64(cfg.RefillRate) * (float64(windowDuration) / float64(cfg.RefillInterval))
			maxPossible := float64(cfg.MaxTokens) + tokensRefilled

			for i := range epRecords {
				windowEnd := epRecords[i].time.Add(windowDuration)
				count := 0
				for j := i; j < len(epRecords); j++ {
					if epRecords[j].time.After(windowEnd) {
						break
					}
					count++
				}
				if float64(count) > maxPossible+1.0 {
					t.Fatalf("endpoint %q exceeded rate limit: %d requests in 2s window, max %f",
						cfg.Endpoint, count, maxPossible)
				}
			}
		}
	})
}

// TestProperty_RateLimitInvariant_TokensNeverExceedCapacity tests that regardless
// of how much time passes or how many refills occur, the token count never exceeds
// the configured maximum.
func TestProperty_RateLimitInvariant_TokensNeverExceedCapacity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxTokens := rapid.IntRange(1, 100).Draw(t, "maxTokens")
		refillRate := rapid.IntRange(1, 200).Draw(t, "refillRate")
		refillIntervalMs := rapid.IntRange(50, 10000).Draw(t, "refillIntervalMs")
		elapsedSeconds := rapid.IntRange(0, 7200).Draw(t, "elapsedSeconds")

		cfg := EndpointConfig{
			Endpoint:       "test/cap",
			MaxTokens:      maxTokens,
			RefillRate:     refillRate,
			RefillInterval: time.Duration(refillIntervalMs) * time.Millisecond,
		}

		startTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		tb := NewTokenBucketLimiter([]EndpointConfig{cfg})
		b := tb.buckets["test/cap"]
		b.lastRefill = startTime

		futureTime := startTime.Add(time.Duration(elapsedSeconds) * time.Second)
		b.mu.Lock()
		b.refill(futureTime)
		tokens := b.tokens
		b.mu.Unlock()

		if tokens > float64(maxTokens)+0.001 {
			t.Fatalf("tokens (%f) exceeded maxTokens (%d) after %d seconds elapsed "+
				"(refillRate=%d, refillInterval=%dms)",
				tokens, maxTokens, elapsedSeconds, refillRate, refillIntervalMs)
		}
	})
}

// TestProperty_RateLimitInvariant_ConsumeReducesTokensByExactlyOne tests that each
// successful token consumption reduces the token count by exactly 1.
func TestProperty_RateLimitInvariant_ConsumeReducesTokensByExactlyOne(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxTokens := rapid.IntRange(5, 30).Draw(t, "maxTokens")

		cfg := EndpointConfig{
			Endpoint:       "test/consume",
			MaxTokens:      maxTokens,
			RefillRate:     10,
			RefillInterval: 2 * time.Second,
		}

		frozenTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		tb := NewTokenBucketLimiter([]EndpointConfig{cfg})
		tb.nowFunc = func() time.Time { return frozenTime }
		b := tb.buckets["test/consume"]
		b.lastRefill = frozenTime

		// Consume tokens one by one and verify the count decreases by 1 each time.
		numToConsume := rapid.IntRange(1, maxTokens).Draw(t, "numToConsume")
		for i := 0; i < numToConsume; i++ {
			b.mu.Lock()
			tokensBefore := b.tokens
			b.mu.Unlock()

			success := b.tryConsume(frozenTime)
			if !success {
				t.Fatalf("expected successful consume at iteration %d (tokens before: %f)",
					i, tokensBefore)
			}

			b.mu.Lock()
			tokensAfter := b.tokens
			b.mu.Unlock()

			diff := tokensBefore - tokensAfter
			if diff < 0.999 || diff > 1.001 {
				t.Fatalf("consume did not reduce tokens by 1: before=%f, after=%f, diff=%f",
					tokensBefore, tokensAfter, diff)
			}
		}
	})
}

// TestProperty_RateLimitInvariant_ExactlyMaxTokensBeforeBlock tests that a fully
// filled bucket allows exactly MaxTokens consumptions before blocking (with frozen time).
func TestProperty_RateLimitInvariant_ExactlyMaxTokensBeforeBlock(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxTokens := rapid.IntRange(1, 40).Draw(t, "maxTokens")
		refillRate := rapid.IntRange(1, 50).Draw(t, "refillRate")
		refillIntervalMs := rapid.IntRange(500, 5000).Draw(t, "refillIntervalMs")

		cfg := EndpointConfig{
			Endpoint:       "test/exact",
			MaxTokens:      maxTokens,
			RefillRate:     refillRate,
			RefillInterval: time.Duration(refillIntervalMs) * time.Millisecond,
		}

		frozenTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		tb := NewTokenBucketLimiter([]EndpointConfig{cfg})
		tb.nowFunc = func() time.Time { return frozenTime }
		b := tb.buckets["test/exact"]
		b.lastRefill = frozenTime

		// Should succeed exactly maxTokens times.
		for i := 0; i < maxTokens; i++ {
			if !b.tryConsume(frozenTime) {
				t.Fatalf("expected success at consume %d/%d", i+1, maxTokens)
			}
		}

		// The (maxTokens+1)-th attempt should fail.
		if b.tryConsume(frozenTime) {
			t.Fatalf("expected failure after consuming all %d tokens", maxTokens)
		}
	})
}
