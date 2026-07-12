# 分层路由 g3 预注册协议（firstbattle.hierarchical_router.g3）

> 本文件是研究者可见的公开协议说明，与
> `research/firstbattle/hierarchical_router_g3_preregistration.json`（机器可读真相）配套。
> 冻结的数值/结构以 JSON 为准；本 md 只做解释，不引入新的可执行阈值。
>
> g3 是新 generation、新 protocol hash。协议冻结后，修改节点、司天监特征、特征目录、成本、候选定义或任一
> 绑定 hash 都必须开新 generation（新 protocol hash）。

## 1. 目标与硬边界

- 唯一目标：**结果优先倒序归纳**——先从固定历史 reverse-induction 区枚举资金流调整后的净赚钱事件，再倒推决策前行情与结构特征，随后用未参与归纳的历史分片（recheck / forward / WF）正序证伪；无 `RESEARCH_PASS` 条件叶即 `NO_TRADE`。
- **倒序定义**：`净赚钱结果 → 决策前共同条件 → 付款机制`；规则形成后必须回到该规则触发的全部 winner/loss/neutral 样本裁决，**禁止只总结赢家**。
- **不寻找**跨行情的永久单方向万能策略；方向只存在于 `SkyState`、机制、付款者与可执行条件绑定的叶子内。
- fail-closed：未知、歧义、反向冲突、未认证、OOD 一律 `NO_TRADE`；缺数据只关闭**相关叶**，不停整棵树。
- 只用决策时刻可见、**已完整收盘**的字段，不等待未来样本。
- 发现器（倒序归纳 / 正序证伪）只能**提名候选**，不能批准叶子。
- 本轮只驱动 research/WF 路由，**不接生产下单路径**，**exam_actions=0**，不创建/解封/消费任何真实考卷。

## 2. 关闭并引用 g1 / g2（只引用不可修改）

g3 永久冻结 g1 与 g2 并**只引用、不修改**其 hash 与结论：

| 引用项 | g1 | g2 |
| --- | --- | --- |
| generation | `firstbattle-router-g1` | `firstbattle-router-g2` |
| router protocol hash | `0eab9bda…7d31` | `395d15ec…118ec` |
| 认证赚钱叶 | `0` | `0` |
| `RESEARCH_PASS` 叶 | `0` | `0` |
| 搜索组合 / 提名 | — | `162` 组合 / `0` 提名 |
| system_status | `NO_GO` | `NO_GO` |

`protocol_test.go` 的 `TestG1ResultReferencedImmutable` / `TestG2ResultReferencedImmutable` 分别从
`router_stage_summary.json` / `g2_stage_summary.json` 复核 g1/g2 结论未被 g3 悄悄改写。禁止复活 g1 funding 规则、
g2 `funding_payer_flip_rebalance` 或任何 g1/g2 机制 ID（语义墓地）。

## 3. 绑定的既有契约（binds）

路由协议绑定但**不篡改**以下契约；任一 hash 变化都必须新 generation：

| 绑定项 | hash |
| --- | --- |
| 历史目录 DataHash | `f41ed2586d91054c4b5b2e15e60f32a4d64ccde0032e76dfdc5fc6dee316379c` |
| 历史行情契约（history_regime_contract） | `a80978f75a74d68a8ac8dcc988ea2e16b01623f739ebab23428cf92f23fd7127` |
| 历史逐叶数据需求（history_data_requirements） | `07b709f9ddd036749da76d28c66e7efe70638cec7c15bbe1b29789a8534622f7` |
| g1 路由协议 | `0eab9bda13055209c81d2b47f0286a0317b79dbb642592ec1b00cd4771397d31` |
| g2 路由协议 | `395d15ece40050e7227e0cff5a74a91c68cdab83285c02a10657d13f21a118ec` |

## 4. 七族历史覆盖（data_families）

G3 inventory 保留现有 7 族（catalog 预检只盘点时间覆盖，不读取收益）：

`okx_1h` / `okx_5m` / `okx_funding` / `bybit_funding` / `rubik_taker_1h` / `rubik_taker_5m` / `rubik_lsratio_1h`

