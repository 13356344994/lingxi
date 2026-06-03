# lingxi
`lingxi` 是一个围绕 Binance 现货 / U 本位合约组合、资金费率机会筛选、执行风控、历史模拟和本地面板观测构建的单体交易系统原型。

## 项目目标
- 在资金费率机会出现前完成候选筛选、流动性和容量过滤。
- 在资金费结算窗口附近执行现货/合约对冲开平仓。
- 对执行异常、平台异常、订单不确定性和残腿风险做风控约束。
- 将运行状态、风险事件、结算结果、监控记录和回测结果落盘并可视化。

## 当前能力
- 实盘运行入口和 Web Dashboard
- Binance REST / WebSocket 行情与用户数据流
- 订单回报中心 `OrderEventHub`
- 分层配置 `EngineConfig` 与启动前校验
- 风控语义、风险事件和阶段迁移模型
- 回测、历史回放、月度真实行情模拟
- 历史请求缓存、运行日志、控制操作审计
- JSON / JSONL 记录落盘与运行期保留策略

## 运行模式
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
- `Shadow`（影子只读冒烟）模式
  - `go run ./cmd/lingxi shadow`
  - 照常对接真实行情/账户、照常完成扫描/评估/决策，但任何会动用资金或改变交易所状态的签名请求
    （下单/撤单/改杠杆/划转/借还币等一切非 GET）都会在唯一咽喉点 `signedRequest` 被**硬拦截**、
    绝不触达交易所，下单意图以 `SHADOW_BLOCKED` 日志行落盘供核查
  - 用途：实盘前的安全冒烟，抓「下错方向 / 异常数量 / 失控循环」等灾难性 bug；**不是**用来校准经济阈值
    （模拟/影子量不到真实滑点、成交率、真实资金费与竞争，纸面数字会系统性偏乐观）
  - 也可通过配置 `execution.shadow_trading: true` 开启

`backtest/README.md` 里有更详细的样例文件说明和回放字段解释。

## 配置与密钥
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
- 后续建议引入 `config.yaml` 和环境变量覆盖，把策略与风控参数从代码里分离出去

## 仓库结构
- `cmd/lingxi/main.go`
  - 进程入口薄壳：信号处理 + 根 context，委托 `internal/app.Run`
- `internal/app/`
  - 实盘主引擎编排、面板、记录存储、风控语义、回测/模拟等核心域逻辑
  - `funding_sniper_engine.go` 主引擎编排核心
  - `run.go` 应用编排入口（实盘/回测模式选择与依赖装配）
  - `dashboard_server.go` 本地控制面板与 API
  - `report_store.go` 记录存储、JSON / JSONL 迁移与保留策略
  - `close_protocol.go` / `open_protocol.go` 开平仓协议
  - `strategy_evaluator.go` 选币/打分/成本进场闸
  - `engine_settlement.go` / `funding_settlement_worker.go` 资金费结算
- `internal/` 其余包
  - `core`（配置内核）、`exchange`、`money`（定点记账）、`fillmodel`（成交/费用模型）、
    `lifecycle`、`strategy`、`risk`、`store`、`marketdata` 等可独立单测的边界包
- `backtest/`
  - 样例回测输入与 README
- `internal/app/webpanel/index.html`
  - 面板前端

## 记录文件
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

说明：
- 高频记录已经逐步转到 `JSONL append-only`
- 运行态记录和缓存默认只保留最近几小时，避免长期无限增长

## 不应提交的文件
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

## 开发命令
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

如果本机 Go 构建缓存权限受限，建议：
- 优先修复本机权限问题
- 或在 CI 中至少固定执行 `gofmt`、`go vet`、`go test`

## 面板与安全
- 面板默认只监听 `127.0.0.1`
- 修改类 API 依赖本机来源校验和 `controlToken`
- `/records/` 目录会通过 HTTP 暴露记录文件，所以不要把密钥、真实私密文件放进 `records/`
- 如果未来需要监听非 localhost，必须再加正式认证和更严格的访问控制

## 账户级风控刹车（fail-safe，只停机不下单）
- 合约默认 **全仓(CROSSED) + 2x 杠杆**：全仓保住 delta 中性对冲不被单腿强平打破，2x 拉远强平价
- 实时浮亏 panic-flatten + 单币止血 + 开仓前爆仓距离校验
- `MaxConsecutiveLossCycles`：按 funding 实收结算后的真实净利连亏 N 周期即停机（当日维度，跨重启保留）
- `MaxAccountDrawdownUSDT`：以全部已结算周期净利的累计曲线为准，自历史高水位回撤超阈值即停机
  （补 `MaxDailyLossUSDT` 日清零抓不住的跨日慢性失血；回撤由结算记录回放得出，重启自动复原）

## 实盘前 Checklist
- 确认 `secret.yaml` 只在本机存在，且权限正确
- 确认 `DefaultEngineConfig()` 或运行时风控参数已按资金规模调整
- 确认 Dashboard 只监听本机地址
- 确认测试、`go vet`、`go build` 可通过
- 确认 `records/` 清理策略符合本次实验需求
- 确认没有误提交运行产物、缓存和日志
- 小额实盘跑出若干完整周期后，用 `go run ./cmd/lingxi reconcile` 核对实盘是否按模型预期赚钱，再考虑放大档位(起步用 shadow)

## 后续优先方向
- 拆薄 `funding_sniper_engine.go`
- 让状态迁移彻底成为主流程骨架
- 给 `RiskEvent` 增加更丰富的上下文
- 扩大业务流程级测试
- 抽象存储接口，为 JSONL / SQLite 演进做准备
