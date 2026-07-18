# Production Grid Stabilization Bugfix Requirements

## Introduction

本文档定义 OKX 现货网格 bot 的生产稳定性修复需求，适用于 AWS EC2 新加坡部署上的 `DOGE-USDT` 与 `WIF-USDT`。本 spec 以现有 `okx-altcoin-hft-grid` 和 `grid-drift` spec 为基线；对于本次范围，部署地点由旧文档中的东京变更为新加坡，任何仍指向东京的部署假设、IP 白名单说明或运维记录均须更新。现有代码中声称已实现的多币种、动态资金分配、自适应范围、私有 WebSocket 成交处理、BUY 成交后的 counter SELL、网格漂移、启动撤旧单和 30 秒 rebalancer，均视为待验证的既有能力，而非已通过验收的事实。

本次修复范围仅包含生产网格链路的可恢复性、一致性、可观测性和安全边界。均值回归策略明确不在本次范围内，不得因本修复被修改、启用或纳入生产验收。

本文使用以下需求术语：**Private_WS** 指 OKX 私有 WebSocket 订单/成交通道；**Unique_Fill** 指由交易所提供的稳定成交标识以及所属订单、交易对和新增累计成交量共同确定的唯一成交事实；**Counter_SELL** 指合格 BUY 成交后用于覆盖该新增成交量的反向 SELL 网格单；**Current_Reference_Price** 指 rebalancer 使用并记录的、经过校验且数据年龄不超过 5 秒的 OKX ticker `last`；**Production_State_Directory** 指由 systemd 为服务用户 `okxtrader` 创建并授权的绝对路径 `/var/lib/okx-hft-grid`；**Stale_Order** 指 `ABS(orderPrice - Current_Reference_Price) / Current_Reference_Price > 0.02` 的 bot 自有未完成网格单，恰好等于 2% 不属于 Stale_Order；**Safe_Stop** 指在状态不可信时停止新增风险敞口，只允许经确认的风险降低操作，并发出可观测告警；**Healthy** 指服务进程、Production_State_Directory、配置、行情、Private_WS、订阅和最近一次对账均满足要求的可机器判定状态。

### Approved Decisions

以下决策已经批准，是本 Requirements 的验收基线，后续不得再以不同默认值解释：

- Counter_SELL 必须在观察到合格 BUY fill 后 5 秒内发起，并自同一观察时点起 15 秒内得到交易所确认状态或明确的安全失败终态。
- Private_WS heartbeat 为 20 秒；距最后一次可验证存活最迟 45 秒必须判定失活，并在判定后 5 秒内开始重连。
- REST fill/order 周期对账间隔为 30 秒；服务启动、Private_WS 重连、消息缺口时立即执行，不等待下一周期。
- Rebalancer 的 Current_Reference_Price 使用经过校验且年龄不超过 5 秒的 ticker `last`。
- Counter_SELL 最终失败默认只 Safe-Stop 对应交易对：停止新增 BUY、尽力撤销其剩余 BUY、保留风险降低 SELL；仅当共享依赖不可信时全局停止新增风险。
- 生产状态目录为 systemd 管理的 `/var/lib/okx-hft-grid`，服务用户为 `okxtrader`；具体 systemd 目录指令和迁移机制留给 Design 决定。
- 告警渠道可配置，journald 是任何配置或外部渠道状态下都不可禁用的强制兜底。
- 自适应范围 1.5%-4% 表示以当前参考价为中心、上下每一侧各自采用的半宽，而不是上下界各取不同百分比或总宽度。

**Bug Condition C(X)：**

