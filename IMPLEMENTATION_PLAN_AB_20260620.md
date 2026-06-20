# Lingxi 整改方案 A+B 核心 — 函数级落地清单（2026-06-20）

> **范围**：P0 止盲 + P1 去污 + P3 perp-perp 三连（Top30 取消 / notional 搜索 / 深度滑点对齐）+ P4 绝对净收益 + P5 MNT 收口（与 P0 并行）。
> **依据**：`FULL_PIPELINE_ANALYSIS_20260620.md`（正向分析 + 日志证据）。本文是"改哪个函数、加哪个字段、日志长什么样"的实现规格。
> **用户已锁定决策**：P4=绝对净收益；极端币不设保底；A+B 一起上；P5 并行。

---

## 决策落地总览

| 模块 | 改动文件 | 核心动作 |
|---|---|---|
| P4 绝对净收益 | `unified_rotation.go`、`unified_opportunity.go` | 主排序改 `ExpectedNetEVUSDT`，资本效率降 tiebreaker |
| P3-1 取消 Top30 | `cross_venue_scanner.go`、`unified_rotation_runner.go` | snapshot 保留全量，面板单独取 TopN |
| P3-2 notional 搜索 | `cross_venue_scanner.go`、`cross_venue_rotation.go` | 离散名义搜 argmax(netEV) |
| P3-3 深度滑点对齐 | `cross_venue_scanner.go`、`cross_venue_rotation.go`、`cross_venue_slippage.go` | 扫描期接 `crossVenuePerpPerpEVSlippageLegs` |
| P0 止盲 | `unified_rotation_runner.go`、`unified_rotation.go`、`unified_candidates_log.go` | 结构化计数 + `rotation_decision_diag.jsonl` |
| P1 去污 | `unified_rotation_runner.go`、`unified_rotation.go` | rotationCandidates / diagnosticCandidates 分层 |
| P5 MNT 收口 | `position_dust.go`、两 spot-perp executor | 统一 orphan 扫描 + 去重注册表 + 二次核验 |

---

## P4：绝对净收益作为全链主排序

### 现状
- `unifiedWindowScore(opp) = ExpectedNetEVUSDT / totalCapital`（资本效率），用于 `rotationSortedPositiveCandidates` 排序、`planUnifiedRotationOpensFrom` 打分、`decideRotationAt` 的 replace 比较。
  - `unified_rotation.go:178-184, 519-525, 538-554`
- perp-perp 扫描排序用 `RankScore = NetEV`（`cross_venue_rotation.go:408-411`）——已是绝对 EV，但与轮动层口径不一致。

### 改动

**1. 新增主排序函数（`unified_rotation.go`）**
```go
// unifiedPrimaryScore 全链主排序口径：绝对净收益（USDT）。
func unifiedPrimaryScore(opp UnifiedOpportunity) float64 {
    return opp.ExpectedNetEVUSDT
}
// unifiedWindowScore 保留为资本效率，仅作 tiebreaker + 日志展示。
```

**2. 改 `rotationSortedPositiveCandidates`（`unified_rotation.go:511-527`）**
- 主排序键：`unifiedPrimaryScore`（绝对 EV）降序
- tiebreaker：`unifiedWindowScore`（资本效率）降序 → 再 `Symbol` 升序
```go
sort.SliceStable(positive, func(i, j int) bool {
    pi, pj := unifiedPrimaryScore(positive[i]), unifiedPrimaryScore(positive[j])
    if pi != pj { return pi > pj }
    ci, cj := unifiedWindowScore(positive[i]), unifiedWindowScore(positive[j])
    if ci != cj { return ci > cj }
    return positive[i].Symbol < positive[j].Symbol
})
```

**3. 改 `planUnifiedRotationOpensFrom`（`unified_rotation.go:530-556`）**
- `score := unifiedPrimaryScore(opp)`（写入 RotationAction.Score / Reason）
- Reason 同时带两值：`open_net_ev=%.4f cap_eff=%.6f`

**4. ⚠️ 关键一致性：replace/SWITCH 比较（`decideRotationAt:730-822`）**
- `continuationScore` 与 `openScore` 必须**同量纲**才能比。当前都是 per-capital。
- 改 `openScore` 为绝对 EV 后，`continuationScore`（`continuationScore(pos, inputs)`）也要改成**剩余绝对 EV**，且 `switchCostPerUnit` 不再除 `heldCapital`，直接用 `switchCost`（USDT）：
  ```go
  adjustedOpenScore := openScore - switchCost   // 都是 USDT
  ```
