# Design: Perpetual Swap Adaptive Market Making

## Overview

This design replaces the spot execution path with an OKX perpetual swap (BTC-USDT-SWAP) path that supports bidirectional trading and algorithmic leverage (1x-3x). Three new pure/testable algorithm modules drive decisions: a Direction Signal, a Dynamic Leverage controller, and a Swap Position Lifecycle manager. The existing rebalance loop, inventory tracker, momentum filter, and fill handler are adapted rather than rewritten where possible.

Non-goals: no cross-margin, no multi-symbol swap, no options, no leverage above 3x, no spot fallback.

## Architecture

```
                 ┌─────────────────────────┐
   30s tick ───▶ │  SwapRebalanceLoop      │
                 │  (replaces inventory     │
                 │   rebalance loop)        │
                 └───────────┬─────────────┘
                             │
        ┌────────────────────┼────────────────────┐
        ▼                    ▼                    ▼
 ┌─────────────┐    ┌────────────────┐   ┌──────────────────┐
 │DirectionSig │    │DynamicLeverage │   │SwapPositionMgr   │
 │ LONG/SHORT/ │    │  1x .. 3x      │   │ open/close/decay │
 │  FLAT       │    │                │   │  stop/funding    │
 └─────────────┘    └────────────────┘   └──────────────────┘
        │                    │                    │
        └────────────────────┴────────────────────┘
                             ▼
                 ┌─────────────────────────┐
                 │  SwapGateway (OKX)      │
                 │  set-leverage / order   │
                 │  contracts, reduceOnly  │
                 └─────────────────────────┘
```

## Components

### 1. DirectionSignal (`internal/strategy/direction_signal.go`)

Pure module. Input: recent price series (from a ring buffer). Output: enum {LONG, SHORT, FLAT} plus a confidence score in [0,1].

Algorithm (EMA crossover + slope confirmation):
- Maintain short EMA (e.g. 5 samples) and long EMA (e.g. 20 samples).
- LONG when shortEMA > longEMA AND slope of shortEMA > 0 for the last K samples.
- SHORT when shortEMA < longEMA AND slope < 0 for the last K samples.
- FLAT otherwise (crossover region / flat slope).
- Confidence = normalized absolute EMA gap / recent ATR, clamped to [0,1].
- Warmup: return FLAT until at least `longWindow` samples exist.

```go
type Direction int
const ( Flat Direction = iota; Long; Short )

type DirectionSignal struct { /* ring buffer, ema state */ }
func (d *DirectionSignal) Update(price decimal.Decimal)
func (d *DirectionSignal) Evaluate() (Direction, float64) // dir, confidence
```

### 2. DynamicLeverage (`internal/strategy/dynamic_leverage.go`)

Pure function. Inputs: recent realized volatility (stddev of returns), direction confidence. Output: leverage in [1.0, 3.0].

```
leverage = clamp( base + confidence*confGain - volatility*volPenalty, 1.0, 3.0 )
```
- base = 1.0
- High volatility drives leverage down; high confidence drives it up.
- Insufficient/invalid data → return 1.0.
- Never returns > 3.0 (hard clamp, asserted by property test).

```go
func ComputeLeverage(realizedVol, confidence float64) float64
```

### 3. SwapPositionManager (extends inventory_tracker concepts)

Tracks a single open swap position with side (long/short), entry price, contracts, entry time, and the leverage used. Provides:
- `OpenTarget(dir Direction, bestBid, bestAsk, tick)` → entry limit price (bestBid-tick for long, bestAsk+tick for short).
- `CloseTarget(elapsed)` → reduce-only close price with time decay.
  - Long close = entry × (1 + margin); Short close = entry × (1 - margin).
  - margin schedule: 0-1h full spread; 1-6h ×0.8; 6-12h ×0.67; floor ≥ 0.05% (> 0.04% round-trip fee).
  - Underwater (mark worse than entry): tighten to floor.
- `ShouldHardStop(mark, stopPct)` → true if unrealized loss > stopPct of margin.
- `ShouldForceClose(elapsed, maxHold)`.

### 4. SwapGateway (`internal/execution/swap_gateway.go`)

