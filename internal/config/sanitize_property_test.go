package config

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// **Validates: Requirements 13.4**
//
// Property 24: Log Sanitization
// For any log event containing API key, secret key, passphrase, or any string matching
// configured credential patterns, the sanitized log output SHALL NOT contain the secret
// values, replacing them with a fixed-length mask of 8 asterisk characters.

// Property: For any string containing "api_key=<value>" where value is 16+ alphanumeric
// chars, SanitizeLog output does NOT contain the value.
func TestProperty_APIKeyValueIsRemoved(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		valueLen := rapid.IntRange(16, 64).Draw(t, "valueLen")
		value := rapid.StringMatching("[a-zA-Z0-9]{"+itoa(valueLen)+"}").Draw(t, "value")

		prefix := rapid.StringMatching("[a-z ]{0,20}").Draw(t, "prefix")
		suffix := rapid.StringMatching("[a-z ]{0,20}").Draw(t, "suffix")

		input := prefix + "api_key=" + value + " " + suffix
		result := SanitizeLog(input)

		if strings.Contains(result, value) {
			t.Fatalf("SanitizeLog output still contains api_key value %q\nInput:  %s\nOutput: %s", value, input, result)
		}
	})
}

// Property: For any string containing "secret_key=<value>" where value is 16+ chars,
// SanitizeLog output does NOT contain the value.
func TestProperty_SecretKeyValueIsRemoved(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		valueLen := rapid.IntRange(16, 64).Draw(t, "valueLen")
		value := rapid.StringMatching("[a-zA-Z0-9]{"+itoa(valueLen)+"}").Draw(t, "value")

		prefix := rapid.StringMatching("[a-z ]{0,20}").Draw(t, "prefix")
		suffix := rapid.StringMatching("[a-z ]{0,20}").Draw(t, "suffix")

		input := prefix + "secret_key=" + value + " " + suffix
		result := SanitizeLog(input)

		if strings.Contains(result, value) {
			t.Fatalf("SanitizeLog output still contains secret_key value %q\nInput:  %s\nOutput: %s", value, input, result)
		}
	})
}

// Property: For any string containing "passphrase=<value>" where value is 16+ chars,
// SanitizeLog output does NOT contain the value.
func TestProperty_PassphraseValueIsRemoved(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		valueLen := rapid.IntRange(16, 64).Draw(t, "valueLen")
		value := rapid.StringMatching("[a-zA-Z0-9]{"+itoa(valueLen)+"}").Draw(t, "value")

		prefix := rapid.StringMatching("[a-z ]{0,20}").Draw(t, "prefix")
		suffix := rapid.StringMatching("[a-z ]{0,20}").Draw(t, "suffix")

		input := prefix + "passphrase=" + value + " " + suffix
		result := SanitizeLog(input)

		if strings.Contains(result, value) {
			t.Fatalf("SanitizeLog output still contains passphrase value %q\nInput:  %s\nOutput: %s", value, input, result)
		}
	})
}

// Property: For any string containing a standalone hex string of 32+ chars,
// SanitizeLog output does NOT contain that hex string.
func TestProperty_HexStringIsRemoved(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		hexLen := rapid.IntRange(32, 64).Draw(t, "hexLen")
		hexStr := rapid.StringMatching("[0-9a-f]{"+itoa(hexLen)+"}").Draw(t, "hexStr")

		prefix := rapid.StringMatching("[a-z ]{0,20}").Draw(t, "prefix")
		suffix := rapid.StringMatching("[a-z ]{0,20}").Draw(t, "suffix")

		input := prefix + " " + hexStr + " " + suffix
		result := SanitizeLog(input)

		if strings.Contains(result, hexStr) {
			t.Fatalf("SanitizeLog output still contains hex string %q\nInput:  %s\nOutput: %s", hexStr, input, result)
		}
	})
}

// Property: For any plain text that does NOT match credential patterns (short words,
// normal sentences without hex/base64), SanitizeLog returns the input unchanged.
func TestProperty_NonCredentialTextUnchanged(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		wordCount := rapid.IntRange(1, 8).Draw(t, "wordCount")
		words := make([]string, wordCount)
		for i := range words {
			// Only lowercase letters, max 10 chars (well below 24/32 thresholds)
			wordLen := rapid.IntRange(1, 10).Draw(t, "wordLen")
			words[i] = rapid.StringMatching("[a-z]{"+itoa(wordLen)+"}").Draw(t, "word")
		}
		input := strings.Join(words, " ")

		result := SanitizeLog(input)

		if result != input {
			t.Fatalf("Non-credential input was modified\nInput:  %q\nOutput: %q", input, result)
		}
	})
}

