# Grid SELL Quantity & Tick Parser Fix — Bugfix Design

## Overview

Two narrow production bugs prevent the grid bot from operating on the Singapore deployment:

1. **SELL quantity bug**: In `fill_handler.go`, when a BUY fills in cash mode the counter SELL order uses `cfg.OrderSize` as quantity. OKX deducts maker fee from the received asset, so available balance is `fillSz * (1 - feeRate)`. The SELL is rejected for insufficient balance.

2. **Tick parser bug**: In `parser.go`, the OKX tickers channel push does not include a `seqId` field (Go zero-value 0). The sequence validation rule `tick.SequenceId <= prev` evaluates `0 <= 0` on the very first tick and rejects it, blocking all subsequent ticks. Additionally, the `"last"` JSON tag mapping should be verified against OKX actual format (current mapping appears correct — OKX sends `"last"` at data level for tickers channel).

The fix is minimal: adjust SELL quantity calculation and skip sequence validation when `seqId == 0`.

## Glossary

- **Bug_Condition (C)**: The input condition that triggers incorrect behavior
- **Property (P)**: The correct behavior that must hold after the fix
- **Preservation**: Existing behaviors that must remain unchanged
- **fill_handler.go**: `internal/execution/fill_handler.go` — places counter-orders on fill events
- **parser.go**: `internal/marketdata/parser.go` — parses and validates OKX WebSocket tick data
- **FeeRate**: `GridConfig.FeeRate` — single-side maker fee rate (e.g., 0.0008 = 0.08%)
- **fillSz**: Actual quantity filled on the exchange (reported in fill event)
- **SeqId**: Sequence identifier in OKX push data; present in books channel, absent in tickers channel

## Bug Details

### Bug Condition

**Bug 1 — SELL Quantity**

The bug manifests when a BUY order fills in cash (spot) mode and the fill handler places a counter SELL order. The handler uses `cfg.OrderSize` as SELL quantity, but the account only holds `fillSz * (1 - feeRate)` of the asset because OKX deducted the maker fee in-kind.

**Formal Specification:**
```
FUNCTION isBugCondition_SellQuantity(X)
  INPUT: X of type FillEvent {side, tdMode, feeRate, fillSz}
  OUTPUT: boolean

  RETURN X.side = "buy"
         AND X.tdMode = "cash"
         AND X.feeRate > 0
END FUNCTION
```

**Bug 2 — Tick Parser Sequence Validation**

The bug manifests when the tickers channel pushes data without a `seqId` field. The Go struct deserializes this as `SeqId = 0`. On the first tick, no previous sequence is stored, so `loaded = false` falls through to `Store(symbol, 0)`. On the second tick, `prev = 0` and `tick.SequenceId = 0`, so `0 <= 0` is true → tick rejected.

Actually, examining the code more carefully: on the first tick `loaded = false`, so the `if loaded` block is skipped and `Store(symbol, 0)` is called. On the **second** tick, `loaded = true`, `prev = 0`, `tick.SequenceId = 0`, and `0 <= 0` evaluates true → rejected. So the first tick passes but every subsequent tick is rejected.

**Formal Specification:**
```
FUNCTION isBugCondition_TickParser(X)
  INPUT: X of type ParsedTick {seqId}
  OUTPUT: boolean

  RETURN X.seqId = 0
END FUNCTION
```

### Examples

- **Bug 1**: BUY 323 WIF fills, feeRate=0.0008 → available=322.7416, SELL attempts 323 → OKX rejects "insufficient balance"
- **Bug 1**: BUY 1000 DOGE fills, feeRate=0.001 → available=999, SELL attempts 1000 → rejected
- **Bug 2**: First tick arrives with seqId=0, passes (no prev stored). Second tick arrives with seqId=0, `0 <= 0` → rejected as "sequence out of order: current=0, previous=0"
- **Bug 2 edge**: All subsequent ticks also rejected because stored prev remains 0 and incoming seqId is always 0

## Expected Behavior

### Preservation Requirements

