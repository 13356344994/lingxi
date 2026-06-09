# lingxi 实盘修复方案（基于代码 + 日志实证）

> 本文件**不依赖任何既有 md 自评**，所有结论来自对 `internal/**` 代码与 `records/**` 运行日志的逐条核对。
> 核对时间：2026-06-09。核对的代码版本：`aff14b5`（最新 HEAD）。
>
> **当前运行姿态（你确认的意图，本方案的总前提）：**
> 1. **两个平台都要**（Binance + Bybit）。
> 2. **正、负资金费率都吃**。
> 3. **暂不做手续费 / 借贷费 / 滑点的经济闸** —— 只要**费率方向正确就进去吃**。
> 4. 真实成本（手续费含 **BNB 折扣**、借币利息、滑点、实收 funding）**靠实盘日志事后复盘**，不在进场前估算挡单。
>
> 所以本方案的目标**不是提高盈利闸**，而是：**让任何方向正确的机会都能"干净地开仓 → 平仓 → 结算"，把每一分真实成本与收益落进可复盘的日志**。任何"为了显得安全而抬高入场闸"的改动都不做。

---

## 0. 日志铁证：系统当前的真实状态

| 指标 | 实测值 | 来源 |
|---|---|---|
| Binance 完整结算周期 | **0 次** | `records/settlements.json` = `[]`、`settlements_journal.jsonl` = 0 行 |
| `-3045`（借币失败） | 历史 **82 次**（集中在旧版本 `f6dc151`，6-07） | `lingxi_server.log` / `execution_incidents.jsonl` |
| `abort_after_adverse_selection` | **82 次** | `lingxi_server.log` |
| 平台熔断触发 | **44 次** | `lingxi_server.log` |
| bookTicker `已失效`（平仓/救援路径） | **17 次** | `lingxi_server.log` |
| 进程启动 / fatal 崩溃 | **31 次启动 / 2 次崩溃** | `lingxi_server.log` |
| 卡死残腿救援任务 | **2 个：ENAUSDT、TAOUSDT**，`Attempts=5`，`KnownSpot/FuturesExecutedQty=0` | `live_exposure_rescue_tasks.json` |
| Bybit 持仓 | 1 个 **MBOX-USDT** 滚动持有，已收 4 次资金费（下期 0.0013%） | `bybit/bybit_positions.json` |
| 当日实现盈亏 | **0**（无周期结算过） | `live_daily_risk_state.json` |

**一句话**：跑了 2.5 天、重启 31 次、崩溃 2 次，**Binance 一笔完整对冲套利都没成交过**；Bybit 挂着 1 个仓在滚动。

---

## 1. 根因链（日志现象 → 代码根因）

### ✅ 根因 1（已修，无需再做）：R29 借币预检 fail-open
- 历史现象：`R29 查询最大可借量失败(不据此剔除) → monitor->open → 合约腿成交 → 借币卖现货撞 -3045 → 回补合约`，每周期空烧手续费。
- **当前代码已修**：`internal/app/borrow_capacity.go:162-183` 的 `queryMaxBorrowable` 现在区分
  「确定性借不到（-3045/-3006/-3008）→ 剔除候选 + 写 allowlist 缓存」与「真·网络未知 → advisory 不剔除」。
  最新日志已见 `海选剔除借不到的负 carry 候选 FIDAUSDT ... 近期确定性借不到,TTL内跳过`。
- **结论**：此项**不再需要改**。仅需做 §3 的 P2-2（确认 allowlist TTL 不会长期误杀"后来又能借"的币）。

### 🔴 根因 2（活 bug）：救援机制自我绞杀（self-deadlock）
- 现象（`execution_incidents.jsonl:32-40`）：
  ```
  ExposureManager 处理失败(尝试 3/6): 平台级风控熔断触发: 单轮恢复动作过多 count=4
  ExposureManager 处理失败(尝试 4/6): ...熔断触发...
  ExposureManager 处理失败(尝试 5/6): ...熔断触发...
  ```
- 代码根因：
  - `exposure_manager.go:434` 每次救援 `exposure_rescue_start` 都调 `noteRecoveryAction`；
  - `risk_supervisor.go:226-227`：当 `recoveryActions > MaxRecoveryActionsPerCycle`(默认 **3**，`defaults.go:137`) 即熔断 `单轮恢复动作过多`；
  - 但 ExposureManager 的重试上限是 **6 次**（`第 N/6 次`）。
  - ⇒ **6 > 3**：第 4 次救援必然触发熔断，之后第 5/6 次救援又被这个熔断挡死 → 残腿永久卡队（ENA/TAO 就是这么卡住的）。
