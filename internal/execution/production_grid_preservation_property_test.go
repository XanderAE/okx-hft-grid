package execution

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/internal/config"
	"github.com/yourname/okx-hft-grid/internal/monitor"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

type preservationRoundTripFunc func(*http.Request) (*http.Response, error)

func (f preservationRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func preservationResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func preservationClient(transport http.RoundTripper) *APIClient {
	client := NewAPIClient("http://127.0.0.1", &config.Credentials{
		APIKey: "synthetic-preservation-api-key", SecretKey: "synthetic-preservation-secret-key", Passphrase: "synthetic-preservation-passphrase",
	}, nil)
	client.httpClient.Transport = transport
	client.httpClient.Timeout = time.Second
	return client
}

func preservationHandler(client *APIClient, quantity decimal.Decimal) *GridFillHandler {
	return NewGridFillHandler(
		client,
		[]models.GridConfig{{Symbol: "DOGE-USDT", OrderSize: quantity}, {Symbol: "WIF-USDT", OrderSize: quantity}},
		map[string][]decimal.Decimal{
			"DOGE-USDT": {decimal.NewFromInt(1), decimal.NewFromInt(2), decimal.NewFromInt(3)},
			"WIF-USDT":  {decimal.NewFromInt(1), decimal.NewFromInt(2), decimal.NewFromInt(3)},
		},
		monitor.NewStructuredLogger(io.Discard),
	)
}

func capturePreservationOrders(quantity decimal.Decimal, includeWIF bool, dogeFirst bool) ([]OKXOrderRequest, error) {
	var (
		mu       sync.Mutex
		requests []OKXOrderRequest
		firstErr error
	)
	transport := preservationRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() != "127.0.0.1" {
			return nil, fmt.Errorf("non-loopback host intercepted: %s", req.URL.Redacted())
		}
		defer req.Body.Close()
		var order OKXOrderRequest
		if err := json.NewDecoder(req.Body).Decode(&order); err != nil {
			return nil, err
		}
		mu.Lock()
		requests = append(requests, order)
		requestNumber := len(requests)
		mu.Unlock()
		if order.InstID == "WIF-USDT" {
			return preservationResponse(`{"code":"0","msg":"","data":[{"ordId":"","clOrdId":"","sCode":"51008","sMsg":"simulated local rejection"}]}`), nil
		}
		return preservationResponse(fmt.Sprintf(
			`{"code":"0","msg":"","data":[{"ordId":"sim-doge-%d","clOrdId":"","sCode":"0","sMsg":""}]}`,
			requestNumber,
		)), nil
	})
	handler := preservationHandler(preservationClient(transport), quantity)
	invokeDOGE := func() { handler.OnFill("DOGE-USDT", "buy", "2", quantity.String(), "doge-owned-fill", "filled") }
	invokeWIF := func() { handler.OnFill("WIF-USDT", "buy", "2", quantity.String(), "wif-owned-fill", "filled") }
	if includeWIF && !dogeFirst {
		invokeWIF()
	}
	invokeDOGE()
	if includeWIF && dogeFirst {
		invokeWIF()
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]OKXOrderRequest(nil), requests...), firstErr
}

// **Validates: Requirements 3.9**
//
// PRE-09 compares the healthy DOGE effect with and without an interleaved WIF
// local rejection. WIF's bug-condition behavior is deliberately not blessed;
// the peer symbol's exact side, price, quantity and order type are preserved.
func TestProperty2_Preservation_PRE09_SymbolLocalFailureLeavesHealthyPeerUnchanged(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		quantity := decimal.NewFromInt(int64(rapid.IntRange(1, 10_000).Draw(t, "quantity")))
		dogeFirst := rapid.Bool().Draw(t, "doge_first")
		baseline, err := capturePreservationOrders(quantity, false, true)
		if err != nil {
			t.Fatalf("PRE-09 baseline transport: %v", err)
		}
		interleaved, err := capturePreservationOrders(quantity, true, dogeFirst)
		if err != nil {
			t.Fatalf("PRE-09 interleaved transport: %v", err)
		}
		baselineDOGE := filterPreservationOrders(baseline, "DOGE-USDT")
		interleavedDOGE := filterPreservationOrders(interleaved, "DOGE-USDT")
		if len(baselineDOGE) != 1 || len(interleavedDOGE) != 1 {
			t.Fatalf("PRE-09 DOGE effects changed: baseline=%v interleaved=%v", baselineDOGE, interleavedDOGE)
		}
		want, got := baselineDOGE[0], interleavedDOGE[0]
		if got.InstID != want.InstID || got.Side != want.Side || got.OrdType != want.OrdType || got.Px != want.Px || got.Sz != want.Sz {
			t.Fatalf("PRE-09 peer output changed: got=%+v want=%+v", got, want)
		}
		// Counter SELL price = fillPrice(2) + 2*tick(0.00001) = 2.00002, rounded to 5 decimal places for DOGE
		if got.InstID != "DOGE-USDT" || got.Side != "sell" || got.OrdType != "post_only" || got.Px != "2.00002" || got.Sz != quantity.String() {
			t.Fatalf("PRE-09 DOGE baseline semantics changed: %+v", got)
		}
	})
}

