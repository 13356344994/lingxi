# lingxi 项目评估总览（Project Evaluation Overview）

> 面向**第三方项目评估**的自包含说明：项目做了什么、由哪几部分组成、每部分用到什么技术、工程成熟度与已知局限。
> 细节口径以代码与 `HANDOFF_20260617.md` 为准；本文件是导览与评估索引。
> **最后更新：2026-06-18**

---

## 一、一句话定位

`lingxi` 是一个用 **Go** 写的**资金费率套利交易系统**：**四所跨所 perp-perp 实盘（~440U）** + **五策略统一轮动系统（shadow，已部署主循环）** + 遗留 Binance 单体引擎（已 de-wire）+ 多所适配层。覆盖「机会筛选 → 对冲开仓 → 持仓监控 → 资金费结算 → 平仓 → 对账」闭环，并自带本地 Web 控制面板。

### 策略本质：delta 中性的资金费率套利（funding carry）

**生产形态（2026-06-18）**：

1. **旧路径 — 跨所 perp-perp（live）**：空高费所 + 多低费所，delta 中性，~440U，独立执行器 `cross_venue_executor`。
2. **新路径 — 五策略统一轮动（shadow）**：每 60s 扫描五类结构（perp-perp、同/跨所 spot-perp 正费率、同/跨所 perp-spot 负费率），按本窗口净 USDT/占用资金排序，续持/换仓决策，四执行器 + 资金路由。默认不下真单。

- **正向 carry**：现货买入 + 合约做空，吃**正**资金费率。
- **负向 carry**：全仓杠杆**借币做空现货** + 合约做多，吃**负**资金费率（含借币利息；币安 `BorrowRepay` 已接入 venue 层）。
- 离线经济数据 **NO-GO**（正费率净年化约 −58%/−60%，负费率扣借币约 −53%）；见 `tools/go_no_go/RESULT.md`。**不得用离线数字粉饰 live 预期。**

---

## 二、技术栈总览

| 维度 | 内容 |
|---|---|
| 语言 | Go（`go.mod` 要求 **go 1.25.3**） |
| 外部依赖 | 仅 2 个：`github.com/gorilla/websocket`（WS 行情/用户数据流）、`gopkg.in/yaml.v3`（配置/密钥解析） |
| 代码规模 | 总计约 **68,000 行 Go**；304 个 `.go` 文件（其中 127 个测试文件） |
| 交易所接口 | Binance 现货 + U 本位合约 REST + WebSocket（行情、用户数据流、下单回报）；Bybit v5 统一账户 REST（`internal/venue/bybit/`，现货+永续 delta 中性执行器） |
| 持久化 | 本地文件：JSON / JSONL（append-only 事实源 + 物化视图），无外部数据库 |
| 部署 | Linux（CentOS/AlmaLinux）+ systemd 守护 + 外部 watchdog；当前部署在东京 VPS 降低对币安的网络延迟 |
| 可观测性 | 本地 Web 面板、Prometheus `/metrics`、`/livez` `/healthz` 探针、心跳文件、Telegram 告警 |
| 工程工具 | `Makefile`（fmt/vet/test/race/cover/build/vuln）、GitHub Actions CI（`.github/workflows/ci.yml`，含 `govulncheck`） |

> 设计哲学值得评估者注意：**几乎零第三方依赖**（连 HTTP 客户端、限频、记账、回测、面板都是手写），换来可控性与可审计性，代价是自制轮子的维护负担。主配置已是真 YAML（`yaml.v3` + 反射自动绑定），README 旧 R17「自制扁平解析器」描述已过时。

---

## 三、系统架构：分部分拆解 + 每部分用到的技术

进程是单体 Go 应用，核心是一个大型引擎对象 `FundingRateSniperEngine`(Binance),另有 Bybit 独立执行器与 Orchestrator 编排。代码量分布如下：

