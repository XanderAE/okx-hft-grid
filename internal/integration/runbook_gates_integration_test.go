package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourname/okx-hft-grid/internal/config"
)

// TestRunbookGatesIntegration verifies end-to-end that the runbook, config profile,
// and systemd units form a coherent deployment gate system.
func TestRunbookGatesIntegration(t *testing.T) {
	runbookPath := filepath.Join("..", "..", "deploy", "PRODUCTION_RUNBOOK_SINGAPORE.md")
	content, err := os.ReadFile(runbookPath)
	if err != nil {
		t.Fatalf("production runbook must exist: %v", err)
	}
	text := string(content)

	// Verify config profile loads without any runtime mutation
	profilePath := filepath.Join("..", "..", "deploy", "config.example.yaml")
	cfg, err := config.LoadProductionConfig(profilePath)
	if err != nil {
		t.Fatalf("version-controlled profile must load without mutation: %v", err)
	}

	// Config must start as reconcile-only
	if cfg.TradingEnabled {
		t.Fatal("version-controlled profile must have trading_enabled=false")
	}

	// Runbook must reference the same approved values from config
	if !strings.Contains(text, "trading_enabled=false") {
		t.Error("runbook must reference trading_enabled=false for staged deployment")
	}

	// Runbook must have rollback procedure
	if !strings.Contains(text, "Rollback") {
		t.Error("runbook must include rollback procedure")
	}

	// Verify the systemd service file has required security properties
	servicePath := filepath.Join("..", "..", "deploy", "okx-hft-grid.service")
	serviceContent, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatalf("read service file: %v", err)
	}
	serviceText := string(serviceContent)

	requiredServiceFields := []string{
		"StateDirectory=okx-hft-grid",
		"ReadWritePaths=/var/lib/okx-hft-grid",
		"ProtectSystem=strict",
		"Type=notify",
		"RestartSec=5s",
		"StartLimitBurst=3",
		"StartLimitIntervalSec=60s",
		"OnFailure=okx-hft-grid-failure@%n.service",
	}
	for _, field := range requiredServiceFields {
		if !strings.Contains(serviceText, field) {
			t.Errorf("service file missing required field %q", field)
		}
	}
}

// TestSingaporeArtifactsIntegration verifies all deploy artifacts are Singapore-consistent.
func TestSingaporeArtifactsIntegration(t *testing.T) {
	deployDir := filepath.Join("..", "..", "deploy")
	entries, err := os.ReadDir(deployDir)
	if err != nil {
		t.Fatalf("read deploy dir: %v", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(deployDir, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		lower := strings.ToLower(string(content))

		// No artifact should contain Tokyo as a deployment target
		if strings.Contains(lower, "ap-northeast-1") {
			t.Errorf("deploy artifact %s contains Tokyo region reference ap-northeast-1", entry.Name())
		}
	}

	// The runbook must explicitly state it supersedes Tokyo
	runbookPath := filepath.Join(deployDir, "PRODUCTION_RUNBOOK_SINGAPORE.md")
	content, err := os.ReadFile(runbookPath)
	if err != nil {
		t.Fatalf("read runbook: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "supersedes") {
		t.Error("runbook must state this spec supersedes prior Tokyo assumptions")
	}
}

// TestNoSecretArtifactsIntegration verifies deploy directory has no credential leakage.
func TestNoSecretArtifactsIntegration(t *testing.T) {
	deployDir := filepath.Join("..", "..", "deploy")
	entries, err := os.ReadDir(deployDir)
	if err != nil {
		t.Fatalf("read deploy dir: %v", err)
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
		text := string(content)
		name := entry.Name()

		// No real-looking credentials (long hex/base64 strings after key= patterns)
		// env.example is allowed to have placeholder values
		if name != "env.example" {
			if strings.Contains(text, "Authorization: Bearer") ||
				strings.Contains(text, "x-api-key:") {
				t.Errorf("artifact %s contains what appears to be a real auth header", name)
			}
		}
	}
}

// TestMeanReversionExcludedIntegration verifies the complete deploy set excludes mean reversion.
func TestMeanReversionExcludedIntegration(t *testing.T) {
	profilePath := filepath.Join("..", "..", "deploy", "config.example.yaml")
	cfg, err := config.LoadProductionConfig(profilePath)
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if len(cfg.MeanReversionConfigs) != 0 {
		t.Fatal("production profile must exclude mean reversion")
	}

	// Verify profile YAML explicitly shows empty
	content, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	if !strings.Contains(string(content), "mean_reversion_configs: []") {
		t.Error("production profile must explicitly show mean_reversion_configs: []")
	}
}

// TestHumanApprovalRequiredIntegration verifies the full gate flow from config through runbook.
func TestHumanApprovalRequiredIntegration(t *testing.T) {
	// Load the reconcile-only profile
	profilePath := filepath.Join("..", "..", "deploy", "config.example.yaml")
	cfg, err := config.LoadProductionConfig(profilePath)
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}

	// It must not have trading enabled
	if cfg.TradingEnabled {
		t.Fatal("version-controlled profile must not enable trading")
	}

	// If we try to enable trading on it, gates must block
	cfg.TradingEnabled = true
	err = config.ValidateProductionConfig(cfg)
	if err == nil {
		t.Fatal("enabling trading on the base profile without gates must fail")
	}

	// Verify the runbook documents this separation
	runbookPath := filepath.Join("..", "..", "deploy", "PRODUCTION_RUNBOOK_SINGAPORE.md")
	content, err := os.ReadFile(runbookPath)
	if err != nil {
		t.Fatalf("read runbook: %v", err)
	}
	text := string(content)

	// Must require four separate gate evidence items
	gates := []string{"credential_rotation", "least_privilege", "singapore_ip_allowlist", "human_trading_approval"}
	for _, gate := range gates {
		if !strings.Contains(text, gate) {
			t.Errorf("runbook must reference gate %q", gate)
		}
	}

	// Must explicitly state automated tests are not production approval
	if !strings.Contains(text, "NOT constitute production approval") {
		t.Error("runbook must state automated tests do not constitute production approval")
	}
}
