package config

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

type guardRoundTripFunc func(*http.Request) (*http.Response, error)

func (f guardRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// **Validates: Requirements 2.14, 3.12**
func TestAutomatedValidationGuardRandomizedForbiddenCombinations(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		reason := rapid.IntRange(0, 3).Draw(t, "forbidden_reason")
		options := AutomatedValidationOptions{
			ExecutionMode: ExecutionModeUnit,
			Environment:   map[string]string{},
		}
		target := randomizedForbiddenEndpoint(t)
		secretValue := ""

		switch reason {
		case 0:
			// Endpoint itself is forbidden.
		case 1:
			target = fmt.Sprintf("http://127.0.0.1:%d", rapid.IntRange(1024, 65535).Draw(t, "port"))
			options.ExecutionMode = ExecutionModeProduction
		case 2:
			target = "http://[::1]:8080"
			options.TradingEnabled = true
		case 3:
			target = "http://localhost:8080"
			name := productionCredentialEnvironmentNames[rapid.IntRange(0, len(productionCredentialEnvironmentNames)-1).Draw(t, "credential_name")]
			secretValue = "secret-" + rapid.StringMatching(`[a-zA-Z0-9]{8,24}`).Draw(t, "credential_value")
			options.Environment[name] = secretValue
		}

		guard := NewAutomatedValidationGuard(options)
		request, err := guard.NewRequest(http.MethodGet, target, nil)
		if err == nil || request != nil {
			t.Fatalf("forbidden combination reached request construction: reason=%d target=%s request=%v", reason, target, request)
		}
		if secretValue != "" && strings.Contains(err.Error(), secretValue) {
			t.Fatalf("guard error leaked credential value: %v", err)
		}
	})
}

func randomizedForbiddenEndpoint(t *rapid.T) string {
	switch rapid.IntRange(0, 5).Draw(t, "endpoint_kind") {
	case 0:
		return "https://www.okx.com"
	case 1:
		prefix := rapid.StringMatching(`[a-z]{1,10}`).Draw(t, "okx_prefix")
		return "wss://" + prefix + ".okx.com/ws/v5/private"
	case 2:
		prefix := rapid.StringMatching(`[a-z]{1,10}`).Draw(t, "aws_prefix")
		return "https://" + prefix + ".ap-southeast-1.amazonaws.com"
	case 3:
		return "http://169.254.169.254/latest/meta-data"
	case 4:
		first := rapid.SampledFrom([]int{1, 8, 10, 100, 172, 192, 203, 223}).Draw(t, "first_octet")
		return fmt.Sprintf("http://%d.%d.%d.%d:%d", first,
			rapid.IntRange(0, 255).Draw(t, "octet_2"), rapid.IntRange(0, 255).Draw(t, "octet_3"),
			rapid.IntRange(1, 254).Draw(t, "octet_4"), rapid.IntRange(1, 65535).Draw(t, "ip_port"))
	default:
		host := rapid.StringMatching(`[a-z]{1,16}`).Draw(t, "public_host")
		return "https://" + host + ".example"
	}
}

// **Validates: Requirements 2.14, 3.12**
func TestAutomatedValidationGuardAllowsOnlyGeneratedLoopbackTargets(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		guard := NewAutomatedValidationGuard(AutomatedValidationOptions{
			ExecutionMode: ExecutionMode(rapid.SampledFrom([]string{"unit", "replay", "simulated"}).Draw(t, "mode")),
			Environment:   map[string]string{},
		})
		port := rapid.IntRange(1, 65535).Draw(t, "port")
		var target string
		if rapid.Bool().Draw(t, "ipv6") {
			target = fmt.Sprintf("http://[::1]:%d/simulator", port)
		} else {
			target = fmt.Sprintf("http://127.%d.%d.%d:%d/simulator",
				rapid.IntRange(0, 255).Draw(t, "octet_2"), rapid.IntRange(0, 255).Draw(t, "octet_3"),
				rapid.IntRange(1, 254).Draw(t, "octet_4"), port)
		}
		request, err := guard.NewRequest(http.MethodGet, target, nil)
		if err != nil || request == nil {
			t.Fatalf("loopback simulator target rejected: target=%s err=%v", target, err)
		}
	})
}

func TestAutomatedValidationGuardDemoRequiresIndependentOptIn(t *testing.T) {
	incomplete := NewAutomatedValidationGuard(AutomatedValidationOptions{
		ExecutionMode: ExecutionModeSimulated,
		Environment:   map[string]string{},
		Demo: &DemoValidationPolicy{
			Enabled:           true,
			EndpointAllowlist: []string{"https://www.okx.com"},
		},
	})
	if err := incomplete.ValidateEndpoint("https://www.okx.com"); err == nil {
		t.Fatal("incomplete demo policy allowed a non-loopback endpoint")
	}

	complete := NewAutomatedValidationGuard(AutomatedValidationOptions{
		ExecutionMode: ExecutionModeSimulated,
		Environment:   map[string]string{},
		Demo: &DemoValidationPolicy{
			Enabled:                     true,
			SimulatedTrading:            true,
			SimulatedTradingHeader:      "1",
			CredentialClass:             "non-production",
			EndpointAllowlist:           []string{"https://www.okx.com"},
			AccountFingerprint:          "approved-demo-account",
			ApprovedAccountFingerprints: []string{"approved-demo-account"},
		},
	})
	if err := complete.ValidateEndpoint("https://www.okx.com/api/v5/account/balance"); err != nil {
		t.Fatalf("fully approved demo endpoint rejected: %v", err)
	}
	if err := complete.ValidateEndpoint("http://169.254.169.254/latest/meta-data"); err == nil {
		t.Fatal("demo exception allowed the metadata endpoint")
	}
}

func TestForbiddenEndpointFailsBeforeTransportAndDial(t *testing.T) {
	guard := NewAutomatedValidationGuard(AutomatedValidationOptions{
		ExecutionMode: ExecutionModeUnit,
		Environment:   map[string]string{},
	})
	for _, target := range []string{
		"https://www.okx.com/api/v5/trade/order",
		"http://169.254.169.254/latest/meta-data",
		"https://ec2.ap-southeast-1.amazonaws.com",
		"http://8.8.8.8",
	} {
		t.Run(target, func(t *testing.T) {
			request, err := guard.NewRequest(http.MethodGet, target, nil)
			if err == nil || request != nil {
				t.Fatalf("forbidden target reached request construction: request=%v err=%v", request, err)
			}

			dialCalls := 0
			dial := guard.GuardDialContext(func(context.Context, string, string) (net.Conn, error) {
				dialCalls++
				return nil, fmt.Errorf("unexpected underlying dial")
			})
			host := strings.TrimPrefix(strings.Split(strings.TrimPrefix(target, "https://"), "/")[0], "http://")
			_, _ = dial(context.Background(), "tcp", net.JoinHostPort(strings.Trim(host, "[]"), "443"))
			if dialCalls != 0 {
				t.Fatalf("forbidden target reached underlying dial %d times", dialCalls)
			}

			transportCalls := 0
			wrapped := guard.WrapRoundTripper(guardRoundTripFunc(func(*http.Request) (*http.Response, error) {
				transportCalls++
				return nil, fmt.Errorf("unexpected transport")
			}))
			constructed, constructErr := http.NewRequest(http.MethodGet, target, nil)
			if constructErr != nil {
				t.Fatalf("fixture request: %v", constructErr)
			}
			_, _ = wrapped.RoundTrip(constructed)
			if transportCalls != 0 {
				t.Fatalf("forbidden target reached underlying transport %d times", transportCalls)
			}
		})
	}
}