```text
FUNCTION isBugCondition(X)
  INPUT: X of type ProductionGridObservation
  OUTPUT: boolean

  RETURN (X.hasEligibleUniqueBuyFill
          AND (NOT X.hasExactlyOneEligibleCounterSell
               OR X.counterSellInitiationLatency > 5 seconds
               OR (X.elapsedSinceFillObserved >= 15 seconds
                   AND NOT X.hasExchangeConfirmedCounterSellStateOrSafeFailureTerminalState)))
      OR (X.privateWSIsDisconnectedOrHalfOpen
          AND NOT X.detectedWithin45SecondsAndStartedReconnectWithin5Seconds)
      OR (X.isStartupReconnectOrGapObservation
          AND NOT X.immediateRESTFillOrderReconciliationStarted)
      OR (X.elapsedSinceLastPeriodicRESTFillOrderReconciliation >= 30 seconds
          AND NOT X.nextReconciliationStarted)
      OR (X.hasBotOwnedOpenOrderWithDeviationGreaterThanTwoPercent
          AND X.elapsedSinceEligibleRebalanceCycle >= 30 seconds
          AND NOT X.hasTerminalCancelAndReplacementOutcome)
      OR (X.serviceExpectedToRun
          AND (NOT X.productionStateDirectoryWritableOrAuthorized OR NOT X.serviceHealthy))
      OR X.runtimeBehaviorDependsOnManualSedMutation
      OR X.orderDoesNotMeetCurrentInstrumentPrecision
      OR X.fillOrderOrFailureStateLeaksAcrossSymbols
      OR (X.criticalStateIsUncertain AND X.systemContinuesIncreasingRisk)
END FUNCTION
```

**Expected Property P(result)：**

```text
FUNCTION expectedBehavior(result)
  INPUT: result of type ProductionGridOutcome
  OUTPUT: boolean

  RETURN result.fillCoverageIsCompleteIdempotentAndWithinDeadline
     AND result.privateWSRecoveryIsDetectableReconciledAndWithinDeadline
     AND result.periodicRESTReconciliationIntervalIs30Seconds
     AND result.rebalancerUsesFreshValidatedTickerLastAndProducesAuditableTerminalOutcomes
     AND result.productionStateIsRecoverableOrFailsClosed
     AND result.ordersUseCashModeAndValidInstrumentPrecision
     AND result.symbolStateAndFailureScopeAreIsolated
     AND result.logsMetricsAndAlertsAreSanitizedAndObservable
END FUNCTION

FOR ALL X WHERE isBugCondition(X) DO
  ASSERT expectedBehavior(FIXED(X))
END FOR

FOR ALL X WHERE NOT isBugCondition(X) DO
  ASSERT ORIGINAL(X) = FIXED(X)
END FOR
```

自动化验收必须在本地、隔离环境、模拟 OKX 服务、历史事件回放或 OKX 模拟交易环境中完成；不得连接生产 EC2、不得使用生产凭证、不得下达或撤销真实资金订单。生产启用只能在自动化验收通过、凭证轮换和最小权限前置条件满足后，由人工明确批准。

## Bug Analysis

### Current Behavior (Defect)

当前缺陷横跨成交闭环、Private_WS 恢复、订单重平衡和服务生命周期。缺少 Counter_SELL 可能来自 Private_WS 断线漏失 fill、服务未运行、重复事件处理不确定或下单失败不可见；旧订单不重挂以及状态目录不可写后服务退出则进一步放大未覆盖仓位风险。

**Current defect evidence（只读观察，仅作为待测试的根因候选）：**

- 在 `internal/execution/fill_handler.go` 的当前 fill 回调路径中，只观察到一次 `PlaceOrder` 调用；发生调用错误或拒绝后函数即返回。在该路径中未观察到有界重试、持久化 fill 去重或 REST 补偿接入。这些事实支持 1.1、1.3-1.5 的根因候选，但在探索测试完成前不得表述为已证实根因。
- 在 `cmd/main.go` 的当前 rebalancer 中，观察到 30 秒 ticker、通过 REST ticker `last` 获取参考价，并使用严格 `> 2%` 判断；同时，取消请求返回后未观察到对交易所取消终态的明确查询确认，重挂调用错误或拒绝时会静默 `continue`。这些事实支持 1.6-1.7 的根因候选，但不替代运行时反例和交易所状态测试。
- 在 `deploy/okx-hft-grid.service` 中，观察到 `User=okxtrader`、`WorkingDirectory=/opt/okx-hft-grid` 和 `ProtectSystem=strict`，但未声明 `ReadWritePaths=` 或 `StateDirectory=`。该组合可能阻止当前相对状态路径写入，是 1.8-1.9 的根因候选；是否实际触发必须在隔离环境验证，具体 systemd 机制留给 Design。

