# 分层路由 g4 预注册协议（firstbattle.hierarchical_router.g4）

> 本文件是研究者可见的公开协议说明，与
> `research/firstbattle/hierarchical_router_g4_preregistration.json`（机器可读真相）配套。
> 冻结的数值/结构以 JSON 为准；本 md 只做解释，不引入新的可执行阈值。
>
> g4 是新 generation、新 protocol hash。协议先冻结净利润机会地图计算边界，本次为计划阶段 5
> 追加两个最小 mechanism 模板与严格 WF 政策；**仍不预先冻结具体方向策略**。

## 1. 目标与硬边界

- 唯一目标：**先构建全市场扣成本净利润机会地图**，再从重复净赢家倒推决策前条件；发现宽进，严格机制与路由认证另阶段。
- **不寻找**跨行情的永久单方向；方向只存在于地图单元（SkyState × 流动性 × 标的类别 × horizon × 方向 × 本金档位）内。
- fail-closed：未知、歧义、OOD、未认证一律 `NO_TRADE`；可选 funding/rubik 缺失只关闭相应特征列，**不阻塞 OHLCV 主扫描**。
- 只用决策时刻可见、**已完整收盘**的字段；不等待未来样本。
- 发现与 validation 快速筛选只**提名/淘汰候选**，不批准 `RESEARCH_PASS` 叶。
- 本轮只驱动 research 机会地图与候选账本；**exam_actions=0**，不接生产下单路径。

## 2. 关闭并引用 g1 / g2 / g3（只引用不可修改）

g4 永久冻结 g1、g2、g3 并**只引用、不修改**其 hash 与结论：

| 引用项 | g1 | g2 | g3 |
| --- | --- | --- | --- |
| generation | `firstbattle-router-g1` | `firstbattle-router-g2` | `firstbattle-router-g3` |
| router protocol hash | `0eab9bda…7d31` | `395d15ec…118ec` | `1628e1a5…7fb` |
| 搜索/规则数 | — | `162` 组合 | `192` 规则 |
| 提名 / RESEARCH_PASS | `0` / `0` | `0` / `0` | `0` / `0` |
| system_status | `NO_GO` | `NO_GO` | `NO_GO` |

`protocol_test.go` 的 `TestG1G2G3ResultReferencedImmutable` 分别从
`router_stage_summary.json` / `g2_stage_summary.json` / `g3_stage_summary.json` 复核三代结论未被 g4 悄悄改写。

## 3. 绑定的既有契约（binds）

| 绑定项 | hash |
| --- | --- |
| 历史目录 DataHash | `f41ed2586d91054c4b5b2e15e60f32a4d64ccde0032e76dfdc5fc6dee316379c` |
| 历史行情契约 | `a80978f75a74d68a8ac8dcc988ea2e16b01623f739ebab23428cf92f23fd7127` |
| 历史逐叶数据需求 | `07b709f9ddd036749da76d28c66e7efe70638cec7c15bbe1b29789a8534622f7` |
| g1 / g2 / g3 路由协议 | 见上表 |

## 4. 独立数据 profile（data_profiles）

两族 OHLCV profile **独立**分区、独立 horizon 集、独立 purge/embargo：

| profile | 主数据族 | outcome horizon | max lookback | purge/embargo |
| --- | --- | --- | --- | --- |
| `okx_1h` | `okx_1h` | 1h / 4h / 24h | 24h | `max(lookback, horizon)` |
| `okx_5m` | `okx_5m` | 5m / 15m / 1h / 4h | 4h | `max(lookback, horizon)` |

可选非阻塞特征族（缺失只关列，不阻塞主路径）：
`okx_funding` / `bybit_funding` / `rubik_taker_1h` / `rubik_lsratio_1h` / `rubik_taker_5m`

入场/出场价代理：`next_bar_open_after_decision_close` → `horizon_end_bar_open`（下一根可成交价）。

## 5. 时间分区（60/20/20）

每 profile **独立** chronological 分区：

| 分区 | permille | 用途 |
| --- | --- | --- |
| discovery | 600 | 机会地图扫描、Sky/流动性分桶拟合、倒推候选 |
| validation | 200 | 独立历史快速筛选（未参与倒推） |
| walk_forward | 200 | 仅供 EXPLAINED + validation pass 候选严格认证 |

`purge/embargo = max(profile.max_feature_lookback_hours, outcome_horizon_hours)`——**不再统一 168h**。

## 6. 本金档位与三种收益字段

- 本金档位（USDT）：**30 / 100 / 1000**
- 冻结收益字段：**gross**、**fee_net**、**conservative_net**
- 方向：`long/short` 对称；中性带 `neutral_band_bps = 3`

## 7. 保守成本代理（cost_model）

> **声明**：这是基于现有 OHLCV（及可选 funding）的**保守可执行代理**，不是完整盘口或 guaranteed fill 证明。

