# lingxi

`lingxi` 是一个围绕 Binance 现货 / U 本位合约组合、资金费率机会筛选、执行风控、历史模拟和本地面板观测构建的单体交易系统原型。

> 本 README 经过一次「以代码现实为准」的全面校订：凡是 README 承诺但代码尚未实现 / 默认关闭的能力，都在「已知风险与局限」一节明确标注，避免文档制造虚假安全感。最后附「通往 9.6 分的优化路线图」。

---

## 一、项目目标
- 在资金费率机会出现前完成候选筛选、流动性和容量过滤。
- 在资金费结算窗口附近执行现货/合约对冲开平仓（delta 中性，吃资金费）。
- 对执行异常、平台异常、订单不确定性和残腿风险做风控约束。
- 将运行状态、风险事件、结算结果、监控记录和回测结果落盘并可视化。

策略本质是**资金费率套利（funding carry）**：
- **正向 carry**：现货持有 + 合约做空，吃正资金费率。
- **负向 carry**：全仓杠杆借币做空现货 + 合约做多，吃负资金费率（产生借币利息成本）。

边际很薄：单周期对冲往返的真实盈亏平衡线 = `FeeReserveRate(0.0030) + ExitSpreadReserveRate(0.0005) + MinNetProfitRate(0.0020) = 0.0055`。低于此预估费率的机会注定亏损或被监控期净利门拒绝。

---

## 二、系统架构总览

进程是单体 Go 应用，核心是一个大型引擎对象 `FundingRateSniperEngine`，周边由可独立单测的边界包支撑。

### 2.1 入口与编排
- `cmd/lingxi/main.go`：进程入口薄壳，负责信号处理 + 根 context，委托 `internal/app.Run`。
- `internal/app/run.go`：应用编排入口，按运行模式（实盘 / 回测 / 对账 / 影子）选择路径并装配依赖。
- `internal/app/funding_sniper_engine.go`：主引擎编排核心（扫描→评估→开仓→监控→结算→平仓的总调度）。
- `internal/app/runtime_controller.go`：运行期控制器（启停、参数热调整、控制审计）。

### 2.2 交易核心域（`internal/app/`）
- `strategy_evaluator.go` / `strategy_models.go`：选币、打分、成本进场闸（cost gate）。
- `open_protocol.go` / `engine_open_exec.go`：开仓协议（先合约后现货、并发/顺序池、超配回滚、裸腿修正、开仓后逆向选择复核）。
- `close_protocol.go`：平仓协议（资金费结算窗口附近的对冲平仓、被动现货优先）。
- `engine_settlement.go` / `funding_settlement_worker.go`：两阶段结算——平仓先记交易/费用/借币，资金费实收后异步重算 `NetPnL`（用真实 `/fapi/v1/income`）。
- `engine_cost_guard.go` / `cost_accounting.go` / `cost_reporter.go`：成本核算、借币利息估算、进场成本闸。
- `live_negative_carry.go`：负向 carry（借币做空现货）专属路径与保守费率估计。
- `exposure_manager.go`：残腿/敞口救援队（落盘任务、重启恢复、`reduceOnly` 物理硬保证只减不加）。
- `recovery_protocol.go` / `order_recovery_policy.go`：订单恢复与不确定终态处理策略。
- `realtime_drawdown.go`：实时浮亏 panic-flatten 与单币止血。
- `risk_supervisor.go` / `risk_event.go` / `risk_contract.go`：风险监督器、风险事件、风控契约。
- `live_safety.go`：账户级 fail-safe（连亏熔断、回撤熔断、日级预算、急停文件）。
- `lifecycle_state_machine.go` / `lifecycle_order_gate*`：生命周期状态机与下单闸。
- `dust_ledger.go`：灰尘残差台账（不可交易的微小残腿登记）。

### 2.3 网络与下单咽喉点
- `internal/exchange/`：**唯一摸网络的包**。`client.go`（现货/合约元数据、账户费率、历史抓取、限频/回退/used-weight 治理）、`http.go` / `transport.go`（代理与连接池）、`symbols.go`（黄金币种交集）、`cache.go`（历史负载缓存）。
- `internal/app/binance_gateway.go`：`SignedRequest` / `GetJSON` / `APIKeyRequest` 收口，联动平台熔断、used-weight 预算、本地下单限频。
- `internal/app/engine_fees_market.go`：`signedRequest` 是**所有签名请求的唯一咽喉点**——影子模式在此硬拦截一切非 GET 调用（绝无真实订单/资金动作漏出）。
- `internal/app/order_events.go`（`OrderEventHub`）/ `user_data_stream.go` / `websocket_bookticker.go`：订单回报中心、用户数据流、bookTicker/depth 行情流。

### 2.4 边界包（可独立单测，无网络依赖）
- `internal/core/`：配置内核（`EngineConfig`、`DefaultEngineConfig()`、`Validate()`、Profile）。
- `internal/money/`：**定点记账内核**（micro-USDT int64，消除 float 累加漂移）。
- `internal/risk/`：纯风险语义（动作/严重度词汇 + 确定性裁判函数，如对冲偏离、流动性份额、成交覆盖率）。
- `internal/fillmodel/`：成交/费用/残差模型。
- `internal/execution/`：定价与追价（pricing）。
- `internal/lifecycle/`：生命周期状态模型。
- `internal/strategy/`：策略纯逻辑。
- `internal/marketdata/`：微观结构（深度/价差）。
- `internal/store/` / `internal/app/report_store*.go`：记录存储、JSON/JSONL 编解码与迁移、保留策略。
- `internal/reconcile/`：只读对账内核。
- `internal/metrics/`：指标。

### 2.5 回测 / 模拟
- `backtest_runner.go` / `backtest_csv_io.go`：CSV 回测与回放。
- `historical_*.go`（monthly_backtest / simulate / execution / pricing / kline_cache / candidate …）：真实历史行情月度模拟全链路。

### 2.6 面板
- `internal/app/dashboard_server.go` + `internal/app/webpanel/index.html`：本地控制面板与 API。

---

## 三、当前能力
- 实盘运行入口和 Web Dashboard
- Binance REST / WebSocket 行情与用户数据流
- 订单回报中心 `OrderEventHub`
- 分层配置 `EngineConfig` 与启动前校验
- 风控语义、风险事件和阶段迁移模型
- 回测、历史回放、月度真实行情模拟
- 历史请求缓存、运行日志、控制操作审计
- JSON / JSONL 记录落盘与运行期保留策略
- 定点记账（`money.Micros`）贯穿结算
- 残腿救援（`ExposureManager`）与重启恢复
- 影子（只读冒烟）模式硬拦截

---

## 四、运行模式
- `Dashboard` 模式
  - `go run ./cmd/lingxi`
  - 启动本地面板，默认监听 `127.0.0.1:8080`
  - 页面可启动实盘引擎、月度模拟、查看记录与运行状态
- `Backtest` 模式
  - `go run ./cmd/lingxi backtest`
  - 读取 `backtest/sample_backtest_trades.csv`
- `Replay` 模式
  - `go run ./cmd/lingxi backtest replay`
  - 读取 `backtest/sample_backtest_replay.csv`
- `Timeline` 模式
  - `go run ./cmd/lingxi backtest timeline`
  - 读取 `backtest/sample_backtest_timeline.csv`
- `Monthly` 模式
  - `go run ./cmd/lingxi backtest-monthly secret.yaml 30`
  - 使用真实历史行情做月度模拟
- `Reconcile`（只读对账）模式
  - `go run ./cmd/lingxi reconcile`
  - 把已落盘结算记录与「模型预期」逐周期对账，输出胜率、实盘累计净利 vs 预期、资金费预测误差、
    超阈值周期与 HEALTHY/UNHEALTHY 结论
  - 不触碰任何下单/实盘路径，可在实盘运行期间随时执行；用于小额实盘验证「钱是不是按模型假设赚到的」
  - **注意**：当前对账把「实测手续费/借币成本」抄到「模型预期」一侧，因此成本侧的模型误差不会出现在对账里（见「已知风险与局限 R6」）。
- `Shadow`（影子只读冒烟）模式
  - `go run ./cmd/lingxi shadow`
  - 照常对接真实行情/账户、照常完成扫描/评估/决策，但任何会动用资金或改变交易所状态的签名请求
    （下单/撤单/改杠杆/划转/借还币等一切非 GET）都会在唯一咽喉点 `signedRequest` 被**硬拦截**、
    绝不触达交易所，下单意图以 `SHADOW_BLOCKED` 日志行落盘供核查
  - 用途：实盘前的安全冒烟，抓「下错方向 / 异常数量 / 失控循环」等灾难性 bug；**不是**用来校准经济阈值
    （模拟/影子量不到真实滑点、成交率、真实资金费与竞争，纸面数字会系统性偏乐观）
  - 也可通过配置 `execution.shadow_trading: true` 开启

`backtest/README.md` 里有更详细的样例文件说明和回放字段解释。

---

## 五、核心交易流程（一个完整周期）
1. **唤醒**（`WakeMinute=57`）：资金费结算前数分钟，同步服务器时间偏移、刷新账户。
2. **扫描**（`ScanWindow=3min`）：拉 `premiumIndex`、黄金币种交集，按预估费率/流动性粗筛。
3. **评估**：`strategy_evaluator` 深度打分 + 成本进场闸（费用 + 价差 + 借币储备 + 最小净利门）。
4. **预备**（`OpenPreparationLead=5s`）：设杠杆/全仓、确保 USDT/BNB 资金、构建顺序/并发开仓计划。
5. **战前 Go/No-Go**（`PreOpenNoGoLead=1s`）：再次校验 ticker 新鲜度、资金费稳定性、保证金充足。
6. **开仓**（fire-at）：先下合约批量单 → 解析成交 → 下现货对冲单 → 超配回滚 / 裸腿修正 → 开仓后逆向选择复核（不达标即时撤退）。
7. **监控/持有**：实时 bookTicker + 监控期净利门 + 实时浮亏 panic-flatten。
8. **平仓**（资金费结算 + `ExitDelayAfterFunding=2s`）：对冲平仓，被动现货优先。
9. **结算**：先记交易/费用/借币 → 资金费实收后异步重算 `NetPnL` → 更新连亏/回撤/日级预算。
10. **对账**（可选、只读）：`reconcile` 验证实盘是否按模型赚钱。

任意一步异常 → 残腿落入 `ExposureManager` 托管或灰尘台账，符号进入冷却。

---

## 六、配置与密钥
运行密钥默认从根目录 `secret.yaml` 读取，格式如下：

```yaml
api: your_binance_api_key
secret: your_binance_api_secret
proxy_url: http://127.0.0.1:7890
```

说明：
- `proxy_url` 可选
- 可直接复制 `secret.example.yaml` 为 `secret.yaml` 后再填写真实密钥
- `secret.yaml` 仅用于本机，不应提交到仓库
- 当前策略 / 风控默认值在 `DefaultEngineConfig()` 中定义
- **当前局限**：策略 / 风控参数硬编码在 `DefaultEngineConfig()`，调参 = 改代码 = 重新构建；建议引入 `config.yaml` 和环境变量覆盖（见路线图 P3）