1.1 WHEN `DOGE-USDT` 或 `WIF-USDT` 的 bot 自有 BUY 单产生 Unique_Fill，但 Private_WS 事件丢失、处理器未运行或服务处于 inactive/dead THEN 当前部署可能不产生可观察的 Counter_SELL，使新增现货仓位未被网格卖单覆盖。

1.2 WHEN Private_WS 半开、静默断线或不再传递有效消息且没有正常 close 事件 THEN 依赖手工将超时改为 86400 秒的部署可能长期不能判定断线、重连或恢复订阅，同时进程仍可能表面存活。

1.3 WHEN Private_WS 在 BUY fill 前后断线、服务重启或订阅存在时间缺口 THEN 当前行为没有可验证保证会通过 REST 订单/成交对账发现并补偿漏失 fill，遗漏的 Counter_SELL 可跨重连或重启持续存在。

1.4 WHEN OKX 重放、重复、乱序发送同一成交更新，或 REST 对账再次返回已由 Private_WS 处理的 Unique_Fill THEN 当前行为没有可验证的持久化去重和幂等保证，可能重复创建 Counter_SELL 或无法证明成交覆盖量正确。

1.5 WHEN Counter_SELL 或 rebalancer 的取消/重挂请求被 OKX 拒绝、网络结果不确定或重试耗尽 THEN 当前日志和状态可能不足以确定拒绝原因、最终订单状态以及系统是否仍在增加该币种风险。

1.6 WHEN 健康运行期间到达 30 秒 rebalancer 周期，且 bot 自有未完成订单相对新鲜 Current_Reference_Price 的偏离超过 2% THEN 当前部署未可靠撤销并重挂该 Stale_Order，也未提供每个旧单的交易所确认终态证据。

1.7 WHEN Stale_Order 在取消过程中部分成交、已成交、已撤销，或取消响应超时但交易所实际状态未知 THEN 当前 rebalancer 可能在未确认旧单终态前重挂，造成重复挂单、成交遗漏或本地/交易所状态不一致；重挂失败还可能缺少可观察结果。

1.8 WHEN 当前部署在 `ProtectSystem=strict` 的 systemd sandbox 中尝试向相对状态路径写入，或目标状态目录不存在、所有权错误、未授权给 `okxtrader` 或不可写 THEN 当前程序可能因无法写日志或数据库而退出，导致 systemd 显示 inactive/dead，且交易闭环停止。

1.9 WHEN `okx-hft-grid.service` 被期望运行但进程崩溃、主机重启或服务进入 inactive/dead THEN 当前部署没有形成可验收的 enable、restart、健康检查和反复失败后告警闭环。

1.10 WHEN 部署需要 `tdMode` 从 `cross` 改为 `cash`、Private_WS heartbeat 从 25 秒改为 20 秒、自适应范围从每侧 3%-8% 改为每侧 1.5%-4%，或需要修补 Private_WS 超时 THEN 当前行为依赖部署后手工 `sed`，构建产物与实际运行行为不可复现；其中 86400 秒超时还可能掩盖断线。

1.11 WHEN 订单价格或数量不满足 `DOGE-USDT` 或 `WIF-USDT` 当前 OKX `tickSz`、`lotSz`、`minSz` 等 instrument 规则 THEN OKX 可能拒绝初始单、Counter_SELL 或 rebalancer 重挂单，使网格状态不完整。

1.12 WHEN 两个交易对并发收到 fill、执行 rebalancer 或其中一个交易对进入失败恢复 THEN 当前系统缺少可验收证据证明成交去重、订单状态、资金分配和失败范围不会跨 `DOGE-USDT` 与 `WIF-USDT` 相互污染。

