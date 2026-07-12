# 分层路由 g2 预注册协议（firstbattle.hierarchical_router.g2）

> 本文件是研究者可见的公开协议说明，与
> `research/firstbattle/hierarchical_router_g2_preregistration.json`（机器可读真相）配套。
> 冻结的数值/结构以 JSON 为准；本 md 只做解释，不引入新的可执行阈值。
>
> g2 是新 generation、新 protocol hash。协议冻结后，修改节点、司天监特征、成本、候选定义或任一
> 绑定 hash 都必须开新 generation（新 protocol hash）。

## 1. 目标与硬边界

- 唯一目标：按大行情（司天监）与可执行域（分拣器）分层，寻找具体机制下**已认证**的条件方向赚钱叶子；无认证条件叶即 `NO_TRADE`。
- **不寻找**跨行情的永久单方向万能策略；方向只存在于 `SkyState`、机制、付款者与可执行条件绑定的叶子内。
- fail-closed：未知、歧义、反向冲突、未认证、OOD 一律 `NO_TRADE`；缺数据只关闭**相关叶**，不停整棵树。
- 只用决策时刻可见、**已完整收盘**的字段，不等待未来样本。
- 发现器（正向机制 / 逆向奖金地图）只能**提名候选**，不能批准叶子。
- 本轮只驱动 research/WF 路由，**不接生产下单路径**，不创建/解封/消费任何真实考卷。

## 2. 关闭并引用 g1（closes_g1，只引用不可修改）

g2 永久冻结 g1 并**只引用、不修改**其 hash 与零候选结论：

| g1 引用项 | 值 |
| --- | --- |
| g1 generation | `firstbattle-router-g1` |
| g1 router protocol hash | `0eab9bda13055209c81d2b47f0286a0317b79dbb642592ec1b00cd4771397d31` |
| g1 认证赚钱叶 | `0` |
| g1 `RESEARCH_PASS` 叶 | `0` |
| g1 system_status | `NO_GO` |
| g1 CLOSED（2 个 funding 组合） | `backward.negative_funding_receiver_release_short` / `backward.positive_funding_receiver_release_long` |
| g1 UNTESTABLE（4 个强平叶） | `panic_liquidation.{relay,exhaustion}.{long,short}_squeezed.*` |

`protocol_test.go` 的 `TestG1ResultReferencedImmutable` 从 `router_stage_summary.json` 复核 g1 仍是
0 提名 / 0 `RESEARCH_PASS` / `NO_GO`，防止用 g2 悄悄改写 g1 结论。禁止修改 g1 阈值、改名、换皮重考或复活
`CLOSED/UNTESTABLE` 叶。

## 3. 绑定的既有契约（binds）

路由协议绑定但**不篡改**以下契约；任一 hash 变化都必须新 generation：

| 绑定项 | hash |
| --- | --- |
| 历史目录 DataHash | `f41ed2586d91054c4b5b2e15e60f32a4d64ccde0032e76dfdc5fc6dee316379c` |
| 历史行情契约（history_regime_contract） | `a80978f75a74d68a8ac8dcc988ea2e16b01623f739ebab23428cf92f23fd7127` |
| 历史逐叶数据需求（history_data_requirements） | `07b709f9ddd036749da76d28c66e7efe70638cec7c15bbe1b29789a8534622f7` |
| g1 路由协议（firstbattle.hierarchical_router） | `0eab9bda13055209c81d2b47f0286a0317b79dbb642592ec1b00cd4771397d31` |

`preregistration`/`protocol_test.go` 从被引用契约实际复现前三者与 g1 router hash（regime/requirements 通过
`researchboundary` 加载校验，g1 router 通过 `datatruth.HashJSON(audit.protocol_hash=null)` 复现），防止绑定到
与真实契约不符的 hash。

## 4. 冻结的 g2 路由结构

运行时漏斗严格按 `node_order` 推进：

```text
DataTruth → SkyBureau（司天监） → Sorter（分拣器） → MechanismFamily → ConditionalLeaf（条件方向叶子）
```

