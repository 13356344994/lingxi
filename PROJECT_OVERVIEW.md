# lingxi 项目评估总览（Project Evaluation Overview）

> 面向**第三方项目评估**的自包含说明：项目做了什么、由哪几部分组成、每部分用到什么技术、工程成熟度与已知局限。
> 细节口径以代码与 `README.md` 为准；本文件是导览与评估索引。

---

## 一、一句话定位

`lingxi` 是一个用 **Go** 写的**资金费率套利交易系统**:**Binance 全引擎(单体)+ Bybit 独立精简执行器 + Orchestrator 多所编排**,覆盖「机会筛选 → 对冲开仓 → 持仓监控 → 资金费结算 → 平仓 → 对账」的完整闭环,并自带本地 Web 控制面板(venue 切换)、历史回测与多档运行模式。多所架构详见 `MULTI_VENUE_REFACTOR.md`。

### 策略本质：delta 中性的资金费率套利（funding carry）
- **正向 carry**：现货买入 + 合约做空，吃**正**资金费率。
- **负向 carry**：全仓杠杆**借币做空现货** + 合约做多，吃**负**资金费率（含借币利息成本）。
- 当前配置为**跨周期滚动持有**：吃完一次后在结算前复核下期边际净 carry，>0 续持、≤0 才平；组合上限 10 个币、无名义顶（实际只受可用资金约束）。
- 边际极薄：单周期对冲往返盈亏平衡线 ≈ `费用 + 两腿价差 + 最低净利` ≈ 0.0055（默认值，可配置覆盖）。

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
- `internal/app/run.go`：按运行模式（实盘 / 回测 / 对账 / 影子）装配依赖。
- `internal/app/funding_sniper_engine.go`（约 1081 行，最大文件）：主引擎编排核心（扫描→评估→开仓→监控→结算→平仓总调度）。
- `internal/app/orchestrator.go` + `internal/app/bybit_runtime.go`：多所编排与 Bybit 执行循环（独立 `records/bybit/` 记录目录）。
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

**已知局限（节选高严重度,2026-06 校订后口径）**
- ✅ R2/R4/R11/R12/R17/R20/R24 已修复（强平距离闸、atomic 时钟偏移、config.yaml、首错取消、真 YAML、Run 返回 error、配置自动绑定），详见 README §十二。
- ⚠️ R3：强平/ADL 检测代码已写,但被币安 WS 字段解析 bug 打断,当前实际不生效（见 `PROJECT_AUDIT.md` C-1）。
- 🔴 R14：单体进程无热备/外部 Kill Switch，物理断连即裸奔（放大资金前的最大黑天鹅）。
- 🔴 R26：API Key 硬化（关提现权限 + IP 白名单）未由代码强制。
- 🟠 R13/R15：缺 TWAP/VWAP/冰山等平滑执行；回测微观失真、纸面阈值偏乐观。
- 🟡 R10/R21：上帝对象、引擎状态面过大（结构性债,仍真实存在）。
- 🟠 新发现问题以 `PROJECT_AUDIT.md`（2026-06-10 审计）为准:WS 字段覆盖(C-1)、domain_events 压缩失效(C-2)、Bybit 数据/健壮性缺口(C-3~C-6)等。

> 说明：部分上述条目（如 R7 撞限相关）已在近期迭代中部分缓解，详见 `README.md` 第十二节与本文件 §七。

**成熟度自评**：README 自评现状 ≈ **6.8 / 10**（工程素养 A 级，短板在「安全姿态」与「文档=现实」），并给出通往 9.6 的 P0~P5 路线图。

---

## 九、评估者 5 分钟速览路径

1. 读 `README.md` §一~§五（定位、架构、流程）与 §十二（已知风险与局限，**最诚实的部分**）。
2. 看 `internal/core/defaults.go`：所有策略/风控默认闸值的单一来源。
3. 看 `internal/app/funding_sniper_engine.go`：主循环编排骨架。
4. 看 `internal/money/`：定点记账，判断金融正确性姿态。
5. 看 `internal/exchange/client.go`：网络治理（限频/回退/used-weight）。
6. 跑 `make check`（fmt+vet+test+build）与 `make race`。
7. 部署/运维看 `deploy/RUNBOOK.md`；多所架构看 `MULTI_VENUE_REFACTOR.md`；最新问题清单看 `PROJECT_AUDIT.md`。

---

## 十、关键文件索引

| 用途 | 路径 |
|---|---|
| 项目说明（最权威、最详尽） | `README.md` |
| 部署运维 | `deploy/RUNBOOK.md`(旧 `DEPLOY_HANDOFF.md` 因快照严重过时已删除) |
| 优化计划 | `IMPROVEMENT_PLAN.md` |
| 事故运行手册 | `deploy/RUNBOOK.md` |
| 进程入口 | `cmd/lingxi/main.go` |
| 主引擎 | `internal/app/funding_sniper_engine.go` |
| 配置默认值 | `internal/core/defaults.go` |
| 用户配置覆盖 | `config.yaml` |
| 网络客户端 | `internal/exchange/client.go` |
| 构建/检查 | `Makefile`、`.github/workflows/ci.yml` |