- `rotationBestReplaceCandidate` 内部打分同步改绝对 EV。
- **注**：当前 `held=0`（116 轮），replace 路径未被触发，但必须改对以防未来持仓后口径错乱。**这是本项最易埋 bug 处，需配单测。**

**5. 日志/面板字段（`unified_opportunity.go` + 候选日志）**
- 决策日志每条 OPEN 输出：`primary_score(=net_ev)` / `capital_efficiency` / `selected_reason`
- 面板候选结构补 `PrimaryScore` 字段（= ExpectedNetEVUSDT），与 `CapitalEfficiencyScore` 并列

**6. perp-perp 侧**
- `crossVenueFinalizeOpportunityRanking` 的 `RankScore = NetEV` 保持不变（已是绝对 EV，现在与轮动层统一）。

### 单测
- `rotationSortedPositiveCandidates`：A(EV=2,cap=150) 应排在 B(EV=0.5,cap=30) 前。
- SWITCH 一致性：构造 held + 高 EV 候选，验证 adjustedOpenScore 与 contScore 同量纲比较。

---

## P3-1：取消 perp-perp 对统一轮动的 Top30 截断

### 现状
`cross_venue_scanner.go:1147-1150` 对全部 opps 排序后 `opps = opps[:topN]`（topN=30），`snap.Opportunities` 因此只剩 30 条；runner 在 `unified_rotation_runner.go:1025-1038` 只能遍历这 30 条。

### 改动

**1. scanner 保留全量（`computeCrossVenueOpportunities:1086-1153`）**
- 排序后**不截断**返回全量 `opps`（仍按 RankScore 降序）。
- 截断只用于诊断日志 `finalizePerpPerpDiagRound` 的 inTopN 判定（保留 topN 参数仅做标注，不删数据）。
- `CrossVenueScanSnapshot.Opportunities` 改为承载全量。

**2. 面板/leaderboard 单独取 TopN**
- 面板读 `snap.Opportunities` 处（dashboard API）改为 `opportunitiesTopN(snap.Opportunities, displayTopN)`。
- leaderboard 走 `unified_candidates.jsonl`，本就独立 rank，无需改。
- 新增配置 `CrossVenueScannerConfig.PanelDisplayTopN`（默认 30），仅控展示。

**3. 体量控制**
- 全量 perp-perp 候选写入 `unified_candidates.jsonl` 会放大文件（当前 perp_perp 30/轮 → 可能数百/轮）。
  - 候选日志只写 `would_execute || netEV>阈值 || watchlist` 的对象，避免每轮数百条全落盘；或加采样。

### 验收
日志 `perp_perp=N` 不再恒为 30；H-USDT 能出现在 rotationCandidates（netEV>0 时）。

---

## P3-2：notional 搜索（不再固定 150）

### 现状
`cross_venue_scanner.go:1129` 定死 `opp.TradableNotional = crossVenuePerpPerpScanTradableNotional(...)`（上限 MaxPositionUSDT=150），随后 `:1135` 用该名义算一次 EV。

### 改动

**1. 新增搜索函数（`cross_venue_rotation.go`）**
```go
// crossVenuePerpPerpSearchBestNotional 在离散名义上搜 netEV 最大且满足约束的名义。
func crossVenuePerpPerpSearchBestNotional(
    cfg CrossVenueExecutorConfig, opp CrossVenueOpportunity, now time.Time,
    evHigh, evLow float64, tradableCap float64,
    slipLegs []crossVenueEVSlippageLeg, // P3-3 深度滑点
) (bestOpp CrossVenueOpportunity, bestNotional float64) {
    grid := []float64{25, 50, 75, 100, 150}
    if tradableCap > 0 { grid = append(grid, tradableCap) }
    best := math.Inf(-1)
    for _, n := range grid {
        n = clampToVenueStep(n, opp, tradableCap)   // 尊重 minNotional/步进
        if n <= 0 { continue }
        cand := opp; cand.TradableNotional = n
        cand = crossVenueEstimateOpportunityMetricsEVRatesWithLegs(cfg, cand, now, evHigh, evLow, slipLegs)
        if cand.NetEV > best {
            best = cand.NetEV; bestOpp = cand; bestNotional = n
        }
    }
    return bestOpp, bestNotional
}
```

**2. 接入点（`computeCrossVenueOpportunities:1129-1135`）**
- 用 `tradableCap = crossVenuePerpPerpScanTradableNotional(...)` 作为搜索上界
- `opp = crossVenuePerpPerpSearchBestNotional(...)` 取最优名义的 opp
- 诊断日志补 `chosen_notional` 字段，便于看 H 选了哪个名义

