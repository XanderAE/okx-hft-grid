package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// **Validates: Requirements 2.2, 2.3, 2.4, 3.3, 3.6, 3.7**

// --- Table Tests ---

func TestResolveNetworkEndpoints_Defaults(t *testing.T) {
	// Blank fields resolve to role-specific defaults
	cfg := &SystemConfig{}

	resolved, err := ResolveNetworkEndpoints(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.RESTBaseURL != DefaultRESTBaseURL {
		t.Fatalf("REST default: got %q, want %q", resolved.RESTBaseURL, DefaultRESTBaseURL)
	}
	if resolved.PublicWebSocketURL != DefaultPublicWebSocketURL {
		t.Fatalf("Public WS default: got %q, want %q", resolved.PublicWebSocketURL, DefaultPublicWebSocketURL)
	}
	if resolved.PrivateWebSocketURL != DefaultPrivateWebSocketURL {
		t.Fatalf("Private WS default: got %q, want %q", resolved.PrivateWebSocketURL, DefaultPrivateWebSocketURL)
	}
	// Public and private MUST be distinct
	if resolved.PublicWebSocketURL == resolved.PrivateWebSocketURL {
		t.Fatalf("defaults must be distinct: public=%q private=%q", resolved.PublicWebSocketURL, resolved.PrivateWebSocketURL)
	}
}

func TestResolveNetworkEndpoints_NilConfig(t *testing.T) {
	_, err := ResolveNetworkEndpoints(nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestResolveNetworkEndpoints_ExplicitProductionValues(t *testing.T) {
	cfg := validProductionConfigForTest()
	cfg.PrivateWebSocketURL = DefaultPrivateWebSocketURL

	resolved, err := ResolveNetworkEndpoints(cfg)
	if err != nil {
		t.Fatalf("valid production endpoints rejected: %v", err)
	}
	if resolved.RESTBaseURL != "https://www.okx.com" {
		t.Fatalf("REST: got %q", resolved.RESTBaseURL)
	}
	if resolved.PublicWebSocketURL != "wss://ws.okx.com:8443/ws/v5/public" {
		t.Fatalf("Public WS: got %q", resolved.PublicWebSocketURL)
	}
	if resolved.PrivateWebSocketURL != "wss://ws.okx.com:8443/ws/v5/private" {
		t.Fatalf("Private WS: got %q", resolved.PrivateWebSocketURL)
	}
}

func TestResolveNetworkEndpoints_PublicNeverUsedAsPrivate(t *testing.T) {
	// If private is blank but public is set, private gets the default, not the public value
	cfg := &SystemConfig{
		WebSocketURL: "wss://ws.okx.com:8443/ws/v5/public",
	}
	resolved, err := ResolveNetworkEndpoints(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.PrivateWebSocketURL == resolved.PublicWebSocketURL {
		t.Fatalf("private should not copy public: got %q", resolved.PrivateWebSocketURL)
	}
	if resolved.PrivateWebSocketURL != DefaultPrivateWebSocketURL {
		t.Fatalf("private should resolve to default: got %q", resolved.PrivateWebSocketURL)
	}
}

func TestProductionEndpointRoles_InvalidSchemes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SystemConfig)
	}{
		{"plaintext REST", func(c *SystemConfig) { c.RESTURL = "http://www.okx.com" }},
		{"plaintext public ws", func(c *SystemConfig) { c.WebSocketURL = "ws://ws.okx.com:8443/ws/v5/public" }},
		{"plaintext private ws", func(c *SystemConfig) { c.PrivateWebSocketURL = "ws://ws.okx.com:8443/ws/v5/private" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validProductionConfigForTest()
			cfg.PrivateWebSocketURL = DefaultPrivateWebSocketURL
			tt.mutate(cfg)
			_, err := ResolveNetworkEndpoints(cfg)
			if err == nil {
				t.Fatal("expected error for plaintext scheme")
			}
		})
	}
}

func TestProductionEndpointRoles_InvalidHosts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SystemConfig)
	}{
		{"REST wrong host", func(c *SystemConfig) { c.RESTURL = "https://api.okx.com" }},
		{"REST lookalike", func(c *SystemConfig) { c.RESTURL = "https://www.0kx.com" }},
		{"WS wrong host", func(c *SystemConfig) { c.WebSocketURL = "wss://ws2.okx.com:8443/ws/v5/public" }},
		{"Private WS wrong host", func(c *SystemConfig) { c.PrivateWebSocketURL = "wss://private.okx.com:8443/ws/v5/private" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validProductionConfigForTest()
			cfg.PrivateWebSocketURL = DefaultPrivateWebSocketURL
			tt.mutate(cfg)
			_, err := ResolveNetworkEndpoints(cfg)
			if err == nil {
				t.Fatal("expected error for wrong host")
			}
		})
	}
}

