# 跨交易所合约-合约套利新版本实现规格 v5

> 这份文档是实现规格书，不是策略介绍，也不是建议清单。
> 目标是把当前跨交易所 `perp-perp` 策略，升级成一个在安全约束下运行的净 EV 组合轮动引擎。
>
> v5 相比 v4 的核心升级只有五件事:
>
> - 彻底拆分排序分与 size 因子，禁止量纲混用
> - 把资本效率作为一等目标，而不是只看绝对净利
> - 给共同可比窗口加最小窗口和 funding 边界保护
> - 把冷启动接管、残仓恢复、过渡态恢复写入硬验收
> - 把“轮动优于基线”写成可执行的对照验证协议

---

## 1. 总目标

系统必须满足以下目标:

1. 在风险、保证金、API/IP、安全闸约束下，最大化组合未来净收益。
2. 优先分配容量给资本效率高、质量高、可兑现性强的机会。
3. 当前持仓只按“从当前时点开始的未来价值”判断保留、换仓或平仓，不回看沉没成本。
4. 没有正 EV 且质量可接受的机会时，允许空仓，空仓是正确状态。
5. 执行层不得因为轮动需求把系统变成高频重扫器、暴力轮询器或状态不一致系统。

---

## 2. 术语与强定义

### 2.1 候选机会 `CandidateOpportunity`

同一 `symbol` 在两个不同 `venue` 之间形成的一条可交易 pair。

必须包含:

- `Symbol`
- `HighVenue`
- `LowVenue`
- `SnapshotID`
- `SnapshotTime`
- `FreshnessAgeSeconds`
- `HighFundingRate`
- `LowFundingRate`
- `RawSpreadAPR`
- `ComparableWindowHours`
- `ExpectedHoldHours`
- `SpreadEV`
- `OpenCost`
- `CloseCost`
- `ExpectedSlippage`
- `ObservableRiskCost`
- `PolicyBuffer`
- `NetEV`
- `CapitalEfficiencyEV`
- `RiskAdjustedEVPerHour`
- `HistoryQualityScore`
- `LiquidityScore`
- `StabilityScore`
- `ExecutionScore`
- `RecoveryQualityScore`
- `RankScore`
- `SizeMultiplier`
- `TradableNotional`
- `TradableQty`
- `NoTradeReason`

### 2.2 当前持仓 `CurrentPosition`

已经在场的跨所 pair 持仓。

必须包含:

- `Symbol`
- `HighVenue`
- `LowVenue`
- `EntryTime`
- `EntryNotional`
- `EntryDecisionCycleID`
- `CurrentSnapshotID`
- `CurrentFutureEV`
- `CurrentCapitalEfficiencyEV`
- `CurrentRiskAdjustedEVPerHour`
- `CurrentRiskScore`
- `EstimatedExitCost`
- `EstimatedSwitchCost`
- `CloseAttempts`
- `ChurnStreak`
- `CloseFailStreak`
- `SwitchCooldownUntil`
- `HistoryQualityScore`
- `ExecutionScore`
- `RecoveryQualityScore`
- `FreshnessAgeSeconds`
- `TransitionState`
- `Priority`

### 2.3 净收益 `NetEV`

`NetEV` 指“从当前时点开始，到可比窗口结束时，该机会的预期净利润”，单位必须统一为 `USDT`。

公式:

`NetEV = SpreadEV - OpenCost - CloseCost - ExpectedSlippage - ObservableRiskCost - PolicyBuffer`

强约束:

- 所有机会必须使用同一成本口径。
- 所有机会必须使用同一风险缓冲口径。
- 不允许把“未知”或“数据缺失”当作 `0` 成本或 `0` 收益。
- `ObservableRiskCost` 与 `PolicyBuffer` 必须分别记录、分别展示、分别可调。

### 2.4 资本效率 `CapitalEfficiencyEV`

`CapitalEfficiencyEV` 指“每单位有效保证金占用在可比窗口内能够产生的净收益”。

建议定义:

`CapitalEfficiencyEV = NetEV / EffectiveMarginOccupied`

其中 `EffectiveMarginOccupied` 必须包含:

- 双边保证金占用
- 过渡态额外 headroom 占用
- venue 特有的风险冗余占用

### 2.5 时间风险收益 `RiskAdjustedEVPerHour`

`RiskAdjustedEVPerHour` 指“按时间和风险折算后的单位小时净收益”。

建议定义:

`RiskAdjustedEVPerHour = NetEV / ComparableWindowHours / max(RiskWeight, epsilon)`

该指标用于避免系统只偏爱绝对收益大但占资重、持有久、退出慢的机会。

### 2.6 当前持仓未来价值 `CurrentFutureEV`

`CurrentFutureEV` 指“从现在开始，当前持仓继续持有到本轮可比窗口结束时的预期净利润”。

公式:

`CurrentFutureEV = RemainingSpreadEV - FutureExitCost - FutureRiskCost`

强约束:

- 不回看历史开仓手续费。
- 只看未来边际收益和未来退出成本。
- 必须与候选 `NetEV` 使用同一可比窗口。
- 若 old/new funding 时钟不同步，必须退化到最短共同可比窗口。
- 若共同可比窗口短于 `MinComparableWindow`，本轮不得触发换仓比较。

### 2.7 可比窗口 `ComparableWindow`

估值窗口必须显式定义为“从当前时点起，到下一次共同 funding 边界的最短共同可比窗口”。

必须增加三个保护条件:

- `MinComparableWindow`: 最短可比窗口，低于此值不得比较 old/new EV。
- `PostFundingCooldown`: funding 边界后冷静期，在此期间不得因短窗口噪声触发换仓。
- `MaxDecisionAgeSeconds`: 决策时允许使用的最大快照年龄，超过即视为陈旧数据。

### 2.8 换仓收益 `SwitchGain`

`SwitchGain = EV(new) - EV(old) - SwitchCost - SwitchHeadroomCost`

其中:

- `SwitchCost` 包含平旧仓和开新仓的显式成本。
- `SwitchHeadroomCost` 包含过渡期额外保证金占用、pair 互斥等待成本、数据新鲜度损失、顺序性成本。
- 只有当 `SwitchGain > SwitchBuffer` 时才允许换仓。
- 换仓必须声明 `SwitchSequence` 和 `PartialSwitchFailure` 恢复逻辑。

### 2.9 排序分 `RankScore`

`RankScore` 只用于排序，不得直接用于 size。

建议定义:

`RankScore = Rank(NetEV, CapitalEfficiencyEV, RiskAdjustedEVPerHour, HistoryQualityScore, LiquidityScore, StabilityScore, ExecutionScore, RecoveryQualityScore)`

强约束:

- `RankScore` 可以是加权分、分层排序分、字典序排序结果。
- `RankScore` 不要求是 `0~1`。
- `RankScore` 绝不能在 size 公式里直接 `clamp` 使用。

### 2.10 仓位因子 `SizeMultiplier`

`SizeMultiplier` 只用于仓位缩放，必须严格归一化到 `0~1`。

建议由以下因子共同决定:

- `CapitalEfficiencyEV`
- `ExecutionScore`
- `LiquidityScore`
- `RecoveryQualityScore`
- `FreshnessPenalty`
- 当前组合容量与相关性拥挤程度

### 2.11 质量分

所有质量分必须归一化到 `0~1`，且不得混用:

- `HistoryQualityScore`: 历史兑现质量
- `LiquidityScore`: 流动性质量
- `StabilityScore`: 价差和 funding 持续性质量
- `ExecutionScore`: 开平仓可兑现质量
- `RecoveryQualityScore`: 退出、恢复、残腿修复的可兑现质量

### 2.12 禁止的含糊表达

以下词语不能单独作为决策依据，必须落到公式、阈值或枚举原因:

- “看起来不错”
- “质量高”
- “值得做”
- “显著优于”
- “尽量用满仓位”

系统允许空仓，空仓不是失败。

---

## 3. 核心运行原则

### 3.1 只看未来，不回看沉没成本

对当前持仓是否继续持有，只看:

- 从现在开始还能赚多少
- 风险是否仍然可接受
- 是否存在更优替代

历史开仓手续费属于沉没成本，不参与继续持有判断。

