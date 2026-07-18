# Implementation Plan: OKX Altcoin HFT Grid Trading System

## Overview

基于 Go 语言实现的高频网格/均值回归交易系统。利用 Go 的高并发模型（goroutines/channels）、低 GC 延迟和编译期类型安全特性，在东京服务器上实现亚毫秒级交易延迟。系统采用事件驱动架构，各核心组件通过 channel 通信，最大化吞吐量。

## Tasks

- [x] 1. 项目结构与核心接口定义
  - [x] 1.1 初始化 Go 项目结构和模块
    - 创建 `go.mod`（module 名如 `github.com/yourname/okx-hft-grid`）
    - 创建目录结构：`cmd/`, `internal/marketdata/`, `internal/orderbook/`, `internal/strategy/`, `internal/execution/`, `internal/risk/`, `internal/ratelimiter/`, `internal/config/`, `internal/monitor/`, `internal/persistence/`, `pkg/models/`
    - 添加核心依赖：`github.com/gorilla/websocket`, `github.com/shopspring/decimal`, `github.com/mattn/go-sqlite3`, `github.com/prometheus/client_golang`
    - _Requirements: 12.6, 10.2_

  - [x] 1.2 定义核心数据模型和枚举类型
    - 在 `pkg/models/` 中实现 `TickData`, `Order`, `Position`, `GridConfig`, `MeanReversionConfig`, `RiskLimits`, `GridLevel` 结构体
    - 定义 `OrderStatus`, `Side`, `OrderType`, `GridType`, `MAType`, `SignalDirection` 枚举
    - 使用 `shopspring/decimal` 库避免浮点精度问题
    - _Requirements: 3.1, 3.2, 5.1, 6.1_

  - [x] 1.3 定义核心组件接口
    - 在 `internal/` 各子包中定义 `MarketDataEngine`, `OrderBookManager`, `StrategyEngine`, `OrderExecutionEngine`, `RiskManager`, `RateLimiter` 接口
    - 定义事件类型：`MarketEvent`, `FillEvent`, `OrderUpdateEvent`
    - 定义回调接口和 channel 类型
    - _Requirements: 1.6, 2.3, 6.1, 7.1_

- [x] 2. 配置管理与安全凭证
  - [x] 2.1 实现配置加载和校验模块
    - 在 `internal/config/` 中创建 `config.go`，支持 YAML 配置文件加载
    - 实现 `GridConfig` 校验（upperPrice > lowerPrice, gridCount in [3,500], orderSize >= 交易所最小值）
    - 实现 `MeanReversionConfig` 校验（entryThreshold > exitThreshold, entryThreshold in [1.0,5.0], exitThreshold in [0.1,2.0], lookbackPeriod in [10,500]）
    - 实现 `RiskLimits` 校验（所有值为正数）
    - 配置校验失败时返回详细错误信息
    - _Requirements: 3.9, 5.10, 5.11, 9.5, 9.6, 9.7_

  - [x] 2.2 实现安全凭证管理
    - 从环境变量读取 API key、secret key、passphrase
    - 启动时校验所有凭证非空，缺失时拒绝启动并输出错误信息
    - 检测是否以 root 运行，配置文件权限是否超过 600
    - 实现日志脱敏函数：匹配 credential 模式的字符串替换为 8 个 `*`
    - _Requirements: 10.1, 13.3, 13.4, 13.7_

  - [x] 2.3 编写配置校验属性测试
    - **Property 19: Configuration Validation**
    - **Validates: Requirements 3.9, 5.10, 5.11, 9.5, 9.6, 9.7**

  - [x] 2.4 编写凭证启动校验属性测试
    - **Property 34: Credential Startup Validation**
    - **Validates: Requirement 13.7**

  - [x] 2.5 编写日志脱敏属性测试
    - **Property 24: Log Sanitization**
    - **Validates: Requirement 13.4**

- [x] 3. 速率限制器实现
  - [x] 3.1 实现令牌桶速率限制器
    - 在 `internal/ratelimiter/` 中创建 `token_bucket.go`
    - 每个 OKX API 端点维护独立令牌桶（约 20 requests/2s）
    - 实现 `TryAcquire()` 方法：无令牌时阻塞等待最多 5 秒
    - 超时返回错误（包含端点名称和下一个可用令牌时间）
    - 支持配置不同端点的限额
    - _Requirements: 8.1, 8.2, 8.3, 8.4, 8.5, 8.6_

  - [x] 3.2 编写速率限制器属性测试
    - **Property 18: Rate Limiter Invariant**
    - **Validates: Requirements 8.1, 8.2, 8.3, 8.5, 8.6**

