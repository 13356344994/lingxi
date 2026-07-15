# LINGXI 机制搜索 V2 — Grok 候选（交易所边角 / 运营约束 / 反常识）

> Artifact: `firstbattle.mechanism-search-v2.candidates-grok`
> Owner: `grok-4.5-high-fast`
> Generation: `mechanism-search-v2-grok-v1`
> Status: `OUTCOME_BLIND_CANDIDATES_ONLY`
> JSON: `research/firstbattle/mechanism_search_v2/candidates_grok.json`
> JSON hash (self-excluded): `615c1e5aa5267e30698ee8839b41d88b09e54bc5b70ebc7a5c5e0efb0a5dcddc`
> Document hash: `c3e5f75ab9ce193d5c308c0adf8ea3c58ef0a6d38c8547bdf141f52c1cc7a37e`
> labels_unblinded: `false`
> routes_started: `0`
> trade_allowed: `false`
> Created: `2026-07-14T03:15:00Z`

## 0. 边界

- 只写本 owner 文件；不提交；不购买；不 bulk 下载；不联网交易；不读收益/考卷。
- 不修改 G1–G10、records、冻结合同、registry、其他 owner 候选。
- **无法满足现有历史完整分母的候选标 REJECTED / DATA_BLIND，不造可测性。**

## 1. H0 与竞争解释（先于候选）

| ID | 陈述 |
|---|---|
| **H0** | 边角/运营 edge 主要来自不完整分母、PIT 幻觉、选择偏差或成本低估。 |
| H_CALENDAR_FORCED_FEE | 结算/借贷/交割日历迫使主体在固定时刻付款或调仓。 |
| H_DISCRETE_OPS_CONSTRAINT | min/档位/抵押/划转/维护窗等离散约束制造小账户可执行或应空仓的局部结构。 |
| H_MARK_INDEX_RULE_CLAMP | 标记/指数夹具触发强制流。 |
| H_PUBLIC_FACTOR_MISLABEL | 候选只是 funding 符号、波动或跨所 mid 差换皮——必须拒绝。 |

最强组合反例：机构可批量复制小单并聚合；无持续硬容量/权限分割则护城河消失。

## 2. 数据源调查结论（免费/官方 / 本地）

| 家族 | 官方免费 | 本地 | 完整分母裁决 |
|---|---|---|---|
| funding/settlement calendars | CDN funding 2022-03+ | `records/history/okx_funding` ≈401 符号 tape | tape≠schedule 版本链 → **不可测** |
| borrowing calendars | CDN borrowing 2021-12+ | 仅 snapshot；`borrow_probes` 空 | **DATA_BLIND** |
| listing/delisting/migration | 无完整档案 | listTime 快照 | **REJECTED/PIT_UNPROVEN** |
| rule/cap/floor | 无历史版本库 | 当前 instruments | **PIT_UNPROVEN** |
| index/mark dislocation | 未证明完整错位档案 | mids / 本地强平 PARTIAL | **DATA_BLIND**（官方强平 batch ABSENT） |
| collateral/borrow/transfer | ABSENT | authorization history ABSENT | **REJECTED** |
| 非速度跨所运营不对称 | L2 候选但非运营日历 | mid 弱代理 | **DATA_BLIND**；禁速度换皮 |

绑定（只引用不修改）：contracts `4a4c201d…`；registry `1c667256…`；reopen inventory `b9b62bc4…`；manifest `66b47378…`；P0 fitness `393a1295…`。

## 3. 候选一览（7）

可测完整分母：**0**。全部 `REJECTED_*` / `DATA_BLIND*`。`routes_started=0`。

### GROK-MSV2-01 — funding_interval_densification_calendar_tax
- **状态:** `DATA_BLIND`
- **付款者:** 穿过新增结算点的付款方向持仓者（结算时钟强制扣 funding）
- **分支:** 可证日历加密 → 付款方向减仓/flat；收款侧默认 NO_TRADE；无 KnownFrom → NO_TRADE
- **机构:** 全市场日历编排 + 调仓冲击常超过可省费用
- **护城河:** 硬结算时钟 + 小名义无冲击减仓；可自动化则失败
- **个人:** 小账户可在结算前低冲击 flat
- **分母/PIT:** 需官方 interval/schedule 版本链；本地仅有 fundingTime tape，空洞≠变更
- **matched loser:** 同 schedule 事件同方向桶未减仓者 + flat 对照
- **最强反例/替代:** 采集空洞误当变更；或退化为 G2 funding 符号因子
- **最短证伪:** 只查官方 schedule 版本库是否存在；无则保持 DATA_BLIND
- **预算:** 1 CPU / 4h / 1 attempt / 256MB；禁 CDN bulk 与收益解盲
- **≠G1–G10:** 动作是“避开新增 settlement”，不是 G2 payer-side 预测或 G10 残差

