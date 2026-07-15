# G10 非同源野路机制组合

> Portfolio ID: `firstbattle.g10.wild-route-portfolio`
> JSON portfolio hash: `166ea394419544f55e54919216b6e67b31e2d1dfaf11ef27df06bb3686072977`
> Document hash: `733b7ae620b083eb18802bcdcb5c4520b345df7c05ef5da97140105d2efacec8`
> Status: `HYPOTHESIS_LIBRARY_ONLY`
> Routes started: `0`

## 1. 问题重述

本资产不替 G10 找借口，也不选下一条策略。问题是：

> 在不读取候选未来收益、不修改 G10 v1 的前提下，哪些真正非同源的个人 edge 机制值得保留为假设；现有冻结历史能否观察和识别它们；若不能，最早阻断在哪里？

正式 FULL_PREFIX evidence audit 已完成并裁决 `DATA_BLIND`：

- instrument `ctType`：`0/394` 可证明；
- dynamic min-notional：`0/12826` 可证明；
- validation map 只有 `84` 个聚合 cells，没有 event/day/shock identity；
- winner binding 只有 PlaybookHash + opaque source winner IDs hash；
- matched control set、baseline support set、events/days/shock counts 均不可冻结；
- labels 未解盲，observed-failure denominator 为 0。

因此当前不能把任何路线写成可检验、已证伪或值得立即启动。

## 2. 正式审计绑定

- Protocol hash: `dccd7974a7d172e3894174a953c4cce02ea840a48bb41fbdec589ed4d1153dd3`
- Source manifest hash: `c8c5f38e6542855aa6fbc8e3373a751a24646922d0838a9774fce4a7e41a9379`
- Audit summary hash: `cf9da460a7807c4d131bbc1e2b22ec9c0e860902fbb6a9726707d7c697b9d9ab`
- Audit receipt hash: `29494eb6dfc62c8317315f93391aa60bd9d4f7ae515a166f94ae0a28c99b4f9a`
- Classification evidence hash: `8e5846c730a292645eedb63298c3535587d95cc9a13b3cd3cf88b2aa30d7fb18`
- Winner/control binding hash: `70a5dd09190e384ace12a4179a0ef04ead76128c1ebc4d280d3e007878728c39`
- Support counts hash: `890cb74d1aaf35f86f3309bc925fc373399916b1f92539d4a973d592554e228d`

## 3. 竞争假设与最强反例

竞争假设：

1. `H0_NULL_SELECTION_AND_COST`：个人残差只是选择偏差、数据缺口、成本低估或配对失败，没有可复现净 edge。
2. `H1_RULE_DISCONTINUITY`：交易所离散规则和门槛制造微小容量机会。
3. `H2_SPARSE_FORCED_FLOW`：可验证的强制行动在稀疏窗口产生付款者。
4. `H3_MICRO_EXECUTION_CLIFF`：小单 fill/冲击/流程存在大资金无法线性复制的容量断层。
5. `H4_CROSS_VENUE_ASYMMETRY`：非纯速度型的场所规则、抵押品或授权差异产生错位。

最强反例：机构可以用统一基础设施在许多标的和场所复制小单，将单事件小容量聚合成机构级容量。公开后若不存在硬容量、权限、流程或维护成本约束，“机构不知道”“策略新奇”“单市场容量小”都不是护城河。

## 4. Outcome-blind 排序

排序权重固定为：

- data fitness 30%；
- identifiability 25%；
- personal executability 20%；
- institutional exclusion strength 15%；
- short falsification cost 10%。

禁止使用 future return、realized PnL、winner profit、G10 residual magnitude 或近期实盘表现。

排序结果：

1. `micro_execution_capacity_cliff`，2.85：`RETAIN_TOP_PRIORITY_NOT_STARTED`
2. `exchange_rule_discontinuity`，2.775：`RETAIN_SECONDARY_NOT_STARTED`
3. `sparse_forced_flow`，2.175：`RETAIN_DORMANT_NOT_STARTED`
4. `cross_venue_operational_asymmetry`，1.65：`REJECT_NEAR_TERM_ARCHIVE_ONLY`

