package config

import (
	"os"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// **Validates: Requirements 13.7**
//
// Property 34: Credential Startup Validation
// For any system startup, the system SHALL validate that all required API credentials
// (API key, secret key, passphrase) are present and non-empty, refusing to start if
// any credential is missing.

// nonEmptyString generates non-empty strings suitable for credential values.
func nonEmptyString() *rapid.Generator[string] {
	return rapid.StringMatching(`[a-zA-Z0-9\-_]{1,64}`)
}

// TestProperty_CredentialStartupValidation_AllPresent verifies that for any set of
// 3 non-empty credential strings, when all are set as env vars, LoadCredentials
// succeeds and returns those exact values.
func TestProperty_CredentialStartupValidation_AllPresent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		apiKey := nonEmptyString().Draw(t, "apiKey")
		secretKey := nonEmptyString().Draw(t, "secretKey")
		passphrase := nonEmptyString().Draw(t, "passphrase")

		// Set env vars
		os.Setenv("OKX_API_KEY", apiKey)
		os.Setenv("OKX_SECRET_KEY", secretKey)
		os.Setenv("OKX_PASSPHRASE", passphrase)
		defer func() {
			os.Unsetenv("OKX_API_KEY")
			os.Unsetenv("OKX_SECRET_KEY")
			os.Unsetenv("OKX_PASSPHRASE")
		}()

		creds, err := LoadCredentials()
		if err != nil {
			t.Fatalf("expected no error with all credentials set, got: %v", err)
		}
		if creds.APIKey != apiKey {
			t.Fatalf("expected APIKey=%q, got %q", apiKey, creds.APIKey)
		}
		if creds.SecretKey != secretKey {
			t.Fatalf("expected SecretKey=%q, got %q", secretKey, creds.SecretKey)
		}
		if creds.Passphrase != passphrase {
			t.Fatalf("expected Passphrase=%q, got %q", passphrase, creds.Passphrase)
		}
	})
}

// TestProperty_CredentialStartupValidation_MissingReturnsError verifies that for any
// combination where at least one credential is empty/unset, LoadCredentials returns an
// error mentioning the missing credential name.
func TestProperty_CredentialStartupValidation_MissingReturnsError(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Use a bitmask (1-7) to guarantee at least one credential is missing
		// bits: 0=apiKey missing, 1=secretKey missing, 2=passphrase missing
		missingMask := rapid.IntRange(1, 7).Draw(t, "missingMask")

		apiKeyMissing := missingMask&1 != 0
		secretKeyMissing := missingMask&2 != 0
		passphraseMissing := missingMask&4 != 0

		// Clean env first
		os.Unsetenv("OKX_API_KEY")
		os.Unsetenv("OKX_SECRET_KEY")
		os.Unsetenv("OKX_PASSPHRASE")
		defer func() {
			os.Unsetenv("OKX_API_KEY")
			os.Unsetenv("OKX_SECRET_KEY")
			os.Unsetenv("OKX_PASSPHRASE")
		}()

		// Set credentials that are NOT missing
		if !apiKeyMissing {
			os.Setenv("OKX_API_KEY", nonEmptyString().Draw(t, "apiKeyVal"))
		}
		if !secretKeyMissing {
			os.Setenv("OKX_SECRET_KEY", nonEmptyString().Draw(t, "secretKeyVal"))
		}
		if !passphraseMissing {
			os.Setenv("OKX_PASSPHRASE", nonEmptyString().Draw(t, "passphraseVal"))
		}

		_, err := LoadCredentials()
		if err == nil {
			t.Fatalf("expected error when credentials are missing (mask=%d)", missingMask)
		}

		errMsg := err.Error()

		// Verify the error mentions each missing credential
		if apiKeyMissing && !strings.Contains(errMsg, "OKX_API_KEY") {
			t.Fatalf("error should mention OKX_API_KEY when it's missing, got: %v", err)
		}
		if secretKeyMissing && !strings.Contains(errMsg, "OKX_SECRET_KEY") {
			t.Fatalf("error should mention OKX_SECRET_KEY when it's missing, got: %v", err)
		}
		if passphraseMissing && !strings.Contains(errMsg, "OKX_PASSPHRASE") {
			t.Fatalf("error should mention OKX_PASSPHRASE when it's missing, got: %v", err)
		}
	})
}

// TestProperty_CredentialStartupValidation_AllMissingMentionsAll verifies that when
// multiple credentials are missing, the error message mentions ALL missing credential
// names.
func TestProperty_CredentialStartupValidation_AllMissingMentionsAll(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a bitmask for which credentials are missing (at least one must be missing)
		// bits: 0=apiKey missing, 1=secretKey missing, 2=passphrase missing
		missingMask := rapid.IntRange(1, 7).Draw(t, "missingMask")

		apiKeyMissing := missingMask&1 != 0
		secretKeyMissing := missingMask&2 != 0
		passphraseMissing := missingMask&4 != 0

		// Clean env first
		os.Unsetenv("OKX_API_KEY")
		os.Unsetenv("OKX_SECRET_KEY")
		os.Unsetenv("OKX_PASSPHRASE")
		defer func() {
			os.Unsetenv("OKX_API_KEY")
			os.Unsetenv("OKX_SECRET_KEY")
			os.Unsetenv("OKX_PASSPHRASE")
		}()

		// Set credentials that are NOT missing
		if !apiKeyMissing {
			os.Setenv("OKX_API_KEY", nonEmptyString().Draw(t, "apiKeyVal"))
		}
		if !secretKeyMissing {
			os.Setenv("OKX_SECRET_KEY", nonEmptyString().Draw(t, "secretKeyVal"))
		}
		if !passphraseMissing {
			os.Setenv("OKX_PASSPHRASE", nonEmptyString().Draw(t, "passphraseVal"))
		}

		_, err := LoadCredentials()
		if err == nil {
			t.Fatalf("expected error when credentials are missing (mask=%d)", missingMask)
		}

		errMsg := err.Error()

		// The error message must contain the name of EVERY missing credential
		if apiKeyMissing && !strings.Contains(errMsg, "OKX_API_KEY") {
			t.Fatalf("error should mention OKX_API_KEY (mask=%d), got: %v", missingMask, err)
		}
		if secretKeyMissing && !strings.Contains(errMsg, "OKX_SECRET_KEY") {
			t.Fatalf("error should mention OKX_SECRET_KEY (mask=%d), got: %v", missingMask, err)
		}
		if passphraseMissing && !strings.Contains(errMsg, "OKX_PASSPHRASE") {
			t.Fatalf("error should mention OKX_PASSPHRASE (mask=%d), got: %v", missingMask, err)
		}

		// The error message must NOT mention credentials that ARE present
		if !apiKeyMissing && strings.Contains(errMsg, "OKX_API_KEY") {
			t.Fatalf("error should NOT mention OKX_API_KEY when it's present (mask=%d), got: %v", missingMask, err)
		}
		if !secretKeyMissing && strings.Contains(errMsg, "OKX_SECRET_KEY") {
			t.Fatalf("error should NOT mention OKX_SECRET_KEY when it's present (mask=%d), got: %v", missingMask, err)
		}
		if !passphraseMissing && strings.Contains(errMsg, "OKX_PASSPHRASE") {
			t.Fatalf("error should NOT mention OKX_PASSPHRASE when it's present (mask=%d), got: %v", missingMask, err)
		}
	})
}
