# Implementation Plan: Production Grid Stabilization

## Scope and Execution Rules

本计划仅依据同目录已批准的 `requirements.md` 与 `design.md`，按 **Explore → Preserve → Implement → Validate** 执行。父任务 3、4 仅分组；Task Execution Orchestrator 只排队叶任务。

- Task 1 必须先在 **UNFIXED** 代码上运行并得到预期失败；Task 2 随后在 **UNFIXED** 代码上建立且通过 observation-first preservation baseline。二者完成前不得实现修复。
- 自动化验证仅可使用 loopback simulator、回放、`t.TempDir()` 或隔离 fixture，并须在 DNS/dial/request 前阻断生产端点、生产账户、生产凭证环境和 EC2 metadata。
- 明确排除：真实部署、任何 `systemctl`/EC2 操作、凭证轮换执行、真实 OKX 连接或订单、自动化生产批准，以及均值回归代码/语义修改。人工 runbook 只记录待执行门禁，不执行这些动作。
- 不允许部署后 `sed`；生产行为必须来自受版本控制且启动时严格校验的配置。
- `cmd/main.go` 的集中改动只允许在叶任务 3.10；其他任务通过组件接口和测试 seam 工作，避免同 wave 冲突。
- 下列测试命令均为后续任务的目标命令；本次重建计划不运行测试、构建或生产命令。

## Tasks

- [x] 1. Write bug condition exploration property tests on UNFIXED code
  - **Property 1: Bug Condition** - Recoverable and Idempotent Production Grid Closure
  - 只添加 PBT、fake clock/store/gateway 和 loopback simulator seam，不改生产实现。用已固定的 `pgregory.net/rapid v1.1.0` 覆盖 design EXP-01..14：重复/乱序与累计 fill、accept 后响应丢失、Private_WS half-open、立即/30 秒对账、`cross`/缺少 `clOrdId`、任意 decimal instrument rules、cancel/fill race、unowned order、legacy WAL/StateDirectory、DOGE/WIF 交错、journald fallback、生产 I/O 阻断。
  - scoped deterministic cases 必须断言 design 的 `expectedBehavior`，且“请求已发送”不算终态；记录 reproduced/refuted、seed、fake time、exchange script、observed state 和精确最小 counterexample，不得含凭证值。
  - **Expected result:** UNFIXED 上失败即本任务成功。执行 subagent 应按 orchestrator 的 bugfix special case 报告 expected failure 与精确 counterexample，不得调用或臆造不存在的 PBT status 工具名。
  - **STOP:** 一旦本任务被判为 `unexpected_pass`（整组在 UNFIXED 上未产生有效反例），立即停止整个 DAG，并请求用户选择：①检查复现/测试接缝；或 ②确认代码已先行修复并重新建立 baseline。不得继续 Task 2。
  - **Target tests:** `go test ./internal/... ./pkg/... ./cmd -run '^TestProperty1_BugCondition_' -count=1`
  - **Completion:** EXP-01..14 均已分类，至少一个有效最小反例证明 `isBugCondition(X)`，预期失败已按 special case 报告，且网络记录器证明无生产 I/O。
  - _Depends on: none_
  - _Requirements: 1.1-1.17, 2.1-2.17_

- [x] 2. Write observation-first preservation property tests on UNFIXED code
  - **Property 2: Preservation** - Healthy Grid Behavior and Non-Bug Inputs
  - 先观察并固化 design PRE-01..12：健康 BUY/SELL fill、偏离 `0..2%`、DOGE/WIF 动态分配、adaptive range/GridState、Bot_Owned cleanup、公开行情、POST_ONLY/盈利/风控、持久化 round-trip、symbol-local failure、均值回归未启用、脱敏与测试隔离。
  - comparator 只可忽略 design 允许的新 correlation ID、时间戳、durability 记录及强制 `cash`/metadata normalization；不得忽略 side、level、quantity、PnL、ownership、risk 或 symbol 差异。
  - **Expected result:** 全部 preservation properties 必须在 UNFIXED 上通过；失败时仅修 observation fixture/oracle 或回到需求澄清，不得开始实现。
  - **Target tests:** `go test ./internal/... ./pkg/... ./cmd -run '^TestProperty2_Preservation_' -count=1`
  - **Completion:** PRE-01..12 均有记录的 baseline、生成域和 comparator，UNFIXED 上全部通过，且无生产 I/O。
  - _Depends on: 1 (expected failure and counterexample documented; not `unexpected_pass`)_
  - _Requirements: 3.1-3.12_

