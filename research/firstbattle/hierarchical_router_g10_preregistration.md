# 分层路由 g10 预注册协议（firstbattle.hierarchical_router.g10）

> 本文件是研究者可见的公开协议说明，与
> `research/firstbattle/hierarchical_router_g10_preregistration.json`（机器可读真相）配套。
> 冻结的数值/结构以 JSON 为准；本 md 只做解释，不引入新的可执行阈值。
>
> g10 是新 generation、新 protocol hash。协议交付**专属小资金野路子 Brain**——不再寻找公共因子或机构可扩展策略，
> 而是在每个市场时刻根据账户权益、真实执行条件、事件状态和历史相似案例动态选择 8 类动作。
> **研究阶段 exam_actions=0**，不创建/解封考卷。

## 1. 目标与硬边界

- 唯一目标：**只属于该小账户的最终决策 Brain**——动态选择 `NO_TRADE / LONG / SHORT / HOLD / ADD / TRIM / CLOSE / REVERSE`，并决定持有时间与微仓数量。
- **永久冻结 G1–G6**；G6 结论是 256 EventCard、0 validation pass、`NO_GO`、`EXAM_UNAVAILABLE`，不得复活 EventCard。
- 允许开仓门为**个人保守净期望不低于 0**（break-even 门）；但 stress loss、偿付、最小下单、数据 OOD 必须通过。
- 目标微仓本金 **30/100/1000U**，并发 **1/2/3**，单事件 stress risk **0.5%**、账户合计 **2%**、有效杠杆硬上限 **5**。
- 残差地图 tier：**actual / 30 / 100 / 1000 / 10k / 100k**；专属残差=用户档 conservative net ≥0 而大资金档失效。
- Episodic memory 检索 **K=8**；可选残差模型最多 **32** 参数，默认 `RESIDUAL_DISABLED`。
- 正式 validation 必须覆盖**完整历史 catalog**；禁止用 3 标的 smoke 代替正式结论。
- 无合法预留隐藏卷时 **`EXAM_UNAVAILABLE/NO_GO`**，不等待未来数据。
- 默认 **Composer 2.5** 承担实现；所有真实扫描默认低 CPU：`GOMAXPROCS=1`、低优先级、分片 checkpoint。

## 2. 关闭并引用 g1–g6（只引用不可修改）

| 引用项 | g1 | g2 | g3 | g4 | g5 | g6 |
| --- | --- | --- | --- | --- | --- | --- |
| generation | `firstbattle-router-g1` | `firstbattle-router-g2` | `firstbattle-router-g3` | `firstbattle-router-g4` | `firstbattle-router-g5` | `firstbattle-router-g6` |
| router protocol hash | `0eab9bda…7d31` | `395d15ec…118ec` | `1628e1a5…7fb` | `55d5da85…6425` | `bfe9f784…3a7d` | `d34b2798…b759` |
| 搜索/规则/候选/策略 | — | 162 组合 | 192 规则 | 1024 候选 | 0 policy | 256 EventCard |
| validation pass | — | — | — | **0** | SKIPPED | **0** |
| system_status | `NO_GO` | `NO_GO` | `NO_GO` | `NO_GO` | `NO_GO` | `NO_GO` |

## 3. Brain 动作与开仓门

| 组件 | 冻结值 |
| --- | --- |
| Brain 动作（8 类） | `NO_TRADE` / `LONG` / `SHORT` / `HOLD` / `ADD` / `TRIM` / `CLOSE` / `REVERSE` |
| 开仓动作 | `LONG` / `SHORT` |
| 无匹配事件允许 | `HOLD` / `TRIM` / `CLOSE` |
| 个人 break-even 门 | `PersonalConservativeExpectedNet >= 0` |
| 必须同时通过 | stress loss / 偿付 / OOD / min lot / 执行指纹可用 |
| 接近 0 edge | 只允许更小微仓 |
| 反事实范围 | 当前事件 only，禁止全市场 Top-K |

## 4. Episodic Memory 与残差模型

| 组件 | 冻结值 |
| --- | --- |
| 最大检索 K | **8** |
| 相似度特征 | 连续市场状态 / 事件阶段 / 流动性 / 个人执行条件 |
| 禁止查询特征 | 未来 outcome / realized pnl / 事后标签 |
| 负样本多数 | 否决开仓 |
| 样本不足 | 缩仓或 `NO_TRADE` |
| 残差模型参数上限 | **32** |
| 默认状态 | `RESIDUAL_DISABLED` |
| 启用条件 | 独立 WF 有增益 |

