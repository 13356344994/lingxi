# LINGXI 方向量化 — 交接文档(2026-06-28)

> 给接手 agent:看这一份即可接手。系统为**四所方向量化"微仓广撒网探索档"**,目标是用户的"小资金广撒网多捞钱 / 超频 scalper"。当前**尚未找到扣费后稳健正 edge**,处于微仓采样阶段。

> ⚠️ **定性写死(务必认清,别再误判)**:**当前系统是"微仓 5m 慢信号探索器(micro 5m exploration)",不是最终的超频 scalper(ultra-frequency scalper)。**
> - 信号来自 **5m K 线 regime(慢)**;入场 = 5m 收盘即开(无 1m/30s 临门确认);管理 = 90s 完整循环 + 20s 硬止损(非秒级)。
> - **30–60min 的持仓时长是"慢信号"的必然结果**,不是 scalper 行为。离线已证:在 5m 慢信号上缩短持仓/scratch/价格型入场确认**都不能把 PF 拉过 1**(见 §4)。
> - **真正的 scalper 需要新增一个 fast layer**(秒级成交后反馈 + 1m/30s 微结构入场确认 + 3-5s 快速管理),而不是把 5m 慢信号跑快一点。
> - 已加 **fast scalper 骨架开关(全部默认 false、当前无任何引用=inert,改值不影响实盘)**:`fast_scalper_enabled / post_fill_feedback_enabled / fast_manage_enabled`。结算/持仓已加 **分层标记** `signal_layer / entry_layer / management_layer`(当前全 = `slow_5m` / `slow_5m_close` / `slow_90s_20s`),为未来"慢探索层 + 快 scalper 层"并存预留。**启用 fast 层前必须先实现该层 + 离线验证 + 用户确认。**
> - ⚠️ **骨架已实现但【未部署】(6-28 20:36)**:上述开关 + 分层字段的代码已在工作区(build/vet/test 全绿),但**运行中的 binary 尚未包含**,故新仓/结算暂时还不带 `signal_layer` 等字段。**刻意不为它单独重启**(避免打断 OpenMicro v2 / settlement env 采样节奏)。**下次因其他必要原因重启/部署时顺带带上即可。未 commit。**

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

