# 分层路由 g6 预注册协议（firstbattle.hierarchical_router.g6）

> 本文件是研究者可见的公开协议说明，与
> `research/firstbattle/hierarchical_router_g6_preregistration.json`（机器可读真相）配套。
> 冻结的数值/结构以 JSON 为准；本 md 只做解释，不引入新的可执行阈值。
>
> g6 是新 generation、新 protocol hash。协议先冻结小资金事件机会边界、事件队列、微仓 risk 定额与
> StopError 动态退出；**研究阶段 exam_actions=0**，不创建/解封考卷。

## 1. 目标与硬边界

- 唯一目标：**小资金可执行事件机会**——从历史净赢家事件倒推条件，独立历史全触发验证，事件队列微仓 sizing，论点证伪或事件完成动态退出。
- **永久冻结 G1–G5**；G5 结论是 0 policy 退化 `TRAINING_REJECTED_DEGENERATE`、`NO_GO`，不得复活动态线性策略。
- 目标本金 **30/100/1000U**，真实硬上限 **5 倍**；研究仓位按事件 stress risk 定额，优先微仓而非用满杠杆。
- 不拼亚秒速度；事件决策和执行尺度限定 **5m–24h**，下一根可成交价入场，delay **0–3** 全测。
- 继续逆向思维：先从历史净赢家事件倒推条件，再回到该条件触发的全部 winner/loss/neutral 做正序验证。
- 不要求策略承载机构资金；几十至几千 U 的容量即可，但必须通过真实最小下单、手续费、滑点和偿付门。
- 无合法预留隐藏卷时 **`EXAM_UNAVAILABLE/NO_GO`**，不等待未来数据。
- 默认 **Composer 2.5** 承担实现；所有真实扫描默认低 CPU：`GOMAXPROCS=1`、低优先级、分片 checkpoint。

## 2. 关闭并引用 g1 / g2 / g3 / g4 / g5（只引用不可修改）

| 引用项 | g1 | g2 | g3 | g4 | g5 |
| --- | --- | --- | --- | --- | --- |
| generation | `firstbattle-router-g1` | `firstbattle-router-g2` | `firstbattle-router-g3` | `firstbattle-router-g4` | `firstbattle-router-g5` |
| router protocol hash | `0eab9bda…7d31` | `395d15ec…118ec` | `1628e1a5…7fb` | `55d5da85…6425` | `bfe9f784…3a7d` |
| 搜索/规则/候选/策略 | — | 162 组合 | 192 规则 | 1024 候选 | 0 policy |
| validation pass | — | — | — | **0** | SKIPPED |
| system_status | `NO_GO` | `NO_GO` | `NO_GO` | `NO_GO` | `NO_GO` |

## 3. 冻结事件单元与事件族

| 组件 | 冻结值 |
| --- | --- |
| event_id | `symbol + decision_time + event_family` |
| 窗口 | 同标的事件窗口**不重叠**时可重复 |
| shock cluster | 按共同市场 shock/机制 episode 分组；相邻 bar 不能当独立样本 |
| 主 OHLCV 事件族 | `ohlcv_extreme_continuation` / `ohlcv_extreme_reversal` / `session_liquidity_recovery` / `btc_relative_dislocation` |
| optional context | funding spread、Rubik taker/LS ratio（缺失不阻塞主事件） |
| UNTESTABLE | liquidation / depth / open_interest / xvenue |
| 方向 | long / short |
| horizon | okx_5m: 5/15/60/240m；okx_1h: 1/4/24h |
| delay bars | 0, 1, 2, 3 |
| 本金档 | 30 / 100 / 1000 USDT |

## 4. 小账户事件队列与仓位

| 组件 | 冻结值 |
| --- | --- |
| 30U 最大并发 | **1** 个事件仓 |
| 100U 最大并发 | **2** 个事件仓 |
| 1000U 最大并发 | **3** 个事件仓 |
| 单事件 stress loss | ≤ **0.5%** equity |
| 总 stress loss | ≤ **2%** equity |
| 有效杠杆 | 每 bar MTM 后复算 Σevent_notional/equity；>5× breach |
| 队列排序 | `conservative_edge / stress_loss / margin_time` 降序 |
| 同一事件 | 只持有一个仓 |
| 最小下单 | 不足最小名义/张数直接跳过，**禁止向上凑单** |
| pending 执行 | 按决策时 notional 在执行时重验；`FlushMatured` 以**当前 bar index** 到期，禁止提前一 bar |
| 窗口非重叠 | `profile_bar_duration × (1 + delay_bars + horizon_bar_count)`，禁止硬编码 12h |