- [x] 4. 行情数据引擎
  - [x] 4.1 实现 WebSocket 连接管理
    - 在 `internal/marketdata/` 中创建 `ws_client.go`
    - 实现 OKX WebSocket 连接（wss://ws.okx.com:8443/ws/v5/public）
    - 实现心跳机制（30 秒超时检测）
    - 实现指数退避重连（初始 1s，最大 60s）
    - 连接成功后自动订阅配置的 ticker/depth/trades 频道
    - 断连时标记数据为 STALE，暂停新订单生成
    - 重连成功后重新订阅、请求全量快照、验证 sequenceId 连续性
    - _Requirements: 1.1, 1.7, 1.8, 1.9, 14.1_

  - [x] 4.2 实现行情数据解析与校验
    - 创建 `parser.go`：解析 OKX JSON 行情数据为 `TickData` 结构体
    - 校验规则：lastPrice/bestBid/bestAsk > 0 且 <= 99,999,999.99
    - 校验 bestBid < bestAsk（无交叉盘口）
    - 校验 timestamp 在服务器时间 ±5 秒范围内
    - 校验 sequenceId 单调递增（丢弃乱序数据并记录警告）
    - 校验失败丢弃整条数据，发出 DATA_INVALID 事件，递增失败计数器
    - 目标：解析+校验在 100μs 内完成（p99）
    - _Requirements: 1.2, 1.3, 1.4, 1.5, 9.1, 9.2, 9.3, 9.4, 12.1_

  - [x] 4.3 实现事件分发机制
    - 创建 `dispatcher.go`：通过 Go channel 将有效行情通知 OrderBookManager 和 StrategyEngine
    - 注册/注销回调处理器
    - 分发延迟目标 < 50μs
    - _Requirements: 1.6, 12.1_

  - [x] 4.4 编写 Tick 数据校验属性测试
    - **Property 1: Tick Data Validation Completeness**
    - **Validates: Requirements 1.3, 1.4, 1.5, 9.1, 9.2, 9.3, 9.4**

- [x] 5. 订单簿管理器
  - [x] 5.1 实现本地订单簿数据结构
    - 在 `internal/orderbook/` 中创建 `orderbook.go`
    - 使用排序切片或红黑树维护 bids（降序）和 asks（升序）
    - 实现全量快照替换：`ApplySnapshot()`
    - 实现增量更新：`ApplyDelta()`（插入新价位、更新数量、删除 quantity=0 的价位）
    - 实现 sequenceId 校验：不连续时丢弃本地簿并请求全量
    - _Requirements: 2.1, 2.2, 2.9, 2.10_

  - [x] 5.2 实现订单簿查询与衍生指标
    - 实现 `GetMidPrice()`: (bestBid + bestAsk) / 2，精度 8 位小数
    - 实现 `GetSpread()`: bestAsk - bestBid，非负，精度 8 位小数
    - 实现 `GetVWAP(side, quantity)`: 从最优价开始逐档累积，返回加权均价
    - bid/ask 为空时返回错误
    - VWAP 深度不足时返回错误及最大可填充量
    - _Requirements: 2.3, 2.4, 2.5, 2.6, 2.7_

  - [x] 5.3 实现订单簿异常检测
    - 检测交叉盘口（bestBid >= bestAsk）
    - 检测单次更新深度变化超过 50%
    - 异常时记录日志并在 1 秒内请求全量重同步
    - 重同步期间丢弃该 symbol 的所有增量更新
    - _Requirements: 2.8, 2.10_

  - [x] 5.4 编写订单簿增量更新属性测试
    - **Property 26: Order Book Incremental Update Correctness**
    - **Validates: Requirements 2.1, 2.2**

  - [x] 5.5 编写订单簿 Mid Price/Spread 属性测试
    - **Property 22: Order Book Mid Price and Spread**
    - **Validates: Requirements 2.3, 2.5**

  - [x] 5.6 编写 VWAP 计算属性测试
    - **Property 27: Order Book VWAP Calculation**
    - **Validates: Requirement 2.6**

  - [x] 5.7 编写序列号间隙检测属性测试
    - **Property 28: Order Book Sequence Gap Detection**
    - **Validates: Requirements 2.9, 2.10**

  - [x] 5.8 编写订单簿异常检测属性测试
    - **Property 29: Order Book Anomaly Detection**
    - **Validates: Requirement 2.8**

