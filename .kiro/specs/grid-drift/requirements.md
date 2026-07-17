# Requirements Document

## Introduction

The Grid Drift feature adds an automatic grid relocation mechanism to the existing grid trading strategy. Currently, the grid strategy operates within a fixed price range (UpperPrice to LowerPrice). When the market price moves outside this range, the strategy ceases trading. Grid Drift detects when the price approaches or exceeds a grid boundary and automatically shifts the entire grid range in the direction of price movement, cancelling stale orders and placing new ones to maintain continuous trading without manual intervention.

## Glossary

- **Grid_Drift_Engine**: The component responsible for detecting boundary proximity, computing the new grid range, and orchestrating the drift operation (cancel old orders, recalculate levels, place new orders).
- **Drift_Trigger**: The condition that initiates a grid drift. Defined as the market price crossing a configurable threshold distance from the grid boundary.
- **Drift_Threshold**: A configurable percentage of the grid range that defines how close the price must be to a boundary before triggering a drift. For example, a threshold of 10% means drift triggers when the price enters the outermost 10% of the current range.
- **Drift_Step**: The amount by which the grid range shifts during a single drift operation, expressed as a number of grid spacing intervals.
- **Cooldown_Period**: The minimum time interval between consecutive drift operations to prevent rapid successive relocations during volatile price action.
- **Grid_Strategy**: The existing grid trading strategy engine implemented in `internal/strategy/` that manages grid levels, order placement, and fill handling.
- **Order_Manager**: The component in `internal/execution/` that handles order placement and cancellation via the OKX REST API.
- **Grid_State**: The runtime state of the grid strategy, tracking levels, positions, average entry price, and realized PnL.
- **Boundary_Zone**: The region of the grid range within Drift_Threshold distance of either boundary. Price entering this zone triggers drift evaluation.

## Requirements

### Requirement 1: Drift Trigger Detection

**User Story:** As a trader, I want the system to automatically detect when the market price approaches a grid boundary, so that the grid can be relocated before trading stops.

#### Acceptance Criteria

1. WHEN the current market price enters the upper Boundary_Zone (price >= UpperPrice - Drift_Threshold * (UpperPrice - LowerPrice)), THE Grid_Drift_Engine SHALL initiate an upward drift evaluation.
2. WHEN the current market price enters the lower Boundary_Zone (price <= LowerPrice + Drift_Threshold * (UpperPrice - LowerPrice)), THE Grid_Drift_Engine SHALL initiate a downward drift evaluation.
3. WHEN the current market price exceeds the UpperPrice, THE Grid_Drift_Engine SHALL initiate an immediate upward drift.
4. WHEN the current market price falls below the LowerPrice, THE Grid_Drift_Engine SHALL initiate an immediate downward drift.
5. THE Grid_Drift_Engine SHALL evaluate drift conditions on every price update received from the market data feed.

### Requirement 2: Grid Range Relocation

**User Story:** As a trader, I want the grid range to shift in the direction of price movement, so that the strategy continues operating around the current market price.

#### Acceptance Criteria

1. WHEN an upward drift is initiated, THE Grid_Drift_Engine SHALL compute a new UpperPrice as the current UpperPrice plus Drift_Step multiplied by the grid spacing interval.
2. WHEN an upward drift is initiated, THE Grid_Drift_Engine SHALL compute a new LowerPrice as the current LowerPrice plus Drift_Step multiplied by the grid spacing interval.
3. WHEN a downward drift is initiated, THE Grid_Drift_Engine SHALL compute a new UpperPrice as the current UpperPrice minus Drift_Step multiplied by the grid spacing interval.
4. WHEN a downward drift is initiated, THE Grid_Drift_Engine SHALL compute a new LowerPrice as the current LowerPrice minus Drift_Step multiplied by the grid spacing interval.
5. THE Grid_Drift_Engine SHALL recalculate grid levels using the same GridType (arithmetic or geometric) and GridCount as the original configuration after computing the new range.
6. THE Grid_Drift_Engine SHALL verify that the new LowerPrice remains positive before executing the drift.
7. IF the computed new LowerPrice is zero or negative, THEN THE Grid_Drift_Engine SHALL set the new LowerPrice to the minimum tick size and adjust the range accordingly.

### Requirement 3: Order Lifecycle During Drift

**User Story:** As a trader, I want stale orders to be cancelled and new orders placed during a drift, so that my capital is deployed at the correct price levels.

#### Acceptance Criteria

1. WHEN a drift is executed, THE Grid_Drift_Engine SHALL cancel all open orders at grid levels that are no longer within the new grid range.
2. WHEN a drift is executed, THE Grid_Drift_Engine SHALL retain open orders at grid levels that remain within the new grid range.
3. WHEN old orders are cancelled and new levels are computed, THE Grid_Drift_Engine SHALL place new orders at the newly created grid levels using the same OrderSize from the GridConfig.
4. THE Grid_Drift_Engine SHALL place BUY orders at new levels below the current market price and SELL orders at new levels above the current market price.
5. IF an order cancellation fails, THEN THE Grid_Drift_Engine SHALL retry the cancellation up to 3 times with 1-second intervals before marking the drift as partially failed.
6. IF order placement at new levels fails, THEN THE Grid_Drift_Engine SHALL retry placement up to 3 times with 1-second intervals before logging an alert.

