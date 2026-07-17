# Implementation Plan: Grid Drift

## Overview

Implement automatic grid range relocation (drift) for the grid trading strategy. The GridDriftEngine monitors price proximity to grid boundaries and shifts the entire range — cancelling stale orders and placing new ones — to keep the grid centered around the market price.

## Tasks

- [x] 1. Add DriftConfig model and extend GridConfig
  - [x] 1.1 Add DriftConfig struct and Drift field to GridConfig in `pkg/models/grid.go`
    - Add `DriftConfig` struct with fields: `Enabled`, `DriftThreshold`, `DriftStep`, `CooldownPeriod`, `MaxDrifts`
    - Add `DriftDirection` type and constants (`DriftNone`, `DriftUp`, `DriftDown`)
    - Add `DriftEvent` struct for audit logging
    - Add `Drift *DriftConfig` field to `GridConfig`
    - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5_

  - [x] 1.2 Add DriftConfig validation in `internal/config/config.go`
    - Add `ValidateDriftConfig` function enforcing: DriftThreshold in [0.01, 0.50], DriftStep > 0, CooldownPeriod >= 5s, MaxDrifts >= 0
    - Call `ValidateDriftConfig` from `ValidateGridConfig` when `cfg.Drift != nil`
    - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5_

- [x] 2. Implement GridDriftEngine
  - [x] 2.1 Create `internal/strategy/grid_drift.go` with the GridDriftEngine struct and constructor
    - Define `GridDriftEngine` struct with fields: config, driftConfig, state, execEngine, logger, metrics, alerter, lastDriftTime, driftCount, gridSpacing, mu
    - Implement `NewGridDriftEngine(...)` constructor that caches grid spacing and initializes state
    - Implement `Reset()` and `DriftCount()` methods
    - _Requirements: 1.5, 6.5_

  - [x] 2.2 Implement drift trigger detection in `internal/strategy/grid_drift.go`
    - Implement `shouldTriggerDrift(price)` returning `(DriftDirection, bool)` — checks upper/lower boundary zone and price-exceeds-boundary conditions
    - Implement `isCooldownActive()` — compares `time.Since(lastDriftTime)` against `CooldownPeriod`
    - Implement `isEmergencyOverride(price, direction)` — checks if price exceeds boundary by > 2× DriftStep × spacing
    - _Requirements: 1.1, 1.2, 1.3, 1.4, 5.1, 5.4_

  - [x] 2.3 Implement range computation in `internal/strategy/grid_drift.go`
    - Implement `computeNewRange(direction)` returning `(newLower, newUpper)`
    - Shift = DriftStep × gridSpacing; add for DriftUp, subtract for DriftDown
    - Clamp newLower to minimum tick size if ≤ 0 (requirement 2.7)
    - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.6, 2.7_

  - [x] 2.4 Implement order cancellation logic in `internal/strategy/grid_drift.go`
    - Implement `cancelStaleOrders(newLevels)` — identifies levels no longer in range, cancels via execEngine with up to 3 retries per order
    - Return count of cancelled and any error; abort if zero cancelled
    - _Requirements: 3.1, 3.2, 3.5, 8.1, 8.2_

  - [x] 2.5 Implement order placement logic in `internal/strategy/grid_drift.go`
    - Implement `placeNewOrders(newLevels, currentPrice)` — places BUY below price and SELL above price at newly created levels with up to 3 retries
    - Return count of placed and any error; partial placement is acceptable
    - _Requirements: 3.3, 3.4, 3.6, 8.3_

  - [x] 2.6 Implement the full drift execution orchestrator in `internal/strategy/grid_drift.go`
    - Implement `executeDrift(direction, currentPrice, emergency)` — cancel stale → update config boundaries → recalculate levels via `CalculateGridLevels` → update state.Levels → place new orders
    - Record lastDriftTime, increment driftCount
    - Emit DriftEvent log, emit metrics, send alert if MaxDrifts reached
    - _Requirements: 4.1, 4.2, 4.3, 4.4, 7.1, 7.2, 7.3, 8.1, 8.4, 8.5_

  - [x] 2.7 Implement `OnPriceUpdate(currentPrice)` as the public entry point
    - Acquire mutex, check enabled, check MaxDrifts, call shouldTriggerDrift, check cooldown (allow emergency override), call executeDrift
    - Log cooldown suppression when applicable
    - _Requirements: 1.5, 5.1, 5.2, 5.3, 6.6, 7.5_

