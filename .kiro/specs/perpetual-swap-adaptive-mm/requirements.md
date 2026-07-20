# Requirements: Perpetual Swap Adaptive Market Making

## Introduction

Convert the BTC trading bot from spot (cash) to OKX perpetual swap (BTC-USDT-SWAP), enabling lower maker fees (0.02% vs 0.08%), bidirectional trading (long and short), and algorithmic leverage control (1x-3x). Backtesting showed spot is unprofitable at 0.08% fees while swap at 0.02% turns profitable (+0.59% over 7 days at 0.3% spread / 1.5% stop). This spec replaces the spot execution path entirely — no spot mode is retained.

The bot must never exceed 3x leverage, must derive direction (long/short/flat) algorithmically, and must respect funding costs and liquidation distance as first-class risk constraints.

## Requirements

### Requirement 1: Perpetual Swap Execution

**User Story:** As a trader, I want the bot to trade BTC-USDT-SWAP so that I pay 0.02% maker fees instead of 0.08% spot fees.

#### Acceptance Criteria
1. WHEN the bot places any order THEN it SHALL target instrument `BTC-USDT-SWAP` using `tdMode=isolated`.
2. WHEN the bot sizes an order THEN it SHALL convert USDT notional to integer contract counts (1 contract = 0.01 BTC) and reject sizes below the exchange minimum.
3. WHEN the bot places an order THEN it SHALL include a valid `lever` parameter and an alphanumeric `clOrdId` (letters and digits only, max 32 chars).
4. WHEN the bot starts THEN it SHALL set the instrument's leverage on OKX via the set-leverage endpoint before placing any order.
5. WHEN the swap order is a maker order THEN it SHALL use Post-Only to guarantee the 0.02% maker fee.

### Requirement 2: Algorithmic Direction Signal

**User Story:** As a trader, I want the bot to decide long vs short vs flat algorithmically so that it profits in both up and down trends.

#### Acceptance Criteria
1. WHEN the bot has fewer than N price samples (warmup) THEN it SHALL take no directional position and log a warmup state.
2. WHEN recent price action shows a sustained uptrend THEN the direction signal SHALL be LONG (open long: buy to open, sell to close).
3. WHEN recent price action shows a sustained downtrend THEN the direction signal SHALL be SHORT (open short: sell to open, buy to close).
4. WHEN price action is choppy or ambiguous THEN the direction signal SHALL be FLAT (no new position; manage existing only).
5. WHEN a position is already open THEN the bot SHALL NOT open an opposing position until the current one is closed.
6. The direction algorithm SHALL be deterministic and unit-testable in isolation from network I/O.

### Requirement 3: Dynamic Leverage Control (max 3x)

**User Story:** As a trader, I want leverage to scale with market conditions so that I take more size when conditions are favorable and less when volatile.

#### Acceptance Criteria
1. WHEN computing leverage THEN the bot SHALL produce a value in the closed range [1.0, 3.0] and SHALL NEVER exceed 3.0.
2. WHEN recent realized volatility is high THEN leverage SHALL decrease toward 1x.
3. WHEN recent realized volatility is low AND the direction signal has high confidence THEN leverage MAY increase toward 3x.
4. WHEN the leverage input data is insufficient or invalid THEN the bot SHALL default to 1x.
5. The leverage function SHALL be pure and unit-testable.

### Requirement 4: Bidirectional Position Lifecycle

**User Story:** As a trader, I want the bot to open, manage, and close both long and short positions with the same spread/decay/stop discipline.

#### Acceptance Criteria
1. WHEN a LONG entry fills THEN the bot SHALL place a reduce-only close order at entry × (1 + spread).
2. WHEN a SHORT entry fills THEN the bot SHALL place a reduce-only close order at entry × (1 - spread).
3. WHEN a position is held THEN the bot SHALL apply time decay to the close target (spread shrinks toward a fee-covering floor over hours).
4. WHEN a position is underwater THEN the bot SHALL tighten the close target toward the fee floor to exit faster.
5. WHEN unrealized loss exceeds the hard stop percentage THEN the bot SHALL close at market immediately.
6. WHEN a position is held beyond the max hold time THEN the bot SHALL close at market.
7. The fee-covering floor SHALL be strictly greater than the round-trip fee (2 × 0.02% = 0.04%), e.g. at least 0.05%.

### Requirement 5: Swap Risk Constraints

**User Story:** As a trader, I want liquidation and funding risks controlled so that leverage never causes catastrophic loss.

#### Acceptance Criteria
1. WHEN leverage is set THEN the bot SHALL verify the resulting liquidation price is at least the hard-stop distance away from entry; if not, it SHALL reduce leverage until it is.
2. WHEN a funding timestamp approaches AND holding a position that would pay funding THEN the bot MAY close before funding if the position is not profitable enough to absorb it (configurable).
3. WHEN total position notional would exceed the configured max THEN the bot SHALL NOT open new positions.
4. WHEN the account available margin is insufficient for the intended contract count THEN the bot SHALL reduce the count or skip the order, and SHALL log the reason.

### Requirement 6: Configuration and Safety

**User Story:** As an operator, I want swap parameters version-controlled and validated so that production runs are safe and reproducible.

#### Acceptance Criteria
1. WHEN config loads THEN it SHALL validate instrument=BTC-USDT-SWAP, td_mode=isolated, max_leverage≤3, spread≥0.0005, stop_loss>0, and reject anything else.
2. WHEN production trading is enabled THEN all existing production gate evidence requirements SHALL still apply.
3. WHEN the bot starts THEN it SHALL load existing swap positions from OKX (filtered for dust) so restarts do not orphan positions.
4. WHEN any order is rejected by OKX THEN the bot SHALL log the full sCode and sMsg.
5. No spot (cash) code path SHALL remain reachable in production composition.

### Requirement 7: Backtest Validation Before Production

**User Story:** As a trader, I want the swap strategy backtested with funding and fees before risking real money.

#### Acceptance Criteria
1. WHEN the strategy is implemented THEN a backtest SHALL simulate long+short entries, dynamic leverage, funding cost, and 0.02% fees on historical BTC-USDT-SWAP data.
2. WHEN the backtest runs THEN it SHALL report P&L, win rate, max drawdown, liquidation events (must be zero), and trades/day.
3. WHEN any parameter set produces a liquidation event in backtest THEN that set SHALL be rejected.