| 包 | 约 LOC | 职责 |
|---|---|---|
| `internal/app` | 56,010 | 交易主引擎、协议、风控、面板、回测、Bybit 执行器（`bybit_*.go`）、多所编排（`orchestrator.go`） |
| `internal/venue` | 3,615 | 多所适配层：`venue` 抽象 + `binance/` 适配 + `bybit/` v5 客户端（行情/交易/资金/对账） |
| `internal/exchange` | 3,248 | Binance REST/WS 客户端、限频、缓存 |
| `internal/core` | 1,921 | 配置内核 `EngineConfig` + 默认值 + 校验 + `venues:` 多所配置 |
| `internal/store` | 763 | 记录存储、JSON/JSONL 编解码与迁移 |
| `internal/reconcile` | 288 | 只读对账内核 |
| `internal/metrics` | 287 | 指标 |
| `internal/execution` | 323 | 定价与追价（pricing） |
| `internal/lifecycle` | 248 | 生命周期状态模型 |
| `internal/risk` | 252 | 纯风险语义（裁判函数） |
| `internal/strategy` | 219 | 策略纯逻辑 |
| `internal/marketdata` | 205 | 微观结构（深度/价差） |
| `internal/money` | 191 | **定点记账内核**（micro-USDT int64） |
| `internal/fillmodel` | 166 | 成交/费用/残差模型 |

### 3.1 入口与编排
- `cmd/lingxi/main.go`：进程入口薄壳，信号处理 + 根 context。
- `internal/app/run.go`：按运行模式装配依赖；**五策略 runner**（`unified_rotation_runner.go`）与跨所执行器并行；单所引擎已 de-wire。
- `internal/app/unified_rotation_runner.go`：统一轮动主循环（扫描→候选→决策→资金路由→四执行器）。
- `internal/app/cross_venue_*.go`：跨所 perp-perp 执行器（当前 **live** 主路径之一）。
- `internal/app/funding_sniper_engine.go`：Binance 遗留单体引擎（已退役自启）。
- `internal/app/orchestrator.go` + `bybit_runtime.go`：多所编排。
- `internal/app/runtime_controller.go`：运行期控制器（启停、参数热调整、控制审计）。
- **技术**：Go context 取消传播、`sync.Once`/atomic/多把分域锁、`defer` 优雅停机 + panic 恢复转受控关停。

### 3.2 交易核心域（`internal/app/`）
| 模块 | 文件 | 技术要点 |
|---|---|---|
| 选币/打分/成本进场闸 | `strategy_evaluator.go`、`strategy_models.go` | 多闸门筛选（跳变/基差/费率区间/净利门） |
| 开仓协议 | `open_protocol.go`、`engine_open_exec.go` | 先合约后现货、并发/顺序池、超配回滚、裸腿修正、开仓后逆向选择复核 |
| 平仓协议 | `close_protocol.go` | 结算窗口附近对冲平仓、被动现货优先 |
| 两阶段结算 | `engine_settlement.go`、`funding_settlement_worker.go` | 平仓先记交易/费用/借币，资金费实收后异步用真实 `/fapi/v1/income` 重算 `NetPnL` |
| 成本核算 | `engine_cost_guard.go`、`cost_accounting.go`、`cost_reporter.go` | 借币利息估算、两腿价差 `entrySpreadRate`、冲击储备 |
| 负向 carry | `live_negative_carry.go` | 借币做空现货专属路径、保守费率估计（R7） |
| 撞限豁免（新增） | `engine_cap_pinned_funding.go` | 资金费撞交易所 cap/floor 时不再被 R7 误归零（详见 §七） |
| 残腿/敞口救援 | `exposure_manager.go` | 落盘任务、重启恢复、`reduceOnly` 物理只减不加 |
| 订单恢复 | `recovery_protocol.go`、`order_recovery_policy.go` | 不确定终态处理、幂等下单 |
| 实时止血 | `realtime_drawdown.go` | 实时浮亏 panic-flatten、单币止血 |
| 风险监督 | `risk_supervisor.go`、`risk_event.go`、`risk_contract.go` | 平台熔断、读/写请求分离计数 |
| 账户级 fail-safe | `live_safety.go` | 连亏熔断、回撤熔断、日级预算、急停文件 |
| 生命周期 | `lifecycle_state_machine.go`、`lifecycle_order_gate*` | 阶段状态机 + 下单闸 |

### 3.3 网络与下单咽喉点
- `internal/exchange/`：唯一摸网络的包。`client.go`（元数据、账户费率、历史抓取、**限频/回退/used-weight 治理**）、`http.go`/`transport.go`（代理 + 连接池）、`symbols.go`（黄金币种交集 + 资金费上下限）、`cache.go`（历史负载缓存）。
- `internal/app/binance_gateway.go`：`SignedRequest`/`GetJSON`/`APIKeyRequest` 收口，联动平台熔断、used-weight 预算、本地下单限频。
- `internal/app/engine_fees_market.go`：`signedRequest` 是**所有签名请求的唯一咽喉点**——影子模式在此硬拦截一切非 GET 调用。
- `order_events.go`（`OrderEventHub`）/ `user_data_stream.go` / `websocket_bookticker.go`：订单回报中心、用户数据流、bookTicker/depth 行情流。
- **技术**：HMAC-SHA256 签名、recvWindow、服务器时间偏移同步、WS 自动重连、断连盲窗状态机。