**3. 约束**
- `clampToVenueStep`：名义需满足两腿各自 `minNotional` 与步进；不足则跳过该 grid 点。
- 上界不超过 `tradableCap`（深度允许）。

### 验收
H-USDT 在小名义（如 50/75）上可能 `netEV>0`；诊断日志显示 `chosen_notional<150`。

---

## P3-3：扫描 EV 接入深度滑点（与执行闸同口径）

### 现状
- 扫描 EV：`crossVenueEstimateOpportunityMetricsEVRates` 内 `crossVenueExpectedSlippageUSDT(cfg, notional, 4)` = 4腿 × 20bps 固定（`cross_venue_rotation.go:355, 184-190`）。
- 执行闸：`crossVenuePerpPerpEVSlippageLegs` 深度模拟（`cross_venue_slippage.go:152-177`，`cross_venue_gates.go:229`）。
- 证据：H 的 `slip=1.20` 恒定，与 spreadAPR 无关。

### 改动

**1. 新增带 legs 的 EV 估算（`cross_venue_rotation.go`）**
```go
// crossVenueEstimateOpportunityMetricsEVRatesWithLegs 用深度腿估算滑点（缺深度回退固定 bps）。
func crossVenueEstimateOpportunityMetricsEVRatesWithLegs(
    cfg CrossVenueExecutorConfig, opp CrossVenueOpportunity, now time.Time,
    evHigh, evLow float64, slipLegs []crossVenueEVSlippageLeg,
) CrossVenueOpportunity {
    // 与现有 EV 函数一致，唯一区别：
    // expectedSlippage := crossVenueExpectedSlippageUSDTWithLegs(cfg, notional, slipLegs)
    // 若 slipLegs 为空 → 回退 crossVenueExpectedSlippageUSDT(cfg, notional, 4)
}
```
- 复用已存在的 `crossVenueExpectedSlippageUSDTWithLegs`（`cross_venue_slippage.go:141-150`）。

**2. 扫描期取深度腿（`computeCrossVenueOpportunities`）**
- scanner 已持有 `s.venues` 和 ctx；在配对后取四腿深度：
  ```go
  highMid, lowMid := crossVenuePerpPerpMids(...) // 或复用 tradableNotional 内部已拉的深度
  slipLegs := crossVenuePerpPerpEVSlippageLegs(fetchDepth, ctx, highV, lowV, sym, highMid, lowMid)
  ```
- 复用度优先：`crossVenuePerpPerpScanTradableNotional` 内部已 fetchDepth，可重构为同时返回深度腿，避免重复拉盘口。

**3. 口径统一校验**
- 加单测：同一 opp + 同一深度，`扫描期 netEV` 与 `evaluateCrossVenueOpenEV` 的成本项滑点一致（容差内）。

### 验收
H 的 `slip` 不再恒定 1.20，随名义/深度变化；不再"扫描排名好、执行翻负"。

---

## P0：把候选数变成可解释结构（止盲，与 P5 并行最先做）

### 改动

**1. 决策诊断结构（`unified_rotation.go`）**
```go
type RotationDecisionDiag struct {
    CycleID            string         `json:"cycle_id"`
    RawTotal           int            `json:"raw_total"`
    RawByStrategy      map[string]int `json:"raw_by_strategy"`
    ScannerWouldExec   int            `json:"scanner_would_execute_total"`   // len(RejectReasons)==0
    RotationWouldExec  int            `json:"rotation_would_execute_total"`  // 过 rotationCanOpenOpportunity
    WouldExecByStrategy map[string]int `json:"rotation_would_execute_by_strategy"`
    SoftRejectCount    int            `json:"soft_reject_count"`
    HardRejectCount    int            `json:"hard_reject_count"`
    BlockedByNetEV     int            `json:"blocked_by_net_ev"`
    BlockedByHardReject int           `json:"blocked_by_hard_reject"`
    BlockedByBalance   int            `json:"blocked_by_balance"`
    TopRejectReasons   map[string]int `json:"top_reject_reasons"`
}
```

**2. 诊断函数（`unified_rotation.go`）**
```go
// rotationDiagnoseCandidates 只诊断不执行：对每个候选跑 rotationCanOpenOpportunity，累计分布。
func rotationDiagnoseCandidates(candidates []UnifiedOpportunity, available map[string]float64, cfg RotationConfig) RotationDecisionDiag
```
- 复用 `rotationCanOpenOpportunity`（`unified_rotation.go:446-493`）的 reason 返回值归类：
  - `net_ev<=0` → BlockedByNetEV
  - `venue_balance_insufficient:*` / `requires_rebalance_unfundable` → BlockedByBalance
  - `reject:*`（hard） → BlockedByHardReject
  - 其余（cooldown/min_notional/max_concurrent/budget）单独计数