### Requirement 4: Position Preservation During Drift

**User Story:** As a trader, I want my existing positions and unrealized PnL to be preserved during a grid drift, so that I do not incur unexpected losses from the relocation.

#### Acceptance Criteria

1. WHEN a drift is executed, THE Grid_Drift_Engine SHALL preserve the current Position value in Grid_State without modification.
2. WHEN a drift is executed, THE Grid_Drift_Engine SHALL preserve the AvgEntryPrice in Grid_State without modification.
3. WHEN a drift is executed, THE Grid_Drift_Engine SHALL preserve the RealizedPnL in Grid_State without modification.
4. WHEN a drift is executed, THE Grid_Drift_Engine SHALL preserve the TotalBuys and TotalSells counters in Grid_State without modification.

### Requirement 5: Cooldown Mechanism

**User Story:** As a trader, I want a cooldown period between drift operations, so that rapid market oscillations near a boundary do not cause excessive order churn and unnecessary fees.

#### Acceptance Criteria

1. WHILE the time since the last completed drift is less than the Cooldown_Period, THE Grid_Drift_Engine SHALL suppress drift trigger evaluations.
2. WHEN a drift completes successfully, THE Grid_Drift_Engine SHALL record the completion timestamp for cooldown enforcement.
3. THE Grid_Drift_Engine SHALL use a configurable Cooldown_Period with a default value of 30 seconds.
4. IF the market price exceeds the grid boundary by more than 2x the Drift_Step spacing during cooldown, THEN THE Grid_Drift_Engine SHALL override the cooldown and execute an emergency drift.

### Requirement 6: Drift Configuration

**User Story:** As a trader, I want to configure drift parameters, so that I can tune the behavior to different market conditions and trading pairs.

#### Acceptance Criteria

1. THE Grid_Drift_Engine SHALL accept a DriftThreshold parameter as a decimal between 0.01 and 0.50 representing the percentage of grid range that defines the Boundary_Zone.
2. THE Grid_Drift_Engine SHALL accept a DriftStep parameter as a positive integer representing the number of grid intervals to shift per drift operation.
3. THE Grid_Drift_Engine SHALL accept a CooldownPeriod parameter as a duration in seconds with a minimum value of 5 seconds.
4. THE Grid_Drift_Engine SHALL accept a MaxDrifts parameter as a positive integer representing the maximum number of drifts allowed within a single trading session.
5. THE Grid_Drift_Engine SHALL accept an Enabled parameter as a boolean to enable or disable drift functionality.
6. IF the MaxDrifts limit is reached, THEN THE Grid_Drift_Engine SHALL stop triggering drifts and emit an alert to the monitoring system.

### Requirement 7: Drift Event Logging and Monitoring

**User Story:** As a trader, I want drift events to be logged and monitored, so that I can review drift activity and diagnose issues.

#### Acceptance Criteria

1. WHEN a drift is initiated, THE Grid_Drift_Engine SHALL log the trigger reason, current price, old range (UpperPrice, LowerPrice), and new range.
2. WHEN a drift completes successfully, THE Grid_Drift_Engine SHALL log the number of orders cancelled, number of orders placed, and elapsed time.
3. WHEN a drift fails or partially fails, THE Grid_Drift_Engine SHALL log the failure reason and the list of failed order operations.
4. THE Grid_Drift_Engine SHALL emit a metric for drift count, drift latency, and drift failure count to the monitoring system.
5. WHEN a drift is suppressed due to cooldown, THE Grid_Drift_Engine SHALL log the suppression event with the remaining cooldown time.

### Requirement 8: Drift Atomicity and Error Recovery

**User Story:** As a trader, I want drift operations to be atomic or recoverable, so that a partial failure does not leave the grid in an inconsistent state.

#### Acceptance Criteria

1. THE Grid_Drift_Engine SHALL execute drift operations in a defined sequence: cancel stale orders first, then recalculate levels, then place new orders.
2. IF the cancellation phase fails completely (no orders cancelled after retries), THEN THE Grid_Drift_Engine SHALL abort the drift and retain the current grid range.
3. IF the placement phase fails partially (some orders placed, some not), THEN THE Grid_Drift_Engine SHALL log the partially-placed state and continue operating with the orders that were successfully placed.
4. WHEN a drift is aborted, THE Grid_Drift_Engine SHALL retain the previous Grid_State and grid levels without modification.
5. THE Grid_Drift_Engine SHALL update the internal Grid_State level list only after all cancellations complete and before new order placement begins.