### 3.2 机会选择必须先过经济正确性

候选入池的硬前提:

- `NetEV > 0`
- 数据新鲜度合格
- 深度容量足够
- 保证金足够
- venue 可交易
- 未被 cooldown
- 未被 blacklist
- 未触发 halt 或健康闸

### 3.3 组合目标不是只看绝对净利

组合决策必须同时考虑三类目标:

1. `Absolute NetEV`: 组合总净收益
2. `Capital Efficiency`: 单位保证金收益
3. `Execution Quality`: 开平仓和恢复可兑现性

不允许只按绝对 `NetEV` 填满组合。

### 3.4 排序和仓位必须分离

- `RankScore` 只负责排序
- `SizeMultiplier` 只负责缩放仓位
- `TradableNotional` 只负责容量上限

任何实现都不得把 `RankScore` 直接当作 `SizeMultiplier` 使用。

### 3.5 scanner 与 executor 必须严格分离

- `scanner` 负责拉快照、评分、排序、产出候选池
- `executor` 负责消费候选池、对比当前持仓、开平仓、换仓和恢复
- `executor` 不得在热路径中重新发起全市场 funding 重扫
- `executor` 不得绕过 scanner 的 cooldown、backoff、黑名单和新鲜度约束

### 3.6 API/IP 安全是硬约束

不得因为轮动策略而:

- 提高无意义 REST 访问频率
- 取消缓存优先
- 取消退避
- 取消 venue 级限频
- 取消并发上限
- 取消影子/实盘隔离

### 3.7 SSOT 与写边界是硬约束

- `cross_venue_positions_store.go` 的权威持仓台账是 SSOT。
- executor 内存态、dashboard、report、recovery 建议都只能是派生视图或恢复输入。
- recovery 只能基于权威台账和交易所真值恢复，不能反向覆盖真值。
- 任何恢复行为都不得在未确认交易所真实仓位前覆盖台账。

---

## 4. 决策周期与数据新鲜度

### 4.1 决策周期标识

每一轮决策必须有:

- `DecisionCycleID`
- `SnapshotID`
- `SnapshotTime`
- `DecisionTime`
- `CycleReason`

### 4.2 新鲜度预算

必须显式配置并检查:

- `MaxSnapshotAgeSeconds`
- `MaxCompareAgeSeconds`
- `MaxSwitchAgeSeconds`
- `MaxOrderbookAgeSeconds`

若超过预算:

- 不得开新仓
- 不得用来比较 old/new
- 必要时进入 `hold` 或 `halt`

### 4.3 funding 边界保护

必须显式配置:

- `MinComparableWindow`
- `PostFundingCooldown`
- `SkipCompareWhenWindowTooShort`

规则:

- 若共同窗口太短，则 `HOLD` 或 `SKIP_COMPARE`
- 不得在 funding 结算边界附近因噪声触发频繁换仓

### 4.4 快照血缘

任何 compare/switch 决策必须能追溯到:

- 使用了哪一轮 `SnapshotID`
- 当前持仓重估使用了哪一轮 `SnapshotID`
- old/new 是否基于同轮或同 lineage 快照

若不能追溯，不得执行。

---

## 5. 候选评分与入池

### 5.1 候选净收益

公式:

`NetEV = SpreadEV - OpenCost - CloseCost - ExpectedSlippage - ObservableRiskCost - PolicyBuffer`

`ObservableRiskCost` 至少拆成:

- `LiquidityRiskCost`
- `ExecutionFailureCost`
- `LiquidationBufferCost`
- `FreshnessPenalty`
- `VenueReliabilityPenalty`

### 5.2 排序分

建议使用分层排序，而不是单一乘法黑盒。

推荐优先级:

1. `NetEV > 0`
2. `CapitalEfficiencyEV >= MinCapitalEfficiencyEV`
3. `RiskAdjustedEVPerHour >= MinRiskAdjustedEVPerHour`
4. `ExecutionScore >= MinExecutionScore`
5. `RecoveryQualityScore >= MinRecoveryQualityScore`
6. 对剩余候选按 `RankScore` 排序

### 5.3 仓位因子

