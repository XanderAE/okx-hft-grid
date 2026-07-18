package config

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
)

var productionCredentialEnvironmentNames = []string{
	"OKX_API_KEY",
	"OKX_SECRET_KEY",
	"OKX_PASSPHRASE",
}

// EndpointGuard validates a request target and its dial address without DNS.
type EndpointGuard interface {
	ValidateEndpoint(rawURL string) error
	ValidateDialAddress(address string) error
}

// DialContextFunc matches net.Dialer's DialContext method.
type DialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

// DemoValidationPolicy is an explicit, non-default exception for an OKX
// simulated-trading account. Every field is required before a non-loopback
// endpoint can be allowlisted.
type DemoValidationPolicy struct {
	Enabled                     bool
	SimulatedTrading            bool
	SimulatedTradingHeader      string
	CredentialClass             string
	EndpointAllowlist           []string
	AccountFingerprint          string
	ApprovedAccountFingerprints []string
}

// AutomatedValidationOptions describe an isolated test/replay process. A nil
// Environment map uses the process environment; a non-nil map is an injectable
// environment snapshot for deterministic tests.
type AutomatedValidationOptions struct {
	ExecutionMode  ExecutionMode
	TradingEnabled bool
	Environment    map[string]string
	LookupEnv      func(string) (string, bool)
	Demo           *DemoValidationPolicy
}

// AutomatedValidationGuard is deny-by-default: unit/replay/simulated execution
// may reach loopback only unless the full demo exception is explicitly proven.
type AutomatedValidationGuard struct {
	options AutomatedValidationOptions
}

// NewAutomatedValidationGuard constructs a policy without performing I/O.
func NewAutomatedValidationGuard(options AutomatedValidationOptions) *AutomatedValidationGuard {
	if options.ExecutionMode == "" {
		options.ExecutionMode = ExecutionModeUnit
	}
	if options.Environment != nil {
		copyEnvironment := make(map[string]string, len(options.Environment))
		for name, value := range options.Environment {
			copyEnvironment[name] = value
		}
		options.Environment = copyEnvironment
	}
	return &AutomatedValidationGuard{options: options}
}

// DefaultAutomatedValidationGuard uses unit mode, disabled trading, and the
// current process environment. It is suitable as the default client policy.
func DefaultAutomatedValidationGuard() *AutomatedValidationGuard {
	return NewAutomatedValidationGuard(AutomatedValidationOptions{
		ExecutionMode:  ExecutionModeUnit,
		TradingEnabled: false,
	})
}

// Validate verifies mode, trading and credential-environment invariants without
// inspecting or returning any credential value.
func (g *AutomatedValidationGuard) Validate() error {
	if g == nil {
		return fmt.Errorf("automated validation guard is required")
	}
	mode := g.options.ExecutionMode
	if mode == "" {
		mode = ExecutionModeUnit
	}
	switch mode {
	case ExecutionModeUnit, ExecutionModeReplay, ExecutionModeSimulated:
	case ExecutionModeProduction:
		return fmt.Errorf("automated validation forbids execution_mode=%s", ExecutionModeProduction)
	default:
		return fmt.Errorf("automated validation rejects unknown execution_mode")
	}
	if g.options.TradingEnabled {
		return fmt.Errorf("automated validation requires trading_enabled=false")
	}
	for _, name := range productionCredentialEnvironmentNames {
		if value, ok := g.lookupEnv(name); ok && strings.TrimSpace(value) != "" {
			return fmt.Errorf("automated validation forbids populated production credential environment %s", name)
		}
	}
	if g.options.Demo != nil && g.options.Demo.Enabled {
		if err := validateDemoPolicy(mode, g.options.Demo); err != nil {
			return err
		}
	}
	return nil
}

func (g *AutomatedValidationGuard) lookupEnv(name string) (string, bool) {
	if g.options.LookupEnv != nil {
		return g.options.LookupEnv(name)
	}
	if g.options.Environment != nil {
		value, ok := g.options.Environment[name]
		return value, ok
	}
	return os.LookupEnv(name)
}