func TestProductionEndpointRoles_InvalidPorts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SystemConfig)
	}{
		{"REST explicit port", func(c *SystemConfig) { c.RESTURL = "https://www.okx.com:443" }},
		{"WS wrong port", func(c *SystemConfig) { c.WebSocketURL = "wss://ws.okx.com:8444/ws/v5/public" }},
		{"Private WS wrong port", func(c *SystemConfig) { c.PrivateWebSocketURL = "wss://ws.okx.com:443/ws/v5/private" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validProductionConfigForTest()
			cfg.PrivateWebSocketURL = DefaultPrivateWebSocketURL
			tt.mutate(cfg)
			_, err := ResolveNetworkEndpoints(cfg)
			if err == nil {
				t.Fatal("expected error for wrong port")
			}
		})
	}
}

func TestProductionEndpointRoles_InvalidPaths(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SystemConfig)
	}{
		{"REST with path", func(c *SystemConfig) { c.RESTURL = "https://www.okx.com/api/v5" }},
		{"WS wrong path", func(c *SystemConfig) { c.WebSocketURL = "wss://ws.okx.com:8443/ws/v5/trade" }},
		{"Private WS wrong path", func(c *SystemConfig) { c.PrivateWebSocketURL = "wss://ws.okx.com:8443/ws/v5/trade" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validProductionConfigForTest()
			cfg.PrivateWebSocketURL = DefaultPrivateWebSocketURL
			tt.mutate(cfg)
			_, err := ResolveNetworkEndpoints(cfg)
			if err == nil {
				t.Fatal("expected error for wrong path")
			}
		})
	}
}

func TestProductionEndpointRoles_Userinfo(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SystemConfig)
	}{
		{"REST userinfo", func(c *SystemConfig) { c.RESTURL = "https://user:pass@www.okx.com" }},
		{"WS userinfo", func(c *SystemConfig) { c.WebSocketURL = "wss://user:pass@ws.okx.com:8443/ws/v5/public" }},
		{"Private WS userinfo", func(c *SystemConfig) { c.PrivateWebSocketURL = "wss://key:secret@ws.okx.com:8443/ws/v5/private" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validProductionConfigForTest()
			cfg.PrivateWebSocketURL = DefaultPrivateWebSocketURL
			tt.mutate(cfg)
			_, err := ResolveNetworkEndpoints(cfg)
			if err == nil {
				t.Fatal("expected error for userinfo")
			}
		})
	}
}

func TestProductionEndpointRoles_QueryAndFragment(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SystemConfig)
	}{
		{"REST query", func(c *SystemConfig) { c.RESTURL = "https://www.okx.com?token=secret" }},
		{"WS query", func(c *SystemConfig) { c.WebSocketURL = "wss://ws.okx.com:8443/ws/v5/public?api_key=x" }},
		{"Private WS fragment", func(c *SystemConfig) { c.PrivateWebSocketURL = "wss://ws.okx.com:8443/ws/v5/private#section" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validProductionConfigForTest()
			cfg.PrivateWebSocketURL = DefaultPrivateWebSocketURL
			tt.mutate(cfg)
			_, err := ResolveNetworkEndpoints(cfg)
			if err == nil {
				t.Fatal("expected error for query/fragment")
			}
		})
	}
}

func TestProductionEndpointRoles_RoleSwap(t *testing.T) {
	// Public URL has private path
	cfg := validProductionConfigForTest()
	cfg.WebSocketURL = "wss://ws.okx.com:8443/ws/v5/private"
	cfg.PrivateWebSocketURL = "wss://ws.okx.com:8443/ws/v5/public"
	_, err := ResolveNetworkEndpoints(cfg)
	if err == nil {
		t.Fatal("expected error for role swap")
	}
	if !strings.Contains(err.Error(), "role swap") && !strings.Contains(err.Error(), "path") {
		t.Fatalf("error should indicate role swap or wrong path, got: %v", err)
	}
}