- [x] 3. Implement the production-grid stabilization fix

  - [x] 3.1 Implement production-network guardrails and strict no-sed configuration
    - 集中实现 deny-by-default test transport/dial policy，在 DNS/dial/request 构造前拒绝生产 OKX、公网 IP、`169.254.169.254`、`.amazonaws.com`、production mode、生产凭证环境名和 `trading_enabled=true`；默认仅允许 loopback，demo 必须独立 opt-in、模拟标记与 allowlist。
    - strict known-field 配置在任何网络使用前校验 Singapore、spot `cash`、20/45/5、30 秒 reconcile/rebalance、ticker age 5 秒、对称每侧 `0.015..0.04`、绝对 state path、DOGE/WIF、production mean reversion empty 和人工 gate evidence；拒绝 `cross`、60/86400 秒、total/asymmetric width、相对路径和未知字段。有效配置摘要只用非敏感 allowlist，运行行为不得依赖 `sed`。
    - **Target tests:** `go test ./internal/config ./internal/integration -run 'Test(AutomatedValidationGuard|ForbiddenEndpoint|StrictProductionConfig|ApprovedTiming|SymmetricHalfWidth|NoSedDependency|EffectiveConfigSanitized)' -count=1`
    - **Completion:** 随机 host/IP/mode/env 组合证明禁止输入在 I/O 前失败；合法版本化配置无需热修改，非法配置在发单前失败且输出无 secret。
    - _Depends on: 2_
    - _Requirements: 2.2, 2.3, 2.6, 2.10, 2.13, 2.14, 2.16, 3.4, 3.10, 3.11, 3.12_

  - [x] 3.2 Implement durable schema, fill ledger, intent, and transactional outbox
    - 扩展 domain/store：base-10 decimal fill/order/recovery records，canonical `Unique_Fill` key，order cumulative watermark，bot lineage，reconciliation watermark，safe-stop/rebalance outcome；SQLite 使用 additive migrations、WAL、`synchronous=FULL` 和 `BEGIN IMMEDIATE`。
    - 同一事务写入 fill delta、既有 GridState/PnL 更新、deterministic Counter_Order_Intent 与 outbox；重复/乱序不二次更新。outbox 用 lease、固定 `clOrdId`、query-before-same-ID-retry 和 fake-clock 5 秒发起/15 秒确认或 Safe_Failure_Terminal；DB 事务内禁止网络 I/O。
    - **Target tests:** `go test ./pkg/models ./internal/persistence ./internal/execution -run 'Test(FillIdentity|CumulativeDelta|SchemaMigration|LedgerIntentAtomicity|OutboxLease|CrashRecovery|CounterSellDeadline)' -count=1`
    - **Completion:** duplicate/permutation/crash-point PBT 证明每个合格 delta 至多一个 intent/effect、无半提交和超额覆盖；critical commit uncertainty 可被上层识别为 shared failure。
    - _Depends on: 2_
    - _Requirements: 2.1, 2.4, 2.5, 2.8, 2.12, 2.15, 2.17, 3.1, 3.8_

  - [x] 3.3 Implement systemd StateDirectory lifecycle and WAL-aware legacy migration
    - 更新隔离 fixture 所验证的 unit：`StateDirectory=okx-hft-grid`、`StateDirectoryMode=0750`、`ReadWritePaths=/var/lib/okx-hft-grid`、`ProtectSystem=strict`、notify readiness、5 秒 restart、3/60 限流和无凭证 journald OnFailure；应用在开 socket 前执行 canonical path 与 create/write/fsync/close probe。
    - legacy `data/hft_state.db` 通过 SQLite backup API 合并 committed WAL，使用 migration lock、target-dir temp、integrity/schema/logical checks、fingerprint marker、fsync 和同文件系统 atomic rename；target 冲突、损坏、权限错误或未批准空状态一律 fail closed，保留 legacy 证据。
    - **Target tests:** `go test ./internal/persistence ./internal/integration -run 'Test(StateDirectoryUnit|WritableProbe|LegacyMigrationWithWAL|MigrationConflictFailsClosed|AtomicPublish|RestartPolicyFixture)' -count=1`
    - **Completion:** WAL-only rows 无损迁移，所有 fault point 均不发布空/半迁移 DB、不进入交易；只测试 unit/隔离 fixture，不执行 `systemctl` 或真实主机操作。
    - _Depends on: 3.1, 3.2_
    - _Requirements: 2.8, 2.9, 2.15, 2.17, 3.8_

  - [x] 3.4 Implement cash/context/structured ExchangeGateway and exact instrument rules
    - 将 API adapter 改为 context-aware typed gateway：所有 spot order 使用 `tdMode=cash` 与 deterministic `clOrdId`，支持 place/cancel/query、pending/history/fills/ticker/instruments pagination，并返回结构化 HTTP/transport/OKX `code/sCode/sMsg`；不解析拼接错误，不把 send acknowledgment 当终态。
    - `InstrumentRulesProvider` 缓存/刷新 `tickSz`、`lotSz`、`minSz` 和 authoritative `minNotional`；用 decimal integer multiples 做 BUY floor、SELL ceil、qty floor，规范化后重跑 POST_ONLY/profit/risk/minimum。初始、counter、replacement 共用 normalizer，过期/不匹配/no-send 均阻断。
    - **Target tests:** `go test ./internal/execution -run 'Test(APIClientCashMode|StructuredOKXResult|ContextDeadline|QueryByClientID|Pagination|InstrumentRules|ArbitraryDecimalMultiple|Minimums|StaleRules)' -count=1`
    - **Completion:** single/batch payload 均为 `cash` 且有稳定 ID；timeout-after-accept 可查询恢复；包括 `tickSz=0.0025` 的生成式输入从不发送非法订单，错误与日志均脱敏。
    - _Depends on: 3.1, 3.2_
    - _Requirements: 2.1, 2.3, 2.5, 2.7, 2.10, 2.11, 2.14, 2.17, 3.7, 3.11, 3.12_

  - [x] 3.5 Implement Private_WS 20/45/5 liveness and recovery state machine
    - 用可注入 clock/dialer 实现 20 秒 heartbeat、独立 watchdog、最后 Verified_Liveness 后不超过 45 秒 unhealthy、unhealthy 后 5 秒内开始有界退避重连；socket-open/任意 bytes 不刷新存活。
    - login 与 required subscription ack 必须逐项严格确认；启动、重连或 gap 时关闭 shared risk-increasing gate并立即触发 reconciliation，只有 auth/subscription 与对账成功后 Ready。
    - **Target tests:** `go test ./internal/marketdata -run 'TestPrivateWS_(Heartbeat20|HalfOpen45|ReconnectWithin5|StrictAck|GapTriggersReconcile|ReadyGate)' -count=1`
    - **Completion:** fake-time timeline 覆盖 silent peer、wrong ack、异常关闭、重连耗尽与恢复；20/45/5 边界确定且无 sleep/flaky timing，恢复前不会放行新增风险。
    - _Depends on: 3.1_
    - _Requirements: 2.2, 2.3, 2.12, 2.15, 2.17, 3.6_

  - [x] 3.6 Implement immediate/30-second reconciliation and committed watermarks
    - 统一 WS/REST observation 到同一 FillProcessor；startup、reconnect、gap、uncertain outbox 立即触发，周期按上次 cycle start 固定 30 秒。每 symbol 不重叠，运行中触发只 coalesce 一个 follow-up并记录 overrun。
    - 分页查询 bot-owned fills/orders，使用 overlap window 与 exchange-authoritative state；只有完整分页、所有 apply 与事务提交成功后推进 `(exchange_ts, stable_id)` watermark，任何 partial/parse/auth/DB failure 均不推进并进入相应 gate。
    - **Target tests:** `go test ./internal/execution -run 'Test(ReconcileImmediateTriggers|ThirtySecondSchedule|Pagination|WatermarkCommitOnly|OverlapIdempotency|TriggerCoalescing|MissedFillCompensation)' -count=1`
    - **Completion:** fake-time/分页故障 PBT 证明无 60 秒 fallback、无跳窗、失败不推进 watermark；startup/reconnect/gap 在周期前启动且补偿 fill 仅产生一个 intent。
    - _Depends on: 3.2, 3.4, 3.5_
    - _Requirements: 2.1, 2.3, 2.4, 2.5, 2.12, 2.15, 2.17, 3.1, 3.8, 3.9_

  - [x] 3.7 Implement scoped Safe_Stop, operation gating, and symbol isolation
    - 新增持久化 `TradingGate`：symbol-local failure 默认只拒绝该 symbol 的 Risk_Increasing；persistence、Private_WS、账户对账或组合风控不可信时全局拒绝。只允许已确认撤 Bot_Owned BUY、对账和不超过已知仓位的 risk-reducing SELL，并与既有 global emergency stop 组合而非替代。
    - 所有 keys/locks/watermarks/intents/allocation snapshots 带 symbol；DOGE/WIF 交错不得跨 symbol mutation。恢复必须依赖对应 dependency health、成功 reconciliation epoch；未知 effect/migration conflict 要求人工确认。
    - **Target tests:** `go test ./internal/risk ./internal/execution -run 'Test(SafeStopScope|OperationClassification|SharedFailureGlobal|SymbolIsolation|RecoveryEpoch|EmergencyStopComposition)' -count=1`
    - **Completion:** 生成式双 symbol interleavings 证明 local failure 不停健康 peer、shared uncertainty 两者停增险、任何未知状态不被误判为 risk reduction，状态可重启恢复。
    - _Depends on: 3.2, 3.4_
    - _Requirements: 2.5, 2.11, 2.12, 2.15, 2.17, 3.1, 3.3, 3.7, 3.9_

  - [x] 3.8 Implement ownership-safe rebalancer with terminal confirmation and race handling
    - 抽出 testable per-symbol Rebalancer：30 秒固定调度、jitter `<=5s`、非重叠 lock；只使用 age `<=5s` 的 validated ticker `last`，严格 `>2%` 才 stale，`<=2%` 保留。
    - 仅处理 `clOrdId + bot_orders lineage` 证明的 Bot_Owned order；cancel 前后查询 exchange terminal state，cancel/fill race 先送 FillProcessor，只有 confirmed cancelled 才按当前 occupancy、allocation、risk 与 instrument rules 创建一个 deterministic replacement。每旧单持久化 `replaced|filled|already-cancelled|kept-by-rule|failed-safe`，未知状态不重挂并 Safe_Stop。
    - **Target tests:** `go test ./internal/execution -run 'Test(RebalancerFreshTicker|StrictTwoPercentBoundary|OwnershipFilter|CancelTerminalConfirmation|CancelFillRace|NoOverlap|AuditedOutcome)' -count=1`
    - **Completion:** state-model/PBT 覆盖 partial fill、timeout-but-cancelled、already filled/cancelled、manual order 和 replacement reject；无提前/批量失控重挂、无 silent continue、每单在下一周期前有唯一终态。
    - _Depends on: 3.2, 3.4, 3.6, 3.7_
    - _Requirements: 2.6, 2.7, 2.11, 2.12, 2.15, 2.17, 3.2, 3.3, 3.5, 3.7, 3.9_

  - [x] 3.9 Implement machine health, structured observability, and journald-first alerts
    - `HealthRegistry` 固定输出 `healthy|degraded/reconciling|safe-stopped`，聚合 service/state dir、Private_WS、30 秒 reconciliation lag、fill/outbox、Counter_SELL 5/15、rebalancer reference/outcome、Safe_Stop scope 与 Singapore location；metrics 使用 bounded labels，order/fill ID 不作 label。
    - logger 对 key/value 执行敏感字段 denylist；alert 必须先同步写脱敏 `alert_raised` 到 stdout/journald sink，再异步外部投递并记录 success/failure，未配置或外部成功/失败均不能消除 journal evidence。
    - **Target tests:** `go test ./internal/monitor ./internal/integration -run 'Test(HealthStates|RequiredMetrics|StructuredSanitization|JournaldFirstAlert|ExternalAlertFailure|FillToTerminalCorrelation)' -count=1`
    - **Completion:** 仅凭 health/log/metrics 可关联 BUY fill 到唯一 Counter_SELL 或 safe terminal；secret-shaped PBT 无泄漏，外部渠道任意结果都有不可禁用 journal fallback。
    - _Depends on: 3.3, 3.5, 3.6, 3.7, 3.8_
    - _Requirements: 2.5, 2.8, 2.9, 2.12, 2.15, 2.16, 2.17, 3.9, 3.11_

  - [x] 3.10 Wire production startup and remove unsafe inline paths
    - 这是唯一允许集中修改 `cmd/main.go` 的任务。顺序固定为 strict config/guard → state probe/migration/store → observability → recovery state/gates → gateway/instrument rules → Private_WS auth/subscription → immediate reconciliation/outbox recovery → ownership-safe startup cleanup → fresh public ticker → healthy/READY → approved initial grid与 schedules。
    - 移除/替换 inline one-shot fill placement、symbol precision switch、empty reconciliation adapter、broad pending-order cancel 和 unsafe inline rebalancer；所有 risk-increasing entry（含 SELL fill 后 counter BUY）统一经过 gate，同时保留动态分配、grid/PnL/POST_ONLY 语义。production profile 不加载且不修改均值回归。
    - **Target tests:** `go test ./cmd ./internal/integration -run 'Test(StartupOrder|NoRiskBeforeReady|RecoveryBeforeInitialGrid|OwnedCleanupOnly|StartupFailureClosed|ProductionComposition)' -count=1`
    - **Completion:** fault-injected startup 在任一依赖不可信时保持 degraded/safe-stopped 且零新增风险；健康路径只在 auth/subscription、对账、outbox 与 ticker 就绪后 READY，无遗留旁路。
    - _Depends on: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 3.8, 3.9_
    - _Requirements: 2.1-2.17, 3.1-3.12_