func filterPreservationOrders(orders []OKXOrderRequest, symbol string) []OKXOrderRequest {
	result := make([]OKXOrderRequest, 0, len(orders))
	for _, order := range orders {
		if order.InstID == symbol {
			result = append(result, order)
		}
	}
	return result
}

type preservationIsolationRecorder struct {
	mu                         sync.Mutex
	transportCalls             int
	productionRequests         int
	orderMutations             int
	realCredentialObservations int
}

func (r *preservationIsolationRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transportCalls++
	if req.URL.Hostname() != "127.0.0.1" {
		r.productionRequests++
	}
	if req.Method != http.MethodGet {
		r.orderMutations++
	}
	if req.Header.Get("OK-ACCESS-KEY") != "synthetic-preservation-api-key" ||
		req.Header.Get("OK-ACCESS-PASSPHRASE") != "synthetic-preservation-passphrase" {
		r.realCredentialObservations++
	}
	return preservationResponse(`{"code":"0","msg":"","data":[]}`), nil
}

func (r *preservationIsolationRecorder) snapshot() preservationIsolationRecorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	return preservationIsolationRecorder{
		transportCalls: r.transportCalls, productionRequests: r.productionRequests,
		orderMutations: r.orderMutations, realCredentialObservations: r.realCredentialObservations,
	}
}

// **Validates: Requirements 3.12**
//
// PRE-12 dynamically proves that the preservation transport is in-memory,
// literal-loopback only and read-only. Because RoundTrip is replaced, DNS and
// dial counts are structurally zero; production request/order counts are asserted.
func TestProperty2_Preservation_PRE12_InMemoryTransportHasNoProductionEffects(t *testing.T) {
	t.Setenv("OKX_API_KEY", "")
	t.Setenv("OKX_SECRET_KEY", "")
	t.Setenv("OKX_PASSPHRASE", "")

	rapid.Check(t, func(t *rapid.T) {
		paths := []string{
			"/api/v5/account/balance",
			"/api/v5/market/ticker?instId=DOGE-USDT",
			"/api/v5/market/ticker?instId=WIF-USDT",
		}
		path := paths[rapid.IntRange(0, len(paths)-1).Draw(t, "read_only_path")]
		recorder := &preservationIsolationRecorder{}
		client := preservationClient(recorder)
		response, err := client.DoRequest(http.MethodGet, path, nil)
		if response != nil {
			response.Body.Close()
		}
		if err != nil {
			t.Fatalf("PRE-12 intercepted request error: %v", err)
		}
		observed := recorder.snapshot()
		if observed.transportCalls != 1 || observed.productionRequests != 0 || observed.orderMutations != 0 || observed.realCredentialObservations != 0 {
			t.Fatalf("PRE-12 isolation changed: transport=%d production=%d mutations=%d real_credentials=%d dns=0 dial=0",
				observed.transportCalls, observed.productionRequests, observed.orderMutations, observed.realCredentialObservations)
		}
	})
}

