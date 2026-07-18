package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/yourname/okx-hft-grid/internal/config"
	"pgregory.net/rapid"
)

type preservationBaseline struct {
	SchemaVersion              int      `json:"schema_version"`
	BaselineState              string   `json:"baseline_state"`
	AllowedComparatorIgnores   []string `json:"allowed_comparator_ignores"`
	ForbiddenComparatorIgnores []string `json:"forbidden_comparator_ignores"`
	IsolationObservation       struct {
		ExternalDNS        int    `json:"external_dns"`
		ExternalDials      int    `json:"external_dials"`
		ProductionRequests int    `json:"production_requests"`
		RealOrderMutations int    `json:"real_order_mutations"`
		CredentialClass    string `json:"credential_class"`
	} `json:"isolation_observation"`
	SourceSHA256 map[string]string `json:"source_sha256"`
	Observations []struct {
		ID              string   `json:"id"`
		Requirements    []string `json:"requirements"`
		GeneratedDomain string   `json:"generated_domain"`
		Baseline        string   `json:"baseline"`
		Comparator      string   `json:"comparator"`
		IgnoredFields   []string `json:"ignored_fields"`
	} `json:"observations"`
}

func loadPreservationBaseline(t *testing.T) preservationBaseline {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "production_grid_preservation_baseline.json"))
	if err != nil {
		t.Fatalf("read preservation baseline: %v", err)
	}
	var baseline preservationBaseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatalf("parse preservation baseline: %v", err)
	}
	return baseline
}

func preservationRepoRoot() string {
	return filepath.Clean(filepath.Join("..", ".."))
}