建议定义:

`SizeMultiplier = g(CapitalEfficiencyEV, ExecutionScore, LiquidityScore, RecoveryQualityScore, FreshnessPenalty, PortfolioCrowdingPenalty)`

强约束:

- `SizeMultiplier` 必须在 `0~1`
- `SizeMultiplier = 0` 表示不得分配仓位
- 不得因为 `RankScore` 高就自动给满仓

### 5.4 候选入池硬门槛

候选必须同时满足:

- `NetEV > 0`
- `TradableNotional >= MinOpenNotional`
- `FreshnessAgeSeconds <= MaxSnapshotAgeSeconds`
- `MarginAvailable = true`
- `DepthSufficient = true`
- `VenueHealthy = true`
- `NotInCooldown = true`
- `NotBlacklisted = true`

### 5.5 软门槛与受控补位

以下项优先作为软门槛和排序/size 因子:

- `HistoryQualityScore`
- `LiquidityScore`
- `StabilityScore`
- `ExecutionScore`
- `RecoveryQualityScore`

如果候选池过少，必须执行受控补位判定:

1. 先把软阈值下调到最小安全下限
2. 重新评分、重新排序
3. 只在存在“正 EV 且质量可接受”的 fallback 时才允许补位
4. fallback 只允许递减 size，不允许突破硬门槛
5. 如果不存在 acceptable fallback，则保持空仓

### 5.6 历史质量数据

scanner 必须维护但不要求全部写入主快照:

- 最近 N 次净利均值
- 胜率
- 平仓成功率
- 换仓成功率
- churn 率
- 极端亏损频率
- 平仓失败次数
- 恢复成功率
- 最近一次异常时间

---

## 6. 当前持仓未来价值与保留逻辑

### 6.1 持仓未来价值模型

当前持仓必须持续重估:

- `CurrentFutureEV`
- `CurrentCapitalEfficiencyEV`
- `CurrentRiskAdjustedEVPerHour`
- `CurrentRiskScore`
- `EstimatedExitCost`
- `EstimatedSwitchCost`

### 6.2 当前持仓保留条件

只有同时满足以下条件时，才继续持有:

- `CurrentFutureEV > 0`
- `CurrentCapitalEfficiencyEV` 没有显著劣化
- 不存在足以覆盖换仓成本的新候选
- 风险状态未恶化
- 数据新鲜度合格
- 平仓失败风险未上升到危险阈值

### 6.3 当前持仓必须重估的情况

以下情况必须重估:

- 进入新决策窗口
- funding 边界变化
- 市场显著变化
- close fail streak 上升
- 出现更优候选
- 风险状态变化
- 新鲜度超限

---

## 7. 换仓规则

### 7.1 换仓触发公式

只有当:

`EV(new) - EV(old) - SwitchCost - SwitchHeadroomCost > SwitchBuffer`

时才允许换仓。

### 7.2 换仓额外保护

必须同时满足:

- `ComparableWindowHours >= MinComparableWindow`
- 不在 `PostFundingCooldown` 内
- `new` 与 `old` 使用可追踪、可比的快照 lineage
- 满足最小持有时间
- 满足换仓冷却
- 满足每轮最大换仓数限制

### 7.3 换仓顺序

必须显式声明:

- `CloseThenOpen`
- `OpenThenClose`
- `StagedSwitch`

默认策略建议:

- 默认 `CloseThenOpen`
- 只有 headroom 充足、市场质量高、恢复质量高时才允许 `OpenThenClose`
- 风险高、容量紧、恢复差时禁止 `OpenThenClose`

### 7.4 部分失败恢复

必须明确:

- 旧仓未平完如何回补
- 新仓未开完如何回退
- 残腿如何收敛
- 过渡态保证金如何回收
- 冷却和黑名单如何施加

### 7.5 不允许的行为

- 不能因为短暂 spread 波动频繁换仓
- 不能因为共同窗口过短而换仓
- 不能忽略过渡态成本
- 不能忽略部分失败恢复

---

## 8. 平仓、残腿与恢复

### 8.1 平仓目标

平仓目标是安全收敛，而不是机械立即平掉。

