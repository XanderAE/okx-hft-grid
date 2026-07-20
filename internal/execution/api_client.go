package execution

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"time"

	"github.com/yourname/okx-hft-grid/internal/config"
	"github.com/yourname/okx-hft-grid/internal/ratelimiter"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

const (
	// maxBatchSize is the maximum number of orders per batch request to OKX.
	maxBatchSize = 20

	// retry429MaxRetries is the maximum number of retries for HTTP 429 responses.
	retry429MaxRetries = 3

	// retry429InitialBackoff is the initial backoff duration for HTTP 429 retries.
	retry429InitialBackoff = 100 * time.Millisecond

	// retry429MaxBackoff is the maximum backoff duration for HTTP 429 retries.
	retry429MaxBackoff = 5000 * time.Millisecond

	// retry5xxMaxRetries is the maximum number of retries for HTTP 5xx responses.
	retry5xxMaxRetries = 3

	// retry5xxInterval is the fixed interval between HTTP 5xx retries.
	retry5xxInterval = 100 * time.Millisecond
)

// APIClient encapsulates OKX REST API calls with HMAC-SHA256 signing,
// TLS 1.2+ enforcement, and retry logic for rate limiting and server errors.
type APIClient struct {
	baseURL       string
	credentials   *config.Credentials
	httpClient    *http.Client
	rateLimiter   ratelimiter.RateLimiter
	endpointGuard config.EndpointGuard
}

// NewAPIClient creates a client with the deny-by-default automated-validation
// policy. Existing unit/replay callers can reach loopback only; production
// composition must explicitly supply a validated ProductionNetworkGuard.
func NewAPIClient(baseURL string, creds *config.Credentials, rl ratelimiter.RateLimiter) *APIClient {
	return NewAPIClientWithEndpointGuard(baseURL, creds, rl, config.DefaultAutomatedValidationGuard())
}

// NewAPIClientWithEndpointGuard creates an API client with an explicit target
// policy. A nil policy fails closed by falling back to automated loopback-only
// validation.
func NewAPIClientWithEndpointGuard(baseURL string, creds *config.Credentials, rl ratelimiter.RateLimiter, endpointGuard config.EndpointGuard) *APIClient {
	if endpointGuard == nil {
		endpointGuard = config.DefaultAutomatedValidationGuard()
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		// InsecureSkipVerify defaults to false, ensuring certificate verification
	}

	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if err := endpointGuard.ValidateDialAddress(address); err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, address)
		},
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return endpointGuard.ValidateEndpoint(req.URL.String())
		},
	}

	return &APIClient{
		baseURL:       baseURL,
		credentials:   creds,
		httpClient:    httpClient,
		rateLimiter:   rl,
		endpointGuard: endpointGuard,
	}
}

// SignRequest generates an HMAC-SHA256 signature for OKX API authentication.
// The signature is computed as: Base64(HMAC-SHA256(secretKey, timestamp+method+path+body))
func (c *APIClient) SignRequest(method, path, body string, timestamp string) string {
	message := timestamp + method + path + body
	mac := hmac.New(sha256.New, []byte(c.credentials.SecretKey))
	mac.Write([]byte(message))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// hardTTL returns the instrument rules hard TTL for the gateway. The value is
// kept here to avoid a circular dependency.
func (c *APIClient) hardTTL() time.Duration {
	return 15 * time.Minute
}

// DoRequest executes a signed HTTP request to the OKX REST API with retry logic.
// It handles HTTP 429 with exponential backoff and HTTP 5xx with fixed interval retries.
// This method uses a background context; prefer DoRequestContext for deadline-aware calls.
func (c *APIClient) DoRequest(method, path string, body interface{}) (*http.Response, error) {
	return c.DoRequestContext(context.Background(), method, path, body)
}

// DoRequestContext executes a signed HTTP request with the given context for
// deadline/cancellation support. It handles HTTP 429 with exponential backoff
// and HTTP 5xx with fixed interval retries, respecting context cancellation.
func (c *APIClient) DoRequestContext(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	if c == nil || c.endpointGuard == nil {
		return nil, fmt.Errorf("request blocked: endpoint guard is required")
	}
	targetURL := c.baseURL + path
	// This check intentionally precedes body serialization, signing and
	// http.NewRequest so forbidden targets fail before request construction.
	if err := c.endpointGuard.ValidateEndpoint(targetURL); err != nil {
		return nil, fmt.Errorf("request blocked by endpoint guard: %w", err)
	}

	var bodyBytes []byte
	var err error

	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
	}

	bodyStr := string(bodyBytes)

	// Attempt the request with retry logic
	var lastErr error
	retries429 := 0
	retries5xx := 0

	for {
		// Respect context cancellation between retries
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("request cancelled: %w", err)
		}

		timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		signature := c.SignRequest(method, path, bodyStr, timestamp)

		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("OK-ACCESS-KEY", c.credentials.APIKey)
		req.Header.Set("OK-ACCESS-SIGN", signature)
		req.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
		req.Header.Set("OK-ACCESS-PASSPHRASE", c.credentials.Passphrase)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}

		// Handle HTTP 429 - Rate Limited
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			retries429++
			if retries429 > retry429MaxRetries {
				lastErr = fmt.Errorf("HTTP 429: rate limit exceeded after %d retries", retry429MaxRetries)
				break
			}
			backoff := time.Duration(float64(retry429InitialBackoff) * math.Pow(2, float64(retries429-1)))
			if backoff > retry429MaxBackoff {
				backoff = retry429MaxBackoff
			}
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("request cancelled during backoff: %w", ctx.Err())
			case <-time.After(backoff):
			}
			continue
		}

		// Handle HTTP 5xx - Server Error
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			retries5xx++
			if retries5xx > retry5xxMaxRetries {
				lastErr = fmt.Errorf("HTTP %d: server error after %d retries", resp.StatusCode, retry5xxMaxRetries)
				break
			}
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("request cancelled during retry: %w", ctx.Err())
			case <-time.After(retry5xxInterval):
			}
			continue
		}

		return resp, nil
	}

	return nil, lastErr
}