- 本质：救援是**最后一道防线**，却被"防开仓恢复死循环"的同一道熔断节流。**这是机制内部的逻辑矛盾。**

### 🔴 根因 3（活 bug）：平仓 / 救援路径**没有 bookTicker REST 兜底**
- 现象：`并发现货平仓失败且恢复空头失败: 现货 bookTicker 已失效 age=504ms / 合约 bookTicker 已失效 age=630ms`；
  `ExposureManager 买回现货残腿失败: 现货 bookTicker 已失效 age=4.645s`。
- 代码根因：REST 补鲜能力 `PreOpenTickerRESTRefresh` 只接在**开仓**路径
  （`execution_protection.go` / `directional_spot_execution.go` / `engine_settlement.go`），
  **`close_protocol.go` 与 `exposure_manager.go` 仍硬依赖 WS bookTicker**，薄盘币 WS 长时间不推 → 直接报"已失效"放弃。

### 🔴 根因 4（活 bug，承接代码评审）：单腿回退
- 代码：`realtime_drawdown.go:580-612` 单币止血 fallback **只平合约腿**（`placeSingleFuturesMarketOrderIdempotent`），
  现货 / 全仓杠杆腿被留下裸露。即"晴天双腿平、暴雨天（平仓失败回退）退化成单腿"。
- 关联：ENA/TAO 卡死时 `KnownSpot/FuturesExecutedQty=0` —— **系统自己都不知道有没有裸敞口**，对冲系统最危险的状态。

### 🟠 根因 5（功能缺口）：Bybit 不支持负 carry
- 代码：`internal/venue/bybit/account.go:182` 明确 `Capabilities.SupportsNegativeCarry=false`，`MaxBorrowable` 返回 `ErrUnsupported`；
  `bybit_executor` 默认 `MinFundingRate: 0.0005`（仅正费率，`defaults.go:282`）。
- ⇒ **"两个平台正负费率都吃"目前在 Bybit 侧无法成立**：Bybit 只能吃正 carry。这是实现你意图的硬前提缺口。

### 战略根因（不是 bug，但决定成败）：edge 与可执行性反相关
- 决策漏斗反复显示：能过海选的几乎全是 FIDA/HOME/TAO/ENA 这类**长尾负费率币**——费率肥，但**借不到 + 盘口薄宽**。
- 你的"只看方向就吃"会主动放进这些币 ⇒ **§2 的执行可靠性修复（根因 2/3/4）是这套姿态能不能跑通的前提**，否则会持续制造卡死残腿。

---

## 2. 修复方案（按优先级）

> 原则：只修"能不能干净开/平"，**不碰入场费率/成本闸**（你要只看方向）。

### P0 —— 止血 + 让"开了的单能平掉"（最高优先，直接服务采数据）

#### ✅ P0-1. 解开救援自锁（根因 2）—— 已完成（2026-06-09）
- **实现**：新增 `RiskSupervisor.NoteRescueAction`（`risk_supervisor.go`）+ 引擎包装 `noteRescueAction`（`engine_risk_util.go`），
  救援动作只做审计（`RecordException`）+ 发风险事件，**不累加** `recoveryActionsThisCycle`、**永不**触发/被平台熔断。
  `exposure_manager.go` 的 3 处救援调用（`handleTask` / `dynamicallyFlattenExposure` / `flattenSpotResidual`）全部切到
  `noteRescueAction`；开仓恢复路径（`engine_open_exec.go:620`）保持原 `noteRecoveryAction` 不变（仍熔断开仓恢复死循环）。
- **安全不丢失**：救援期间 `RecordException(severity=major)` 仍使 `healthStatusLocked` 转 `degraded`、停止扩张。
- **回归测试**：`TestNoteRescueActionNeverTripsFuse`（救援调用 3×limit+5 次不熔断/不计预算；开仓恢复超预算仍熔断）。
- **验收**：`go build ./...` / `go vet ./...` / `go test ./...` / `go test -race`（救援相关）全绿。

#### P0-1（原始描述存档）. 解开救援自锁（根因 2）
- **改点**：让 ExposureManager 的救援动作**不计入** `MaxRecoveryActionsPerCycle`，或给救援单独的预算 / 让它真正 bypass 平台熔断。
  - 具体候选改法（择一）：
    - (a) `exposure_manager.go` 救援路径调用一个**不累加 `recoveryActionsThisCycle`** 的记录函数（新增 `noteRescueAction`，只审计不计熔断）；
    - (b) `risk_supervisor.go:226` 的熔断判定排除 `action=="exposure_rescue_start"`；
    - (c) 把 ExposureManager 重试上限（6）与 `MaxRecoveryActionsPerCycle`（3）解耦，并保证救援上下文 `withExposureManagerRequest` 全程豁免平台熔断。