- [x] 6. Checkpoint - 基础设施验证
  - Ensure all tests pass, ask the user if questions arise.

- [ ] 7. 网格策略核心逻辑
  - [-] 7.1 实现网格价位计算
    - 在 `internal/strategy/` 中创建 `grid.go`
    - 实现等差网格：step = (upperPrice - lowerPrice) / gridCount
    - 实现等比网格：ratio = (upperPrice / lowerPrice) ^ (1 / gridCount)
    - 生成 gridCount + 1 个价位，严格升序
    - 首价 = lowerPrice，末价 ≈ upperPrice（容差 1e-9 × priceRange）
    - 使用 `decimal` 库确保精度
    - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5_

  - [x] 7.2 实现网格订单布局
    - 实现 `PlaceGridOrders(levels, currentPrice, config)`
    - 当前价格下方放 BUY 订单，上方放 SELL 订单
    - 等于当前价格的格位不放订单
    - 所有订单使用 POST_ONLY 类型
    - 确保每个格位不会同时有买单和卖单
    - POST_ONLY 被拒绝时重试 3 次（间隔 1000ms）
    - _Requirements: 3.6, 3.7, 3.8, 3.10_

  - [x] 7.3 实现网格成交处理
    - 实现 `HandleGridFill(fill, gridState, config)`
    - BUY 成交 → 在上一格放 SELL（quantity 相同）
    - SELL 成交 → 在下一格放 BUY（quantity 相同）
    - 利润检查：net profit > 双边手续费时才放 counter order
    - SELL 成交时更新 realizedPnL = (fill_price - vwap_buy_price) * quantity
    - 边界检查：最高格 SELL 成交不放新单，最低格 BUY 成交不放新单
    - 持仓限制：counter BUY 超过 maxPosition 时跳过
    - 下单失败重试 3 次（间隔 1s），失败后标记需人工干预
    - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 4.6, 4.7_

  - [x] 7.4 编写网格价位单调性属性测试
    - **Property 2: Grid Level Monotonicity and Structure**
    - **Validates: Requirements 3.3, 3.4, 3.5**

  - [x] 7.5 编写等差网格间距属性测试
    - **Property 3: Grid Arithmetic Equal Intervals**
    - **Validates: Requirement 3.1**

  - [x] 7.6 编写等比网格比率属性测试
    - **Property 4: Grid Geometric Equal Ratios**
    - **Validates: Requirement 3.2**

  - [x] 7.7 编写网格订单方向属性测试
    - **Property 5: Grid Order Direction Consistency**
    - **Validates: Requirements 3.6, 3.7**

  - [x] 7.8 编写网格互斥属性测试
    - **Property 6: Grid Level Mutual Exclusion**
    - **Validates: Requirement 3.8**

  - [x] 7.9 编写网格成交 counter-order 属性测试
    - **Property 7: Grid Fill Counter-Order Placement**
    - **Validates: Requirements 4.1, 4.2, 4.6**

  - [x] 7.10 编写网格利润保证属性测试
    - **Property 8: Grid Profit Guarantee**
    - **Validates: Requirement 4.3**

  - [x] 7.11 编写网格持仓上限属性测试
    - **Property 9: Grid Position Bound**
    - **Validates: Requirement 4.5**

  - [x] 7.12 编写网格已实现盈亏属性测试
    - **Property 33: Grid Realized PnL Calculation**
    - **Validates: Requirement 4.4**

- [ ] 8. 均值回归策略
  - [-] 8.1 实现移动平均计算
    - 在 `internal/strategy/` 中创建 `mean_reversion.go`
    - 实现 SMA：sum(prices) / length
    - 实现 EMA：alpha = 2/(lookbackPeriod+1)，结果在 [MIN(prices), MAX(prices)] 之间
    - 实现 VWAP：按成交量加权均价
    - 使用环形缓冲区存储价格历史
    - _Requirements: 5.1, 5.2_

  - [x] 8.2 实现 Z-Score 计算与信号生成
    - 计算 Z_Score = (currentPrice - mean) / stdDev
    - stdDev = 0 时抑制信号生成，保留前一信号状态
    - 数据不足 lookbackPeriod 时抑制信号
    - Z_Score < -entryThreshold → BUY 信号
    - Z_Score > +entryThreshold → SELL 信号
    - |Z_Score| < exitThreshold → CLOSE 信号
    - 实现冷却期控制（cooldownMs 范围 [100, 60000]）
    - _Requirements: 5.3, 5.4, 5.5, 5.6, 5.7, 5.8, 5.9_

  - [x] 8.3 编写均值回归信号方向属性测试
    - **Property 10: Mean Reversion Signal Direction**
    - **Validates: Requirements 5.3, 5.4, 5.6, 5.7, 5.8**

  - [x] 8.4 编写 EMA 有界性属性测试
    - **Property 11: EMA Boundedness**
    - **Validates: Requirement 5.2**

  - [x] 8.5 编写信号冷却期属性测试
    - **Property 12: Signal Cooldown Enforcement**
    - **Validates: Requirement 5.9**