## 3. 当前线上配置(已部署,2026-06-30 09:30 更新)
- 活跃所:**OKX / Bybit**(active=true);**Binance 已停新开(active=false,6-30,仅看护在场仓)**;**Gate 已停用**。
- 资金档(capital_regime,**已分化,统计务必分开**):**OKX=50U、Bybit=50U(6/28 19:25 BJ 起)、Binance=15U(已停新开)**。`stop_loss_pct=0.05`。
- ✅ **regime_degrade 风险降档已上线灰度(6-30 09:30)**:命中坏环境只把**新开仓名义 7.5U→5U**(不停机/不禁开/不改信号/cross_venue/cost_floor/stop/TP/stale/cooldown)。**离散 OR 命中**(各记单一 `regime_degrade_reason`):`transition_band`(btc_trend∈[0.1,0.2))/ `extreme_trend`(btc_trend≥0.4)/ `broad_up`(market_mode)。**刻意不罩 0.2-0.4 与 mixed**。config:`regime_degrade_enabled=true`、`_notional_usdt=5`、`_btc_lo=0.1`、`_btc_hi=0.2`、`_btc_extreme=0.4`、`_modes="broad_up"`。回滚:`_btc_extreme:0`(关 extreme 段)或 `_enabled:false`(全关)+重启。详见 §11.5。
- **OpenMicro v2 record-only 埋点已部署(6/28 18:34 BJ)**:`open_micro_record_enabled=true`、`open_micro_record_venue=""`(所有活跃所)。**纯记录、不改任何交易行为**(详见 §7/§9)。
- ✅ **cross-venue lead-lag peer 采集器已上线(7-02,record-only)**:`internal/app/fast_peer_ws_collector.go`。**OKX + Binance + Bybit 三所公共 WS**(不接私有/不读账户/不下单/不改 scorer/exit/资金)。symbol=**三所合约清单交集**(种子=主流 + OKX shortlist,上限 `fast_ws_max_symbols`=15),写 `records/crossvenue/fast_peer_leadlag_events.jsonl`(venue=okx/binance/bybit,单文件跨所对齐)。config:`fast_peer_ws_enabled=true`、`fast_peer_venues="binance,bybit"`(OKX 恒为第三 venue)。**已知坑**:(1)Binance/Bybit 只有三所都上市的重叠币才有数据,故必须取交集(否则订阅 OKX 小币=收不到);(2)Binance 消息含 `"e"`(字符串事件类型)会被 Go json 大小写不敏感匹配污染 `"E"` float 字段→整条 unmarshal 失败,已加显式 `Ev string json:"e"` 吸收;(3)**本机到 Binance 期货 aggTrade 流 i/o timeout(仅 bookTicker 可用)**→ Binance 侧只有 book/mid_ret/book_imbalance,trade flow 由 OKX+Bybit 提供。复盘:`records/_fast_leadlag_review.py`(peer midret/tradeimb/bookimb/xspread → 预测 OKX 后续 3/5/10s 位移;门槛 n≥100 & grossPF>1.3 & net@5>1.05 & mean≥5bps & 不依赖单symbol/窗口)。**初步结论(3万事件):Binance midret→OKX 方向性强(grossPF 达 3.69)但幅度仅 0.6-0.9bps,net@5≈0 → 与 OKX 内部同病,暂未发现过成本 edge。** 数据持续累积。
- 🔴 **【定论】位移放大来源研究(7-02,只读 `records/_fast_leadlag_amplify.py`,46.6k 事件/15币)**:系统性搜 ≥5bps 过成本子集,6 维度(时间尺度/波动过滤/symbol分层/venue共振/spread过滤/方向类型)**全部不达标**。要点:(1)**OKX internal dynamic 与 cross-venue lead-lag 都有方向信息,但位移只有 0.5-2.1bps**;(2)**当前 OKX fast universe + 当前执行成本 5-10bps 下,无法支撑 fast live 盈利**;(3)时间尺度不释放位移(30-60s 反而衰减到~0);强 peer move 只提纯 grossPF、不放大 OKX 位移;venue 共振不放大;窄 spread 也救不了(net@5≈0);reversal 明确亏损(顺势才对)。(4)**唯一值得继续追的结构 = "peer 已动 / OKX 未动" 的 lag-follow**(mean 随 horizon 增长:5s+1.4→10s+1.6→**30s+2.1bps**,gPF~2),但 30s mean≈2.1bps **仍低于成本**、n 仅~240。→ 出路只剩 **换 universe(小币滞后空间更大:APT/NEAR/LTC 弹性最高)/ 换成本结构(往返压到~1-2bps)/ 换交易所 / 换执行方式**,而非再调 scorer。**fast live 继续保持 record-only,不恢复真实新开仓。**
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
7. **硬止损是当前亏损主因(post_50U_window 复算,n=264,只读)**:**硬止损仓 净 −10.34U(n107),非硬止损仓 净 +5.92U(n157)**——即"非硬止损整体为正,亏在硬止损"。最强可归因维度=**BTC regime**:btc_trend **0.1-0.2 过渡带 PF 0.30/净−3.08U(最大亏桶,n86)**、`broad_up` PF 0.22/硬止损59%(n17);**非单调**(强趋势 ≥0.2 反而 PF 0.77)。runup/same_side 同向但弱(见 §11)。**taker 非主罪**(taker PF0.58≈maker0.64)。
6. 离线研究工具(已加,默认skip,`EDGE_BT=1` 跑、需外网):`internal/app/directional_exitsim_test.go`(`TestDirectionalScalpExitSweep`/`TestDirectionalScalpEntryQuality`)、`directional_funding_edge_test.go`(`TestDirectionalFundingCarryEdge`)。