- [x] 3. Checkpoint
  - Ensure all tests pass, ask the user if questions arise.

- [x] 4. Integrate with Scheduler and add metrics
  - [x] 4.1 Add `DriftEngine *GridDriftEngine` field to `StrategyInstance` in `internal/strategy/scheduler.go`
    - Initialize DriftEngine in `LoadStrategy` when `config.Grid.Drift != nil && config.Grid.Drift.Enabled`
    - _Requirements: 6.5_

  - [x] 4.2 Call `DriftEngine.OnPriceUpdate` from `processGridUpdate` in `internal/strategy/scheduler.go`
    - Insert call before existing grid signal logic: `if instance.DriftEngine != nil { instance.DriftEngine.OnPriceUpdate(tick.LastPrice) }`
    - _Requirements: 1.5_

  - [x] 4.3 Register drift Prometheus metrics in `internal/monitor/metrics.go`
    - Add `grid_drift_total` (Counter), `grid_drift_latency_ms` (Histogram), `grid_drift_failure_total` (Counter), `grid_drift_suppressed_total` (Counter)
    - Add helper methods: `IncrementDriftCount()`, `RecordDriftLatency(ms)`, `IncrementDriftFailure()`, `IncrementDriftSuppressed()`
    - Register all new metrics in `NewMetricsServer`
    - _Requirements: 7.4_

- [x] 5. Write unit tests
  - [x] 5.1 Create `internal/strategy/grid_drift_test.go` with unit tests for GridDriftEngine
    - Test `shouldTriggerDrift`: price in upper zone → DriftUp, price in lower zone → DriftDown, price in middle → DriftNone, price beyond boundary → immediate drift
    - Test `computeNewRange`: verify width preservation, verify DriftUp shifts up, DriftDown shifts down, verify LowerPrice clamping near zero
    - Test `isCooldownActive` and `isEmergencyOverride`
    - Test full `OnPriceUpdate` flow with mocked execEngine: verify cancel→place sequence, verify position preservation, verify MaxDrifts stops drifting
    - Test abort on total cancellation failure (state unchanged)
    - Test partial placement success (no abort)
    - _Requirements: 1.1–1.4, 2.1–2.7, 3.1–3.6, 4.1–4.4, 5.1–5.4, 8.1–8.5_

  - [x] 5.2 Create `internal/config/config_drift_test.go` with unit tests for ValidateDriftConfig
    - Test valid configs pass, threshold out of range fails, DriftStep=0 fails, CooldownPeriod<5s fails, MaxDrifts<0 fails
    - _Requirements: 6.1, 6.2, 6.3, 6.4_

- [x] 6. Update deploy configuration example
  - [x] 6.1 Add drift config section to `deploy/env.example` or create a `deploy/config.example.yaml` showing DriftConfig fields with comments
    - Show example: `drift: { enabled: true, drift_threshold: 0.10, drift_step: 2, cooldown_period: 30s, max_drifts: 50 }`
    - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.5_

- [x] 7. Final checkpoint
  - Ensure all tests pass, ask the user if questions arise.

## Notes

- The design uses Go — the same language as the existing codebase. No language selection needed.
- Position, AvgEntryPrice, RealizedPnL, TotalBuys, and TotalSells are never touched during drift execution.
- The engine reuses `CalculateGridLevels` for level recalculation, preserving arithmetic/geometric consistency.
- Cancel-first ordering prevents exceeding exchange order limits.
- Partial placement failure is acceptable per requirement 8.3.

## Task Dependency Graph

```json
{
  "waves": [
    { "id": 0, "tasks": ["1.1"] },
    { "id": 1, "tasks": ["1.2", "2.1"] },
    { "id": 2, "tasks": ["2.2", "2.3"] },
    { "id": 3, "tasks": ["2.4", "2.5"] },
    { "id": 4, "tasks": ["2.6"] },
    { "id": 5, "tasks": ["2.7", "4.3"] },
    { "id": 6, "tasks": ["4.1", "4.2"] },
    { "id": 7, "tasks": ["5.1", "5.2", "6.1"] }
  ]
}
```