func validateDemoPolicy(mode ExecutionMode, demo *DemoValidationPolicy) error {
	if mode != ExecutionModeSimulated {
		return fmt.Errorf("demo validation requires execution_mode=simulated")
	}
	if !demo.SimulatedTrading || demo.SimulatedTradingHeader != "1" {
		return fmt.Errorf("demo validation requires the simulated-trading marker")
	}
	if demo.CredentialClass != "non-production" {
		return fmt.Errorf("demo validation requires non-production credential classification")
	}
	if len(demo.EndpointAllowlist) == 0 {
		return fmt.Errorf("demo validation requires a non-empty endpoint allowlist")
	}
	if strings.TrimSpace(demo.AccountFingerprint) == "" || !stringInList(demo.AccountFingerprint, demo.ApprovedAccountFingerprints) {
		return fmt.Errorf("demo validation requires an approved account fingerprint")
	}
	return nil
}

// ValidateEndpoint rejects forbidden targets from parsed text alone. It never
// resolves a hostname and therefore fails before DNS or dial.
func (g *AutomatedValidationGuard) ValidateEndpoint(rawURL string) error {
	if err := g.Validate(); err != nil {
		return err
	}
	scheme, host, err := parseEndpoint(rawURL)
	if err != nil {
		return err
	}
	if scheme != "http" && scheme != "https" && scheme != "ws" && scheme != "wss" {
		return fmt.Errorf("automated validation rejects endpoint scheme")
	}
	return g.validateHost(host)
}

// ValidateDialAddress applies the same deny policy to host:port before an
// underlying dialer can resolve or connect.
func (g *AutomatedValidationGuard) ValidateDialAddress(address string) error {
	if err := g.Validate(); err != nil {
		return err
	}
	host := address
	if splitHost, _, err := net.SplitHostPort(address); err == nil {
		host = splitHost
	}
	host = canonicalHost(host)
	if host == "" {
		return fmt.Errorf("automated validation rejects an empty dial target")
	}
	return g.validateHost(host)
}

func (g *AutomatedValidationGuard) validateHost(host string) error {
	host = canonicalHost(host)
	if host == "" {
		return fmt.Errorf("automated validation rejects an empty endpoint host")
	}
	if isMetadataHost(host) {
		return fmt.Errorf("automated validation forbids the EC2 metadata target")
	}
	if isAmazonAWSHost(host) {
		return fmt.Errorf("automated validation forbids AWS production hosts")
	}
	if isLoopbackHost(host) {
		return nil
	}
	if g.demoAllowsHost(host) {
		return nil
	}
	if isProductionOKXHost(host) {
		return fmt.Errorf("automated validation forbids production OKX hosts")
	}
	if address, err := netip.ParseAddr(host); err == nil && !address.Unmap().IsLoopback() {
		return fmt.Errorf("automated validation forbids non-loopback IP targets")
	}
	return fmt.Errorf("automated validation permits loopback targets only")
}

func (g *AutomatedValidationGuard) demoAllowsHost(host string) bool {
	demo := g.options.Demo
	if demo == nil || !demo.Enabled || validateDemoPolicy(g.options.ExecutionMode, demo) != nil {
		return false
	}
	for _, allowed := range demo.EndpointAllowlist {
		if allowlistHost(allowed) == host {
			return true
		}
	}
	return false
}

// NewRequest validates before delegating to net/http request construction.
func (g *AutomatedValidationGuard) NewRequest(method, rawURL string, body io.Reader) (*http.Request, error) {
	if err := g.ValidateEndpoint(rawURL); err != nil {
		return nil, err
	}
	return http.NewRequest(method, rawURL, body)
}

// GuardDialContext wraps a dial function with pre-DNS target validation.
func (g *AutomatedValidationGuard) GuardDialContext(next DialContextFunc) DialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if err := g.ValidateDialAddress(address); err != nil {
			return nil, err
		}
		if next == nil {
			return nil, fmt.Errorf("underlying dialer is required")
		}
		return next(ctx, network, address)
	}
}