---

## 七、记录文件
当前记录目录默认是 `records/`，属于运行产物，不应提交。

常见输出包括：
- `settlements.json`
- `backtest_records.json`
- `backtest_run_summaries.json`
- `residual_risk_records.json`
- `monitor_records.jsonl`
- `execution_incidents.jsonl`
- `runtime_events.jsonl`
- `dashboard_control_audit.jsonl`
- `live_exposure_rescue_tasks.json`（残腿救援台账，重启恢复用）
- `live_daily_risk_state.json`（连亏/日级风控状态，跨重启恢复用）

说明：
- 高频记录已经逐步转到 `JSONL append-only`
- 运行态记录和缓存默认只保留最近几小时，避免长期无限增长

---

## 八、不应提交的文件
以下内容默认应留在本机：
- `secret.yaml`
- `records/`
- `.lingxi.pid`
- 本地编译产物 `lingxi`
- 真实运行日志
- 历史缓存和审计日志

适合提交的内容：
- `.go` 源码
- `go.mod` / `go.sum`
- `webpanel/index.html`
- `backtest/README.md`
- `backtest/` 下少量脱敏样例数据
- `README.md`
- `.gitignore`

---

## 九、开发命令
本地常用检查：

```bash
make fmt
make vet
make test
make race
make cover
make build
```

也可以直接执行底层 Go 命令：

```bash
gofmt -w *.go
go test ./...
go vet ./...
go build ./...
```

CI（`.github/workflows/ci.yml`）会跑：`gofmt` 检查 → `go vet` → `go test` → `go test -race` → `cover` → `build` + `make build` → `govulncheck`。

如果本机 Go 构建缓存权限受限，建议优先修复本机权限问题，或在 CI 中至少固定执行上述检查。

---

## 十、面板与安全
- 面板默认只监听 `127.0.0.1`
- 修改类 API 依赖本机来源校验和 `controlToken`
- `/records/` 目录会通过 HTTP 暴露记录文件，所以不要把密钥、真实私密文件放进 `records/`
- 如果未来需要监听非 localhost，必须再加正式认证和更严格的访问控制

---

## 十一、账户级风控刹车（fail-safe，只停机不下单）
- 合约默认 **全仓(CROSSED) + 2x 杠杆**：全仓保住 delta 中性对冲不被单腿强平打破，2x 拉远强平价。
- 实时浮亏 panic-flatten + 单币止血（`realtime_drawdown.go`）。
- 开仓前**保证金充足性**校验：`assessFuturesLeverageRisk` 检查 `initialMargin + maintMargin ≤ futuresAvailable`，不足则拒单。
- `MaxConsecutiveLossCycles`：按 funding 实收结算后的真实净利连亏 N 周期即停机（当日 UTC 维度，跨重启保留）。
- `MaxAccountDrawdownUSDT`：以全部已结算周期净利的累计曲线为准，自历史高水位回撤超阈值即停机（回撤由结算记录回放得出，重启自动复原）。

> ⚠️ 以下两条曾在旧版 README 中被表述为「已具备」，经代码核对**与现实不符**，已下调为「待实现」：
> - **开仓前「爆仓距离/强平距离」校验**：当前**未实现**。代码只有保证金充足性检查，没有对强平价或强平距离的计算。2x 全仓拉远了强平价，但不存在显式的「距离校验」。
> - **对冲两腿名义偏离熔断（Gate 4 关键不变量守卫）**：代码存在（`risk.EvaluateHedgeImbalance` + `assertHedgeInvariant`），但 `HedgeImbalanceAbortUSDT` / `HedgeImbalanceAbortRate` 在 `DefaultEngineConfig()` 中**默认 0 = 关闭**，且 `config_loader.go` 无对应 YAML key，**不改代码无法开启**。出厂状态下该守卫为空操作。

---

## 十二、已知风险与局限（实盘前必须正视）

> 严重度：🔴 致命（可致真实裸敞口 / 虚假安全感） · 🟠 重大（误判盈亏 / 误停机） · 🟡 一般（边界 / 可维护性）

### 🔴 R1. Gate 4 对冲偏离守卫默认关闭
`HedgeImbalanceAbortUSDT/Rate` 默认 0，运行时对冲两腿名义偏离的熔断为空操作。一旦某腿在持有期被悄悄放大/缩小，没有运行时拦截。**影响**：裸敞口可在运行期累积而不被熔断。

### 🔴 R2. 缺少「开仓前爆仓距离校验」
README 旧承诺与代码不符（见上节）。仅有保证金充足性检查，无强平距离计算。**影响**：极端行情下单腿被强平的概率高于文档暗示。

### 🔴 R3. panic-flatten 只看合约腿浮亏，对现货腿是盲的
`realtime_drawdown.go` 的实时急停用「受管合约仓位 `UnrealizedProfit` 之和」判断，忽略现货腿；且某腿 `PositionAmt→0` 后受管浮亏被从 map 删除。**影响**：现货腿先出问题、或单腿被强平/ADL 时，急停可能不触发。全系统**无专门的单腿强平/ADL 事件处理器**，中途单腿被打掉只能等下次启动 `verifyTradingEnvironment` 才发现。

### 🔴 R4. `serverTimeOffset` 数据竞争
`engine_types.go` 中 `serverTimeOffset time.Duration` 为裸字段，`syncServerTimeOffset` 裸写、`serverNow()` 裸读，且 `serverNow()` 被监控 worker / 并发开平仓 / 签名时间戳并发调用，无锁/atomic 保护。**影响**：撕裂读取导致 fire-at 时点漂移，在错误时刻动手。`go vet` 抓不到，`go test -race` 大概率会爆。（同类较轻：`websocket_bookticker.go` `DepthCache.refCount` 未加锁递增。）

### ✅ R5. 净利 `NetPnL` 不含滑点与恢复摩擦 —— 经审定为**误判**,口径本就诚实(S4 收尾·对账口径审查)
原描述「滑点/恢复摩擦不进 NetPnL」**经代码审查证实不成立**:
- **滑点已在 `NetPnL`**:`TradingPnL` 由**实际成交价**算出 —— 现货 `entry=OpenSpotQuoteQty/qty`、`exit=CloseSpotQuoteQty/qty`(`close_protocol.go:264-267`),合约用**交易所实现盈亏**或实际成交均价(`:273-283`)。成交劣于参考价的滑点已隐含其中。`OpenSlippageUSDT/CloseSlippageUSDT` 只是「相对参考价偏离」的诊断量,**再从 NetPnL 扣一次即重复计数**。
- **恢复摩擦已在 `NetPnL` 且度量恒为 0**:恢复成交的额外名义/手续费已折进 `hedge.CloseSpotQuoteQty`、`Close*FeeUSDT`(`close_protocol.go:135/171-174`)→ 已在 `TradingPnL/TotalFees`;而 `RecoveryFrictionUSDT` 各路径以 `targetPrice=execution.AvgPrice` 自比(`recovery_protocol.go:475-476/523-524/569/612`)→ 结构性恒 0。
- **结论**:连亏/回撤/单周期熔断口径**未偏乐观**(滑点/恢复成本已计入权威 NetPnL)。**真正漏算项:无**。
- **已修复**:`NetPnLAfterFrictions` 经语义重定为「零滑点理想净利基准」=`NetPnL − Σ滑点`(把已在 NetPnL 的滑点加回得到理想值),`gap=NetPnL−基准=Σ滑点≤0` 为执行拖累 KPI,修掉了 M-S3.0「NetPnL+滑点」的重复计数。诊断专用,不参与任何熔断。

### 🟠 R6. 对账会给亏钱策略盖「健康」章
`reconcile_command.go` 把实测手续费/借币成本抄到「模型预期」一侧，成本侧模型误差在对账里恒为 0；默认只有绝对阈值（`SettlementDiffAlertUSDT=3`），相对阈值=0，小名义下大比例泄漏能蒙混过 HEALTHY。**影响**：对账无法完成「验证钱按模型赚到」的核心使命。

### 🟠 R7. 资金费估计在符号冲突时退回乐观值
当 premium 与 last funding rate 符号不一致时，保守估计返回 0，估计值退回 `lastFundingRate`（可能过度乐观）。**影响**：在资金费率正要反转时（最危险的时刻）用旧方向费率开仓。

### ✅ R8. 周期/日级亏损熔断在资金费到账前误触发（已修复 · S3 收尾）
原问题：`MaxCycleLossUSDT` / `MaxDailyLossUSDT` 在平仓时用 pre-funding 的 `NetPnL` 判断（此时 `FundingIncomePending=true`），对靠资金费翻正的策略会误停机。
- **日亏（`MaxDailyLossUSDT`）**：已对齐 final——平仓先按 pre-funding 计入 `DailyRealizedPnL`，funding 实收后由结算 worker 补 `deltaFunding` 修正到真实净利。
- **连亏 / 账户回撤**：已在 `noteCycleFinalSettlement` 用 funding-settled 的 final 净利推进。
- **单周期（`MaxCycleLossUSDT`）口径修正（本次收尾）**：① 平仓同步「早刹车」改用**投影最终净利 = `NetPnL + ExpectedFundingIncome`**（`projectedCycleNetPnL`），消灭「资金费未到账被误判巨亏」的误停，仍保留 `fatalEngineError` 永久停机以挡住开/平腿真实巨亏；② funding 实收后由 `noteCycleFinalSettlement` 记录当日最差单周期最终净亏（`WorstCycleNetLossUSDT`，当日维度、跨日清零），经 `currentDailyLimitReasonLocked → checkDailyRiskLimits` 做权威复核停机，兜住「实收资金费远低于预期」的漏网。
- **测试**：`TestCycleLossBreakerTripsOnFinalNetLoss`、`TestProjectedCycleNetPnLAddsExpectedFundingWhenPending`。

### 🟠 R9. 连亏计数 UTC 每日清零
连亏维度按 UTC 交易日清零，与 README「补跨日慢性失血」的意图存在缝隙：每天小亏、不触发回撤峰值的慢性失血可能被漏掉。

### 🟡 R10. 上帝对象与锁纪律靠人脑维持
`FundingRateSniperEngine` 背 6 把锁 + 多个 atomic + 9 个 `sync.Once`，多个 750+ 行巨型文件；按字段分锁（`platformRiskMu`/`orderRateMu` 守 `platformState` 不同字段）靠约定维持，新增字段易踩进 R4 类竞争。

### 🟡 R11. 配置与代码未分离
策略/风控参数硬编码，调参需改代码重新构建——实盘期间是事故源。

### 🟡 R12. 并发开仓首错不取消兄弟任务
`runBoundedConcurrentTasks` 首个 worker 失败不取消其余 worker，放大竞争窗口与潜在多腿暴露。

---

> 以下 R13~R15 为战略性/规模化局限（外部评审意见，经代码核对采纳）。它们在**当前小额默认档**（`PerSymbolQuoteUSDT=100`、`MaxCycleOpenNotionalUSDT=300`）下多数不是即时亏损源，但**是放大资金前必须先解决的前置条件**，对应路线图 P4。