- [x] 4. Validate and hand off

  - [x] 4.1 Run final local integration, race, and full validation
    - 先重跑 Task 1 的**同一组** Property 1（修复后应通过），再重跑 Task 2 的**同一组** Property 2（仍应通过）；不得复制或放宽测试。随后运行 local REST/Private_WS simulator、event replay、crash-restart、isolated systemd/filesystem fixture、双 symbol interleaving与全仓 race/full suites。
    - 所有 transport recorder 必须继续证明零生产 DNS/dial/request、零真实订单；失败回到拥有行为的实现任务，不以重试或扩大 comparator 掩盖竞态。执行 subagent 按 orchestrator bugfix special case 报告属性结果，不引用不存在的状态工具。
    - **Target tests:** `go test ./internal/... ./pkg/... ./cmd -run '^(TestProperty1_BugCondition_|TestProperty2_Preservation_)' -count=1`; `go test ./internal/integration/... -run 'Test(ProductionGrid|CrashRestart|SystemdFixture|NoProductionIO)' -count=1`; `go test -race ./... -count=1`; `go test ./... -count=1`
    - **Completion:** Property 1 修复后全通过、Property 2 保持通过，integration/race/full suites 全绿，无 flaky sleep、data race、生产 I/O 或未分类 terminal outcome；保存脱敏验收摘要。
    - _Depends on: 3.10_
    - _Requirements: 2.1-2.17, 3.1-3.12_

  - [x] 4.2 Finalize manual Singapore production runbook and queue handoff
    - 只编写/校验人工 checklist：不可变 artifact/config checksum、Singapore/IP 假设、旧凭证撤销与最小权限轮换的**待人工证据**、`trading_enabled=false` reconcile-only、StateDirectory/WAL backup、health/journald 证据、独立人工 trading approval、观察窗口、Safe_Stop 与 schema/outbox-compatible rollback；明确禁止 `sed`，均值回归排除。
    - runbook 命令仅作为人工步骤文本，不执行真实部署、`systemctl`、EC2、凭证轮换或 OKX 操作；不得包含凭证值、旧东京假设或把自动化通过解释为生产批准。
    - **Target tests:** `go test ./internal/config ./internal/integration -run 'Test(RunbookGates|SingaporeArtifacts|NoSedDependency|NoSecretArtifacts|MeanReversionExcluded|HumanApprovalRequired)' -count=1`
    - **Completion:** runbook 可从 reconcile-only 安全推进或回滚，所有生产动作仍明确 pending human approval；脱敏验收摘要、counterexample 记录和 queue 状态可追踪。
    - _Depends on: 4.1_
    - _Requirements: 2.8, 2.9, 2.13, 2.14, 2.15, 2.16, 2.17, 3.8, 3.10, 3.11, 3.12_