- **验收**：构造一次 abort→救援，日志中救援能跑满 6 次而**不出现** `单轮恢复动作过多`；卡死残腿能被救援收敛。

#### ✅ P0-2. 平仓 + 救援路径接入 bookTicker REST 兜底（根因 3）—— 已完成（2026-06-09）
- **实现**：新增 context 标记 `withFlattenIntent`/`isFlattenIntent` + 判定 `tickerStaleRESTRefreshAllowed`（平仓 OR 救援即允许，`execution_protection.go`）。
  `fetchSpotBookTicker`/`fetchFuturesBookTicker` 改为：缓存有票但已陈旧时，若 flatten/救援意图且**非 cache-only 模式**则 fall through 到 REST 现拉真·当前价；
  其它路径（开仓/监控）维持原「报失效」语义；cache-only（影子/安全期）仍硬挡 REST。
- **覆盖入口**：标记下沉到 CloseProtocol 三个 choke point（`executeSequence` / `closeHedgePosition` / `closeHedgePositionParallel`），
  覆盖结算平仓、滚动持有 drop 平仓、abort 平仓、panic-flatten；救援路径另由 `withExposureManagerRequest` 覆盖（已有）。
- **回归测试**：`TestFetchBookTickerStaleCacheFallsBackToRESTOnFlattenIntent`（普通 ctx 仍报失效；flatten/救援意图回退 REST 取到现价；cache-only 硬挡）。
- **验收**：`go build` / `go vet` / 相关测试 / `go test -race`（取价·平仓·救援路径）全绿。

#### P0-2（原始描述存档）. 平仓 + 救援路径接入 bookTicker REST 兜底（根因 3）
- **改点**：把开仓路径的 REST 补鲜能力接到 `close_protocol.go` 与 `exposure_manager.go` 取价处：
  WS bookTicker `age` 超阈时**回退 REST 现拉一次当前价**，而不是直接报"已失效"放弃。
- **验收**：`已失效` 导致的平仓/救援失败在日志中消失；薄盘币（TAO/ENA 类）也能平掉。

#### P0-3. 清理 2 个卡死残腿（ENAUSDT / TAOUSDT）
- **步骤**：
  1. `lingxi reconcile`（只读对账）+ 直接查币安账户，确认两个币**实际现货/合约持仓是否为 0**；
  2. 若确实无敞口 → **停机时**清空 `records/live_exposure_rescue_tasks.json` 的两条任务；
  3. 若有真实裸腿 → 用 `lingxi flatten` 应急平。
- **铁律**：在确认前**不要让引擎无人值守续跑**，否则它带着"未知敞口"接管真实仓位。

### P1 —— 修单腿回退（防真出事时裸奔）

#### ✅ P1-1. 单币止血 fallback 补现货腿（根因 4）—— 已完成（2026-06-09）
- **实现**：`realtime_drawdown.go` 抽出共享兜底 `convergeManagedSpotLegBestEffort`（负 carry BUY 买回+还币 / 正 carry·孤儿 SELL 现货钱包）
  + `resolveManagedSymbolSpotLeg`（台账权威 / 孤儿回退 `managedExposureTargets()` 推断 pair·base）。
  全局 panic 的现货收敛改调共享 helper（消灭两处逻辑漂移——R3 根因正是单币兜底与全局 panic 不一致）。
- **修掉两个 R3 漏洞**：① 单币止血兜底原先**只平合约腿、漏平现货/杠杆腿**；② 原先在**合约腿已被强平(qty≈0)时直接 return**，
  恰恰把裸露的现货/杠杆腿留下不管（ADL/单腿强平场景）。现兜底对合约腿(若有残余)与现货/杠杆腿(方向感知)都收敛。
- **回归测试**：`TestResolveManagedSymbolSpotLeg`（tracked/孤儿/未知三种定位）；既有 `TestChaosSpotLegFaultsNeverLeaveNakedFutures` 等仍全绿。
- **验收**：`go build` / `go vet` / 相关测试 / `go test -race`（止血·panic·exposure 路径）全绿，0 lint。

#### P1-1（原始描述存档）. 单币止血 fallback 补现货腿（根因 4）
- **改点**：`realtime_drawdown.go:580-612` 的 `symbol_cut` 兜底在平合约腿后，**对称平掉现货/全仓杠杆腿**（方向感知）。
- **验收**：构造"平仓主路径失败 → 单币切仓 fallback"，结束后双腿都归零，无残留现货/杠杆腿。

### P2 —— 实现"两平台正负都吃"（功能缺口，采数据所需）

