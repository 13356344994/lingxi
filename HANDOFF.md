# LINGXI 方向量化 — 交接文档(2026-06-28)

> 给接手 agent:看这一份即可接手。系统为**四所方向量化"微仓广撒网探索档"**,目标是用户的"小资金广撒网多捞钱 / 超频 scalper"。当前**尚未找到扣费后稳健正 edge**,处于微仓采样阶段。

---

## 0. 铁律(务必遵守)
- **单一写入者串行**:后台另有一个 45min 运维巡检 loop agent(`lingxiopt`)也会改代码/重启。改动前注意冲突;一次只一个写入者。
- **不凭直觉上量**:任何边际敏感改动(出场时序/选币/信号)**先用离线框架验证**再上线。已有惨痛教训(资费 carry 被单窗口过拟合骗到)。
- 改完必须 `go build ./... && go vet ./internal/app/ && go test ./internal/app/ -run 'Directional|OKX|...' -count=1` 全绿再部署。
- **只在用户明确要求时 git commit**。

---

## 1. 部署 / 运行
- 单 binary,systemd 管理。部署:`./start.sh`(go build + SELinux restorecon + `systemctl restart lingxi`)。
- 日志:`records/lingxi_server.log`。状态:`systemctl is-active lingxi`。
- 面板:`http://127.0.0.1:8080/`;方向 API:`/api/okx-directional?venue=okx|bybit|binance`(含 stats_today/stats_all_time)。
- 结算:`records/{venue}_directional/settlements.jsonl`;持仓:`.../positions.json`;控制:`.../control.json`。

## 2. 配置优先级(重要,踩过坑)
**`config.yaml` 是真源,覆盖 `internal/core/defaults.go`**;`records/{venue}_directional/control.json` 再覆盖 config 的 `active / trading_capital_usdt / stop_loss_pct`。改参数优先改 config.yaml(否则改 defaults 无效)。`stale_cut_minutes` 不在 config → 用 defaults(=30)。
- ⚠️ **已知三处不一致(待对齐,边际敏感先别动值)**:`stop_loss_pct` config=0.20 / defaults=0.05 / control(实际生效)=0.05;`max_hold` config=60 / defaults=30;`take_profit_pct` 是**死参数**(live 出场走 `decideDirectionalPositionAction`,不读它)。
- ✅ **生效参数已上启动日志(2026-06-28)**:引擎启动打印 `[OKX方向][生效参数]` 四行(本金/止损/杠杆/并发/每仓名义、入场闸、出场闸、regime 闸),已含 control 覆盖。**排查"改了配置没生效"先看这四行。** 代码 `logEffectiveParams()`。

## 3. 当前线上配置(已部署)
- 活跃所:**OKX / Bybit / Binance**;**Gate 已停用**(曾返回假成交价/幽灵仓)。
- 每所:`trading_capital_usdt=15`、`stop_loss_pct=0.05`(本金口径,5x≈1%价格)、`active=true`。
- `max_concurrent=30`(上限)、`per_position_notional_usdt=7.5`(**固定名义模型**:每仓7.5U名义/1.5U保证金,实际持仓数=资金/1.5U,上限30 → 15U/所≈10币、三所≈30币)。`leverage=5`。
- 入场(为"广撒网"已放宽,**但见 §6 待决策**):`min_open_conviction=0.55`、`zscore_entry=1.0`、`cost_floor_bps=40`、`open_taker_fallback_bps=30`(maker挂不上以≤30bps IOC吃单兜底)、`universe_top_n=20`。
- 出场:`max_hold=60`、`stale_cut=30`、`abs_max_hold=60`、quick_tp=0.12、partial_tp=0.06、trail_gap=0.06。
- 循环:完整 90s + **快速风控 20s**(仅盘口、触5%止损即吃单快平)。
- 跨所印证:**仅当≥2所反向且0同向才否决**(已从"0同向即否决"软化);否则按同向/反向数加减信心。
- global_halt:方向引擎**只认 `.halt-live` 人工急停**(`IsEmergencyHaltActive`),**不被账户回撤型持久 global_halt 锁死**(2026-06-28 14:42 修复)。
- 趋势态:**已禁用**(`TradeTrendRegime=false`,回测最大失血源),线上确认 0 trend 开仓。
- 仓位级统计:已实现(`PositionID` 合并分批止盈),面板显示仓位级胜率/PF/最大亏。

