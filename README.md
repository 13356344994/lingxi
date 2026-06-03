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
- `main.go`
  - 启动入口和回测入口
- `funding_sniper_engine.go`
  - 当前实盘主引擎编排核心
- `runtime_controller.go`
  - 后台任务控制、运行日志、页面状态
- `dashboard_server.go`
  - 本地控制面板与 API
- `report_store.go`
  - 记录存储、JSON / JSONL 迁移与保留策略
- `risk_event.go` / `risk_semantics.go`
  - 风险语义和风险事件模型
- `engine_stage.go`
  - 阶段枚举与合法迁移规则
- `order_events.go`
  - 订单回报缓存和等待中心
- `historical_monthly_backtest.go`
  - 月度真实行情模拟
- `historical_data_cache.go`
  - 历史请求缓存
- `backtest/`
  - 样例回测输入与 README
- `webpanel/index.html`
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

## 实盘前 Checklist
- 确认 `secret.yaml` 只在本机存在，且权限正确
- 确认 `DefaultEngineConfig()` 或运行时风控参数已按资金规模调整
- 确认 Dashboard 只监听本机地址
- 确认测试、`go vet`、`go build` 可通过
- 确认 `records/` 清理策略符合本次实验需求
- 确认没有误提交运行产物、缓存和日志

## 后续优先方向
- 拆薄 `funding_sniper_engine.go`
- 让状态迁移彻底成为主流程骨架
- 给 `RiskEvent` 增加更丰富的上下文
- 扩大业务流程级测试
- 抽象存储接口，为 JSONL / SQLite 演进做准备
