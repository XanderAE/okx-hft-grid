package config

import "regexp"

const sanitizedMask = "********"

// Precompiled credential patterns for log sanitization.
var (
	// Matches key=value or key: value patterns where key indicates a credential
	kvPattern = regexp.MustCompile(`(?i)((?:api[_-]?key|secret[_-]?key|passphrase|password|token|api[_-]?secret|access[_-]?key)[=:]\s*)["']?([^\s,;"']+)["']?`)
	// Matches standalone hex strings of 32+ chars (common API key format)
	hexPattern = regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)
	// Matches base64-encoded strings of 24+ chars (common secret format)
	base64Pattern = regexp.MustCompile(`\b[A-Za-z0-9+/]{24,}={0,2}\b`)
)

// SanitizeLog replaces credential patterns in the input string with a fixed-length
// mask of exactly 8 asterisks ("********"). This prevents API keys, secrets, and
// passphrases from appearing in log output.
//
// Patterns matched:
//   - Key=value pairs with known credential key names (api_key, secret_key, passphrase, etc.)
//   - Standalone hex strings of 32+ characters
//   - Base64-encoded strings of 24+ characters
func SanitizeLog(input string) string {
	result := input

	// First, handle key=value patterns (replace only the value portion)
	result = kvPattern.ReplaceAllString(result, "${1}"+sanitizedMask)

	// Then handle standalone hex strings (32+ chars)
	result = hexPattern.ReplaceAllString(result, sanitizedMask)

	// Handle standalone base64 strings (24+ chars)
	result = base64Pattern.ReplaceAllLiteralString(result, sanitizedMask)

	return result
}
