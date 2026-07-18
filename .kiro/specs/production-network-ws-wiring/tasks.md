# Implementation Plan: Production Network and WebSocket Wiring

## Execution Rules

Execute this bugfix as **Explore → Preserve → Implement → Validate** against the approved `requirements.md` and `design.md` in this directory.

- Do not connect to production OKX, EC2, AWS, metadata services, or real accounts. Production URLs are data for pure parsing/validation tests only.
- Use synthetic credentials, literal loopback servers, recording constructors/dialers, and temporary state. Never print or persist a credential, signature, authorization header, environment dump, or production gate evidence value.
- Keep `trading_enabled=false`; do not add an environment-variable bypass or broaden an endpoint allowlist.
- Do not modify REST signing, Private_WS authentication semantics, strategy behavior, credentials, orders, or the broader completed `production-grid-stabilization` implementation beyond the endpoint-composition seam.
- No deployment, `systemctl`, production request, or real order action is part of any task.

## Tasks

- [x] 1. Reproduce both composition defects on the unfixed baseline
  - Extract only the minimum behavior-neutral, dependency-injected observation seams needed to inspect REST constructor selection and resolved client configs without opening sockets. The seam must initially preserve the current defective choices.
  - Add `Property 1: Fix Checking` tests for valid production composition with synthetic populated production credential environment names. Assert that the current REST path selects the default automated guard rather than an explicit `ProductionNetworkGuard`, while constructor, DNS, dial, and request recorders remain at zero.
  - Add a public/private WebSocket composition test showing that `websocket_url=/ws/v5/public` is currently copied into both client configs.
  - Add a role-aware loopback WebSocket counterexample: the public path returns a synthetic OKX `60008` for an `orders` subscription, while the private path accepts login and subscription. Do not contact OKX.
  - Add a generated independent-loopback property showing the current composition cannot preserve distinct public/private URL inputs.
  - Record the exact sanitized counterexamples and expected failures. If all bug-condition tests unexpectedly pass on the unfixed baseline, stop and review whether the bug was already fixed or the seam is invalid.
  - **Target tests:** `go test ./cmd -run '^TestProperty1_BugCondition_(ProductionRESTGuardSelection|WebSocketRoleSeparation|IndependentLoopbackInjection|Local60008Counterexample)$' -count=1`
  - **Completion:** Both confirmed defects are reproduced with zero external network activity and no secret value in output.
  - _Depends on: none_
  - _Requirements: 1.1, 1.2, 1.3, 1.4_

- [x] 2. Establish preservation baselines on the unfixed code
  - Add or retain `Property 2: Preservation Checking` coverage proving `execution.NewAPIClient` remains backed by `DefaultAutomatedValidationGuard`, nil explicit guards fail closed, loopback requests are allowed, and production OKX/AWS/metadata/non-loopback targets plus populated production credential environments are rejected before I/O.
  - Reuse the existing generated `ProductionNetworkGuard` allowlist tests and add host/scheme variants only where coverage is missing; never execute an accepted production target.
  - Preserve existing synthetic credential loading/HMAC behavior, TLS verification, public parser behavior, private message handling, and structured sanitization.
  - Capture the production profile baseline, including `trading_enabled=false`, Singapore identity, `cash`, approved timing/range/state values, and empty mean reversion.
  - **Target tests:** `go test ./internal/config ./internal/execution ./internal/marketdata ./internal/monitor ./cmd -run '^(TestProperty2_Preservation_|TestAutomatedValidationGuard|TestForbiddenEndpoint|TestNewAPIClient_TLSConfiguration|TestSignRequest_|TestStrictProductionConfig|TestEffectiveConfigSanitized)' -count=1`
  - **Completion:** All preservation tests pass before the fix, all network recorders show loopback or zero I/O, and no comparator ignores endpoint role, guard type, security, or secret leakage.
  - _Depends on: 1 (expected failure documented)_
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7_