| 组件 | 冻结值 |
| --- | --- |
| 往返 taker 手续费 | 10 bps |
| 基础 spread/slippage floor | 2 bps |
| 容量冲击 | `min(50, capital/rolling_quote_volume × 10000)` bps |
| bar range 毒性代理 | `min(100, (high-low)/mid × 10000 × 0.5)` bps |
| funding | 持有期内跨结算的 settled funding 绝对值之和（可用时） |

公式：

- `fee_net = gross - roundtrip_taker_fee`
- `conservative_net = gross - fee - floor - capacity - toxicity - funding`

validation 额外要求 **delay+1bar** 入场与 **cost_stress_multiplier=1.5** 压力测试。

## 8. 机会地图维度（profit_map）

地图按以下维度聚合（**全 catalog 合格标的**，不得硬编码字母序 20 截断）：

1. `sky_coarse_market`（ret/rv 为 low/mid/high；fund 为 low/mid/high/unknown，缺失不阻塞；te/dd='*'）
2. `liquidity_bucket`
3. `symbol_class`（discovery 区 volume tertile：major/mid/long_tail）
4. `horizon`
5. `direction`
6. `capital_tier`

CLI **必须显式**传入 `max_files` / `max_lines` / `catalog_symbol_limit`（0 = catalog 全量上限）。
扫描需 bounded、checkpoint、流式落盘；`progress_every_lines = 100000`，每 25 个标的保存一次聚合快照；
每个 map cell 的分位样本和 winner 样本均固定最多 32 条，防止全市场 checkpoint 膨胀。

manifest/summary 必须同时输出 `Available / Selected / Truncated / FullMarket / Scope`。
`catalog_symbol_limit=0` 且未显式 `allow_truncated_smoke` 时，`Selected<Available` 必须失败。
任何截断只能标记 `TRUNCATED_SMOKE`，并禁止进入 candidate validation、mechanism、strict WF、
exam 或 certified leaf；正式路径只接受 `FULL_MARKET`。

## 9. 倒推候选发现（discovery_budget）

- 最多 **1024** 候选（single cap 900 + dual cap 124）
- 单/双特征 + 分位数网格 `[0.25, 0.5, 0.75]`
- append-only ledger；**validation 前一次性冻结**
- **不要求 payer**；无法解释机制标记 `UNEXPLAINED_CANDIDATE`，允许进入 validation
- 禁止 `pnl/outcome/forward_return/current bar` 进入特征
- `btc_relative_return` / `btc_beta_proxy` / `time_of_day_session` / profile volume 可参与 discovery，
  但 feature membership 永远不能自动解释机制。

**冻结特征公式**（`feature_catalog`）：

| 特征 | 公式 |
| --- | --- |
| `symbol_realized_vol_*` | `sqrt(mean(squared_simple_returns)) * 10000`（RMS）；最短 1 bar，非零 return 时不得恒为 0 |
| `volatility_cost_ratio` | profile 主 horizon RMS vol bps / `roundtrip_taker_fee_bps`（okx_1h→1h，okx_5m→5m） |

discovery summary 必须区分：

- `declared_distinct_feature_names`
- `available_profile_feature_slots`
- `covered_feature_names`
- `missing_declared_feature_names`

**PhenomenonID**（`phenomenon_id`）：同 `profile/coarseSky/horizon/direction/conditions` 为同一 phenomenon；
**忽略 capital tier**；`liquidity_bucket` / `symbol_class` 为 subpaths 并报告相关簇。

枚举冻结为 **feature × cell 二维 round-robin**：

- 顺序为 `allocation_round → feature_slot → profile → next_fair_cell`。
- 每个 profile 的所有可用 feature 获得第 N 条 registered allocation 前，任何 feature 不得获得第 N+1 条。
- cell 轮转覆盖 profile / horizon / direction / capital tier / cell，禁止 feature-major 前缀耗尽。
- invalid/duplicate 进入 ledger，但不增加 feature registered allocation；checkpoint 保存 feature 与 cell 双 cursor。

## 10. validation discovery-screen（BH-FDR q=0.10，非最终 Holm）

独立 validation 区重新计算冻结规则：**先 deterministic gates，再 BH-FDR discovery-screen**。

| 门 | 阈值 |
| --- | --- |
| 最少机会数 | 20 |
| 最少跨日 | **10**（profile 统一；优先于 1h10/5m8 分拆） |
| 最少跨 symbol | 3 |
| fee_net / conservative_net 均值 | ≥ 0 |
| 单事件贡献上限 | 25% |
| **按 block/day 正贡献上限** | **35%** |
| delay+1bar | 必须 |
| cost stress ×1.5 | conservative_net 仍 ≥ 0 |

统计 screen（在 deterministic gates 之后）：

- 按 **candidate horizon** 独立构造 UTC anchored **24h observation + horizon gap** 非重叠 blocks
- 至少 **8** 个 nonzero blocks
- 对 nonzero block conservative net 做 **one-sided positive** exact binomial sign p（log-gamma + log-sum-exp）
- 全 frozen candidate family 上 **Benjamini-Hochberg FDR q=0.10**
- validation **不做最终 Holm**（Holm 仅 strict WF 阶段）

