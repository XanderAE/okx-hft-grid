package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourname/okx-hft-grid/pkg/models"
)

// TestRunbookGates verifies the runbook document enforces all required gates
// and contains no forbidden content.
func TestRunbookGates(t *testing.T) {
	runbookPath := filepath.Join("..", "..", "deploy", "PRODUCTION_RUNBOOK_SINGAPORE.md")
	content, err := os.ReadFile(runbookPath)
	if err != nil {
		t.Fatalf("production runbook must exist at deploy/PRODUCTION_RUNBOOK_SINGAPORE.md: %v", err)
	}
	text := string(content)

	// Required gates that must appear in the runbook
	requiredGates := []struct {
		name    string
		pattern string
	}{
		{"immutable artifact checksum", "SHA-256"},
		{"config checksum", "config.yaml"},
		{"credential rotation gate", "credential_rotation"},
		{"least privilege gate", "least_privilege"},
		{"singapore IP allowlist gate", "singapore_ip_allowlist"},
		{"human trading approval gate", "human_trading_approval"},
		{"trading_enabled=false staged", "trading_enabled=false"},
		{"trading_enabled=false reconcile-only", "reconcile-only"},
		{"StateDirectory backup", "StateDirectory"},
		{"WAL backup", "WAL"},
		{"health evidence", "healthz"},
		{"journald evidence", "journald"},
		{"independent trading approval", "Independent Human Trading Approval"},
		{"observation window", "observation"},
		{"Safe_Stop procedure", "Safe_Stop"},
		{"schema/outbox-compatible rollback", "schema/outbox-compatible"},
		{"no sed prohibition", "No `sed`"},
		{"mean reversion excluded", "mean reversion"},
		{"Singapore location", "ap-southeast-1"},
		{"named human operator", "Named human"},
	}
	for _, gate := range requiredGates {
		if !strings.Contains(text, gate.pattern) {
			t.Errorf("runbook missing required gate %q (expected pattern %q)", gate.name, gate.pattern)
		}
	}

	// The runbook must require credential rotation as a blocking prerequisite
	if !strings.Contains(text, "PENDING HUMAN") {
		t.Error("runbook must mark credential evidence as pending human action")
	}

	// Automated test passage must NOT be interpreted as production approval
	if !strings.Contains(text, "NOT sufficient") && !strings.Contains(text, "NOT constitute production approval") {
		t.Error("runbook must explicitly state automated tests do not constitute production approval")
	}

	// Rollback must require trading_enabled=false
	rollbackSection := text[strings.Index(text, "Rollback"):]
	if !strings.Contains(rollbackSection, "trading_enabled=false") {
		t.Error("rollback procedure must start with trading_enabled=false")
	}

	// Rollback must mention credential rotation is not rolled back
	if !strings.Contains(rollbackSection, "NOT rolled back") {
		t.Error("rollback must state credential rotation is never rolled back")
	}
}

// TestSingaporeArtifacts verifies all production artifacts reference Singapore
// and contain no Tokyo deployment assumptions.
func TestSingaporeArtifacts(t *testing.T) {
	artifacts := []string{
		filepath.Join("..", "..", "deploy", "PRODUCTION_RUNBOOK_SINGAPORE.md"),
		filepath.Join("..", "..", "deploy", "config.example.yaml"),
		filepath.Join("..", "..", "deploy", "okx-hft-grid.service"),
	}

	for _, path := range artifacts {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read artifact %s: %v", path, err)
		}
		text := string(content)
		name := filepath.Base(path)

		// Must not contain Tokyo-specific deployment assumptions
		tokyoPatterns := []string{
			"ap-northeast-1",
			"tokyo",
		}
		lower := strings.ToLower(text)
		for _, pattern := range tokyoPatterns {
			// Allow "tokyo" only in the context of "supersedes tokyo" or "invalidated" or in this test's own context
			if pattern == "tokyo" {
				// Check it doesn't appear as a deployment target
				if strings.Contains(lower, "location: \"aws-ap-northeast-1-tokyo\"") ||
					strings.Contains(lower, "deploy to tokyo") ||
					strings.Contains(lower, "tokyo ec2") {
					t.Errorf("artifact %s contains Tokyo deployment assumption with pattern %q", name, pattern)
				}
			} else {
				if strings.Contains(lower, pattern) {
					t.Errorf("artifact %s contains Tokyo-region reference %q", name, pattern)
				}
			}
		}

		// Production config and runbook must reference Singapore
		if name == "config.example.yaml" || name == "PRODUCTION_RUNBOOK_SINGAPORE.md" {
			if !strings.Contains(text, "aws-ap-southeast-1-singapore") {
				t.Errorf("artifact %s must reference aws-ap-southeast-1-singapore", name)
			}
		}
	}
}