- [x] 3. Implement the narrow composition fix

  - [x] 3.1 Add role-specific endpoint configuration, resolution, and validation
    - Add `private_websocket_url` to strict `SystemConfig`; retain `websocket_url` as the public field.
    - Implement a pure role-labelled resolver for REST, public WebSocket, and private WebSocket values. Blank production values resolve to `https://www.okx.com`, `wss://ws.okx.com:8443/ws/v5/public`, and `wss://ws.okx.com:8443/ws/v5/private` respectively; the public field must never be the private fallback.
    - Extend production validation to reject wrong schemes, hosts, ports, paths, userinfo, queries, fragments, malformed URLs, role swaps, and equal public/private endpoints before construction or dial. Keep the existing TLS official-host allowlist unchanged.
    - Extend the allowlisted effective config summary with separate public/private role fields using sanitized origins only. Ensure errors and logs never reproduce raw URL inputs or secret-shaped values.
    - Add table and property tests for defaults, exact role mapping, generated invalid URLs, strict unknown-field decoding, and secret-safe summaries.
    - **Target tests:** `go test ./internal/config ./cmd -run 'Test(ResolveNetworkEndpoints|ProductionEndpointRoles|ProductionEndpointProperty|StrictPrivateWebSocketField|EffectiveConfigEndpointRolesSanitized)' -count=1`
    - **Completion:** Endpoint resolution is pure and deterministic; valid production values are exact and distinct; every invalid production role fails before any recorded constructor/dial call.
    - _Depends on: 2_
    - _Requirements: 2.2, 2.3, 2.4, 3.3, 3.6, 3.7_

  - [x] 3.2 Explicitly inject the production REST guard and wire WebSocket roles independently
    - In the production branch, call `config.NewProductionNetworkGuard(cfg)`, propagate validation errors, validate the resolved REST base, and pass the returned guard to `execution.NewAPIClientWithEndpointGuard`.
    - In every non-production/default branch, continue using `execution.NewAPIClient`; do not introduce a mode switch, global override, nil policy, or permissive fallback.
    - Build public and private client configs from their own defaults and assign only `resolved.PublicWebSocketURL` and `resolved.PrivateWebSocketURL`. Remove the assignment that copies `cfg.WebSocketURL` into private configuration.
    - Keep all existing heartbeat, authentication, subscription, parser, callback, trading-gate, reconciliation, and strategy wiring unchanged.
    - Make startup-composition fixtures provide explicit, independently recorded loopback public/private endpoints. Assert no risk/order behavior is enabled merely by this wiring fix and retain `trading_enabled=false` in production fixtures.
    - **Target tests:** `go test ./cmd ./internal/config ./internal/execution ./internal/marketdata -run 'Test(ProductionRESTGuardInjection|ProductionRESTGuardRejectsBeforeIO|PublicPrivateWebSocketComposition|IndependentLoopbackWebSockets|Local60008Counterexample|StartupComposition)' -count=1`
    - **Completion:** Recording seams observe an explicit `ProductionNetworkGuard` only for production; public/private configs receive the correct distinct endpoints; all integration traffic is loopback.
    - _Depends on: 3.1_
    - _Requirements: 2.1, 2.2, 2.3, 3.1, 3.2, 3.3, 3.5, 3.7_

  - [x] 3.3 Update only deployment artifacts required by the new field
    - Add `private_websocket_url: "wss://ws.okx.com:8443/ws/v5/private"` to `deploy/config.example.yaml`; keep the existing public `/ws/v5/public`, REST URL, and `trading_enabled=false` unchanged.
    - Add public/private endpoint verification items to `deploy/PRODUCTION_RUNBOOK_SINGAPORE.md`. Do not add or run deployment commands, credentials, production probes, or approval shortcuts.
    - Update `deploy/README.md` only if implementation review finds an existing endpoint-field description that would otherwise be incorrect.
    - Add static artifact tests proving the example loads strictly, both role endpoints are exact, no bypass field exists, no secret value is present, and the staged profile remains reconcile-only.
    - **Target tests:** `go test ./internal/config ./internal/integration -run 'Test(ProductionEndpointArtifacts|RunbookEndpointRoles|NoSecretArtifacts|HumanApprovalRequired|NoSedDependency)' -count=1`
    - **Completion:** Version-controlled artifacts represent both endpoint roles without altering any production gate or exposing sensitive data.
    - _Depends on: 3.2_
    - _Requirements: 2.4, 3.4, 3.6, 3.7_

- [x] 4. Validate the fix and preservation properties

  - [x] 4.1 Rerun focused fix and preservation suites
    - Rerun the exact Property 1 tests from Task 1; they must now pass without weakening assertions or replacing the local `60008` oracle.
    - Rerun the exact Property 2 tests from Task 2; they must remain green.
    - Run targeted config, execution, market-data, startup composition, sanitization, and deployment-artifact tests with fresh counts.
    - Verify every recorder reports zero production DNS/dial/request activity and all observed WebSocket/HTTP traffic is loopback.
    - **Target tests:** `go test ./cmd ./internal/config ./internal/execution ./internal/marketdata ./internal/monitor ./internal/integration -run '^(TestProperty1_BugCondition_|TestProperty2_Preservation_|TestProductionRESTGuard|TestPublicPrivateWebSocket|TestProductionEndpoint|TestEffectiveConfig)' -count=1`
    - **Completion:** Property 1 passes after the fix, Property 2 still passes, synthetic `60008` is avoided by correct private routing, and no secret or external I/O is observed.
    - _Depends on: 3.3_
    - _Requirements: 2.1, 2.2, 2.3, 2.4, 3.1-3.7_

  - [x] 4.2 Run repository-wide validation and produce a sanitized handoff
    - Run formatting checks for changed Go files, then repository-wide tests and the race detector. Fix failures rather than relaxing endpoint/security assertions.
    - Review the final diff to confirm there is no credential change, bypass environment variable, widened allowlist, raw URL logging, production I/O, real-order code path, unrelated stabilization change, or `trading_enabled=true` artifact.
    - Record only sanitized test summaries and local counterexample resolution; do not include environment values or full authenticated payloads.
    - **Target tests:** `go test ./... -count=1`; `go test -race ./... -count=1`
    - **Completion:** Full and race suites pass, the diff remains composition-only, and the implementation is ready for human review but not production execution.
    - _Depends on: 4.1_
    - _Requirements: 2.1-2.4, 3.1-3.7_

## Dependency Order

```text
1 → 2 → 3.1 → 3.2 → 3.3 → 4.1 → 4.2
```

Only Task 1 is initially ready. No implementation task begins until the unfixed counterexamples and preservation baseline are recorded.
