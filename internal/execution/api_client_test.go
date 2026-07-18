package execution

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/internal/config"
	"github.com/yourname/okx-hft-grid/pkg/models"
)

// mockRateLimiter is a no-op rate limiter for testing.
type mockRateLimiter struct{}

func (m *mockRateLimiter) TryAcquire(endpoint string) error                   { return nil }
func (m *mockRateLimiter) GetNextAvailableTime(endpoint string) time.Duration { return 0 }

func newTestClient(serverURL string) *APIClient {
	creds := &config.Credentials{
		APIKey:     "test-api-key",
		SecretKey:  "test-secret-key",
		Passphrase: "test-passphrase",
	}
	client := NewAPIClient(serverURL, creds, &mockRateLimiter{})
	return client
}

func TestSignRequest_Correctness(t *testing.T) {
	creds := &config.Credentials{
		APIKey:     "my-api-key",
		SecretKey:  "my-secret-key",
		Passphrase: "my-passphrase",
	}
	client := NewAPIClient("https://www.okx.com", creds, &mockRateLimiter{})

	method := "POST"
	path := "/api/v5/trade/order"
	body := `{"instId":"BTC-USDT","tdMode":"cross","side":"buy","ordType":"limit","px":"50000","sz":"0.01"}`
	timestamp := "2024-01-15T10:30:00.000Z"

	signature := client.SignRequest(method, path, body, timestamp)

	// Verify manually: HMAC-SHA256(secretKey, timestamp+method+path+body)
	message := timestamp + method + path + body
	mac := hmac.New(sha256.New, []byte(creds.SecretKey))
	mac.Write([]byte(message))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if signature != expected {
		t.Errorf("SignRequest mismatch:\n  got:  %s\n  want: %s", signature, expected)
	}
}

func TestSignRequest_DifferentInputsProduceDifferentSignatures(t *testing.T) {
	creds := &config.Credentials{
		APIKey:     "key",
		SecretKey:  "secret",
		Passphrase: "pass",
	}
	client := NewAPIClient("https://www.okx.com", creds, &mockRateLimiter{})

	sig1 := client.SignRequest("POST", "/api/v5/trade/order", `{"a":"1"}`, "2024-01-15T10:30:00.000Z")
	sig2 := client.SignRequest("POST", "/api/v5/trade/order", `{"a":"2"}`, "2024-01-15T10:30:00.000Z")
	sig3 := client.SignRequest("GET", "/api/v5/trade/order", `{"a":"1"}`, "2024-01-15T10:30:00.000Z")

	if sig1 == sig2 {
		t.Error("Different bodies should produce different signatures")
	}
	if sig1 == sig3 {
		t.Error("Different methods should produce different signatures")
	}
}