- [ ] 9. 订单执行引擎
  - [-] 9.1 实现订单状态机
    - 在 `internal/execution/` 中创建 `order_manager.go`
    - 实现状态转换：PENDING → SUBMITTED → OPEN → (PARTIALLY_FILLED →) FILLED | CANCELLED | EXPIRED
    - 非法转换时拒绝并返回错误，保留当前状态不变
    - 处理 OKX WebSocket 推送的订单更新
    - API 拒绝时设置状态为 REJECTED 并记录原因
    - _Requirements: 6.1, 6.2, 6.3_

  - [x] 9.2 实现 REST API 调用与重试机制
    - 创建 `api_client.go`：封装 OKX REST API 调用
    - 实现 HMAC-SHA256 请求签名（timestamp 在 30 秒内）
    - TLS 1.2+ 且验证服务器证书（验证失败不发送凭证）
    - HTTP 429 指数退避重试（初始 100ms，翻倍，最大 5000ms，最多 3 次）
    - HTTP 5xx 重试（最多 3 次，间隔 100ms）
    - 重试耗尽标记订单为 ERROR 并发出失败事件
    - _Requirements: 6.5, 6.6, 6.7, 13.1, 13.2, 13.6_

  - [x] 9.3 实现批量下单与对账
    - 实现 `BatchPlaceOrders()`：每批最多 20 单，返回逐单结果
    - 实现 60 秒定期对账：查询交易所订单/持仓状态
    - 发现不一致以交易所为准更新本地状态
    - 对账失败时记录日志，下一周期重试，不修改本地状态
    - _Requirements: 6.4, 6.8, 6.9_

  - [ ] 9.4 编写订单状态机属性测试
    - **Property 13: Order State Machine Validity**
    - **Validates: Requirements 6.1, 6.2**

  - [ ] 9.5 编写批量下单大小限制属性测试
    - **Property 30: Batch Order Size Limit**
    - **Validates: Requirement 6.4**

  - [ ] 9.6 编写对账交易所权威性属性测试
    - **Property 31: Reconciliation Exchange Authority**
    - **Validates: Requirement 6.8**

  - [x] 9.7 编写 HMAC-SHA256 签名属性测试
    - **Property 25: HMAC-SHA256 Request Signing**
    - **Validates: Requirement 13.2**

- [ ] 10. Checkpoint - 核心交易逻辑验证
  - Ensure all tests pass, ask the user if questions arise.

- [x] 11. 风控管理器
  - [x] 11.1 实现风控规则检查引擎
    - 在 `internal/risk/` 中创建 `risk_manager.go`
    - 实现单币种持仓限额检查（position qty × market price > maxPositionPerSymbol → 拒绝）
    - 实现总投资组合限额检查（sum(abs(notional)) > maxTotalPosition → 拒绝）
    - 实现日亏损限额检查（dailyPnL < -maxDailyLoss → 拒绝所有后续订单直到次日）
    - 实现下单频率检查（1 秒滑动窗口内达到 maxOrdersPerSecond → 拒绝）
    - 实现挂单数量检查（symbol 挂单数 >= maxOpenOrders → 拒绝）
    - 实现最小价差检查（spread < minSpreadBps → 拒绝）
    - 风控检查目标延迟 < 50μs（p99）
    - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 12.3_

  - [x] 11.2 实现紧急停止机制
    - 日亏损低于 -emergencyStopLoss 时触发 Emergency_Stop
    - 1 秒内：撤销所有挂单、停止所有策略、发送 CRITICAL 告警
    - Emergency_Stop 激活期间拒绝所有交易操作
    - 继续评估风控规则并报告具体违规原因
    - 收到手动确认后解除 Emergency_Stop
    - _Requirements: 7.7, 7.8, 7.9, 7.10_

  - [x] 11.3 实现极端行情检测
    - 检测 1 分钟内价格变化超过 5%
    - 检测 spread 超过滚动 5 分钟平均 spread 的 3 倍
    - 触发时：撤销所有网格订单、暂停均值回归策略、发送告警
    - _Requirements: 14.4_

  - [x] 11.4 编写风控持仓限额属性测试
    - **Property 14: Risk Check Position Limit**
    - **Validates: Requirements 7.1, 7.2**

  - [x] 11.5 编写日亏损限额属性测试
    - **Property 15: Risk Check Daily Loss Limit**
    - **Validates: Requirement 7.3**

  - [x] 11.6 编写下单频率限制属性测试
    - **Property 16: Risk Check Order Rate Limit**
    - **Validates: Requirement 7.4**

  - [x] 11.7 编写紧急停止属性测试
    - **Property 17: Emergency Stop Irreversibility**
    - **Validates: Requirements 7.7, 7.8, 7.9**

  - [x] 11.8 编写挂单数和价差限制属性测试
    - **Property 32: Risk Check Open Orders and Spread Limits**
    - **Validates: Requirements 7.5, 7.6**

  - [x] 11.9 编写极端行情检测属性测试
    - **Property 23: Extreme Market Condition Detection**
    - **Validates: Requirement 14.4**

