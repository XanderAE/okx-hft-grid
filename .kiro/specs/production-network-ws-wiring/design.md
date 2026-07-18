# Production Network and WebSocket Wiring Bugfix Design

## Overview

This design makes startup network composition explicit and role-safe while preserving the completed `.kiro/specs/production-grid-stabilization` implementation. It does not redesign REST signing, WebSocket protocols, fill handling, trading gates, persistence, or strategy behavior.

The fix has two narrow boundaries:

1. Production REST composition explicitly creates `ProductionNetworkGuard` from the validated production profile and passes it to `NewAPIClientWithEndpointGuard`.
2. Public and private WebSocket URLs are resolved independently; `websocket_url` remains public, and a new `private_websocket_url` supplies the private role.

The production example remains `trading_enabled=false`. All automated validation remains loopback-only, and no test invokes a production URL, credential, EC2 host, DNS lookup, dial, or real order.

## Confirmed Repository Facts

- `cmd/main.go` currently calls `execution.NewAPIClient`, whose documented default is `DefaultAutomatedValidationGuard`.
- `DefaultAutomatedValidationGuard` rejects production mode, populated production credential environment names, official OKX hosts, AWS hosts, metadata, and non-loopback targets before I/O.
- `config.NewProductionNetworkGuard(cfg)` and `execution.NewAPIClientWithEndpointGuard` already exist and fail closed.
- `ProductionNetworkGuard` accepts TLS endpoints only and limits hosts to `www.okx.com` and `ws.okx.com`.
- `marketdata.DefaultWebSocketURL` is the correct public endpoint, and `marketdata.PrivateWebSocketURL` is the correct private endpoint.
- `cmd/main.go` currently overwrites `DefaultPrivateWSClientConfig().URL` with `cfg.WebSocketURL` whenever that public field is populated.
- `SystemConfig`, strict YAML decoding, and the effective config summary currently model only `websocket_url`.
- `deploy/config.example.yaml` sets that field to `/ws/v5/public`; therefore current production composition sends private authentication/subscription traffic to the public endpoint.

## Design Decisions

### Configuration Contract

`SystemConfig` gains one field:

```go
PrivateWebSocketURL string `yaml:"private_websocket_url"`
```

The compatibility and role rules are:

| Role | Config field | Empty-value default | Production value |
|---|---|---|---|
| REST execution | `rest_url` | `https://www.okx.com` | exactly `https://www.okx.com` |
| Public market data | `websocket_url` | `wss://ws.okx.com:8443/ws/v5/public` | exactly that public URL |
| Private orders/fills | `private_websocket_url` | `wss://ws.okx.com:8443/ws/v5/private` | exactly that private URL |

`websocket_url` is not reinterpreted or renamed, minimizing compatibility impact. It is never used as the private fallback. Blank production fields select the role-specific defaults; explicit production values must match the approved scheme, host, port, and path after safe parsing. Userinfo, query, fragment, malformed URLs, plaintext schemes, role swaps, alternate paths, and lookalike hosts fail before client construction or dial.

The approved endpoint literals should have one authoritative definition or invariant tests proving the configuration constants and `marketdata` defaults remain identical. No environment-variable endpoint override or bypass is introduced.

### Resolved Composition

A small pure resolver produces role-labelled values before any network client is constructed:

```go
type resolvedNetworkEndpoints struct {
    RESTBaseURL        string
    PublicWebSocketURL string
    PrivateWebSocketURL string
}

func resolveNetworkEndpoints(cfg *config.SystemConfig) (resolvedNetworkEndpoints, error)
```

Resolution order:

1. Reject nil configuration.
2. Resolve each field independently to its role-specific default.
3. For production mode, run existing production-profile validation, validate all endpoint syntax, apply the exact role rules above, and require public and private URLs to differ.
4. Return role-labelled values; never copy one WebSocket field into the other.
5. For automated modes, retain explicit loopback values for composition tests. Tests must supply both WebSocket roles when they connect clients.

The resolver has no DNS, dial, request, credential-read, or logging side effect, making production defaults safe to test as data.

### REST Guard Selection and Injection

Production and default construction remain deliberately asymmetric:

```text
IF cfg.execution_mode = production THEN
    guard := config.NewProductionNetworkGuard(cfg)
    require guard.ValidateEndpoint(resolved.RESTBaseURL) succeeds
    apiClient := execution.NewAPIClientWithEndpointGuard(
        resolved.RESTBaseURL, credentials, rateLimiter, guard)
ELSE
    apiClient := execution.NewAPIClient(
        resolved.RESTBaseURL, credentials, rateLimiter)
END IF
```

This preserves deny-by-default behavior for every existing default caller. A nil explicit guard still falls back to `DefaultAutomatedValidationGuard`. Production guard creation errors are returned from `initializeComponents`; they are not logged with configuration payloads and are never converted into a permissive fallback.