## 4. 关键已验证结论(别重复踩坑)
1. **当前 5m 价格信号(EMA/z-score 反转)扣费后无净 edge**:离线全配置(信心0.55→0.99 × 出场风格 × regime × 5/8/12bps成本)均净负;实盘**仓位级 PF≈0.77**(81仓)。
2. **砍得越快越亏**:stale5 净-535%/胜26% vs stale40 净-69%/胜49%;反转/棘轮 churn 在高信心档也伤 edge。→ 不要再缩短 stale。
3. **资费 carry-fade 不稳健**:5m/25h 窗口中度资费桶净+1.17%,但 1H/12天窗口符号翻转 → 过拟合,非真 edge。
4. **"高资费+高ATR 肥币池"有害**(跨窗口+实盘一致);极端 funding 是最差桶。
5. **覆盖受"信号供给"限制**:shortlist 中 85-89% 是"无信号";给再多资金也撒不满30币。"广撒网"瓶颈在信号数量,不在资金/并发。
6. 离线研究工具(已加,默认skip,`EDGE_BT=1` 跑、需外网):`internal/app/directional_exitsim_test.go`(`TestDirectionalScalpExitSweep`/`TestDirectionalScalpEntryQuality`)、`directional_funding_edge_test.go`(`TestDirectionalFundingCarryEdge`)。

## 5. 待修真 bug(监控盯盘发现,优先级高)
- ✅ **`cooldownUntil` 不落盘 —— 已修(2026-06-28)**:已持久化 `cooldownUntil + coinLossCount + dayStartCST` 到 `records/{venue}_directional/cooldowns.json`,启动 `restoreCooldowns()` 恢复。跨日只恢复未到期冷却、丢弃旧亏损计数;同日恢复并钉死 `dayStartCST` 防 `refreshCSTDay` 误清。落盘点:`closePosition`、`refreshCSTDay`(跨日清零后)。代码 `okx_directional_engine.go`:`persistCooldowns/loadCooldowns/restoreCooldowns`。**自此"硬止损→当日 ban""单币亏2次→晾一天"重启后仍生效。**
- 🔴 **账户风控全天空转**:从 10:19 起每分钟报"真实现金单 tick 骤降 65-122U > 阈值"→ 权益数据判不可靠 → 294次跳过、回撤/日损保护未运行。根因疑为**重启后权益快照基线重置**(差值误判骤降)+ `cum_fund_flow≈-64U` 来源不明。**修:权益基线跨重启持久化;排查 -64U 出流。**(注:已不影响方向探索档开仓,因方向只认 .halt-live;但账户级日损保护空转。)
- 注:盯盘3 曾报"global_halt 回归锁死"是**误报**(分析跨越了14:42修复点),实际已解除。

## 6. ⚠️ 待用户决策(我已提出,未拍板)
**GPT 第7条主张"超频≠降门槛,应保持信号质量、提高检查频率"**,与我们一小时前为"广撒网"做的放宽(conviction 0.55、zscore 1.0、taker fallback)**直接冲突**。
→ **决策点:是否把入场门槛收回质量优先(0.55→0.7、1.0→1.2、关/留 taker fallback),改用"多币×高频检查"获取广度?** 我倾向收回(放宽大概率在加垃圾单)。**接手 agent 请先与用户确认此项再动入场参数。**

## 7. 下一步路线(GPT 超频 scalper 蓝图 + 我的"先测后建"纪律)
**硬事实:超频是放大器不是印钞机。核心 EV 现为负,先做"能创造 edge"的模块,别先堆速度。**
- ✅ **阶段A —— 已落地(2026-06-28)**:**成交后仪表化已上**。`settlements.jsonl` 每条新增 `hold_seconds / entry_slip_bps / exit_slip_bps / open_via_taker / close_via_taker / fee_usdt(交易所实扣) / mfe_account_pct / mae_account_pct`(本金口径运行态 MFE/MAE,在 90s+20s 两循环采样)。**仅记录、不改交易行为。** 注:MFE/MAE 当前是"自开仓起的运行态高/低水位"(非严格 10/30/60s 切片;要精确秒级切片需新增快采样器,见阶段B)。**接手第一件事:攒够样本后做离线归因——按 `open_via_taker` 分桶比 PF(taker fallback 是否在亏)、按 `mae_account_pct`/`hold_seconds` 验证秒级退出能否把 PF 拉过 1。这就是 §6 决策(收不收回放宽门槛)的数据依据。**
- **阶段B**:快速管理循环(止盈/移动止损/停滞/反转进 5s 循环;硬止损 3-5s);成交后无正反馈秒平(阈值用阶段A数据定)。
- **阶段C**:5m 只做候选,1m/30s bookTicker 做临门入场确认(spread稳定/盘口不抽流动性/方向已启动)——治 maker 逆选择。
- **阶段D**:自动降档(symbol/venue/global 的 PF+连亏驱动,只调小/调严/冷却,**不停机**)。
- **阶段E**:universe 重写为"可交易性优先"(spread稳定/盘口深度/滑点/tick适配),资费仅辅助;负分项:极端funding/高ATR/近期硬止损币/停滞亏损币/价差抖动/薄盘口降权。
- 判据始终是**仓位级 PF**:持续>1 才放大仓位/资金,否则维持微仓采样。

## 8. 进行中
- 盯盘 composer:执行健康/风控质量/广撒网覆盖 三份已完成(结论已并入本文档 §4/§5);**盈亏PF归因盯盘(agent a04b0b8d)可能仍在跑**,其结论(按币/资费/出场原因的盈亏归因、最赚最亏子集)接手后请查看并入。