- [ ] 12. 数据持久化层
  - [x] 12.1 实现 SQLite 状态持久化
    - 在 `internal/persistence/` 中创建 `store.go`
    - 使用 SQLite + WAL 模式存储订单状态、持仓数据、策略状态
    - 状态变更后 1 秒内触发持久化（write-ahead logging 保证持久性）
    - 启动时加载持久化状态，与交易所对账后恢复
    - 损坏检测：无法读取时停止交易并通知运维，保留损坏文件
    - _Requirements: 10.2, 10.3, 10.5_

  - [x] 12.2 实现环形缓冲区时序数据存储
    - 在 `internal/persistence/` 中创建 `ring_buffer.go`
    - 使用 memory-mapped file 或内存环形缓冲区
    - 可配置容量（默认 1,000,000 records/instrument）
    - 满时覆盖最旧数据
    - 支持按时间范围查询
    - _Requirements: 10.4_

  - [ ] 12.3 编写状态持久化 round-trip 属性测试
    - **Property 20: State Persistence Round-Trip**
    - **Validates: Requirements 10.2, 10.3**

  - [ ] 12.4 编写环形缓冲区容量属性测试
    - **Property 21: Ring Buffer Capacity Bound**
    - **Validates: Requirement 10.4**

- [ ] 13. 监控与告警系统
  - [x] 13.1 实现 Prometheus 指标暴露
    - 在 `internal/monitor/` 中创建 `metrics.go`
    - 暴露指标：order_latency_ms（直方图）、orders_per_second（计数器）、error_rate（比率）
    - 更新间隔 ≤ 5 秒
    - 提供 `/metrics` HTTP 端点
    - _Requirements: 11.1_

  - [x] 13.2 实现告警通知系统
    - 创建 `alerter.go`：支持 Telegram 和 Discord bot 推送
    - Emergency_Stop 触发时 5 秒内发送 CRITICAL 告警（含触发原因和组合状态摘要）
    - 对账发现无法自动修复的差异时 30 秒内发送 WARNING 告警
    - 通知渠道不可达时重试 3 次（间隔 10s），仍失败则本地记录 CRITICAL 日志
    - _Requirements: 11.2, 11.3, 11.5_

  - [x] 13.3 实现结构化日志系统
    - 创建 `logger.go`：结构化 JSON 日志输出到 stdout（供 journald 收集）
    - 每条交易日志包含：timestamp(ms精度)、action_type、instrument、quantity、price、order_id、result
    - 日志保留最少 30 天
    - 集成日志脱敏（task 2.2 实现的脱敏函数）
    - _Requirements: 11.4, 13.4_

  - [ ] 13.4 编写交易日志完整性属性测试
    - **Property 35: Trading Action Log Completeness**
    - **Validates: Requirement 11.4**

- [ ] 14. Checkpoint - 风控和监控验证
  - Ensure all tests pass, ask the user if questions arise.