func TestProductionEndpointRoles_EqualPublicPrivate(t *testing.T) {
	cfg := validProductionConfigForTest()
	cfg.WebSocketURL = "wss://ws.okx.com:8443/ws/v5/public"
	cfg.PrivateWebSocketURL = "wss://ws.okx.com:8443/ws/v5/public"
	_, err := ResolveNetworkEndpoints(cfg)
	if err == nil {
		t.Fatal("expected error for equal public/private")
	}
	if !strings.Contains(err.Error(), "path") && !strings.Contains(err.Error(), "distinct") {
		t.Fatalf("error should indicate wrong path or non-distinct, got: %v", err)
	}
}

func TestProductionEndpointRoles_MalformedURLs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SystemConfig)
	}{
		{"REST malformed", func(c *SystemConfig) { c.RESTURL = "://not-a-url" }},
		{"WS malformed", func(c *SystemConfig) { c.WebSocketURL = "not a url at all" }},
		{"Private WS malformed", func(c *SystemConfig) { c.PrivateWebSocketURL = "\x00bad" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validProductionConfigForTest()
			cfg.PrivateWebSocketURL = DefaultPrivateWebSocketURL
			tt.mutate(cfg)
			_, err := ResolveNetworkEndpoints(cfg)
			if err == nil {
				t.Fatal("expected error for malformed URL")
			}
		})
	}
}

func TestStrictPrivateWebSocketField(t *testing.T) {
	// The private_websocket_url field must be accepted by strict YAML decoding
	profilePath := filepath.Join("..", "..", "deploy", "config.example.yaml")
	cfg, err := LoadProductionConfig(profilePath)
	if err != nil {
		t.Fatalf("versioned profile with private_websocket_url must load: %v", err)
	}
	if cfg.PrivateWebSocketURL != DefaultPrivateWebSocketURL {
		t.Fatalf("private_websocket_url not loaded from profile: got %q", cfg.PrivateWebSocketURL)
	}

	// Unknown field still rejects
	contents, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	contents = append(contents, []byte("\nunknown_ws_field: true\n")...)
	path := filepath.Join(t.TempDir(), "unknown.yaml")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write strict profile: %v", err)
	}
	_, err = LoadProductionConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown_ws_field") {
		t.Fatalf("unknown field was not rejected: %v", err)
	}
}

func TestEffectiveConfigEndpointRolesSanitized(t *testing.T) {
	// Test that production mode summary includes role-labelled endpoint fields
	cfg := validProductionConfigForTest()
	cfg.TradingEnabled = true
	cfg.ProductionGates = completeProductionGatesForTest()
	cfg.PrivateWebSocketURL = DefaultPrivateWebSocketURL

	summary, err := EffectiveConfigSanitized(cfg)
	if err != nil {
		t.Fatalf("effective config summary: %v", err)
	}

	// Must contain the separate role fields
	if !strings.Contains(summary, "public_websocket_origin") {
		t.Fatalf("summary missing public_websocket_origin field: %s", summary)
	}
	if !strings.Contains(summary, "private_websocket_origin") {
		t.Fatalf("summary missing private_websocket_origin field: %s", summary)
	}
	// Must have sanitized origins (scheme://host:port only)
	if !strings.Contains(summary, "wss://ws.okx.com:8443") {
		t.Fatalf("summary missing sanitized WS origin: %s", summary)
	}

	// For secret-leakage testing in URLs, use non-production mode to bypass
	// endpoint validation (which correctly rejects userinfo/query/fragment).
	nonProdCfg := *cfg
	nonProdCfg.ExecutionMode = ExecutionModeUnit
	nonProdCfg.RESTURL = "https://api-user:api-secret@www.okx.com/internal?token=secret"
	nonProdCfg.WebSocketURL = "wss://ws-user:ws-pass@ws.okx.com:8443/ws/v5/public?sig=secret"
	nonProdCfg.PrivateWebSocketURL = "wss://priv-user:priv-pass@ws.okx.com:8443/ws/v5/private?key=abcdef123456"

	summary2, err := EffectiveConfigSanitized(&nonProdCfg)
	if err != nil {
		t.Fatalf("non-production sanitized summary: %v", err)
	}

	// Must NOT contain secrets from any URL
	for _, secret := range []string{
		"api-user", "api-secret", "token=secret",
		"ws-user", "ws-pass", "sig=secret",
		"priv-user", "priv-pass", "key=abcdef123456",
	} {
		if strings.Contains(summary2, secret) {
			t.Fatalf("summary leaked %q: %s", secret, summary2)
		}
	}
}

