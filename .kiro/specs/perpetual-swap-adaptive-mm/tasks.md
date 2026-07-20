# Implementation Plan: Perpetual Swap Adaptive Market Making

Order of work: build and test pure algorithm modules first, then validate profitability by backtest, then wire the swap execution path, and only last touch production config. No production code path is enabled until the backtest proves zero liquidations and positive expectancy.

- [ ] 1. DirectionSignal module (pure, tested)
  - Create `internal/strategy/direction_signal.go` with EMA-crossover + slope logic returning {Flat, Long, Short} and confidence [0,1].
  - Warmup returns Flat until longWindow samples exist.
  - Unit tests: uptrend→Long, downtrend→Short, chop→Flat, warmup→Flat, determinism.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.6_

- [ ] 2. DynamicLeverage function (pure, property-tested)
  - Create `internal/strategy/dynamic_leverage.go` with `ComputeLeverage(realizedVol, confidence) float64`.
  - Property test: result always in [1.0, 3.0] for ALL generated inputs including NaN/negative/huge; invalid→1.0.
  - Unit tests: high vol lowers leverage, high confidence raises it.
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5_

- [ ] 3. SwapPositionManager (tested)
  - Create `internal/execution/swap_position.go` tracking side/entry/contracts/leverage/openedAt.
  - Implement OpenTarget, CloseTarget (time decay + underwater tighten, floor ≥ 0.05%), ShouldHardStop, ShouldForceClose for BOTH long and short.
  - Unit/property tests: close margin always > 0.04% round-trip fee; long/short symmetry; stop triggers correctly.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 4.7_

- [ ] 4. Swap-aware backtest and parameter validation
  - Extend `cmd/backtest/main.go` to simulate long+short entries via DirectionSignal, dynamic leverage, funding cost, 0.02% fee, and a liquidation check.
  - Run parameter grid; assert chosen params produce ZERO liquidation events and positive P&L.
  - Record the recommended spread/stop and confidence/leverage settings.
  - **Gate:** do not proceed to production wiring unless a viable param set exists.
  - _Requirements: 7.1, 7.2, 7.3_

- [ ] 5. SwapGateway execution layer
  - Create `internal/execution/swap_gateway.go`: set-leverage call, contract sizing (1 contract = 0.01 BTC, floor, reject <1), Post-Only open, reduce-only close, posSide long/short, alphanumeric clOrdId, sCode/sMsg parsing.
  - Liquidation-distance check reduces leverage until (1/L) > stop + maintenance buffer.
  - Integration tests against a loopback fake swap server for long and short open→fill→close; zero real network.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 5.1, 5.4_

- [ ] 6. Swap config and validation
  - Add SwapConfig; validate instrument=BTC-USDT-SWAP, td_mode=isolated, max_leverage≤3, spread≥0.0005, stop_loss>0.
  - Remove the cash-only requirement from production validation; ensure no spot path is reachable in swap composition.
  - Add `deploy/config.example.yaml` swap profile with trading_enabled=false.
  - Tests: valid swap profile loads; leverage>3 rejected; spot instrument rejected.
  - _Requirements: 6.1, 6.2, 6.5_

- [ ] 7. Wire swap rebalance loop into startup
  - Replace the spot inventory-rebalance loop with a SwapRebalanceLoop that: updates DirectionSignal, manages open position (close/decay/stop/force/funding), and on Flat→no action, on Long/Short above confidence→size+lever+open.
  - Load existing swap positions on startup (dust-filtered) so restarts do not orphan positions.
  - Register fill handler to record swap position and place reduce-only close.
  - Integration test: full startup composition with loopback endpoints, trading_enabled=false, asserts no orders placed while flat and correct wiring.
  - _Requirements: 2.5, 4.1, 4.2, 5.2, 5.3, 6.3, 6.4_

- [ ] 8. Full validation and race check
  - `go build ./...`; `go test ./... -count=1`; `go test -race ./... -count=1`.
  - Confirm no spot code path reachable, all leverage ≤ 3x, all tests loopback-only and secret-safe.
  - Produce sanitized summary; keep trading_enabled=false for first deploy.
  - _Requirements: 1-7 (all)_

## Dependency Order

```
1 → 2 → 3 → 4 (gate) → 5 → 6 → 7 → 8
```

Tasks 1-3 are independent pure modules (can be parallel). Task 4 is a hard gate: production wiring (5-7) only begins if backtest proves a safe, profitable parameter set. Task 8 is final validation.
