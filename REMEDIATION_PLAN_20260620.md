# Lingxi 五策略统一轮动整改方案（2026-06-20）

> **定位**：本文是一份可执行的整改路线图（P0–P6），按"先止盲、再去污、再修主战场、再收口恢复"的顺序推进。
> 结论以当前代码与运行日志为准，文档作为任务拆分与验收依据。
> **配套**：与 `AUDIT_FINDINGS_20260620.md` 的全链路审计互为正反面——审计讲"哪里坏了"，本文讲"按什么顺序修"。

---

## 0. 背景与核心矛盾

当前统一轮动每轮日志只打印 `candidates=574~628`、`actions=0`，但这个数字**无法自解释**：

- 它是"574 个真实可开机会"，还是"574 个扫描对象（含大量早拒垃圾）"？目前看不清。
- 已验证事实（cvsnap-20260620T063014 一轮）：

  | 策略 | 候选数 | 正 EV | 主要死因 |
  |---|---|---|---|
  | SVSP（单所 spot-perp） | 297 | 0 | `negative_funding_v1_not_supported`(140) / `no_spot_market`(188) |
  | SVPS（单所 perp-spot） | 247 | 0 | `positive_funding_v1_not_supported`(167) / `not_borrowable`(49) |
  | CVSP（跨所 spot-perp） | **0** | 0 | `build_funding_window` / `build_notional` |
  | CVPS（跨所 perp-spot） | **0** | 0 | `not_borrowable`（占绝大多数 pair） |
  | Perp-Perp（跨所 perp-perp） | 30 | 0 | `net_ev<=0` + **Top30 截断** |

- **全系统 `positive_ev=0`、`actions=0` 是常态，不是某一个闸的问题。**

**核心矛盾**：决策器不是太笨，而是**输入池被污染** + **perp-perp 被提前截断** + **扫描/执行 EV 口径不一致** + **恢复闭环未收口**。

**一句话方案**：先别急着"找更多机会"，先把系统改成——只让真实可开的候选进入统一轮动，让 perp-perp 的扫描经济口径接近真实执行，再把恢复闭环收口。

---

## P0：把"628 个候选"变成真实可解释的数字（止盲，最先做）

### 目标
每一轮都能直接读出：`raw_candidates`、`would_execute_candidates`、`blocked_by_net_ev`、`blocked_by_hard_reject`、`blocked_by_balance` 各有多少。

### 改动位置
- `internal/app/unified_rotation_runner.go`
- `internal/app/unified_rotation.go`
- `internal/app/unified_candidates_log.go`

### 具体做法
1. `scanRotationCandidates` 之后，不再只打印 `candidates=628`，改为打印结构化指标：
   - `raw_total`、`raw_by_strategy`
   - `would_execute_total`、`would_execute_by_strategy`
   - `soft_reject_count`、`hard_reject_count`
2. `DecideRotation` 前，对每个候选跑一遍**只诊断不执行**的 `rotationCanOpenOpportunity`，累计拒绝原因分布。
3. 每轮 top reject reasons 落到 `cycle_explains.jsonl` 或单独的 `rotation_decision_diag.jsonl`。

### 关键修正（务必区分两层 would_execute）
当前 `unified_candidates.jsonl` 的 `would_execute` 定义是 `len(RejectReasons)==0`（**扫描器口径**），≠ 轮动层 `rotationCanOpenOpportunity` 口径。P0 必须分开记录：

| 字段 | 含义 |
|---|---|
| `scanner_would_execute` | 扫描器无 hard reject |
| `rotation_would_execute` | 过 `rotationCanOpenOpportunity` 诊断 |

否则止盲做完仍会混淆。

### 为什么先做
不先修可观测性，后面所有参数优化都会变成盲调。**这是止盲，不是优化。**

---

## P1：把"拒绝对象"和"可交易候选"从同一个池子里分开（去污）

### 目标
统一轮动只接收"理论上允许开仓"的候选；被拒绝对象只用于诊断和面板，不参与组合决策。