#### ✅ P2-1. Bybit 接入负 carry（根因 5）—— 已完成（2026-06-09，opt-in 默认关闭）
- **实现**（默认关闭的 opt-in，开关 `bybit_executor.enable_negative_carry`，关闭时正 carry 路径逐字节不变）：
  - venue 层：`bybit/trading.go` 把中性 `OrderRequest.MarginEffect`(Borrow/Repay) 翻译成 Bybit `isLeverage=1`
    （UTA 现货杠杆**自动借/还**——卖出超持有量自动借入做空,买回自动还币）。
  - 选币：`selectBybitCandidates` 在开关开启时纳入负费率(`|费率| ≥ MinFundingRate`)候选,正/负按 |费率| 同台竞争降序。
  - 定量:`planBybitDeltaNeutralOpen` 毛资金费按 `名义×|费率|` 计(正/负 carry 都是收方)。
  - 执行(`bybit_exec_live.go`)方向感知:负 carry 开=**永续多 + 借币做空现货(MarginEffectBorrow)**;
    平=**永续卖出 reduceOnly + 现货买回还币(MarginEffectRepay,买回量=借入量)**;PnL 符号、日志、续持复核全部按方向区分。
  - 绝不裸腿:负 carry 现货借币卖出失败 → 立即回补永续多(卖出 reduceOnly),沿用既有兜底;借不到的币天然被回补、不留单腿。
  - 续持复核方向感知:`bybitFundingStillFavorable`(正 carry 费率>0 续持 / 负 carry 费率<0 续持)。
- **未翻 `Capabilities.SupportsNegativeCarry`**:Bybit bolt-on 执行器自带 `enable_negative_carry` 开关门控,
  不依赖通用引擎能力位;翻能力位会牵动 conformance/通用路径,留作单独可审计的后续。
- **回归测试**:`TestSelectBybitCandidatesNegativeCarryWhenEnabled`、`TestPlanDeltaNeutralOpenNegativeUsesAbsFundingAndSetsDirection`、
  `TestOpenDeltaNeutralNegativeLongsPerpAndBorrowShortsSpot`、`TestOpenDeltaNeutralNegativeSpotFailRevertsPerpLong`、
  `TestCloseDeltaNeutralNegativeShortsPerpAndRepaysSpotBuy`。
- **验收**:`go build` / `go vet` / 全包 `go test` 全绿。
- **⚠️ 实盘前你必须做**:① 确认 Bybit 统一账户已开通现货杠杆借币、目标币在可借池;② **先 testnet / 极小额**验证「借币空现货能成交 + 买回能还币」,
  确认无误后再在 `config.yaml` 设 `venues.bybit.bybit_executor.enable_negative_carry: true`。**这条路径未经我对真实 API 验证**(你已知悉并接受)。

#### ✅ P2-2. Binance 借币 allowlist 缓存防误杀 —— 经核对【已实现,无需改】
- 反应式缓存:`borrow_availability_cache.go:23` `borrowUnavailableCacheTTL = 20min` 有界 TTL,过期自动删除重探。
- 主动白名单:`borrow_allowlist_cache.go:23` `borrowableAllowlistTTL = 6h` 有界 TTL,过期重拉。
- 两者都不会"永久误杀后来可借的币"。原计划对它的担心是多余的。

### P3 —— 配置姿态对齐"只看方向、不看成本"（登记现状，确认无遗漏挡单）

当前 `config.yaml` 已是数据采集姿态，**登记备查**（确认没有别的闸在挡单）：

| 平台 | 关键项 | 当前值 | 说明 |
|---|---|---|---|
| Binance | `fee_reserve_rate` | 1e-5 | 逼近 0（硬约束 >0） |
| Binance | `exit_spread_reserve_rate` | 0 | |
| Binance | `min_net_profit_rate` | 1e-4 | 硬约束 >0，无法到 0 |
| Binance | `impact_reserve_in_gate_enabled` | false | 不扣冲击 |
| Binance | `min_estimated_funding_rate` | 1e-4 | 正负通用地板 |
| Binance | `max_scan_basis_rate` / `favorable_basis_max_rate` | 0.10 | 放宽基差门 |
| Binance | `borrow_capacity_check_enabled` | true | 借不到的负 carry 在选币阶段干净跳过 |
| Binance | `borrow_unavailable_non_fatal_enabled` | true | 借不到不熔断 |
| Binance | `enable_rolling_hold` | true | 跨周期滚动持有 |
| Bybit | `min_funding_rate` / `min_net_profit_rate` | 0.0002 / **-0.005** | 负门=主动接受手续费亏损采数据 |
| Bybit | `lead_seconds` / `max_hold_settlements` | 1800 / 12 | |

