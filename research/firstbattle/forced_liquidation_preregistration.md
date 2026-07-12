# 强平接力/竭尽 方向机制 · 预注册（Pre-Registration）

- 协议 ID：`firstbattle.forced_liquidation.preregistration`
- Generation：`firstbattle-forced-liquidation-g1`
- 机器可读 manifest：[`forced_liquidation_preregistration.json`](./forced_liquidation_preregistration.json)
- 状态：**封卷前预注册**。本文档与 manifest 在读取任何考卷前锁定，之后不得修改。

本预注册对应「首战完全体」计划第 6~9 节，只覆盖**强平被迫流**这一条方向主线。所有字段与
`internal/mechanism`、`internal/replay`、`internal/exam`、`internal/scorecard` 的现有类型一一对应，
目标是**可直接驱动代码与审计**：manifest 是权威真相，本文档是可读镜像，两者不一致以 manifest 为准。

> 铁律：**封卷前不读取任何收益/结算/盈亏结果**。本文档不含、也禁止含任何考卷内容、时间范围或标的。

---

## 0. 最高原则

- **调研定方向**：方向只来自被迫流机制（谁被迫、被迫向哪边、还剩多少、是否衰减、被吸收多少），
  不来自价格外推或叙事。方向由 `mechanism.Expert.Evaluate` 依据事件卡给出。
- **极端定仓位**：仓位硬上限由逐所维持保证金、真实强平求根与极端场景取最小值决定；
  信心只能在硬上限内缩放（见第 7、8 节）。
- **天道定信心**：`HeavenConfidence ∈ [0,1]` 只缩放，不突破硬顶。
- **不预设赢家**：`H_relay`（接力顺势）与 `H_exhaustion`（竭尽反转）享有**对称预算与对称判定**，
  谁赢由考卷决定，不由本文档决定。

---

## 1. 假设（对称，两条互斥分支）

| 假设 ID | 分支 | 方向规则 | 触发前提 |
|---|---|---|---|
| `H_relay` | 被迫流仍充沛、吸收不足 | `direction = forced_flow.side` | `NOT exhausted` |
| `H_exhaustion` | 被迫流衰减、剩余极少、吸收充分 | `direction = forced_flow.side.opposite()` | `exhausted` |

`exhausted` 的判据完全由专家内核决定，不在本文档重述以外新增自由度：
- 恐慌强平（`panic_liquidation`）：`Decaying ∧ ObservedNotional>0 ∧ AbsorptionRatio≥0.60 ∧ Remaining≤0.20×Observed`；
- 逐档强平（`tiered_liquidation`）：`tiers_remaining<1 ∧ AbsorptionRatio≥0.55`。

两条假设的主指标都是 `net_edge_bps_lower_bound`（条件净收益下界），阈值预注册于 manifest。

### 1.1 双锚 phases（relay / exhaustion）

同一原始 episode 最多产出**两个互相独立**的候选 phase，各自独立锚点、独立 `EntryFeatures`/`FutureLabel`、
独立 `EpisodeID`（`= raw_episode_id + "#" + phase`），**对称预算、对称判定**；严禁用同一「结束时」
特征快照同时检验两者（这是本包修复的核心防泄漏点，见 `internal/mechanismpanel/{types,episode}.go`）。

| phase | 假设 | `as_of` 规则（t_signal） | 方向 |
|---|---|---|---|
| `relay` | `H_relay` | 累计被迫名义（全 venue，`event_time ≤ as_of`）≥ `min_trigger_notional_usdt` 且被迫流加速、payer 明确、方向可交易的最早强平时刻 | `forced_flow.side` |
| `exhaustion` | `H_exhaustion` | gap 确认（末次强平后经过 `merge_gap_ms` 无新事件，或流耗尽）且不再加速、`Remaining ≤ exhaustion_max_remaining_ratio`、`Absorption ≥ exhaustion_absorption_min` 的最早时刻 | `forced_flow.side.opposite()` |

**统一参数**（`phases.shared_unified_params`，与代码 `preregistration.go` 常量、`DefaultAggregatorConfig()` 三方一致）：
`feature_lag_ms=250`、`exhaustion_absorption_min=0.60`、`exhaustion_max_remaining_ratio=0.20`、`symmetric_budget=true`、`no_presupposed_winner=true`。

---

## 2. Payer（谁被迫付款）

无可观测 payer 的候选一律淘汰（`mechanism.RejectNoPayer`）。

| payer 角色 | 被迫方向 `forced_side` | 可观测来源 |
|---|---|---|
| `long_liquidations`（多头被强平，被迫卖出） | `short`（下行压力） | `fast_liquidation_events.jsonl` |
| `short_liquidations`（空头被强平，被迫买入） | `long`（上行压力） | `fast_liquidation_events.jsonl` |