1.13 WHEN 交接过程中使用过的 OKX 凭证曾被暴露 THEN 若生产部署继续使用旧凭证、过宽权限或旧地区 IP 白名单，生产资金和账户仍处于不必要风险中。

1.14 WHEN 自动化验收未明确隔离生产端点、生产 EC2 和真实账户 THEN 测试可能错误地下达、撤销或影响真实资金订单。

1.15 WHEN Production_State_Directory 不可写、Private_WS 状态不可信、REST 对账不可用、行情过期、订单终态未知或 Counter_SELL 持续失败 THEN 当前系统可能继续放置新增 BUY 或其他增加风险敞口的订单，而不是停止并告警。

1.16 WHEN 既有 spec、部署文档或白名单假设仍将运行地点标记为东京 THEN 运维人员可能对新加坡 EC2 使用错误的网络、时延、IP 白名单或故障排查前提。

1.17 WHEN 上述异常发生或恢复 THEN 当前部署缺少一组完整、按交易对区分且经过脱敏的日志、指标、健康状态和可靠告警兜底，无法仅凭可观测证据判断 fill 是否补偿、Counter_SELL 是否唯一、rebalancer 是否运行以及服务是否安全。

### Expected Behavior (Correct)

修复后的系统必须对每个缺陷条件产生确定、可观察、可审计的结果。任何“已发送请求”都不等同于成功；验收必须观察到交易所确认的订单状态或明确的安全失败终态。

2.1 WHEN `DOGE-USDT` 或 `WIF-USDT` 的 Unique_Fill 首次通过 Private_WS 或对账被系统观察到，且该 BUY fill 满足网格边界、最小数量、利润和风控规则 THEN the system SHALL 在观察到 fill 后 5 秒内发起恰好一个覆盖新增合格成交量的 Counter_SELL，并自同一观察时点起 15 秒内记录交易所确认的 accepted/open/filled 状态或明确的安全失败终态；不合格 fill 必须记录不可下单原因并按 2.5 和 2.15 进入安全行为，而不能静默遗漏。

2.2 WHEN Private_WS 半开、静默断线、异常关闭或连续两个预期心跳周期没有得到可验证存活证据 THEN the system SHALL 使用 20 秒 heartbeat，在距最后一次可验证存活不超过 45 秒内把连接标为 unhealthy，并在标记 unhealthy 后 5 秒内开始有界退避重连；重连后必须重新认证、重新订阅订单/成交通道、验证订阅确认，并在完成 2.3 对账前保持相关交易暂停。86400 秒不得作为断线检测或健康判定超时。

2.3 WHEN 服务启动、Private_WS 重连、订阅恢复或检测到消息缺口 THEN the system SHALL 立即执行 OKX REST fill/order 对账而不等待周期调度；WHEN 距上次周期对账启动达到 30 秒 THEN the system SHALL 再次执行周期对账。每次对账必须覆盖自上次已确认水位以来所有可能变化的 bot 自有订单和 fills，以交易所状态为准补齐遗漏；所有新发现 Unique_Fill 必须进入与实时 fill 相同的 Counter_SELL 流程，并输出查询窗口、检查数、补偿 fill 数、差异数和终态。启动、重连或缺口对账完成前不得恢复新增 BUY，且不得保留任何 60 秒周期解释。

2.4 WHEN 同一 Unique_Fill 通过重复/乱序 Private_WS 消息、REST 对账或进程重启后再次出现 THEN the system SHALL 以交易所成交身份、所属订单、交易对和新增累计成交量进行持久化幂等处理，使每个合格成交增量最多产生一个有效 Counter_SELL 意图；任意时刻某 BUY 单的已确认及状态明确的待定 Counter_SELL 覆盖量不得超过其累计合格成交量，恢复完成后应等于其累计合格成交量。