- **注意**：Binance 有写死硬约束 `MinNetProfitRate>0` + `FeeReserveRate>0`，**无法**像 Bybit 设负门。
  这意味着 Binance 实际有效地板 ≈ `资金费 > 实测两腿价差 + 1e-5 + 1e-4`。这是硬约束下的最低，已是"只看方向"的极限。
  - 若要在 Binance 也做到"纯方向、零成本闸"，需改 `internal/core/config.go` 的 `Validate()` 放开这两条硬约束下限——
    **属于改核心校验，谨慎；建议先用当前 1e-4 地板跑，看日志再决定是否动**。

### ✅ P4 —— 观测增强（采数据的核心价值，确保"每分钱可复盘"）—— 已完成（2026-06-09）
- **核对结论（代码实证，非文档自评）**：Binance 侧主成本分项**已是真实口径**：
  - **手续费**：`estimateSpotCommissionUSDT` / `estimateFuturesExecutionFeeUSDT` / `convertCommissionToUSDT`（`engine_fees_market.go`）读
    **真实成交回报的 `commission`+`commissionAsset`**，合约还查 `/fapi/v1/userTrades` 拿逐笔真实佣金；**BNB 付费时按真实 BNB 数量折 USDT**——
    完全覆盖你的 BNB 折扣场景。
  - **资金费**（真实 `/fapi/v1/income`）、**两腿 TradingPnL**（真实成交价）、**滑点**（相对参考价，诊断量）均真实/已落盘。
  - 每周期每标的有结构化可 grep 的 `TRADE_DETAIL` 行（funding/spot_fee/fut_fee/borrow/open_slip/close_slip/recovery_friction/net_pnl），
    落盘 `settlements.json` + append-only `settlements_journal.jsonl`。
- **本轮补的缺口（借贷费真实值）**：原借币成本是「交易所**公布利率** × 持仓小时（向上取整）」的估算（利率真实、非实际扣息）。
  新增 `borrow_interest_observation.go`：负 carry 平仓后以 **goroutine 旁路**拉 `/sapi/v1/margin/interestHistory` 真实扣息，
  折 USDT 后打 `BORROW_INTEREST_ACTUAL`（实际 vs 模型 vs 偏差）日志，便于实盘校准 `AssumedHourlyBorrowInterestRate`。
  - **刻意不改权威 NetPnL/TotalBorrowCosts**（仍走模型口径）：避免跨并发借币误归因、不改任何熔断/风控行为；
    goroutine 旁路对平仓关键路径**零延迟**；查询失败仅记日志。已知口径限制（资产级窗口扣息、不含最后未结算的部分小时）在日志里显式标注。
- **回归测试**：`TestSumMarginInterestByAsset` / `TestSumMarginInterestByAssetEmpty`（解析与汇总）。
- **验收**：`go build` / `go vet` / 新测试 / 相关 close·settlement·drawdown·exposure 测试全绿（仅 pre-existing flaky 的异步结算 worker 测试与本改动无关，见 §备注）。

### P5 —— 结构性（放大资金前，非当务之急）

#### ✅ P5-1. Bybit 持仓状态文件补 schema envelope + fsync —— 已完成（2026-06-09）
- **改点**（`bybit_positions_store.go`）：原先是裸 JSON 数组、无 `schema_version`/checksum、且 `saveLocked` 用 `WriteFile`+`Rename` **缺 fsync**。
  现与币安 live 台账同口径:① 落盘加 `schema_version + SHA256 checksum` 信封(复用 `stampStateEnvelope`/`guardStateEnvelope`);
  ② 原子写补 **fsync**(tmp → `f.Sync()` → rename,防 rename 成功但内容未落盘);③ checksum 不匹配/版本超前 → **拒绝自动接管**(load 报错,reconcile 保守不接管,要求人工核对);
  ④ 旧版本写的裸数组(以 `[` 开头)按 legacy 直接放行,零迁移负担。
- **回归测试**:`TestBybitPositionStoreWritesEnvelope`(落盘带信封)、`...LegacyBareArrayPassthrough`(旧裸数组兼容)、`...RejectsTampered`(篡改拒绝);既有 `...RoundTrip` 仍绿。
- **验收**:`go build` / `go test`(bybit store)全绿。
- **意义**:刚扩展的 Bybit 负 carry 路径会落盘更关键的持仓状态(借币空现货),此前裸 JSON 一旦被篡改/写半截,重启会自信地用错误状态接管真实仓位 —— 现已堵上。