## 5. 待修真 bug(监控盯盘发现,优先级高)
- ✅ **`cooldownUntil` 不落盘 —— 已修(2026-06-28)**:已持久化 `cooldownUntil + coinLossCount + dayStartCST` 到 `records/{venue}_directional/cooldowns.json`,启动 `restoreCooldowns()` 恢复。跨日只恢复未到期冷却、丢弃旧亏损计数;同日恢复并钉死 `dayStartCST` 防 `refreshCSTDay` 误清。落盘点:`closePosition`、`refreshCSTDay`(跨日清零后)。代码 `okx_directional_engine.go`:`persistCooldowns/loadCooldowns/restoreCooldowns`。**自此"硬止损→当日 ban""单币亏2次→晾一天"重启后仍生效。**
- 🔴 **账户风控全天空转(仍未修,6/28 19:50 复查仍在刷:近300行50条)**:每分钟报"真实现金单 tick 骤降 > 阈值"→ 权益不可靠 → 跳过、回撤/日损保护未运行。根因疑**重启后权益快照基线重置** + `cum_fund_flow≈-64U` 来源不明。**修:权益基线跨重启持久化;排查 -64U 出流。**(注:不影响方向探索档开仓,因方向只认 .halt-live;但账户级日损保护空转。)
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

---

## 9. 阶段(phase)与资金档(capital_regime)永久定义 —— 统计绝不能混
分析脚本 `records/_directional_feedback_audit.py`(只读)已固化以下口径:
- **A 旧版大仓**:无 micro(大名义)。历史失血主体(n=213,PF 0.65,净 −28.5U)。
- **B micro_7.5U**:固定名义,无 open_micro 字段。
- **C OpenMicro_v1**:有 `open_micro`、**无** `open_micro.env`。
- **D OpenMicro_v2**:有 `open_micro.env`(2026-06-28 18:34 起)。
- **E taker_fallback 灰度阶段**:`taker_fallback_venues="binance"` 部署起(**6-28 23:29:31 BJ = 6-28 15:29:31 UTC**;实证:该时刻后 OKX/Bybit 再无 taker 开仓、仅 Binance 留 taker;E ≈208 仓)。OKX/Bybit maker-only、Binance 留 taker。**E 是 phase,不是时间窗**。⚠️ 注意是 **6-28** 不是 6-29(曾 typo 成 6-29 → 误剔整整一天 E 数据,已修)。
- **capital_regime(正交)**:大仓 / 15U / **50U(OKX/Bybit ≥6-28 19:25 BJ)**。**50U≠D≠E**,必须单独交叉标记;Binance 全程 15U。
- **analysis_window(第三维,与 phase 区分)**:报告里的时间切片窗口。已命名:**`post_50U_window`=平仓 ≥6-28 19:25BJ(50U切换)的当前资金期**(≈264 仓,跨~30h 多时段;**不是 E,别混**)。
- **铁律**:任何 PF/胜率/亏损结论必须**同时标 phase + capital_regime + analysis_window**,不看全历史混算。**全历史 night PF 差 = 旧版大仓(A)污染,当前版本无 night 样本,严禁据此降档。**

## 10. 进入下一步的样本门槛(硬性,不达不动实盘)
- decision **成交侧 env ≥10**:只能做极早期 opened-vs-rejected 倾向对比;
- decision **成交侧 env ≥30**:才可讨论"过滤候选";
- **settlement env ≥30 且盈/亏样本都有**:才可讨论进入阶段2B(实盘规则);
- **50U 阶段平仓 ≥30**:才可判断 50U 效果(与 15U 分开统计);
- 当前(6-28 19:50)实况:decision env 总~419、成交侧 env~13~26、**settlement env 仅 7、D_v2 仅 5、50U 平仓 ≈3** → **全部不达门槛,处于"继续采样",不得进 2B、不得改任何实盘规则**。