### 改动位置
- `internal/app/single_venue_spot_perp_scanner.go`
- `internal/app/cross_venue_spot_perp_scanner.go`
- `internal/app/single_venue_perp_spot_scanner.go`
- `internal/app/cross_venue_perp_spot_scanner.go`
- `internal/app/unified_rotation_runner.go`

### 具体做法
- **首选（最小改动）**：在 runner 汇总时做一层明确过滤，不改扫描器签名：
  ```
  rotationCandidates  = filter(无 hard reject 且 netEV>min 且 capital>0)
  diagnosticCandidates = all
  ```
- `unified_candidates.jsonl` 继续记录全部对象；`DecideRotation` 只吃 `rotationCandidates`。
- **可选（彻底版）**：扫描器返回两路结果 `tradable []UnifiedOpportunity` / `diagnostic []UnifiedOpportunity`。

### 注意
仅有 soft reject（如 `net_ev<=0`）的候选**也不该**进 `rotationCandidates`，否则 P1 等于没做。

### 为什么
问题不是决策器太笨，而是输入池被污染。决策器应优化组合，不应承担"替扫描器清垃圾"的主职责。**当前最优解：不必一开始就大重构，但必须把"诊断对象"和"可交易对象"分层。**

---

## P2：修 cvsp/cvps 的真实死亡点（基础设施，不要看总数自我安慰）

### 目标
让 cvsp/cvps 至少能稳定产出一部分 `emitted_tradable>0` 的候选。

### 改动位置
- `internal/app/cross_venue_spot_perp_scanner.go`
- `internal/app/cross_venue_perp_spot_scanner.go`
- `internal/app/carry_open_protocol.go`
- `internal/app/unified_rotation_runner.go`

### 具体做法
1. 给 `funding_window` 拒绝单独打**穿透日志**：`symbol` / `next_funding_time` / `hold_hours` / `expected_funding` / `entry_cost` / `gate threshold`。
2. 给 borrow probe 做**可用率统计**，而不是只写失败日志：`borrow_probe_coverage_by_venue` / `borrow_known_true_rate` / `borrow_unknown_rate`。
3. 粗筛后的 symbol 集，先做一层 **venue-specific instrument 有效性过滤**，别把大量 `tick/step=0` 的 symbol 继续扔给 cvps。

### 补充（方案外但必须算进去）
- **共用 Top150 粗筛**：cvsp/cvps 与 svsp/svps 吃同一 symbol 宇宙（`rotationScanFilterSymbols` 取 Top150），极端币可能根本进不了 150。
- instrument 无效应**前置**过滤，否则 cvps funnel 里白做 `pair_attempts`。

### 不该怎么改
不要先把门一股脑放松。先把死亡分布量化出来，再决定是调 `funding_window`、调 `notional`，还是直接剔除坏 symbol。

---

## P3：perp-perp 三连改（最直接影响开仓的主战场）

### 目标
让 perp-perp 从"看起来机会很多但 EV 常被算死"变成"能把真实可开机会送进统一轮动"。

### 改动位置
- `internal/app/cross_venue_scanner.go`
- `internal/app/cross_venue_rotation.go`
- `internal/app/liquidity_sizing.go`
- `internal/app/unified_rotation_runner.go`

### 做法 1：取消 perp-perp 对统一轮动的 Top30 截断（最高优先）
- scanner 保留全量可比较 opps。
- 面板/leaderboard 再单独取 TopN 展示。
- 统一 runner 读取全量 `rotation_opps_full`，不吃子策略提前截断后的世界。
- **证据**：H-USDT `entered=true` 但 `unified_candidates.jsonl` 中 perp_perp+H = **0 条**；runner 只读 `snap.Opportunities[:30]`。

