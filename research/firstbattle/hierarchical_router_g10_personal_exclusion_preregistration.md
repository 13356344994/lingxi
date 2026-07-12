# 分层路由 g10 Personal-Exclusion 预注册协议（firstbattle.hierarchical_router.g10.personal_exclusion）

> 本文件是研究者可见的公开协议说明，与
> `research/firstbattle/hierarchical_router_g10_personal_exclusion_preregistration.json`（机器可读真相）配套。
> 冻结的数值/结构以 JSON 为准；本 md 只做解释，不引入新的可执行阈值。
>
> G10 Personal-Exclusion 是**新 generation、新 protocol hash**，不覆盖、不篡改 G10 v1（`9f86ce54…`）。
> 当前 G10 v1 的 `0 candidates` 记为**执行指纹技术阻断**，不解释为市场无机会。
> **研究阶段 exam_actions=0**，`trade_allowed=false`，不创建/解封考卷。

## 1. 目标与硬边界

- 唯一目标：**三重排除倒序个人 Brain**——先圈定机构、普通散户、通用机器人难以参与的合法区域，再只从具备历史可入场证明的盈利录像倒序生成个人剧本。
- **永久冻结 G1–G6 与 G10 v1**；不得复活 EventCard 或篡改 g10 v1 hash。
- 执行仅限 **OKX**；Binance、Bybit、Gate 等仅提供严格 PIT 的上下线及重大公告信息。
- 单事件终局损失硬上限 **40%**、账户同时终局风险硬上限 **70%**——是**绝对天花板而非建议仓位**。
- 允许大幅浮亏、隔夜持仓、跨 funding；多所资金能力可预置；无需人工确认；长期空仓能力。
- 独立 Solvency/强平防线；禁止机械补仓、向上凑单、毒性成交后补齐仓位。
- 无合法预留隐藏卷时 **`EXAM_UNAVAILABLE/NO_GO`**，不等待未来数据。

## 2. 关闭并引用 g1–g6 与 g10 v1（只引用不可修改）

| 引用项 | g1 | g2 | g3 | g4 | g5 | g6 | g10 v1 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| generation | `firstbattle-router-g1` | `firstbattle-router-g2` | `firstbattle-router-g3` | `firstbattle-router-g4` | `firstbattle-router-g5` | `firstbattle-router-g6` | `firstbattle-router-g10` |
| router protocol hash | `0eab9bda…7d31` | `395d15ec…118ec` | `1628e1a5…7fb` | `55d5da85…6425` | `bfe9f784…3a7d` | `d34b2798…b759` | `9f86ce54…0750` |
| 结论 | NO_GO | 162 组合 0 提名 NO_GO | 192 规则 0 提名 NO_GO | 1024 候选 0 pass NO_GO | 0 policy 退化 NO_GO | 256 EventCard 0 pass NO_GO | 0 candidates（指纹阻断） |

## 3. 个人宪法（constitution）

| 组件 | 冻结值 |
| --- | --- |
| 本金档 | **30 / 100 / 1000 U** |
| 并发 | **1 / 2 / 3** |
| 单事件终局损失硬上限 | **40%** equity（非目标） |
| 账户同时终局风险硬上限 | **70%** equity（非目标） |
| sizing 方向 | 按机会证据和强平距离**向下**裁剪 |
| 执行场所 | **OKX only** |
| 跨所信息 | Binance/Bybit/Gate **PIT 公告 only** |
| 浮亏/隔夜/funding | **允许** |
| 补亏/向上凑单 | **禁止** |
| Solvency | **独立**，Playbook 不可关闭 |

旧 G10 v1 的 0.5%/2% stress 常量仅保留在 `closes_g10` 记录，**不进入** g10p 个人宪法核心。

## 4. 三重排除

| 轴 | 排除证据类型 |
| --- | --- |
| 机构 | 容量悬崖、绝对利润不足、资金长期闲置、授权/托管限制、大订单冲击 |
| 散户 | 长期空仓、无法追价、大浮亏+thesis exit、最低张数非线性、复杂操作纪律 |
| 机器人 | 事件稀疏、固定阈值失效、条件组合变化、排队拥挤、公开后衰减、1/3/5 竞争 crowding |

禁止 `SMALL_BUT_COMPETITIVE`、`PUBLIC_AND_CROWDED`、仅靠币小或公共信号成立的机会。

## 5. Entry / Execution / Exit Proofs

| Proof | 要点 |
| --- | --- |
| EntryProof | 动态 minSz/ctVal/价格；证据 A/B 可认证，C/D 仅发现（`ENTRY_UNPROVEN`）；maker 触价≠成交 |
| ExecutionPolicy | 同点冻结比较 `BOUNDED_MAKER / CONDITIONAL_MARKETABLE_LIMIT / SKIP`；`NoChaseAfterMiss`；Maker 四象限选择偏差门 |
| ExitProof | 平仓深度、下线前可退、极端滑点、部分平仓；能开不能退不得认证 |

## 6. 考试与真钱边界

- `trade_allowed = false`
- `exam_actions = 0`
- 无合法预留卷 → **`EXAM_UNAVAILABLE`**
- protocol hash：`74d043b13fe46468baaa1f670112aeef3b446d0538956936fefc831a0dc39dc8`

## 7. 研究阶段状态

- `exam_actions = 0`
- 无合法预留卷 → `EXAM_UNAVAILABLE`
- 红队 16 项覆盖 constitution/manifest/公告污染/执行选择偏差/风险上限误用