// **Validates: Requirements 3.10**
//
// PRE-10 binds the empty mean-reversion activation profile to byte-for-byte
// source observations. Any production source modification or profile activation
// is outside this stabilization task and fails this baseline.
func TestProperty2_Preservation_PRE10_MeanReversionRemainsExcluded(t *testing.T) {
	baseline := loadPreservationBaseline(t)
	profile, err := config.LoadConfig(filepath.Join("testdata", "production_grid_preservation_profile.yaml"))
	if err != nil {
		t.Fatalf("PRE-10 load profile: %v", err)
	}
	if len(profile.MeanReversionConfigs) != 0 {
		t.Fatalf("PRE-10 mean reversion activated: %d configs", len(profile.MeanReversionConfigs))
	}
	if len(profile.Symbols) != 2 || profile.Symbols[0] != "DOGE-USDT" || profile.Symbols[1] != "WIF-USDT" {
		t.Fatalf("PRE-10 profile symbols changed: %v", profile.Symbols)
	}

	paths := make([]string, 0, len(baseline.SourceSHA256))
	for path := range baseline.SourceSHA256 {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) != 2 {
		t.Fatalf("PRE-10 source baseline count=%d, want 2", len(paths))
	}

	rapid.Check(t, func(t *rapid.T) {
		path := paths[rapid.IntRange(0, len(paths)-1).Draw(t, "mean_reversion_source_index")]
		contents, err := os.ReadFile(filepath.Join(preservationRepoRoot(), filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("PRE-10 read %s: %v", path, err)
		}
		digest := sha256.Sum256(contents)
		actual := hex.EncodeToString(digest[:])
		if actual != baseline.SourceSHA256[path] {
			t.Fatalf("PRE-10 source changed: %s sha256=%s want=%s", path, actual, baseline.SourceSHA256[path])
		}
		symbol := profile.Symbols[rapid.IntRange(0, len(profile.Symbols)-1).Draw(t, "profile_symbol_index")]
		for _, meanReversion := range profile.MeanReversionConfigs {
			if meanReversion.Symbol == symbol {
				t.Fatalf("PRE-10 %s unexpectedly has mean-reversion activation", symbol)
			}
		}
	})
}

// **Validates: Requirements 3.12**
//
// PRE-12 audits every Property 2 source/fixture and the baseline contract. It
// complements the execution-package transport recorder: no production endpoint,
// host metadata endpoint, deployment command, credential fixture or approval
// transition is permitted in this automated suite.
func TestProperty2_Preservation_PRE12_AllEntriesRemainProductionIsolated(t *testing.T) {
	t.Setenv("OKX_API_KEY", "")
	t.Setenv("OKX_SECRET_KEY", "")
	t.Setenv("OKX_PASSPHRASE", "")
	baseline := loadPreservationBaseline(t)
	if baseline.SchemaVersion != 1 || !strings.Contains(baseline.BaselineState, "UNFIXED") {
		t.Fatalf("PRE-12 invalid observation baseline metadata: version=%d state=%q", baseline.SchemaVersion, baseline.BaselineState)
	}
	if baseline.IsolationObservation.ExternalDNS != 0 || baseline.IsolationObservation.ExternalDials != 0 ||
		baseline.IsolationObservation.ProductionRequests != 0 || baseline.IsolationObservation.RealOrderMutations != 0 ||
		baseline.IsolationObservation.CredentialClass != "synthetic-only" {
		t.Fatalf("PRE-12 non-zero baseline isolation observation: %+v", baseline.IsolationObservation)
	}

	expectedAllowed := []string{
		"new_correlation_ids",
		"new_observability_timestamps",
		"new_durability_records",
		"mandatory_td_mode_cash_normalization",
		"mandatory_instrument_metadata_normalization",
	}
	allowed := make(map[string]struct{}, len(baseline.AllowedComparatorIgnores))
	for _, field := range baseline.AllowedComparatorIgnores {
		allowed[field] = struct{}{}
	}
	if len(allowed) != len(expectedAllowed) {
		t.Fatalf("PRE-12 allowed comparator ignore count=%d, want %d: %v", len(allowed), len(expectedAllowed), baseline.AllowedComparatorIgnores)
	}
	for _, field := range expectedAllowed {
		if _, ok := allowed[field]; !ok {
			t.Fatalf("PRE-12 missing approved comparator ignore %q", field)
		}
	}

	expectedForbidden := []string{"side", "level", "quantity", "pnl", "ownership", "risk", "symbol"}
	forbidden := make(map[string]struct{}, len(baseline.ForbiddenComparatorIgnores))
	for _, field := range baseline.ForbiddenComparatorIgnores {
		forbidden[field] = struct{}{}
	}
	if len(forbidden) != len(expectedForbidden) {
		t.Fatalf("PRE-12 forbidden comparator ignore count=%d, want %d: %v", len(forbidden), len(expectedForbidden), baseline.ForbiddenComparatorIgnores)
	}
	for _, field := range expectedForbidden {
		if _, ok := forbidden[field]; !ok {
			t.Fatalf("PRE-12 missing forbidden comparator semantic %q", field)
		}
		if _, incorrectlyAllowed := allowed[field]; incorrectlyAllowed {
			t.Fatalf("PRE-12 semantic field %q is incorrectly allowed to be ignored", field)
		}
	}
	seen := make(map[string]bool, len(baseline.Observations))
	for _, observation := range baseline.Observations {
		if seen[observation.ID] {
			t.Fatalf("PRE-12 duplicate baseline ID %s", observation.ID)
		}
		seen[observation.ID] = true
		if len(observation.Requirements) == 0 || observation.GeneratedDomain == "" || observation.Baseline == "" || observation.Comparator == "" {
			t.Fatalf("PRE-12 incomplete baseline observation: %+v", observation)
		}
		for _, field := range observation.IgnoredFields {
			if _, ok := allowed[field]; !ok {
				t.Fatalf("PRE-12 %s ignores unapproved field %q", observation.ID, field)
			}
			if _, bad := forbidden[field]; bad {
				t.Fatalf("PRE-12 %s masks forbidden semantic field %q", observation.ID, field)
			}
		}
	}
	for i := 1; i <= 12; i++ {
		id := "PRE-" + twoDigit(i)
		if !seen[id] {
			t.Fatalf("PRE-12 missing baseline observation %s", id)
		}
	}
	if len(seen) != 12 {
		t.Fatalf("PRE-12 observation count=%d, want 12", len(seen))
	}

	root := preservationRepoRoot()
	var audited []struct {
		path string
		text string
	}
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if name != "production_grid_preservation_property_test.go" &&
			name != "production_grid_preservation_baseline.json" &&
			name != "production_grid_preservation_profile.yaml" {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		audited = append(audited, struct {
			path string
			text string
		}{path: path, text: string(contents)})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("PRE-12 audit walk: %v", walkErr)
	}
	if len(audited) < 9 {
		t.Fatalf("PRE-12 audited only %d preservation files/fixtures", len(audited))
	}

	forbiddenFragments := []string{
		strings.Join([]string{"www", "okx", "com"}, "."),
		strings.Join([]string{"169", "254", "169", "254"}, "."),
		strings.Join([]string{"amazonaws", "com"}, "."),
		strings.Join([]string{"api", "telegram", "org"}, "."),
		"system" + "ctl",
		"trading_enabled" + ": true",
	}
	rapid.Check(t, func(t *rapid.T) {
		file := audited[rapid.IntRange(0, len(audited)-1).Draw(t, "audited_file_index")]
		lower := strings.ToLower(file.text)
		for _, fragment := range forbiddenFragments {
			if strings.Contains(lower, strings.ToLower(fragment)) {
				t.Fatalf("PRE-12 forbidden production effect marker %q in %s", fragment, file.path)
			}
		}
		if strings.HasSuffix(file.path, ".yaml") {
			for _, credentialField := range []string{"api_key:", "secret_key:", "passphrase:"} {
				if strings.Contains(lower, credentialField) {
					t.Fatalf("PRE-12 credential field %q embedded in fixture %s", credentialField, file.path)
				}
			}
		}
	})
}

func twoDigit(value int) string {
	if value < 10 {
		return "0" + string(rune('0'+value))
	}
	return "1" + string(rune('0'+value-10))
}
