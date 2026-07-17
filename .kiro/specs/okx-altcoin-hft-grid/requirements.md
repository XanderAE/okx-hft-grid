# Requirements Document

## Introduction

本文档定义 OKX 山寨币高频网格/均值回归交易系统的功能和非功能需求。该系统部署在东京 CPU Optimized 服务器（8 vCPU、16GB RAM、150-300GB NVMe、Ubuntu 22.04/24.04）上，通过 OKX API 对山寨币进行自动化网格交易和均值回归交易，利用低延迟优势在价格波动中捕获利润。

## Glossary

- **Market_Data_Engine**: 行情引擎，负责通过 WebSocket 接收、解析和分发 OKX 实时行情数据的组件
- **Order_Book_Manager**: 订单簿管理器，维护每个交易对本地订单簿快照并提供价格查询的组件
- **Strategy_Engine**: 策略引擎，管理和调度所有交易策略、负责信号生成和订单决策的组件
- **Order_Execution_Engine**: 订单执行引擎，管理订单全生命周期并与 OKX API 交互的组件
- **Risk_Manager**: 风控管理器，实时监控交易风险并对交易请求进行审批和限制的组件
- **Grid_Strategy**: 网格策略，在预设价格区间内等差/等比布置买卖订单以捕获波动利润的策略模块
- **Mean_Reversion_Strategy**: 均值回归策略，当价格偏离统计均值时进行反向操作的策略模块
- **Rate_Limiter**: 速率限制器，使用令牌桶算法控制 API 请求频率的组件
- **Grid_Level**: 网格价位，网格策略中预计算的一个价格档位
- **Z_Score**: Z 分数，当前价格相对移动平均值的标准差倍数，用于衡量偏离程度
- **Emergency_Stop**: 紧急停止机制，在极端情况下强制停止所有交易并撤销挂单的安全机制
- **Reconciliation**: 对账，定期比对本地状态与交易所实际状态以确保一致性的过程
- **POST_ONLY**: 仅挂单模式，确保订单只以 Maker 身份挂入订单簿而不主动吃单的订单类型

## Requirements

### Requirement 1: 行情数据接收与分发

**User Story:** As a trading system operator, I want the system to receive and distribute real-time market data from OKX, so that all strategy components can make timely trading decisions based on fresh data.

#### Acceptance Criteria

1. WHEN the system starts, THE Market_Data_Engine SHALL establish a WebSocket connection to OKX within 10 seconds and subscribe to configured trading pair channels (ticker, depth, trades)
2. WHEN a WebSocket message is received, THE Market_Data_Engine SHALL parse and validate the data within 100 microseconds
3. WHEN a tick data arrives, THE Market_Data_Engine SHALL verify that the timestamp is within 5 seconds of current time, prices are positive, and bestBid is less than bestAsk
4. IF the sequenceId of incoming data is not strictly greater than the previous sequenceId, THEN THE Market_Data_Engine SHALL discard the data and log a sequence gap warning
5. IF tick data fails validation (timestamp exceeds 5-second threshold, non-positive price, or bestBid >= bestAsk), THEN THE Market_Data_Engine SHALL discard the data, notify downstream components of a DATA_INVALID event, and increment a validation failure counter
6. WHEN valid market data is received, THE Market_Data_Engine SHALL notify registered downstream components (Order_Book_Manager, Strategy_Engine) via event callbacks within 50 microseconds of validation completion
7. IF the WebSocket connection is lost (heartbeat timeout exceeding 30 seconds or abnormal close), THEN THE Market_Data_Engine SHALL mark all market data as STALE and initiate exponential backoff reconnection starting at 1 second up to 60 seconds maximum; IF reconnection is not already in progress, THE Market_Data_Engine SHALL pause new order generation until reconnection succeeds; existing orders SHALL remain active on the exchange without cancellation
8. WHEN reconnection succeeds, THE Market_Data_Engine SHALL resubscribe to all channels, request full order book snapshots, verify data continuity via sequenceId, and resume strategy notification only after receiving data with a timestamp within 5 seconds of current time
9. IF the WebSocket connection cannot be established within 10 seconds on initial startup or reconnection attempt, THEN THE Market_Data_Engine SHALL log a connection timeout error and retry using the exponential backoff strategy

### Requirement 2: 订单簿管理