// --- Property Tests ---

func TestProductionEndpointProperty_InvalidURLsReject(t *testing.T) {
	// **Validates: Requirements 2.2, 2.3**
	// For any generated invalid production URL, resolver must reject before construction/dial.
	rapid.Check(t, func(t *rapid.T) {
		cfg := validProductionConfigForTest()
		cfg.PrivateWebSocketURL = DefaultPrivateWebSocketURL

		// Generate an invalid mutation
		mutationType := rapid.IntRange(0, 7).Draw(t, "mutationType")
		switch mutationType {
		case 0: // Wrong scheme
			schemes := []string{"http", "ws", "ftp", "ssh", "tcp"}
			scheme := schemes[rapid.IntRange(0, len(schemes)-1).Draw(t, "schemeIdx")]
			target := rapid.IntRange(0, 2).Draw(t, "target")
			switch target {
			case 0:
				cfg.RESTURL = scheme + "://www.okx.com"
			case 1:
				cfg.WebSocketURL = scheme + "://ws.okx.com:8443/ws/v5/public"
			case 2:
				cfg.PrivateWebSocketURL = scheme + "://ws.okx.com:8443/ws/v5/private"
			}
		case 1: // Wrong host
			hosts := []string{"evil.com", "okx.evil.com", "www.0kx.com", "ws2.okx.com"}
			host := hosts[rapid.IntRange(0, len(hosts)-1).Draw(t, "hostIdx")]
			target := rapid.IntRange(0, 2).Draw(t, "target")
			switch target {
			case 0:
				cfg.RESTURL = "https://" + host
			case 1:
				cfg.WebSocketURL = "wss://" + host + ":8443/ws/v5/public"
			case 2:
				cfg.PrivateWebSocketURL = "wss://" + host + ":8443/ws/v5/private"
			}
		case 2: // Userinfo
			user := rapid.StringMatching(`[a-z]{3,8}`).Draw(t, "user")
			pass := rapid.StringMatching(`[a-z0-9]{4,12}`).Draw(t, "pass")
			target := rapid.IntRange(0, 2).Draw(t, "target")
			switch target {
			case 0:
				cfg.RESTURL = "https://" + user + ":" + pass + "@www.okx.com"
			case 1:
				cfg.WebSocketURL = "wss://" + user + ":" + pass + "@ws.okx.com:8443/ws/v5/public"
			case 2:
				cfg.PrivateWebSocketURL = "wss://" + user + ":" + pass + "@ws.okx.com:8443/ws/v5/private"
			}
		case 3: // Query string
			key := rapid.StringMatching(`[a-z]{3,8}`).Draw(t, "key")
			val := rapid.StringMatching(`[a-z0-9]{4,16}`).Draw(t, "val")
			target := rapid.IntRange(0, 2).Draw(t, "target")
			switch target {
			case 0:
				cfg.RESTURL = "https://www.okx.com?" + key + "=" + val
			case 1:
				cfg.WebSocketURL = "wss://ws.okx.com:8443/ws/v5/public?" + key + "=" + val
			case 2:
				cfg.PrivateWebSocketURL = "wss://ws.okx.com:8443/ws/v5/private?" + key + "=" + val
			}
		case 4: // Fragment
			frag := rapid.StringMatching(`[a-z]{3,10}`).Draw(t, "frag")
			target := rapid.IntRange(0, 2).Draw(t, "target")
			switch target {
			case 0:
				cfg.RESTURL = "https://www.okx.com#" + frag
			case 1:
				cfg.WebSocketURL = "wss://ws.okx.com:8443/ws/v5/public#" + frag
			case 2:
				cfg.PrivateWebSocketURL = "wss://ws.okx.com:8443/ws/v5/private#" + frag
			}
		case 5: // Wrong path for WS
			paths := []string{"/ws/v5/trade", "/ws/v4/public", "/api/v5", "/ws/v5/"}
			path := paths[rapid.IntRange(0, len(paths)-1).Draw(t, "pathIdx")]
			target := rapid.IntRange(0, 1).Draw(t, "target")
			switch target {
			case 0:
				cfg.WebSocketURL = "wss://ws.okx.com:8443" + path
			case 1:
				cfg.PrivateWebSocketURL = "wss://ws.okx.com:8443" + path
			}
		case 6: // Role swap
			cfg.WebSocketURL = "wss://ws.okx.com:8443/ws/v5/private"
			cfg.PrivateWebSocketURL = "wss://ws.okx.com:8443/ws/v5/public"
		case 7: // Wrong port
			ports := []string{"443", "8444", "80", "9443"}
			port := ports[rapid.IntRange(0, len(ports)-1).Draw(t, "portIdx")]
			target := rapid.IntRange(0, 1).Draw(t, "target")
			switch target {
			case 0:
				cfg.WebSocketURL = "wss://ws.okx.com:" + port + "/ws/v5/public"
			case 1:
				cfg.PrivateWebSocketURL = "wss://ws.okx.com:" + port + "/ws/v5/private"
			}
		}

		_, err := ResolveNetworkEndpoints(cfg)
		if err == nil {
			t.Fatalf("expected rejection for mutationType=%d, but resolution succeeded", mutationType)
		}
	})
}