这不是“都值得做”：前两条只因已有字段更接近低成本证伪而保留；强制流休眠；跨场所路线因数据最弱且易退化为机构速度套利而近期开除。没有路线满足启动门。

## 5. RouteCard 1：个人执行 / 容量断层

`route_id`: `micro_execution_capacity_cliff`

机制：微小订单可能在 fill probability、冲击、排队毒性和授权摩擦上优于放大订单；付款者是需要立即成交或撤出流动性的对手方。

机构排斥必须来自每事件硬容量、固定审批/监控成本和大单自冲击，不能来自“不知道”。公开后若可统一批量化并聚合，机制关闭。

现有数据：

- `DIRECT`：maker attempt identity/decision time、显式 maker filled/wait；
- `WEAK`：shadow decision-time depth/spread；
- `MISSING`：attempted notional、partial size、ack/cancel/fill 时钟、稳定 lifecycle ID、event identity、ctType/dynamic min-notional。

合法代理：maker fill/wait、部分 settlement fee/slip、shadow depth/spread；这些不能代替 order size、queue 或完整 attempts 分母。

赢家 + 匹配输家：应在冻结 attempt identity/notional 后，以同 symbol/session/spread/depth/notional bucket 和相同 maker action 匹配全部 attempts。当前 event/notional identity 缺失，设计状态 `UNAVAILABLE_EVENT_IDENTITY`，不得执行。

最强替代解释：高 fill 只是报价更宽松、行情顺行或系统选择性记录。

最短证伪：只检查现有冻结历史是否存在 stable lifecycle、attempt size、partial size、ack/cancel/fill clocks 和 event identity；缺一即停，不读取收益。

预算：1 CPU、8 小时、1 次、仅冻结历史。重开仅限当前已有但未纳入的完整 order lifecycle 历史；不得等待未来。

当前分类：`DATA_BLIND`。下游风险：`UNIDENTIFIABLE / POSTHOC_ONLY / UNEXECUTABLE`。

## 6. RouteCard 2：交易所规则 / 离散门槛

`route_id`: `exchange_rule_discontinuity`

机制：ctVal、lot、tick、min size、结算、迁移或风控规则的离散变化，可能使微小账户位于可执行的一侧，而大资金跨过容量、保证金或最小收益贡献断点。付款者是被规则迫使调整订单、仓位或抵押品的一侧。

机构排斥必须来自硬规则、每事件容量和组织固定成本。机构若能自动分拆且不增加成本，个人护城河失败。

现有数据：

- `DIRECT` 但非历史 PIT：当前 snapshot 的 ctVal/lot/tick `394/394`；
- `MISSING`：ctType `0/394`、dynamic min-notional `0/12826`、规则 effective_at/KnownFrom、event identity；
- exchange fill instId/instType 只能语义交叉检查，不能补造 ctType。

赢家 + 匹配输家：规则版本冻结后，匹配同 symbol/version/session/volatility/direction、位于同一离散边界两侧的所有 outcome-blind events。当前没有规则版本与事件 identity，状态 `UNAVAILABLE_EVENT_IDENTITY`。

最强替代解释：边界差异只是连续价格/波动、metadata 回填或幸存标的选择。

最短证伪：仅搜索当前已有不可变历史是否同时含 ctType、effective_at、KnownFrom 和 event identity；不全即停。

预算：1 CPU、8 小时、1 次。重开仅限已存在且来源时间可验证的历史 instrument/rule archive。

当前分类：`DATA_BLIND`。下游风险：`UNIDENTIFIABLE / POSTHOC_ONLY`。

## 7. RouteCard 3：稀疏强制流

`route_id`: `sparse_forced_flow`

机制：清算、到期、保证金、规则整改或抵押品短缺迫使一侧在短窗口成交。个人只在小容量且付款者仍存在时提供或撤回流动性。