## 11. 阶段2B 候选规则清单(草案;均未实现、未上线)
> 每条:依据 / 字段 / 当前样本是否够 / 进实盘最低门槛 / 可能误杀 / 灰度 / 回滚。**当前全部"继续采样/record-only",无一 active 上线。**
> **优先级(6-30 09:30,基于"硬止损=亏损主因"§4.7)**:🥇 **§11.5 BTC/market regime 降档 = ✅ 已上线灰度(downsize 7.5→5U,离散OR)** > §11.3 runup(继续观察,效应弱) > same_side(n<30 不足)。**taker(§11.1)、deep_book(§11.4)已降为观察项。**
1. **taker fallback 限制(分 venue/条件化;门槛已达,2B 方案已写未实现)**——
   - **数据(6-28 22:55,C/D 排除A,maker31/taker30 各≥30)**:C/D maker PF0.95 vs **taker0.49**;D_v2 maker0.99/taker0.36;**okx t0.29 · bybit t0.10 · binance t1.53(例外)**;50U maker1.19/**taker0.09**;taker entry_slip +4~6bps(maker −1)、快进快出3%/停滞27%(maker10%/16%)、spread5.1/runup2.5(maker3.9/1.2)。
   - **2B 方案**:A(首选)**okx/bybit 关 taker、binance 保留**;B 按 `spread≤S & runup≤R` 条件化。
   - **config(不写死所)**:新增 `taker_fallback_venues`(csv 允许名单,**空=现状全允许=默认不变**;设 "binance"=仅 binance 用 taker、okx/bybit maker-only)。可选 `taker_fallback_max_spread_bps/_max_runup_bps`(默认0=关)。
   - **实施点(唯一)**:`placeDirectionalLeg` 开仓腿 taker 兜底闸前加 `openTakerFallbackBpsEffective()`(本所不在名单→返0=maker-only);**只动开仓腿,平仓 IOC/止损/止盈不碰**。
   - **灰度**:`"binance"` 先禁 okx/bybit,观察 30-50 仓比 PF/成交率/错过率/硬止损。**回滚**:置 ""+重启,不影响在场仓。
   - **风险**:成交↓/样本变慢/错过个别好 taker 单;且 maker 自身也才 PF0.95 → 禁 taker 只少漏血、不造 edge(根因仍是 5m 无 edge)。
   - **已实施并灰度部署(E阶段起点 = 6-28 23:29:31 BJ = 6-28 15:29:31 UTC)**:`taker_fallback_venues="binance"`,OKX/Bybit 开仓 maker-only、Binance 留 taker;`openTakerFallbackBpsEffective()` 已上;只动开仓腿,平仓不受影响。启动日志确认 okx/bybit 本所生效=0bps、binance=30bps。
   - **E阶段验证(部署后)**:OKX/Bybit taker=0 ✓、Binance taker 仍可用 ✓;okx/bybit `maker_not_filled` 累计~40(少赚代价);E 阶段平仓 PF~0.5 仍亏(样本<30,未定论)。
   - **mnf 弱反事实(6-29 01:53,OKX 公共1m,bybit用OKX同币近似)**:**OKX 错过的 maker_not_filled 候选若 taker 成交大概率是亏损单**(+30m 盈利占比 0%、均值 −0.59%)→ **"少赚"实为"少亏",支持维持 OKX maker-only**;**Bybit 数据不足(n=1,多为 OKX 未上市币)→ 无恢复 taker 的证据,继续观察**。
   - **当前决定:维持灰度不缩**(不恢复 OKX/Bybit taker);待 E≥30 出 E vs D 正式对比;Bybit 反事实数据补足后再评估。
   - ⬇️ **降级为观察项(6-30 01:52)**:post_50U_window(n=264)复算 **taker PF0.58 ≈ maker0.64,taker 非当前亏损主罪**(主罪是硬止损,见 §4.7 / §11.5)。**不再列为第一候选,维持现状灰度不缩不放,仅继续观察。**
2. **MAE 灾难刹车**——依据:MAE≤−2% 救援率低(早期 80% 仍亏,n小)。字段:`mae_account_pct` 时间切片。门槛:settlement env≥30 且盈亏齐。误杀:深 V 反弹仓。灰度:仅极保守阈值(−2~−3% 账户)、单所。回滚:关闭开关。
3. **runup 防追末端 —— 继续观察(post_50U_window 复算效应偏弱)**——依据:`pre_open_runup_bps`。**post_50U_window(n=264)复算:abs(runup)<2 PF0.76/硬止损35%、≥10 PF0.60/硬止损48%(n33)——方向对(runup高→硬止损高)但弱于早期判断,且最大亏在 2-10 中间桶(PF0.61/净−2.38U/n135),单调性不强。** 字段:`pre_open_runup_bps`。门槛:各桶 env≥30 + 跨窗。误杀:强趋势启动初段;runup 在强 BTC 趋势里可能是正向(需与 §11.5 联动)。灰度:高 runup 候选 maker-only/提 conv(record→active)。回滚:开关。
4. **spread/depth 门槛**——依据:高 spread/低 depth_cover 桶 PF 差(当前被 cross_venue 选择性偏差污染,不可用)。字段:`open_spread_bps`/`depth_cover_ratio_10bps`。门槛:outcome env 足够且去偏。灰度:开仓预检阈值。回滚:开关。
   - 🔴 **deep_book 专项验证结论(6-30 01:31):不进入 2B,降级为"继续 record-only 观察 / 研究线索"。**
   - **背景**:影子快信号首份前向收益表(6-30 01:22)曾显示 `deep_book`(depth_cover≥100x)+5m 扣成本后为正(+0.11%),一度被列为"2B 第一优先候选"。**专项 8 维验证后已收回该定性**(脚本 `records/_deep_book_probe.py`,只读/反事实,`SHADOW_FWD=1` 跑;数据=单一 58 分钟窗口 06-29 16:29–17:27 UTC,n=221、deep_book 121、真实成交侧仅 opened 4/taker 0)。
   - **不进 2B 的原因(6 条)**:
     1. **depth 分桶不单调**:<20x +5m **+0.29%(83%)** / 20-100x **−0.30%(40%)** / ≥100x +0.24%(63%)——U 型,中桶最差;
     2. **浅盘口 <20x 在 +5m 反而最好**(+0.29% > ≥100x 的 +0.24%)→ +5m 的"正"不是深度梯度驱动;
     3. **实盘成交仓里 deep_book PF 0.53 < 非 deep 0.71**(带 depth 字段实盘仓 n=269;deep≥100x 151 仓净 −3.35U,非 deep 118 仓净 −1.44U);
     4. **deep_book 实盘硬止损率更高**(42% vs 37%);
     5. **反事实(deep 更好)与实盘 outcome(deep 更差)直接相反**;
     6. **当前反事实极可能是假阳性**(单一短窗口市场漂移 + 真实成交侧 shadow≈0,无法证伪),**不能上线**。
   - **后续追踪(仅观察项,不作上线候选)**:继续在报告里追 deep_book 的①反事实表现 ②实盘成交表现 ③分 venue ④分 market regime ⑤分不同时间窗口;**重点解释"为什么反事实和实盘相反"**(强假设:反事实把 rejected 候选当成已成交,未计真实下单冲击/滑点/逆选择,导致虚高)。
   - **当前不改任何实盘规则**:不加 depth 过滤、不改 universe、不改 taker、不上 scratch、不改参数。
   - **下一步重点 ≠ deep_book 上线**,而是继续寻找"真正能在实盘 outcome 中改善 PF"的字段或组合。
5. **BTC/market regime 降档 —— ✅ 已上线灰度(6-30 09:30,downsize 7.5→5U)**——
   - **依据(post_50U_window/历史 env,只读派生回测,n=298)**:按命中原因单笔毒性(名义归一收益):`transition_band`(btc 0.1-0.2)n87 PF0.31/硬止损48%/−0.472%;`extreme_trend`(btc≥0.4)n17 PF0.18/硬止损53%/−0.597%(⚠<30);`broad_up` n9 PF0.27/硬止损56%/−0.599%(⚠极少)。**区外 n185 PF0.84/−0.086%(干净)**。**btc_trend 是 W 形:0-0.1 好、0.1-0.2 差、0.2-0.4 好、≥0.4 差** → 故离散 OR 只罩 0.1-0.2 与 ≥0.4,**不罩 0.2-0.4(健康趋势)与 mixed(单笔毒性低,罩了≈全局缩仓)**。
   - **已上线规则**:命中坏环境只把**新开仓名义 7.5U→5U**;**只影响新开仓**,在场仓/平仓/信号/cross_venue/cost_floor/stop/TP/stale/cooldown/universe/taker **全不动**。命中=离散 OR,各记 `regime_degrade_reason`(transition_band/extreme_trend/broad_up)。**固定优先级:transition_band > extreme_trend > broad_up**(transition 与 extreme 是互斥 btc 区间;唯一重叠 = btc≥0.4 且 broad_up → 记 `extreme_trend`)。按 reason 对照务必用此口径。
   - **字段/记录**:`regime_degrade_hit` + `regime_degrade_reason` + `regime_degrade_notional_usdt` 已写入 `open_decision_events` / `position.json` / `settlements.jsonl`。
   - **后续验证门槛(实盘 recorded reason,不要 hit-vs-non-hit 一锅端)**:**每个 reason 单独**统计 n / PF / hard_stop / 名义归一收益;**特别盯 extreme_trend(n=17 仍灰度,未最终验证)实盘是否继续毒**。脚本 `records/_regime_degrade_backtest.py`【第4节】已按 reason 分桶,随样本累积。
   - **回滚**:`regime_degrade_btc_extreme:0`(关 extreme 段)或 `regime_degrade_enabled:false`(全关)+重启;在场仓不受影响。
   - **附:same_side 拥挤**:≥3 PF0.33/硬止损44%(**n=18<30 不足**);mixed(PF0.57,亏损额大但单笔毒性低)——**均不纳入降档**(继续采样)。
6. **time×venue 降档**——依据:**当前版本当前资金档**某时段某所 PF 差(**禁用旧版 A 数据**)。门槛:该(phase,cap,session,venue)桶≥30。动作=降档。回滚:开关。
7. **symbol/venue 自动降档**——依据:滚动仓位级 PF/连亏/硬止损率。**不停机,只调小/调严/延 cooldown。** 字段:实时仓位级统计反哺开仓(目前 stats 仅展示,未反哺)。门槛:滚动窗≥20-30。回滚:档位归位。
8. **不可交易币动态预过滤(低风险工程优化;非 edge 过滤)**——
   - **性质**:这是**工程预过滤,不是 edge 过滤**;目标=减少无效评估 + 不再让"永远开不出"的币占 shortlist 坑,**不改变任何可交易标的的交易规则/参数/edge**。
   - **依据(漏斗归因 D)**:`量化后不可交易` 共 203 次,高度集中:**LAB-USDT 160、KORU-USDT 29、SKHYNIX 10、VELVET 4** —— 这些币在 7.5U 名义下量化后不可交易,却反复入选 shortlist、每轮空跑评估。
   - **设计(动态,禁止写死黑名单)**:基于 `per_position_notional_usdt` × 当前 mid × instrument 的 `step/minQty/minNotional/tickSize` 计算"量化后是否可交易"(复用 `normalizeVenueQty`/`openLegTradableAfterQuantize`);不可交易则在 `scanOKXUniverse`(选币)或 `openFromShortlist`(候选环节)**剔除/降权**。币随名义/价格变化可恢复,故必须动态判定、**不得硬编码 LAB/KORU**。
   - **字段**:instrument 元数据(`lookupVenueInstrumentForMarket` 已有)+ `per_position_notional_usdt`。
   - **样本门槛**:无需 outcome 样本(纯执行可行性,确定性逻辑);但仍**先离线/只读核验命中集**(确认只剔真不可交易、不误剔可交易币)再上线。
   - **灰度/回滚**:`tradability_prefilter_enabled` 默认 false;开关关=行为不变。回滚=置 false。
   - **当前**:仅列候选,**不实现、不改 universe**(按纪律)。

> §11 其余项进度(6-28 21:01):A 信号供给不足=主因,解法在未来 fast layer(只观察);B cross_venue/cost_floor 暂不动(需 outcome 数据);C universe 可交易性优先=继续观察;E 资金非瓶颈,不再加钱。

## 12. 阶段2B 实现骨架(规划;不实现、不部署)
统一模式(所有规则共用,确保 record-only→灰度→回滚安全):
- **加规则位置**:入场过滤→`openFromShortlist`(在 micro 已建好之后、`openPosition` 之前);出场/刹车→`decideDirectionalPositionAction`(纯函数,可单测)或 `manageRiskFast`(20s,刹车类);降档→新 `degradeTier` 状态由 `runOnce` 每轮按滚动 PF 重算,`openFromShortlist`/`openPosition` 读它调 minConv/并发/taker/名义。
- **config 字段**:每规则一个 `*_enabled bool`(`core/config.go`+`defaults.go`+`config.yaml`),**默认 false**;+ 阈值字段;+ `*_venue` 灰度串(空=所有所,复用 OpenMicro 灰度模式)。
- **不影响现有交易**:开关 false 时代码不进任何分支(参照 `OpenMicroRecordEnabled` 写法);Fetch 失败留空不阻断。
- **测试**:纯函数规则加单测(参照 `decideDirectionalPositionAction` 测试);`go build ./... && go vet && go test -run 'Directional|OKX|Universe|Stats'` 全绿;先单所灰度→观察仓位级 PF→再全开。
- **回滚**:开关置 false + 重启(秒级);或 `git revert`。
- 优先序:taker 限制 > MAE 刹车 > runup 防追 > spread/depth 过滤 > 环境降档 > 自动降档。

## 13. 下一任 agent 第一件事(照做)
1. 跑 `python3 records/_directional_feedback_audit.py`(只读;读全部 directional 结算 + open_decision_events)。
2. 看 §1 样本量、§7 时间维度、第10节阶段切分、第8/9 节 env 分桶——**确认 phase + capital_regime 都分开了**。
3. 查 **D_v2 settlement env 是否≥30、50U 平仓是否≥30**(§10 门槛)。**不够就继续采样,什么实盘规则都不要改。**
4. 已就绪但**仅记录**的功能(都不改交易):time-slice MFE(mfe@30/60/180s)、OpenMicro v1(spread/depth/runup/maker轮数)、OpenMicro v2 env(BTC/funding/波动/拥挤/广度)、`open_decision_events.jsonl`(含 env,治幸存者偏差)、`shadow_fast_signal_events.jsonl`(影子快信号,record-only)、cooldown 持久化、仓位级 stats。
   - 只读研究脚本(需 `SHADOW_FWD=1` + 外网):`records/_deep_book_probe.py`(deep_book 8 维专项:venue/phase/成交侧/horizon/depth单调性/成本敏感/实盘对齐;结论见 §11.4)。
5. **当前禁止**:上 scratch、关 taker、改 universe、**加 depth/deep_book 过滤(专项验证已否决,见 §11.4)**、开 avoid_btc_chase、改参数、动资金、据 night 全历史降档、混 A/B/C/D、据 <30 settlement env 给规则、据单一行情窗口的反事实下结论、git commit(除非用户明确要求)。
6. 账户风控空转(§5 🔴)仍未修,优先级高,但属账户级、不影响方向开仓。
