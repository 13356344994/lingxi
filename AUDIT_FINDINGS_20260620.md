# lingxi 全流程审核问题清单（2026-06-20）

> 汇总本轮所有审核员发现的问题,按严重度排列。覆盖:初始深度审计(风控收敛、状态恢复)、5 路代码审计(EV 完整性 / 开仓执行 / 管理平仓 / 资金恢复 / 24h 日志)、重启后 4 个结果监控、5 个"处理得当性"审判(选币 / EV·决策 / 资金路由 / 开仓 / 持仓平仓)。
>
> 状态图例:✅ 本次已修复(commit `1d42eba`) ｜ 🔧 待你决策 ｜ ⚠️ 未修复 ｜ 📝 说明/观测 ｜ 🔬 已实盘验证
>
> 严重度:**P0** 阻断核心功能/危及本金 ｜ **P1** 影响项目上限 ｜ **P2** 真实但可控 ｜ **P3** 低/观测/运维

## 严重度统计(截至第二批修复后,2026-06-20)

| 严重度 | 总数 | ✅已修 | ◐部分 | ⚠️未修 | 📝运维/说明 |
|---|---|---|---|---|---|
| P0 | 2 | 2 | 0 | 0 | 0 |
| P1 | 9 | 9 | 0 | 0 | 0 |
| P2 | 16 | 11 | 0 | 5 | 0 |
| P3 | 5 | 0 | 0 | 2 | 3 |

## 修复状态总览(第二批,commit 待提交)

> 用户 2026-06-20 决策:松全部旋钮、启用 auto-flatten、加 carry 硬止损、SWITCH 延至结算、粗筛改复合分。下列为第二批修改者落地结果,全包 `go build/vet/test` 通过。

| ID | 状态 | 说明 |
|---|---|---|
| **P0-1** | ✅已修 | capture 0.5→1.0、dust 0.05→0.01、MinOpenNotional 20→5(按决策全松) |
| **P0-2** | ✅已修 | carry 加基差/距强平硬止损(基差走阔>2% 或距强平<15%,不受 funding defer) |
| P1-5 | ✅已修 | 管线侧(P2-16)+ bybit 适配器:spot 拉取失败传播、FetchBookTicker 按 market 选 spot/linear、QtyStep→BasePrecision step 回退 |
| P1-6 | ✅已修 | 元数据无法验证对齐时 reject(不再回退原始 qty) |
| P1-7 | ✅已修 | carry reconcile 加未登记孤腿扫描(默认 flatten/halt+告警) |
| P1-8 | ✅已修 | config 启用 auto-flatten + 平仓路径加固(flatten 错误不再被吞);**需重启生效** |
| P1-9 | ✅已修 | 粗筛改 funding×流动性复合分(无量数据回退纯 funding 上限放宽至 250) |
| P2-3 | ✅已修 | router 对 split-unified(gate)做单钱包校验/划转 + gate 适配器已实现 `TransferWallet`(spot↔futures),实盘划转可执行 |
| P2-4 | ✅已修 | SWITCH min-hold 延至结算 |
| P2-5 | ✅已修 | NextFundingTime 缺失时先 sync,补不到则保守 defer |
| P2-6 | ✅已修 | perp-perp dust 改按 notional 比例(0.125U→0.025U@5U) |
| P2-10 | ✅已修 | 路由前/每笔开仓前复查 halt |
| P2-13 | ✅已修 | switch_intents per-path 锁(真 lost-update bug,-race 通过) |
| P2-14 | ✅已修 | 不可借/无效 instrument 写观测候选(would_execute=false,可审计但不可执行) |
| P2-15 | ✅已修 | 连续≥5 轮 cvsp=0&&cvps=0 打 MAJOR 告警(节流) |
| P2-16 | ✅已修 | FetchInstruments 失败打 error 日志 + 区分"真无现货"vs"拉取失败" |
| P2-7 | ⚠️未修 | 未知首腿成交禁止自动回滚(保守取舍,暂留) |
| P2-8 | ⚠️未修 | 跨所缺口不自动划转(刻意保守) |
| P2-9 | ⚠️未修 | 钱包拆分按金额排序(低危,主要影响展示) |
| P2-11 | ⚠️未修 | flatten_all 失败排查(待复现诊断) |
| P2-12 | ⚠️未修 | watcher 60s 延迟(可降间隔/挂实时流) |
| P3-3 | ⚠️未修 | requires_rebalance 3bps / RebalanceLatencyHours(低优,跳过) |
| P3-4 | ⚠️未修 | dynamic_sizing 未接入 |
| P3-1/2/5 | 📝 | 日志轮转/dust 清理(用户决定不管)/候选表象说明 |

