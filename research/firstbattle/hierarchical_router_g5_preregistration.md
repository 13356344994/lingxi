# 分层路由 g5 预注册协议（firstbattle.hierarchical_router.g5）

> 本文件是研究者可见的公开协议说明，与
> `research/firstbattle/hierarchical_router_g5_preregistration.json`（机器可读真相）配套。
> 冻结的数值/结构以 JSON 为准；本 md 只做解释，不引入新的可执行阈值。
>
> g5 是新 generation、新 protocol hash。协议先冻结动态动作空间、连续特征、正则化排序策略边界与公开
> exam commitment schema；**研究阶段 exam_actions=0**，不创建/解封考卷。

## 1. 目标与硬边界

- 唯一目标：**结果优先动态策略学习**——从历史动作收益立方体倒推连续状态下的最优动作，横截面排名选 Top-K，账户级状态转移回放。
- **永久冻结 G1–G4**；G4 结论是 401/401 + 200/200 全市场地图、1024 冻结候选、0 validation pass、`NO_GO`，不得调阈值复活。
- 司天监和分拣器改为**连续软特征/惩罚**；只有数据失真、偿付风险和不可执行才硬拒绝。
- **不使用深度 RL** 作为第一代；只用可解释、可复现的正则化横截面排序策略。
- 无合法预留隐藏卷时 **`EXAM_UNAVAILABLE/NO_GO`**，不等待未来数据。

## 2. 关闭并引用 g1 / g2 / g3 / g4（只引用不可修改）

| 引用项 | g1 | g2 | g3 | g4 |
| --- | --- | --- | --- | --- |
| generation | `firstbattle-router-g1` | `firstbattle-router-g2` | `firstbattle-router-g3` | `firstbattle-router-g4` |
| router protocol hash | `0eab9bda…7d31` | `395d15ec…118ec` | `1628e1a5…7fb` | `55d5da85…6425` |
| 搜索/规则/候选 | — | 162 组合 | 192 规则 | 1024 候选 |
| validation pass | — | — | — | **0** |
| 全市场覆盖 | — | — | — | okx_1h **401/401**, okx_5m **200/200** |
| system_status | `NO_GO` | `NO_GO` | `NO_GO` | `NO_GO` |

## 3. 冻结动作空间

| 组件 | 冻结值 |
| --- | --- |
| verbs | flat / open_long / open_short / hold / increase / trim / close / reverse |
| delay bars | 0, 1, 2, 3 |
| oracle size fractions | 0, 0.1, 0.25, 0.5, 1.0 |
| policy size | continuous [0, 1] risk budget |
| max account leverage | 5 |
| Top-K | 5 |
| effective leverage | 每 bar MTM 后复算 Σfixed_notional/equity；>5× breach，≤0 solvency |
| pending 执行 | 按决策时 NotionalUSDT 在执行时重算 size fraction，不超过批准额度 |

## 4. 连续特征与模型

- Sky 输出：btc_return / btc_vol / btc_trend / btc_drawdown / confidence
- Sorter 输出：liquidity_score / vol_cost_penalty / capacity_headroom
- 横截面 rank：return / vol / liquidity / beta
- 模型：`regularized_linear_pairwise_ranking_action_value`（**禁止 deep RL**）
- 动作编码交互：`context × verb/direction/start_state/numeric`
- 固定动作检测：flat/long/short 代表 context × start_state；拒绝 fixed direction/horizon/verb、hold-forever、never-close、always-reverse
- 冻结 lambda grid、training seed=42、max attempts=256

## 5. 时间分区与成本

- 每 profile 独立 **60/20/20 research/validation/WF**
- purge/embargo = `max(lookback, action_horizon)`
- 成本模型**继承 G4** conservative 代理（fee + floor + capacity + bar range + funding）

## 6. 公开 exam commitment schema

- **4 standard + 1 golden**，golden 必须最后
- 公开字段仅：`id`, `queue`, `kind`, `family`, `alpha_spend`, `data_hash`, `challenge_hash`, `protocol_hash`, `status`
- **禁止**公开：时间范围、cluster、fixture path、stratum 等隐藏字段
- 每卷一次性消费；crash 亦永久消费
- `router-g5-commitments` 只验证公开 JSON 与 gauntlet 状态，**不读取 hidden manifest**

## 7. 研究阶段状态

- `exam_actions = 0`
- 无合法预留卷 → `EXAM_UNAVAILABLE`
- protocol hash：`bfe9f784039a89b773f2f9a0fb86d252c39ffa52b7f7202071e02bcc32333a7d`

## 8. 动态账户 validation 门

| 门控 | 冻结值 |
| --- | --- |
| 适用分区 | validation only，不读 discovery labels / WF |
| 初始本金 | 30 / 100 / 1000 USDT（**三档必须分别通过**，非按小本金优化） |
| oracle/feature 参考本金 | 1000 USDT = 对 30/100 的保守 capacity 上界 |
| delay 网格 | 0, 1, 2, 3 |
| 成本压力 | ×1.5（base delay=0 场景额外测） |
| 净收益 | base 与全部 delay 场景 > 0；三档 equity tier 各自全部场景 > 0 |
| 最大回撤 | ≤ 30% |
| 杠杆/偿付 | 无 breach；breach 后 pending open/increase/reverse fail-closed，允许 trim/close/flat |
| active days | ≥ 20；未达标 → INCONCLUSIVE（**明确非 VALIDATION_PASS**） |
| 单 symbol 正贡献 | ≤ 35% |
| 固定动作拒绝 | fixed direction/horizon/verb、hold-forever、never-close、always-reverse |
| cluster LOO | 按 bar symbol PnL 增量累计，不得重复累计终身值 |
| daily replay EOF | 有 prior decision 时尾窗 ApplyHoldFunding+MarkToMarket 后，equity delta 计入最后 UTC day DailyPnL，symbol PnL 增量计入 ClusterPnL；无 prior decision 则明确跳过 |
| 状态 | VALIDATION_PASS / VALIDATION_FAIL / INCONCLUSIVE |

## 9. 严格 Walk-Forward+ 与机制审查

| 组件 | 冻结值 |
| --- | --- |
| 入口 | 仅 `VALIDATION_PASS` 且 report hash 验证通过；否则 `SKIPPED_VALIDATION_FAIL` |
| 分区 | feature manifest `walk_forward` |
| fold 数 | 5 连续时间 fold；fold 间 purge/embargo = max(lookback, action_horizon) |
| policy | 冻结，禁止按 WF 标签调参 |
| 回放 | 每 fold 独立账户 Top-K；delay 0..3 + cost ×1.5 |
| fold 门 | 每 fold 净收益 > 0；worst fold > 0；LOFO > 0 |
| 稳健 | day/shock/symbol cluster leave-one-out |
| 多重比较 | one-sided sign p + Holm-Bonferroni 覆盖 selected policy + ledger attempts |
| sign-test 回放 | 与 validation scenario 相同 funding settlements + stress；EOF 尾窗计提到最后 UTC day（equity delta→DailyPnL，symbol 增量→ClusterPnL）；无 prior decision 跳过 |
| 机制 | 无 forced-flow/counterparty evidence → 最多 `UNEXPLAINED_POLICY_PASS`；trade/exam/certified=false |
| 输出 | `g5_wf_report.json`, `g5_policy_mechanism_report.json`, `g5_research_summary.json`, `wf_receipt.json` |
| exam_actions | 0 |
| 考试 | 不做（`no_exam=true`） |