| 节点 | 决策时刻输入 | fail-closed |
| --- | --- | --- |
| DataTruth | 三时钟 / PIT / 完整性 / 去重 | invalid/missing/late/correction/duplicate/out_of_order/unknown → `NO_TRADE` |
| SkyBureau | `SkyState` / OOD | unknown / OOD / missing → `NO_TRADE`；`SkyState` 只表示行情，不隐含方向 |
| Sorter | 数据完整性 / 历史覆盖 / 决策前流动性 / 波动成本比 / 单币与相关簇容量 / 机制字段 | OOD/歧义/不可执行/缺机制字段 → 拒绝该叶；缺 depth 不阻塞只需 OHLCV/funding 的叶 |
| MechanismFamily | 机制家族 id / 可观测 payer | unknown_family / no_payer → `NO_TRADE`；多家族树可并行 |
| ConditionalLeaf | `SkyState/SorterDomain/Mechanism/Variant/Direction` / 叶状态 | 无匹配/歧义 → `NO_TRADE`；`CLOSED/UNTESTABLE`→`LEAF_CLOSED`；叶数据缺失→`LEAF_DATA_MISSING`；未认证→`NOT_CERTIFIED` |

条件叶规则：同一机制可在不同 `SkyState` 走相反方向，其余行情 `NO_TRADE`；**禁止**把不同 `SkyState`
合并成一个全市场方向后再裁决。叶 ID 由 `internal/routerg2/candidates.CanonicalID` 编码
`SkyState/SorterDomain/Mechanism/Variant/Direction`。

## 5. 司天监行情层（sky）

`internal/routerg2/sky` 只用**已完整收盘**的历史 bar（`open_time + timescale <= decision_time`）计算五个维度，
绑定 `history_regime_contract`：

| 维度 | 输入 | 回看 | 定义 |
| --- | --- | --- | --- |
| `btc_return_24h` | `okx_1h:BTC-USDT-SWAP` | 24h | 首尾已收盘价的有符号收益 |
| `btc_trend_efficiency_24h` | `okx_1h:BTC-USDT-SWAP` | 24h | Kaufman 效率比（净位移 / 路径长度） |
| `btc_realized_volatility_24h` | `okx_1h:BTC-USDT-SWAP` | 24h | 1h 对数收益平方和的平方根 |
| `btc_drawdown_168h` | `okx_1h:BTC-USDT-SWAP` | 168h | 窗口峰值到最新收盘的回撤 |
| `okx_btc_funding_state` | `okx_funding:BTC-USDT-SWAP` | 24h | 决策前已结算 funding 均值 |

- 时间尺度 `timescales_hours = [1, 4, 24]`，分桶 `research_quantiles`，分位 `[1/3, 2/3]`（两切点→三桶 low/mid/high）。
- **分桶切点只在 G2 discovery research 区按特征分布拟合一次（`Fit`），不读取任何收益**；`FeatureSet` / `Cutpoints`
  结构上不含收益字段，无法泄漏 PnL。
- 切点冻结（`Freeze`）后**原样应用**到 forward/WF（`Classify`），并可用 `Cutpoints.Hash()` 证明同一套切点；
  禁止用当前 universe 回填历史，禁止用考试结果反向修改分类。
- 数据缺失或支持域不足一律 `OOD`（`missing_action = OOD_NO_TRADE`）；`SkyState` 只表示行情，不隐含方向
  （标签中不出现 `long/short`）。

## 6. 发现预算与双向候选

- 结果 horizon `outcome_horizons_hours = [1, 4, 24]`，`long/short` 对称（`direction_symmetry = long_short_symmetric`）；
  冻结成本 `cost_bps = 12`、中性带 `neutral_band_bps = 3`。
- 正向（`forward`）：`max_families = 8`、`max_candidates_per_family = 4`；候选必写清 payer、被迫行为、为何不能等待、
  竞争为何不能抹平、可观测 invalidator、期限、适用 `SkyState`、sorter 支持域、方向规则；无 payer/期限/invalidator 直接拒绝。