### GROK-MSV2-02 — borrow_rate_spike_or_limit_suspension_forced_repay
- **状态:** `REJECTED_DATA_BLIND`
- **付款者:** 借币负债账户（利息 / limit·暂停下被迫偿还）
- **分支:** 有负债且变更可见 → repay/降杠杆；禁止利率方向赌
- **分母:** 官方 CDN 未获取；本地无 timeseries / suspend 日历
- **最短证伪:** 确认本地无 borrowing 历史链 → 维持 REJECTED（不因“CDN 宣称存在”伪造成可测）
- **≠G1–G10:** 借币运营约束 ≠ 永续 funding 状态

### GROK-MSV2-03 — listing_delisting_migration_forced_inventory
- **状态:** `REJECTED_DATA_BLIND`
- **付款者:** 截止时钟前仍持有将被取消/强制结算库存者
- **分母:** 官方完整 list/delist/migrate 档案 ABSENT；公告碎片不构成 PIT
- **最短证伪:** archive ABSENT 已确认 → REJECTED stop
- **≠G1–G10:** 产品生命周期强制退出 ≠ G4 收益格子 / G10 规则断点全家桶

### GROK-MSV2-04 — mark_index_clamp_dislocation_forced_flow
- **状态:** `DATA_BLIND`
- **付款者:** 被 mark/维持保证金规则强制减仓或强平者
- **分母:** 官方强平 batch ABSENT；本地强平 JSONL ≠ 官方完整分母；无 mark↔index 长期错位档案
- **禁止:** 速度套利换皮；购买 Tardis 冒充官方
- **≠G1–G10:** 因果核是 mark/index 夹具，不是泛化 G10 sparse forced 残差（数据上同等阻断）

### GROK-MSV2-05 — collateral_haircut_transfer_permission_trap
- **状态:** `REJECTED_DATA_BLIND`
- **付款者:** 折扣下调或划转限制下需维持保证金的抵押持有者
- **分母:** `collateral_authorization_history=ABSENT`
- **最短证伪:** ABSENT → REJECTED
- **≠G1–G10:** 权限/折扣日历，不是 mid lead-lag 或 G5 动作学习

### GROK-MSV2-06 — discrete_min_notional_lot_roundup_refusal
- **状态:** `DATA_BLIND_PIT_UNPROVEN`
- **反常识机制:** 优势常是**拒绝凑单并 NO_TRADE**，不是成交
- **付款者:** 必须立即成交从而接受 round-up / 跨档名义者
- **分母:** 需 PIT 规则版本 + 完整 attempts（含未提交/reject）；当前 PIT_UNPROVEN 且 attempts 不完整
- **≠G1–G10 / micro cliff:** 成功定义是正确空仓避开凑单，不是 fill/冲击曲线参数变体

### GROK-MSV2-07 — cross_venue_ops_window_permission_asymmetry_nonspeed
- **状态:** `DATA_BLIND`
- **付款者:** 因维护/解锁/权限无法在最优场所对冲、必须在可达场所调整者
- **硬排除:** 任何纯速度 / mid lead-lag / xgap 包装
- **分母:** 无多所运营日历；仅有 mid 弱代理；L2 不得挤占 P0
- **≠G1–G10:** 触发器是 ops/permission 窗口，不是价格领先毫秒（对 G10 cross-venue 野路加硬日历门槛，现仍 blind）

## 4. Journal

1. 先冻结 H0/竞争解释与硬拒绝表。
2. 对照 registry + 本地存在性：funding tape 有、schedule/borrow/权限/上下架/mark 错位完整分母无。
3. 产出 7 候选，**0 条**宣称现有完整分母可测。
4. 自 hash；`labels_unblinded=false`；`routes_started=0`；不提交。

## 5. 给编排者的建议

短名单若需要 funding/borrowing 官方免费史，须另批**最小存在性 pilot**，先解决 schedule/PIT/强制主体分母，禁止“下载即开跑”。本 owner 不自批 shortlist。

## 6. 安全开关

`NO_GO` / `EXAM_UNAVAILABLE` / `exam_actions=0` / `trade_allowed=false` / `no_commit=true`