必须优先:

- 避免残腿
- 减少无效重试
- 控制退出成本
- 保持状态可恢复

### 8.2 平仓分层

平仓必须按层级执行:

1. 正常退出
2. 缩量退出
3. 更激进退出
4. 残腿修复
5. 降级/黑名单

### 8.3 主动平仓触发条件

- `CurrentFutureEV <= 0`
- 风险恶化
- 被更优候选替代且 `SwitchGain` 足够
- 平仓失败次数过多
- 持有时间超过上限
- 快照和真实状态持续不一致

### 8.4 冷启动接管与残仓治理

这是硬规则:

- 系统启动时必须扫描交易所真实仓位，不得因台账为空而跳过。
- 若交易所存在未登记仓位，系统必须进入显式分支:
  - `import`
  - `flatten`
  - `halt`
- 不得在“真实持仓未确认”时继续开新仓。
- 冷启动接管结果必须记录审计事件。

### 8.5 恢复不变量

- 未确认真值前不得覆盖 SSOT
- orphan leg 必须可识别
- partial switch 必须可识别
- 所有恢复路径必须幂等

---

## 9. 组合优化

### 9.1 组合目标

在所有约束下最大化以下组合目标:

`PortfolioObjective = Max(AbsoluteNetEV, CapitalEfficiency, ExecutionQuality, RecoveryQuality)`

落地时至少要显式实现:

- 总净收益约束
- 单位保证金收益约束
- 单位时间收益约束
- 恢复可兑现性约束

### 9.2 组合约束

必须同时满足:

- 总名义上限
- 单币名义上限
- 单 venue 名义上限
- 保证金上限
- 最大同时持仓数
- 相关性簇上限
- venue fault domain 上限
- 数据域故障暴露上限
- 过渡态 headroom 上限
- 风控状态约束

### 9.3 分配原则

在可用容量内:

1. 先过硬门槛
2. 再看 `CapitalEfficiencyEV`
3. 再看 `RiskAdjustedEVPerHour`
4. 再按 `RankScore` 排序
5. 最后用 `SizeMultiplier` 分配 size
6. 低质量机会不得抢占高质量机会容量
7. 候选不足时允许空仓
8. fallback 只允许递减补位，不允许消灭空仓

### 9.4 明确禁止

- 不允许为了填满仓位塞垃圾机会
- 不允许只按 `Top N NetEV` 代替组合优化
- 不允许把重占资低效率机会默认视为优先机会

---

## 10. 状态机

系统必须按以下状态运行:

1. `scan`
2. `score`
3. `rank`
4. `select`
5. `open`
6. `hold`
7. `compare`
8. `switch`
9. `close`
10. `settle`
11. `rebalance`
12. `recover`
13. `import`
14. `flatten`
15. `halt`
16. `failed`

每次迁移必须记录:

- `DecisionCycleID`
- `from`
- `to`
- `reason`
- `SnapshotID`
- `timestamp`
- `positionKey`

---

## 11. 观测与可解释性

### 11.1 必须展示的候选信息

- 候选池排序
- `NetEV`
- `CapitalEfficiencyEV`
- `RiskAdjustedEVPerHour`
- `RankScore`
- `SizeMultiplier`
- 质量分
- 数据新鲜度

### 11.2 必须展示的持仓信息

- `CurrentFutureEV`
- `CurrentCapitalEfficiencyEV`
- `CurrentRiskAdjustedEVPerHour`
- 当前风险分
- 当前退出难度
- 当前是否在过渡态

### 11.3 必须展示的行为解释

- 本轮为什么开仓
- 本轮为什么不换仓
- 本轮为什么不补位
- 本轮为什么必须停机或降级
- 本轮 `NoTradeReason` Top N

### 11.4 必须展示的运行安全状态

- 当前冷却状态
- 当前黑名单状态
- 当前退避状态
- 当前 API 限频状态
- 当前 headroom 占用
- 当前 orphan/partial switch 状态

---

## 12. 文件映射与改造边界

### 12.1 `internal/app/cross_venue_scanner.go`

必须负责:

- 拉 funding snapshot
- 拉深度与盘口缓存
- 产出 `NetEV`
- 产出 `CapitalEfficiencyEV`
- 产出 `RiskAdjustedEVPerHour`
- 产出 `RankScore`
- 产出 `SizeMultiplier` 所需输入
- 维护质量历史
- 记录 `SnapshotID` 和新鲜度

不得负责:

- 下单
- 平仓
- 换仓执行

### 12.2 `internal/app/cross_venue_gates.go`

必须负责:

- 硬门槛判断
- 软门槛判断
- 新鲜度判断
- 换仓门槛判断
- fallback accept 判断

不得负责:

- 重扫全市场
- 绕过缓存和退避

### 12.3 `internal/app/cross_venue_executor.go`

必须负责:

- 消费候选池
- 计算目标组合
- 对比当前持仓与新候选
- 决定新开、保留、换仓、平仓
- 管理过渡态
- 记录 `DecisionCycleID`

不得负责:

- 热路径全市场重扫
- 绕过 scanner 冷却、黑名单、backoff

### 12.4 `internal/app/cross_venue_manage.go`

必须负责:

- 维护 `CurrentFutureEV`
- 维护 `CurrentCapitalEfficiencyEV`
- 维护 `CurrentRiskAdjustedEVPerHour`
- 维护 `CurrentRiskScore`
- 维护 `CloseAttempts`、`CloseFailStreak`、`ChurnStreak`

### 12.5 `internal/app/cross_venue_close.go`

必须负责:

- 分层退出
- 残腿修复
- 退出失败升级治理
- orphan 收敛

### 12.6 `internal/app/cross_venue_positions_store.go`

必须负责:

- 作为 SSOT 原子落盘
- 存储 `SchemaVersion`
- 存储 `DecisionCycleID`
- 存储未来价值相关字段
- 存储过渡态字段
- 幂等写入

### 12.7 `internal/app/cross_venue_settlement.go`

必须负责:

- 区分估算值与真值
- `RealizedFunding` 只由真值更新
- 幂等结算
- 有限次重试

### 12.8 `internal/app/recovery_protocol.go`

必须负责:

- 冷启动接管
- orphan 恢复
- partial switch 恢复
- 基于台账和交易所真值恢复

不得负责:

- 覆盖权威真值

---

## 13. API/IP 安全约束

### 13.1 限频

- funding snapshot 有最小间隔
- 深度和盘口优先复用缓存
- 同一 venue 不得短时间重复全量查询

### 13.2 并发限制

- venue 级并发上限
- symbol 级并发上限
- 全局并发上限

### 13.3 失败退避

- 超时后指数退避
- `429/418/403` 后更长冷却
- 不允许立即重试风暴

### 13.4 执行层边界

- executor 不得额外全量重扫
- executor 不得为了补位暴力请求市场数据
- executor 不得绕过 scanner 新鲜度与 backoff

---

## 14. 测试必须覆盖的内容

### 14.1 候选评分

必须覆盖:

- `NetEV` 计算
- `CapitalEfficiencyEV` 计算
- `RiskAdjustedEVPerHour` 计算
- `RankScore` 排序
- `SizeMultiplier` 归一化
- 软阈值放宽
- acceptable fallback 判定

### 14.2 当前持仓重估

必须覆盖:

- `CurrentFutureEV` 计算
- 沉没成本不回看
- old/new 同口径窗口比较
- `MinComparableWindow` 保护
- `PostFundingCooldown` 保护

### 14.3 换仓治理

必须覆盖:

- `SwitchGain` 计算
- 最小持有时间
- 换仓冷却
- 每轮最大换仓数
- 部分失败恢复

### 14.4 冷启动与恢复

这是一级必测项，必须覆盖:

- 空台账启动
- 交易所残仓存在
- orphan leg 存在
- partial switch 中断重启
- 恢复时台账与真实仓位不一致
- `import/flatten/halt` 分支正确触发

### 14.5 API 安全

必须覆盖:

- 限频
- 退避
- 缓存优先
- 不重复全量扫描
- executor 不越权

### 14.6 经济回归

必须覆盖:

