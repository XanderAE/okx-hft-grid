package config

import (
	"errors"
	"fmt"
	"os"
)

// Credentials holds OKX API authentication credentials.
type Credentials struct {
	APIKey     string
	SecretKey  string
	Passphrase string
}

// LoadCredentials reads API credentials from environment variables and validates
// that all required fields are non-empty. Returns an error if any credential is missing.
func LoadCredentials() (*Credentials, error) {
	apiKey := os.Getenv("OKX_API_KEY")
	secretKey := os.Getenv("OKX_SECRET_KEY")
	passphrase := os.Getenv("OKX_PASSPHRASE")

	var errs []string

	if apiKey == "" {
		errs = append(errs, "OKX_API_KEY environment variable is not set or empty")
	}
	if secretKey == "" {
		errs = append(errs, "OKX_SECRET_KEY environment variable is not set or empty")
	}
	if passphrase == "" {
		errs = append(errs, "OKX_PASSPHRASE environment variable is not set or empty")
	}

	if len(errs) > 0 {
		msg := "credential validation failed:"
		for _, e := range errs {
			msg += fmt.Sprintf("\n  - %s", e)
		}
		return nil, errors.New(msg)
	}

	return &Credentials{
		APIKey:     apiKey,
		SecretKey:  secretKey,
		Passphrase: passphrase,
	}, nil
}