#### ✅ P5-2. panic-flatten 后硬停机 + 人工 ack —— 已完成（2026-06-09，opt-in 默认关闭）
- **改点**：新增 `RiskConfig.PanicFlattenHardStopEnabled`(cfg `panic_flatten_hard_stop_enabled`,默认 false)。
  开启后:实时浮亏 / 盈亏盲窗 / 保证金健康触发 `panicFlattenManagedExposure` 完成双腿收敛后,经 `requestPanicHardStopIfEnabled`
  设引擎级 `panicHardStopReason`;主循环 `checkLiveSafetyGuards` 经新增的 `checkPanicHardStop` 返回 `fatalEngineError`
  → **永久硬停机,要求人工核对账户/原因后重启**,而非默认的「平台熔断冷却结束自动复活继续交易」。
- **零行为变更**:开关关闭(默认)时 `requestPanicHardStopIfEnabled` 为 no-op、`panicHardStopReason` 恒空、guard 放行——保持熔断冷却自动复活。
- **回归测试**:`TestPanicHardStopOptInGatesFatalStop`(关→no-op 放行;开→panic 后 guard 返回 fatalEngineError)。
- **验收**:`go build` / `go vet` / 全包 `go test` 全绿。
- ⚠️ **数据采集姿态建议保持 false**(无人值守持续运行);校准期/放大前希望"出事即停等人"再置 true。

#### ⬜ P5-3 / P5-4（大改 / 基建,非当务,留待放大资金前）
- `FundingRateSniperEngine` 上帝对象拆分 / venue 抽象绞杀完成(大重构,风险高,需分多次小步)。
- 单机 → 至少做到"物理断连时安全停机而非裸奔"(热备/外部编排,超出单体原型范围)。

---

## 3. 实施顺序与总验收

1. **P0-3**（先人工清残腿、对账）→ 2. **P0-1**（解救援自锁）→ 3. **P0-2**（平仓 REST 兜底）→
   4. **P1-1**（单腿回退补全）→ 5. **P4**（确认成本分项齐全）→ 6. **P2**（Bybit 负 carry / allowlist TTL）→ 7. **P5**。

- 每步：`go vet ./... && go test ./... && go test -race ./internal/app && go build ./...` 全绿。
- **不改变任何入场费率/成本闸**（只增加执行可靠性与可观测性），保证"只看方向就吃"的姿态不被破坏。

### 备注：顺带修复的 pre-existing flaky 测试（2026-06-09）
- `TestScheduleAsyncFundingSettlementUpdatesSettlementAndDailyPnL`、`TestFundingSettlementWorkerRecoversPendingTaskAfterRestart`
  原本在完整套件/慢机器下偶发失败（"got 0.00000000"），单独跑也会 flake。根因：异步结算 worker **先写结算记录、再写
  `riskBudgetState.DailyRealizedPnL`（两步非原子）**，测试在「记录已结算」时立刻断言 dailyPnL，撞上两步之间的窗口即误判。
- **修法（纯测试，不动生产代码）**：把「dailyPnL 已更新」并入轮询终态条件，只在 deadline 超时才失败；deadline 500ms→2s 容忍慢机。
- **验证**：两测试连跑 5/5 通过；`go test ./...` 全包全绿；`go test -race`（close/settlement/drawdown/exposure/rescue）通过。

### 当前进度（2026-06-09）
- ✅ P0-1 救援自锁 / ✅ P0-2 平仓·救援 REST 兜底 / ✅ P1-1 单腿回退补全 / ✅ P4 真实成本观测（含借贷费真实扣息旁路）。
- ✅ P2-1 Bybit 负 carry（opt-in 默认关）/ ✅ P2-2 借币 allowlist TTL（经核对已实现）。
- ✅ P5-1 Bybit 持仓 envelope+fsync / ✅ P5-2 panic 后硬停机(opt-in 默认关)。
- ✅ 顺带修复 2 个 pre-existing flaky 结算测试（套件现可干净全绿）。
- ⬜ P0-3（运维：重启新二进制后人工对账清 ENA/TAO 残腿，代码替代不了）/ ⬜ P5-3·P5-4（上帝对象拆分·断连安全停机，大改/基建,放大资金前再做）。

### 北极星验收
> 让系统在**正、负任一方向**出现"方向正确且可执行（可借 + 盘口能成交）"的机会时，
> 能**干净地开仓 → 持有 → 平仓 → 结算**，并把这一笔的**真实手续费（含 BNB 折扣）、借币利息、两腿价差、滑点、实收 funding**
> 全部落进日志——拿到**第一笔可被精确复盘的完整周期数据**。
>
> 在拿到这个数据之前，所有风控/架构打磨都是次要的。这第一笔真实数据，才是回答"这个 edge 到底还存不存在"的唯一依据。