**User Story:** As a strategy engine, I want an accurate local order book for each trading pair, so that I can calculate precise execution prices and detect market conditions.

#### Acceptance Criteria

1. WHEN a full order book snapshot is received, THE Order_Book_Manager SHALL replace the local order book for that symbol with the snapshot data, ordered by price level descending for bids and ascending for asks
2. WHEN an incremental order book update is received, THE Order_Book_Manager SHALL apply the delta to the existing local order book by inserting new price levels, updating quantities for existing price levels, and removing price levels where quantity equals zero
3. WHEN mid price is requested, THE Order_Book_Manager SHALL return the calculation (bestBid + bestAsk) / 2 as a decimal value with up to 8 decimal places of precision
4. IF either the bid side or the ask side of the order book is empty when mid price or spread is requested, THEN THE Order_Book_Manager SHALL return an error indication stating that the calculation is unavailable due to insufficient book depth
5. WHEN spread is requested, THE Order_Book_Manager SHALL return the calculation bestAsk - bestBid as a non-negative decimal value with up to 8 decimal places of precision
6. WHEN VWAP is requested for a given quantity, THE Order_Book_Manager SHALL walk the specified side (bid or ask) of the order book from the best price level, accumulating volume until the requested quantity is filled, and return the volume-weighted average price across all consumed levels
7. IF the requested VWAP quantity exceeds the total available depth on the specified side of the order book, THEN THE Order_Book_Manager SHALL return an error indication stating that insufficient liquidity is available, along with the maximum quantity that can be filled
8. IF a crossed order book is detected where bestBid >= bestAsk, or depth on either side changes by more than 50% of total levels within a single update, THEN THE Order_Book_Manager SHALL log the anomaly with timestamp and symbol, and request a full order book resynchronization within 1 second
9. WHEN an incremental update arrives with a sequence number that is not exactly 1 greater than the last processed sequence number, THE Order_Book_Manager SHALL discard the local book for that symbol and request a full snapshot within 1 second
10. WHEN a full order book resynchronization is requested, THE Order_Book_Manager SHALL discard all incremental updates received for that symbol until the new full snapshot is successfully applied

### Requirement 3: 网格策略计算

**User Story:** As a trader, I want the system to compute and manage grid trading levels, so that I can profit from price oscillations within a defined range.

#### Acceptance Criteria

1. WHEN a grid configuration specifies ARITHMETIC type, THE Grid_Strategy SHALL calculate grid levels with equal price intervals: step = (upperPrice - lowerPrice) / gridCount
2. WHEN a grid configuration specifies GEOMETRIC type, THE Grid_Strategy SHALL calculate grid levels with equal price ratios: ratio = (upperPrice / lowerPrice) ^ (1 / gridCount)
3. IF the grid configuration is valid (upperPrice > lowerPrice AND gridCount is an integer between 3 and 500 inclusive), THEN THE Grid_Strategy SHALL produce exactly gridCount + 1 grid levels
4. THE Grid_Strategy SHALL ensure all generated grid levels are in strictly ascending price order
5. THE Grid_Strategy SHALL ensure the first grid level price equals lowerPrice and the last grid level price equals upperPrice within a tolerance of 1e-9 relative to the price range (upperPrice - lowerPrice)
6. WHEN placing initial grid orders, THE Grid_Strategy SHALL place BUY orders at all grid levels strictly below the current price, SELL orders at all grid levels strictly above the current price, and no order at a grid level equal to the current price
7. THE Grid_Strategy SHALL use POST_ONLY order type for all grid orders to avoid taker fees
8. THE Grid_Strategy SHALL ensure no grid level simultaneously holds both a buy order and a sell order
9. IF the grid configuration is invalid (upperPrice ≤ lowerPrice OR gridCount < 3 OR gridCount > 500), THEN THE Grid_Strategy SHALL reject the configuration and provide an error message indicating the specific validation failure
10. IF a POST_ONLY order is rejected by the exchange, THEN THE Grid_Strategy SHALL retry the order placement up to 3 times with a delay of 1000 milliseconds between attempts before reporting a placement failure

### Requirement 4: 网格成交处理

**User Story:** As a trader, I want the system to automatically place counter orders when grid orders are filled, so that I can continuously capture grid profits.

#### Acceptance Criteria