## Task Dependency Graph

以下 JSON 是完整叶任务 DAG；父任务 `3`、`4` 不进入 queue。每个 wave 仅含叶任务，且只有 3.10 可集中修改 `cmd/main.go`，其 wave 独占。

```json
{
  "schemaVersion": 1,
  "leafTaskCount": 14,
  "waveCount": 10,
  "maxParallelism": 3,
  "queueReady": ["1"],
  "parentTasksExcludedFromQueue": ["3", "4"],
  "cmdMainGoPolicy": {
    "largeRewriteTask": "3.10",
    "exclusiveWave": 8
  },
  "leafTasks": [
    {"id": "1", "dependsOn": []},
    {"id": "2", "dependsOn": ["1"]},
    {"id": "3.1", "dependsOn": ["2"]},
    {"id": "3.2", "dependsOn": ["2"]},
    {"id": "3.3", "dependsOn": ["3.1", "3.2"]},
    {"id": "3.4", "dependsOn": ["3.1", "3.2"]},
    {"id": "3.5", "dependsOn": ["3.1"]},
    {"id": "3.6", "dependsOn": ["3.2", "3.4", "3.5"]},
    {"id": "3.7", "dependsOn": ["3.2", "3.4"]},
    {"id": "3.8", "dependsOn": ["3.2", "3.4", "3.6", "3.7"]},
    {"id": "3.9", "dependsOn": ["3.3", "3.5", "3.6", "3.7", "3.8"]},
    {"id": "3.10", "dependsOn": ["3.1", "3.2", "3.3", "3.4", "3.5", "3.6", "3.7", "3.8", "3.9"]},
    {"id": "4.1", "dependsOn": ["3.10"]},
    {"id": "4.2", "dependsOn": ["4.1"]}
  ],
  "waves": [
    {"wave": 1, "tasks": ["1"]},
    {"wave": 2, "tasks": ["2"]},
    {"wave": 3, "tasks": ["3.1", "3.2"]},
    {"wave": 4, "tasks": ["3.3", "3.4", "3.5"]},
    {"wave": 5, "tasks": ["3.6", "3.7"]},
    {"wave": 6, "tasks": ["3.8"]},
    {"wave": 7, "tasks": ["3.9"]},
    {"wave": 8, "tasks": ["3.10"]},
    {"wave": 9, "tasks": ["4.1"]},
    {"wave": 10, "tasks": ["4.2"]}
  ]
}
```

## Queue Readiness

- 当前仅叶任务 **1** ready；其 expected failure/counterexample 被确认前，Task 2 与所有实现任务均 blocked。
- Task 1 若为 `unexpected_pass`，queue 必须保持停止并等待用户选择，不得自动推进。
- 叶任务总数：14；实现/验证叶任务：12；waves：10；DAG 无环，且 `cmd/main.go` 集中改动不与任何任务同 wave。
