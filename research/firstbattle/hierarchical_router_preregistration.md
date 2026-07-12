# 分层路由 g1 预注册协议（firstbattle.hierarchical_router）

> 本文件是研究者可见的公开协议说明，与
> `research/firstbattle/hierarchical_router_preregistration.json`（机器可读真相）配套。
> 冻结的数值/结构以 JSON 为准；本 md 只做解释，不引入新的可执行阈值。
>
> 协议冻结后，修改节点、特征、成本、候选定义或任一绑定 hash 都必须开新 generation（新 protocol hash）。

## 1. 目标与硬边界

- 唯一目标：不同行情或特殊情况下走不同、**已认证**的赚钱路径；无认证路径即 `NO_TRADE`。
- fail-closed：未知、歧义、反向冲突、未认证一律 `NO_TRADE`；缺数据只关闭**相关叶**，不停整棵树。
- 只用决策时刻可见字段，不等待未来数据回答策略问题。
- 发现器（正向/逆向）只能**提名候选**，不能批准叶子；运行时树只含决策时刻正向可计算的节点。
- 第一版只驱动 research/shadow 路由，**不接生产下单路径**（`internal/hierarchicalrouter` 不 import 下单模块）。

## 2. 绑定的既有契约（binds）

路由协议绑定但**不篡改**以下契约；任一 hash 变化都必须新 generation：

| 绑定项 | hash |
| --- | --- |
| 历史目录 DataHash | `f41ed2586d91054c4b5b2e15e60f32a4d64ccde0032e76dfdc5fc6dee316379c` |
| 历史行情契约（history_regime_contract） | `a80978f75a74d68a8ac8dcc988ea2e16b01623f739ebab23428cf92f23fd7127` |
| 历史逐叶数据需求（history_data_requirements） | `07b709f9ddd036749da76d28c66e7efe70638cec7c15bbe1b29789a8534622f7` |
| 强平 g1 协议（forced_liquidation.preregistration） | `6b16eb531230a14242f29d398fd357188867062f2fb2f0f0af6b7f554dd83bcb` |

`preregistration_test.go` 从被引用契约实际复现前三者与强平 g1 hash 并与本 manifest 比对
（regime/requirements 通过 `researchboundary` 加载校验，强平 g1 通过
`datatruth.HashJSON(audit.protocol_hash=null)` 复现），防止绑定到与真实契约不符的 hash。

## 3. 冻结的节点顺序

路由漏斗严格按此顺序推进（`node_order`，代码 `NodeOrder`，`Route` 逐层追加到 `trace.Path`）：

```text
DataTruth → GlobalContext → MechanismFamily → PhaseSide → Executability → Certification → PortfolioRisk
```

| 节点 | 决策时刻输入 | fail-closed |
| --- | --- | --- |
| DataTruth | 三时钟 / PIT / 完整性 / 去重 | invalid/missing/late/correction/duplicate/out_of_order/unknown → `NO_TRADE` |
| GlobalContext | 历史行情契约分类 / OOD | unknown / unsupported(OOD) → `NO_TRADE` |
| MechanismFamily | 机制家族 id / 可观测 payer | unknown_family / no_payer → `NO_TRADE`；多家族树可并行产生候选 |
| PhaseSide | selector_type / variant / phase / forced_side（选叶）| 强平叶按 `(phase, forced_side)`；其它机制按预注册 `variant` 与冻结方向；unknown/ambiguous/无匹配叶 → `NO_TRADE`；选中叶后 `CLOSED/UNTESTABLE`→`LEAF_CLOSED`，叶数据缺失→`LEAF_DATA_MISSING`（只关此叶）|
| Executability | cost / depth / latency | invalid / unknown → `NO_TRADE` |
| Certification | 叶子认证状态 | `LIVE_APPROVED`→进入下一节点；`SHADOW_PASS`→shadow-only；其余→`NOT_CERTIFIED` `NO_TRADE` |
| PortfolioRisk | 相关簇 / 反向冲突 / 仲裁规则 | 风险闸拒绝→`NO_TRADE`；反向冲突且无仲裁规则→空仓（账户层 `RouteAll`）|

方向规则（冻结，`expectedDirection` 强制、`NewRoot` 拒绝违反者）：

- `relay`：`direction = forced_side`（顺被迫流）；
- `exhaustion`：`direction = forced_side.opposite`（逆被迫流承接）。

## 4. 叶子状态机

- 全集 `leaf_states`：`UNRESEARCHED / RESEARCH_PASS / EXAM_PASS / SHADOW_PASS / LIVE_APPROVED / CLOSED / UNTESTABLE`。
- 晋级序 `order`：`UNRESEARCHED → RESEARCH_PASS → EXAM_PASS → SHADOW_PASS → LIVE_APPROVED`；未达对应状态不得越级发候选。
- 终止关闭态 `terminal_closed`：`CLOSED / UNTESTABLE`，不可被发现器复活；修改后必须新 `leaf_id` / 新 generation。
- 候选下发：`LIVE_APPROVED`→live 候选，`SHADOW_PASS`→shadow 候选，其余→`NO_TRADE(NOT_CERTIFIED)`。

### 当前叶子清单