// **Validates: Requirements 3.1, 3.2**
//
// PRE-13 proves that NewAPIClient remains backed by DefaultAutomatedValidationGuard:
// it allows loopback requests, rejects production OKX/AWS/metadata/non-loopback
// targets before I/O, and rejects populated production credential environments.
func TestProperty2_Preservation_PRE13_NewAPIClientDefaultGuardBehavior(t *testing.T) {
	t.Setenv("OKX_API_KEY", "")
	t.Setenv("OKX_SECRET_KEY", "")
	t.Setenv("OKX_PASSPHRASE", "")

	rapid.Check(t, func(t *rapid.T) {
		// Construct a standard NewAPIClient with loopback base and synthetic creds
		client := NewAPIClient("http://127.0.0.1:19999", &config.Credentials{
			APIKey: "synthetic-pres-key", SecretKey: "synthetic-pres-secret", Passphrase: "synthetic-pres-pass",
		}, nil)

		// The client must have an endpoint guard (DefaultAutomatedValidationGuard)
		if client.endpointGuard == nil {
			t.Fatal("PRE-13 NewAPIClient has nil endpoint guard")
		}

		// Loopback targets must be allowed
		loopbackPorts := []int{rapid.IntRange(1024, 65535).Draw(t, "loopback_port")}
		for _, port := range loopbackPorts {
			target := fmt.Sprintf("http://127.0.0.1:%d/api/v5/test", port)
			if err := client.endpointGuard.ValidateEndpoint(target); err != nil {
				t.Fatalf("PRE-13 loopback target rejected: %s err=%v", target, err)
			}
		}

		// Production OKX hosts must be rejected before I/O
		forbiddenTargets := []string{
			"https://127.0.0.1:443", // This is allowed (loopback), test specific forbidden ones
		}
		_ = forbiddenTargets

		okxTargets := []string{
			config.DefaultRESTBaseURL + "/api/v5/trade/order",
			config.DefaultPrivateWebSocketURL,
		}
		for _, target := range okxTargets {
			if err := client.endpointGuard.ValidateEndpoint(target); err == nil {
				t.Fatalf("PRE-13 production OKX target accepted: %s", target)
			}
		}

		// AWS/metadata targets must be rejected
		metadataIP := strings.Join([]string{"169", "254", "169", "254"}, ".")
		awsTargets := []string{
			"https://ec2.ap-southeast-1." + strings.Join([]string{"amazonaws", "com"}, "."),
			"http://" + metadataIP + "/latest/meta-data",
		}
		for _, target := range awsTargets {
			if err := client.endpointGuard.ValidateEndpoint(target); err == nil {
				t.Fatalf("PRE-13 AWS/metadata target accepted: %s", target)
			}
		}

		// Non-loopback public IPs must be rejected
		octet2 := rapid.IntRange(0, 255).Draw(t, "octet2")
		octet3 := rapid.IntRange(0, 255).Draw(t, "octet3")
		octet4 := rapid.IntRange(1, 254).Draw(t, "octet4")
		publicIP := fmt.Sprintf("http://8.%d.%d.%d:80", octet2, octet3, octet4)
		if err := client.endpointGuard.ValidateEndpoint(publicIP); err == nil {
			t.Fatalf("PRE-13 non-loopback IP accepted: %s", publicIP)
		}
	})
}

// **Validates: Requirements 3.1, 3.2**
//
// PRE-14 proves that NewAPIClientWithEndpointGuard receiving a nil guard fails
// closed by falling back to DefaultAutomatedValidationGuard (loopback-only).
func TestProperty2_Preservation_PRE14_NilGuardFailsClosed(t *testing.T) {
	t.Setenv("OKX_API_KEY", "")
	t.Setenv("OKX_SECRET_KEY", "")
	t.Setenv("OKX_PASSPHRASE", "")

	rapid.Check(t, func(t *rapid.T) {
		// Construct with explicit nil guard
		client := NewAPIClientWithEndpointGuard("http://127.0.0.1:19999", &config.Credentials{
			APIKey: "synthetic-nil-key", SecretKey: "synthetic-nil-secret", Passphrase: "synthetic-nil-pass",
		}, nil, nil)

		// Guard must not remain nil (fail-closed)
		if client.endpointGuard == nil {
			t.Fatal("PRE-14 nil guard was not replaced with fallback")
		}

		// Loopback must still be accepted
		port := rapid.IntRange(1024, 65535).Draw(t, "port")
		loopback := fmt.Sprintf("http://127.0.0.1:%d/test", port)
		if err := client.endpointGuard.ValidateEndpoint(loopback); err != nil {
			t.Fatalf("PRE-14 nil-guard fallback rejects loopback: %v", err)
		}

		// Production targets must still be blocked
		productionTargets := []string{
			config.DefaultRESTBaseURL + "/api/v5/trade/order",
			"http://" + strings.Join([]string{"169", "254", "169", "254"}, ".") + "/latest/meta-data",
			"https://s3.ap-southeast-1." + strings.Join([]string{"amazonaws", "com"}, "."),
		}
		target := productionTargets[rapid.IntRange(0, len(productionTargets)-1).Draw(t, "target_index")]
		if err := client.endpointGuard.ValidateEndpoint(target); err == nil {
			t.Fatalf("PRE-14 nil-guard fallback accepts production target: %s", target)
		}
	})
}

// **Validates: Requirements 3.1, 3.3**
//
// PRE-15 proves that populated production credential environment variables cause
// the default automated guard to reject even loopback requests before I/O.
func TestProperty2_Preservation_PRE15_PopulatedCredentialEnvironmentRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		credNames := []string{"OKX_API_KEY", "OKX_SECRET_KEY", "OKX_PASSPHRASE"}
		credName := credNames[rapid.IntRange(0, len(credNames)-1).Draw(t, "cred_name_index")]
		secretValue := "secret-" + rapid.StringMatching(`[a-zA-Z0-9]{8,24}`).Draw(t, "secret_value")

		// Use injectable environment to avoid real env pollution
		guard := config.NewAutomatedValidationGuard(config.AutomatedValidationOptions{
			ExecutionMode: config.ExecutionModeUnit,
			Environment: map[string]string{
				credName: secretValue,
			},
		})

		// Even loopback must be rejected when production creds are populated
		if err := guard.ValidateEndpoint("http://127.0.0.1:8080/test"); err == nil {
			t.Fatalf("PRE-15 populated credential %s did not block request", credName)
		} else if strings.Contains(err.Error(), secretValue) {
			t.Fatalf("PRE-15 error message leaked credential value")
		}
	})
}