1. WHEN a grid BUY order is filled and the current grid level is not the highest configured level, THE Grid_Strategy SHALL place a SELL order at the next higher grid level with the same quantity as the filled BUY order
2. WHEN a grid SELL order is filled and the current grid level is not the lowest configured level, THE Grid_Strategy SHALL place a BUY order at the next lower grid level with the same quantity as the filled SELL order
3. IF the calculated net profit (current_sell_price - volume_weighted_average_buy_price) * quantity does not exceed double the single-side trading fee * quantity * 2, THEN THE Grid_Strategy SHALL recalculate profit from the current price components at the time of fill evaluation and skip placing the counter sell order if unprofitable, logging the skipped action
4. WHEN a grid SELL order is filled, THE Grid_Strategy SHALL update the realized PnL by adding (fill_price - volume_weighted_average_buy_price) * fill_quantity
5. IF placing a counter BUY order would cause total position quantity to exceed the configured maxPosition, THEN THE Grid_Strategy SHALL skip placing that BUY order
6. WHEN a grid SELL order is filled at the highest grid level, THE Grid_Strategy SHALL not place a counter order beyond the grid boundary
7. IF counter order placement fails due to exchange rejection or network error, THEN THE Grid_Strategy SHALL retry placement up to 3 times with 1-second intervals before marking the grid level as requiring manual intervention

### Requirement 5: 均值回归信号计算

**User Story:** As a trader, I want the system to generate mean reversion trading signals, so that I can profit when prices deviate significantly from their statistical mean and revert back.

#### Acceptance Criteria

1. WHEN calculating the mean reversion signal, THE Mean_Reversion_Strategy SHALL compute the moving average using the configured MA type (SMA, EMA, or VWAP) over the lookback period, where lookback period is an integer between 10 and 500 (inclusive)
2. WHEN computing EMA, THE Mean_Reversion_Strategy SHALL use alpha = 2 / (lookbackPeriod + 1) and produce a result bounded between MIN(prices) and MAX(prices) in the lookback window
3. WHEN the number of available price data points is greater than or equal to the lookback period, THE Mean_Reversion_Strategy SHALL calculate the Z_Score as (currentPrice - mean) / standardDeviation
4. IF the standard deviation is zero during Z_Score calculation, THEN THE Mean_Reversion_Strategy SHALL suppress signal generation and retain the previous signal state unchanged
5. IF the number of available price data points is less than the lookback period, THEN THE Mean_Reversion_Strategy SHALL suppress signal generation until sufficient data is accumulated
6. WHEN Z_Score is less than negative entryThreshold, THE Mean_Reversion_Strategy SHALL generate a BUY signal
7. WHEN Z_Score is greater than positive entryThreshold, THE Mean_Reversion_Strategy SHALL generate a SELL signal
8. WHEN the absolute value of Z_Score is less than exitThreshold, THE Mean_Reversion_Strategy SHALL generate a CLOSE signal
9. WHEN a signal is generated, THE Mean_Reversion_Strategy SHALL enforce a cooldown period of at least cooldownMs milliseconds (range: 100 to 60000) before generating the next signal
10. THE Mean_Reversion_Strategy SHALL require entryThreshold to be greater than exitThreshold, entryThreshold to be within 1.0 to 5.0, and exitThreshold to be within 0.1 to 2.0
11. IF entryThreshold or exitThreshold violates the configured bounds or entryThreshold is less than or equal to exitThreshold, THEN THE Mean_Reversion_Strategy SHALL reject the configuration and report a validation error indicating which parameter is invalid

### Requirement 6: 订单执行管理

**User Story:** As a system operator, I want reliable order execution with full lifecycle management, so that I can ensure orders are correctly placed, tracked, and reconciled with the exchange.

#### Acceptance Criteria