2.5 WHEN Counter_SELL、取消或重挂请求被拒绝、超时或结果不确定 THEN the system SHALL 记录经过脱敏的结构化事件，至少包含时间、操作、交易对、side、价格、数量、客户端/交易所订单关联标识、OKX `sCode`/`sMsg` 或等价原因、尝试次数和最终分类；日志不得包含 API key、secret、passphrase、签名、Authorization 内容或环境变量值。系统必须执行有界重试和订单状态查询；若 Counter_SELL 在 2.1 的 15 秒期限内仍不能得到交易所确认，则必须形成安全失败终态并默认仅 Safe-Stop 对应交易对：停止其新增 BUY、尽力撤销其剩余未成交 BUY、保留已确认或可确认的风险降低 SELL，并在 30 秒内通过 2.17 的告警机制发出 CRITICAL 告警。不得无界重试相同无效参数，也不得因单交易对失败默认停止另一健康交易对。

2.6 WHEN 系统 Healthy 且 rebalancer 启用 THEN the system SHALL 每 30 秒执行一次每交易对独立的检查，允许调度抖动不超过 5 秒；Current_Reference_Price 必须取自经过字段、数值和时效校验的 OKX ticker `last`，且从该 ticker 的接收或可验证交易所时间到使用时不得超过 5 秒。对每个相对该价格偏离严格大于 2% 的 bot 自有未完成网格单，系统必须在首次符合条件后的一个周期内启动取消与重挂流程。ticker 缺失、无效或过期时不得据此重平衡，并须把该周期标记为 skipped/unhealthy。

2.7 WHEN rebalancer 处理 Stale_Order THEN the system SHALL 先查询并确认旧单可取消状态，只有在交易所确认取消后才创建替代单；若取消期间发生 fill，必须先按 2.1-2.4 处理该 fill；若终态不确定，必须停止该交易对的重挂并告警。每个 Stale_Order 必须在下一周期前得到 `replaced`、`filled`、`already-cancelled`、`kept-by-rule` 或 `failed-safe` 之一的可审计终态，且同一交易对不得并发运行重叠的 rebalancer 周期。重挂调用错误或拒绝不得静默跳过。

2.8 WHEN 部署、服务启动或主机重启 THEN systemd SHALL 在任何交易连接和策略启动前创建并授权 Production_State_Directory `/var/lib/okx-hft-grid`，其规范化绝对路径必须由服务用户 `okxtrader` 实际可写；服务必须验证日志和数据库所需文件可创建、写入、同步和关闭。WHEN 旧部署存在 legacy `data/hft_state.db` THEN the system SHALL 在进入交易状态前迁移该状态或以兼容方式读取并转入 Production_State_Directory，不得静默创建空状态并遗失既有订单、fill 水位、去重记录、持仓、策略或 PnL 状态。迁移或兼容验证失败时，服务必须拒绝进入交易状态、通过 journald 输出非敏感原因并告警；运行期间持久化写入失败时必须进入 Safe_Stop。具体 systemd 目录指令及迁移实现机制由 Design 决定，本需求只约束结果。

2.9 WHEN 部署或主机重启后 `okx-hft-grid.service` 被期望运行 THEN the system SHALL 处于 enabled 状态并自动启动；WHEN 进程意外退出 THEN systemd SHALL 在约 5 秒后自动重启，并在 60 秒窗口内最多尝试 3 次。成功恢复须在 60 秒内显示 active 且完成可机器判定健康检查；反复失败须停止重启风暴、保留 journald 原因并在 30 秒内告警。健康输出必须区分 `healthy`、`degraded/reconciling` 和 `safe-stopped`，不能仅以进程存活判定可交易。

2.10 WHEN 创建全新构建产物或部署配置 THEN the system SHALL 无需任何部署后 `sed` 即得到以下受版本控制且启动时校验的有效行为：所有 OKX 现货订单使用 `tdMode=cash`；Private_WS heartbeat 为 20 秒并遵守 2.2 的有限存活判定；自适应范围使用单一的每侧半宽 `r`，其中 `0.015 <= r <= 0.04`，下界为 `Current_Reference_Price * (1 - r)`，上界为 `Current_Reference_Price * (1 + r)`。启动日志须输出脱敏后的有效配置摘要；任何 `cross` 模式、允许超过 45 秒才发现 Private_WS 失活的配置、把 1.5%-4% 解释为总宽度或不对称上下百分比的配置、或其他非法范围都必须在发单前被拒绝。