func TestDoRequest_SuccessfulRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers are set
		if r.Header.Get("OK-ACCESS-KEY") == "" {
			t.Error("Missing OK-ACCESS-KEY header")
		}
		if r.Header.Get("OK-ACCESS-SIGN") == "" {
			t.Error("Missing OK-ACCESS-SIGN header")
		}
		if r.Header.Get("OK-ACCESS-TIMESTAMP") == "" {
			t.Error("Missing OK-ACCESS-TIMESTAMP header")
		}
		if r.Header.Get("OK-ACCESS-PASSPHRASE") == "" {
			t.Error("Missing OK-ACCESS-PASSPHRASE header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("Missing Content-Type header")
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"12345","clOrdId":"client1","sCode":"0","sMsg":""}]}`))
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	resp, err := client.DoRequest("POST", "/api/v5/trade/order", map[string]string{"instId": "BTC-USDT"})
	if err != nil {
		t.Fatalf("DoRequest failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
}

func TestDoRequest_429RetryWithExponentialBackoff(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	start := time.Now()
	resp, err := client.DoRequest("POST", "/api/v5/trade/order", nil)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("DoRequest should succeed after retries: %v", err)
	}
	defer resp.Body.Close()

	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("Expected 3 attempts, got %d", atomic.LoadInt32(&attempts))
	}

	// Should have waited at least 100ms + 200ms = 300ms for 2 retries
	if elapsed < 300*time.Millisecond {
		t.Errorf("Expected at least 300ms of backoff, got %v", elapsed)
	}
}

func TestDoRequest_429RetryExhaustion(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	resp, err := client.DoRequest("POST", "/api/v5/trade/order", nil)

	if err == nil {
		resp.Body.Close()
		t.Fatal("Expected error after retry exhaustion")
	}

	// 1 initial attempt + 3 retries = 4 total attempts
	if atomic.LoadInt32(&attempts) != 4 {
		t.Errorf("Expected 4 attempts (1 + 3 retries), got %d", atomic.LoadInt32(&attempts))
	}
}

func TestDoRequest_5xxRetry(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	resp, err := client.DoRequest("POST", "/api/v5/trade/order", nil)

	if err != nil {
		t.Fatalf("DoRequest should succeed after 5xx retries: %v", err)
	}
	defer resp.Body.Close()

	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("Expected 3 attempts, got %d", atomic.LoadInt32(&attempts))
	}
}

func TestDoRequest_5xxRetryExhaustion(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	resp, err := client.DoRequest("POST", "/api/v5/trade/order", nil)

	if err == nil {
		resp.Body.Close()
		t.Fatal("Expected error after 5xx retry exhaustion")
	}

	// 1 initial attempt + 3 retries = 4 total attempts
	if atomic.LoadInt32(&attempts) != 4 {
		t.Errorf("Expected 4 attempts (1 + 3 retries), got %d", atomic.LoadInt32(&attempts))
	}
}

func TestPlaceOrder_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/trade/order" {
			t.Errorf("Expected path /api/v5/trade/order, got %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("Expected POST, got %s", r.Method)
		}

		w.WriteHeader(http.StatusOK)
		resp := OKXResponse{
			Code: "0",
			Msg:  "",
		}
		data, _ := json.Marshal([]OKXOrderData{{OrdID: "okx-123", ClOrdID: "local-456", SCode: "0", SMsg: ""}})
		resp.Data = data
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	result, err := client.PlaceOrder(&OrderRequest{
		Symbol:    "BTC-USDT",
		Side:      models.SideBuy,
		OrderType: models.OrderTypeLimit,
		Price:     decimal.NewFromFloat(50000),
		Quantity:  decimal.NewFromFloat(0.01),
	})

	if err != nil {
		t.Fatalf("PlaceOrder failed: %v", err)
	}
	if !result.Success {
		t.Fatalf("PlaceOrder not successful: %s", result.Error)
	}
	if result.ExchangeOrderID != "okx-123" {
		t.Errorf("Expected exchange order ID 'okx-123', got '%s'", result.ExchangeOrderID)
	}
}

func TestPlaceOrder_OKXError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		resp := OKXResponse{
			Code: "51000",
			Msg:  "Parameter error",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	result, err := client.PlaceOrder(&OrderRequest{
		Symbol:    "BTC-USDT",
		Side:      models.SideBuy,
		OrderType: models.OrderTypeLimit,
		Price:     decimal.NewFromFloat(50000),
		Quantity:  decimal.NewFromFloat(0.01),
	})

	if err != nil {
		t.Fatalf("PlaceOrder should not return transport error: %v", err)
	}
	if result.Success {
		t.Fatal("PlaceOrder should not be successful with OKX error code")
	}
	if result.Error == "" {
		t.Fatal("Expected error message")
	}
}

func TestCancelOrder_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/trade/cancel-order" {
			t.Errorf("Expected path /api/v5/trade/cancel-order, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		resp := OKXResponse{Code: "0", Msg: ""}
		resp.Data, _ = json.Marshal([]OKXOrderData{{OrdID: "okx-123", SCode: "0"}})
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestClient(server.URL)
	result, err := client.CancelOrder("BTC-USDT", "okx-123")

	if err != nil {
		t.Fatalf("CancelOrder failed: %v", err)
	}
	if !result.Success {
		t.Fatalf("CancelOrder not successful: %s", result.Error)
	}
	if result.OrderID != "okx-123" {
		t.Errorf("Expected order ID 'okx-123', got '%s'", result.OrderID)
	}
}

func TestBatchPlaceOrders_MaxBatchSize(t *testing.T) {
	var batchCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&batchCount, 1)

		// Parse request to count orders in batch
		var orders []OKXOrderRequest
		json.NewDecoder(r.Body).Decode(&orders)

		if len(orders) > maxBatchSize {
			t.Errorf("Batch exceeded max size: got %d, max %d", len(orders), maxBatchSize)
		}

		w.WriteHeader(http.StatusOK)
		data := make([]OKXOrderData, len(orders))
		for i := range data {
			data[i] = OKXOrderData{OrdID: fmt.Sprintf("ord-%d", i), ClOrdID: fmt.Sprintf("cl-%d", i), SCode: "0"}
		}
		resp := OKXResponse{Code: "0", Msg: ""}
		resp.Data, _ = json.Marshal(data)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestClient(server.URL)

	// Create 25 orders (should split into 20 + 5)
	orders := make([]*OrderRequest, 25)
	for i := range orders {
		orders[i] = &OrderRequest{
			Symbol:    "BTC-USDT",
			Side:      models.SideBuy,
			OrderType: models.OrderTypeLimit,
			Price:     decimal.NewFromFloat(50000),
			Quantity:  decimal.NewFromFloat(0.01),
		}
	}

	results, err := client.BatchPlaceOrders(orders)
	if err != nil {
		t.Fatalf("BatchPlaceOrders failed: %v", err)
	}

	if len(results) != 25 {
		t.Errorf("Expected 25 results, got %d", len(results))
	}

	if atomic.LoadInt32(&batchCount) != 2 {
		t.Errorf("Expected 2 batch API calls, got %d", atomic.LoadInt32(&batchCount))
	}
}

func TestNewAPIClient_TLSConfiguration(t *testing.T) {
	creds := &config.Credentials{
		APIKey:     "key",
		SecretKey:  "secret",
		Passphrase: "pass",
	}

	client := NewAPIClient("https://www.okx.com", creds, &mockRateLimiter{})

	if client.httpClient == nil {
		t.Fatal("httpClient should not be nil")
	}

	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport should be *http.Transport")
	}

	if transport.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig should not be nil")
	}

	if transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("Expected MinVersion TLS 1.2 (%d), got %d", tls.VersionTLS12, transport.TLSClientConfig.MinVersion)
	}

	// InsecureSkipVerify should be false (default) to verify server certificates
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should be false to enforce certificate verification")
	}
}

func TestPlaceOrder_NilRequest(t *testing.T) {
	client := newTestClient("http://localhost")
	_, err := client.PlaceOrder(nil)
	if err == nil {
		t.Fatal("Expected error for nil request")
	}
}

func TestCancelOrder_EmptyOrderID(t *testing.T) {
	client := newTestClient("http://localhost")
	_, err := client.CancelOrder("BTC-USDT", "")
	if err == nil {
		t.Fatal("Expected error for empty order ID")
	}
}

func TestSignRequest_TimestampInSignature(t *testing.T) {
	creds := &config.Credentials{
		APIKey:     "key",
		SecretKey:  "secret",
		Passphrase: "pass",
	}
	client := NewAPIClient("https://www.okx.com", creds, &mockRateLimiter{})

	// Same request with different timestamps should produce different signatures
	sig1 := client.SignRequest("POST", "/api/v5/trade/order", `{}`, "2024-01-15T10:30:00.000Z")
	sig2 := client.SignRequest("POST", "/api/v5/trade/order", `{}`, "2024-01-15T10:30:01.000Z")

	if sig1 == sig2 {
		t.Error("Different timestamps should produce different signatures")
	}
}