覆盖复核显示 `rubik_taker_5m` 约为 `2026-07-07T14:40:00Z` 至 `2026-07-09T14:39:17.232Z`，仅约 48 小时，
不足以支持 40/20/20/20 四分区及相邻 168h embargo。因此它在本代为 `optional_nonblocking` inventory，
不进入最小 FeatureCatalog，**不得生成 G3 规则**；其缺失或覆盖不足不得阻塞其他特征。

## 5. 冻结的 g3 路由结构

运行时漏斗严格按 `node_order` 推进：

```text
DataTruth → SkyBureau（司天监） → Sorter（分拣器） → MechanismFamily → ConditionalLeaf（条件方向叶子）
```

与 g2 同构；机制族由倒序归纳提出，不预先锁定单一 funding 族。叶 ID 由
`internal/routerg3/candidates.CanonicalID` 编码 `SkyState/SorterDomain/Mechanism/Variant/Direction`。

## 6. 司天监行情层（sky）

与 g2 相同的五个 BTC 维度；**分桶切点只在 G3 reverse-induction 区拟合一次**（`fit_on = reverse_induction_partition_only`），
不读取收益；切点冻结后原样应用到 recheck / forward / WF。

## 7. 冻结决策前特征目录（feature_catalog）

禁止 `pnl` / `outcome` / `forward_return` 进入特征列；阈值只来自 induction 区特征分位数 `[0.25, 0.5, 0.75]`，
禁止用收益峰值直接选任意阈值。每规则最多 **2** 个特征（单特征或双特征）。

| 类别 | 特征 |
| --- | --- |
| 价格收益/波动 | `symbol_return_{1h,4h,24h}`、`symbol_realized_vol_{1h,4h,24h}` |
| OKX-Bybit funding | `okx_bybit_funding_spread`、`okx_bybit_funding_sign_divergence` |
| Rubik taker imbalance | `rubik_taker_imbalance_1h`（5m 因覆盖不足排除） |
| Long-short ratio | `rubik_lsratio_1h` |
| 流动性/容量代理 | `quote_volume_24h_log_bucket`、`volatility_cost_ratio` |

`rubik_taker_imbalance_1h` 与 `rubik_lsratio_1h` 的最大 staleness 均冻结为 **2h**；超过 2h 按 `MISSING`
处理，不得沿用旧值生成规则或候选。

## 8. Feature → Economic Mechanism 冻结归因

候选形成前必须按 `feature_to_economic_mechanism_attribution` 归因，不能在看到结果后另编 payer：

| Mechanism feature | family / mechanism | payer 与被迫行为 | cannot-wait / competition defense |
| --- | --- | --- | --- |
| `okx_bybit_funding_spread` / `okx_bybit_funding_sign_divergence` | `cross_venue_funding_pressure` / `cross_venue_funding_pressure_rebalance` | `cross_venue_carry_holders`；venue carry 或 margin rebalance | funding settlement；fragmented venue capital/capacity |
| `rubik_taker_imbalance_1h` | `urgent_taker_impact` / `urgent_taker_impact_reversion` | `urgent_aggressive_takers`；execution urgency 造成临时冲击，随后库存吸收/回归 | execution urgency；temporary impact/limited capacity |
| `rubik_lsratio_1h` | `crowded_leverage` / `crowded_leverage_rebalance` | `crowded_leveraged_side`；margin/carry pressure 驱动去杠杆或再平衡 | margin/carry pressure；fragmented positioning/limited balance-sheet capacity |

- cross-venue funding 机制明确是**同时跨 venue 的 spread/sign divergence**，不是 g1 单 venue
  `funding_post_settlement_release` / receiver release，也不是 g2 相邻 `funding_sign_flip` /
  `funding_payer_flip_rebalance`。
- 价格 return/vol、`quote_volume_24h_log_bucket`、`volatility_cost_ratio` 仅是 conditioner：
  可缩小支持域，不能独立提供 payer 或 mechanism。
- 单/双特征规则含 **0 个 mechanism feature** → `NARRATIVE_ONLY/CLOSED`；包含多个不同 mechanism family
  → `AMBIGUOUS/CLOSED`；同 family 的多个 mechanism feature 允许；一个 mechanism family 加 conditioner
  归因到该 family。关闭规则仍占 alpha ledger。