---

## 3. 可观测字段与三时钟

时钟语义（`internal/datatruth` 事件信封）：
- `event_time_ms`：交易所事件时间——**特征截断以此为准**；
- `receive_time_ms`：本地接收时间——迟到样本按接收时间二次剔除；
- `process_time_ms`：本地处理时间——**仅审计，禁止进入特征**。

| 数据流 | 角色 | 关键字段 | 映射到 `MechanismEventCard` |
|---|---|---|---|
| `fast_liquidation_events.jsonl` | 被迫流主源 | side / notional / price / ts | `ForcedFlow.Payer/Side/ObservedNotional` |
| `fast_xvenue_liq_events.jsonl` | 跨所被迫流 | venue / side / notional | `ForcedFlow.RemainingNotional`, `Transmission.Cluster` |
| `fast_xmeta_snapshots.jsonl` | 传导 | beta / cluster_id / lead_lag | `Transmission.Beta/Cluster/LeadLagMs` |
| `fast_market_mids.jsonl` | 盘口/吸收 | mid / spread_bps / top_depth | `Transmission.AbsorptionRatio`, `Executability.*` |
| `instrument_meta.json`（PIT） | 合约元 | tick / lot / min_notional | `Executability.LotNotional/MinNotional` |
| `universe.json`（PIT） | 标的宇宙 | as_of 已知标的 | 禁止用当前 universe 回填历史 |

命名信号（`card.Signals`）：`absorption_ratio`、`remaining_forced_notional`、`tiers_remaining`、`forced_flow_decaying`。

**禁作特征**：任何 `settlements` / `realized_usdt` / 盈亏结果字段。

---

## 4. Episode 定义

- **一个 episode** = 同一相关簇内、由同向被迫流触发、在被迫流枯竭或制度期限前的一段连续被迫窗口。
- **锚点 `t0`** = 首个满足触发条件的强平事件 `event_time_ms`；触发条件：窗口被迫名义 `≥ 50000 USDT`
  且 payer 明确、方向可交易。
- **相关簇合并**：同一 BTC/强平簇按**一个相关 episode** 处理（`cluster_key=Transmission.Cluster`，
  `merge_gap_ms=60000`），避免相关样本重复计数。
- `max_episode_duration_ms=3600000`；期限来自 `Regime.DeadlineUnixMs`，无期限（≤0）者淘汰（`RejectNoDeadline`）。

---

## 5. 特征时间截断（防泄漏，双锚）

每个 phase 有自己的 `as_of_ms`（`= t_signal`，见 §1.1）与 `decision_ms`（`= as_of_ms + feature_lag_ms`，
`feature_lag_ms = 250`）。不再是单一 `t0`，也不再从 `as_of` 预减 lag。特征/标签的边界为：
1. **特征**只用 `event_time_ms ≤ as_of_ms` 的事件（不预减 lag）；
2. `(as_of_ms, as_of_ms + feature_lag_ms]` 这段宽 `feature_lag_ms` 的**等待生效窗口**对特征与标签都不可见；
3. **标签**只用 `event_time_ms > decision_ms` 的事件；
4. `receive_time_ms ≤ as_of_ms`（迟到二次剔除）；
5. 只用 `as_of_ms` 时点已知的 PIT instrument/universe；
6. `as_of` 之后的修正链（`HasCorrection`）不得回填；
7. `process_time` 永不进入特征。

该「特征 ≤ as_of、标签 > decision、中间 embargo 不可见」的双锚闸门由运行时强制
（`internal/mechanismpanel.computeFeaturesAsOf` 与 `pendingLabel.absorbFuture`）。

泄漏守卫：`no_future_event_time`、`no_future_receive_time`、`no_current_universe_backfill`、`no_post_t0_correction`、`feature_label_embargo_gap`。

特征清单见 manifest `features`（被迫名义、剩余名义、一阶差分、吸收比、tiers_remaining、beta、spread、盘口深度）。

---

## 6. 标签（Label）

> 标签是**规格**，由密封考试内的代码计算；研究者封卷前不读取任何收益结果。

- 锚点 = 该 phase 的 `decision_ms`（`= as_of_ms + feature_lag_ms`）；relay 与 exhaustion **各自独立锚点**，不共用单一 `t0`；
- `horizon_ms = min(Regime.DeadlineUnixMs − decision_ms, max_label_horizon_ms)`，`max_label_horizon_ms = 1800000`；
- 研究侧多前瞻窗口 `label_horizons_ms = [30000, 60000, 120000, 300000]`（与 `DefaultAggregatorConfig().LabelHorizons` 一致），均 ≤ `max_label_horizon_ms`；
- **staleness**：`max_label_staleness_ms = 10000`；每个 horizon 只接受首个
  `offset >= horizon && offset-horizon <= max_label_staleness_ms` 的价格，更晚样本将该 horizon 冻结为 unobserved；