- [x] 15. 策略引擎与组件集成
  - [x] 15.1 实现策略引擎调度器
    - 在 `internal/strategy/` 中创建 `engine.go`
    - 管理 Grid 和 MeanReversion 策略实例生命周期（加载/启动/停止）
    - 将行情事件路由到对应策略
    - 汇总策略信号提交风控审批
    - 追踪每策略盈亏和状态
    - 信号计算目标 < 500μs（p99）
    - _Requirements: 12.2_

  - [x] 15.2 实现故障恢复与重启流程
    - 在 `cmd/` 中创建 `main.go`：系统入口和初始化流程
    - 启动时加载持久化状态 → 连接交易所 → 对账 → 60 秒内完成恢复
    - 对账期间交易所不可达超过 60 秒 → 停止交易并通知运维
    - 交易所重新可达时立即恢复对账
    - 实现 WebSocket 断连后的状态验证（本地订单与交易所比对）
    - 重连成功后全量快照 + 状态验证完成后才恢复策略
    - _Requirements: 10.3, 10.6, 10.7, 14.1, 14.2, 14.3, 14.5_

  - [x] 15.3 实现 systemd 服务配置
    - 创建 `deploy/okx-hft-grid.service` systemd unit 文件
    - 配置：非 root 用户运行、崩溃后重启（5s 延迟，60s 内最多 3 次）
    - 日志输出到 journald
    - 环境变量文件引用（存放 API 凭证）
    - _Requirements: 13.3, 14.6_

  - [x] 15.4 集成端到端交易循环
    - 连接所有组件：MarketData → OrderBook → StrategyEngine → RiskManager → OrderExecution
    - 实现主事件循环（goroutine + channel 架构）
    - 确保端到端延迟 < 2ms（p99）
    - 延迟超过 2ms 时记录违规日志
    - 支持最多 100 个活跃交易对，总内存 < 2GB
    - _Requirements: 12.4, 12.5, 12.6, 12.7_

  - [x] 15.5 编写端到端集成测试
    - 使用模拟 OKX 服务器测试完整交易循环
    - 覆盖：行情接收 → 策略计算 → 风控 → 下单 → 成交 → counter order
    - 覆盖：断连重连恢复流程
    - _Requirements: 12.4, 14.1, 14.3_

- [ ] 16. Final checkpoint - 全系统验证
  - Ensure all tests pass, ask the user if questions arise.

## Notes

- Tasks marked with `*` are optional and can be skipped for faster MVP
- Each task references specific requirements for traceability
- Checkpoints ensure incremental validation
- Property tests validate universal correctness properties from the design document
- Unit tests validate specific examples and edge cases
- Go 实现使用 `gopkg.in/yaml.v3` 解析配置，`github.com/shopspring/decimal` 处理精确数值计算
- 属性测试建议使用 `github.com/flyingmutant/rapid` 或 `pgregory.net/rapid` 库
- 系统通过 goroutines + channels 实现事件驱动并发，避免锁竞争

## Task Dependency Graph

```json
{
  "waves": [
    { "id": 0, "tasks": ["1.1"] },
    { "id": 1, "tasks": ["1.2", "1.3"] },
    { "id": 2, "tasks": ["2.1", "2.2", "3.1"] },
    { "id": 3, "tasks": ["2.3", "2.4", "2.5", "3.2"] },
    { "id": 4, "tasks": ["4.1", "4.2", "4.3"] },
    { "id": 5, "tasks": ["4.4", "5.1"] },
    { "id": 6, "tasks": ["5.2", "5.3"] },
    { "id": 7, "tasks": ["5.4", "5.5", "5.6", "5.7", "5.8"] },
    { "id": 8, "tasks": ["7.1", "8.1", "9.1"] },
    { "id": 9, "tasks": ["7.2", "7.3", "8.2", "9.2"] },
    { "id": 10, "tasks": ["7.4", "7.5", "7.6", "7.7", "7.8", "7.9", "7.10", "7.11", "7.12", "8.3", "8.4", "8.5", "9.3"] },
    { "id": 11, "tasks": ["9.4", "9.5", "9.6", "9.7", "11.1"] },
    { "id": 12, "tasks": ["11.2", "11.3"] },
    { "id": 13, "tasks": ["11.4", "11.5", "11.6", "11.7", "11.8", "11.9"] },
    { "id": 14, "tasks": ["12.1", "12.2", "13.1", "13.2", "13.3"] },
    { "id": 15, "tasks": ["12.3", "12.4", "13.4"] },
    { "id": 16, "tasks": ["15.1", "15.2", "15.3"] },
    { "id": 17, "tasks": ["15.4"] },
    { "id": 18, "tasks": ["15.5"] }
  ]
}
```