**3. 调用点（`unified_rotation_runner.go:632` 附近，DecideRotation 前）**
```go
diag := rotationDiagnoseCandidates(candidates, freeAvailable, r.rotationCfg)
diag.CycleID = snapshotID
appendRotationDecisionDiag(r.recordDir, diag)   // → records/opportunities/rotation_decision_diag.jsonl
```

**4. 升级扫描完成日志（`unified_rotation_runner.go:1056`）**
```
[统一轮动] 扫描完成 snapshot=%s funding_symbols=%d
  raw_total=%d raw_by_strategy=[svsp=%d cvsp=%d svps=%d cvps=%d perp_perp=%d]
  scanner_would_exec=%d rotation_would_exec=%d soft_reject=%d hard_reject=%d
```

**5. 升级决策完成日志（`unified_rotation_runner.go:657`）**
```
[统一轮动] 决策完成: held=%d raw=%d rotation_candidates=%d actions=%d
  blocked[net_ev=%d hard=%d balance=%d] top_reject=%q live=%v
```

**6. `unified_candidates_log.go` 字段区分**
- `UnifiedCandidateLogRecord` 加 `ScannerWouldExecute bool` + `RotationWouldExecute bool`，替代当前单一 `WouldExecute` 的混用（保留 `WouldExecute` 兼容旧面板，标记为 scanner 口径）。

### 验收
每轮能直接读出"574 里 rotation_would_execute=?、死因 top3=?"；读不出 = P0 没达标。

---

## P1：输入池去污（决策只吃可开候选）

### 改动（`unified_rotation_runner.go` 汇总处，`defaultScanRotationCandidates` 返回后）

```go
// 分层：诊断全量 vs 决策可开
diagnosticCandidates := candidates  // 全量，写 jsonl + 面板 + P0 诊断
rotationCandidates := make([]UnifiedOpportunity, 0, len(candidates))
for _, opp := range candidates {
    if rotationFirstHardRejectReason(opp) != "" { continue }    // 硬拒
    if opp.CandidateOnly { continue }
    if unifiedOpportunityTotalCapital(opp) <= 0 { continue }
    if opp.ExpectedNetEVUSDT <= rotationMinNetProfitUSDT(r.rotationCfg) { continue } // 含 soft net_ev<=0
    rotationCandidates = append(rotationCandidates, opp)
}
```
- `DecideRotation` / `runOnceDecisionAndDispatch` 只吃 `rotationCandidates`。
- `unified_candidates.jsonl`、P0 诊断、面板继续用 `diagnosticCandidates`（全量）。
- 复用 `rotationFirstHardRejectReason`（`unified_rotation.go:294-307`）+ `rotationScannerSoftRejectReasons`（`:288-292`）。

### 注意
- 仅 soft reject（`net_ev<=0`）的对象也被上面 `netEV<=min` 这条挡掉——符合"soft reject 也不进 rotationCandidates"。
- 决策日志的 `rotation_candidates=%d` 即 `len(rotationCandidates)`，与 P0 的 `rotation_would_execute` 对齐校验。

### 验收
`DecideRotation` 输入从 ~574 降到真实可开数（预计个位数~两位数）；决策器不再替扫描器清垃圾。

---

## P5：MNT 重复 flatten 收口（与 P0 并行）

### 现状
- 现货 orphan 各策略各扫一次，无跨策略去重（`position_dust.go:372-385`）。
- 永续腿有 `carryStrategyOwnsPerpLeg` 去重，现货腿没有等价物。
- 冷启动 flatten fire-and-forget，无二次核验。
- 证据：MNT-USDT≈18.43 被 SVSP+CVSP 同刻扫 14 次。

### 改动

**1. 现货 orphan 跨策略归属（`position_dust.go` + `carry_position_reconcile.go`）**
```go
// carryStrategyOwnsSpotLeg 查四个 carry store 是否已登记该 (venue, symbol) 现货腿。
func carryStrategyOwnsSpotLeg(recordDir, venue, symbol string) bool
```
- 在 `carryScanUntrackedSVSPLegs` / `carryScanUntrackedCVSPLegs` 现货发现处（`:372-385` 等）加：
  ```go
  if carryStrategyOwnsSpotLeg(recordDir, name, sym) { continue }
  ```
- 与永续腿 `carryPerpLegSkipUntrackedScan` 对称。