Adapts order placement to swap semantics:
- Set leverage: `POST /api/v5/account/set-leverage` with instId, lever, mgnMode=isolated, before first order.
- Contract sizing: `contracts = floor(notionalUSDT / (contractFaceValue × price))` where contractFaceValue=0.01 BTC. Reject if contracts < 1.
- Order params: instId=BTC-USDT-SWAP, tdMode=isolated, ordType=post_only, side (buy/sell), posSide (long/short), reduceOnly for closes, clOrdId alphanumeric.
- Parse sCode/sMsg on rejection.

### Liquidation Safety

For isolated margin at leverage L, approximate liquidation distance ≈ (1/L) minus maintenance margin. Require liquidation distance > hard stop (1.5%). Since 3x → ~33% liq distance ≫ 1.5% stop, the stop always triggers far before liquidation. The set-leverage step verifies L such that (1/L) > stopPct + maintenanceBuffer; else reduce L.

## Funding Awareness

OKX funding every 8h. Before a funding timestamp, if position unrealized PnL < expected funding cost, optionally close early (config flag `avoid_funding`). Backtest models funding as periodic cost.

## Data Models

```go
type SwapConfig struct {
    Instrument   string          // "BTC-USDT-SWAP"
    TdMode       string          // "isolated"
    MaxLeverage  float64         // <= 3.0
    Spread       decimal.Decimal // e.g. 0.003
    StopLoss     decimal.Decimal // e.g. 0.015
    MaxHoldHours int
    NotionalUSDT decimal.Decimal // per-trade notional
    AvoidFunding bool
}

type SwapPosition struct {
    Side      Direction
    Entry     decimal.Decimal
    Contracts int
    Leverage  float64
    OpenedAt  time.Time
}
```

## Execution Flow (per 30s tick)

1. Update DirectionSignal with latest price.
2. If holding a position → run CloseTarget / hard stop / force close / funding checks; adjust reduce-only close order.
3. If flat → evaluate DirectionSignal. If FLAT, do nothing. If LONG/SHORT with confidence above threshold:
   a. Compute realized volatility, call ComputeLeverage.
   b. Verify liquidation distance; set leverage on OKX.
   c. Compute contracts from notional; verify margin; place Post-Only open order (bid-tick for long, ask+tick for short).
4. On fill (private WS) → SwapPositionManager records position, places reduce-only close.

## Error Handling

| Failure | Outcome |
|---|---|
| set-leverage rejected | Do not open; log sCode/sMsg; retry next tick |
| contracts < 1 | Skip order; log |
| insufficient margin | Reduce contracts or skip; log |
| order rejected | Log full sCode/sMsg; no crash |
| liq distance < stop | Reduce leverage until safe or skip |

## Testing Strategy

- **Unit/property:** DirectionSignal determinism; ComputeLeverage ∈ [1,3] for all inputs (property test); CloseTarget margins always > fee floor; contract sizing floor/reject.
- **Integration (loopback):** full open→fill→close cycle for both long and short against a fake swap server; no real network.
- **Backtest:** long+short, dynamic leverage, funding, 0.02% fee, on BTC-USDT-SWAP history; assert zero liquidation events for chosen params.
- All tests use synthetic credentials and loopback; never touch production OKX.

## Rollout

1. Implement modules + swap gateway behind config.
2. Backtest to pick spread/stop/leverage params (start from 0.3% / 1.5% / dynamic).
3. Deploy with trading_enabled=false (reconcile-only) to verify wiring.
4. Enable with smallest notional; scale only after observing correct long+short cycles.

## File Impact

| Path | Change |
|---|---|
| internal/strategy/direction_signal.go | NEW |
| internal/strategy/dynamic_leverage.go | NEW |
| internal/execution/swap_gateway.go | NEW |
| internal/execution/swap_position.go | NEW (or extend inventory_tracker) |
| cmd/main.go | swap rebalance loop replaces spot loop |
| internal/config/*.go | swap config + validation, drop cash requirement |
| cmd/backtest/main.go | long+short + leverage + funding model |
| deploy/config.example.yaml | swap profile |
