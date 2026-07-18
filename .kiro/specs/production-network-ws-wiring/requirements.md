# Production Network and WebSocket Wiring Bugfix Requirements

## Introduction

This bugfix closes two production composition regressions found on the Singapore EC2 deployment after completion of `.kiro/specs/production-grid-stabilization`. That completed spec remains the behavioral and security baseline; this spec changes only REST network-policy selection, public/private WebSocket endpoint separation, their test seams, and the minimum deployment documentation needed to represent those endpoints.

The production profile remains reconcile-only with `trading_enabled=false`. This work must not modify or expose credentials, introduce a bypass environment variable, contact production OKX or EC2 from automated tests, place or cancel real orders, or weaken any deny-by-default validation established by `production-grid-stabilization`.

The existing `websocket_url` field denotes the public market-data endpoint. A separate private endpoint setting is required for authenticated order updates. Production defaults are `wss://ws.okx.com:8443/ws/v5/public` and `wss://ws.okx.com:8443/ws/v5/private`; the REST default is `https://www.okx.com`.

**Bug Condition C(X):**

```text
FUNCTION isBugCondition(X)
  INPUT: X of type StartupNetworkCompositionInput
  OUTPUT: boolean

  RETURN (X.isValidProductionProfile
          AND (X.component = REST_EXECUTION
               OR X.component = PUBLIC_PRIVATE_WEBSOCKETS))
      OR (X.isAutomatedValidation
          AND X.requiresIndependentPublicAndPrivateLoopbackEndpoints)
END FUNCTION
```

**Fix and preservation properties:**

```text
FUNCTION expectedBehavior(result)
  INPUT: result of type StartupNetworkCompositionResult
  OUTPUT: boolean

  RETURN result.productionRESTUsesExplicitValidatedProductionNetworkGuard
     AND result.productionRESTBaseIsOfficialTLSOKX
     AND result.publicAndPrivateWebSocketRolesAreIndependent
     AND result.productionPublicEndpoint = "wss://ws.okx.com:8443/ws/v5/public"
     AND result.productionPrivateEndpoint = "wss://ws.okx.com:8443/ws/v5/private"
     AND result.automatedValidationRemainsDenyByDefault
     AND result.testsUseLoopbackOnly
     AND result.outputsContainNoSecrets
     AND result.tradingEnabled = false
END FUNCTION

FOR ALL X WHERE isBugCondition(X) DO
  ASSERT expectedBehavior(FIXED(X))
END FOR

FOR ALL X WHERE NOT isBugCondition(X) DO
  ASSERT ORIGINAL(X) = FIXED(X)
END FOR
```

The equality in preservation checking covers externally observable behavior and permits only the new role-specific endpoint fields and sanitized role labels required by this spec.

## Bug Analysis

### Current Behavior (Defect)

1.1 WHEN a valid production profile uses the official OKX REST base and the process contains the required populated `OKX_API_KEY`, `OKX_SECRET_KEY`, and `OKX_PASSPHRASE` variables THEN `cmd/main.go` constructs the execution client with `execution.NewAPIClient`, which installs `DefaultAutomatedValidationGuard` and rejects the production credentials and official host before the intended REST call.

1.2 WHEN the production `websocket_url` is `wss://ws.okx.com:8443/ws/v5/public` THEN `cmd/main.go` assigns that URL to both the public `WSClient` and `DefaultPrivateWSClientConfig`, so Private_WS can authenticate but the `orders` subscription is sent to the public endpoint and fails with OKX code `60008`.

1.3 WHEN a unit, replay, or startup-composition test needs separate loopback behavior for public market data and authenticated private order events THEN the single composition-level WebSocket override forces both clients to receive the same URL, preventing a faithful assertion that each role was wired independently.

1.4 WHEN production startup validation or the effective configuration summary is used to review endpoint wiring THEN the configuration models only one WebSocket URL, so a public/private role mismatch is not rejected explicitly and the two effective roles cannot be audited independently.