**2. recent_flatten_registry（新增 `records/recent_flatten.jsonl` + 内存表）**
```go
type recentFlattenKey struct{ Venue, Symbol, Side string; QtyBand int }
// 记录最近 N 分钟已 flatten 的腿；flatten 前查，命中则跳过。
func (e *...Executor) recentlyFlattened(key recentFlattenKey, within time.Duration) bool
func (e *...Executor) markFlattened(key recentFlattenKey)
```
- `flattenUntrackedLeg` 入口先查 registry，命中跳过 + 打日志 `已近期处理，跳过重复 flatten`。
- QtyBand 用对数分桶（防数量微小漂移绕过去重）。

**3. flatten 后二次核验**
- `flattenUntrackedLeg` 下单后改为：重拉 `FetchBalances`/`FetchPositions` 确认该腿已清；
  - 已清 → markFlattened + 正常日志
  - 未清 → 标记 `unresolved_orphan`（写 `dust_ledger.json` 或独立 ledger），**下次不再当全新事件**重复下单

**4. （可选，彻底版）统一 spot-orphan reconciler**
- 抽 `runUnifiedSpotOrphanReconcile(ctx)`：扫一次全所现货余额 → 按归属规则分配给某策略处理 → 单点 flatten。
- 由 `runReconcileAll` 在五执行器 reconcile **之前**调用一次。
- A+B 阶段可先用 1+2+3（去重注册表）止血，统一 reconciler 列入后续。

### 验收
MNT 类残腿不再被两套策略同刻扫描/flatten；日志出现 `跳过重复 flatten` 或 `unresolved_orphan`。

---

## 配置层联动（A+B 期间需同步检查）

| 配置 | 位置 | A+B 期间建议 |
|---|---|---|
| `strategy_mode: profit_seeking` | `config.yaml:11` | P0 跑出 blocked_by_net_ev 分布后再决定是否临时降 `min_net` |
| `max_cycle_open_notional_usdt: 60` | `config.yaml:26` | 与 `max_position_usdt:150` 对齐，否则候选转正也开不出 |
| perp-perp 不走 Top150 粗筛 | runner | P3-1 取消 Top30 后，确认 perp-perp 全量进 rotationCandidates |

---

## 实施次序（A+B 一批内的推荐顺序）

1. **P0 + P5 并行**（止盲 + 止血，互不依赖）
2. **P1**（依赖 P0 的 hard/soft 分类口径）
3. **P3-1 取消 Top30**（让 H 进池）
4. **P3-3 深度滑点** → **P3-2 notional 搜索**（搜索依赖深度滑点同口径）
5. **P4 绝对净收益**（最后统一排序，含 SWITCH 一致性单测）

> P3-3 先于 P3-2：notional 搜索的每个 grid 点都要用深度滑点算 netEV，否则搜出来的"最优名义"仍是固定 bps 口径下的假最优。

---

## 单测清单（防回归）

| 测试 | 验证点 |
|---|---|
| `TestRotationPrimaryScoreAbsoluteEV` | 大 EV/大本金 排在 小 EV/小本金 前 |
| `TestRotationSwitchScoreConsistency` | replace 比较两端同量纲（USDT） |
| `TestPerpPerpNoTop30Truncation` | computeCrossVenueOpportunities 返回全量 |
| `TestPerpPerpNotionalSearchPicksMaxNetEV` | 小名义 netEV>0 时被选中 |
| `TestPerpPerpScanSlippageMatchesGate` | 扫描滑点与执行闸滑点同口径 |
| `TestRotationCandidatesExcludeHardReject` | 硬拒/soft net_ev<=0 不进 rotationCandidates |
| `TestRotationDecisionDiagCounts` | 诊断计数与实际拒绝分布一致 |
| `TestSpotOrphanCrossStrategyDedup` | 同一现货残腿不被两策略重复 flatten |
| `TestFlattenSecondVerification` | flatten 后未清标记 unresolved_orphan |

---

## 验收标准（A+B 完成）

1. 每轮日志能读出 `rotation_would_execute` 与死因 top3（P0）
2. `DecideRotation` 输入 = 真实可开候选，远小于 raw（P1）
3. `perp_perp=N` 不再恒为 30（P3-1）
4. H-USDT 在 spike 窗口能 `netEV>0` 且进 rotationCandidates（P3-2/3-3）
5. 全链主排序 = 绝对净收益，日志含 net_ev + capital_efficiency（P4）
6. MNT 类残腿不再跨策略重复 flatten（P5）
7. `actions=0` 从 116/116 常态变成少数市场窗口现象（综合）