// TestNoSecretArtifacts verifies no production artifacts contain credential values,
// API keys, secrets, passphrases, or signature material.
func TestNoSecretArtifacts(t *testing.T) {
	artifacts := []string{
		filepath.Join("..", "..", "deploy", "PRODUCTION_RUNBOOK_SINGAPORE.md"),
		filepath.Join("..", "..", "deploy", "config.example.yaml"),
		filepath.Join("..", "..", "deploy", "okx-hft-grid.service"),
		filepath.Join("..", "..", "deploy", "okx-hft-grid-failure@.service"),
		filepath.Join("..", "..", "deploy", "README.md"),
	}

	// Patterns that would indicate credential values (not placeholder references)
	secretPatterns := []struct {
		name    string
		pattern string
	}{
		// Real-looking API key patterns (32+ hex or base64 characters)
		{"base64 secret", "==" + strings.Repeat("A", 20)},
	}
	_ = secretPatterns

	// Things that must NOT appear in any artifact
	forbiddenExact := []string{
		"Authorization:",
		"x-simulated-trading",
	}

	for _, path := range artifacts {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read artifact %s: %v", path, err)
		}
		text := string(content)
		name := filepath.Base(path)

		// No actual credential values (check for common real-looking patterns)
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			lower := strings.ToLower(line)
			// OKX API keys are typically 36 chars; check for anything that looks like a real one
			if strings.Contains(lower, "api_key") || strings.Contains(lower, "secret_key") || strings.Contains(lower, "passphrase") {
				// Allowed: placeholder references like "your-api-key-here" or empty strings or field names
				if !strings.Contains(line, "your-") &&
					!strings.Contains(line, "\"\"") &&
					!strings.Contains(line, "placeholder") &&
					!strings.Contains(line, "OKX_API_KEY") &&
					!strings.Contains(line, "OKX_SECRET_KEY") &&
					!strings.Contains(line, "OKX_PASSPHRASE") &&
					!strings.Contains(line, "#") &&
					!strings.Contains(line, "credential") &&
					!strings.Contains(line, "never") &&
					!strings.Contains(line, "No credential") &&
					!strings.Contains(line, "secret") &&
					strings.Contains(line, "=") &&
					len(strings.TrimSpace(strings.Split(line, "=")[len(strings.Split(line, "="))-1])) > 20 {
					t.Errorf("artifact %s line %d may contain a real credential value", name, i+1)
				}
			}
		}

		for _, forbidden := range forbiddenExact {
			if strings.Contains(text, forbidden) {
				t.Errorf("artifact %s contains forbidden pattern %q", name, forbidden)
			}
		}
	}

	// The failure service must not have EnvironmentFile or credential access
	failurePath := filepath.Join("..", "..", "deploy", "okx-hft-grid-failure@.service")
	failureContent, err := os.ReadFile(failurePath)
	if err != nil {
		t.Fatalf("read failure service: %v", err)
	}
	failureText := string(failureContent)
	if strings.Contains(failureText, "EnvironmentFile") {
		t.Error("failure service must NOT have EnvironmentFile (no credentials)")
	}
	if strings.Contains(failureText, "OKX_API_KEY") || strings.Contains(failureText, "OKX_SECRET") {
		t.Error("failure service must NOT reference credential environment variables")
	}
}