// Property: The mask in the output is always exactly "********" (8 asterisks).
// When credentials are present, the output must contain exactly the mask pattern
// and not a different number of asterisks.
func TestProperty_MaskIsExactly8Asterisks(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		valueLen := rapid.IntRange(16, 64).Draw(t, "valueLen")
		value := rapid.StringMatching("[a-zA-Z0-9]{"+itoa(valueLen)+"}").Draw(t, "value")

		keyName := rapid.SampledFrom([]string{"api_key", "secret_key", "passphrase", "token"}).Draw(t, "keyName")
		input := keyName + "=" + value

		result := SanitizeLog(input)

		// The result must contain the 8-asterisk mask
		if !strings.Contains(result, "********") {
			t.Fatalf("Sanitized output does not contain the 8-asterisk mask\nInput:  %s\nOutput: %s", input, result)
		}

		// Verify that any sequence of asterisks in the output is exactly 8 long.
		withoutMask := strings.ReplaceAll(result, "********", "")
		if strings.Contains(withoutMask, "*") {
			t.Fatalf("Output contains asterisks that are not part of the 8-char mask\nInput:  %s\nOutput: %s\nAfter removing mask: %s", input, result, withoutMask)
		}
	})
}

// Property: When a secret appears at the beginning, middle, or end of a string,
// it is always sanitized regardless of position.
func TestProperty_SecretAtVariousPositions(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		valueLen := rapid.IntRange(32, 48).Draw(t, "valueLen")
		secret := rapid.StringMatching("[0-9a-f]{"+itoa(valueLen)+"}").Draw(t, "secret")

		position := rapid.SampledFrom([]string{"beginning", "middle", "end"}).Draw(t, "position")

		var input string
		switch position {
		case "beginning":
			input = secret + " normal log message here"
		case "middle":
			input = "log prefix " + secret + " log suffix"
		case "end":
			input = "some log message ending with " + secret
		}

		result := SanitizeLog(input)

		if strings.Contains(result, secret) {
			t.Fatalf("Secret at %s position was not sanitized\nInput:  %s\nOutput: %s", position, input, result)
		}
		if !strings.Contains(result, sanitizedMask) {
			t.Fatalf("Output should contain mask when secret is at %s\nInput:  %s\nOutput: %s", position, input, result)
		}
	})
}

// Property: When multiple secrets appear in the same log line, ALL of them
// are sanitized, not just the first occurrence.
func TestProperty_MultipleSecretsAllSanitized(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate two distinct secrets
		len1 := rapid.IntRange(32, 48).Draw(t, "len1")
		len2 := rapid.IntRange(32, 48).Draw(t, "len2")
		secret1 := rapid.StringMatching("[0-9a-f]{"+itoa(len1)+"}").Draw(t, "secret1")
		secret2 := rapid.StringMatching("[0-9a-f]{"+itoa(len2)+"}").Draw(t, "secret2")

		input := "first=" + secret1 + " second=" + secret2 + " end"
		result := SanitizeLog(input)

		if strings.Contains(result, secret1) {
			t.Fatalf("First secret was not sanitized\nInput:  %s\nOutput: %s", input, result)
		}
		if strings.Contains(result, secret2) {
			t.Fatalf("Second secret was not sanitized\nInput:  %s\nOutput: %s", input, result)
		}
	})
}

// Property: Key-value credential patterns with various separators (=, :, with/without
// quotes) are all sanitized.
func TestProperty_CredentialKeyVariants(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		valueLen := rapid.IntRange(16, 48).Draw(t, "valueLen")
		value := rapid.StringMatching("[a-zA-Z0-9]{"+itoa(valueLen)+"}").Draw(t, "value")

		keyName := rapid.SampledFrom([]string{
			"api_key", "api-key", "apikey",
			"secret_key", "secret-key",
			"passphrase", "password", "token",
			"api_secret", "api-secret",
			"access_key", "access-key",
		}).Draw(t, "keyName")

		separator := rapid.SampledFrom([]string{"=", ": "}).Draw(t, "separator")

		input := "config " + keyName + separator + value + " loaded"
		result := SanitizeLog(input)

		if strings.Contains(result, value) {
			t.Fatalf("Credential value for key %q with separator %q was not sanitized\nInput:  %s\nOutput: %s",
				keyName, separator, input, result)
		}
	})
}

// itoa converts an int to its string representation for use in regex quantifiers.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	result := ""
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	return result
}