1. WHEN an order is submitted, THE Order_Execution_Engine SHALL transition the order status through the valid state machine: PENDING → SUBMITTED → OPEN → (PARTIALLY_FILLED →) FILLED | CANCELLED | EXPIRED
2. IF an order status transition is attempted that does not follow the defined state machine, THEN THE Order_Execution_Engine SHALL reject the transition, preserve the current order status unchanged, and return an error indicating the invalid transition attempted and the current valid state
3. WHEN the OKX API returns an order rejection, THE Order_Execution_Engine SHALL update the local order status to REJECTED and log the rejection reason
4. WHEN a batch order placement is requested, THE Order_Execution_Engine SHALL submit up to 20 orders per batch, respecting the OKX API rate limit, and return a per-order result indicating success with order ID or failure with error reason
5. WHEN an API call receives HTTP 429 (rate limited), THE Order_Execution_Engine SHALL retry with exponential backoff (initial 100ms, doubling each retry, max 5000ms, max 3 retries)
6. WHEN an API call receives HTTP 5xx (server error), THE Order_Execution_Engine SHALL retry up to 3 times with 100ms delay between attempts
7. IF all retry attempts are exhausted for an API call (after HTTP 429 or HTTP 5xx retries), THEN THE Order_Execution_Engine SHALL mark the affected order as ERROR, emit a failure event with the last error reason, and cease further retry attempts for that request
8. THE Order_Execution_Engine SHALL perform state reconciliation with the exchange every 60 seconds; WHEN a discrepancy is detected between local and exchange order state, THE Order_Execution_Engine SHALL treat the exchange state as authoritative and update the local state to match
9. IF state reconciliation fails due to network or API error, THEN THE Order_Execution_Engine SHALL log the failure and retry reconciliation at the next 60-second interval without altering local order state

### Requirement 7: 风控管理

**User Story:** As a risk manager, I want the system to enforce comprehensive trading risk limits, so that potential losses are bounded and the system operates within safe parameters.

#### Acceptance Criteria

1. WHEN an order request is received for risk check and the post-order position exposure (calculated as total position quantity × current market price for that symbol) would exceed maxPositionPerSymbol, THE Risk_Manager SHALL reject the order request and return a rejection response indicating the per-symbol position limit breach
2. WHEN an order request is received and the post-order total portfolio exposure (sum of absolute notional exposure across all symbols) would exceed maxTotalPosition, THE Risk_Manager SHALL reject the order request and return a rejection response indicating the total portfolio limit breach
3. WHEN daily PnL (realized PnL plus mark-to-market unrealized PnL, reset at the configured trading-day start time) reaches the negative maxDailyLoss threshold, THE Risk_Manager SHALL reject all new order requests for the remainder of the trading day until the next trading-day start time
4. WHEN the number of submitted order requests in any 1-second sliding window reaches maxOrdersPerSecond, THE Risk_Manager SHALL reject additional order requests until the window clears
5. WHEN the number of open orders for a symbol reaches maxOpenOrders, THE Risk_Manager SHALL reject new order requests for that symbol
6. WHEN the current spread for a symbol is less than minSpreadBps, THE Risk_Manager SHALL reject order requests for that symbol
7. WHEN daily PnL falls below the negative emergencyStopLoss threshold, THE Risk_Manager SHALL trigger Emergency_Stop: cancel all open orders, halt all strategies, and send a CRITICAL alert within 1 second of threshold breach detection
8. WHILE Emergency_Stop is active, THE Risk_Manager SHALL reject all trading operations
9. WHILE Emergency_Stop is active, THE Risk_Manager SHALL continue evaluating all risk check rules and report specific violation reasons, but SHALL reject all trading operations regardless of individual check results
10. WHEN manual confirmation to resume is received while Emergency_Stop is active, THE Risk_Manager SHALL deactivate Emergency_Stop and allow trading operations to proceed

### Requirement 8: 速率限制

**User Story:** As a system operator, I want API requests to be rate-limited, so that the system respects OKX API quotas and avoids being banned.

#### Acceptance Criteria

1. THE Rate_Limiter SHALL use a token bucket algorithm configured according to OKX API endpoint limits (approximately 20 requests per 2 seconds per endpoint)
2. WHEN a token is not available, THE Rate_Limiter SHALL block the request for a maximum of 5 seconds waiting for a token to become available
3. IF a request has been blocked for 5 seconds without acquiring a token, THEN THE Rate_Limiter SHALL return a timeout error to the caller without sending the API request
4. WHEN maximum retries (3) are exhausted without success, THE Rate_Limiter SHALL return an error response indicating rate limit exceeded with the endpoint name and the time until the next available token
5. THE Rate_Limiter SHALL ensure that no API endpoint receives more requests than its configured limit within any 2-second measurement window
6. THE Rate_Limiter SHALL maintain separate token buckets for each distinct OKX API endpoint (e.g., trade/order, trade/batch-orders, market/ticker)