- deadline 与 invalidator 模板逐 family 冻结：cross-venue 截止于 outcome horizon 内下一相关 funding settlement；
  taker impact 截止于 `decision_time + min(horizon,4h)`；crowded leverage 截止于
  `decision_time + min(horizon,24h)`。spread/符号收敛、taker flow 归一/再加速、LS ratio 去拥挤、
  margin/carry pressure 消散以及 liquidity/capacity OOD 等对应模板触发失效。
- G1/G2 mechanism graveyard 冻结为 `funding_post_settlement_release`、两个 funding receiver release、
  `panic_liquidation`、`funding_payer_flip_rebalance`；ID、别名、改名、阈值变体、sign/flip 重述或语义等价均关闭。

## 9. 发现预算与倒序归纳

- 结果 horizon `outcome_horizons_hours = [1, 4, 24]`，`long/short` 对称；冻结成本 `cost_bps = 12`、中性带 `neutral_band_bps = 3`。
- **倒序归纳（reverse_induction）**：仅在 `REVERSE_INDUCTION_ONLY` 分区；`max_files = 512`、`max_lines = 5,000,000`、
  `max_single_feature_rules = 162`、`max_dual_feature_rules = 30`、`max_derived_search_rules = 192`、`checkpoint_required = true`、
  `seed = 20260713`、`permutation_draws = 100000`、`exact_permutation_max_pairs = 20`、
  `min_induction_clusters = 10`；匹配规则
  `same_sky_state_symbol_liquidity_direction_horizon_cluster`。处理组含规则触发的全部事件；control 选择必须
  **outcome-blind**：只按同层、同 symbol/liquidity/direction/horizon/cluster、规则不触发、一对一及冻结的
  time/event-id 顺序匹配，禁止读取 winner/loss/neutral 标签。非触发 winner 可以成为 control；规则若只有 winner
  且找不到非触发 control 仍拒绝。跨簇复用或使用规则触发事件作 control 均拒绝。
- actual rules 只能在 induction winner index 形成后按冻结算法确定。协议**不预称已注册 192 条规则**；
  `actual_generated_attempt_count` / `actual_derived_rule_count` 均为 null，后续只能由 sealed search manifest
  绑定 attempt/registered 数量、status counts、最终 cursor、canonical rules hash 与 alpha ledger hash。
- v3 induction group 使用 coarse Sky pattern：`ret/rv/fund` 各 low/mid/high，`te/dd='*'`，保留
  `SorterDomain/Horizon/Direction`。本代 sorter domain 冻结为 `liquid`，故上限
  `3^3 × 1 × 3 × 2 = 162`；只枚举 induction 内实际 observed groups。`Rule.Fires` 匹配 coarse pattern，
  但 controls 仍必须匹配同一个**完整** `SkyState`。
- FeatureDerivationOrder 机制优先：`okx_bybit_funding_spread` → `sign_divergence` →
  `rubik_taker_imbalance_1h` → `rubik_lsratio_1h`，之后才是 price/vol/liquidity conditioners，
  防止 price-first 叙事规则耗尽预算。
- 派生算法保持 **bounded streaming**：single 按 `feature → quantile → operator → observed group`
  （group 最内层）round-robin；同一轮每个 observed group 最多先得到一条 attempt，再进入下一轮。
  single 阶段在所有 observed groups 至少有一条 registered single、注册数达到 162、或 single stream
  耗尽时结束；剩余预算才进入 dual，dual cap 30，并按
  `feature pair → quantile pair → operator pair → observed group` round-robin。全程禁止按 PnL/胜率重排。
- 每次只生成一条 attempt，依次 canonicalize → dedupe → validate → mechanism attribution → ledger →
  valid registration。single/dual/total active cap 达到时，在请求下一笛卡尔项前立即停止。停止点后的笛卡尔项统一为 `not_considered`，
  不进入 ledger、不占 alpha，也不得生成其 provenance。
- ledger 只记录实际生成并检查过的 attempt，状态固定为 `duplicate / invalid / registered`：
  registered 占一个检验 charge；duplicate/invalid 未做 outcome test，charge 为 0；`not_considered` 不存在于 ledger。
  不得先 materialize 全部 candidates，也不得复制全笛卡尔 provenance。