---

## 4. 全程序 Bug 审计与修复交接（2026-06-09 · 给接手 agent）

> 这是一次"设计/架构/逻辑 bug"全审计的结论 + 修复进度。审计用 `go vet`(全包过)+ `go test -race ./...`(全包过,
> 但 race 只覆盖**测试触达**路径,生产并发路径未必覆盖)+ 多视角代码核对。下列 bug **已逐条人工核实**(标 ✅核实)
> 或代理发现待接手者复核(标 ⚠️待复核)。每条标注严重度、位置、是否影响**当前配置**(`enable_rolling_hold=true`、
> 要"两平台正负都吃"、数据采集姿态)。

### 修复铁律(接手者必须遵守)
1. **新功能/行为变更一律 opt-in 默认关**;**单周期(`EnableRollingHold=false`)路径零行为变更**。
2. 每条修复:`go build ./... && go vet ./... && go test ./... && go test -race ./internal/app/`(相关)全绿 + 带回归测试。
3. **不抬高入场闸**(用户姿态:只看费率方向就吃,成本靠实盘日志看)。
4. 改完更新本文件对应条目状态。

### ✅ 已修(本次会话)
- **#1 滚动持有"组合抹除"(CRITICAL,曾命中当前配置)** —— 已完成。
  - 根因:开仓持久化是「整体替换」语义,`open_protocol.go:251`(内存)/`:294`(并发台账)/`engine_open_exec.go:254`(顺序台账)
    都用「仅本轮新开」替换,且此时 `activeOpenHedges` 仍是既有常驻仓 → 滚动持有下既有 keep 仓被从内存+台账双双抹掉
    → 未跟踪裸敞口;`rolling_hold_engine.go:175` 再抹一次。
  - 改法:新增 `mergeResidentBaselineWith(newHedges)`(`engine_risk_util.go`,把新开并入既有常驻、同名新开覆盖、读
    `activeOpenHedges` 持锁、幂等);上述 3 处持久化改为先 merge 再写;删除 `openResidentPositions` 里冗余的 set/sync。
    单周期开仓前组合为空 → merge==仅新开,**行为不变**。
  - 回归测试:`TestMergeResidentBaselineWith`。验收:`go build`/`go vet`/全包 `go test` 全绿。

### ⬜ 待修(按优先级,接手者从 #2 起)

**#2 借币利息重复计费 + 还币不含利息(HIGH,影响"负 carry + 滚动")** ✅核实
- `rolling_hold_engine.go:47` `settleKeptPositions` 每个 keep 周期调 `settlementBorrowCost(hedge, now)`,而该函数
  (`engine_cost_guard.go:197-213`)算的是 **开仓→现在的累计利息**(起点 `SpotOrder.TransactTime`),不是"距上次结算的增量"
  → 滚动每轮重记 open→now,**借币成本翻 N 倍**,污染 NetPnL/日亏/连亏熔断。drop 平仓(`close_protocol.go:288`)又全额再记一次。
- Bybit 侧同类:`bybit_exec_live.go:358-365` 平仓还币量=本金 `pos.BaseQty`,**不含已计利息** → 可能留残债。
- 修法:借币计费改"增量"(记录上次结算时点,只算 lastSettle→now);Bybit 还币按实际欠款(查 borrow 余额或本金+利息估)。

**#3 部分成交被当全量成交(HIGH,双平台双方向)** ✅核实
- `bybit_exec_live.go` 开仓现货腿只判 `spotExec.ExecutedQty <= executionQtyTolerance`,**没判 `< hedgeQty`**:
  成交 5/10(status=filled)也算成功,却记 `BaseQty=hedgeQty`(永续全量)→ 单边裸腿。币安开仓路径同类(核对 `engine_open_exec.go`/`open_protocol.go` 的成交量校验)。
- 修法:要求 `spotExec.ExecutedQty ≈ hedgeQty`(容差内)才算成功;不足则回补永续到对齐 或 补开现货差额。

**#4 Bybit reconcile 误删负 carry 孤儿借币(HIGH,负 carry)** ⚠️待复核
- `bybit_runtime.go:212-220` perp 平了但还币失败时,只看 `bybitSpotFreeBalance`(借币空头 free base≈0)→ 删台账,借币债务无人管+继续计息。
- 修法:reconcile 检测未还借币负债(借 API 或"还币失败"标志),有债务绝不 `store.remove`。

