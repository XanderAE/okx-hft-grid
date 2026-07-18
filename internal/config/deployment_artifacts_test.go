package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProductionEndpointArtifacts verifies the version-controlled example config
// loads strictly, both role endpoints are exact, no bypass field exists, no secret
// value is present, and the staged profile remains reconcile-only.
//
// **Validates: Requirements 2.4, 3.4, 3.6, 3.7**
func TestProductionEndpointArtifacts(t *testing.T) {
	profilePath := filepath.Join("..", "..", "deploy", "config.example.yaml")

	// Must load strictly without runtime mutation
	cfg, err := LoadProductionConfig(profilePath)
	if err != nil {
		t.Fatalf("production example must load strictly: %v", err)
	}

	// Both role endpoints must be exact
	if cfg.WebSocketURL != DefaultPublicWebSocketURL {
		t.Fatalf("websocket_url must be %q, got %q", DefaultPublicWebSocketURL, cfg.WebSocketURL)
	}
	if cfg.PrivateWebSocketURL != DefaultPrivateWebSocketURL {
		t.Fatalf("private_websocket_url must be %q, got %q", DefaultPrivateWebSocketURL, cfg.PrivateWebSocketURL)
	}
	if cfg.RESTURL != DefaultRESTBaseURL {
		t.Fatalf("rest_url must be %q, got %q", DefaultRESTBaseURL, cfg.RESTURL)
	}

	// Endpoints must be distinct
	if cfg.WebSocketURL == cfg.PrivateWebSocketURL {
		t.Fatal("public and private WebSocket URLs must be distinct in example config")
	}

	// Staged profile must remain reconcile-only
	if cfg.TradingEnabled {
		t.Fatal("trading_enabled must be false in the version-controlled example")
	}

	// No bypass field should exist
	raw, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	text := string(raw)
	bypassPatterns := []string{
		"bypass",
		"override_endpoint",
		"skip_validation",
		"allow_all",
		"disable_guard",
		"permissive",
		"x-simulated-trading",
	}
	lower := strings.ToLower(text)
	for _, pattern := range bypassPatterns {
		if strings.Contains(lower, pattern) {
			t.Fatalf("example config contains bypass-like field %q", pattern)
		}
	}

	// No secret value should be present (actual credential values)
	secretIndicators := []string{
		"Authorization:",
		"Bearer ",
		"x-api-key:",
	}
	for _, indicator := range secretIndicators {
		if strings.Contains(text, indicator) {
			t.Fatalf("example config contains secret indicator %q", indicator)
		}
	}

	// Gate evidence fields must be empty (reconcile-only)
	if cfg.ProductionGates.Complete() {
		t.Fatal("gate evidence must be empty in the reconcile-only staged profile")
	}

	// Validate resolved endpoints are exact
	resolved, err := ResolveNetworkEndpoints(cfg)
	if err != nil {
		t.Fatalf("endpoint resolution failed on example config: %v", err)
	}
	if resolved.RESTBaseURL != DefaultRESTBaseURL {
		t.Fatalf("resolved REST: got %q, want %q", resolved.RESTBaseURL, DefaultRESTBaseURL)
	}
	if resolved.PublicWebSocketURL != DefaultPublicWebSocketURL {
		t.Fatalf("resolved public WS: got %q, want %q", resolved.PublicWebSocketURL, DefaultPublicWebSocketURL)
	}
	if resolved.PrivateWebSocketURL != DefaultPrivateWebSocketURL {
		t.Fatalf("resolved private WS: got %q, want %q", resolved.PrivateWebSocketURL, DefaultPrivateWebSocketURL)
	}
}

// TestRunbookEndpointRoles verifies the production runbook includes verification
// items for both public and private WebSocket endpoint roles.
//
// **Validates: Requirements 2.4, 3.6**
func TestRunbookEndpointRoles(t *testing.T) {
	runbookPath := filepath.Join("..", "..", "deploy", "PRODUCTION_RUNBOOK_SINGAPORE.md")
	content, err := os.ReadFile(runbookPath)
	if err != nil {
		t.Fatalf("production runbook must exist: %v", err)
	}
	text := string(content)

	// Must reference the public WebSocket endpoint with its role
	if !strings.Contains(text, "wss://ws.okx.com:8443/ws/v5/public") {
		t.Error("runbook must contain the exact public WebSocket URL")
	}
	if !strings.Contains(text, "public market data") || !strings.Contains(text, "public") {
		t.Error("runbook must identify the public WebSocket role")
	}

	// Must reference the private WebSocket endpoint with its role
	if !strings.Contains(text, "wss://ws.okx.com:8443/ws/v5/private") {
		t.Error("runbook must contain the exact private WebSocket URL")
	}
	if !strings.Contains(text, "private") {
		t.Error("runbook must identify the private WebSocket role")
	}

	// Must mention both websocket_url and private_websocket_url fields
	if !strings.Contains(text, "websocket_url") {
		t.Error("runbook must reference the websocket_url field")
	}
	if !strings.Contains(text, "private_websocket_url") {
		t.Error("runbook must reference the private_websocket_url field")
	}

	// Must require the endpoints to be distinct
	if !strings.Contains(text, "distinct") {
		t.Error("runbook must state public and private WebSocket endpoints must be distinct")
	}

	// Must still contain trading_enabled=false
	if !strings.Contains(text, "trading_enabled") {
		t.Error("runbook must reference trading_enabled")
	}

	// Must retain the rest_url reference
	if !strings.Contains(text, "rest_url") || !strings.Contains(text, "https://www.okx.com") {
		t.Error("runbook must reference rest_url and the official REST endpoint")
	}

	// Must not contain deployment commands (no systemctl, no sudo in endpoint section)
	// but can have them in other sections - just verify no deployment commands are near endpoint verification
	if strings.Contains(text, "systemctl start") && strings.Contains(text, "private_websocket_url") {
		// Check they aren't in the same paragraph
		wsIdx := strings.Index(text, "private_websocket_url")
		sysIdx := strings.Index(text, "systemctl start")
		// They should not be within 200 chars of each other (endpoint verification
		// should not be mixed with deployment execution)
		if wsIdx > 0 && sysIdx > 0 && abs(wsIdx-sysIdx) < 200 {
			t.Error("endpoint verification items must not be mixed with deployment commands")
		}
	}

	// Must not contain credentials
	if strings.Contains(text, "Authorization:") || strings.Contains(text, "Bearer ") {
		t.Error("runbook must not contain credential values")
	}

	// No production probe or approval shortcut
	if strings.Contains(text, "curl wss://ws.okx.com") || strings.Contains(text, "wscat") {
		t.Error("runbook must not contain production WebSocket probes")
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