### Requirement 9: 数据校验与完整性

**User Story:** As a system operator, I want all incoming data to be validated against strict rules, so that the system never makes trading decisions based on corrupted or invalid data.

#### Acceptance Criteria

1. WHEN tick data arrives, THE Market_Data_Engine SHALL validate that lastPrice, bestBid, and bestAsk are all decimal values greater than zero and less than or equal to 99,999,999.99
2. WHEN tick data arrives, THE Market_Data_Engine SHALL validate that bestBid is strictly less than bestAsk (no crossed book)
3. WHEN tick data arrives, THE Market_Data_Engine SHALL validate that the timestamp is within 5 seconds of local server time
4. IF any tick data field fails validation (criteria 1, 2, or 3), THEN THE Market_Data_Engine SHALL discard the entire tick, log the validation failure with the symbol and failure reason, and not forward the tick to any strategy component
5. WHEN a GridConfig is loaded, THE Grid_Strategy SHALL validate that upperPrice is greater than lowerPrice, gridCount is between 3 and 500 inclusive, and orderSize is greater than or equal to the exchange-provided minimum order size for the target trading pair
6. WHEN a MeanReversionConfig is loaded, THE Mean_Reversion_Strategy SHALL validate that entryThreshold is greater than exitThreshold and both values are positive
7. IF any configuration field fails validation (criteria 5 or 6), THEN THE system SHALL reject the configuration, report an error message indicating which field failed and why, and not activate the strategy

### Requirement 10: 配置管理与持久化

**User Story:** As a system operator, I want configurations to be stored securely and state to be persisted, so that the system can recover from restarts without data loss.

#### Acceptance Criteria

1. THE system SHALL store API keys in encrypted form and load them via environment variables or an encrypted key management service, never in plaintext configuration files
2. THE system SHALL initiate persistence of order state, position data, and strategy state to local storage (SQLite or RocksDB) within 1 second of any state change occurring, using write-ahead logging or equivalent durability guarantee; the persistence operation itself may take longer to complete, and maximum data loss on unexpected termination is bounded to 1 second of state changes
3. WHEN the system restarts, THE system SHALL reload persisted state, compare local order and position records against the exchange's current state, cancel or update any locally-tracked orders whose exchange status differs, and resume trading only after all discrepancies are resolved or flagged, within 60 seconds of restart initiation
4. THE system SHALL store time-series market data using memory-mapped files or ring buffers with a configurable fixed capacity (default: 1,000,000 records per instrument), discarding the oldest records when capacity is reached
5. IF persisted state is corrupted or unreadable on restart, THEN THE system SHALL log an error indicating the corruption, halt automated trading, and notify the operator, preserving the corrupted file for diagnosis
6. IF the exchange is unreachable during post-restart reconciliation for more than 60 seconds, THEN THE system SHALL halt automated trading and notify the operator rather than resume with unreconciled state
7. IF the exchange becomes reachable again during the 60-second timeout period, THEN THE system SHALL resume reconciliation immediately

### Requirement 11: 监控与告警

**User Story:** As a system operator, I want real-time monitoring and alerting, so that I can be notified of system issues and track trading performance.

#### Acceptance Criteria

1. THE system SHALL expose performance metrics (order latency in milliseconds, throughput in orders per second, error rates as percentage) via Prometheus-compatible metrics endpoint, updated at intervals of no more than 5 seconds
2. WHEN Emergency_Stop is triggered, THE system SHALL send a CRITICAL alert via configured notification channel (Telegram or Discord bot) within 5 seconds of the trigger event, including the trigger reason and current portfolio state summary
3. WHEN a state reconciliation detects discrepancies that cannot be auto-resolved, THE system SHALL send a WARNING alert to the operator within 30 seconds of detection, including the type of discrepancy and affected instruments
4. THE system SHALL log all trading actions (orders placed, fills, cancellations) with each entry containing: timestamp (millisecond precision), action type, instrument identifier, quantity, price, order ID, and action result, retained for a minimum of 30 days
5. IF a configured notification channel is unreachable when an alert is triggered, THEN THE system SHALL retry delivery up to 3 times with 10-second intervals, and if still unreachable, SHALL log the undelivered alert locally with CRITICAL severity for later operator review