// OKXOrderRequest represents the OKX API request body for placing a single order.
type OKXOrderRequest struct {
	InstID  string `json:"instId"`
	TdMode  string `json:"tdMode"`
	Side    string `json:"side"`
	OrdType string `json:"ordType"`
	Px      string `json:"px,omitempty"`
	Sz      string `json:"sz"`
	ClOrdID string `json:"clOrdId,omitempty"`
}

// OKXResponse represents a generic OKX API response.
type OKXResponse struct {
	Code string          `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// OKXOrderData represents the order data in an OKX API order response.
type OKXOrderData struct {
	OrdID   string `json:"ordId"`
	ClOrdID string `json:"clOrdId"`
	SCode   string `json:"sCode"`
	SMsg    string `json:"sMsg"`
}

// OKXCancelRequest represents the OKX API request body for canceling an order.
type OKXCancelRequest struct {
	InstID  string `json:"instId"`
	OrdID   string `json:"ordId,omitempty"`
	ClOrdID string `json:"clOrdId,omitempty"`
}

// PlaceOrder sends a POST request to place a single order on OKX.
func (c *APIClient) PlaceOrder(req *OrderRequest) (*OrderResult, error) {
	if req == nil {
		return nil, fmt.Errorf("order request cannot be nil")
	}

	// Acquire rate limit token
	if c.rateLimiter != nil {
		if err := c.rateLimiter.TryAcquire("trade/order"); err != nil {
			return &OrderResult{
				Success: false,
				Error:   fmt.Sprintf("rate limit: %v", err),
			}, nil
		}
	}

	okxReq := &OKXOrderRequest{
		InstID:  req.Symbol,
		TdMode:  "cash",
		Side:    sideToOKX(req.Side),
		OrdType: orderTypeToOKX(req.OrderType),
		Px:      req.Price.String(),
		Sz:      req.Quantity.String(),
		ClOrdID: req.ClientOrderID,
	}

	resp, err := c.DoRequest("POST", "/api/v5/trade/order", okxReq)
	if err != nil {
		return &OrderResult{
			Success: false,
			Error:   err.Error(),
		}, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return &OrderResult{
			Success: false,
			Error:   fmt.Sprintf("failed to read response: %v", err),
		}, err
	}

	var okxResp OKXResponse
	if err := json.Unmarshal(bodyBytes, &okxResp); err != nil {
		return &OrderResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse response: %v", err),
		}, err
	}

	if okxResp.Code != "0" {
		// Try to extract detailed sub-error from data field
		errMsg := fmt.Sprintf("OKX error: code=%s, msg=%s", okxResp.Code, okxResp.Msg)
		var orderData []OKXOrderData
		if json.Unmarshal(okxResp.Data, &orderData) == nil && len(orderData) > 0 {
			errMsg = fmt.Sprintf("OKX error: code=%s, msg=%s, sCode=%s, sMsg=%s",
				okxResp.Code, okxResp.Msg, orderData[0].SCode, orderData[0].SMsg)
		}
		return &OrderResult{
			Success: false,
			Error:   errMsg,
		}, nil
	}

	var orderData []OKXOrderData
	if err := json.Unmarshal(okxResp.Data, &orderData); err != nil {
		return &OrderResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse order data: %v", err),
		}, err
	}

	if len(orderData) == 0 {
		return &OrderResult{
			Success: false,
			Error:   "no order data in response",
		}, nil
	}

	data := orderData[0]
	if data.SCode != "0" {
		return &OrderResult{
			Success: false,
			Error:   fmt.Sprintf("order error: code=%s, msg=%s", data.SCode, data.SMsg),
		}, nil
	}

	return &OrderResult{
		Success:         true,
		ExchangeOrderID: data.OrdID,
		OrderID:         data.ClOrdID,
	}, nil
}

// CancelOrder sends a POST request to cancel an order on OKX.
func (c *APIClient) CancelOrder(instID string, orderID string) (*CancelResult, error) {
	if orderID == "" {
		return nil, fmt.Errorf("order ID cannot be empty")
	}

	// Acquire rate limit token
	if c.rateLimiter != nil {
		if err := c.rateLimiter.TryAcquire("trade/cancel-order"); err != nil {
			return &CancelResult{
				Success: false,
				Error:   fmt.Sprintf("rate limit: %v", err),
			}, nil
		}
	}

	cancelReq := &OKXCancelRequest{
		InstID: instID,
		OrdID:  orderID,
	}

	resp, err := c.DoRequest("POST", "/api/v5/trade/cancel-order", cancelReq)
	if err != nil {
		return &CancelResult{
			Success: false,
			OrderID: orderID,
			Error:   err.Error(),
		}, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return &CancelResult{
			Success: false,
			OrderID: orderID,
			Error:   fmt.Sprintf("failed to read response: %v", err),
		}, err
	}

	var okxResp OKXResponse
	if err := json.Unmarshal(bodyBytes, &okxResp); err != nil {
		return &CancelResult{
			Success: false,
			OrderID: orderID,
			Error:   fmt.Sprintf("failed to parse response: %v", err),
		}, err
	}

	if okxResp.Code != "0" {
		return &CancelResult{
			Success: false,
			OrderID: orderID,
			Error:   fmt.Sprintf("OKX error: code=%s, msg=%s", okxResp.Code, okxResp.Msg),
		}, nil
	}

	return &CancelResult{
		Success: true,
		OrderID: orderID,
	}, nil
}

// BatchPlaceOrders places multiple orders in batches of up to 20 per request.
func (c *APIClient) BatchPlaceOrders(reqs []*OrderRequest) ([]*OrderResult, error) {
	if len(reqs) == 0 {
		return nil, fmt.Errorf("no orders to place")
	}

	results := make([]*OrderResult, 0, len(reqs))

	// Split into batches of maxBatchSize
	for i := 0; i < len(reqs); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(reqs) {
			end = len(reqs)
		}
		batch := reqs[i:end]

		batchResults, err := c.placeBatch(batch)
		if err != nil {
			// On batch failure, mark all remaining as failed
			for j := 0; j < len(batch); j++ {
				results = append(results, &OrderResult{
					Success: false,
					Error:   err.Error(),
				})
			}
			continue
		}
		results = append(results, batchResults...)
	}

	return results, nil
}

// placeBatch places a single batch of orders (up to 20).
func (c *APIClient) placeBatch(reqs []*OrderRequest) ([]*OrderResult, error) {
	// Acquire rate limit token
	if c.rateLimiter != nil {
		if err := c.rateLimiter.TryAcquire("trade/batch-orders"); err != nil {
			results := make([]*OrderResult, len(reqs))
			for i := range results {
				results[i] = &OrderResult{
					Success: false,
					Error:   fmt.Sprintf("rate limit: %v", err),
				}
			}
			return results, nil
		}
	}

	okxReqs := make([]*OKXOrderRequest, len(reqs))
	for i, req := range reqs {
		okxReqs[i] = &OKXOrderRequest{
			InstID:  req.Symbol,
			TdMode:  "cash",
			Side:    sideToOKX(req.Side),
			OrdType: orderTypeToOKX(req.OrderType),
			Px:      req.Price.String(),
			Sz:      req.Quantity.String(),
			ClOrdID: req.ClientOrderID,
		}
	}

	resp, err := c.DoRequest("POST", "/api/v5/trade/batch-orders", okxReqs)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var okxResp OKXResponse
	if err := json.Unmarshal(bodyBytes, &okxResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if okxResp.Code != "0" {
		return nil, fmt.Errorf("OKX batch error: code=%s, msg=%s", okxResp.Code, okxResp.Msg)
	}

	var orderData []OKXOrderData
	if err := json.Unmarshal(okxResp.Data, &orderData); err != nil {
		return nil, fmt.Errorf("failed to parse batch order data: %w", err)
	}

	results := make([]*OrderResult, len(orderData))
	for i, data := range orderData {
		if data.SCode == "0" {
			results[i] = &OrderResult{
				Success:         true,
				ExchangeOrderID: data.OrdID,
				OrderID:         data.ClOrdID,
			}
		} else {
			results[i] = &OrderResult{
				Success: false,
				Error:   fmt.Sprintf("order error: code=%s, msg=%s", data.SCode, data.SMsg),
			}
		}
	}

	return results, nil
}

// sideToOKX converts our Side type to OKX API side string.
func sideToOKX(side models.Side) string {
	switch side {
	case models.SideBuy:
		return "buy"
	case models.SideSell:
		return "sell"
	default:
		return "buy"
	}
}

// orderTypeToOKX converts our OrderType to OKX API ordType string.
func orderTypeToOKX(ot models.OrderType) string {
	switch ot {
	case models.OrderTypeLimit:
		return "limit"
	case models.OrderTypeMarket:
		return "market"
	case models.OrderTypePostOnly:
		return "post_only"
	default:
		return "limit"
	}
}