### 🟠 R13. 执行算法与大资金冲击成本控制（Execution Scalability）
当前开仓以 **Protected IOC + 追价吃单**为主（平仓现货腿有被动优先 `preferPassiveSpot`，但开仓腿整体偏激进吃单）。系统已有流动性份额闸（`MaxRecentVolumeShare` / `MaxOpenInterestShare` / `MaxVisibleDepthShare`），在小额下能控制单笔占用，但**这只是「限制规模」而非「平滑执行」**。
- **缺失的解法**：没有 TWAP / VWAP 时间切片、冰山委托（Iceberg）、或被动做市挂单介入能力。随着资金体量放大，粗暴吃单的流动性冲击成本会非线性上升，无法承载大资金平滑进出。
- **影响**：当前规模无碍；放大档位后冲击成本会吃掉本就只有 0.0055 的薄边际，且 R5 中「滑点不进 `NetPnL`」会让这部分损耗更难被察觉。

### 🔴 R14. 系统级高可用与灾难接管（HA & Disaster Recovery）
当前是**单体进程**。已有的韧性仅限于「重启后」：`ExposureManager` 落盘任务恢复 + 启动期 `verifyTradingEnvironment` 兜底 + `live_daily_risk_state.json` 风控状态复原。
- **缺失的解法**：**进程存活期间**一旦断网、机器宕机或被 OOM kill，正在进行的对冲订单/持仓会直接裸露在市场中，且在重启前无任何自动保护。缺乏多节点热备（Hot Standby）、外部看门狗（Watchdog）、以及交易所侧的 **Kill Switch**（如 Binance 的自动撤单/到期保护）来保证物理断连时的绝对安全。
- **影响**：这是当前最大的「黑天鹅」敞口——代码内的所有 fail-safe 都建立在「进程还活着」的前提上，而进程本身没有冗余。物理断连即裸奔。

### 🟠 R15. 回测引擎的「微观失真」
当前回测/模拟（`fillmodel/cost.go` 残差 haircut + `historical_*` 冲击乘数）无法精确还原 Order Book 级别的排队延迟与滑点；`ExpectedLegFillRatio=0.99`、滑点用 `MarkPrice` 近似（见 R5）。
- **缺失的解法**：缺乏 L3 级 Tick 数据回测框架与订单簿排队位置模拟。在抢资金费的极端踩踏窗口，真实滑点远比模拟恶劣。
- **影响**：纸面阈值系统性偏乐观，回测/影子的盈利数字不能直接外推到实盘；这也是 README 反复强调「shadow 不用于校准经济阈值」的根因。

---

> 以下 R16~R24 为外部评审提出的「交付/治理/工程契约」类问题，经代码逐条核对采纳。它们多数不是当下亏钱的 bug，而是**阻挡外部评审给到 9.6 的非功能性门槛**与**未来演进的事故源**。对应路线图 P5。
>
> 评审另提「仓库缺 `cmd/` 与 `package main`，构建闭环不可信」一条——经核对**对当前仓库不成立**：`cmd/lingxi/main.go` 存在且含 `func main()`，`go build ./... && make build` 闭环完整（评审应是基于不含 `cmd/` 的导出快照）。但「main/build/CI/README 必须始终是同一个现实」的原则有效，已列入 P5 交付治理。

### 🔴 R16. Dashboard 控制令牌明文打印，读面凭证模型偏弱
启动时 `controlToken` 被**明文打到日志**（`dashboard_server.go:118-119`）。鉴权模型上：**写操作强制 loopback + token**（`authorizeRuntimeControl:545`，保守，good），但**读操作只要带合法 token 即可非 loopback 访问**（`authorizeRead:563-568`，覆盖 `/records/`、对账、运行状态等高价值情报）。审计日志里 token 是哈希存的（`:612`，good）。
- **影响**：一旦日志被转存/截屏/共享/被同机其他进程读到，读面就相当于对外开放（前提是面板被绑定到非 loopback——默认 `127.0.0.1` 时不暴露，但默认配置不应是唯一防线）。交易系统的日志里出现明文能力凭证本身就不应该。

### 🟠 R17. 配置系统不是真 YAML，只是「扁平 key:value 解析器」
文件名叫 `config.yaml`，但解析器是逐行 `strings.SplitN(line, ":", 2)`（`engine_config.go:91`），只支持平铺键值，不支持真正 YAML 的层级、数组、复杂字符串语义。
- **影响**：用户以为在写 YAML，实际不是；配置复杂度上来后这套自制 parser 会成为事故源。9.6 要求配置「强约束 + 可验证 + 真实语义一致」。

### 🔴 R18. 运行态关键状态文件缺 schema version / migration 契约
`report_store` 对历史记录有迁移意识（legacy 文件迁移），但真正决定**恢复安全**的运行态文件——未平仓台账（`open_hedge_state.go`）、残腿救援台账（`exposure_manager.go`）、日级风控状态（`live_safety.go`）——都只有业务字段 + `updated_at`，**无显式 `schema_version` / 兼容版本 / 校验机制**。
- **影响**：字段语义一旦变更，重启恢复可能「成功反序列化但语义已错」——这是最危险的一类故障（系统自信地用错误状态接管真实仓位）。

### 🟠 R19. 结算 / pending funding / dust ledger 仍是「整文件重写」
这几类状态更新当前把整个 JSON 数组读进内存、改完整体重写（`report_store.go`）。
- **影响**：当前规模无碍，但对 9.6 而言，写放大、崩溃恢复、部分失败、并发一致性、审计重放都没解决。高分做法是 append-only event log + materialized view，或 SQLite/Postgres 这类有事务语义的存储。

### 🟠 R20. 编排层 `Run()` 仍像伪 main，难宿主化
`internal/app.Run()` 名义上是编排入口，但内部有 **9 处 `log.Fatalf`**（`run.go:31/53/58/62/66/69/77/81/149`）。
- **影响**：很难被别的宿主复用，难做细粒度集成测试、守护进程接管、上层 supervisor 重试。9.6 的编排层应更多返回 `error`，把退出策略交给真正的 `main` 或 supervisor。

### 🟡 R21. 引擎状态面过大（比「文件长」更严重）
`FundingRateSniperEngine`（`engine_types.go`）把交易状态、风控状态、实时账户、运行时控制、恢复、面板、报告几乎全挤在一个核心 struct 上。
- **影响**：「新增字段」本身就是风险事件——每次加字段都要重新判断锁归属、生命周期、落盘责任、恢复语义（R4 的 `serverTimeOffset` 竞争就是这种结构性脆弱的产物）。这是对 R10 的深化。

### 🟡 R22. 原子写已有，但缺「完整性证明」
底层 `WriteJSONFile` 已做临时文件 + `rename`（`fileio.go:66/87`），解决了「写半截」；但运行态文件仍无 checksum、epoch、source-of-truth 标识、写入代次号。
- **影响**：未解决「写对了但语义过时 / 来源不明 / 跨版本不兼容」问题（与 R18 互补）。

### 🟡 R23. 对账报告诊断语义不干净
`reconcile` 把 `CycleID` 填进了 `Symbol` 字段输出报告（`reconcile_command.go`）。功能能用，但诊断语义混淆。
- **影响**：实盘排障越来越依赖自动化报告定位异常时，这类「小脏」会放大排查成本。

### 🟠 R24. 配置暴露是「白名单式人工维护」，天然容易漂移
`envConfigOverrides()`（`config_loader.go:40`）与 `applyEngineConfigOverrides()`（`:107`）都是手工枚举字段。
- **影响**：结构体里有字段 ≠ YAML/env 能配；README 写了能配 ≠ loader 真接上了。**R1 的 `HedgeImbalanceAbort*` 无法通过配置开启，本质就是这个系统性缺陷的实例**。需要结构体自动映射，消灭人工白名单。

---

> 以下 R25~R28 是对「优化计划本身」的审判（god's-eye on the plan）：即使 R1~R24 全修、M0~M6 全做完，
> 一个真正 9.6 的**无人值守实盘**系统仍缺下列维度。它们不是代码 bug，而是计划的结构性盲区。

### 🟠 R25. 无实时告警与可观测性（无人值守的致命盲区）→ S5（告警分发已落地·S5-A）
**S5-A 已落地实时告警分发**：`internal/app/alerting.go` —— `AlertDispatcher`（级别过滤 + 去重窗口 + fire-and-forget 异步推送）+ `AlertSink` 出口接口（`webhook` / `Telegram` / 兜底 `logger`），接在 `emitDomainEvents` funnel 上,熔断/裸腿/恢复升级/fatal 等领域事件按级别自动推送。全部 behind `AlertingConfig.Enabled`(默认 false → 零外部调用)。**S5-B 已落地指标/探针/心跳**:`observability_http.go` —— `/livez`(进程存活)、`/healthz`(就绪:运行+未熔断+健康正常才 200,否则 503)、`/metrics`(Prometheus 文本格式,无新依赖)三个不鉴权端点 + 引擎周期写 `records/heartbeat.json` 心跳文件(供无 HTTP 的外部看门狗判活)。
- **影响**：真金无人值守运行时等于盲飞——出事只能事后从日志里考古。9.6 必须有：关键事件实时推送（Telegram/钉钉/PagerDuty）+ 指标导出（Prometheus/统计端点）+ 存活心跳。

### 🔴 R26. 交易所 API Key 未硬化
`secret.yaml` 明文落盘（README 已提示仅本机），但**代码与文档都未强制**「API Key 关闭提现权限 + 绑定 IP 白名单 + 最小权限（仅现货/合约交易）」这一交易系统铁律。
- **影响**：Key 一旦泄漏且带提现权限，资金可被直接提走——这是比任何策略亏损都严重的单点。9.6 必须：密钥静态加密 / 启动期自检 Key 权限（拒绝带提现权限的 Key）/ 文档强制 IP 白名单。

### 🟠 R27. 无策略 edge 衰减 / regime 守卫
资金费套利的边际随竞争与市场状态衰减。当前除**事后** `reconcile` 外，无**活体**监控在 edge 真实衰减时自动降挡/暂停。
- **影响**：策略可能在「已经不赚钱」之后继续跑很久才被人发现。9.6 需要：滚动窗口实测净 edge 跟踪 + 低于阈值自动降档/停机 + 与灰度门联动。

### 🟡 R28. 无事故运行手册（runbook）与演练
缺少「断连 / 单腿裸露 / 熔断触发 / 交易所故障 / 密钥泄漏」等场景的人工 SOP 与定期演练。
- **影响**：真出事时操作者无章可循，处置延迟放大损失。9.6 需要：成文 runbook + 定期故障演练（含 `shadow`/小额下的撤离演练）。

---

> 以下 R29~R36 是「放大 + 滚动持有」语境下进一步暴露的盲区（部分是对前文的强化，已标注与现有覆盖的关系）。
> 每条标注其归属阶段（→ Sx，见第十八节），并已并入 9.6 判据（§18.4）。