**venue 适配器 follow-up:✅ 已完成(2026-06-20 第三批)**
1. ✅ **bybit 适配器**(P1-5):spot FetchInstruments 失败传播、FetchBookTicker 按 market 选 category、QtyStep→BasePrecision step 回退 → 解锁 cvsp/cvps。
2. ✅ **gate 适配器**(P2-3):已实现 `TransferWallet`(spot↔futures),split-unified 路由规划的 gate 所内划转实盘可执行。

> 注:bybit `FetchDepth` / `FetchRecentQuoteVolume` 仍固定 linear 类目——若用于 spot 深度可能有同类问题,本批未覆盖(低优,可另开)。

---

## P0 — 严重(阻断核心功能 / 危及本金)

### P0-1 🔧 成本模型相对"+EV就进"意图过严,系统几乎永不开仓
- **来源**:EV/决策得当性审判
- **现象**:每轮 ~450 候选,`ExpectedNetEVUSDT>0` 恒为 0,`actions=0`;系统按设计"宁可不开",与用户意图"只要预期净赚>0(哪怕 +0.0000001)就开"冲突。
- **根因**:`capture=0.5` + `dust=max(minNotional)×0.05` + `MinOpenNotionalUSDT=20` 三者叠加,把边际正 EV 机会(如 BEL 差 -0.004U)压成负、或在 rotation 层二次拒。
- **证据**:BEL-USDT 诚实 EV=-0.0038;若 capture=1.0 且去 dust → ≈+0.049(转正)。
- **决策点(待你拍板)**:松旋钮可贴合意图但在执行链未实盘验证下放进更多边际仓。建议方向:`capture 0.5→1.0`、`dust 0.05→0.01`、`MinOpenNotional 20→5`。**但应在执行链有过至少一笔干净实盘验证后再松。**

### P0-2 ⚠️ carry 两腿缺价格/基差硬止损
- **来源**:持仓/平仓得当性审判
- **现象**:carry 执行器 `shouldClosePosition` 只有 MaxHold(默认 72h)与 EV转弱退出,**没有**价差/基差硬止损(只有 perp-perp 有)。
- **风险**:对冲仓在基差极端走阔/单腿被强平场景下,可一直扛到 72h,期间 MTM 亏损无价格触发退出。市场中性下风险有限,但属真实盲区。
- **建议**:为 carry 增加基差/距强平距离的硬止损闸(对齐 perp-perp 的 `shouldCloseCrossVenue` 思路)。

---

## P1 — 高(影响项目上限)

### P1-1 ✅🔬 funding 捕获率假设 100%(EV 收入端系统性虚高)
- **来源**:EV 完整性审计 → 已修
- **修复**:5 线统一乘 `FundingCaptureProbability`(默认 0.5)。**注**:0.5 本身被 P0-1 判为偏严,待调。

### P1-2 ✅ carry 开仓两腿张/币口径不归一(HEI/dust 根因)
- **来源**:开仓执行审计 → 已修
- **修复**:开仓两腿走 `crossVenueLegExecutedCoinQty` 归一 + `carryMatchTwoLegOpensNormalized`;记账取 `min(两腿)`。这是 110 次 BEL dust_orphan 的根因。

### P1-3 ✅ 平仓在 funding 结算前触发(零 funding 捕获根因)
- **来源**:管理/平仓审计 → 已修
- **修复**:dust_orphan / EV转弱平仓加 `now>=NextFundingTime` 闸 + 结算后 sync 确认;真裸腿/硬风险仍立即平。

### P1-4 ✅🔬 不可借币与深度筛选噪音(选仓漏斗坍塌)
- **来源**:24h 日志 / 选仓漏斗监控 → 已修并实盘验证
- **修复**:不可借币筛选阶段排除(`not_borrowable` 候选 359→0);深度拉取覆盖 50→150(`insufficient_depth` 5.4%→0%)。