func TestProductionEndpointProperty_ValidEndpointsAreExact(t *testing.T) {
	// **Validates: Requirements 2.2, 2.4**
	// The only valid production endpoint set is the exact official set.
	rapid.Check(t, func(t *rapid.T) {
		cfg := validProductionConfigForTest()
		cfg.PrivateWebSocketURL = DefaultPrivateWebSocketURL

		resolved, err := ResolveNetworkEndpoints(cfg)
		if err != nil {
			t.Fatalf("valid production endpoints rejected: %v", err)
		}
		if resolved.RESTBaseURL != "https://www.okx.com" {
			t.Fatalf("REST not exact: %q", resolved.RESTBaseURL)
		}
		if resolved.PublicWebSocketURL != "wss://ws.okx.com:8443/ws/v5/public" {
			t.Fatalf("Public WS not exact: %q", resolved.PublicWebSocketURL)
		}
		if resolved.PrivateWebSocketURL != "wss://ws.okx.com:8443/ws/v5/private" {
			t.Fatalf("Private WS not exact: %q", resolved.PrivateWebSocketURL)
		}
		if resolved.PublicWebSocketURL == resolved.PrivateWebSocketURL {
			t.Fatal("public and private must differ")
		}
	})
}

func TestProductionEndpointProperty_SecretSafeSummary(t *testing.T) {
	// **Validates: Requirements 2.4, 3.7**
	// No generated secret-shaped string appears in the effective config summary.
	rapid.Check(t, func(t *rapid.T) {
		cfg := validProductionConfigForTest()
		cfg.TradingEnabled = true
		cfg.ProductionGates = completeProductionGatesForTest()
		cfg.PrivateWebSocketURL = DefaultPrivateWebSocketURL

		// Generate secret-shaped values and embed in URL userinfo/query/fragment
		// Since production validation rejects these, use simple non-production mode for summary testing
		nonProdCfg := *cfg
		nonProdCfg.ExecutionMode = ExecutionModeUnit

		secretValue := rapid.StringMatching(`[A-Za-z0-9+/]{24,32}`).Draw(t, "secret")
		nonProdCfg.RESTURL = "https://user:" + secretValue + "@www.okx.com"
		nonProdCfg.WebSocketURL = "wss://ws.okx.com:8443/ws/v5/public?key=" + secretValue
		nonProdCfg.PrivateWebSocketURL = "wss://ws.okx.com:8443/ws/v5/private?token=" + secretValue

		summary, err := EffectiveConfigSanitized(&nonProdCfg)
		if err != nil {
			t.Fatalf("effective config failed: %v", err)
		}
		if strings.Contains(summary, secretValue) {
			t.Fatalf("summary leaked generated secret %q in output: %s", secretValue, summary)
		}
	})
}