// GuardRoundTripper is a defense-in-depth check for redirects or clients that
// did not use NewRequest. Direct callers should still validate before request
// construction.
type GuardRoundTripper struct {
	Guard EndpointGuard
	Next  http.RoundTripper
}

// RoundTrip rejects the target before invoking the underlying transport.
func (t *GuardRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if t == nil || t.Guard == nil {
		return nil, fmt.Errorf("endpoint guard is required")
	}
	if request == nil || request.URL == nil {
		return nil, fmt.Errorf("request target is required")
	}
	if err := t.Guard.ValidateEndpoint(request.URL.String()); err != nil {
		return nil, err
	}
	next := t.Next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(request)
}

// WrapRoundTripper applies the policy to every request handled by next.
func (g *AutomatedValidationGuard) WrapRoundTripper(next http.RoundTripper) http.RoundTripper {
	return &GuardRoundTripper{Guard: g, Next: next}
}

// ProductionNetworkGuard is an explicit post-validation policy. It is never
// selected by default and only permits official OKX HTTPS/WSS hosts.
type ProductionNetworkGuard struct{}

// NewProductionNetworkGuard requires a valid production profile, including all
// gate evidence whenever production trading is enabled.
func NewProductionNetworkGuard(cfg *SystemConfig) (*ProductionNetworkGuard, error) {
	if err := ValidateProductionConfig(cfg); err != nil {
		return nil, err
	}
	return &ProductionNetworkGuard{}, nil
}

func (g *ProductionNetworkGuard) ValidateEndpoint(rawURL string) error {
	scheme, host, err := parseEndpoint(rawURL)
	if err != nil {
		return err
	}
	if scheme != "https" && scheme != "wss" {
		return fmt.Errorf("production network policy requires TLS endpoints")
	}
	if host != "www.okx.com" && host != "ws.okx.com" {
		return fmt.Errorf("production network policy rejects unapproved endpoint host")
	}
	return nil
}

func (g *ProductionNetworkGuard) ValidateDialAddress(address string) error {
	host := address
	if splitHost, _, err := net.SplitHostPort(address); err == nil {
		host = splitHost
	}
	host = canonicalHost(host)
	if host != "www.okx.com" && host != "ws.okx.com" {
		return fmt.Errorf("production network policy rejects unapproved dial target")
	}
	return nil
}

func parseEndpoint(rawURL string) (string, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" || u.Hostname() == "" {
		return "", "", fmt.Errorf("network policy rejects an invalid endpoint")
	}
	if u.User != nil {
		return "", "", fmt.Errorf("network policy forbids URL userinfo")
	}
	return strings.ToLower(u.Scheme), canonicalHost(u.Hostname()), nil
}

func canonicalHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	return strings.TrimSuffix(host, ".")
}

func isLoopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.Unmap().IsLoopback()
}

func isMetadataHost(host string) bool {
	if host == "169.254.169.254" || host == "instance-data.ec2.internal" {
		return true
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return address.Unmap() == netip.MustParseAddr("169.254.169.254") || address == netip.MustParseAddr("fd00:ec2::254")
}

func isAmazonAWSHost(host string) bool {
	return host == "amazonaws.com" || strings.HasSuffix(host, ".amazonaws.com")
}

func isProductionOKXHost(host string) bool {
	return host == "okx.com" || strings.HasSuffix(host, ".okx.com")
}

func allowlistHost(entry string) string {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return ""
	}
	if strings.Contains(entry, "://") {
		_, host, err := parseEndpoint(entry)
		if err != nil {
			return ""
		}
		return host
	}
	if parsed, err := url.Parse("//" + entry); err == nil && parsed.Hostname() != "" {
		return canonicalHost(parsed.Hostname())
	}
	return canonicalHost(entry)
}

func stringInList(target string, values []string) bool {
	for _, value := range values {
		if target == value {
			return true
		}
	}
	return false
}