## 5. 私人执行指纹与安全

| 组件 | 冻结值 |
| --- | --- |
| schema | `lingxi.routerg10.personal-execution-fingerprint.v1` |
| 输出 | 聚合分位数 + hash；**禁止**原始私人行入库 |
| 维度 | venue / symbol / session / direction / order_type / notional_bucket |
| 聚合项 | fee / slip / maker fill / latency / funding / tick / lot / min-notional |
| UNKNOWN | 对应路径不得假设低成本；**阻塞开仓** |
| 禁止入库字段 | api_key / secret / token / order_id / account_id / raw logs |

## 6. 小容量残差地图

| tier | 用途 |
| --- | --- |
| actual | 账户实际权益 |
| 30 / 100 / 1000 U | 微仓研究档（与 event_account 一致） |
| 10000 / 100000 U | 大资金容量悬崖对照 |
| 专属残差定义 | 用户档 conservative net ≥0 且大档因容量/冲击/保证金/执行失效 |
| 候选状态 | `PERSONAL_WILD_CANDIDATE`（非认证赚钱叶） |

## 7. 小账户微仓与事件队列

| 组件 | 冻结值 |
| --- | --- |
| 30U 最大并发 | **1** |
| 100U 最大并发 | **2** |
| 1000U 最大并发 | **3** |
| 单事件 stress loss | ≤ **0.5%** equity |
| 总 stress loss | ≤ **2%** equity |
| 有效杠杆 | ≤ **5×** |
| 队列排序 | `personal_conservative_edge / stress_loss / margin_time` 降序 |
| 最小下单 | 不足最小名义/张数直接跳过，**禁止向上凑单** |
| 账户会计 | 继承 G5/G6 fixed notional / funding / pending delay / MTM leverage / EOF |

## 8. StopError、Solvency 与 BrainTrace

- 固定 horizon 只作 **deadline fallback**；主要退出：**事件完成 / payer 消失 / invalidator / OOD / deadline**。
- **独立 Solvency 机器**可随时 trim/close；任何 Playbook 都不能关闭它。
- 每个决策输出 append-only **BrainTrace**：StateHash、事件族、指纹版本、记忆命中、反事实表、拒绝原因、最终动作、仓位、deadline、StopError、Solvency、TraceHash。
- 路径矩阵至少覆盖：数据失真→NO_TRADE；延续→LONG；失败突破→SHORT；负记忆→NO_TRADE；事件完成→CLOSE；偿付→TRIM；并发满→拒绝；低于最小名义→跳过。
- **Brain 退化**：单一动作占比 >85%、从不退出、永远 directional → `BRAIN_DEGENERATE`。

## 9. 正式历史 validation / WF

| 门控 | 冻结值 |
| --- | --- |
| 历史范围 | **完整 catalog**；禁止 smoke 代替 |
| 适用分区 | validation only |
| 微仓本金 | 30 / 100 / 1000 USDT |
| 三档必须全过 | **否**；每 Playbook 输出 `eligible_capital_tiers` |
| 净收益门 | base / 全部 delay / cost stress **≥ 0**（break-even，非强制高利润） |
| 最小样本 | ≥ 50 事件 / ≥ 20 独立日 / ≥ 10 shock blocks |
| WF 稳健 | day / shock / symbol LOO + 成本压力 / delay / 容量悬崖 / 账户净值 |
| 机制 | 无 forced-flow evidence → 最多 `UNEXPLAINED_PERSONAL_EDGE`；trade/exam/certified=false |

## 10. 考试与真钱边界

- **4 standard + 1 golden**，golden 必须最后
- 公开字段仅：`id`, `queue`, `kind`, `family`, `alpha_spend`, `data_hash`, `challenge_hash`, `protocol_hash`, `status`
- 当前无合法公开预留卷 → **`EXAM_UNAVAILABLE`**
- `exam_actions = 0`；默认 `trade_allowed=false`

## 11. 研究阶段状态

- `exam_actions = 0`
- 无合法预留卷 → `EXAM_UNAVAILABLE`
- protocol hash：`9f86ce54c24b00de41d1a6735a4f8190b00d49dce2a66371242646ffecd10750`

## 12. 低 CPU 与 checkpoint

| 组件 | 冻结值 |
| --- | --- |
| GOMAXPROCS | 1 |
| nice priority | 19 |
| checkpoint | 必须；按 symbol 或 shock block 分片恢复 |
| 禁止 | 长链一次跑满服务器 |