## 5. StopError 动态退出

- 固定 horizon 只作 **deadline fallback**。
- 主要退出必须来自：**事件完成**、**payer 消失**、**invalidator**、**OOD** 或 **deadline**。
- 统一出口：**StopError**；机械价格止损仅作独立偿付防线，不替代论点退出。

## 6. 时间分区与成本

- 每 profile 独立 **60/20/20 research/validation/WF**
- purge/embargo = `max(lookback, event_deadline)`
- 成本模型**继承 G4** conservative 代理（fee + floor + capacity + bar range + funding）
- validation/WF 额外 delay+1bar 与 cost_stress_multiplier=**1.5**

## 7. 事件 validation 门

| 门控 | 冻结值 |
| --- | --- |
| 适用分区 | validation only，不读 discovery labels / WF |
| 初始本金 | 30 / 100 / 1000 USDT（**三档必须分别通过**） |
| delay 网格 | 0, 1, 2, 3 |
| 成本压力 | ×1.5 |
| 最小样本 | ≥ **50** 事件、≥ **20** 独立日期、≥ **10** non-overlap shock blocks |
| 稀疏样本 | 未达标 → `INCONCLUSIVE`（**明确非 VALIDATION_PASS**） |
| 净收益 | base 与全部 delay 场景 > 0；cost stress > 0 |
| 杠杆/偿付 | 无 breach |
| 单日/单事件正贡献 | 各 ≤ 35% |
| FDR | discovery-screen BH-FDR；family = **registered EventCards + reverseevent ledger 全部 attempt**（含 rejected/duplicate）；receipt 绑定 family size |
| 状态 | VALIDATION_PASS / VALIDATION_FAIL / INCONCLUSIVE |

## 8. 严格 Walk-Forward+ 与机制审查

| 组件 | 冻结值 |
| --- | --- |
| 入口 | 仅 `VALIDATION_PASS` 且 report hash 验证通过；否则 `SKIPPED_VALIDATION_FAIL` |
| 分区 | feature manifest `walk_forward` |
| fold 数 | 5 连续时间 fold |
| EventCard | 冻结，禁止按 WF 标签调参 |
| 回放 | 每 fold 独立事件队列；构建完整 bar 时间轴；在 `CompleteAtBar`/`DeadlineBarIndex` 用对应 open 退出；净值与 `computeOutcome` 一致 |
| WF 扫描 | 纳入全部触发事件（winner/loss/neutral），禁止按 conservative outcome 预筛 |
| fold 门 | 每 fold 净收益 > 0；worst fold > 0；LOFO > 0 |
| 稳健 | day/shock block/symbol leave-one-out |
| 多重比较 | one-sided sign p + Holm-Bonferroni 覆盖 selected EventCard + ledger attempts |
| 机制 | 无 forced-flow/counterparty evidence → 最多 `UNEXPLAINED_EVENT_PASS`；trade/exam/certified=false |
| exam_actions | 0 |

## 9. 公开 exam commitment schema

- **4 standard + 1 golden**，golden 必须最后
- 公开字段仅：`id`, `queue`, `kind`, `family`, `alpha_spend`, `data_hash`, `challenge_hash`, `protocol_hash`, `status`
- **禁止**公开：时间范围、cluster、fixture path、stratum 等隐藏字段
- 每卷一次性消费；crash 亦永久消费
- 当前无合法公开预留卷 → `EXAM_UNAVAILABLE`

## 10. 研究阶段状态

- `exam_actions = 0`
- 无合法预留卷 → `EXAM_UNAVAILABLE`
- protocol hash：`3e97fcc10ae624946b16ae83a31e82bfe5d66cc48527e6337d6742f15b0791af`

## 11. 低 CPU 与 checkpoint

| 组件 | 冻结值 |
| --- | --- |
| GOMAXPROCS | 1 |
| nice priority | 19 |
| checkpoint | 必须；按 symbol 或 shock block 分片恢复 |
| 禁止 | 长链一次跑满服务器 |