**Unchanged Behaviors:**
- SELL fills → counter BUY orders use configured grid order size (USDT spend, no fee deduction needed)
- BUY fills with feeRate=0 → SELL quantity equals full fill size (net = gross when fee is zero)
- Non-zero seqId ticks with strictly increasing sequence → accepted as before
- Non-zero seqId ticks with seqId <= prev → still rejected as "sequence out of order"
- All other validation rules (price range, crossed book, timestamp tolerance) unchanged
- Private WebSocket logic, network guards, credentials, startup composition untouched

**Scope:**
Only two code paths are modified:
1. Counter-order quantity calculation in `OnFill` when side="buy"
2. Sequence validation in `parser.validate()` when `tick.SequenceId == 0`

## Hypothesized Root Cause

### Bug 1: SELL Quantity

The `OnFill` method at line ~125 sets `orderSize = cfg.OrderSize` for all counter-orders regardless of side. For SELL counter-orders in cash mode, the available asset balance is `fillSz * (1 - feeRate)`, not `cfg.OrderSize`. The code does not account for fee deduction from the received asset.

**Root cause**: Missing fee-adjusted quantity calculation for the `side == "buy"` (counter SELL) path.

### Bug 2: Tick Parser

The `validate()` method at Rule 6 (line ~193) checks `tick.SequenceId <= prev` unconditionally. When tickers channel data omits `seqId` (zero value), this comparison becomes `0 <= 0` which is always true after the first tick.

**Root cause**: No guard for `seqId == 0` before applying monotonic sequence validation. The `"last"` field mapping is correct — OKX tickers channel sends `"last"` for last traded price.

## Correctness Properties

Property 1: Bug Condition - Fee-Adjusted SELL Quantity

_For any_ BUY fill event in cash mode with feeRate > 0, the fixed `OnFill` function SHALL calculate the counter SELL quantity as `fillSz * (1 - feeRate)` (net received amount after maker fee deduction), ensuring the order quantity does not exceed available balance.

**Validates: Requirements 2.1, 2.2**

Property 2: Bug Condition - Zero SeqId Bypass

_For any_ tick where `seqId == 0`, the fixed `validate` function SHALL skip sequence ordering validation entirely, allowing the tick to proceed to price/timestamp validation without rejection.

**Validates: Requirements 2.4, 2.5**

Property 3: Preservation - Non-BUY Fills Unchanged

_For any_ fill event where side != "buy" OR feeRate == 0, the fixed `OnFill` function SHALL produce the same counter-order quantity as the original function (cfg.OrderSize for sells, full fillSz when fee is zero).

**Validates: Requirements 3.1, 3.2**

Property 4: Preservation - Non-Zero SeqId Validation Unchanged

_For any_ tick where `seqId > 0`, the fixed `validate` function SHALL apply the same monotonic sequence check as the original function — accept if strictly greater than previous, reject otherwise.

**Validates: Requirements 3.3, 3.4, 3.5**

## Fix Implementation

### Changes Required

**File**: `internal/execution/fill_handler.go`

**Function**: `OnFill`

**Specific Changes**:
1. **Fee-adjusted SELL quantity**: When `side == "buy"`, calculate `orderSize = fillSz * (1 - feeRate)` instead of using `cfg.OrderSize`. Find the matching `GridConfig` to obtain `FeeRate`, then compute `size.Mul(decimal.NewFromInt(1).Sub(cfg.FeeRate))`.
2. **Keep SELL-fill path unchanged**: When `side == "sell"`, continue using `cfg.OrderSize` for the counter BUY (USDT spend amount, no fee adjustment needed).

**File**: `internal/marketdata/parser.go`

**Function**: `validate` (Rule 6 — sequence check)

**Specific Changes**:
1. **Skip when seqId == 0**: Add guard `if tick.SequenceId == 0 { /* skip sequence validation */ }` before the existing monotonic check.
2. **Do not store zero**: Only call `p.lastSequenceIds.Store(...)` when `tick.SequenceId > 0`, so zero-seqId ticks don't pollute the sequence tracking map.

No changes to:
- Private WebSocket (`ws_private.go`, `private_ws_state_machine.go`)
- Network guards (`network_guard.go`)
- Credentials (`credentials.go`)
- Startup composition (`cmd/main.go`)

## Testing Strategy

### Validation Approach