**#5 Bybit 裸永续平仓仍发"现货买回还币"(MED-HIGH,负 carry)** ⚠️待复核
- `bybit_exec_live.go:357-377` 对任意 `pos.Negative` 都跑 spot BUY+Repay;revert 失败留下的纯永续裸腿(`SpotEntryPrice=0`)也照买 → 乱买/PnL 用 0 价算错。
- 修法:`SpotEntryPrice==0`(或加 `SpotLegOpened` 标志)时跳过现货腿。

**#6 全局 panic 与单币止血并发 → 重复平仓单(MED,并发)** ✅核实
- `realtime_drawdown.go:52-58`、`hold_monitor.go:159-166`:两条 goroutine 无互斥,可对同一 hedge 同时发平仓单
  (`managedOpenHedgeBySymbol` 返回 OpenHedge **值拷贝**,各自过 `guardHedgeOrderIntent`、各自 `markClosing`)。
- 修法:全局/单币 panic 共用一把"收敛中"互斥(symbol 级 + 全局级统一登记),或全局 panic 跳过已在单币收敛中的 symbol。

**#7 生命周期状态机 SSOT 与真实阶段不同步(MED,观测)** ✅核实
- `open_protocol.go:92`/`close_protocol.go:79` 声明的 `CurrentStage`(Opening/Closing)与机器实际阶段(PreOpenCheck/CloseMonitoring)
  不一致 → `applyLifecycleProtocolResult` 丢弃迁移(日志"忽略非法生命周期迁移"),主开仓路径 lifecycle **从未走过 Opening→Holding**
  → 面板/审计/依赖 stage 的守卫看到陈旧阶段。**不是无害日志**。
- 修法:让协议声明的 from-stage 与机器当前阶段对齐(或在 prepare/execute 前推进机器到对应阶段)。

**#8 滚动复核固定 +8h、不分 4h/8h 币(MED)** ⚠️待复核
- `rolling_hold_engine.go:155`(`defaultFundingIntervalHours=8`)、`:333` `advanceResidentFundingClocks` 恒 +8h → 4h 币复核时点错。
- 修法:按逐币真实 funding interval(已有 `FundingIntervalsHours` 能力位 / instruments fundingInterval)推进时钟。

**#9 Bybit 负 carry 无借币容量预检(MED)** ⚠️待复核
- 币安有 `dropBorrowUnavailableNegativeCarry`,Bybit 无 → 借不到的币照开永续、白烧手续费再回补。
- 修法:实现 Bybit `MaxBorrowable`(读 UTA wallet-balance `availableToBorrow`)+ 选币阶段预剔除借不到的负 carry 候选。

### ⚠️ 待复核(代理标疑,未逐一证实)
- `currentCycleID` 可能无锁并发读(`open_hedge_state.go:259`、`close_protocol.go:330`、`engine_risk_util.go:402`);确认哪些读在后台 goroutine(`runID` 启动后只读、安全)。
- 恢复重试用纳秒级 `newClientOrderID`(非稳定幂等键),超时后成交可能重复下单(reduceOnly 多半被拒,现货追价腿有风险):`recovery_protocol.go:446`、`exposure_manager.go:517`。
- 恢复用 5s 缓存的合约持仓 → 一次 handleTask 内对已平仓位重复下单:`exposure_manager.go:490-537`。

### 经核对【正确,无需改】
- 单周期正/负 carry 的腿方向、PnL 符号、funding 归因:对(`live_negative_carry.go:86-118`、`historical_pricing.go:58-84`、两阶段结算无 funding 重复计)。
- 恢复路径 reduceOnly 守卫:`guardRecoveryFuturesReduceOnly` 阻止恢复通道重开敞口(`exposure_manager.go:294-301`)。
- P5-2 `panicHardStopReason`、`EmitLocked`/`Emit` 锁纪律、`feedLivenessMu` 锁序、`observeActualBorrowInterest` goroutine:核实一致/低风险。

### 接手建议顺序
#2(经济性正确)→ #3(防裸腿)→ #4/#5(Bybit 负 carry 健壮性)→ #6/#7(稳健性)→ #8/#9(精度/效率)→ 待复核项。
**#1 已修但放大到多币前务必复跑**:`go test -race ./internal/app/` + 构造"持有 A 仓时开 B 仓"的集成场景验证 A 不被抹。

### 运维待办(代码替代不了,用户必做)
- **P0-3**:构建新二进制 → 重启 → `lingxi reconcile` + 查币安账户,确认 `live_exposure_rescue_tasks.json` 里 ENA/TAO 旧卡死残腿的真实持仓,清掉或 `lingxi flatten`,再让引擎接管。
- Bybit 负 carry 启用前:确认 UTA 已开通现货杠杆借币 + testnet/极小额验证借币空现货能成交且能买回还币,才置 `enable_negative_carry: true`。