### 3.4 边界包（无网络依赖、可独立单测）
- `internal/core`：分层配置 `EngineConfig` + `DefaultEngineConfig()` + `Validate()` + Profile 档位。
- `internal/money`：**定点记账**（micro-USDT int64，消除 float 累加漂移）——金融系统关键正确性保证。
- `internal/risk`：纯风险语义（确定性裁判函数：对冲偏离、流动性份额、成交覆盖率）。
- `internal/fillmodel` / `internal/execution` / `internal/marketdata` / `internal/strategy` / `internal/lifecycle`：成交模型、定价追价、微观结构、策略纯逻辑、生命周期模型。
- `internal/store` / `internal/reconcile` / `internal/metrics`：存储编解码、只读对账、指标。

### 3.5 回测 / 模拟
- `backtest_runner.go` / `backtest_csv_io.go`：CSV 回测与回放。
- `historical_*.go`：真实历史行情**月度模拟全链路**（kline 缓存、候选、定价、执行、冲击乘数）。
- **注意**：回测口径与实盘口径不同（basis 当成本 vs 当门），结论不能直接外推实盘（README R15）。

### 3.6 面板
- `internal/app/dashboard_server.go` + `webpanel/index.html`：本地控制面板与 API；写操作强制 loopback + 恒定时间令牌校验，读操作带令牌即可。

---

## 四、运行模式（多模式同一二进制）

| 模式 | 命令 | 用途 |
|---|---|---|
| Dashboard | `go run ./cmd/lingxi` | 启动面板（默认 `127.0.0.1:8080`），可手动启动实盘 |
| Backtest | `... backtest` | CSV 回测 |
| Replay | `... backtest replay` | CSV 回放 |
| Timeline | `... backtest timeline` | 时间线回放 |
| Monthly | `... backtest-monthly secret.yaml 30` | 真实历史行情月度模拟 |
| Reconcile | `... reconcile` | 只读对账：验证实盘是否按模型赚钱 |
| Shadow | `... shadow` | 影子只读冒烟：照常决策但硬拦截一切下单（抓灾难性 bug，不用于校准经济阈值） |

---

## 五、核心交易流程（一个完整周期）

1. **唤醒**（结算前数分钟）：同步服务器时间偏移、刷新账户。
2. **扫描**（3 min 窗口）：拉 `premiumIndex`、黄金币种交集，按预估费率/流动性粗筛。
3. **评估**：深度打分 + 成本进场闸（费用 + 两腿成交价差 + 借币储备 + 冲击储备 + 最小净利门）。
4. **预备**：设杠杆/全仓、确保 USDT/BNB 资金、构建开仓计划。
5. **战前 Go/No-Go**：再校验 ticker 新鲜度、资金费稳定性、保证金充足。
6. **开仓**（fire-at）：先合约批量单 → 解析成交 → 现货对冲单 → 超配回滚/裸腿修正 → 开仓后逆向选择复核。
7. **监控/持有**：实时 bookTicker + 监控期净利门 + 实时浮亏 panic-flatten。
8. **平仓**（结算后延迟）：对冲平仓，被动现货优先。
9. **结算**：先记交易/费用/借币 → 资金费实收后异步重算 `NetPnL` → 更新连亏/回撤/日级预算。
10. **对账**（可选、只读）：验证实盘是否按模型赚钱。

> 任意一步异常 → 残腿落入 `ExposureManager` 托管或灰尘台账，符号进入冷却。

---

## 六、风控与可观测性体系

**账户级 fail-safe（只停机不下单）**：全仓 2x 杠杆拉远强平价、实时浮亏 panic-flatten、连亏熔断（UTC 日维度、跨重启保留）、账户回撤熔断、保证金充足性校验、急停文件、平台请求熔断（读/写分离计数）。

**对冲不变量守卫**：相对偏离维度 `HedgeImbalanceAbortRate=0.10` 出厂武装，持有期周期性复核（正/负 carry 均覆盖）。

**可观测性 / 运维**：
- 实时告警分发（`alerting.go`）：Telegram / webhook / logger，按级别过滤 + 去重。
- HTTP 探针（`observability_http.go`）：`/livez`、`/healthz`、`/metrics`（Prometheus 文本，无新依赖）。
- 心跳文件供外部 watchdog 判活。
- 控制操作全程审计（`dashboard_control_audit.jsonl`）。
- systemd（开机自启 + 崩溃重启）+ watchdog timer + 事故运行手册 `deploy/RUNBOOK.md`。