### 🟠 R29. 借币容量与利率阶梯（负向 carry 放大后的硬约束）→ S4
负向 carry 借币做空现货，放大后会撞**可借量上限 / 利率阶梯跳变 / 借币失败 / 利率突变**。`MaxBorrowNotionalPerAssetUSDT`（§16.7）仅为设计草案，未实现；借币失败与利率突变也无专门处置。
- **影响**：可借量耗尽时开仓失败留下单腿；利率阶梯跳变使负向 carry 实际成本远超模型 → 看似盈利实则亏损。9.6 需：开仓前校验可借量与当前阶梯利率、借币失败降级为不开仓而非裸腿、利率突变触发该币退出。

### 🟡 R30. 组合相关性 / 簇上限（极端行情同向流动性蒸发）→ S4（基础件已落地·M-S4.8）
L2 簇上限在 §16.6 已设计；**M-S4.8 已落地基础件**：`admitByClusterCap` + `portfolioClusterNotionalUSDT` 已按 base 资产聚类硬约束单簇名义，并接入开仓侧 sizing（behind `ClusterMaxNotionalUSDT`，默认 0=关）。10 个币若全是同一类山寨 beta，极端行情会**一起流动性消失、一起反向**，对冲腿同时滑点放大。
- **影响**：名义上分散到 10 个币，实质是一个高相关大仓位。9.6 需：按 base 资产/板块/相关性聚类，`ClusterMaxNotionalUSDT` 硬约束单簇暴露。**待完善**：当前簇键=base 资产（≈每币一簇），真正的高相关防护需板块/相关性映射替换 `clusterKeyForSymbol`（唯一注入点）。

### 🟠 R31. 逐币 funding 时钟（不能继续靠全局 `:57`）→ S2
当前全局 `WakeMinute=57` 单点调度；不同合约 funding 间隔可能不同（8h/4h/可变）。§16.4/M2.2 已设计逐币时钟但未实现。
- **影响**：混合间隔组合下，全局唤醒会错过部分币的结算窗口或在错误时点动作。9.6 需：调度由每个币自身的下次 funding time 驱动。

### 🔴 R32. WebSocket / 订单回报断连盲窗（长持比单周期危险得多）→ S0/S5
`stream_common.go` 有 ping/keepalive 与 context 关闭，但**无「持仓期行情/订单回报失明超过阈值即自动降级/撤离」**。单周期持仓短、盲窗天然有界；滚动长持后，断连期间引擎对市场与成交是「瞎」的。
- **影响**：长持期间 user data stream / bookTicker 断连，引擎可能在失明状态下错过强平、错过单腿成交、用过期价格决策。9.6 需：失明时长守卫——超阈值即停止开新仓 + 降级为 REST 轮询 + 持续失明则保守撤离。

### 🟠 R33. 灰度验证门自动化（不能靠人工感觉）→ S6
§17.4/§18.3 的灰度门当前是人工判断。10k→30k→100k 的放行应由**实测 edge、滑点、成交率、对账健康**自动判定。
- **影响**：人工凭感觉放大，易在「实测已偏离模型」时仍然加码。9.6 需：灰度门为可量化规则（滚动窗口净 edge ≥ 阈值、滑点 ≤ 预算、成交率 ≥ 阈值、`reconcile` 连续 HEALTHY）自动放行/回退。

### ✅ R34. pending funding 卡住的处置（避免无限静默重试）→ S5（已修复·S5-E）
原 `funding_settlement_worker.go` 有重试 + 退避（封顶 4×）但**无终止路径**,会无限静默重试。**S5-E 已落地升级处置**:`shouldEscalatePendingFunding`(纯函数)——重试达 `MaxFundingSettlementAttempts`(默认 30) 或挂起超 `MaxFundingSettlementAge`(默认 12h) → `escalatePendingFundingSettlement`:发 `pending_funding_stuck` critical 告警(经 S5-A 出口)+ **cycle 隔离**(回退平仓时记入的日内净利,使该不完整周期不污染连亏/回撤)+ 标记 `Escalated` 停止自动重试,待人工核实。
- **影响**：资金费迟迟不到账（交易所延迟/口径异常）时会无限静默重试，净利长期挂在 `FundingIncomePending`、风控口径失真。9.6 需：超时升级为 critical 告警 + 人工确认流程 + 该 cycle 隔离不污染连亏/回撤计算。

### ✅ R35. 运行时改参数治理（人本身会成为事故源）→ S1/S5（已补齐 diff 预览 + 回滚·S5-F）
原有 `auditControlAction` + `controlToken` + loopback + 收紧-only 校验。**S5-F 已补**:`PreviewRiskConfigUpdate`(干跑,反射出字段级 before/after `RuntimeConfigFieldDiff`,经 `/api/runtime/config/preview`)+ `RollbackRiskConfig`(改参前快照历史栈,一键回滚到上一版,`/api/runtime/config/rollback`,带 audit)。校验逻辑抽到共用 `buildValidatedRiskUpdate`,保证"预览能过"≡"提交能过"。
- **影响**：运行中手改风控参数若改错（如把熔断阈值改成 0），等于自废武功且难追溯。9.6 需：改参前展示 diff、敏感参数二次确认/权限分级、变更可回滚、全程审计。

### 🟠 R36. 单交易所 / 单账户系统性单点 → S7
全系统绑定单一 Binance 账户：交易所 API 故障、风控规则突变、账户被风控/冻结、稳定币（USDT/FDUSD）脱锚、平台政策限制都是系统性单点。
- **影响**：任一发生都可能让整个组合瞬间无法管理。9.6（理论上限）需：venue/account 抽象层为多账户多所留口、稳定币敞口分散、单账户硬上限、账户异常自动只读+告警。注：完全消除单点超出单体原型范围，至少应做到「单点故障时能安全停机而非裸奔」。

---

## 十三、实盘前 Checklist（已按现实修订）
- [ ] 确认 `secret.yaml` 只在本机存在，且权限正确
- [ ] 确认 `DefaultEngineConfig()` 或运行时风控参数已按资金规模调整
- [ ] **确认是否需要并已开启 `HedgeImbalanceAbortUSDT/Rate`（默认关闭，见 R1）**
- [ ] **知悉「开仓前爆仓距离校验」尚未实现，仅有保证金充足性检查（R2）**
- [ ] **知悉 panic-flatten 不覆盖现货腿、无单腿强平检测（R3）**
- [ ] 确认 Dashboard 只监听本机地址
- [ ] **跑通 `make race`，确认无数据竞争告警（当前预期会命中 R4）**
- [ ] 确认 `go test`、`go vet`、`go build` 可通过
- [ ] 确认 `records/` 清理策略符合本次实验需求
- [ ] 确认没有误提交运行产物、缓存和日志
- [ ] 先用 `shadow` 跑若干完整周期抓灾难性 bug，再小额实盘
- [ ] 小额实盘跑出若干完整周期后，用 `reconcile` 核对（并知悉 R6 的成本侧盲区），再考虑放大档位

---

## 十四、通往 9.6 分的优化路线图

> 现状评分 ≈ **6.8 / 10**。代码工程素养是 A 级，但「安全姿态」与「文档 = 现实」是短板。下列任务按优先级排列，闭环后目标 **9.6 / 10**。

### P0 — 让现实追上承诺（必须，先做）
1. **开启并配置 Gate 4 对冲偏离守卫（修 R1）**
   - 给 `HedgeImbalanceAbortUSDT/Rate` 设非零默认值（如绝对 30 USDT 或相对 2%），并在 `config_loader.go` 暴露 YAML key。
2. **修 `serverTimeOffset` 数据竞争（修 R4）**
   - 改 `atomic.Int64`（存纳秒）或加专用 `serverTimeMu`；同步修 `DepthCache.refCount` 锁。
   - 跑 `make race` 直到全绿，并新增针对性 `-race` 测试。
3. **实现「开仓前爆仓距离校验」或从文档删除该承诺（修 R2）**
   - 推荐实现：用 `markPrice` + 维持保证金率算强平价，要求强平距离 ≥ 可配置阈值（如 ≥ 8%）才放行。

### P1 — 风控覆盖完整化
4. **panic-flatten 纳入现货腿 + 单腿强平/ADL 检测（修 R3）**
   - 急停判据改为「现货腿 + 合约腿合并的真实净敞口浮亏」；监听用户数据流的强平/ADL 事件，单腿归零立即触发对侧 flatten。
5. **运行时对冲不变量从「仅开仓」扩展到「持有/监控/平仓全程」（修 R1 延伸）**
   - 在监控循环周期性调用 `assertHedgeInvariant`。

### P2 — 盈亏与对账诚实化
6. **`NetPnL` 纳入滑点与恢复摩擦（修 R5）**
7. **对账成本侧用模型值而非实测值（修 R6）**
   - 模型预期侧用「模型费率/借币率」算 `totalFees/totalBorrow`，让成本模型误差暴露；并引入相对阈值（`Rel > 0`）。
8. **周期/日级亏损熔断改用 funding-settled final 口径（修 R8）**，与连亏维度对齐。
9. **资金费估计在符号冲突时取保守 0 或拒开，而非退回 `lastFundingRate`（修 R7）**。
10. **连亏维度补跨日累计视图（修 R9）**。

### P3 — 架构与可维护性
11. **拆薄 `funding_sniper_engine.go` / `engine_*.go` 巨型文件**，让状态机成为主流程骨架。
12. **配置与代码分离（修 R11）**：引入 `config.yaml` + 环境变量覆盖 + 启动期校验。
13. **`runBoundedConcurrentTasks` 首错即 cancel 兄弟任务（修 R12）**（`errgroup` + ctx 取消）。
14. **抽象存储接口**，为 JSONL / SQLite 演进做准备。
15. **给 `RiskEvent` 增加更丰富的上下文**，扩大业务流程级与 `-race` 测试覆盖。

### P4 — 规模化与高可用（放大资金前的前置条件）
> 这一层不是为「跑通」服务，而是为「放大档位 / 长期托管」服务。在解决前，建议把档位锁在小额（当前默认）。
16. **执行算法升级（修 R13）**：引入 TWAP/VWAP 时间切片与冰山委托；开仓腿在容量允许时优先被动挂单、超时再转 IOC 吃单；把执行算法做成可按名义规模自动切换的策略。
17. **冲击成本纳入决策与核算**：开仓前用订单簿深度估算预期冲击成本并计入进场闸；事后把实际冲击/滑点计入 `NetPnL`（与 P2 第 6 项联动），让规模化损耗可见。
18. **外部看门狗 + 交易所 Kill Switch（修 R14）**：独立进程/外部监控心跳，进程失联即触发交易所侧自动撤单/平仓保护；评估 Binance 提供的到期撤单（auto-cancel）等原生 Kill Switch。
19. **多节点热备（Hot Standby）（修 R14）**：主备双节点 + 共享/复制的权威生命周期账本（`live_exposure_rescue_tasks.json` 等），主节点失联时备节点接管收敛敞口；明确「同一时刻只有一个节点能下单」的租约机制，杜绝双开。
20. **L3 级 Tick 数据回测框架（修 R15）**：引入订单簿级（L3）回测，模拟排队位置、撮合延迟与踩踏窗口的真实滑点，校正纸面阈值的系统性乐观偏差。