- 逆向（`backward`）：仅在 `DISCOVERY_RESEARCH_ONLY` 分区；`max_files = 512`、`max_lines = 5,000,000`、
  `max_search_combinations = 256`、`max_candidates = 32`、`checkpoint_required = true`、`signed_event_manifest_required = true`、
  `seed = 20260712`、`permutation_draws = 50000`；配对数 ≤20 时做 exact sign-flip permutation，
  配对数 >20 时做 50,000 次 Monte Carlo，使最小 p 值低于 162 组合的 Bonferroni 门槛；冻结 162 个
  `BTC return × realized volatility × BTC funding state × horizon × direction` 组合，触发子为 `funding_sign_flip`；
  匹配规则 `same_sky_state_symbol_liquidity_direction`；处理组含规则触发的全部事件；对照必须
  loss/neutral、同簇一对一、不得跨簇复用或用 winner；记录搜索空间/目录派生/发现 hash 与全部搜索/失败组合；
  找不到 payer/结构机制标 `NARRATIVE_ONLY` 并关闭，不得直接 `RESEARCH_PASS`。
- `max_total_candidates = 64`。正向与逆向同构候选统一进入不可变注册表去重、单向审判。

## 7. 时间分区角色（只公开 research/WF，不暴露考试范围）

- `discovery_research`：早段 research 区，奖金地图发现与司天监分桶拟合都只在此区；
- `forward_falsification`：后段 public research 区，候选冻结后正向重算；
- `walk_forward`：独立历史分片，时间 purge/embargo，symbol/market-cluster block bootstrap；
- 时间比例固定为 60% / 20% / 20%，discovery→forward、WF purge/embargo 均为 168h；
- 考试范围 `exam_scope_hidden = true`，不在协议中暴露。

## 8. 多重检验与失败标准

- 多重检验总预算 `family_wise_alpha = 0.01`：backward 使用 Bonferroni、预算 0.005；
  forward/WF 使用 Holm-Bonferroni、预算 0.005；两者以 union bound 合并。
  每个已注册搜索组合都是一个检验（当前 162，硬上限 256）；禁止 post-hoc 变体、禁止只登记赢家、
  **拆分搜索空间不能重置 alpha**。
- 失败标准：未知/歧义/冲突/未认证/OOD→`NO_TRADE`；无 payer/期限/invalidator→拒绝；只看赢家无匹配对照→拒绝；
  跨簇/复用/winner 对照→拒绝；permutation/placebo 假 edge 不得晋级；逆向跳过正向证伪→拒绝；同构重复→拒绝；
  复活 g1 funding 规则或强平语义→拒绝；current-bar/决策后特征泄漏→拒绝；样本稀疏→`INCONCLUSIVE`（非通过）；
  天道叙事/架构完成度/混合行情整体正负均不能覆盖单条件叶的真实结论。

## 9. protocol hash 与漂移测试

- `protocol_hash`：把 `audit.protocol_hash` 置 null 后对 manifest 做 `datatruth.HashJSON`（键序/空白无关，可逐字节复现）。
- 当前冻结值：`395d15ece40050e7227e0cff5a74a91c68cdab83285c02a10657d13f21a118ec`。
- `internal/routerg2/protocol/protocol_test.go` 强制：
  - `TestManifestCodeConstantsNoDrift`：节点顺序、叶子状态、reason 码、司天监分桶、发现预算、多重检验与红队条数 manifest↔代码逐字段一致；
  - `TestBoundHashesMatchCodeConstants` / `TestBoundHashesMatchReferencedContracts`：绑定 hash 与常量及真实契约（含 g1 router hash）一致；
  - `TestProtocolHashMatchesManifest`：protocol hash 可复现且已填入常量；
  - `TestG1ResultReferencedImmutable`：g2 只引用不修改 g1 零候选结论。
- g2 路由红队 14 项见 `red_team.requirements`（测试断言恰为 14 项，逐项由代码或 manifest 强制）。

## 10. 本轮边界

本轮已实现 `internal/routerg2/{protocol,sky,sorter,candidates,research}`、
`internal/routerdiscovery/g2prize` 与 `cmd/router-g2-*`，并完成 bounded 历史 research smoke；
未读取任何密封考卷，未创建、解封或消费任何真实考卷，未接任何下单路径。
只有出现 `RESEARCH_PASS` 且独立考官导入用户预留 commitment 后，才制定下一阶段的固定顺序密封考试计划。
若 g2 仍无 `RESEARCH_PASS`，完整关闭 g2，再选择不同机制族建立 g3；禁止围绕失败叶继续调参。