- 度量 = **以可成交出场盘口（非入场盘口）计**、扣除全量成本后的**净对数增长**（`net_log_growth_after_costs`）；
- 出场时点取 **invalidator 触发 / deadline 到期 / label horizon 到期** 三者最早者；
- 同簇 episode 合并为一个相关样本，权重不重复计数。

---

## 7. 成本（Cost）

`internal/replay` 全量成本模型，参数由 `fast_maker_attempts` 校准，**禁止为提高成交率而下调**：
- taker 5.0 bps / maker 2.0 bps；资金费按 episode 跨越的结算窗口计提；
- 预期滑点由盘口深度与目标名义推导，**出场用出场盘口**；
- 部分成交、毒性成交、`latency_ms=250` 全部计入。

优化目标 = **条件净收益下界**，不优化成交率。已降级的旧结论：`mid_cross`、固定 `12/18bps`、入场盘口代替出场盘口。

---

## 8. 止错（只认证伪，不认价格止损）

至少一个 `Observable=true` 的 invalidator，否则 `RejectNoInvalidator`。

- `H_relay`：`remaining_forced_notional` 趋零 / `absorption_ratio` 超竭尽阈值 → 离场；
- `H_exhaustion`：`remaining_forced_notional` 回升出竭尽带 / `absorption_ratio` 跌破阈值 → 离场；
- 共享：`deadline` 到期、`OutOfDistribution` 强制空仓。

**偿付防线是独立状态机**：按最差压力场景缓冲削减边际风险最大的仓位；任何专家不得关闭偿付防线。
策略止错（论点死亡）与偿付减仓（最差场景缓冲）互不覆盖。

---

## 9. 极端场景（准入门）

`internal/replay.DefaultStressScenarios` + 附加尾部场景。每个候选须在**全部场景**下
`SurvivingBufferPct > 0`（不破产）方可进入下一区：

`corr1_crash_30`、`corr1_squeeze_30`、`all_long_crash_20`、`all_short_squeeze_20`、
`liquidity_evaporation`、`api_outage_pending_outflow`、`stablecoin_depeg`、`mmr_tier_jump`。

---

## 10. 研究区 / WF 区 / 多卷考试（固定顺序）

固定封卷顺序：**研究区 → 嵌套 walk-forward → 历史挑战卷 → 未接触跨所密封卷 → 最终黄金卷**。

- **研究区**（可反复读取，不含任何密封卷）：机制调研、特征与阈值开发。
- **嵌套 WF**（只作用于研究区）：`expanding_train_rolling_test`，`folds=5`，**时间 `embargo_ms=3600000`**，
  每折 `min_episodes_per_fold=20`。结构参数在此预注册（与 `preregistration.go` 的 `UnifiedWalkForward*` 常量一致）。
- **密封考卷**：只登记 `exam_id` 与固定顺序，**内容/时间/标的/阈值一律不写入本文档**，由考官服务持有。

**运行时参数**（`manifest.runtime`，与运行时默认值逐字段一致）：`max_open_episodes=64`、
`max_pending_labels=128`、`max_context_buffer=4096`、`max_dedupe_entries=4096`、`label_horizons_ms=[30000,60000,120000,300000]`，
以及镜像的 `merge_gap_ms/max_episode_duration_ms/min_trigger_notional_usdt/feature_lag_ms/max_label_staleness_ms`。三方漂移由
`preregistration_test.go` 强制。

| order | exam_id | 角色 |
|---|---|---|
| 2 | `flq-hist-challenge-01` | 历史挑战卷（一次性） |
| 3 | `flq-hist-challenge-02` | 历史挑战卷（一次性） |
| 4 | `flq-hist-challenge-03` | 历史挑战卷（一次性） |
| 5 | `flq-crossvenue-sealed-01` | 未接触跨所密封卷（一次性） |
| 6 | `flq-golden-final` | 最终黄金卷（一次性） |

（order=1 为已消费的 `legacy-execution-challenge`，属旧 baseline，不追认为本 generation 考卷。）
**任何解封、崩溃或结果返回都永久消费该卷**（`exam.Status → consumed_*`），登记于 `records/firstbattle_exams.json`。

---

## 11. 失败标准