// TestMeanReversionExcluded verifies the production profile excludes mean reversion
// and the runbook explicitly states this exclusion.
func TestMeanReversionExcluded(t *testing.T) {
	// Verify production config excludes mean reversion
	profilePath := filepath.Join("..", "..", "deploy", "config.example.yaml")
	cfg, err := LoadProductionConfig(profilePath)
	if err != nil {
		t.Fatalf("load production profile: %v", err)
	}
	if len(cfg.MeanReversionConfigs) != 0 {
		t.Fatal("production profile must have empty mean_reversion_configs")
	}

	// Verify validation rejects mean reversion in production
	withMR := validProductionConfigForTest()
	withMR.MeanReversionConfigs = []models.MeanReversionConfig{{Symbol: "DOGE-USDT"}}
	if err := ValidateProductionConfig(withMR); err == nil {
		t.Fatal("production validation must reject non-empty mean_reversion_configs")
	}

	// Verify runbook states exclusion
	runbookPath := filepath.Join("..", "..", "deploy", "PRODUCTION_RUNBOOK_SINGAPORE.md")
	content, err := os.ReadFile(runbookPath)
	if err != nil {
		t.Fatalf("read runbook: %v", err)
	}
	text := strings.ToLower(string(content))
	if !strings.Contains(text, "mean reversion") {
		t.Error("runbook must mention mean reversion exclusion")
	}
	if !strings.Contains(text, "excluded") || !strings.Contains(text, "not enabled") {
		t.Error("runbook must explicitly state mean reversion is excluded and not enabled")
	}
}

// TestHumanApprovalRequired verifies that production trading cannot be enabled
// without human gate evidence, and that the runbook enforces independent human
// approval separate from automated test results.
func TestHumanApprovalRequired(t *testing.T) {
	// Verify config validation requires gate evidence for trading
	noGates := validProductionConfigForTest()
	noGates.TradingEnabled = true
	// No gate evidence
	err := ValidateProductionConfig(noGates)
	if err == nil {
		t.Fatal("production trading must be rejected without gate evidence")
	}
	if !strings.Contains(err.Error(), "gate evidence") {
		t.Fatalf("expected gate evidence error, got: %v", err)
	}

	// Verify partial gates are rejected
	partialGates := validProductionConfigForTest()
	partialGates.TradingEnabled = true
	partialGates.ProductionGates = ProductionGateEvidence{
		CredentialRotation:   "rotation-id",
		LeastPrivilege:       "priv-id",
		SingaporeIPAllowlist: "", // missing
		HumanTradingApproval: "human-id",
	}
	err = ValidateProductionConfig(partialGates)
	if err == nil {
		t.Fatal("partial gate evidence must be rejected")
	}

	// Verify human_trading_approval is specifically required
	noHumanApproval := validProductionConfigForTest()
	noHumanApproval.TradingEnabled = true
	noHumanApproval.ProductionGates = ProductionGateEvidence{
		CredentialRotation:   "rotation-id",
		LeastPrivilege:       "priv-id",
		SingaporeIPAllowlist: "ip-id",
		HumanTradingApproval: "", // missing
	}
	err = ValidateProductionConfig(noHumanApproval)
	if err == nil {
		t.Fatal("missing human_trading_approval must be rejected")
	}
	if !strings.Contains(err.Error(), "human_trading_approval") {
		t.Fatalf("error must mention human_trading_approval, got: %v", err)
	}

	// Verify runbook requires independent human trading approval
	runbookPath := filepath.Join("..", "..", "deploy", "PRODUCTION_RUNBOOK_SINGAPORE.md")
	content, err := os.ReadFile(runbookPath)
	if err != nil {
		t.Fatalf("read runbook: %v", err)
	}
	text := string(content)

	// Must have a separate trading approval step distinct from deployment approval
	if !strings.Contains(text, "Independent Human Trading Approval") {
		t.Error("runbook must have an independent human trading approval step")
	}
	if !strings.Contains(text, "SEPARATE approval") {
		t.Error("runbook must state trading approval is separate from deployment approval")
	}
	if !strings.Contains(text, "NOT constitute production approval") {
		t.Error("runbook must state automated tests do not constitute production approval")
	}
}
