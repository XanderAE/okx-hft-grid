# Bugfix Requirements Document

## Introduction

Two production bugs on the Singapore EC2 deployment prevent the grid trading bot from operating correctly. Bug 1: the counter-order SELL quantity calculation in `fill_handler.go` uses the raw fill size without deducting maker fees, causing OKX to reject SELL orders due to insufficient available balance. Bug 2: the public WebSocket tick parser in `parser.go` has never successfully parsed a single tick since startup — the `OKXTickerData` struct field mappings do not match OKX's actual WebSocket tickers channel push format (field name mismatch for last price and missing/zero sequence ID), rendering the rebalancer and grid drift non-functional.

## Bug Analysis

### Current Behavior (Defect)

1.1 WHEN a BUY order fills in cash mode (spot trading) and the fill_handler places a counter SELL order THEN the system uses `cfg.OrderSize` (the full grid order size) as the SELL quantity, ignoring that OKX deducted maker fees from the received asset (e.g., BUY 323 WIF fills, fee=0.2584 WIF deducted, available=322.7416, but SELL attempts 323 WIF)

1.2 WHEN the counter SELL order quantity exceeds the available balance (fillSz - fee) THEN OKX rejects the order with "code=1, All operations failed" and the grid loop breaks (no counter-order placed)

1.3 WHEN the public WebSocket tickers channel pushes market data THEN the parser fails to deserialize the last price field (empty string parsed as decimal fails) because OKX's actual push format uses field name `"last"` at the data level but the message may arrive with a different structure or the `SeqId` field is not present in tickers push data

1.4 WHEN the parser encounters the first tick with `SeqId=0` (field not present in tickers channel data, defaults to Go zero value) and no previous sequence is stored THEN the sequence validation rule `tick.SequenceId <= prev` evaluates `0 <= 0` as true and rejects the tick as "sequence out of order: current=0, previous=0"

1.5 WHEN every single tick is rejected by the parser THEN the rebalancer (30s interval) and grid drift have no real-time price data and cannot function, leaving the grid unable to adapt to market movement

### Expected Behavior (Correct)

2.1 WHEN a BUY order fills in cash mode THEN the system SHALL calculate the counter SELL quantity as `fillSz - (fillSz * feeRate)` (net received amount after maker fee deduction) to ensure the SELL quantity does not exceed available balance

2.2 WHEN the counter SELL quantity is calculated with fee deduction THEN the system SHALL successfully place the SELL order without OKX rejection, maintaining the grid trading loop

2.3 WHEN the public WebSocket tickers channel pushes market data THEN the parser SHALL correctly deserialize all price fields from OKX's actual message format, handling both the `"last"` field and any field name variations (e.g., fallback to `"lastPx"` if `"last"` is empty)

2.4 WHEN the tickers channel data does not include a sequence ID field (or it is zero) THEN the parser SHALL skip sequence validation for that tick rather than rejecting it, allowing the first and subsequent ticks to pass through to validation of price/timestamp only

2.5 WHEN ticks are successfully parsed THEN the rebalancer and grid drift SHALL receive real-time price data and function correctly

### Unchanged Behavior (Regression Prevention)

3.1 WHEN a SELL order fills THEN the system SHALL CONTINUE TO place counter BUY orders using the configured grid order size (no fee deduction needed since USDT is received on sell, and BUY orders specify USDT spend amount)

3.2 WHEN a BUY order fills and fee_rate is zero THEN the system SHALL CONTINUE TO use the full fill size as the counter SELL quantity (net amount equals fill size when fee is zero)

3.3 WHEN tick data arrives with a valid non-zero sequence ID that is strictly greater than the previous THEN the system SHALL CONTINUE TO accept the tick (existing monotonic validation preserved for channels that do provide sequence IDs)

3.4 WHEN tick data arrives with a non-zero sequence ID that is less than or equal to the previous THEN the system SHALL CONTINUE TO reject the tick as "sequence out of order" (replay/duplicate protection still active for ordered streams)

3.5 WHEN tick data has lastPrice <= 0, exceeds max price, has crossed book, or has timestamp out of ±5s tolerance THEN the system SHALL CONTINUE TO reject the tick with appropriate validation error (all other validation rules unchanged)

3.6 WHEN the private WebSocket (orders/fills channel) receives data THEN the system SHALL CONTINUE TO function correctly with no changes to connection logic, authentication, or fill reception

---

## Bug Condition Derivation

### Bug 1: Counter-Order SELL Quantity

```pascal
FUNCTION isBugCondition_SellQuantity(X)
  INPUT: X of type FillEvent
  OUTPUT: boolean
  
  // Bug triggers when: BUY fill in cash mode with non-zero fee rate
  RETURN X.side = "buy" AND X.tdMode = "cash" AND X.feeRate > 0
END FUNCTION
```

```pascal
// Property: Fix Checking - Fee-Adjusted SELL Quantity
FOR ALL X WHERE isBugCondition_SellQuantity(X) DO
  counterOrder ← PlaceCounterOrder'(X)
  ASSERT counterOrder.quantity = X.fillSz - (X.fillSz * X.feeRate)
  ASSERT counterOrder.quantity <= X.availableBalance
END FOR
```

```pascal
// Property: Preservation Checking - Non-BUY fills unchanged
FOR ALL X WHERE NOT isBugCondition_SellQuantity(X) DO
  ASSERT PlaceCounterOrder(X) = PlaceCounterOrder'(X)
END FOR
```

### Bug 2: Tick Parser Field Mapping and Sequence Validation

```pascal
FUNCTION isBugCondition_TickParser(X)
  INPUT: X of type RawTickMessage
  OUTPUT: boolean
  
  // Bug triggers when: tickers channel data has zero/absent seqId
  // OR when the last price field is empty due to format mismatch
  RETURN X.seqId = 0 OR X.lastField = ""
END FUNCTION
```

```pascal
// Property: Fix Checking - Tick Parsing Success
FOR ALL X WHERE isBugCondition_TickParser(X) AND X.hasValidPriceInAnyField DO
  result ← ParseAndValidate'(X)
  ASSERT result != nil AND result.LastPrice > 0
END FOR
```

```pascal
// Property: Fix Checking - Zero SeqId Bypass
FOR ALL X WHERE X.seqId = 0 DO
  result ← ParseAndValidate'(X)
  ASSERT sequenceValidation is skipped for X
END FOR
```

```pascal
// Property: Preservation Checking - Non-zero SeqId still validated
FOR ALL X WHERE NOT isBugCondition_TickParser(X) DO
  ASSERT ParseAndValidate(X) = ParseAndValidate'(X)
END FOR
```