### P1-5 ⚠️ 跨所 cvsp/cvps 候选恒为 0(bybit 现货 instrument tick/step=0)
- **来源**:开仓链监控 / 选仓漏斗监控
- **现象**:每轮 ~386 条 bybit 现货 `instrument(tick/step=0)` 被预过滤 → 跨所 spot-perp 两条线基本开不出仓。
- **建议**:排查 bybit 现货 instrument 元数据刷新链路(delist-guard 已加载 1255 条但仍 tick/step=0);修好可解锁跨所两条线。

### P1-6 ⚠️ 开仓预检"元数据缺失回退原始 qty"可能放过不对齐开仓
- **来源**:开仓得当性审判
- **现象**:`carryPreflightSpotPerpOpenAlign` 在 `lookupLegInstrument` 失败且候选腿无 ContractSize 时,回退原始 qty 继续开仓 → 仍可能触发 6/19 式 rollback。
- **建议**:该回退路径改为 **reject**,或对 `ContractBasedPerp` 一律 reject(扫描器 opportunity leg 不含 ContractSize,是结构性来源)。

### P1-7 ⚠️ carry 启动恢复不扫描"交易所有腿但台账无登记"的孤腿
- **来源**:状态恢复审计
- **现象**:carry 策略 `reconcileOnStart` 只处理台账登记的持仓;交易所侧单腿裸仓(无台账)**不会被发现/自动平**(perp-perp 有未登记扫描,carry 没有)。
- **建议**:为 carry 加未登记腿扫描(import/flatten/halt),对齐 perp-perp。

### P1-9 ⚠️ 粗筛排序纯按 funding,池子偏置 illiquid 小币、无主流币
- **来源**:选币得当性审判
- **现象**:`rotationScanFilterSymbols` 只按各 symbol 跨所 `max|rate/h|` 取 top150,**不并入流动性/可交易性**(深度二次拉取在粗筛之后,改变不了"谁进池")。实盘 top 候选是 ESPORTS/BTW/SIREN/XPT/NOKIA 等高波动小币,**BTC/ETH/SOL/BNB/XRP 全部不在池内**。
- **后果**:算力优先喂给"极端 funding + 低流动/元数据残缺"的币,而非"可落地的 carry";与逆向选择同源(本次只把深度覆盖提到 150,**未改排序口径**)。
- **建议**:粗筛改"funding × 可成交深度"复合分,或双桶(高流动 + 高 funding)。

### P1-8 ⚠️ 账户风控是"监控+停新开",非"预检+自动平"
- **来源**:风控收敛审计
- **现象**:`auto_flatten_on_hard_drawdown_enabled=false` → 击穿回撤阈值只停新开仓、不自动平仓;实时 MTM 恐慌强平挂在已退役引擎上,生产轮动路径不享受;`max_strategy_cumulative_loss`/`max_account_open_notional` 仅 60s watcher 查,非开仓前预检。
- **说明**:对 435U 小资金部分属刻意取舍,但属真实上限制约。

---

## P2 — 中(真实但可控)

### P2-1 ✅ 可用余额只取期货钱包 → 假"venue_balance_insufficient"
- **来源**:资金/恢复审计 → 已修(改为现货+合约+杠杆合计 + 出错 fail-closed)

### P2-2 ✅ 取余额出错时静默当 0
- **来源**:资金审计 → 已修(记日志 + fail-closed,不写 0)

### P2-3 ⚠️ unified 所(gate)合计余额口径钱包级假阳性 + 三层口径不一致
- **来源**:资金路由得当性审判
- **现象**:gate 标 `UnifiedWallet=true` 但物理仍分 spot/futures;capital router 对 unified 所**只校验"合计≥需求"、不查单钱包、不规划所内划转** → "合计够但 spot/futures 单池不够"假阳性。binance 分仓所有所内划转仿真+失败阻断,风险显著更低。
- **关键**:allocator/router(合计口径) 与 executor preflight(真钱包口径) **三层不一致**;preflight 能拦住真钱误开,但日志落"跳过开仓"而非"无法备资",运维易误判为"路由过了却开不出"。
- **建议**:gate 也走分仓校验+所内划转,或路由阶段调用与 preflight 同口径的真钱包读取。

