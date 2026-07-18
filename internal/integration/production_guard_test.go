package integration

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/yourname/okx-hft-grid/internal/config"
)

type integrationRoundTripFunc func(*http.Request) (*http.Response, error)

func (f integrationRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestForbiddenEndpointIntegrationHasZeroProductionIO(t *testing.T) {
	guard := config.NewAutomatedValidationGuard(config.AutomatedValidationOptions{
		ExecutionMode: config.ExecutionModeReplay,
		Environment:   map[string]string{},
	})

	dnsCalls := 0
	dialCalls := 0
	requestCalls := 0
	dial := guard.GuardDialContext(func(context.Context, string, string) (net.Conn, error) {
		dialCalls++
		dnsCalls++
		return nil, fmt.Errorf("unexpected network")
	})
	wrapped := guard.WrapRoundTripper(integrationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requestCalls++
		return nil, fmt.Errorf("unexpected request")
	}))

	for _, test := range []struct {
		url     string
		address string
	}{
		{"https://www.okx.com/api/v5/trade/order", "www.okx.com:443"},
		{"http://169.254.169.254/latest/meta-data", "169.254.169.254:80"},
		{"https://ec2.ap-southeast-1.amazonaws.com", "ec2.ap-southeast-1.amazonaws.com:443"},
		{"http://203.0.113.10", "203.0.113.10:80"},
	} {
		request, err := guard.NewRequest(http.MethodPost, test.url, nil)
		if err == nil || request != nil {
			t.Fatalf("forbidden endpoint reached request construction: %s", test.url)
		}
		_, _ = dial(context.Background(), "tcp", test.address)

		fixtureRequest, fixtureErr := http.NewRequest(http.MethodGet, test.url, nil)
		if fixtureErr != nil {
			t.Fatalf("build in-memory fixture request: %v", fixtureErr)
		}
		_, _ = wrapped.RoundTrip(fixtureRequest)
	}

	if dnsCalls != 0 || dialCalls != 0 || requestCalls != 0 {
		t.Fatalf("production I/O recorder is non-zero: dns=%d dial=%d request=%d", dnsCalls, dialCalls, requestCalls)
	}
}

func TestNoSedDependencyIntegrationLoadsImmutableProfile(t *testing.T) {
	profile := filepath.Join("..", "..", "deploy", "config.example.yaml")
	cfg, err := config.LoadProductionConfig(profile)
	if err != nil {
		t.Fatalf("load immutable production profile: %v", err)
	}
	summary, err := config.EffectiveConfigSanitized(cfg)
	if err != nil {
		t.Fatalf("build effective production summary: %v", err)
	}
	if summary == "" || cfg.TradingEnabled {
		t.Fatalf("invalid reconcile-only production profile: trading=%t summary=%q", cfg.TradingEnabled, summary)
	}
}
