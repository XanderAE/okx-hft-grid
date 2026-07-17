package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoadCredentials_AllSet(t *testing.T) {
	// Set all required env vars
	os.Setenv("OKX_API_KEY", "test-api-key-12345")
	os.Setenv("OKX_SECRET_KEY", "test-secret-key-67890")
	os.Setenv("OKX_PASSPHRASE", "test-passphrase")
	defer func() {
		os.Unsetenv("OKX_API_KEY")
		os.Unsetenv("OKX_SECRET_KEY")
		os.Unsetenv("OKX_PASSPHRASE")
	}()

	creds, err := LoadCredentials()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if creds.APIKey != "test-api-key-12345" {
		t.Errorf("expected APIKey 'test-api-key-12345', got %q", creds.APIKey)
	}
	if creds.SecretKey != "test-secret-key-67890" {
		t.Errorf("expected SecretKey 'test-secret-key-67890', got %q", creds.SecretKey)
	}
	if creds.Passphrase != "test-passphrase" {
		t.Errorf("expected Passphrase 'test-passphrase', got %q", creds.Passphrase)
	}
}

func TestLoadCredentials_MissingAPIKey(t *testing.T) {
	os.Unsetenv("OKX_API_KEY")
	os.Setenv("OKX_SECRET_KEY", "test-secret")
	os.Setenv("OKX_PASSPHRASE", "test-passphrase")
	defer func() {
		os.Unsetenv("OKX_SECRET_KEY")
		os.Unsetenv("OKX_PASSPHRASE")
	}()

	_, err := LoadCredentials()
	if err == nil {
		t.Fatal("expected error when OKX_API_KEY is missing")
	}
	if !strings.Contains(err.Error(), "OKX_API_KEY") {
		t.Errorf("error should mention OKX_API_KEY, got: %v", err)
	}
}

func TestLoadCredentials_MissingSecretKey(t *testing.T) {
	os.Setenv("OKX_API_KEY", "test-api-key")
	os.Unsetenv("OKX_SECRET_KEY")
	os.Setenv("OKX_PASSPHRASE", "test-passphrase")
	defer func() {
		os.Unsetenv("OKX_API_KEY")
		os.Unsetenv("OKX_PASSPHRASE")
	}()

	_, err := LoadCredentials()
	if err == nil {
		t.Fatal("expected error when OKX_SECRET_KEY is missing")
	}
	if !strings.Contains(err.Error(), "OKX_SECRET_KEY") {
		t.Errorf("error should mention OKX_SECRET_KEY, got: %v", err)
	}
}

func TestLoadCredentials_MissingPassphrase(t *testing.T) {
	os.Setenv("OKX_API_KEY", "test-api-key")
	os.Setenv("OKX_SECRET_KEY", "test-secret")
	os.Unsetenv("OKX_PASSPHRASE")
	defer func() {
		os.Unsetenv("OKX_API_KEY")
		os.Unsetenv("OKX_SECRET_KEY")
	}()

	_, err := LoadCredentials()
	if err == nil {
		t.Fatal("expected error when OKX_PASSPHRASE is missing")
	}
	if !strings.Contains(err.Error(), "OKX_PASSPHRASE") {
		t.Errorf("error should mention OKX_PASSPHRASE, got: %v", err)
	}
}

func TestLoadCredentials_AllMissing(t *testing.T) {
	os.Unsetenv("OKX_API_KEY")
	os.Unsetenv("OKX_SECRET_KEY")
	os.Unsetenv("OKX_PASSPHRASE")

	_, err := LoadCredentials()
	if err == nil {
		t.Fatal("expected error when all credentials are missing")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "OKX_API_KEY") {
		t.Errorf("error should mention OKX_API_KEY, got: %v", err)
	}
	if !strings.Contains(errMsg, "OKX_SECRET_KEY") {
		t.Errorf("error should mention OKX_SECRET_KEY, got: %v", err)
	}
	if !strings.Contains(errMsg, "OKX_PASSPHRASE") {
		t.Errorf("error should mention OKX_PASSPHRASE, got: %v", err)
	}
}

func TestLoadCredentials_EmptyString(t *testing.T) {
	os.Setenv("OKX_API_KEY", "")
	os.Setenv("OKX_SECRET_KEY", "valid-secret")
	os.Setenv("OKX_PASSPHRASE", "valid-pass")
	defer func() {
		os.Unsetenv("OKX_API_KEY")
		os.Unsetenv("OKX_SECRET_KEY")
		os.Unsetenv("OKX_PASSPHRASE")
	}()

	_, err := LoadCredentials()
	if err == nil {
		t.Fatal("expected error when OKX_API_KEY is empty string")
	}
}
