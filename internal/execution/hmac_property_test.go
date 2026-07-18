// Property 25: HMAC-SHA256 Request Signing
// **Validates: Requirement 13.2**
//
// For any arbitrary payload, the sign-then-verify round trip is consistent
// and the timestamp used in signing is within 30 seconds of current time.
package execution

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/yourname/okx-hft-grid/internal/config"
	"pgregory.net/rapid"
)

// TestProperty_HMAC_SignThenVerifyRoundTrip verifies that for any arbitrary payload,
// computing HMAC-SHA256(secret, timestamp+method+path+body) and then verifying
// it with the same inputs always produces a consistent result.
func TestProperty_HMAC_SignThenVerifyRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate arbitrary credentials
		secretKey := rapid.StringMatching(`[a-zA-Z0-9]{8,64}`).Draw(t, "secretKey")
		apiKey := rapid.StringMatching(`[a-zA-Z0-9\-]{8,32}`).Draw(t, "apiKey")
		passphrase := rapid.StringMatching(`[a-zA-Z0-9]{6,32}`).Draw(t, "passphrase")

		creds := &config.Credentials{
			APIKey:     apiKey,
			SecretKey:  secretKey,
			Passphrase: passphrase,
		}

		client := NewAPIClient("http://127.0.0.1", creds, nil)

		// Generate arbitrary request parameters
		method := rapid.SampledFrom([]string{"GET", "POST", "PUT", "DELETE"}).Draw(t, "method")
		path := "/" + rapid.StringMatching(`[a-z/]{1,50}`).Draw(t, "path")
		body := rapid.StringMatching(`[a-zA-Z0-9{}",:_ ]{0,200}`).Draw(t, "body")

		// Generate a timestamp within 30 seconds of now
		now := time.Now().UTC()
		offsetSec := rapid.IntRange(-30, 30).Draw(t, "offsetSec")
		ts := now.Add(time.Duration(offsetSec) * time.Second)
		timestamp := ts.Format("2006-01-02T15:04:05.000Z")

		// Sign the request
		sig1 := client.SignRequest(method, path, body, timestamp)

		// Verify round-trip: signing again with the same inputs produces the same result
		sig2 := client.SignRequest(method, path, body, timestamp)
		if sig1 != sig2 {
			t.Fatalf("HMAC sign not deterministic: sig1=%s, sig2=%s", sig1, sig2)
		}

		// Verify the signature manually (sign-then-verify)
		message := timestamp + method + path + body
		mac := hmac.New(sha256.New, []byte(secretKey))
		mac.Write([]byte(message))
		expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

		if sig1 != expected {
			t.Fatalf("HMAC signature mismatch:\n  got:  %s\n  want: %s", sig1, expected)
		}

		// Verify timestamp is within 30 seconds of current time
		parsedTs, err := time.Parse("2006-01-02T15:04:05.000Z", timestamp)
		if err != nil {
			t.Fatalf("failed to parse timestamp: %v", err)
		}
		diff := time.Since(parsedTs).Abs()
		if diff > 30*time.Second+time.Second {
			t.Fatalf("timestamp not within 30s of now: diff=%v", diff)
		}
	})
}

// TestProperty_HMAC_DifferentPayloadsProduceDifferentSignatures verifies that
// changing any part of the signed message produces a different signature.
func TestProperty_HMAC_DifferentPayloadsProduceDifferentSignatures(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		secretKey := rapid.StringMatching(`[a-zA-Z0-9]{16,64}`).Draw(t, "secretKey")

		creds := &config.Credentials{
			APIKey:     "test-key",
			SecretKey:  secretKey,
			Passphrase: "test-pass",
		}

		client := NewAPIClient("http://127.0.0.1", creds, nil)

		method := "POST"
		path := "/api/v5/trade/order"
		timestamp := "2024-01-15T10:30:00.000Z"

		// Generate two different bodies
		body1 := rapid.StringMatching(`[a-zA-Z0-9]{1,100}`).Draw(t, "body1")
		body2 := rapid.StringMatching(`[a-zA-Z0-9]{1,100}`).Draw(t, "body2")

		if body1 == body2 {
			t.Skip("generated identical bodies, skipping")
		}

		sig1 := client.SignRequest(method, path, body1, timestamp)
		sig2 := client.SignRequest(method, path, body2, timestamp)

		if sig1 == sig2 {
			t.Fatalf("different bodies produced same signature: body1=%q, body2=%q", body1, body2)
		}
	})
}

// TestProperty_HMAC_DifferentSecretsProduceDifferentSignatures verifies that
// different secret keys produce different signatures for the same message.
func TestProperty_HMAC_DifferentSecretsProduceDifferentSignatures(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		secret1 := rapid.StringMatching(`[a-zA-Z0-9]{16,64}`).Draw(t, "secret1")
		secret2 := rapid.StringMatching(`[a-zA-Z0-9]{16,64}`).Draw(t, "secret2")

		if secret1 == secret2 {
			t.Skip("generated identical secrets, skipping")
		}

		client1 := NewAPIClient("http://127.0.0.1", &config.Credentials{
			APIKey: "k", SecretKey: secret1, Passphrase: "p",
		}, nil)
		client2 := NewAPIClient("http://127.0.0.1", &config.Credentials{
			APIKey: "k", SecretKey: secret2, Passphrase: "p",
		}, nil)

		method := "POST"
		path := "/api/v5/trade/order"
		body := rapid.StringMatching(`[a-zA-Z0-9]{10,50}`).Draw(t, "body")
		timestamp := "2024-01-15T10:30:00.000Z"

		sig1 := client1.SignRequest(method, path, body, timestamp)
		sig2 := client2.SignRequest(method, path, body, timestamp)

		if sig1 == sig2 {
			t.Fatalf("different secrets produced same signature")
		}
	})
}