- 峰值候选内存要求 `O(max_derived_search_rules + current_group)`；只保留 cap 内 registered rules、
  registered hashes、current-group attempt hashes 与 running ledger hash；group 边界丢弃未注册的 group hash。
  每个 round 结束及 progress interval 必须 checkpoint next cursor、stage、observed/covered groups、
  per-group rule counts、status/registered counts、上述 bounded dedupe hashes 和 ledger hash。
  progress/stage 必须报告 `observed_group_count`、`covered_group_count`、每 group 注册规则数及 single/dual/total counts。
  每条 registered 规则仍必须回到全部 winner/loss/neutral 触发样本。
- 搜索空间 ID：`firstbattle.g3.outcome_first_reverse_induction.v1`；定义 hash 是上述**派生算法/config hash**，
  且包含 bounded streaming、attempt ledger 与 feature→mechanism attribution，不包含 induction 后才知道的具体规则答案：
  `e893269b83ee204bca6b2bb58bba0fec6147827dd4413cea361fe3151fa7fc69`。
- `max_total_candidates = 64`。

## 10. 四分区时间角色（40/20/20/20）

| 分区 | permille | 用途 |
| --- | --- | --- |
| reverse_induction | 400 | 枚举净赚钱事件、倒推特征、司天监拟合 |
| discovery_recheck | 200 | 未参与倒推的分片复核；提名必须先过此区 |
| forward_falsification | 200 | 候选冻结后正向重算 |
| walk_forward | 200 | purge/embargo + cluster bootstrap |

相邻分区 **168h embargo**；`exam_scope_hidden = true`，`exam_actions = 0`。
induction 最少 10 个 cluster；recheck 最少 cluster 由 8 提升为 **10**（最少样本仍为 20）。

## 11. 多重检验与失败标准

- 总预算 `family_wise_alpha = 0.01`（Holm-Bonferroni union bound）：
  - reverse_induction：Bonferroni，`0.004`
  - recheck：Holm，`0.002`
  - forward：Holm，`0.002`
  - walk_forward：Holm，`0.002`
- 每个实际 registered 规则都是一个检验（硬上限 192）；append-only ledger 按 streaming generation order
  记录全部已生成 attempt（含 duplicate/invalid/registered），actual count 取自后续 sealed search manifest。
  未生成的 `not_considered` 后缀不进 ledger/alpha。禁止 post-hoc 变体、禁止只登记赢家、**拆分搜索空间不能重置 alpha**。
- reverse-induction 最坏 Bonferroni 门槛为 `0.004/192 ≈ 2.0833e-5`；100,000 draws 的最小 Monte Carlo
  p 值 `1/100001 ≈ 1.0e-5`，因此 192 规则预算在生产参数下可达。
- 失败标准与 g2 同构并追加：outcome/决策后特征泄漏→拒绝；倒序跳过 recheck/forward→拒绝；复活 g1/g2 机制→拒绝。

## 12. protocol hash 与漂移测试

- `protocol_hash`：把 `audit.protocol_hash` 置 null 后对 manifest 做 `datatruth.HashJSON`（键序/空白无关，可逐字节复现）。
- 当前冻结值：`1628e1a575585a7a1c2b7da0e01eb54de9707997272a6301e3824505dece17fb`。
- `internal/routerg3/protocol/protocol_test.go` 强制：
  - `TestManifestCodeConstantsNoDrift`
  - `TestBoundHashesMatchCodeConstants` / `TestBoundHashesMatchReferencedContracts`
  - `TestProtocolHashMatchesManifest` / `TestSearchDefinitionHashMatchesCanonical`
  - `TestFeatureMechanismAttributionNoDrift`
  - `TestBoundedStreamingDerivationNoDrift`
  - `TestV3CoarseRoundRobinCoverageNoDrift`
  - `TestG1ResultReferencedImmutable` / `TestG2ResultReferencedImmutable`
- g3 路由红队 **21** 项见 `red_team.requirements`，含 noise/outcome-blind controls、192 规则生产可达、
  bounded streaming 内存及 coarse-group round-robin 多行情覆盖检查。

## 13. 本轮边界

本轮仅冻结 g3 协议（manifest + `internal/routerg3/protocol`）；未读取任何密封考卷，未创建、解封或消费任何真实考卷，
未接任何下单路径。若 g3 仍无 `RESEARCH_PASS`，完整关闭 g3；禁止围绕失败规则调参，下一 generation 才能更换特征族或机制范围。