### 做法 2：notional 搜索而不是固定/上限 150（最高优先）
- 对每个 perp-perp 候选，至少在离散量上搜：`25 / 50 / 75 / 100 / 150 / tradable_cap`。
- 选 netEV 最大且满足风险/深度约束的名义。
- **证据**：H 在 $150 上 `netEV=-0.73`（spreadEV≈$0.96 被固定 $1.20 滑点+费用算死），小名义可能转正。
- **风险**：必须尊重各所 `minNotional`/步进，不能只扫五个固定点。

### 做法 3：扫描 EV 接入深度滑点（紧随其后）
- 扫描期 `ExpectedSlippageUSDT` 从常数 bps 近似，升级为与执行闸同口径的深度估算。
- 不要求 100% 复用执行函数，但必须保证不出现"扫描排名很好，执行一算全翻负"。
- **证据**：扫描用 `crossVenueExpectedSlippageUSDT`（4 腿 × 20bps 固定）；执行闸用 `crossVenuePerpPerpEVSlippageLegs`（深度模拟）——**同策略两套口径**。

### 优先级
`Top30 取消` 和 `notional 搜索` 最高；`深度滑点对齐` 紧随。**这三条是整份方案里最值钱的一组。**

### 时间修正
Top30 取消**不应等到第 2 周**——H 是"必须吃到"的目标币，它现在死在 Top30。P0+P1 完成后**立刻**接 Top30 旁路。

---

## P4：统一目标函数说清楚（不要再自相矛盾）

### 目标
让系统明确自己到底优化什么。

### 改动位置
- `internal/app/unified_rotation.go`
- `internal/app/unified_opportunity.go`

### 现状（已验证的矛盾）
- 统一轮动主排序：`unifiedWindowScore = ExpectedNetEVUSDT / totalCapital`（**资本效率**）。
- perp-perp 扫描排序：`RankScore = NetEV`（**绝对 EV**）。
- **两套目标混用。**

### 做法（二选一，不要暧昧）
- 若优化"绝对净收益"：全链主排序用 `ExpectedNetEVUSDT`。
- 若优化"有限资本下单位窗口收益"：全链主排序用 window score / capital efficiency。

### 建议
账户只有几百 U，现实上应优化"有限资本下的窗口收益"。但需与"极端币必须吃到（H 类高 EV/低资本效率）"的目标取舍**先拍板**：
- 若坚持极端币必吃 → 需纯 EV 排序，或给 watchlist 保底 slot。
- 无论选哪个，日志/面板都要明确写：`primary_score` / `net_ev` / `capital_efficiency` / `selected_reason`。

> **本项需用户先决策再动手**，否则改完仍会觉得系统"不听话"。

---

## P5：修 MNT-USDT 重复冷启动 flatten（生存性问题）

### 目标
让未登记腿只被识别一次、归属一次、处理一次，不要两套策略反复 flatten 同一残腿。

### 改动位置
- `internal/app/single_venue_spot_perp_executor.go`
- `internal/app/cross_venue_spot_perp_executor.go`
- 各自的 `ReconcileOnStart` / orphan scan 逻辑
- 可能的共用 reconcile helper

### 具体做法
1. 把"未登记现货腿扫描"从各策略各扫一次，改成**统一 spot-orphan reconciler 扫一次**，再按归属规则分配给某策略处理。
2. 增加 `recent_flatten_registry`：记录 `(venue, symbol, side, qty_band)` 在最近 N 分钟是否刚处理过，防止重启/冷启动重复 flatten。
3. 增加 **dust band**：小于真实成交阈值的残腿不再反复当事故处理。
4. flatten 后**强制二次核验**：若交易所仍显示残腿，标记 `unresolved_orphan`，不要下次又当全新事件。

### 证据
日志实证 `[同所spot-perp] reconcile: venue=bybit 未登记 spot MNT-USDT≈18.43`；各策略各自 reconcile，无统一归属与去重。

### 时间修正
**若仍在 live，本项应与 P0 并行**，不必等第 3 周——重复 flatten 会真下单、真花钱、真扰动账户，比"多扫几个 symbol"严重得多。

---

## P6：watchlist 与 reverse funding（最后做，扩大机会空间）

