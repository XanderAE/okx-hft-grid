package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourname/okx-hft-grid/internal/config"
)

// TestProductionEndpointArtifactsIntegration verifies the full deployment artifact set
// is consistent: the example config loads with both endpoint roles, no bypass exists,
// no secret leaks, and the staged profile remains reconcile-only.
//
// **Validates: Requirements 2.4, 3.4, 3.6, 3.7**
func TestProductionEndpointArtifactsIntegration(t *testing.T) {
	profilePath := filepath.Join("..", "..", "deploy", "config.example.yaml")

	// Strict loading must succeed
	cfg, err := config.LoadProductionConfig(profilePath)
	if err != nil {
		t.Fatalf("production example must load strictly: %v", err)
	}

	// Both role endpoints must match authoritative defaults
	if cfg.WebSocketURL != config.DefaultPublicWebSocketURL {
		t.Fatalf("websocket_url: got %q, want %q", cfg.WebSocketURL, config.DefaultPublicWebSocketURL)
	}
	if cfg.PrivateWebSocketURL != config.DefaultPrivateWebSocketURL {
		t.Fatalf("private_websocket_url: got %q, want %q", cfg.PrivateWebSocketURL, config.DefaultPrivateWebSocketURL)
	}
	if cfg.RESTURL != config.DefaultRESTBaseURL {
		t.Fatalf("rest_url: got %q, want %q", cfg.RESTURL, config.DefaultRESTBaseURL)
	}

	// Must be reconcile-only
	if cfg.TradingEnabled {
		t.Fatal("trading_enabled must be false in the production example")
	}

	// Resolved endpoints must be distinct and exact
	resolved, err := config.ResolveNetworkEndpoints(cfg)
	if err != nil {
		t.Fatalf("endpoint resolution: %v", err)
	}
	if resolved.PublicWebSocketURL == resolved.PrivateWebSocketURL {
		t.Fatal("resolved public and private must be distinct")
	}

	// Scan all deploy artifacts for bypass patterns and secrets
	deployDir := filepath.Join("..", "..", "deploy")
	entries, err := os.ReadDir(deployDir)
	if err != nil {
		t.Fatalf("read deploy dir: %v", err)
	}

	bypassPatterns := []string{
		"bypass", "override_endpoint", "skip_validation",
		"allow_all", "disable_guard", "permissive",
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(deployDir, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lower := strings.ToLower(string(content))
		name := entry.Name()

		for _, pattern := range bypassPatterns {
			if strings.Contains(lower, pattern) {
				t.Errorf("artifact %s contains bypass pattern %q", name, pattern)
			}
		}
	}
}

// TestRunbookEndpointRolesIntegration verifies the runbook documents both WebSocket
// endpoint roles for human verification.
//
// **Validates: Requirements 2.4, 3.6**
func TestRunbookEndpointRolesIntegration(t *testing.T) {
	runbookPath := filepath.Join("..", "..", "deploy", "PRODUCTION_RUNBOOK_SINGAPORE.md")
	content, err := os.ReadFile(runbookPath)
	if err != nil {
		t.Fatalf("runbook must exist: %v", err)
	}
	text := string(content)

	// Both endpoint URLs must appear
	if !strings.Contains(text, "wss://ws.okx.com:8443/ws/v5/public") {
		t.Error("runbook missing public WebSocket URL")
	}
	if !strings.Contains(text, "wss://ws.okx.com:8443/ws/v5/private") {
		t.Error("runbook missing private WebSocket URL")
	}

	// Both config fields must be referenced
	if !strings.Contains(text, "websocket_url") {
		t.Error("runbook must reference websocket_url field")
	}
	if !strings.Contains(text, "private_websocket_url") {
		t.Error("runbook must reference private_websocket_url field")
	}

	// REST endpoint must be documented
	if !strings.Contains(text, "https://www.okx.com") {
		t.Error("runbook must reference the REST endpoint")
	}

	// Distinctness must be required
	if !strings.Contains(text, "distinct") {
		t.Error("runbook must require public/private endpoints to be distinct")
	}

	// trading_enabled=false must remain
	if !strings.Contains(text, "trading_enabled=false") || !strings.Contains(text, "trading_enabled") {
		t.Error("runbook must retain trading_enabled=false reference")
	}

	// No credential values
	for _, secret := range []string{"Authorization:", "Bearer ", "x-api-key:"} {
		if strings.Contains(text, secret) {
			t.Errorf("runbook contains secret indicator %q", secret)
		}
	}

	// No production WebSocket probe commands
	for _, probe := range []string{"curl wss://", "wscat", "websocat"} {
		if strings.Contains(text, probe) {
			t.Errorf("runbook must not contain production probe %q", probe)
		}
	}
}