样本不足 → `INCONCLUSIVE`。

## 11. 最小 mechanism 归因

归因状态只有：`EXPLAINED` / `MECHANISM_HYPOTHESIS` / `UNEXPLAINED` /
`AMBIGUOUS` / `GRAVEYARD`。仅允许两个机械 conjunction 提出假设：

| 模板 | 必需特征 | payer / forced deadline | capacity defense |
| --- | --- | --- | --- |
| `cross_asset_beta_rebalance_lag` | `btc_relative_return` + `btc_beta_proxy` | `delayed_beta_hedgers` / `benchmark_or_beta_rebalance_deadline` | `fragmented_alt_liquidity` |
| `scheduled_session_liquidity_transfer` | `time_of_day_session` + (`quote_volume_24h_log_bucket` 或 profile 对应 volume) | `time_constrained_session_flow` / `session_execution_window` | 未预注册 |

裁决顺序：

1. 声称 G1-G3 graveyard mechanism → `GRAVEYARD`。
2. 条件包含 G1-G3 graveyard 特征族 → `UNEXPLAINED`，不得继承历史 payer。
3. 同时命中两个新模板 → `AMBIGUOUS`。
4. 只命中一个模板，且 mechanism claim 为空或逐字匹配 → `MECHANISM_HYPOTHESIS`。
5. 未来独立 evidence 满足新契约并由新 generation 绑定 → `EXPLAINED`。
6. 其余 → `UNEXPLAINED`。

G4 没有独立强制流或真实对手方证据，因此 `feature_membership_auto_explained=false`、
`automatic_explained_count=0`。模板本身也冻结 `trade=false / exam=false / certified_leaf=false`。
未来 evidence 必须包含独立 forced-flow observation、可识别真实 payer/counterparty 证据及时间/来源绑定，
且不得来自 candidate features/outcomes、validation result 或机制名称叙事。

validation pass 但 `UNEXPLAINED` 保留为 `UNEXPLAINED_RESEARCH_PASS`；模板命中则保留为
`MECHANISM_HYPOTHESIS_RESEARCH_RESULT`。两者都严格 **no trade / no exam / no certified leaf**。
`AMBIGUOUS` 与 `GRAVEYARD` 关闭。

## 12. 严格 WF 认证

只有未来独立 evidence 标记为 `EXPLAINED` 且 validation pass 的候选才可进入对应 profile WF；
G4 当前自动入口固定为 **0**。

| 门 | 严格阈值 |
| --- | --- |
| firing / days / symbols / independent blocks | ≥ 30 / ≥ 10 / ≥ 5 / ≥ 10 |
| fee_net mean | > 0 |
| conservative_net mean | > 0 |
| delay+1bar conservative mean | > 0 |
| cost-stress conservative mean | > 0 |
| 单事件贡献 | ≤ 0.25 |
| capacity fail fraction | < 0.5 |
| 统计门 | UTC anchored nonoverlap blocks；全体入选候选按 Holm-Bonferroni family alpha 0.01 |

每个 candidate horizon 独立建块：24h observation window，随后留出一个 candidate-horizon embargo；
下一块必须满足 `next_start >= previous_end + horizon`。sign p 对非零 block conservative net 做
exact two-sided binomial，并强制使用 **log-gamma + log-sum-exp**，禁止用 `int64` 组合数。

认证状态仅 `RESEARCH_PASS` / `CLOSED` / `INCONCLUSIVE`。`RESEARCH_PASS` 仍是 research-only：
**no trade / no exam / no certified money leaf**；`exam_actions=0`。

## 13. protocol hash 与漂移测试

- `protocol_hash`：`audit.protocol_hash = null` 后对 manifest 做 `datatruth.HashJSON`。
- 当前冻结值：`55d5da85272d9d326c81570a677b47b66ede7da69b14fc58fe4c154c0a1d6425`。
- `internal/routerg4/protocol/protocol_test.go` 强制 manifest ↔ 代码一致，并引用真实 g1/g2/g3 历史 hash 与 stage summary。
- 新增逐字段 mechanism / post-validation / strict-WF 漂移测试，以及叙事硬归因拒绝测试。
- 新增 feature×cell 交集公平、binomial overflow、WF overlap、full-market/truncation 隔离测试。
- 新增 RMS 单 bar vol、BH-FDR discovery-screen、day 正贡献 cap、PhenomenonID dedupe、reversemine 版本绑定测试。
- g4 路由红队 **26** 项见 `red_team.requirements`。

## 14. 本轮边界

本轮仅更新 g4 protocol 四文件；**未实现** profit map 扫描器、倒推引擎、归因执行器或 WF 执行器。
未读取任何密封考卷，未创建/解封/消费任何真实考卷，未接任何下单路径。