---

## 七、近期新增能力：撞限豁免（Cap-Pinned Funding Override）

针对一个真实的「漏接极端肥单」缺陷的修复（FIDA -2% 资金费撞底却没开仓）：
- **问题**：当某币实收资金费撞住交易所封顶/封底（如 -2% floor）时，前向溢价信号常因均值回归与之反号，触发 R7 保守化把预估费率归零 → 跳变撞穿 `MaxFundingRateJump` → 极端肥单被误拒。
- **修复**：新增 3 个配置项（默认**关闭**）。开启后若实收费率已贴住 cap/floor 的 `ProximityRate` 倍且方向一致，则按 `lastFundingRate × EstimateFactor` 给保守但非零的预估，跳变随之归零、自然过闸。
- **技术细节**：新增 `/fapi/v1/fundingInfo` 拉取并缓存逐币真实上下限（缺失回退默认 ±2%）；海选与监控期复估两处一致接入；触发时打 `[撞限豁免]` 日志，`scan_observations.jsonl` 加 `cap_pinned_override` 标记；带「只在提升同向确信度时生效」的安全闸。配套 4 组单测。

---

## 八、工程质量（评估者关注点）

**优点**
- 近乎零第三方依赖，全链路可审计；边界包纯逻辑、可独立单测（94 个测试文件）。
- 定点记账（micro-USDT int64）从源头消除浮点累加漂移——金融正确性的硬保证。
- 网络副作用收口到单一咽喉点（`signedRequest`），影子模式可硬拦截，安全冒烟可信。
- append-only 事实源 + 物化视图分层，崩溃恢复有重放能力。
- 文档诚实度高：`README.md` 用「以代码现实为准」的姿态逐条标注「文档承诺 > 代码现实」的缺口，并给出严重度分级（🔴致命/🟠重大/🟡一般）与优化路线图。
- CI 完整：fmt → vet → test → race → cover → build → govulncheck。

**已知局限（节选高严重度,2026-06-18 口径）**
- **经济数据 NO-GO**：离线正/负费率结构净年化大幅为负；实盘 perp-perp 181 笔资金费约 1.6U（`tools/go_no_go/RESULT.md`）。
- **新五策略未 live 验证**：四执行器仅 fake 单测；canary 前须子账户隔离 + shadow 观察。
- ⚠️ R3：强平/ADL 检测被 WS 字段解析 bug 打断（见历史审计）。
- 🔴 R14：单体进程无热备；物理断连风险仍在。
- 🔴 R26：API Key 硬化未由代码强制。
- 完整风险清单见 `README.md` §十二；交接见 `HANDOFF_20260617.md`。

**成熟度自评**：工程实现已达统一轮动 shadow 部署；**盈利性与 canary 就绪度未验证**。勿按 README 旧 6.8/9.6 路线图分数对外承诺。

---

## 九、评估者 5 分钟速览路径

1. 读 `HANDOFF_20260617.md`（**当前生产态与 canary 清单**）。
2. 读 `PERP_PERP_REVIEW_20260617.md` 末尾「实现进度与审计状态」。
3. 读 `tools/go_no_go/RESULT.md`（**NO-GO 数据结论**）。
4. 看 `internal/app/unified_rotation_runner.go` + `unified_rotation.go`（新主循环）。
5. 看 `internal/app/cross_venue_executor.go`（旧 live perp-perp）。
6. 跑 `make check`；部署/运维看 `deploy/RUNBOOK.md`。

---

## 十、关键文件索引

| 用途 | 路径 |
|---|---|
| **交接（当前权威）** | `HANDOFF_20260617.md` |
| Master 方案 + 实现状态 | `PERP_PERP_REVIEW_20260617.md` |
| 项目说明（Binance 遗留引擎详述） | `README.md` |
| go/no-go 数据 | `tools/go_no_go/RESULT.md` |
| 统一轮动主循环 | `internal/app/unified_rotation_runner.go` |
| 跨所 perp-perp（live） | `internal/app/cross_venue_*.go` |
| 配置默认值 | `internal/core/defaults.go` |
| 用户配置覆盖 | `config.yaml` |
| 网络客户端 | `internal/exchange/client.go` |
| 构建/检查 | `Makefile`、`.github/workflows/ci.yml` |