The testing strategy follows a two-phase approach: first, surface counterexamples demonstrating the bugs on unfixed code, then verify the fixes work correctly and preserve existing behavior.

### Exploratory Bug Condition Checking

**Goal**: Surface counterexamples that demonstrate both bugs BEFORE implementing the fix. Confirm root cause analysis.

**Test Plan**: Write unit tests that exercise both buggy code paths on the current unfixed code.

**Test Cases**:
1. **SELL Quantity — BUY fill with fee**: Simulate BUY fill with feeRate=0.0008, assert counter-order quantity > available balance (will demonstrate bug on unfixed code)
2. **SELL Quantity — Verify cfg.OrderSize used**: Show that cfg.OrderSize is used regardless of actual available asset (unfixed behavior)
3. **Tick Parser — Second tick rejected**: Parse two consecutive tickers-channel messages with seqId=0, assert second tick is rejected (will demonstrate bug on unfixed code)
4. **Tick Parser — All ticks after first rejected**: Parse N ticks with seqId=0, show only first passes

**Expected Counterexamples**:
- Counter SELL quantity = cfg.OrderSize > fillSz * (1 - feeRate)
- Second and subsequent ticks with seqId=0 rejected as "sequence out of order: current=0, previous=0"

### Fix Checking

**Goal**: Verify that for all inputs where the bug condition holds, the fixed functions produce expected behavior.

**Pseudocode:**
```
// Bug 1
FOR ALL X WHERE X.side = "buy" AND X.feeRate > 0 DO
  counterOrder := OnFill_fixed(X)
  ASSERT counterOrder.quantity = X.fillSz * (1 - X.feeRate)
  ASSERT counterOrder.quantity <= X.availableBalance
END FOR

// Bug 2
FOR ALL X WHERE X.seqId = 0 AND X.hasValidPrice DO
  result := ParseAndValidate_fixed(X)
  ASSERT result != nil (tick not rejected by sequence check)
END FOR
```

### Preservation Checking

**Goal**: Verify that for all inputs where the bug condition does NOT hold, the fixed functions produce the same results as the original.

**Pseudocode:**
```
// Bug 1 preservation
FOR ALL X WHERE X.side = "sell" OR X.feeRate = 0 DO
  ASSERT OnFill(X).quantity = OnFill_fixed(X).quantity
END FOR

// Bug 2 preservation
FOR ALL X WHERE X.seqId > 0 DO
  ASSERT ParseAndValidate(X) = ParseAndValidate_fixed(X)
END FOR
```

**Testing Approach**: Property-based testing is recommended for preservation checking because:
- It generates many random fill events and tick messages across the input domain
- It catches edge cases (feeRate exactly 0, seqId boundary values) that manual tests miss
- It provides strong guarantees that non-buggy paths are unaffected

### Unit Tests

- Test `OnFill` with BUY fill, feeRate=0.0008 → assert SELL quantity = fillSz * 0.9992
- Test `OnFill` with BUY fill, feeRate=0 → assert SELL quantity = fillSz (preservation)
- Test `OnFill` with SELL fill → assert BUY quantity = cfg.OrderSize (preservation)
- Test `validate` with seqId=0 first tick → passes
- Test `validate` with seqId=0 second tick → passes (fix)
- Test `validate` with seqId=5 then seqId=6 → passes (preservation)
- Test `validate` with seqId=5 then seqId=4 → rejected (preservation)
- Test `validate` with seqId=0 does not pollute sequence map

### Property-Based Tests

- Generate random FillEvents with side="buy" and feeRate in (0, 0.01] → assert SELL qty = fillSz * (1 - feeRate)
- Generate random FillEvents with side="sell" → assert counter BUY qty = cfg.OrderSize (unchanged)
- Generate random tick sequences with seqId=0 → assert all pass sequence validation
- Generate random tick sequences with seqId > 0 strictly increasing → assert all pass
- Generate random tick sequences with seqId > 0 non-increasing → assert rejected

### Integration Tests

- End-to-end: simulate BUY fill → verify counter SELL order placed with fee-adjusted quantity
- End-to-end: feed multiple tickers-channel messages → verify all parsed successfully with seqId=0
- Verify rebalancer receives tick data after parser fix (no longer starved)
