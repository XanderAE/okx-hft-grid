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

// credentialValue represents a credential that can be: unset, empty string, or a valid non-empty string.
type credentialValue struct {
	isSet bool
	value string
}

// isEmpty returns true if the credential is either unset or an empty string.
func (c credentialValue) isEmpty() bool {
	return !c.isSet || c.value == ""
}

// validCredentialGen generates non-empty strings suitable for credential values.
func validCredentialGen() *rapid.Generator[string] {
	return rapid.StringMatching(`[a-zA-Z0-9\-_]{1,64}`)
}

// invalidCredentialGen generates a credential that is either unset or set to empty/whitespace-only.
// The system should reject both unset and empty string credentials.
func invalidCredentialGen() *rapid.Generator[credentialValue] {
	return rapid.Custom[credentialValue](func(t *rapid.T) credentialValue {
		// 0 = unset, 1 = empty string
		mode := rapid.IntRange(0, 1).Draw(t, "invalidMode")
		if mode == 0 {
			return credentialValue{isSet: false, value: ""}
		}
		return credentialValue{isSet: true, value: ""}
	})
}

// setCredentialEnv sets or unsets a credential environment variable.
func setCredentialEnv(envVar string, cv credentialValue) {
	if cv.isSet {
		os.Setenv(envVar, cv.value)
	} else {
		os.Unsetenv(envVar)
	}
}

// cleanupCredentialEnv unsets all credential environment variables.
func cleanupCredentialEnv() {
	os.Unsetenv("OKX_API_KEY")
	os.Unsetenv("OKX_SECRET_KEY")
	os.Unsetenv("OKX_PASSPHRASE")
}

// TestProperty_CredentialValidation_ValidCredentialsAccepted verifies that for any
// set of 3 non-empty credential strings, LoadCredentials succeeds and returns
// those exact values.
func TestProperty_CredentialValidation_ValidCredentialsAccepted(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		apiKey := validCredentialGen().Draw(t, "apiKey")
		secretKey := validCredentialGen().Draw(t, "secretKey")
		passphrase := validCredentialGen().Draw(t, "passphrase")

		os.Setenv("OKX_API_KEY", apiKey)
		os.Setenv("OKX_SECRET_KEY", secretKey)
		os.Setenv("OKX_PASSPHRASE", passphrase)
		defer cleanupCredentialEnv()

		creds, err := LoadCredentials()
		if err != nil {
			t.Fatalf("expected no error with all valid credentials, got: %v", err)
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

// TestProperty_CredentialValidation_AnyMissingOrEmptyRejected verifies that for any
// combination where at least one credential is missing or empty, LoadCredentials
// returns an error. Covers both unset and empty string cases.
func TestProperty_CredentialValidation_AnyMissingOrEmptyRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a bitmask (1-7) to determine which credentials are invalid
		// bits: 0=apiKey invalid, 1=secretKey invalid, 2=passphrase invalid
		invalidMask := rapid.IntRange(1, 7).Draw(t, "invalidMask")

		apiKeyInvalid := invalidMask&1 != 0
		secretKeyInvalid := invalidMask&2 != 0
		passphraseInvalid := invalidMask&4 != 0

		cleanupCredentialEnv()
		defer cleanupCredentialEnv()

		// For invalid credentials, choose between unset and empty string
		if apiKeyInvalid {
			cv := invalidCredentialGen().Draw(t, "apiKeyCred")
			setCredentialEnv("OKX_API_KEY", cv)
		} else {
			os.Setenv("OKX_API_KEY", validCredentialGen().Draw(t, "apiKeyVal"))
		}

		if secretKeyInvalid {
			cv := invalidCredentialGen().Draw(t, "secretKeyCred")
			setCredentialEnv("OKX_SECRET_KEY", cv)
		} else {
			os.Setenv("OKX_SECRET_KEY", validCredentialGen().Draw(t, "secretKeyVal"))
		}

		if passphraseInvalid {
			cv := invalidCredentialGen().Draw(t, "passphraseCred")
			setCredentialEnv("OKX_PASSPHRASE", cv)
		} else {
			os.Setenv("OKX_PASSPHRASE", validCredentialGen().Draw(t, "passphraseVal"))
		}

		_, err := LoadCredentials()
		if err == nil {
			t.Fatalf("expected error when credentials are missing/empty (invalidMask=%d)", invalidMask)
		}

		errMsg := err.Error()

		// Verify the error mentions each invalid credential
		if apiKeyInvalid && !strings.Contains(errMsg, "OKX_API_KEY") {
			t.Fatalf("error should mention OKX_API_KEY when it's invalid, got: %v", err)
		}
		if secretKeyInvalid && !strings.Contains(errMsg, "OKX_SECRET_KEY") {
			t.Fatalf("error should mention OKX_SECRET_KEY when it's invalid, got: %v", err)
		}
		if passphraseInvalid && !strings.Contains(errMsg, "OKX_PASSPHRASE") {
			t.Fatalf("error should mention OKX_PASSPHRASE when it's invalid, got: %v", err)
		}

		// Verify the error does NOT mention credentials that are valid
		if !apiKeyInvalid && strings.Contains(errMsg, "OKX_API_KEY") {
			t.Fatalf("error should NOT mention OKX_API_KEY when it's valid (mask=%d), got: %v", invalidMask, err)
		}
		if !secretKeyInvalid && strings.Contains(errMsg, "OKX_SECRET_KEY") {
			t.Fatalf("error should NOT mention OKX_SECRET_KEY when it's valid (mask=%d), got: %v", invalidMask, err)
		}
		if !passphraseInvalid && strings.Contains(errMsg, "OKX_PASSPHRASE") {
			t.Fatalf("error should NOT mention OKX_PASSPHRASE when it's valid (mask=%d), got: %v", invalidMask, err)
		}
	})
}

