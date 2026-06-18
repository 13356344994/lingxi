# 路线 B 可行性核对:perp-perp 接入统一轮动 Runner

> 来源:Composer 2.5 只读审计(2026-06-18)。对应整改文档任务 17。

**目标**:让 `CrossVenueExecutor`(perp-perp)通过 `rotationExecutorBinding` 与四 spot/perp-spot 执行器同台择优、统一换/平。
**结论**:**可行,但不是"直接 bind 即可"**——6 个 binding 槽位中 3 个缺公开 API、2 个需薄适配、持仓缺 `OpportunityID`;且 run.go 中独立 `CrossVenueExecutor.Run` 与统一 runner 并存,不处理会导致**双重开仓/双重 manage/并发平仓**。

---

## 1. CrossVenueExecutor 现状 vs rotationExecutorBinding 六函数

binding 需:openFn / closeVerifiedFn / realHeldCountFn / heldFn / reconcileFn / manageFn(unified_rotation_runner.go:30-38)。

| 槽位 | 现状 | 判定 |
|------|------|------|
| openFn(ctx, UnifiedOpportunity, live) | 无;内部是 executeCrossVenueOpen(ctx, plan, live)(cross_venue_open.go:150) | **缺失需新增** |
| closeVerifiedFn(ctx, oppID, symbol, reason) | 无;有私有 closeCrossVenuePosition(cross_venue_open.go:377)、批量 CloseAllPositions(cross_venue_executor.go:1248) | **缺失需新增** |
| realHeldCountFn(ctx) | 无;有 snapshotPositions(:1115) | **缺失需新增** |
| heldFn() | snapshotPositions()(:1115) | **需适配**→collectCrossVenueHeldRecords(新) |
| reconcileFn(ctx, live) | reconcileOnStart(ctx) 无 live(:899) | **需薄适配** ReconcileOnStart |
| manageFn(ctx, live) | manageOpenPositions(ctx, live) 小写(:469) | **需导出** ManageOpenPositions |

底层开平仓/台账/风控已成熟,缺的是与 cvsp 同构的 **rotation 适配层**。

---

## 2. CrossVenuePosition → HeldPosition 映射

**关键阻塞:`CrossVenuePosition` 无 `OpportunityID` 字段**(cross_venue_positions_store.go:55-134),而 collectHeldPositions 会跳过空 OpportunityID(unified_rotation_runner.go:720-722)。必须开仓时写入 opp.ID(或对存量仓 fallback 派生 `unifiedOpportunityID(perp_perp, Symbol, EntrySnapshotID, HighVenue, LowVenue)`)。

建议新增 `collectCrossVenueHeldRecords`(参照 collectCrossVenueSpotPerpHeldRecords :989-1011):
- Capital:notional=crossVenuePositionNotionalUSDT(:620-628);margin=rotationPerpMarginWithBuffer(:1104-1111);CapitalRequiredByVenue={HighVenue:margin, LowVenue:margin}。
- ContinuationInputs:ExpectedFundingUSDT=pos.EstFundingUSDT;ExpectedBorrowCostUSDT=0(perp-perp 无借币);HoldingRiskUSDT=notional×0.001。
- delistLegs:{HighVenue,MarketPerp}+{LowVenue,MarketPerp}(两腿均 perp)。

---

## 3. UnifiedOpportunity ↔ perp-perp 开仓

候选侧已存在 `UnifiedOpportunityFromCrossVenue`(unified_opportunity.go:90-132)+ 注入(:681-689)。**反向 `UnifiedOpportunity→CrossVenueOpportunity` 不存在,openFn 需新增**:还原 Symbol/HighVenue/LowVenue(从 Legs Side)、SnapshotID(从 opp.ID 解析)、funding/EV 字段。

openFn 内部链路建议:UnifiedOpportunity → CrossVenueOpportunity(新)→ enrichOpportunityFromSnapshot(:740)→ evaluateCrossVenueOpenGate(cross_venue_gates.go:63)→ crossVenueOpenPlan + executeCrossVenueOpen(:150)→ 写 OpportunityID + upsert。

**Notional 对齐风险**:gate 内 notional 来自 cfg.MaxPositionUSDT+深度cap,与候选 Legs[].Notional 可能不一致;openFn 应把决策 notional 注入 gate(cap 到 rotationMaxLegNotional)。

---

## 4. 资金统一与 double-sizing 风险

两套 sizing:统一 runner(rotationFreeAvailableUSDT→DecideRotation→PlanCapitalRouting) vs perp-perp 独立(fetchPerpAvailableUSDT + allocatedUSDT 杠杆上限,cross_venue_gates.go:202-224)。带配额的 RunUnifiedShadowAllocator 仅测试,未接线。