四个强平候选叶绑定强平 g1 generation/hash，当前全部 `UNTESTABLE/NO_GO`
（缺可证明历史 liquidation/OI/depth/xvenue/PIT，且**不等待未来样本**），路由到它们必然 `LEAF_CLOSED`：

```text
panic_liquidation.relay.long_squeezed.short      (relay,  forced short → short)  UNTESTABLE
panic_liquidation.relay.short_squeezed.long      (relay,  forced long  → long)   UNTESTABLE
panic_liquidation.exhaustion.long_squeezed.long  (exh,    forced short → long)   UNTESTABLE
panic_liquidation.exhaustion.short_squeezed.short(exh,    forced long  → short)  UNTESTABLE
```

已认证赚钱叶：0；`RESEARCH_PASS` 叶：0；`UNTESTABLE` 叶：4。

## 5. coarse trace 纪律

`DecisionTrace` 只含：instrument、mechanism_family、节点路径、终止节点、`Outcome`、coarse `Reason`、
叶子身份、候选方向。**禁止**出现 `net_edge / pnl / realized_usdt / exam_id / hidden_time_range /
threshold_values / near_threshold_margins / per_trade_returns` 等泄漏子串
（`coarse_trace_rule.must_not_contain`，`TestCoarseTraceNoLeak` 断言序列化后无泄漏）。

冻结的 reason 码全集见 `reason_codes` / `ReasonCodes`。

## 6. 发现预算与单向审判

发现器只提名候选，统一进入不可变注册表，单向审判，不能等待未来数据或直接晋级。

- 正向（`forward`）：`max_families=8`、`max_candidates_per_family=4`；候选必写清 payer、被迫行为、
  为何不能等待、竞争为何不能抹平、可观测 invalidator、期限；无 payer/无期限/无 invalidator 直接拒绝；
  上层 context 用行情契约的 BTC 行情层，不用收益定义行情。
- 逆向（`backward`）：仅在 `RESEARCH_ONLY` 分区定义；`max_files=512`、`max_lines=5,000,000`、
  `max_search_combinations=256`、`max_candidates=32`、`default_progress_every=100000`、
  `checkpoint_required=true`；必须抽取匹配对照、只回溯 decision time 之前完成的数据、
  记录 origin/DiscoveryDataHash/搜索组合数/候选排名/匹配规则；用 permutation/placebo、cluster bootstrap
  与选择税；**每个搜索过的组合都占用多重检验预算，不能只登记赢家**；找不到 payer/结构机制标 `NARRATIVE_ONLY` 并关闭。
- `max_total_candidates=64`。

单向审判（`single_direction_judgment`）：同构候选去重；逆向候选必须在独立正向时间序列重算特征；
任何候选先通过正向证伪才能 `RESEARCH_PASS`；失败候选永久入墓；修改必须新 ID / 新 generation。

## 7. 多重检验与失败标准

- 多重检验：`family_wise_control=Holm-Bonferroni`、`family_wise_alpha=0.01`；每个搜索组合都是一个检验
  （`max_search_combinations=256`）；禁止 post-hoc 变体；禁止只登记赢家。
- 失败标准（`failure_criteria`）：未知/歧义/冲突/未认证→`NO_TRADE`；无 payer/无期限/无 invalidator→拒绝；
  只看赢家无匹配对照→拒绝；permutation/placebo 假 edge 不得晋级；逆向候选跳过正向证伪→拒绝；
  同构重复→拒绝；改冻结候选或改名重考→拒绝；样本稀疏→`INCONCLUSIVE`（非通过）；无认证→`NO_TRADE`；
  天道叙事/架构完成度/金额暂时为正均不能覆盖失败。

## 8. 冲突规则

- 同向多树候选：保留，交账户层处理相关性与资金竞争；
- 反向冲突且无预注册仲裁规则：该 instrument 空仓（`CONFLICT_NO_ARBITRATION`）；
- 禁止按最近谁赚钱临时改变路由顺序。

## 9. protocol hash 与漂移测试

- `protocol_hash`：把 `audit.protocol_hash` 置 null 后对 manifest 做 `datatruth.HashJSON`（键序/空白无关，可逐字节复现）。
- 当前冻结值：`0eab9bda13055209c81d2b47f0286a0317b79dbb642592ec1b00cd4771397d31`。
- `internal/hierarchicalrouter/preregistration_test.go` 强制：
  - `TestManifestCodeConstantsNoDrift`：节点顺序、叶子状态、reason 码、发现预算、多重检验、失败标准 manifest↔代码逐字段一致；
  - `TestManifestLeavesMatchCode`：叶身份/方向/generation/hash/状态与代码一致；
  - `TestBoundHashesMatchCodeConstants` / `TestBoundHashesMatchReferencedContracts`：绑定 hash 与常量及真实契约一致；
  - `TestProtocolHashMatchesManifest`：protocol hash 可复现且已填入。
- 路由红队 12 项见 `red_team.requirements`（测试断言恰为 12 项，逐项由代码或 manifest 强制）。

## 10. 本轮边界

本轮只新增 `internal/hierarchicalrouter/` 与本对协议文件，未修改其它文件；未读取真实收益/考卷/大 history
目录；未创建、解封或消费任何真实考卷；未接任何下单路径。只有出现 `RESEARCH_PASS` 且独立考官导入用户
预留 commitment 后，才制定下一阶段的固定顺序密封考试计划。