// TestProperty_CredentialValidation_AllPermutationsCovered verifies that every
// single permutation of missing credentials (7 total: 1 missing, 2 missing, 3 missing)
// is correctly detected and all missing names are included in the error.
func TestProperty_CredentialValidation_AllPermutationsCovered(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// For each of the 3 credentials, independently decide if it's: unset (0) or empty (1)
		// At least one must be invalid, so we pick a mask from 1-7
		missingMask := rapid.IntRange(1, 7).Draw(t, "missingMask")

		// For each missing credential, randomly choose between unset and empty
		apiKeyMode := rapid.IntRange(0, 1).Draw(t, "apiKeyMode")     // 0=unset, 1=empty
		secretKeyMode := rapid.IntRange(0, 1).Draw(t, "secretKeyMode")
		passphraseMode := rapid.IntRange(0, 1).Draw(t, "passphraseMode")

		cleanupCredentialEnv()
		defer cleanupCredentialEnv()

		apiKeyMissing := missingMask&1 != 0
		secretKeyMissing := missingMask&2 != 0
		passphraseMissing := missingMask&4 != 0

		// Set up environment
		if apiKeyMissing {
			if apiKeyMode == 1 {
				os.Setenv("OKX_API_KEY", "")
			}
			// mode 0 = already unset from cleanup
		} else {
			os.Setenv("OKX_API_KEY", validCredentialGen().Draw(t, "validApiKey"))
		}

		if secretKeyMissing {
			if secretKeyMode == 1 {
				os.Setenv("OKX_SECRET_KEY", "")
			}
		} else {
			os.Setenv("OKX_SECRET_KEY", validCredentialGen().Draw(t, "validSecretKey"))
		}

		if passphraseMissing {
			if passphraseMode == 1 {
				os.Setenv("OKX_PASSPHRASE", "")
			}
		} else {
			os.Setenv("OKX_PASSPHRASE", validCredentialGen().Draw(t, "validPassphrase"))
		}

		_, err := LoadCredentials()
		if err == nil {
			t.Fatalf("expected error for missingMask=%d (apiMode=%d, secretMode=%d, passMode=%d)",
				missingMask, apiKeyMode, secretKeyMode, passphraseMode)
		}

		errMsg := err.Error()

		// Count how many missing credentials are mentioned in the error
		expectedMissing := 0
		if apiKeyMissing {
			expectedMissing++
			if !strings.Contains(errMsg, "OKX_API_KEY") {
				t.Fatalf("error should mention OKX_API_KEY (mask=%d), got: %v", missingMask, err)
			}
		}
		if secretKeyMissing {
			expectedMissing++
			if !strings.Contains(errMsg, "OKX_SECRET_KEY") {
				t.Fatalf("error should mention OKX_SECRET_KEY (mask=%d), got: %v", missingMask, err)
			}
		}
		if passphraseMissing {
			expectedMissing++
			if !strings.Contains(errMsg, "OKX_PASSPHRASE") {
				t.Fatalf("error should mention OKX_PASSPHRASE (mask=%d), got: %v", missingMask, err)
			}
		}

		// Verify at least one was expected missing
		if expectedMissing == 0 {
			t.Fatalf("test logic error: missingMask=%d should have at least one missing credential", missingMask)
		}
	})
}

// TestProperty_CredentialValidation_EmptyStringEquivalentToUnset verifies that
// setting a credential to an empty string is treated the same as not setting it.
func TestProperty_CredentialValidation_EmptyStringEquivalentToUnset(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Pick which credential to test (0=apiKey, 1=secretKey, 2=passphrase)
		credIndex := rapid.IntRange(0, 2).Draw(t, "credIndex")

		// Set all valid first
		validKey := validCredentialGen().Draw(t, "validKey")
		validSecret := validCredentialGen().Draw(t, "validSecret")
		validPass := validCredentialGen().Draw(t, "validPass")

		// Test with unset
		cleanupCredentialEnv()
		os.Setenv("OKX_API_KEY", validKey)
		os.Setenv("OKX_SECRET_KEY", validSecret)
		os.Setenv("OKX_PASSPHRASE", validPass)

		envVars := []string{"OKX_API_KEY", "OKX_SECRET_KEY", "OKX_PASSPHRASE"}
		targetEnv := envVars[credIndex]

		// Test unset case
		os.Unsetenv(targetEnv)
		_, errUnset := LoadCredentials()

		// Restore and test empty string case
		os.Setenv("OKX_API_KEY", validKey)
		os.Setenv("OKX_SECRET_KEY", validSecret)
		os.Setenv("OKX_PASSPHRASE", validPass)
		os.Setenv(targetEnv, "")
		_, errEmpty := LoadCredentials()

		defer cleanupCredentialEnv()

		// Both should return errors
		if errUnset == nil {
			t.Fatalf("expected error when %s is unset", targetEnv)
		}
		if errEmpty == nil {
			t.Fatalf("expected error when %s is empty string", targetEnv)
		}

		// Both errors should mention the missing credential
		if !strings.Contains(errUnset.Error(), targetEnv) {
			t.Fatalf("unset error should mention %s, got: %v", targetEnv, errUnset)
		}
		if !strings.Contains(errEmpty.Error(), targetEnv) {
			t.Fatalf("empty string error should mention %s, got: %v", targetEnv, errEmpty)
		}
	})
}