2.11 WHEN 准备初始网格单、Counter_SELL 或 rebalancer 替代单 THEN the system SHALL 使用下单前获取且仍有效的 OKX instrument 元数据校验并规范化价格和数量，使价格为 `tickSz` 的合法倍数、数量为 `lotSz` 的合法倍数且不低于 `minSz` 及适用最小名义金额；不得用二进制浮点近似绕过精度校验。若规范化后无法形成合格订单，系统必须不发送该订单、记录原因并对未覆盖 BUY fill 执行 2.5 的安全行为。

2.12 WHEN `DOGE-USDT` 与 `WIF-USDT` 并发运行 THEN the system SHALL 按交易对隔离 fill 水位、去重记录、网格状态、rebalancer 锁、订单关联、资金分配结果和失败状态；DOGE fill 不得创建 WIF 订单，反之亦然。单交易对故障仅暂停该交易对，除非 Private_WS、账户对账、持久化或组合级风控等共享依赖不可信；共享依赖故障时两个交易对都必须停止新增风险，恢复后分别对账再恢复。

2.13 WHEN 任何生产部署获准开始 THEN the system SHALL 将交接中暴露过的凭证全部撤销并轮换，并把完成轮换作为部署阻断前置条件；新凭证必须仅具现货交易所需最小权限，禁用提币和资金划转，限制到新加坡 EC2 的批准 IP，并通过环境注入或受控 secret 服务提供。任何 spec、配置样例、日志、指标、告警或验收输出都不得记录凭证值。

2.14 WHEN 执行自动化验收 THEN the system SHALL 只使用模拟/回放/隔离测试或 OKX 模拟交易能力以及非生产凭证，且必须通过测试护栏阻止生产 OKX URL、生产账户和生产 EC2；自动化验收不得产生、撤销或修改真实资金订单。生产交易启动必须是与测试分离的人工审批动作。

2.15 WHEN Production_State_Directory 不可写、Private_WS 未恢复且未对账、REST 对账不可用、Current_Reference_Price 过期、订单终态未知或 Counter_SELL 无法确认 THEN the system SHALL 进入与故障范围相匹配的 Safe_Stop，不得创建新增 BUY 或其他增加风险敞口的订单；只允许状态已确认的撤单、对账和用于覆盖已知现货仓位的风险降低 SELL。Counter_SELL 最终失败默认仅 Safe-Stop 对应交易对，并保留其风险降低 SELL；只有 Private_WS、账户对账、持久化、组合级风控或其他共享依赖不可信时，才必须全局停止所有交易对新增风险。系统须在 30 秒内告警，持续暴露阻断原因，并仅在依赖恢复且交易所对账成功后自动恢复；无法证明安全时须等待人工确认。

2.16 WHEN 生成或更新本次范围内的部署配置、运行手册、健康信息、IP 白名单说明或验收证据 THEN the system SHALL 将生产地点标记为 AWS EC2 新加坡，并明确本 spec 在部署地点上取代既有 spec 的东京假设；不得静默沿用东京 IP、时延或网络路径假设。

2.17 WHEN 服务启动、Private_WS 状态变化、fill 被接收/去重/补偿、Counter_SELL 被处理、rebalancer 运行、对账运行或 Safe_Stop 变化 THEN the system SHALL 输出按交易对标记的脱敏结构化日志和机器可读健康/指标，至少可观察：服务期望状态与实际状态、Production_State_Directory 可写性、Private_WS 最后存活时间/连接状态/重连次数/订阅状态、最近成功对账及相对 30 秒周期的 lag、补偿 fill 数、重复 fill 抑制数、Counter_SELL 发起延迟及成功/拒绝/未确认数、rebalancer 最近运行时间/Current_Reference_Price 来源与年龄/发现 Stale_Order 数/取消与重挂结果、Safe_Stop 范围与原因、告警投递结果。告警渠道必须可配置，但无论外部渠道未配置、不可用或投递失败，journald 都必须记录不可禁用的脱敏兜底告警；外部告警失败不得抑制或伪装 journald 结果。验收必须能仅凭这些信号关联一个 BUY fill 到唯一 Counter_SELL 或明确安全失败终态。