- 轮动版 vs 基线版 realized edge 对比
- 补位是否稀释资本效率
- 质量分是否提升真实可兑现收益
- 引入新鲜度保护后是否降低噪声换仓

---

## 15. 对照验证协议

### 15.1 验证目标

必须验证以下命题:

- 轮动版 realized edge 不劣于基线版
- 补位不会系统性稀释资本效率
- 新鲜度与 funding 边界保护会降低噪声换仓
- 恢复逻辑不会制造额外残腿和记账污染

### 15.2 必须使用的验证方法

至少使用:

- `Shadow Portfolio`
- `Counterfactual Replay`
- `Same Opportunity Set`
- `Same Cost Model`
- `Same Risk Budget`

### 15.3 验证口径

轮动版与基线版必须共享:

- 同一市场窗口
- 同一候选集
- 同一成本模型
- 同一保证金预算
- 同一风控约束

### 15.4 必须输出的归因

- `Funding Alpha`
- `Execution Drag`
- `Switch Drag`
- `Freshness Drag`
- `Recovery Loss`
- `Fallback Dilution`

### 15.5 验收标准

- 轮动版 realized edge 不劣于基线版
- 资本效率不显著下降
- 换仓次数受控
- 补位不导致系统性收益密度下降

---

## 16. 分阶段实施计划

### 16.1 阶段 0: 经济口径冻结

必须先冻结:

- `NetEV`
- `CapitalEfficiencyEV`
- `RiskAdjustedEVPerHour`
- `CurrentFutureEV`
- `SwitchGain`
- `RankScore`
- `SizeMultiplier`

### 16.2 阶段 1: 数据模型落地

必须完成:

- 候选字段入模
- 当前持仓字段入模
- `DecisionCycleID` / `SnapshotID` 入模
- 过渡态字段入模

### 16.3 阶段 2: 候选评分与补位

必须完成:

- scanner 产出新评分字段
- gates 接入硬门槛、软门槛和 fallback
- executor 支持递减补位

### 16.4 阶段 3: 持仓未来价值与换仓

必须完成:

- `CurrentFutureEV` 持续重估
- 同口径比较器
- 换仓保护条件
- 部分失败恢复

### 16.5 阶段 4: 冷启动接管与恢复

必须完成:

- 冷启动真实仓位扫描
- `import/flatten/halt` 分支
- orphan 和 partial switch 恢复
- 幂等恢复

### 16.6 阶段 5: 观测、测试、灰度

必须完成:

- 观测面板字段补齐
- 一级必测补齐
- shadow 模式验证
- 小流量实盘验证

---

## 17. 总体验收标准

### 17.1 策略层验收

- 候选池按 `NetEV + CapitalEfficiency + Quality` 排序，而不是只看 spread
- 当前持仓按 `CurrentFutureEV` 决定保留、换仓或平仓
- 候选不足时允许空仓
- fallback 只在 acceptable 时触发，且 size 递减
- `RankScore` 与 `SizeMultiplier` 完全分离

### 17.2 安全层验收

- scanner / executor 严格分离
- 缓存优先
- `429/418/403` 后明确退避
- API 请求不会因轮动而失控上涨
- compare/switch 不混用陈旧与新鲜数据

### 17.3 恢复层验收

- 冷启动时会扫描交易所真实仓位
- 空台账但有真实仓位时，不会静默继续运行
- orphan/partial switch 都有明确恢复分支
- 台账、状态机、结算真值可恢复且一致

### 17.4 运行层验收

- 系统允许空仓且不把空仓视为故障
- 不会因为阈值过严误判空仓为失败
- 不会因为 funding 边界短窗口频繁抖动
- 过渡态保证金占用可观测、可回收

### 17.5 经济正确性验收

- 轮动版 realized edge 不劣于基线版
- 引入质量分后净 realized edge 提升或至少不下降
- 引入补位后资本效率不系统性下降
- 质量分和真实成交、结算、恢复结果可以闭环回灌

---

## 18. 一句话总结

这版改造不是把现有策略“再调大一点”，而是把它从:

> 噪声驱动的开仓引擎

改造成:

> 在安全约束下运行、按未来净价值和资本效率轮动的 `perp-perp` 组合执行引擎
