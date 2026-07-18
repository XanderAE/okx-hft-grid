package config

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	// DefaultRESTBaseURL is the official OKX REST endpoint.
	DefaultRESTBaseURL = "https://www.okx.com"
	// DefaultPublicWebSocketURL is the official OKX public WebSocket endpoint.
	DefaultPublicWebSocketURL = "wss://ws.okx.com:8443/ws/v5/public"
	// DefaultPrivateWebSocketURL is the official OKX private WebSocket endpoint.
	DefaultPrivateWebSocketURL = "wss://ws.okx.com:8443/ws/v5/private"
)

// ResolvedNetworkEndpoints contains the role-labelled endpoint values produced
// by resolveNetworkEndpoints. Each role has exactly one resolved value.
type ResolvedNetworkEndpoints struct {
	RESTBaseURL         string
	PublicWebSocketURL  string
	PrivateWebSocketURL string
}

// ResolveNetworkEndpoints resolves role-specific endpoint values from the system
// configuration. It is pure: no DNS, dial, request, credential-read, or logging
// side effect. For production mode it validates that endpoints satisfy strict
// scheme/host/port/path rules and that public and private are distinct.
func ResolveNetworkEndpoints(cfg *SystemConfig) (ResolvedNetworkEndpoints, error) {
	if cfg == nil {
		return ResolvedNetworkEndpoints{}, fmt.Errorf("endpoint resolution requires non-nil configuration")
	}

	var resolved ResolvedNetworkEndpoints

	// Resolve REST base
	resolved.RESTBaseURL = strings.TrimSpace(cfg.RESTURL)
	if resolved.RESTBaseURL == "" {
		resolved.RESTBaseURL = DefaultRESTBaseURL
	}

	// Resolve public WebSocket (from websocket_url field)
	resolved.PublicWebSocketURL = strings.TrimSpace(cfg.WebSocketURL)
	if resolved.PublicWebSocketURL == "" {
		resolved.PublicWebSocketURL = DefaultPublicWebSocketURL
	}

	// Resolve private WebSocket (from private_websocket_url field)
	// The public field is NEVER used as the private fallback.
	resolved.PrivateWebSocketURL = strings.TrimSpace(cfg.PrivateWebSocketURL)
	if resolved.PrivateWebSocketURL == "" {
		resolved.PrivateWebSocketURL = DefaultPrivateWebSocketURL
	}

	// Production mode requires strict validation
	if cfg.ExecutionMode == ExecutionModeProduction {
		if err := validateProductionEndpoints(resolved); err != nil {
			return ResolvedNetworkEndpoints{}, err
		}
	}

	return resolved, nil
}

// validateProductionEndpoints enforces strict scheme, host, port, path, and
// role-safety constraints on production endpoints. It rejects:
// - Wrong schemes (non-TLS)
// - Unapproved hosts
// - Wrong ports
// - Wrong paths
// - Userinfo, query, fragment
// - Malformed URLs
// - Role swaps (public path on private, private path on public)
// - Equal public and private endpoints
func validateProductionEndpoints(resolved ResolvedNetworkEndpoints) error {
	// Validate REST endpoint
	if err := validateProductionREST(resolved.RESTBaseURL); err != nil {
		return fmt.Errorf("production REST endpoint: %w", err)
	}

	// Validate public WebSocket endpoint
	if err := validateProductionWebSocket(resolved.PublicWebSocketURL, "public", "/ws/v5/public"); err != nil {
		return fmt.Errorf("production public WebSocket endpoint: %w", err)
	}

	// Validate private WebSocket endpoint
	if err := validateProductionWebSocket(resolved.PrivateWebSocketURL, "private", "/ws/v5/private"); err != nil {
		return fmt.Errorf("production private WebSocket endpoint: %w", err)
	}

	// Require public and private to differ
	if resolved.PublicWebSocketURL == resolved.PrivateWebSocketURL {
		return fmt.Errorf("production WebSocket endpoints must be distinct: public and private are equal")
	}

	return nil
}

// validateProductionREST validates the REST base URL for production mode.
func validateProductionREST(rawURL string) error {
	u, err := parseProductionURL(rawURL)
	if err != nil {
		return err
	}

	if u.Scheme != "https" {
		return fmt.Errorf("requires TLS (https), got scheme %q", u.Scheme)
	}
	if u.Hostname() != "www.okx.com" {
		return fmt.Errorf("requires host www.okx.com, got %q", u.Hostname())
	}
	if u.Port() != "" {
		return fmt.Errorf("requires default port, got %q", u.Port())
	}
	// REST base should have empty or root path
	p := strings.TrimRight(u.Path, "/")
	if p != "" {
		return fmt.Errorf("requires empty path, got %q", u.Path)
	}

	return nil
}

// validateProductionWebSocket validates a WebSocket URL for production mode
// against the expected role path.
func validateProductionWebSocket(rawURL, role, expectedPath string) error {
	u, err := parseProductionURL(rawURL)
	if err != nil {
		return err
	}

	if u.Scheme != "wss" {
		return fmt.Errorf("requires TLS (wss), got scheme %q", u.Scheme)
	}
	if u.Hostname() != "ws.okx.com" {
		return fmt.Errorf("requires host ws.okx.com, got %q", u.Hostname())
	}
	if u.Port() != "8443" {
		return fmt.Errorf("requires port 8443, got %q", u.Port())
	}
	if u.Path != expectedPath {
		// Check for role swap
		otherPath := "/ws/v5/private"
		if role == "private" {
			otherPath = "/ws/v5/public"
		}
		if u.Path == otherPath {
			return fmt.Errorf("role swap detected: %s endpoint has %s path", role, otherRoleName(role))
		}
		return fmt.Errorf("requires path %q, got %q", expectedPath, u.Path)
	}

	return nil
}

// parseProductionURL parses and validates basic structural constraints on a
// production endpoint URL. It rejects malformed URLs, userinfo, query, and fragment.
func parseProductionURL(rawURL string) (*url.URL, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, fmt.Errorf("URL must not be empty")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("malformed URL")
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("malformed URL: missing scheme or host")
	}
	if u.User != nil {
		return nil, fmt.Errorf("URL must not contain userinfo")
	}
	if u.RawQuery != "" {
		return nil, fmt.Errorf("URL must not contain query parameters")
	}
	if u.Fragment != "" {
		return nil, fmt.Errorf("URL must not contain fragment")
	}

	return u, nil
}

func otherRoleName(role string) string {
	if role == "public" {
		return "private"
	}
	return "public"
}
