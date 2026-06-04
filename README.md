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

### 🟠 R5. 净利 `NetPnL` 不含滑点与恢复摩擦
`NetPnL = 资金费 + 交易盈亏 − 费用 − 借币`，**滑点和恢复摩擦只进 attribution，不进 `NetPnL`**。恢复摩擦是实打实的逆向成本。**影响**：连亏/回撤熔断口径偏乐观。

### 🟠 R6. 对账会给亏钱策略盖「健康」章
`reconcile_command.go` 把实测手续费/借币成本抄到「模型预期」一侧，成本侧模型误差在对账里恒为 0；默认只有绝对阈值（`SettlementDiffAlertUSDT=3`），相对阈值=0，小名义下大比例泄漏能蒙混过 HEALTHY。**影响**：对账无法完成「验证钱按模型赚到」的核心使命。

### 🟠 R7. 资金费估计在符号冲突时退回乐观值
当 premium 与 last funding rate 符号不一致时，保守估计返回 0，估计值退回 `lastFundingRate`（可能过度乐观）。**影响**：在资金费率正要反转时（最危险的时刻）用旧方向费率开仓。

### 🟠 R8. 周期/日级亏损熔断在资金费到账前误触发
`MaxCycleLossUSDT` / `MaxDailyLossUSDT` 在平仓时用 pre-funding 的 `NetPnL` 判断（此时 `FundingIncomePending=true`）。对靠资金费翻正的策略会误停机（连亏维度已修为用 final，cycle/daily 未修）。**影响**：本该盈利的周期被误判停机（方向偏安全，但口径与两阶段结算未对齐）。

### 🟠 R9. 连亏计数 UTC 每日清零
连亏维度按 UTC 交易日清零，与 README「补跨日慢性失血」的意图存在缝隙：每天小亏、不触发回撤峰值的慢性失血可能被漏掉。

### 🟡 R10. 上帝对象与锁纪律靠人脑维持
`FundingRateSniperEngine` 背 6 把锁 + 多个 atomic + 9 个 `sync.Once`，多个 750+ 行巨型文件；按字段分锁（`platformRiskMu`/`orderRateMu` 守 `platformState` 不同字段）靠约定维持，新增字段易踩进 R4 类竞争。

### 🟡 R11. 配置与代码未分离
策略/风控参数硬编码，调参需改代码重新构建——实盘期间是事故源。

### 🟡 R12. 并发开仓首错不取消兄弟任务
`runBoundedConcurrentTasks` 首个 worker 失败不取消其余 worker，放大竞争窗口与潜在多腿暴露。

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

### 验收标准（达到 9.6 的判据）
- README 中每一条能力都能在代码中找到对应实现，零「文档承诺 > 代码现实」缺口。
- 所有账户级风控默认开启且有合理默认值，无「默认关闭的关键守卫」。
- `make race` 全绿；新增覆盖单腿强平、对冲偏离、时间偏移并发的回归测试。
- 对账能真实反映成本侧模型误差，且对小额实盘跑出的 HEALTHY 结论可信。
- panic-flatten 覆盖双腿，存在单腿强平/ADL 检测与自动收敛。

---

## 十五、后续优先方向（汇总）
- 拆薄 `funding_sniper_engine.go`
- 让状态迁移彻底成为主流程骨架
- 给 `RiskEvent` 增加更丰富的上下文
- 扩大业务流程级测试
- 抽象存储接口，为 JSONL / SQLite 演进做准备
- 落地「通往 9.6 分的优化路线图」P0~P3