### Expected Behavior (Correct)

2.1 WHEN a valid production profile is composed THEN the system SHALL construct `config.NewProductionNetworkGuard(cfg)` and inject the returned guard into `execution.NewAPIClientWithEndpointGuard`; construction SHALL fail closed if production configuration or endpoint validation fails, and the selected REST base SHALL be `https://www.okx.com`. The production guard SHALL allow only approved official OKX hosts over TLS and SHALL reject plaintext, userinfo, lookalike, arbitrary public, loopback, metadata, and unapproved hosts before DNS, dial, or request execution.

2.2 WHEN public and private WebSocket clients are composed for production THEN the system SHALL resolve and wire their endpoints independently: the public client SHALL receive `wss://ws.okx.com:8443/ws/v5/public`, and the private client SHALL receive `wss://ws.okx.com:8443/ws/v5/private`. A configured public URL SHALL never overwrite the private URL. Wrong schemes, hosts, ports, paths, userinfo, queries, fragments, or a public/private role swap SHALL fail before either WebSocket dials; the private `orders` subscription SHALL therefore never be sent to the public endpoint.

2.3 WHEN unit, replay, simulated, or startup-composition validation is executed THEN the system SHALL support independently supplied loopback REST, public WebSocket, and private WebSocket endpoints while retaining `execution.NewAPIClient` and `DefaultAutomatedValidationGuard` as the default deny-by-default path. Tests SHALL prove all observed DNS/dial/request targets are loopback and SHALL not contact production OKX, EC2, metadata services, or real accounts.

2.4 WHEN configuration is decoded, validated, summarized, or logged THEN the system SHALL distinguish the public and private WebSocket roles with strictly known fields and sanitized role-labelled output. Validation and output SHALL never include credential values, signatures, authorization headers, URL userinfo, query values, fragments, environment dumps, or production gate evidence values; raw endpoint URLs SHALL not be logged. The version-controlled production example and runbook SHALL name both endpoints if the new field is required, while retaining `trading_enabled=false`.

### Unchanged Behavior (Regression Prevention)

3.1 WHEN `execution.NewAPIClient` is called by an existing unit, replay, simulator, or other default caller THEN the system SHALL CONTINUE TO install `DefaultAutomatedValidationGuard`, allow loopback only, reject populated production credential environment variables, and block production OKX, AWS, metadata, and non-loopback targets before I/O.

3.2 WHEN `execution.NewAPIClientWithEndpointGuard` receives a nil guard THEN the system SHALL CONTINUE TO fail closed by falling back to the automated-validation guard; no global mode switch or bypass environment variable SHALL be added.

3.3 WHEN `ProductionNetworkGuard` validates a production endpoint THEN the system SHALL CONTINUE TO require a valid production profile, TLS, and the approved official OKX host allowlist; this spec SHALL not broaden that allowlist or disable certificate verification.

3.4 WHEN credentials are loaded or used for REST signing and Private_WS authentication THEN the system SHALL CONTINUE TO use the existing controlled credential source and cryptographic behavior without modifying, rotating, printing, serializing, or embedding credential values in tests or artifacts.

3.5 WHEN public market data or private order messages arrive through correctly assigned endpoints THEN the system SHALL CONTINUE TO use the existing parser, subscriptions, heartbeat/recovery behavior, fill callback, reconciliation, trading gate, and order semantics established by `production-grid-stabilization`; only endpoint resolution and composition are in scope.

3.6 WHEN the production profile is loaded or staged THEN the system SHALL CONTINUE TO use `trading_enabled=false`, Singapore deployment identity, `cash` mode, approved timing/range/state settings, empty mean-reversion configuration, and all existing manual production approval gates.

3.7 WHEN automated regression, unit, property, or integration tests run THEN the system SHALL CONTINUE TO use synthetic credentials, loopback transports, temporary state, and sanitized output, with zero production network or real-order side effects.