### P5 — 交付与治理门槛（非功能性 9.6 门槛）
> 这一层不影响策略赚钱，但**直接决定外部评审能否给到 9.6**：能力凭证、配置、恢复、存储、交付必须达到生产级治理水准。
21. **能力凭证治理（修 R16）**：`controlToken` 不再明文打印（改为打印哈希指纹或写入受限文件）；读面与控制面授权模型彻底分离（读面可选独立只读令牌）；支持显式关闭 `/records/` 静态暴露。
22. **配置治理（修 R17 / R24）**：换成真正的 YAML/TOML/JSON 配置库；配置结构体自动映射（消灭 `envConfigOverrides`/`applyEngineConfigOverrides` 人工白名单）；引入配置 schema、默认值来源标注、启动时完整 diff 输出（实际生效值 vs 默认值）。
23. **恢复治理（修 R18 / R22）**：给 open hedge、exposure rescue、daily risk 状态增加 `schema_version` / `state_version` / `written_by_version` + 校验和；恢复逻辑显式区分「可自动恢复的旧状态」与「必须人工确认的未知/不兼容状态」，后者拒绝自动接管。
24. **存储治理（修 R19）**：结算与 pending 状态从「整体 JSON 重写」升级为事务化存储或 append-only 事件流 + 物化视图；把「审计记录（事实源）」与「当前状态（视图）」彻底分层，不再让一个文件同时承担两种职责。
25. **编排与交付治理（修 R20）**：`Run()` 内的 `log.Fatalf` 改为返回 `error`，退出策略交给 `main`/supervisor，使编排层可宿主化、可集成测试、可被进程治理接管；并建立「`main`/`build`/CI/README 始终对齐」的发布检查（每次发布验证 `go build ./... && make build` 且 README 能力清单与代码一一对应）。

### 验收标准（达到 9.6 的判据）
- README 中每一条能力都能在代码中找到对应实现，零「文档承诺 > 代码现实」缺口。
- 所有账户级风控默认开启且有合理默认值，无「默认关闭的关键守卫」。
- `make race` 全绿；新增覆盖单腿强平、对冲偏离、时间偏移并发的回归测试。
- 对账能真实反映成本侧模型误差，且对小额实盘跑出的 HEALTHY 结论可信。
- panic-flatten 覆盖双腿，存在单腿强平/ADL 检测与自动收敛。
- 放大档位前：执行侧具备平滑进出能力（TWAP/VWAP/Iceberg），存在外部看门狗 + 交易所 Kill Switch，且回测以 L3 数据校正过阈值。
- 治理达标：日志无明文凭证、读/写授权分离、配置真语义 + 自动映射、运行态文件带版本与校验、关键状态事务化/事件化、编排层无 `log.Fatalf` 且 main/build/CI/README 对齐。

---

## 十五、后续优先方向（汇总）
- 拆薄 `funding_sniper_engine.go`
- 让状态迁移彻底成为主流程骨架
- 给 `RiskEvent` 增加更丰富的上下文
- 扩大业务流程级测试
- 抽象存储接口，为 JSONL / SQLite 演进做准备
- 落地「通往 9.6 分的优化路线图」P0~P3（先闭环现实=承诺）
- 放大资金前落地 P4（执行算法 / 外部看门狗 + Kill Switch / 多节点热备 / L3 回测）
- 冲刺 9.6 评审落地 P5 治理门槛（凭证 / 配置 / 恢复 / 存储 / 交付）
- 落地「组合滚动持有 + 动态仓位管理」设计（见第十六节，重构主线）

---

## 十六、组合滚动持有与动态仓位管理设计（待实现，重构主线）

> 本节是后续重构/优化的实现依据。目标：把系统从「单周期打一枪就走 + 固定单币上限」升级为
> 「跨周期滚动持有的资金费 carry 组合 + 市场驱动的动态仓位管理」。
> 标注 `[现状]` 的是当前代码事实，`[目标]` 是要实现的行为，`[依赖]` 是必须先具备的前置条件。

### 16.1 现状基线（为什么要重构）
- **`[现状]` 实盘是严格单周期模型**：`close_protocol.go` 在资金费结算后 `ExitDelayAfterFunding`（默认 2s）即
  `setCurrentPositions(nil)` 并 `clearPersistedOpenHedges()`，持仓不跨周期存活。`MaxFundingCyclesHeld`
  仅存在于回测/历史配置（`historical_config.go`），实盘路径无滚动持有概念。
- **`[现状]` 单币 sizing 是静态的**：`engine_cost_guard.go:32`
  `rawOrderQty = (PerSymbolQuoteUSDT × PerSymbolQuoteMultiplier) / MarkPrice`，
  其中 `PerSymbolQuoteUSDT=100`、`PerSymbolQuoteMultiplier` 来自写死的 `GetSymbolRiskProfile`，
  再被 `MaxCycleOpenNotionalUSDT=300` 截断。
- **`[现状]` 市场信号只当过滤器**：`MaxRecentVolumeShare`/`MaxOpenInterestShare`/`MaxVisibleDepthShare`/
  `DepthLevels`（`defaults.go:54-57`）目前只用于「超标即拒」，未用于反推「该下多大」。
- **`[现状]` 无跨周期组合级上限**：当前只有每周期 `MaxCycleOpenNotionalUSDT` 与每日 `MaxDailyOpenNotionalUSDT`，
  没有「同时持有 ≤ N 币、总名义 ≤ X USDT」的常驻组合约束（因为持仓不跨周期）。

### 16.2 目标模型（一句话）
**一个常驻的、上限 ≤ 10 个币 / ≤ 100,000 USDT（10W，最终目标）总名义的资金费 carry 组合；每个币按其
自身资金费时钟在结算前复核「下一周期是否仍盈利」，盈利则滚动续持（省下往返成本），不盈利则果断平仓；
空出的槽位在找币流程中按市场容量动态定 size 后补入。**

#### 16.2.1 核心原则：架构一次到位（面向 10W），资金灰度投放（由验证门控制）
> 这是本重构的**最高指导原则**，避免「小资金专用版以后重写」与「第一天就上 10W 真金」两个极端。

- **架构层面：按 10W 一次设计到位。** 所有抽象（容量驱动的动态 size、执行切片、组合管理、HA、风控）
  都按 10W 标准建。**关键是 sizing 从第一天就是 capacity-driven（由市场深度/OI 反推），绝不写死小额**——
  这样扩容是「拧旋钮」而非「重写」，从根上消除「小资金假设硬编码」的后患。
- **资金层面：必须灰度爬坡（如 10k → 30k → 100k），每档由验证门放行。** 真实滑点、成交率、真实资金费、
  竞争只能用真钱在真市场量到（shadow/回测系统性偏乐观，见 R15）。未量到真实执行质量前直接上 10W，
  亏的是「模型误差」的钱，与架构成熟度无关。这是风险管理，不是架构妥协。
- **结论**：`MaxPortfolioNotionalUSDT` 等限额按 10W 设计、可一行配置调高；但**实际投放档位由灰度计划与
  验证门（经济回归 HEALTHY、执行质量达标、HA 就绪）控制**，二者解耦。

#### 16.2.2 10W 目标带来的硬约束（比小档更严苛）
10W / ≤10 币 ⇒ 单币均值约 1 万 U，在 `:57` 抢费窗口对许多 alt 属于大单。这把以下几项从「晚期可选」
升级为「资金放大前的硬门」：
- **执行切片（TWAP/冰山）+ 容量定 size 成为核心**（不可留到最后；否则冲击成本吃掉薄边际 0.0055）。
- **币种宇宙收缩**：`MaxOpenInterestShare(0.0005)` + 深度份额 ⇒ 能吃下 1 万/腿的币不多，10 槽可能凑不满；
  要么放宽币种、要么单币更集中 → **L2 簇上限 / 流动性分层更重要**。
- **负向 carry 借币容量**：10W 级别借币做空现货会撞保证金账户的**可借量上限 / 利率阶梯** →
  新增「借币容量与利率阶梯」约束（`MaxBorrowNotionalPerAssetUSDT` 等）。
- **保证金与强平**：100k 名义 2x 全仓，账户权益 vs 维持保证金缓冲需重算 → **R2 爆仓距离校验从「应该有」变「必须有」**。
- **HA**：100k 裸奔代价巨大 → **R14 看门狗 + 交易所 Kill Switch 提前为资金放大前的硬门**。

### 16.3 动态单币上限（想法 1）
注入点：把 `engine_cost_guard.go:32` 的静态 `PerSymbolQuoteMultiplier` 替换为市场计算的容量。

**多重天花板取最小 + 边际择优加权：**

```
perSymbolCap = min(
    depthCap   = MaxVisibleDepthShare × 可见深度名义,   // 别打穿盘口（需重度打折，见坑 1）
    volumeCap  = MaxRecentVolumeShare × 近窗成交名义,   // 别成为市场主力
    oiCap      = MaxOpenInterestShare × 未平仓名义,      // 合约腿容量
    liqSafeCap = 由强平距离反推的安全名义,               // 依赖 R2 的爆仓距离校验
    hardCapUSDT                                          // 绝对硬顶（尾部风险封顶）
)
edgeScore     = clamp(预期净资金费 / 入场阈值, edgeMin, edgeMax)   // 类分数 Kelly
finalNotional = clamp(baseUnit × edgeScore, minNotional, perSymbolCap)
```

**实现要点：**
1. `[目标]` 把上述份额阈值从「过滤器」复用为「容量推导输入」，新增一个 `resolveDynamicPerSymbolCap(target)` 纯函数（可单测、无网络）。
2. `[目标]` size 决策前先估算并扣除**冲击成本**：以「边际资金费收入 = 边际冲击成本」为停止加仓点。
3. `[依赖]` 冲击成本必须计入进场闸与 `NetPnL`（对应 R5：当前滑点不入账；P2 第 6 项）。