| 风险 | 机制 | 严重度 |
|------|------|--------|
| 双重开仓 | exec.Run→evaluateNewOpens(:405) 与 runner openOpportunity 并行 | **高** |
| 双重 manage | exec.tick→manageOpenPositions(:380) 与 runner runManageAll(:297) 并行 | **高** |
| 并发平仓 | manage shouldCloseCrossVenue→close 与 runner closeHeldVerified 同周期 | 中 |
| 保证金重复划拨 | ExecuteCapitalRouting + ensureCrossVenuePerpMargin | 中 |
| 并发上限双计 | CrossVenueExecutor.MaxConcurrent vs RotationConfig.MaxConcurrent | 中 |

**路线 B 必做**:接入 binding 后**关闭** CrossVenueExecutor 独立 open 路径(SetOpeningsEnabled(false) 或 tick 跳过 evaluateNewOpens),并避免双 manage(只保留 runner manageFn 一条路径)。

---

## 5. manage vs verified-close 协调

manage 会触发的平仓:closing 续平、孤腿、两腿已平 finalize、对冲偏差 rebalance、强平距离 reduceCrossVenueRisk(50% 减仓非全平)、价差硬/软止损、内部 switch(cross_venue_manage.go:793-849)。

缺口:① 无 CloseOnePositionVerified(无法"无交易所腿→只清台账");② 无 oppID 查找;③ 双 manage 循环加速 CloseAttempts escalation;④ 内部 switch 与统一 SWITCH 重复。

协调建议:manage 负责孤腿/rebalance/强平距离/硬止损/closing 续平;统一 CLOSE/SWITCH 走 CloseOnePositionVerified(reason 带 rotation 前缀);停用 evaluateNewOpens 与内部 switch 开新。

---

## 6. 最小改动接线清单(只描述)

**Step1 执行器适配 API**:新增 executeOpen(UnifiedOpportunity)、CloseOnePositionVerified、CountRealHeldPositions;薄包装 ReconcileOnStart/ManageOpenPositions;CrossVenuePosition 增 OpportunityID 字段。
**Step2 Runner binding**:collectCrossVenueHeldRecords + bindCrossVenuePerpPerpRotationExecutor + NewUnifiedRotationRunner 增参注册 UnifiedStrategyPerpPerp。
**Step3 装配消歧(run.go)**:注入同一 CrossVenueExecutor 实例;停 go exec.Run 的 open+manage(或 SetOpeningsEnabled(false)+跳过 manage),仅保留 RunEquitySampler;更新 :162 注释。
**Step4 资金/notional 对齐**:openFn gate notional 对齐 rotationMaxLegNotional;评估跳过 ensureCrossVenuePerpMargin 改依赖 ExecuteCapitalRouting;统一 MaxConcurrent。
**Step5 控制面/可观测**:countRotationCandidates 增 PerpPerp 计数;buildRotationPositionViews 增 perp-perp 视图;delist 两腿 perp。
**Step6 Switch 决策**:统一 SWITCH 替代 crossVenueSwitchCloseReason,或分层(内部 switch=同 symbol 腿迁移,runner SWITCH=跨策略)。

---

## 7. 风险点

1. 双循环未消歧→双重开仓/manage(run.go:157+180)
2. OpportunityID 缺失→held 不入池、CLOSE 无法定位
3. 快照 ID 每轮变→必须开仓时持久化 ID
4. 内部 switch 与统一 SWITCH 冲突
5. reduceCrossVenueRisk 减仓 vs CLOSE 全平交叉
6. gate 与 router 重复备资
7. 存量无 OpportunityID 仓需 fallback/backfill
8. reconcileOnStart 双调用(exec.Run :346 与 runner :269)

---

## 8. 必须新增测试

bind/collect smoke;executeOpen from UnifiedOpportunity(shadow/live);CloseOnePositionVerified(无腿幂等清台账 / 双腿 verified close);CountRealHeldPositions;PerpPerp held 进 DecideRotation 占 max_concurrent;有 binding 时 OPEN 派发;perp-perp↔cvsp SWITCH+路由;SetOpeningsEnabled(false) 后仅 runner 开仓;manage 硬止损+runner CLOSE 幂等;无 ID 存量仓 fallback。

---

## 9. 总结

技术可行(底层成熟);工作量中等(3 新公开方法+1 持仓字段+1 collect/binding+装配层双循环改造);阻塞项:① 停用独立 open/manage 双循环 ② OpportunityID 持久化 ③ 反向映射。**不建议在不改 run.go 的情况下仅加 binding**——必然 double-sizing。正确形态:perp-perp 引擎降级为"绑定执行器",统一 runner 成唯一 open/manage/reconcile 调度面。