### 目标
在基础链路正常后再扩大机会空间。

### 改动位置
- `internal/app/unified_rotation_runner.go`
- 单所扫描器配置

### 具体做法
1. `rotationScanFilterSymbols()` 之后增加 **watchlist union**，让 H/BTT/PEPE 这类 symbol 即使不进 150 也能进入二次扫描。
2. `SingleVenueReverseFundingEnabled` 改成按 **venue / symbol / regime 可控**，而不是简单全局开。

### 为什么放最后
现在不是"机会太少"，而是"输入不真实、恢复不稳定"。先扩 universe 只会放大噪音。

---

## 附：方案没写但必须盯的配置反噬

P0 做完会直接撞上——很多候选不是算法挡的，是配置自己焊死的：

- `config.yaml: strategy_mode: profit_seeking` → 强制 `min_net≥0.002` + impact reserve，系统性压低 `would_execute`。
- `max_cycle_open_notional_usdt: 60` vs `cross_venue_executor.max_position_usdt: 150` → 即使候选转正，单周期也可能开不出去。
- perp-perp 与 carry 四策略**数据路径不对称**：150 粗筛只卡 carry，不卡 perp-perp；"五策略公平比较"在输入层就不成立。

---

## 实施顺序

| 阶段 | 内容 | 目标 |
|---|---|---|
| **第 1 周** | P0 + P1（+ P5 若 live 并行） | 把"628 候选"拆成真实结构；去污；收口冷启动 flatten |
| **第 2 周** | P3 Top30 + notional 搜索（紧接 P1）+ P2 | 让 `would_execute` 候选开始稳定出现 |
| **第 3 周** | P3 深度滑点对齐 + P4（需先拍板目标函数） | 消除扫描/执行口径不一致；统一排序口径 |
| **第 4 周** | P6 watchlist / reverse funding | 扩大机会空间 |

> 调整说明（相对朋友原顺序）：
> - **P3 Top30 提前**：H 等不起，P1 之后立刻做。
> - **P5 提前/并行**：live 残留腿是生存性风险。
> - **P4 前置拍板**：目标取向是用户决策，不是技术决策。

---

## 验收标准

不是看日志更热闹，而是看这 7 个指标：

1. `would_execute_total > 0`，且**连续多个周期**成立。
2. cvsp/cvps `emitted_tradable` 不再长期为 0。
3. perp-perp 不再出现大量"高 spread 但固定 150 算死且被 Top30 截断"的模式。
4. `actions=0` 从长期常态变成少数市场窗口现象。
5. MNT 这类未登记腿不再跨两套策略重复 flatten。
6. **（新增）** 每轮日志能直接读出"574 里 `rotation_would_execute=?`，死因 top3=?"——读不出 = P0 未达标，不得往下走。
7. **（新增）** `would_execute` 不再混用 scanner 层 / rotation 层口径。

---

## 起步建议（落地分级）

- **方案 A（最小改动，先做）**：P0 死因拆解 + `rotation_decision_diag.jsonl` + P1 runner 层过滤 + P3 Top30 旁路。
- **一周内并入 B 核心**：P3 notional 搜索 + 扫描 EV 深度滑点。
- **P5 并行**（若 live）。
- **方案 C（双出口扫描器 / 统一 orphan reconciler / 全链单一目标函数）**：等 A/B 跑出 `would_execute>0` 后再做。

理由：A 最快回答"574 到底是什么"并让 H 有机会进轮动；C 改动面太大，现阶段上等于盲改时加重构风险。

---

## 待用户决策项（动手前必须确认）

1. **P4 目标取向**：优化"绝对净收益" vs "有限资本下窗口收益"——决定全链主排序口径。
2. **极端币策略**：是否为 H/BTT/PEPE 等设保底 slot（与 P4、P6 耦合）。
3. **起步范围**：先做方案 A，还是直接 A+B 核心一起上。
4. **P5 时机**：当前是否 live、是否需要与 P0 并行。