> **实现状态（M-S4.6 已落地纯函数）**：`dynamic_sizing.go:resolveDynamicPerSymbolCap` 已实现「多重天花板取最小
> （depth/volume/oi/liqSafe/hardCap）+ 边际择优夹取（edgeScore=clamp(预期净费率/入场阈值,EdgeMin,EdgeMax)）」,
> 含 :57 踩踏窗深度打折（坑1,默认 0.5）、绝对硬顶推导 `resolveHardCapUSDT`、`BindingCeiling` 可观测,以及
> fail-safe（无天花板或容量低于最小单 → 不可交易）。**接入开仓侧**（替换静态 `PerSymbolQuoteMultiplier`）须配合
> **冲击成本入闸/计入 NetPnL（M-S4.7/R5）**与 **L2 簇上限/L3 sizing（M-S4.8）**,且全程 behind `DynamicSizingEnabled`;
> `liqSafeCap` 依赖 R2 强平距离反推（当前为可选输入,缺省即跳过）。
>
> **冲击成本入闸（M-S4.7 已落地·M3.2）**：`roundTripImpactReserveRate`(复用 `strategy.EstimateMarketImpactRate`,
> 现货+合约各开/平共 4 次冲击)+ `impactAdjustedNetProfitRate` 已接入监控入场闸 `checkTargetCost`:**`DynamicSizingEnabled`
> 启用时**把往返市场冲击从净利率扣除(坑2:不扣冲击就放大 size 只会放大隐性亏损),默认关闭对入场闸零行为变更;
> 流动性基数复用监控期已算的可见深度名义,**不在热循环加网络调用**。冲击储备经 `LastImpactReserveRate/USDT` 可观测。
> **R5「滑点计入 NetPnL」经对账口径审查审定为误判(S4 收尾)**:`TradingPnL` 用**实际成交价**计算,滑点**已隐含在
> 权威 NetPnL 内**;恢复摩擦真实成本亦已折进平仓名义与 Fee 字段,且 `RecoveryFrictionUSDT` 结构性恒 0。**真正漏算项:无**。
> M-S3.0 的 `NetPnLAfterFrictions = NetPnL + 滑点` 属**重复计数**,已重定语义为「零滑点理想净利基准」=`NetPnL − Σ滑点`,
> `gap` 为执行拖累 KPI(诊断专用、不碰熔断)。详见上文 R5 条目。
>
> **L2 簇上限 + L3 sizing 接入（M-S4.8 已落地·M3.3）**：① **L2 簇上限**——`clusterKeyForSymbol`(按现货 base 资产聚类,
> 唯一注入点,后续可换板块/相关性映射)+ `admitByClusterCap`(`ClusterMaxNotionalUSDT<=0` 不启用)+
> `portfolioManager.portfolioClusterNotionalUSDT`(按簇汇总常驻组合合约腿名义,补 R30 缺口)。② **L3 sizing 接入开仓侧**——
> `constrainOrderQuantityByMarketCapacity` 在 **`DynamicSizingEnabled` 启用时**于既有 L0 实时容量数量之上,用
> `resolveDynamicPerSymbolCap`(L1:深度/成交/OI/硬顶 + 边际择优 + :57 深度打折)→ `constrainedSizingNotionalUSDT`
> 叠加 L2 簇余量、L3 组合名义余量收紧,最后**与 L0 数量取 min**。**仅收紧、绝不放大**(money-safe 首接:开关启用时
> 下单量只会 ≤ 静态路径;edge 放大尾部留待小档实盘校准后另开),L2/L3 无余量到最小单则归零跳过。L2/L3 状态经
> `residentSizingLimitsSnapshot` 单次取 `platformRiskMu` 读取避免漂移,收紧原因经 `LastDynamicSizingReason` 可观测。
> 默认 `DynamicSizingEnabled=false`、`ClusterMaxNotionalUSDT=0`,对静态开仓路径零行为变更。

**坑（务必规避）：**
- **坑 1：深度 ≠ 容量。** `:57` 抢费窗口是踩踏窗口，`:55` 看到的盘口到 `:57` 会蒸发（R13/R15）。
  depth 推出的容量要重度打折，大单必须切片执行（TWAP/冰山，P4 #16）。
- **坑 2：放大即冲击。** 不先扣冲击成本就放大 size，只会放大隐性亏损（薄边际 0.0055 被非线性吃掉）。
- **坑 3：强平安全要随 size 放大。** `liqSafeCap` 必须真实存在——而当前「开仓前爆仓距离校验」不存在（R2）。

### 16.4 滚动持有与逐币资金费时钟调度（想法 2）
**`[目标]` 调度从「全局 `:57` 单点」改为「事件驱动 + 逐币资金费时钟」：**
- 组合里每个币按**它自己的下次 funding time** 触发复核，而非一个全局 `:55` 定时器
  （⚠️ Binance 不同合约结算间隔不同：8h / 4h / 部分可变，混合组合各币结算时刻不同步）。
- 对每个币，在其 `funding_time − RevalidateLead`（建议 5min，即你说的 `:55`）执行 **keep/drop 复核**；
  在其 `funding_time − OpenPreparationLead`（即 `:57`）执行新仓补位。

**续持判定阈值 ≠ 入场阈值（省钱的核心）：**
- 新开：必须覆盖**完整往返** ≈ `FeeReserveRate(0.0030) + ExitSpreadReserveRate + MinNetProfitRate ≈ 0.0055`。
- 续持：只需覆盖**多持一周期的边际成本** ≈ 借币利息（负向 carry）+ 持有期对冲漂移风险（正向 carry≈0）。
- `[目标]` 新增 `HoldRollThresholdRate`（续持门槛，远低于入场门槛）与 `RollSafetyBufferRate`（安全垫）：
  仅当「下期保守预期净资金费 > HoldRollThreshold 且明显为正（> buffer）」才续持，贴近 0 即果断平。
- `[依赖]` 续持预测用**保守估计**，规避 R7（premium/last rate 符号冲突时退回乐观 `lastFundingRate`）。

### 16.5 组合再平衡：滞后带 keep/drop/add（防 churn）
每个结算边界把「已持仓（无入场成本）」与「新候选（含入场往返成本）」放进同一排序，但**必须加滞后带**，
否则会被手续费来回摩擦磨光利润：

```
对每个新候选 c 与可能被替换的持仓 h：
  仅当 netCarry(c) − netCarry(h) > 入场往返成本 + 退出往返成本   // 滞后带
  才执行 drop(h) + add(c)；否则保持 h 不动。
被 drop / 自然到期不续持的仓位释放槽位 → :57 找币上限 = MaxPortfolioSymbols − 续持数。
```

`[目标]` 抽象出纯函数 `rebalancePortfolio(held, candidates, costs, limits) -> {keep, drop, add}`，可单测、确定性。

> **实现状态（S4 c-iii-4 已落地）**：`:57` 开仓补位侧已接入 `rebalancePortfolio`，在 L3 币数(`MaxPortfolioSymbols`)+
> 名义(`MaxPortfolioNotionalUSDT`)双重上限内按净 carry 择优**填空槽**（`plan.Add` 受双上限封顶，已修复「补位可能越过 L3
> 币数」的缺口）。当前阶段滞后带取 `+Inf` → **已持常驻仓在补位阶段绝不被换出**（churn 抑制最强档），持仓的 keep/drop
> 仅由到期复核 `evaluateRollDecision` 决定。**「常驻仓被更优新候选主动换出」(plan.Drop) 属独立特性**，因其会关闭一个刚被判
> keep 的仓位、收益需先量化，故经济性待 **M-S4.5 单周期 vs 滚动续持回归**验证后另设里程碑接入，本步不做。全部 behind
> `EnableRollingHold`，默认关闭对单周期路径零影响。

### 16.6 分层限额模型（仓位管理总框架）

| 层级 | 含义 | 落点 / 依赖 |
|---|---|---|
| **L0 单笔执行上限** | 别打穿盘口（depth/volume/OI 份额） | 16.3；扩展 `defaults.go:54-57` 份额闸 |
| **L1 单币上限** | `min(L0容量, 绝对硬顶, 强平安全)` | 16.3；✅ M-S4.8 接入开仓侧（behind `DynamicSizingEnabled`，仅收紧）；`liqSafeCap` 待 R2 |
| **L2 相关性/簇上限** | 别把大额砸进高相关山寨簇 | ✅ M-S4.8（`clusterKeyForSymbol`/`admitByClusterCap`，按 base 资产聚类；待板块/相关性映射） |
| **L3 组合上限** | 同时 ≤ `MaxPortfolioSymbols`(10) 且 ≤ `MaxPortfolioNotionalUSDT`(100000) | ✅ 常驻/跨周期 L3 已接入补位(c-iii-4)与 sizing(M-S4.8) |
| **L4 滚动再平衡** | 每个币的结算边界 keep/drop/add + 滞后带 | 16.4 / 16.5 |

**分配策略：** 10 个槽位**按净 carry 边际择优加权**（不等权），但受 L1 单币上限与 L2 簇上限约束防集中。

### 16.7 新增配置项（✅ 已落地为 `core.PortfolioConfig`，默认全关闭/零行为变更；纳入真 YAML + 自动映射，见 R17/R24）
> 架构上限按 10W 设计；「当前投放档」由 `MaxPortfolioNotionalUSDT` 这一个旋钮随灰度计划调高（见 16.2.1）。
> 「灰度起步值」列是首次实盘验证的建议起点，不是架构上限。

| 配置项 | 含义 | 架构上限（10W） / 灰度起步值 |
|---|---|---|
| `MaxPortfolioSymbols` | 组合最大同时持仓币数 | 10 |
| `MaxPortfolioNotionalUSDT` | 组合最大总名义（**灰度旋钮**） | 上限 100000 / 起步 10000 |
| `EnableRollingHold` | 是否启用跨周期滚动持有 | false（默认关闭，opt-in） |
| `RevalidateLead` | 结算前复核提前量（`:55`） | 5min |
| `HoldRollThresholdRate` | 续持门槛费率（< 入场门槛） | 与边际持有成本自洽，待标定 |
| `RollSafetyBufferRate` | 续持安全垫 | 待标定 |
| `RebalanceHysteresisCostMultiplier` | 换仓滞后带（往返成本倍数） | ≥ 1.0 |
| `DynamicSizingEnabled` | 启用市场驱动动态 size | false（默认关闭，opt-in） |
| `PerSymbolHardCapUSDT` | 单币绝对硬顶 | 按 `MaxPortfolioNotionalUSDT / MaxPortfolioSymbols` 起步 |
| `ClusterMaxNotionalUSDT` | 单簇上限（L2） | 待标定 |
| `MaxBorrowNotionalPerAssetUSDT` | 负向 carry 单资产借币名义上限（撞可借量/利率阶梯前封顶） | 待标定（按账户可借量与利率阶梯） |
| `CapitalRampSchedule` | 灰度爬坡计划（如 `10000,30000,100000`） | 由验证门逐档放行 |

### 16.8 状态、恢复与风控耦合（滚动持有把这些从「可选」变「必选」）
- `[依赖] R1`：持有多周期后两腿名义会漂移，**必须开启对冲偏离运行时守卫**（`HedgeImbalanceAbort*`，当前默认关闭），
  且从「仅开仓校验」扩展到「持有期周期性校验」。
- `[依赖] R3`：panic-flatten 必须纳入现货腿 + 单腿强平/ADL 检测（长持期间单腿被打掉的概率随持有时长上升）。
- `[依赖] R-borrow`：负向 carry 长持时借币利息连续累积，roll 判定必须扣**真实跨周期借币**（非最小计费小时）。
- `[依赖] R18`：未平仓台账从「平仓即清空」变为**长期存活的权威组合**，必须带 `schema_version` + 校验和，跨重启恢复。
- `[依赖] R14`：进程崩溃 mid-hold 会留下真实多仓裸奔，外部看门狗 + 交易所 Kill Switch 的重要性显著上升。

### 16.9 分阶段落地顺序（建议）
> 架构按 10W 一次建到位；资金灰度 `10k → 30k → 100k` 由验证门放行（见 16.2.1）。
1. **地基（先修风控）**：R1（对冲偏离运行时守卫）+ R3（panic 纳入现货腿）+ R2（爆仓距离校验，10W 必须）——滚动持有的安全前提。
2. **滚动调度**：逐币 funding clock + keep/drop/add 滞后带再平衡 + L3 组合上限（按 10W 设计）；
   `EnableRollingHold` 默认关闭、opt-in；首档灰度起步 `MaxPortfolioNotionalUSDT=10000` 验证逻辑。