- **逐假设独立裁决**：各假设按自身预注册阈值判定；relay 失败不自动判 exhaustion 通过，反之亦然。
- **黄金卷失败即终局**：本 generation 永久失败；修改后必须新 generation、新考卷。
- **不可覆盖**：天道叙事、架构完成度、金额暂时为正均不能覆盖失败。
- **无通用门**：禁止用统一 PF 或固定交易笔数覆盖事件稀疏度差异；每机制阈值看卷前锁定。
- **稀疏兜底**：episode 不足 `min_episodes_per_exam=30` 判 `inconclusive`，不判通过。

---

## 12. 多重比较预算

- 假设 2 × 每假设变体 1 × 密封卷 5 = **10 个预注册检验**；
- 族错误率控制 **Holm-Bonferroni**，`family_wise_alpha=0.01`；
- 每张考卷按 `order` 消费 alpha 预算；预算耗尽后**不得追加变体或考卷**；
- 禁止事后（post-hoc）新增变体。

---

## 13. 禁止事项

1. 封卷决策前读取任何收益/结算/盈亏结果。
2. 触碰、预览或重跑任何密封卷 / 跨所卷 / 黄金卷。
3. 用当前 universe 或 `t0` 之后的修正回填历史特征。
4. 看卷后修改阈值、假设、特征或成本参数。
5. 优化成交率而非条件净收益下界。
6. 用固定价格止损覆盖运行时可观测 invalidator。
7. 用统一 PF / 固定交易笔数作为跨机制通用门。
8. 在 relay 与 exhaustion 之间预设赢家或非对称分配预算。
9. 接受无 payer / 无期限 / 无可观测 invalidator、仅凭「跌多会涨」的候选。
10. 把考卷内容或时间范围写入研究者可见文档。

### 13.1 红队 12 项要求

机器可读版见 `manifest.red_team.requirements`（`preregistration_test.go` 断言恰为 **12 项**，
且每项都由代码或流程强制，不靠自觉）：

| # | id | 要求（enforced_by 见 manifest） |
|---|---|---|
| 1 | `rt01_no_revenue_peek_before_seal` | 封卷前不读取任何收益/结算/盈亏 |
| 2 | `rt02_no_touch_sealed_exams` | 不触碰/预览/重跑密封卷 |
| 3 | `rt03_no_current_universe_or_post_asof_backfill` | 不用当前 universe 或 as_of 后修正回填 |
| 4 | `rt04_no_threshold_change_after_seal` | 看卷后不改阈值/假设/特征/成本 |
| 5 | `rt05_optimize_net_edge_not_fill_rate` | 优化净收益下界而非成交率 |
| 6 | `rt06_no_price_stop_override_invalidator` | 不用价格止损覆盖可观测 invalidator |
| 7 | `rt07_no_unified_gate` | 不用统一 PF/固定笔数作通用门 |
| 8 | `rt08_symmetric_relay_exhaustion` | relay/exhaustion 对称、不预设赢家 |
| 9 | `rt09_reject_no_payer_deadline_invalidator` | 淘汰无 payer/无期限/无 invalidator 候选 |
| 10 | `rt10_feature_label_physical_separation` | 特征≤as_of、标签>decision、双锚独立 |
| 11 | `rt11_manifest_code_drift_guard` | manifest↔常量↔运行时三方一致 |
| 12 | `rt12_reproducible_protocol_hash` | protocol hash 可复现并封卷前登记 |

---

## 14. 审计与代码驱动

- **协议哈希**：`datatruth.HashJSON` 对 manifest（`audit.protocol_hash=null`）求值，结果写入 `exam.ProtocolHash`。
  - 规范（可复现）：读取本 manifest → `json.Unmarshal` 到 `map[string]any` → 令 `audit.protocol_hash=null` →
    `datatruth.HashJSON`（对对象键递归排序后 SHA-256，小写 hex）。哈希对键序与空白不敏感。
  - 当前值：`6b16eb531230a14242f29d398fd357188867062f2fb2f0f0af6b7f554dd83bcb`
    （`manifest.audit.protocol_hash`，由 `preregistration_test.go` `TestProtocolHashMatchesManifest` 复算校验）。
- **收据边界**：无 KMS 时 receipt 为 `UNSIGNED_BOUNDARY` 接口产物，不得当作密码学证明。
- **驱动对象**：`mechanism` 专家、`replay` 成本/压力、`exam` 考卷登记、`scorecard` 准入门。
- manifest 与代码类型不一致时，以 manifest 为准并当作缺陷修复（新 generation）。本次修订即按此原则纠正了
  manifest 缺失的 `phases` 双锚、`feature_lag` 语义、label horizons/staleness、runtime 与红队清单等结构矛盾，
  未改动任何经济阈值。
