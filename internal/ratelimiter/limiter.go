package ratelimiter

import "time"

// RateLimiter defines the interface for API request rate limiting using token bucket algorithm.
// Each OKX API endpoint has its own token bucket (approximately 20 requests/2s per endpoint).
type RateLimiter interface {
	// TryAcquire attempts to acquire a token for the given endpoint.
	// Blocks for up to 5 seconds if no token is available.
	// Returns nil on success, or an error if the timeout is reached without acquiring a token.
	TryAcquire(endpoint string) error

	// GetNextAvailableTime returns the duration until the next token becomes available
	// for the given endpoint.
	GetNextAvailableTime(endpoint string) time.Duration
}

// EndpointConfig holds rate limit configuration for a specific API endpoint.
type EndpointConfig struct {
	Endpoint       string        // API endpoint path, e.g. "trade/order"
	MaxTokens      int           // Maximum tokens in the bucket
	RefillRate     int           // Tokens refilled per interval
	RefillInterval time.Duration // Refill interval duration
}

// DefaultEndpointConfigs returns the default rate limit configurations for OKX API endpoints.
func DefaultEndpointConfigs() []EndpointConfig {
	return []EndpointConfig{
		{Endpoint: "trade/order", MaxTokens: 20, RefillRate: 20, RefillInterval: 2 * time.Second},
		{Endpoint: "trade/batch-orders", MaxTokens: 20, RefillRate: 20, RefillInterval: 2 * time.Second},
		{Endpoint: "trade/cancel-order", MaxTokens: 20, RefillRate: 20, RefillInterval: 2 * time.Second},
		{Endpoint: "trade/cancel-batch-orders", MaxTokens: 20, RefillRate: 20, RefillInterval: 2 * time.Second},
		{Endpoint: "market/ticker", MaxTokens: 20, RefillRate: 20, RefillInterval: 2 * time.Second},
		{Endpoint: "account/positions", MaxTokens: 20, RefillRate: 20, RefillInterval: 2 * time.Second},
	}
}