3. **动态 size（capacity-driven，从第一天起不写死小额）**：L0/L1 容量定 size + 冲击成本入闸 + 借币容量约束。
4. **执行切片**：TWAP/冰山（P4 #16）——10W 级别的资金放大硬门，必须先就位。
5. **状态/恢复硬化 + HA**：R18 台账版本契约 + R14 崩溃恢复/看门狗/Kill Switch（已有真实长存组合，放大前的硬门）。
6. **逐档放大**：每过一道验证门（经济回归 HEALTHY + 执行质量达标 + HA 就绪）才把 `MaxPortfolioNotionalUSDT` 调高一档。

### 16.10 验收与测试要点
- 纯函数 `resolveDynamicPerSymbolCap` / `rebalancePortfolio` / 续持判定，全部确定性单测（无网络）。
- 滞后带有效性测试：构造「新候选仅略优于持仓」场景，断言**不发生换仓**（防 churn 回归）。
- 逐币时钟测试：混合 8h/4h 间隔的组合，断言各币按各自 funding time 触发复核。
- L3 上限测试：续持 + 新开合计触顶时，断言新仓被拒且不破坏已持仓。
- 恢复测试：滚动持有期模拟重启，断言组合台账（含版本）正确恢复且对冲不变量成立。
- ✅ 经济回归(M-S4.5 已落地)：`rolling_economics.go` 在同一 funding 费率序列上对比「单周期」vs「滚动续持」累计净利,
  复用实盘同一条 `evaluateRollDecision` 判据 + 真实往返成本常数,确定性纯函数仿真。`rolling_economics_test.go` 钉死:
  ① 全程盈利时滚动净利优势 = 省下的 (N−1) 次往返成本(可精确对账);② 滚动在「续持有利但不值得新开」的费率带
  多吃 funding;③ 费率转差时滚动果断平仓、绝不劣于单周期;④ 负向 carry 借币正确计入;⑤ 多币组合汇总。

---

## 十七、重构线路图（执行顺序）

> 本节把第十四节（风险/治理路线图 P0~P5）与第十六节（滚动持有 + 动态仓位设计）编织成**一条有依赖
> 顺序、每步可独立交付的执行路径**。原则：**安全先于价值、结构先于功能、每步可回滚、每步绿。**

### 17.0 贯穿全程的硬约束（每个里程碑都适用）
- **每个 PR 必须**：`make fmt && make vet && make test && make race && make build` 全绿；新增逻辑配确定性单测。
- **新行为一律 opt-in**：`EnableRollingHold` / `DynamicSizingEnabled` 等默认 `false`，灰度开启、可一键回滚。
- **小步快跑**：一个里程碑 = 一个可评审 PR；不把「改结构」和「改行为」混在同一个 PR。
- **放大前先验证经济性**：任何提高资金档位的动作，前置「经济回归测试」通过（§16.10 最后一条）。
- **架构按 10W 一次到位，资金灰度爬坡**（见 16.2.1）：代码/限额/执行/HA 全部按 10W 标准建，且 sizing 从第一天
  起 capacity-driven、不写死小额；实际投放档位由 `MaxPortfolioNotionalUSDT` 旋钮随灰度计划（如 10k→30k→100k）
  调高，每档由验证门放行。**「灰度」是资金投放策略，不是把架构做成小资金版。**

### 17.1 里程碑总览

| 阶段 | 里程碑 | 目标 | 修复/落地 | 依赖 |
|---|---|---|---|---|
| **P0 地基** | M0 | 止血 + 安全网（滚动持有的前提） | R4/R1/R3/R2 | — |
| **P1 基建** | M1 | 配置真语义 + 状态版本契约 | R17/R24/R18/R22 | M0 |
| **P2 解耦** | M2 | 从上帝对象切出组合管理 + 调度 | R10/R21/R20 | M1 |
| **P3 动态仓位** | M3 | 市场驱动单币 size + 冲击成本入账 | 想法1 / R5 | M2 |
| **P4 滚动持有** | M4 | 跨周期续持 + 滞后带再平衡 | 想法2 / R7 | M2、M0 |
| **P5 规模化/HA** | M5 | 执行切片 + 看门狗 + 热备 + L3 回测 | R13/R14/R15 | M3、M4 |
| **P6 治理收尾** | M6 | 凭证/存储/对账诊断/编排 | R16/R19/R23 | 并行可做 |

### 17.2 各里程碑详细

**M0 — 止血与安全网（必须最先，低风险高收益）**
- M0.1 修 `serverTimeOffset` 数据竞争（R4）+ `DepthCache.refCount` 竞争 → `make race` 全绿。
- M0.2 开启并接通对冲偏离运行时守卫（R1）：给 `HedgeImbalanceAbort*` 设非零默认 + 配置 key；把 `assertHedgeInvariant`
  从「仅开仓」扩展到「持有期周期性校验」。
- M0.3 panic-flatten 纳入现货腿 + 单腿强平/ADL 检测（R3）。
- M0.4 实现「开仓前爆仓距离校验」或从文档删除该承诺（R2）。
- **退出标准**：race 绿；对冲偏离守卫默认生效并有测试；急停覆盖双腿；R2 文档=现实。

**M1 — 配置与状态契约（新功能的使能基建）**
- M1.1 换真 YAML 库 + 结构体自动映射（R17/R24），消灭人工白名单——这样 §16.7 的新配置项「写进结构体即可配」。
- M1.2 给未平仓台账 / 残腿救援 / 日级风控加 `schema_version` + 校验和（R18/R22）；恢复逻辑区分「可自动恢复」与「需人工确认」。
- **退出标准**：所有 `EngineConfig` 字段可经 YAML/env 配置并有启动 diff 输出；状态文件带版本，跨版本不兼容时拒绝自动接管。

**M2 — 引擎解耦（让大重构可控）**
- M2.1 从 `FundingRateSniperEngine` 切出 `PortfolioManager`（R10/R21）：拥有「常驻持仓集合 + L3 组合上限 + 组合生命周期」，
  明确锁归属与落盘责任。
- M2.2 切出 `Scheduler`：由**逐币资金费时钟**驱动事件（取代全局 `:57` 单点），为 M4 铺路。
- M2.3（可选）`Run()` 去 `log.Fatalf`、改返回 `error`（R20），提升可测试性。
- **退出标准**：组合状态与调度有独立类型与单测；现有单周期行为在重构后**行为不变**（回归测试锁定）。

**M3 — 动态仓位（想法 1，behind `DynamicSizingEnabled`）**
- M3.1 纯函数 `resolveDynamicPerSymbolCap`（L0/L1 多重天花板取最小，§16.3），替换静态 `PerSymbolQuoteMultiplier`。
- M3.2 冲击成本估算入进场闸 + 计入 `NetPnL`（R5）。
- ✅ M3.3 L2 簇上限 + L3 组合上限接入 sizing（M-S4.8：`admitByClusterCap` + `constrainedSizingNotionalUSDT` 接开仓侧，仅收紧，behind `DynamicSizingEnabled`）。
- **退出标准**：纯函数全单测；开关关闭时行为=旧版；小档实盘冲击成本可见、可对账。

**M4 — 滚动持有（想法 2，behind `EnableRollingHold`）**
- M4.1 多周期持有：取消无条件平仓，按逐币 funding clock 在 `RevalidateLead`（:55）做 keep/drop 复核。
- M4.2 纯函数 `rebalancePortfolio`（滞后带 keep/drop/add，§16.5），防 churn。
- M4.3 续持门槛/安全垫 + 保守资金费估计（R7 耦合）；负向 carry 扣真实跨周期借币。
- **退出标准**：§16.10 全部测试通过；经济回归证明「滚动 vs 单周期」净利提升；小档跑满周期 + `reconcile` HEALTHY。

**M5 — 规模化与高可用（10W 资金放大前的硬门，非晚期可选）**
- M5.1 执行切片 TWAP/冰山（R13/P4 #16）；M5.2 外部看门狗 + 交易所 Kill Switch（R14）；
  M5.3 多节点热备 + 单写租约（R14）；M5.4 L3 Tick 回测（R15）。
- **退出标准**：10W 档位下冲击成本受控、物理断连有兜底、阈值经 L3 校正。

**M6 — 治理收尾（与上面并行，冲刺 9.6）**
- 凭证治理（R16）/ 存储事件化（R19）/ 对账诊断清洁（R23）/ 编排去 Fatalf（R20，若 M2.3 未做）。

### 17.3 依赖关系（关键路径）
```
M0(安全网) ─┬─> M2(解耦) ─┬─> M3(动态size) ─┐
            │             └─> M4(滚动持有) ─┼─> M5(规模化/HA)
M1(配置/状态)┘                              ┘
M6(治理) 可与 M2~M5 并行
```
- **不可逾越的红线**：M4（滚动持有）必须在 M0.2 + M0.3（对冲偏离守卫 + 双腿急停）之后；否则长持期间裸敞口无人看管。
- **M3 与 M4 可并行**（都依赖 M2），但**先 M4 后 M3**更稳：先用固定小 size 验证滚动逻辑，再叠加动态放大。
- **放大到 10W 必须等 M5 全部就位（执行切片 + HA）**，否则冲击成本吃掉薄边际、且 100k 裸奔无兜底。

### 17.4 面向 10W 的资金灰度与验证门
> 架构（M0~M6）按 10W 一次建到位；**资金投放与架构进度解耦**，由下列验证门逐档放行。

| 档位 | 前置门（必须全部满足） |
|---|---|
| **shadow** | M0 完成；`shadow` 模式跑满若干完整周期，无「下错方向/异常数量/失控循环」 |
| **数十~数百 U（首次真金，实际起步）** | M0 完成 + M4 滚动逻辑就位；`reconcile` 连续 HEALTHY；这是**校准真实滑点/成交率/资金费**的档位，亏得起学费 |
| **1k~5k** | M1~M3 就位（配置/状态契约 + 动态 capacity sizing + 冲击成本入账 R5）；数百 U 档实测净利与模型一致 |
| **1W~3W** | M5 执行切片 + 告警/可观测（R25）就位；借币容量校验通过；前档冲击成本符合预算 |
| **10W（最终目标）** | M5 全部就位（TWAP/冰山 + 看门狗 + Kill Switch + 热备 + L3 校正）+ edge 守卫（R27）；前档净利稳定符合模型 |

- 每档放行只改一个旋钮 `MaxPortfolioNotionalUSDT`，不改代码——这正是「架构一次到位」的兑现方式。
- 任一档位出现「实测净利显著低于模型 / 冲击成本超预算 / 对账 UNHEALTHY」即**回退上一档**并定位偏差，修正后再前进。
- ⚠️ **起步就是数十~数百 U**：在这个档位上，机会成本极低而信息价值极高——它的唯一使命是用真金把回测/shadow 的乐观偏差（R15）校准成真实参数，而不是赚钱。