Tests observe constructor/guard selection through a pure or recording seam. They validate official production URLs by calling guard validation methods only; they never call `DoRequest`, `Connect`, DNS, or an underlying dialer for a production target.

### Public and Private WebSocket Wiring

Composition builds both client configs from their own defaults and assigns only the matching resolved value:

```text
publicConfig := marketdata.DefaultWSClientConfig()
publicConfig.URL = resolved.PublicWebSocketURL
publicConfig.Symbols = cfg.Symbols

privateConfig := marketdata.DefaultPrivateWSClientConfig()
privateConfig.URL = resolved.PrivateWebSocketURL
```

A configured public endpoint cannot mutate `privateConfig`. Existing heartbeat, authentication, subscription, reconnect, callback, and parser configuration remains unchanged.

Automated startup tests use independent loopback endpoints or distinct paths on one loopback server. The public fixture accepts only public subscriptions; the private fixture accepts login plus `orders`. A role-aware fixture returns a local synthetic `60008` error if private subscription reaches the public path, making the original regression deterministic without contacting OKX.

### Strict Production Validation

Production endpoint validation has two layers:

1. **Role validation:** exact REST/public/private bases and paths, performed on parsed URLs before construction.
2. **Network policy:** existing `ProductionNetworkGuard` enforces TLS and the official host allowlist on REST request and dial paths.

The existing allowlist is not expanded. Certificate verification and TLS minimums are unchanged. Automated guard checks remain unchanged and continue to run before request construction, transport, or dial.

### Sanitized Configuration and Logs

The effective config summary gains a separate private WebSocket role field. Both WebSocket fields remain allowlisted, role-labelled, and reduced to a sanitized origin; URL userinfo, path details not explicitly approved for output, query, and fragment are omitted. Production gate evidence and credentials remain excluded.

No component logs raw endpoint input. Startup logs may state endpoint roles and sanitized origins, but not full URLs, request headers, signatures, credential values, environment contents, or validation payloads. Secret-shaped tests cover REST, public URL, private URL, guard errors, and startup summary output.

### Deployment Artifacts

Because the configuration surface changes, `deploy/config.example.yaml` must add:

```yaml
private_websocket_url: "wss://ws.okx.com:8443/ws/v5/private"
```

The existing public field remains `/ws/v5/public`, and `trading_enabled` remains `false`. `deploy/PRODUCTION_RUNBOOK_SINGAPORE.md` adds a human verification item for both role-specific endpoints. `deploy/README.md` needs no change unless implementation review finds it documents endpoint fields. No deployment command is run by this work.

## Error Handling

| Failure | Required outcome |
|---|---|
| Invalid production profile | Return initialization error before client construction or I/O |
| Production guard cannot be constructed | Fail closed; do not substitute automated or nil policy |
| REST endpoint has wrong scheme/host/base | Fail before request construction or dial |
| Public/private endpoint is swapped or malformed | Fail before either WebSocket dial |
| Automated endpoint is non-loopback | Existing automated guard blocks before request/dial; test fails |
| Raw URL contains userinfo/query/fragment | Production validation rejects; output does not reproduce the value |
| Constructor receives nil explicit guard | Existing automated guard fallback remains active |
| Sanitized summary cannot be built | Return/log only a generic sanitized error; never log raw config |

## Correctness Properties

### Property 1: Fix Checking — Production Network Composition Is Explicit and Role-Safe

_For every_ `StartupNetworkCompositionInput X` where `isBugCondition(X)` is true, fixed composition must satisfy all of the following without network I/O:

- a valid production REST composition selects a successfully validated `ProductionNetworkGuard` and passes it to the guarded API-client constructor;
- the REST base is exactly `https://www.okx.com`, and pure guard checks reject every non-TLS or non-allowlisted host variant;
- the public and private clients receive independently resolved values;
- blank production values resolve to the exact `/ws/v5/public` and `/ws/v5/private` defaults;
- configured production role swaps, malformed or secret-bearing URLs, alternate paths, and unapproved targets fail before construction/dial;
- independently generated loopback public/private values remain distinguishable in automated composition;
- output and failures contain no generated secret value; and
- `trading_enabled` remains false in the production example.

**Validates: Requirements 2.1, 2.2, 2.3, 2.4**

### Property 2: Preservation Checking — Default Validation and Existing Runtime Semantics Remain Closed

_For every_ input where `isBugCondition(X)` is false, and for all established non-network behavior exercised by the completed stabilization suite, fixed behavior equals the baseline except for the new private endpoint field and sanitized role label:

- `NewAPIClient` and nil-guard construction remain loopback-only and reject production credential environments/targets before I/O;
- `ProductionNetworkGuard` retains its TLS and official-host allowlist;
- credential loading, signing, authentication payload generation, public parsing, private message handling, reconciliation, fill callbacks, trading gates, and strategies retain their existing semantics;
- the approved production profile remains reconcile-only and otherwise unchanged; and
- every automated test remains synthetic, temporary, loopback-only, and secret-safe.

**Validates: Requirements 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7**

## Testing Strategy

### Bug-Condition Regression Tests

| Test | Method | Required assertion |
|---|---|---|
| Production REST guard selection | Valid production config plus synthetic populated credential environment names; recording constructor or inspectable selection seam | guarded constructor receives `*config.ProductionNetworkGuard`; zero request/dial calls |
| REST allowlist property | Generate schemes, official/lookalike hosts, ports, userinfo, metadata, AWS, loopback, and public IPs | only approved TLS OKX hosts pass guard validation; no resolver/dial invoked |
| Production endpoint defaults | Pure resolver with blank endpoint fields | exact official REST, public, and private values; public != private |
| WebSocket role regression | Capture both configs for `config.example.yaml` | public gets `/public`; private gets `/private` |
| Local 60008 counterexample | Role-aware loopback WebSocket fixture | old shared wiring receives synthetic 60008; fixed wiring authenticates/subscribes only on private path |
| Loopback injection property | Generate distinct literal-loopback URLs/paths | each client receives its own exact value; recorder sees no external target |
| Production role validation | Table/property inputs for swaps, wrong paths, schemes, hosts, ports, userinfo, queries, fragments | fail before client constructor/dial |
| Secret-safe output property | Generate secret-shaped credential, URL, query, signature, and evidence strings | none appear in summaries, logs, or errors |

### Preservation Tests

Rerun unchanged coverage from `production-grid-stabilization`, especially:

- `internal/config/network_guard_test.go` automated guard and pre-I/O denial properties;
- `internal/execution/api_client_test.go` signing, TLS, loopback request, and nil/default behavior;
- `internal/config/production_config_test.go` strict production values and sanitized summary;
- `internal/monitor/production_grid_preservation_property_test.go` controlled credential and observability sanitization;
- `cmd/startup_composition_test.go` startup composition, adapted only to provide distinct public/private loopback endpoints;
- existing market-data public/private unit tests and repository-wide property/race tests.

Production URLs are tested only as parsed strings against pure validators. Integration tests connect only to `127.0.0.0/8`, `::1`, or `localhost` fixtures with synthetic credentials.

## Expected File Impact

| Path | Change |
|---|---|
| `cmd/main.go` | Resolve role-labelled endpoints; explicitly create/inject production guard; stop copying public URL into private config |
| `cmd/startup_composition_test.go` | Supply independent loopback public/private endpoints and assert role wiring |
| `cmd/production_network_ws_wiring_*_test.go` | Add focused fix and preservation properties if separate files keep the scope clearer |
| `internal/config/config.go` | Add strict `private_websocket_url` field |
| `internal/config/production_config.go` | Validate role-specific production endpoints and add sanitized private role summary |
| `internal/config/network_guard.go` | Reuse existing policy; add only narrowly scoped endpoint constants/helpers if needed |
| `internal/config/*_test.go` | Add endpoint-role, default, strict decode, allowlist, and sanitization coverage |
| `internal/marketdata/ws_client.go`, `ws_private.go` | Preserve protocols/defaults; only centralize constants or remove raw URL logging if required |
| `deploy/config.example.yaml` | Add approved private endpoint; retain public endpoint and `trading_enabled=false` |
| `deploy/PRODUCTION_RUNBOOK_SINGAPORE.md` | Add manual verification of both endpoints |

No credential file, environment value, production host, deployment service, or order is modified or contacted during implementation or validation.

## Requirements Traceability

| Requirement | Design mechanism | Verification |
|---|---|---|
| 2.1 | Explicit production branch and guarded API-client constructor | guard-selection and allowlist properties |
| 2.2 | Independent resolver/configs plus exact production role validation | defaults, role swap, local 60008 tests |
| 2.3 | Separate loopback fields and unchanged automated guard | generated loopback/forbidden-target properties |
| 2.4 | Strict field, role-labelled sanitized summary, deployment update | decode, secret-output, artifact tests |
| 3.1-3.3 | Keep default/nil guards and production allowlist unchanged | existing network guard suite plus preservation property |
| 3.4 | No credential-path changes | existing HMAC/credential tests and secret-output property |
| 3.5 | Composition-only change | existing public/private and stabilization regression suites |
| 3.6 | Version-controlled reconcile-only profile | production config/artifact tests |
| 3.7 | Pure validators and loopback-only integration | network recorder and full-suite validation |