### P2-4 ⚠️ SWITCH min-hold(固定 1h)与软平 min-hold(延至结算)不一致
- **来源**:持仓/平仓得当性审判
- **现象**:开仓后 1h~结算点之间,轮动 SWITCH 可换出旧仓(不受 funding defer),可能在结算前丢 funding。
- **建议**:SWITCH 也对临近结算的持仓延至结算后,或显式接受此取舍。

### P2-5 ⚠️ NextFundingTime 缺失时平仓闸 fail-open
- **来源**:持仓/平仓得当性审判
- **现象**:`nextFunding.IsZero()` 时 defer 关闭,dust/EV 会立即平;若开仓时未 sync funding meta,新闸失效退回旧行为。
- **建议**:缺失时主动 `syncFundingMeta` 补全,或保守 defer。

### P2-6 ⚠️ perp-perp 固定 dust/policy/observable 成本(~0.13U 绝对量)相对薄仓过大
- **来源**:EV/决策得当性审判
- **现象**:4–17U 薄仓的 spread 收入(几分钱)远低于固定 ~0.13U 缓冲 → 结构性负 EV。
- **建议**:改为按 notional 比例,而非绝对量。

### P2-7 ⚠️ 未知首腿成交状态 → 持久化但禁止自动回滚(残留裸腿至下轮)
- **来源**:开仓执行审计
- **现象**:查单未知时 persist `opening_*` 并禁止自动回滚,等下一轮 reconcile/人工。设计上保守,但残留敞口窗口真实存在。

### P2-8 ⚠️ 跨所资金缺口永不自动补 → 跨所机会长期 unfundable
- **来源**:资金/恢复审计
- **现象**:需跨所搬钱的机会一律标 unfundable、只日志不划转。
- **说明**:本次刻意不做自动跨所划转(资金安全风险高);已加 `cross_venue_funding_gap` 可统计标记。

### P2-9 ⚠️ 钱包拆分推断按容量排序,可能错配 spot/perp 钱包归属
- **来源**:资金/恢复审计
- **现象**:`rotationAssignCrossVenueWalletSplit` 按金额排序推断钱包归属,而非按腿所属 venue。

### P2-10 ⚠️ in-flight 扫描绕过 halt(最长一轮扫描时长)
- **来源**:资金/恢复审计
- **现象**:`runCycle` 只在派发前查一次 halt,异步 `runOnce` 中途不复查;`live` 在 dispatch 时一次性捕获。
- **建议**:`ExecuteCapitalRouting` 与每笔 OPEN 前复查 `IsGlobalHaltActive`。

### P2-11 ⚠️ flatten_all 多次失败(closed=0)
- **来源**:24h 日志取证
- **现象**:6/18–6/19 `flatten_all` 失败 4 次(closed=0)。
- **建议**:排查全平失败根因(可能与 dust/残腿/已平幂等有关)。

### P2-12 ⚠️ 账户风控 60s 延迟 + 同 60s 轮动 tick,击穿到停新开有窗口
- **来源**:风控收敛 / 资金恢复审计
- **建议**:halt-only 路径可降低 watcher 间隔,或挂实时权益流。

### P2-13 ⚠️ switch_intents.json / 磁盘-内存状态非原子耦合
- **来源**:状态恢复审计
- **现象**:`switch_intents.json` 无锁;`persistPosition` 先写盘再更内存(分锁),并发 manage/open 对同一仓 upsert 可能 last-writer-wins。

### P2-14 ⚠️ 审计断层:不可借/无效 instrument 静默丢弃,不写候选日志
- **来源**:选币得当性审判
- **现象**:P1④ 改动后不可借币 `return false`(不再写 `not_borrowable` 到 `unified_candidates.jsonl`);instrument `tick/step=0` 仅打"跳过"日志。结果:大量"该知道的拒绝"不进候选日志 → leaderboard/复盘看不到"因不可借/无效元数据丢了谁"(本次为开仓清噪付出的可审计性代价)。
- **建议**:恢复显式 reject 记录(或单独写 `scan_observation`/`invalid_instrument` reason),与 `no_spot_market` 同级可审计。