---

## 十八、通往 9.6 的完整优化流程（总纲）

> 本节是**唯一的总纲**：把第十二节（R1~R28 风险）、第十四节（P0~P5）、第十六节（设计）、第十七节（M0~M6 线路）
> 收敛成一条端到端、有评分判据的优化流程。**「理论上达到 9.6」= 下列每个评分维度都做到位**；
> 真实是否到 9.6，由你的实盘记录（`reconcile` + 实测净利 vs 模型）最终裁定——代码只能保证「理论充分」，
> 不能替代「经验验证」。

### 18.1 评分基线与目标（审判视角）
> 现状总分 ≈ **6.0**（在原 8 维 6.8 基础上，计入运维/安全/容灾三个被忽略的维度后下修）。

| # | 评分维度 | 现状 | 9.6 目标 | 由哪些阶段达成 |
|---|---|---|---|---|
| 1 | 工程素养 / 代码质量 | 8.5 | 9.5 | S2（解耦）+ 全程绿 |
| 2 | 残腿 / 异常恢复 | 8.0 | 9.5 | S1（状态契约）+ M4 恢复测试 |
| 3 | 风控实现 | 5.0 | 9.5 | S0（爆仓距离/双腿急停/对冲守卫）|
| 4 | 风控默认配置 | 3.5 | 9.5 | S0（守卫默认开）+ S1（配置真语义）|
| 5 | 盈亏 / 对账正确性 | 5.5 | 9.5 | S3（滑点/冲击/借币入账）+ 对账成本侧修正 |
| 6 | 并发安全 | 6.0 | 9.5 | S0（race 清零）+ S2（解耦降复杂度）|
| 7 | 文档与代码一致性 | 4.0 | 9.6 | S0（现实=承诺）+ 每阶段同步 README |
| 8 | 策略经济性稳健度 | 6.0 | 9.5 | S4（滚动省往返）+ S6（edge 守卫）|
| 9 | 运维可观测性与告警 | 2.0 | 9.5 | S5（告警/指标/心跳，R25）|
| 10 | 安全与凭证治理 | 3.0 | 9.5 | S5（Key 硬化 R26 + 凭证治理 R16）|
| 11 | 高可用 / 容灾 | 2.0 | 9.5 | S7（看门狗/Kill Switch/热备 R14）|

### 18.2 端到端优化流程（S0 → S8）
> 每个阶段 = 目标 + 覆盖项（复用前文 R/M/P 编号，不重复展开）+ 退出标准 + 评分增量。**阶段间严格按依赖推进，每步全绿、可回滚。**

**S0 安全地基（现实=承诺）** —— 覆盖 M0 / R1·R2·R3·R4 / §14 P0~P1
- 修数据竞争 → `make race` 绿；开对冲偏离守卫并扩到持有期；panic 纳入现货腿 + 单腿强平/ADL 检测；实现爆仓距离校验。
- **退出**：无默认关闭的关键守卫；race 绿；README 无「承诺>现实」缺口。**评分↑：维度 3/4/6/7。**

**S1 配置与状态契约** —— 覆盖 M1 / R17·R18·R22·R24
- 真 YAML + 结构体自动映射（消灭白名单漂移）；运行态文件加 `schema_version`+校验和，恢复区分「可自动/需人工」。
- **退出**：所有字段可配 + 启动 diff；状态跨版本不兼容时拒绝自动接管。**评分↑：维度 2/4。**

**S2 引擎解耦** —— 覆盖 M2 / R10·R20·R21
- 切出 `PortfolioManager` + 逐币 `Scheduler`；`Run()` 去 `log.Fatalf` 改返回 error。
- **退出**：组合/调度独立可测；旧单周期行为回归不变。**评分↑：维度 1/6。**

**S3 盈亏诚实化** —— 覆盖 R5·R6·R7·R8·R9 / §14 P2
- 滑点+冲击成本+恢复摩擦入 `NetPnL`；对账成本侧用模型值并引入相对阈值；熔断口径统一 funding-settled；资金费保守估计。
- **退出**：对账能暴露成本侧模型误差；连亏/回撤口径与两阶段结算一致。**评分↑：维度 5/8。**

**S4 滚动持有 + 动态仓位** —— 覆盖 M3·M4 / §16 想法1·想法2
- capacity-driven 动态 size（从第一天不写死小额）+ 逐币 funding clock 滚动续持 + 滞后带再平衡（防 churn）+ L0~L3 限额。
- **退出**：§16.10 全测试通过；经济回归证明滚动>单周期。**评分↑：维度 8。**

**S5 可观测性与安全治理** —— 覆盖 R16·R25·R26 / §14 P5
- 关键事件实时告警（Telegram/钉钉）+ 指标导出 + 存活心跳；API Key 启动期权限自检（拒绝带提现权限）+ IP 白名单（文档强制）+ 密钥静态加密；Dashboard 凭证哈希化、读/写授权分离。
- **进度**：✅ S5-A 告警分发(`alerting.go`);✅ S5-B `/livez`+`/healthz`+`/metrics`+心跳文件(`observability_http.go`);✅ S5-C 启动期 API Key 权限自检(`verifyAPIKeyPermissions`:带提现权限拒绝启动,未开 IP 白名单告警);✅ S5-D 凭证卫生(控制令牌改写 0600 文件+只记指纹,secret 路径脱敏)。✅ S5-E R34 pending funding 升级处置;✅ S5-F R35 改参 diff 预览 + 一键回滚(`PreviewRiskConfigUpdate`/`RollbackRiskConfig`)。**S5 代码侧已全部完成。** 密钥静态加密 + IP 白名单(交易所侧)留实盘/基建侧。
- **退出**：熔断/裸腿/UNHEALTHY 会实时推送；带提现权限的 Key 被拒启动；日志无明文凭证。**评分↑：维度 9/10。**

**S6 策略健康守卫** —— 覆盖 R27
- 滚动窗口实测净 edge 跟踪，低于阈值自动降档/停机，并与灰度门联动。
- **退出**：edge 衰减到阈值下自动降挡，有测试。**评分↑：维度 8。**
- **进度**：✅ S6-A 滚动窗实测净 edge 跟踪器(`edge_tracker.go`:`realizedEdgeWindow`/`computeRealizedEdgeStats`/`decideEdgeHealth`,funding 实收后入窗 + 启动回填,behind `EdgeWindowCycles`>0,阈值 `MinRealizedNetEdgeRate` 默认 0=只观测)。✅ S6-B edge 衰减自动降档:`currentGearLocked` 在 edge 低于阈值时降到 `GearObserve`,`verifyPreOpenGoNoGo`→`checkGearAllowsOpen` 用 `GearAllowsNewOrders` 拦截开新仓(非致命、可自动恢复、已有持仓照常结算),并发去重告警。待:S6-C R33 灰度门自动 promote/rollback + `CapitalRampSchedule` 解析。

**S7 高可用与容灾** —— 覆盖 M5.2·M5.3 / R14
- 外部看门狗 + 交易所 Kill Switch（到期自动撤单）；多节点热备 + 单写租约（杜绝双开）。
- **退出**：进程被杀/断连有交易所侧兜底；主备切换不双开。**评分↑：维度 11。**

**S8 规模化执行与高保真回测** —— 覆盖 M5.1·M5.4 / R13·R15·R19·R23
- TWAP/冰山执行切片；L3 Tick 回测校正阈值；结算/pending 事件化存储；对账诊断字段清洁；事故 runbook + 演练（R28）。
- **退出**：大单冲击受控；纸面阈值经 L3 校正；有成文 runbook 与演练记录。**评分↑：维度 1/5/8。**

### 18.3 阶段→灰度联动
- S0 完成 → 可上 `shadow`。
- S0~S4 完成 → 可上**数十~数百 U**首次真金（校准真实参数）。
- S1~S5 完成 + 前档参数验证 → 爬到 **1k~5k**。
- S5~S7 完成 → 爬到 **1W~3W**。
- S0~S8 全完成 + edge 守卫 + 前档稳定 → 爬到 **10W**。
（与 §17.4 验证门一致：架构按阶段建到位，资金由实盘记录逐档放行。）

### 18.4 「理论 9.6」判据清单（self-check）

**基础（R1~R28）**
- [ ] 无默认关闭的关键风控；无「文档承诺 > 代码现实」缺口。
- [ ] `make race` 绿；关键并发路径有 `-race` 回归测试。
- [ ] `NetPnL` 含滑点/冲击/借币/恢复摩擦；对账能暴露成本侧模型误差。
- [ ] 滚动持有经济回归证明优于单周期；防 churn 测试通过。
- [ ] 动态 size 为 capacity-driven，可一旋钮从数百 U 调到 10W 不改代码。
- [ ] 熔断/裸腿/UNHEALTHY 实时告警；指标可导出；存活心跳。
- [ ] API Key 无提现权限 + IP 白名单；日志无明文凭证。
- [ ] edge 衰减自动降档；进程/断连有交易所侧 Kill Switch 兜底；主备不双开。
- [ ] 有成文事故 runbook 与定期演练。

**放大 + 滚动持有专项（R29~R36，本次补充）**
- [ ] **借币容量/利率阶梯（R29）**：开仓前校验可借量与当前阶梯利率；借币失败→不开仓（不留裸腿）；利率突变→该币退出。
- [ ] **簇/相关性上限（R30）**：按 base/板块/相关性聚类，`ClusterMaxNotionalUSDT` 硬约束单簇暴露，防 10 币同向。
- [ ] **逐币 funding 时钟（R31）**：调度由每个币自身下次 funding time 驱动，不再靠全局 `:57`，兼容 8h/4h/可变间隔。
- [ ] **断连盲窗守卫（R32）**：行情/订单回报失明超阈值→停开新仓 + 降级 REST 轮询 + 持续失明保守撤离。
- [ ] **灰度门自动化（R33）**：放行/回退由实测 edge、滑点、成交率、`reconcile` 健康按量化规则自动判定，非人工感觉。
- [ ] **pending funding 处置（R34）**：重试超 N 次/超 T 小时→critical 告警 + 人工确认；该 cycle 隔离不污染连亏/回撤。
- [ ] **改参治理（R35）**：热改风控有 diff 预览 + 权限分级/二次确认 + 一键回滚 + 全程审计。
- [ ] **单点容灾（R36）**：单交易所/账户故障时系统能安全停机而非裸奔；稳定币敞口分散；预留 venue/account 抽象口。

- [ ] **最终裁定**：连续多周期小额实盘 `reconcile` HEALTHY 且实测净利稳定贴合模型（这一条只能由你的实盘记录给出）。

### 18.5 一句话总结
**代码侧能做到的极限是「理论 9.6」——把 R1~R28 全闭环、S0~S8 全落地、每个评分维度都拉到 9.5±。
但最后那 0.1、那一锤定音的「真的赚钱且稳健」，只能由你数十~数百 U 起步、逐档放大的实盘记录来盖章。
这份总纲的作用，是确保当你的实盘数据说话时，系统已经没有任何「本可避免的结构性缺陷」在拖后腿。**
