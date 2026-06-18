# Lingxi 账户级风控覆盖矩阵

> 来源:Composer 2.5 只读审计(2026-06-18)。对应整改文档 P0-C / 任务 9。

**当前 live 路径**：`CrossVenueExecutor`（perp-perp）+ `UnifiedRotationRunner`（SVSP/CVSP/SVPS/CVPS 四 carry）
**Legacy**：`FundingRateSniperEngine`（单所 Binance，2026-06-17 de-wire）

---

## 1. 结论摘要

| 维度 | 当前 live 路径 | Legacy FundingRateSniperEngine |
|------|----------------|-------------------------------|
| 日亏/单周期亏/高水位回撤/连亏熔断 | **未挂载** | 完整挂载 |
| 100ms 实时浮亏 monitor + panicFlatten | **未挂载** | 挂载 |
| live_daily_risk_state.json | **未挂载** | 挂载 |
| acquireSingleWriterLease(单写租约) | **未挂载**（live Start 已被拦截,不可达） | 代码在 runLiveEngine |
| .halt-live 急停 | perp-perp **仅禁开新仓** | 主循环 fatal |
| 交易所 dead-man | perp-perp **部分 venue**(OKX/Gate) | Binance 逐 symbol |
| watchdog + lingxi flatten | 进程级有效;flatten **仅读 legacy 台账** | flatten 目标台账 |

**核心判断**:账户级风控实现集中在 `FundingRateSniperEngine`;`startLiveStack` 装配的 cross-venue + rotation 路径**不会**调用这些方法。`RiskConfig.MaxDailyLossUSDT` 等对当前 live 路径**无运行时效果**。

De-wire 证据:`run.go:185-186`、`runtime_controller.go:301-302`(live Start 硬拒)、`strategy_control.go:20-26`(默认不自启 legacy)。

---

## 2. 当前实盘 goroutine 拓扑(file:line)

| # | 组件 | 入口 | 条件 |
|---|------|------|------|
| G0 | 进程心跳 | run.go:83 → observability_http.go:56 | 始终 |
| G1 | CrossVenueScanner | run.go:150 → cross_venue_scanner.go:271 | scanner.Enabled |
| G2 | CrossVenueExecutor 主循环 | run.go:157 → cross_venue_executor.go:331 | scanner & executor.Enabled |
| G3 | 权益采样(3min) | run.go:158 → equity_snapshots.go:111 | 同上 |
| G4 | UnifiedRotationRunner(60s) | run.go:180 → unified_rotation_runner.go:223 | scanner.Enabled |
| 主 | Dashboard HTTP | dashboard_server.go:151 | 始终 |

**不会**启动的 legacy goroutine(仅 FundingRateSniperEngine.Start 内,funding_sniper_engine.go:151-165):startRealtimeDrawdownMonitor、startDailyRiskStateWriter、startDeadMansSwitch、exposureManager.Start、主风控循环等。

---

## 3. 覆盖矩阵

图例:wired=当前 live 会调用;not-wired=无 call site;legacy-only=仅 FundingRateSniperEngine;dashboard-only/CLI-only。

| 风控机制 | CrossVenueExecutor | Rotation+四carry | Legacy Engine |
|----------|-------------------|------------------|---------------|
| startRealtimeDrawdownMonitor | not-wired | not-wired | legacy-only (funding_sniper_engine.go:160→realtime_drawdown.go:24) |
| panicFlattenManagedExposure(运行时) | not-wired | not-wired | legacy-only (realtime_drawdown.go:58) |
| MaxDailyLossUSDT 硬熔断 | not-wired | not-wired | legacy-only (live_safety.go:455 ← funding_sniper_engine.go:174) |
| MaxCycleLossUSDT | not-wired | not-wired | legacy-only (live_safety.go:459；realtime_drawdown.go:77) |
| MaxAccountDrawdownUSDT | not-wired | not-wired | legacy-only (live_safety.go:475-480) |
| 连亏 MaxConsecutiveLossCycles | not-wired | not-wired | legacy-only (live_safety.go:471) |
| live_daily_risk_state.json | not-wired | not-wired | legacy-only (live_safety.go:99/221) |
| acquireSingleWriterLease | not-wired | not-wired | legacy-only(不可达,runtime_controller.go:301)；CLI-only(emergency_flatten.go:63) |
| .halt-live 急停 | **wired(仅禁开仓)** cross_venue_executor.go:465-466 | not-wired | legacy-only fatal (live_safety.go:401) |
| 交易所 dead-man 续期 | **wired(perp-perp,OKX/Gate)** cross_venue_manage.go:114,921 | not-wired | legacy-only (funding_sniper_engine.go:165) |
| 进程 watchdog 心跳 | **wired(进程级)** run.go:83 | 同左(共享进程) | 同左 |
| watchdog flatten 兜底 | **not-wired(台账未纳入)** watchdog.sh:102-110 | not-wired | CLI-only 仅 legacy 台账 |
| Dashboard flatten-all | dashboard-only strategy_control.go:286 | dashboard-only strategy_control.go:301-359 | dashboard-only |

Position store SSOT:perp-perp=records/crossvenue/positions.json;四 carry=records/<strategy>/positions.json;legacy=records/live_open_hedges.json。

---

## 4. 最坏情况(当前架构无账户级兜底)

1. 全账户当日已实现+浮亏超 MaxDailyLossUSDT — 阈值仅 legacy 调用。
2. 单周期/单 symbol 浮亏超 MaxCycleLossUSDT — 无 realtime monitor。
3. 跨日慢性失血超 MaxAccountDrawdownUSDT — live 路径不更新累计曲线。
4. 连亏 fuse — live 路径不更新 live_daily_risk_state。
5. 行情失明 — rotation 无 feed-blindness panic flatten。
6. 主进程 kill -9 后裸仓 — watchdog flatten 不覆盖 crossvenue/rotation 台账,无自动 CLI 收敛。
7. 双进程同账户 — live 路径不抢单写租约,理论可双开。
8. Rotation active=true 时 .halt-live 不绑定 — 急停只挡 perp-perp 开仓,挡不住 carry 开新仓。

仍有的非账户级兜底:per-position manage(到期/孤腿/断腿)、CVE 健康闸禁开、MaxConcurrent/MaxPositionUSDT、部分 venue dead-man、Dashboard 手动 flatten-all。

---

## 5. 接入建议(只描述)

**阶段 A 只读报警**:① 账户级 PnL 聚合 watcher(挂 run.go:83 同级,对比 MaxDailyLoss 仅报警);② 扩展 watchdog ledger_has_hedges 纳入五类台账;③ Dashboard 暴露「live 是否 wired MaxDailyLoss」;④ Rotation runOnceSafe 入口读 .halt-live。

**阶段 B 硬熔断**:① 抽共享 AccountRiskBudget(CVE tick + rotation runOnce 共同调用,新 records/account_risk_state.json);② 实时浮亏 monitor 下沉到共享 supervisor,触发先 SetOpeningsEnabled(false)+SetActive(false) 再按 store CloseAllPositions;③ live 路径恢复 single-writer lease;④ 统一 emergency flatten 覆盖五类 store + 同步 watchdog.sh;⑤ dead-man 覆盖 carry(OKX/Gate perp 腿)。