### Unchanged Behavior (Regression Prevention)

修复应最小化对非 bug 条件的影响。以下行为在满足新的安全门禁时必须保持；若它们与交易所事实或 Safe_Stop 冲突，以交易所事实和安全停止为优先。

3.1 WHEN Private_WS 健康、成交事件未丢失且订单状态可信 THEN the system SHALL CONTINUE TO 通过既有网格成交路径处理 BUY/SELL fills，并保持 SELL fill 后的既有 counter BUY 行为、网格边界规则、手续费/利润规则和持仓限制不变。

3.2 WHEN bot 自有未完成网格单相对新鲜 Current_Reference_Price 的偏离小于或等于 2% THEN the system SHALL CONTINUE TO 保留该订单，不得仅因本次 rebalancer 修复而撤销或重挂；启动清理、人工操作、成交、风控或其他既有明确规则除外。

3.3 WHEN `DOGE-USDT` 与 `WIF-USDT` 均处于 Healthy 状态 THEN the system SHALL CONTINUE TO 同时运行多币种网格和动态资金分配，并遵守现有单币种及组合级资金/风险上限，不得退化为硬编码固定分配。

3.4 WHEN 自适应范围正常计算 THEN the system SHALL CONTINUE TO 根据现有波动和价格输入调整网格范围，只把生产边界改为以 Current_Reference_Price 为中心、上下每侧相同的 1.5%-4% 半宽；既有 Grid Drift 的持仓、平均成本和已实现 PnL 保持语义不变。

3.5 WHEN 服务正常启动且发现由本 bot 可证明拥有的历史旧单 THEN the system SHALL CONTINUE TO 在建立新网格前执行启动清理和交易所对账；不得扩大范围去撤销无法通过 client/order 标识证明属于本 bot 的人工单或其他策略订单。

3.6 WHEN 公开行情 WebSocket 正常且数据有效 THEN the system SHALL CONTINUE TO 接收、校验和分发 `DOGE-USDT`、`WIF-USDT` 行情；Private_WS 稳定性修复不得改变有效行情的价格语义或把过期行情视为有效。

3.7 WHEN 合格网格订单被正常创建 THEN the system SHALL CONTINUE TO 使用既有 POST_ONLY、网格方向、盈利性和风险检查规则，并统一使用本次明确的 spot `cash` 模式及合法精度。

3.8 WHEN Production_State_Directory 中的本地持久化状态可读且与交易所一致，或 legacy `data/hft_state.db` 已完成无损迁移/兼容读取 THEN the system SHALL CONTINUE TO 保存订单、成交、持仓、策略和去重状态，并在重启时以交易所状态为权威完成恢复，不得因目录迁移或本修复而重置合法 PnL、持仓、fill 水位或去重记录。

3.9 WHEN 某一交易对发生仅限该交易对的非共享故障 THEN the system SHALL CONTINUE TO 允许另一交易对在自身 Healthy、账户级状态可信且组合风控允许时运行；不得把 DOGE 的订单、fill、计数器或 rebalancer 结果写入 WIF，反之亦然。

3.10 WHEN 均值回归配置或代码存在于仓库中 THEN the system SHALL CONTINUE TO 将均值回归排除在本次修复、部署变更和验收之外，且不得自动启用该策略。

3.11 WHEN 记录正常交易、恢复或错误事件 THEN the system SHALL CONTINUE TO 执行既有日志脱敏和凭证仅从受控来源加载的安全约束；本次增强的拒绝原因日志和 journald 告警兜底不得降低保密级别。

3.12 WHEN 执行本 spec 的任何自动化验证 THEN the system SHALL CONTINUE TO 与真实资金、生产凭证和生产订单完全隔离；人工生产审批不属于自动化测试通过后的隐式动作。