### Requirement 12: 性能要求

**User Story:** As a system architect, I want the system to meet strict latency and throughput targets, so that it can effectively operate as a high-frequency trading system.

#### Acceptance Criteria

1. THE Market_Data_Engine SHALL parse and validate incoming market data within 100 microseconds at the 99th percentile, measured under a sustained load of 10,000 ticks per second
2. WHEN a market update is received, THE Strategy_Engine SHALL compute trading signals within 500 microseconds at the 99th percentile, measured under a sustained load of 10,000 ticks per second
3. WHEN an order is submitted for risk evaluation, THE Risk_Manager SHALL complete the order risk check within 50 microseconds at the 99th percentile
4. THE system SHALL achieve end-to-end latency (market data reception to order API request) of less than 2 milliseconds at the 99th percentile, measured under a sustained load of 10,000 ticks per second with up to 100 active symbols
5. THE Market_Data_Engine SHALL process at least 10,000 ticks per second sustained over a continuous 60-second measurement window without exceeding latency targets defined in criterion 1
6. THE system SHALL maintain total memory usage below 2 gigabytes while tracking up to 100 active symbols with full order book depth
7. IF end-to-end latency exceeds 2 milliseconds for any single order path, THEN THE system SHALL log the latency violation and continue processing subsequent events without interruption

### Requirement 13: 安全要求

**User Story:** As a system operator, I want the system to follow security best practices, so that trading funds and credentials are protected from unauthorized access.

#### Acceptance Criteria

1. THE system SHALL communicate with OKX API exclusively over TLS 1.2 or higher with server certificate verification, rejecting connections where the certificate chain cannot be validated against the system trust store
2. THE system SHALL sign all REST API requests using HMAC-SHA256 and include a timestamp within 30 seconds of current server time to prevent replay attacks
3. IF the system detects it is running as root or configuration files have permissions more permissive than 600, THEN THE system SHALL refuse to start and output an error message indicating the specific permission violation
4. THE system SHALL sanitize all log output to exclude API keys, secret keys, passphrases, and any string matching configured credential patterns, replacing them with a fixed-length mask of 8 asterisk characters
5. THE system SHALL configure OKX API keys with trading-only permissions (no withdrawal, no transfer) and restrict access to a single whitelisted IP address matching the server's public IP
6. IF TLS certificate verification fails or the connection cannot be established securely, THEN THE system SHALL abort the API request without sending any credentials (TLS certificate SHALL be verified before transmitting any credential data) and log the failure reason without exposing sensitive data
7. WHEN the system starts, THE system SHALL validate that all required API credentials (API key, secret key, passphrase) are present and non-empty, refusing to start if any credential is missing

### Requirement 14: 故障恢复

**User Story:** As a system operator, I want the system to gracefully handle failures and automatically recover, so that trading can resume with minimal manual intervention.

#### Acceptance Criteria

1. WHEN a WebSocket disconnection occurs, THE system SHALL maintain existing open orders on the exchange (no unnecessary cancellation) and attempt reconnection using exponential backoff starting at 1 second, up to a maximum of 5 attempts within 60 seconds
2. IF all reconnection attempts are exhausted without success, THEN THE system SHALL halt all active strategies, send an alert, and await manual intervention
3. WHEN reconnection succeeds, THE system SHALL obtain a full order book snapshot and verify that local state matches exchange state (open order quantities and prices) within 10 seconds; strategy operations SHALL resume only after both the snapshot is fully applied AND state verification is complete
4. WHEN extreme market conditions are detected (price change exceeding 5% within 1 minute, or spread exceeding 3 times the rolling 5-minute average spread), THE system SHALL cancel all grid orders, pause mean reversion strategy, and send an alert
5. WHEN state reconciliation detects discrepancies (position quantity differs by more than 1 unit from exchange-reported position, or order count differs from exchange-reported open orders), THE system SHALL halt the affected strategy and wait for manual confirmation; WHEN manual confirmation is received, THE system SHALL resume the affected strategy and allow future normal operation
6. THE system SHALL be managed by systemd with automatic restart on crash (restart delay of 5 seconds, maximum 3 restarts within 60 seconds before stopping) and structured log output to journald