### P2-15 ⚠️ cvsp/cvps 连续多轮 0 产出,无告警/无降级
- **来源**:选币得当性审判
- **现象**:自 17:23 起 `cvsp=0 cvps=0` 持续多轮,系统无 major 告警、无按 venue 聚合的 skip 统计 → 两条策略线"静默死亡"。
- **建议**:连续 N 轮 `cvsp=0 && cvps=0` 触发告警 + 按 venue 聚合 skip 计数。

### P2-16 ⚠️ bybit FetchInstruments 失败时静默返回空 map(不记 error)
- **来源**:选币得当性审判
- **现象**:`unified_market_pipeline.go` 中 `FetchInstruments` 出错直接 `return out`(空 map),不打 error 日志 → 无法区分"真无现货"与"元数据拉取失败";疑为 bybit 现货 `tick/step=0` 海量(全日志 ~12004 条)的根因之一。
- **建议**:失败打 error 日志并区分两种情况(与 P1-5 同根)。

---

## P3 — 低 / 观测 / 运维

### P3-1 📝 日志重启时被截断,丢失 ~22h(无轮转)
- **来源**:24h 日志取证 — 用户已决定不管(新服务跑几次自然积累)

### P3-2 📝 dust traps(PEPE/BNB/FLUX)卡在钱包占用资金
- **来源**:24h 日志取证 — 用户已决定不管

### P3-3 ⚠️ EV 中 `requires_rebalance` 仅 3bps 软惩罚,`RebalanceLatencyHours` 未用
- **来源**:EV 完整性审计

### P3-4 ⚠️ `dynamic_sizing.go` 未接入(代码内 TODO)
- **来源**:风控收敛审计

### P3-5 📝 ~450 候选表象具误导性(多为 landscape 诊断行,非完整 EV 候选)
- **来源**:EV/决策得当性审判 — 观测说明,非缺陷

---

## 附:本次已修复清单(commit `1d42eba`)

1. 开仓择优改按收益率(`unifiedWindowScore`)+ 最小仓位下限
2. 开仓失败 (策略,symbol,所对) 冷却
3. 深度拉取覆盖 50→150
4. 不可借币筛选阶段排除(负 carry)
5. carry 开仓两腿张/币归一 + 可对齐预检 + 记账取 min
6. 单所 SVSP/SVPS 联合步长 + 最小名义预检
7. dust_orphan/EV转弱平仓加 NextFundingTime 闸;SWITCH 加固定 min-hold
8. EV 口径诚实化(capture 折现 + dust 成本 + 基差只罚不利侧 + perp-perp 离散结算/taker)
9. 余额改现货+合约+杠杆合计,出错 fail-closed
10. enable_rolling_hold no-op 告警

## 附:旋钮(P0-1)—— 用户已决策"全松",已落地

| 旋钮 | 原值 | 现值(已改) |
|---|---|---|
| FundingCaptureProbability | 0.5 | **1.0** ✅ |
| carryScannerDustResidualFraction | 0.05 | **0.01** ✅ |
| MinOpenNotionalUSDT | 20 | **5** ✅ |

> ⚠️ **风险提示(保留)**:这三个旋钮已按用户决策松开,会显著增加开仓数。但执行链仍**未经一笔干净实盘验证**(尚无 `realized_funding>0`)。建议部署后**密切监控首批真实开仓**的两腿对齐/dust/rollback,一旦异常立即收紧。

## 附:仍未实盘验证项(需 actions>0 + 穿越 funding 结算点)

- 开仓两腿对齐是否真消除 dust/rollback
- NextFundingTime 闸是否真让仓持有穿越结算点
- 收益率择优是否真按预期选高收益率/小占用
- **核心目标 `realized_funding>0` 是否出现**

---

*更新记录:已并入全部审核员报告。**第二批修复(2026-06-20)已落地并标注(见顶部"修复状态总览"):P0/P1 全部 ✅ 或 ◐,P2 大部分 ✅,全包 go build/vet/test 通过。** 剩余 P2-7/8/9/11/12、P3-3/4 及两条 venue 适配器 follow-up(bybit / gate TransferWallet)未覆盖。*