机构排斥可能来自事件稀疏、单事件容量小、全天候授权/监控固定成本和大单自冲击；不能依赖秘密或纯速度。

现有数据：

- `WEAK`：OHLCV extremes、funding、Rubik taker-flow 等候选代理；
- `DIRECT` 但选择性：部分 settlement/maker 执行成本；
- `MISSING`：forced actor/action identity、event+receive time、payer disappearance、完整 event denominator、event/day/shock identity。

价格 shock、funding 或 taker flow 不能证明 forced payer。

赢家 + 匹配输家：先用 forced action identity 定义完整事件，再匹配同 action type、liquidity、direction、intensity、session 和 prior volatility。当前状态 `UNAVAILABLE_FORCED_EVENT_IDENTITY`。

最强替代解释：所谓强制流只是新闻、一般高波动或公共 momentum/reversal。

最短证伪：盘点现有历史是否含稳定 forced event ID、event/receive time、方向/规模和机制结束标记；缺一即停。

预算：1 CPU、12 小时、1 次。重开仅限当前已存在且能形成完整事件分母的合法 forced-flow 历史；不得等待未来 liquidation。

当前分类：`DATA_BLIND`。下游风险：`UNIDENTIFIABLE / POSTHOC_ONLY / INSUFFICIENT_SUPPORT`。

## 8. RouteCard 4：跨场所运营非对称

`route_id`: `cross_venue_operational_asymmetry`

机制：非纯速度型的合约规格、抵押品、授权、结算或最小下单差异，使一个场所被迫调整，另一个场所可用小单承接。

机构排斥必须来自资金分割、准入/授权、硬容量和维护成本。纯 quote 差或亚秒速度优势不是个人 edge。

现有数据：

- `WEAK`：shadow xgap/xliq、equity snapshots；
- `MISSING`：同步双边 executable quote/depth、双腿 fee/slip/latency、各场所 ctType、collateral/borrow/transfer/authorization state、跨场所 event identity。

赢家 + 匹配输家：需先冻结同步双边可成交、资金已就位的完整事件分母，再匹配同 venue pair/spec/session/depth/collateral state。当前状态 `UNAVAILABLE_CROSS_VENUE_EVENT_IDENTITY`。

最强替代解释：价差不可成交、时钟不同步、资金不可调度，或本质上是机构更擅长的速度套利。

最短证伪：仅盘点现有历史是否同步覆盖双边 quote/depth、venue fee、ctType 和资金授权；缺一即停。

预算：1 CPU、6 小时、1 次。当前近期开除；只有已存在、时钟可校验且机制非速度型的多场所历史才能重开。

当前分类：`DATA_BLIND`。下游风险：`UNIDENTIFIABLE / UNEXECUTABLE`。

## 9. Mechanism Hypothesis Library

未充分证伪路线及重开条件已写入 JSON：

- 执行容量断层：保留第一优先，但不启动；
- 规则离散门槛：保留第二优先，但不启动；
- 稀疏强制流：休眠；
- 跨场所非对称：近期拒绝，仅归档。

Library 不赋予交易权、考试资格或自动立项权。任何路线只有在“当前已有历史”满足其重开条件后，才能提交独立新 generation 预注册；仍需用户批准。禁止等待未来数据。

## 10. 决策与安全

- routes started: `0`
- routes retained: `3`
- routes archived near-term: `1`
- current action: `STOP`
- `exam_actions=0`
- `trade_allowed=false`
- `system_status=NO_GO`

G1–G10 frozen protocol/artifacts、records、考卷和真钱开关均不可修改。

## 11. Hash 复算

JSON：将 `hashes.portfolio_hash` 置为 `null` 后使用仓库 `datatruth.HashJSON`。

Markdown：将唯一一行 `> Document hash: \`<value>\`` 规范化为
`> Document hash: \`SELF_EXCLUDED\``，保持其余 UTF-8 LF 字节不变，再做 SHA-256。
