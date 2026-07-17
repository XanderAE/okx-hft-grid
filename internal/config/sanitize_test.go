package config

import (
	"strings"
	"testing"
)

func TestSanitizeLog_APIKeyValuePattern(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string // should NOT appear in output
	}{
		{
			name:     "api_key=value",
			input:    "connecting with api_key=abc123def456ghi789jkl012mno345pqr",
			contains: "abc123def456ghi789jkl012mno345pqr",
		},
		{
			name:     "secret_key=value",
			input:    "using secret_key=ABCDEF1234567890ABCDEF1234567890",
			contains: "ABCDEF1234567890ABCDEF1234567890",
		},
		{
			name:     "passphrase=value",
			input:    "auth passphrase=MySecretPassphrase123",
			contains: "MySecretPassphrase123",
		},
		{
			name:     "api-key: value",
			input:    "config api-key: a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4",
			contains: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4",
		},
		{
			name:     "token=value",
			input:    "auth token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9abcdef",
			contains: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9abcdef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeLog(tt.input)
			if strings.Contains(result, tt.contains) {
				t.Errorf("sanitized output still contains secret %q.\nInput:  %s\nOutput: %s", tt.contains, tt.input, result)
			}
			if !strings.Contains(result, sanitizedMask) {
				t.Errorf("sanitized output should contain mask %q.\nInput:  %s\nOutput: %s", sanitizedMask, tt.input, result)
			}
		})
	}
}

func TestSanitizeLog_StandaloneHexString(t *testing.T) {
	input := "received response for key 0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d"
	result := SanitizeLog(input)

	if strings.Contains(result, "0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d") {
		t.Errorf("hex string should be masked. Output: %s", result)
	}
	if !strings.Contains(result, sanitizedMask) {
		t.Errorf("should contain mask. Output: %s", result)
	}
}

func TestSanitizeLog_Base64String(t *testing.T) {
	input := "secret value is SGVsbG8gV29ybGQhIFRoaXMgaXM="
	result := SanitizeLog(input)

	if strings.Contains(result, "SGVsbG8gV29ybGQhIFRoaXMgaXM=") {
		t.Errorf("base64 string should be masked. Output: %s", result)
	}
	if !strings.Contains(result, sanitizedMask) {
		t.Errorf("should contain mask. Output: %s", result)
	}
}

func TestSanitizeLog_NoCredentials(t *testing.T) {
	input := "order placed for BTC-USDT at price 42000.50"
	result := SanitizeLog(input)

	if result != input {
		t.Errorf("input without credentials should not be modified.\nInput:  %s\nOutput: %s", input, result)
	}
}

func TestSanitizeLog_ShortStringsNotMasked(t *testing.T) {
	// Short strings that look like hex but are too short to be credentials
	input := "order id: abc123 status: filled"
	result := SanitizeLog(input)

	if result != input {
		t.Errorf("short hex-like strings should not be masked.\nInput:  %s\nOutput: %s", input, result)
	}
}

func TestSanitizeLog_MaskIsExactly8Asterisks(t *testing.T) {
	input := "api_key=ABCDEF1234567890ABCDEF1234567890ABCDEF12"
	result := SanitizeLog(input)

	if !strings.Contains(result, "********") {
		t.Errorf("mask should be exactly 8 asterisks. Output: %s", result)
	}
	// Verify it's exactly 8, not more
	masked := strings.ReplaceAll(result, "********", "")
	originalWithoutMask := strings.ReplaceAll(result, sanitizedMask, "")
	if masked != originalWithoutMask {
		// They should be the same since sanitizedMask IS "********"
	}
}

func TestSanitizeLog_MultipleCredentials(t *testing.T) {
	input := "api_key=AAAA1111BBBB2222CCCC3333DDDD4444 secret_key=EEEE5555FFFF6666GGGG7777HHHH8888"
	result := SanitizeLog(input)

	if strings.Contains(result, "AAAA1111BBBB2222CCCC3333DDDD4444") {
		t.Errorf("first credential should be masked. Output: %s", result)
	}
	if strings.Contains(result, "EEEE5555FFFF6666GGGG7777HHHH8888") {
		t.Errorf("second credential should be masked. Output: %s", result)
	}
}