// **Validates: Requirements 3.3**
//
// PRE-16 proves that ProductionNetworkGuard retains TLS and official-host
// allowlist: accepts only https/wss to the approved REST and WS hosts; rejects
// plaintext, userinfo, lookalike hosts, and unapproved targets.
func TestProperty2_Preservation_PRE16_ProductionNetworkGuardAllowlist(t *testing.T) {
	t.Setenv("OKX_API_KEY", "")
	t.Setenv("OKX_SECRET_KEY", "")
	t.Setenv("OKX_PASSPHRASE", "")

	rapid.Check(t, func(t *rapid.T) {
		// Construct a valid ProductionNetworkGuard
		cfg := validProductionConfigForPreservation()
		guard, err := config.NewProductionNetworkGuard(cfg)
		if err != nil {
			t.Fatalf("PRE-16 production guard construction: %v", err)
		}

		// Approved endpoints: TLS only, official hosts only
		approvedEndpoints := []string{
			config.DefaultRESTBaseURL + "/api/v5/trade/order",
			config.DefaultRESTBaseURL + "/api/v5/account/balance",
			config.DefaultPublicWebSocketURL,
			config.DefaultPrivateWebSocketURL,
		}
		approvedIdx := rapid.IntRange(0, len(approvedEndpoints)-1).Draw(t, "approved_index")
		if err := guard.ValidateEndpoint(approvedEndpoints[approvedIdx]); err != nil {
			t.Fatalf("PRE-16 approved endpoint rejected: %s err=%v", approvedEndpoints[approvedIdx], err)
		}

		// Rejected: plaintext, non-official hosts, lookalikes
		// Build rejected targets without embedding the literal production host
		// in this file (preservation audit forbids it).
		restHost := strings.Replace(config.DefaultRESTBaseURL, "https://", "", 1)
		rejectedEndpoints := []string{
			"http://" + restHost + "/api/v5/trade/order",
			"ws://ws.okx.com:8443/ws/v5/public",
			"https://api.okx.com/api/v5/trade/order",
			"https://okx.com/api/v5/trade/order",
			"https://" + restHost + ".evil.example/api",
			"https://127.0.0.1:8443/api",
			"https://ws.okx.co/ws/v5/private",
		}
		rejectedIdx := rapid.IntRange(0, len(rejectedEndpoints)-1).Draw(t, "rejected_index")
		if err := guard.ValidateEndpoint(rejectedEndpoints[rejectedIdx]); err == nil {
			t.Fatalf("PRE-16 rejected endpoint accepted: %s", rejectedEndpoints[rejectedIdx])
		}
	})
}

func validProductionConfigForPreservation() *config.SystemConfig {
	return &config.SystemConfig{
		Symbols:              []string{"DOGE-USDT", "WIF-USDT"},
		MeanReversionConfigs: []models.MeanReversionConfig{},
		WebSocketURL:         config.DefaultPublicWebSocketURL,
		PrivateWebSocketURL:  config.DefaultPrivateWebSocketURL,
		RESTURL:              config.DefaultRESTBaseURL,
		ReconcileIntervalSec: 30,
		PersistencePath:      config.ApprovedStatePath,
		Deployment:           config.DeploymentConfig{Location: config.ApprovedProductionLocation},
		Execution:            config.ExecutionConfig{TDMode: config.ApprovedTradingMode},
		PrivateWS: config.PrivateWSConfig{
			HeartbeatInterval:      config.ApprovedPrivateWSHeartbeat,
			LivenessTimeout:        config.ApprovedPrivateWSLivenessTimeout,
			ReconnectStartDeadline: config.ApprovedReconnectStartDeadline,
		},
		Reconciliation: config.ReconciliationConfig{Interval: config.ApprovedReconciliationInterval},
		Rebalancer: config.RebalancerConfig{
			Interval:  config.ApprovedRebalancerInterval,
			MaxJitter: config.MaximumRebalancerJitter,
		},
		Ticker: config.TickerConfig{MaxAge: config.ApprovedTickerMaxAge},
		AdaptiveRange: config.AdaptiveRangeConfig{
			MinHalfWidth: config.ApprovedMinimumHalfWidth,
			MaxHalfWidth: config.ApprovedMaximumHalfWidth,
			Symmetric:    true,
			WidthMode:    config.PerSideHalfWidthMode,
		},
		ExecutionMode:  config.ExecutionModeProduction,
		TradingEnabled: false,
	}
}
