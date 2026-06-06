# 项目交接说明:lingxi 资金费率套利系统 — 部署到东京 VPS

> 给「服务器端 Agent」的自包含交接文档。无需本地历史对话,照此即可部署运行。

## 你的角色
你运行在一台东京的 CentOS 云服务器(root,公网 IP `108.61.250.51`)上。
任务:把这个 Go 写的「币安现货/U本位合约 资金费率套利」程序部署起来、稳定运行,并接入面板观测。

## 为什么搬到东京
程序原先在中国大陆本地跑,访问 `fapi.binance.com` 网络极不稳,只读行情 GET 频繁
`context deadline exceeded` / TLS 握手超时,触发平台级熔断(连续请求错误达阈值就 `pause_cycle` 停摆),
导致明明有好机会却开不出单。搬到东京 VPS 就是为了低延迟、稳连接。**「网络延迟」是头等验收项。**

---

## 一、项目是什么(策略本质)
delta 中性的资金费率套利(funding carry):
- **正向 carry**:现货买入 + 合约做空,吃正资金费率。
- **负向 carry**:全仓杠杆借币做空现货 + 合约做多,吃负资金费率(有借币利息成本)。

在资金费结算窗口附近开对冲仓、吃一次资金费;当前配置为「跨周期滚动持有」:吃完一次后在结算前
复核下期边际净 carry,>0 就续持、≤0 才平。组合上限 10 个币、无名义顶 → 实际只受可用资金约束。

边际很薄。当前是「**极小仓买真实数据**」的数据采集姿态,不是稳盈配置,会亏学费,属于有意为之。

## 二、技术栈 / 仓库 / 关键文件
- 语言:Go(`go.mod` 要求 `go 1.25.3`,请装 ≥1.25 的 Go)
- 唯一外部依赖:`github.com/gorilla/websocket`
- 仓库(私有):`git@github.com:13356344994/lingxi.git`,分支 `main`,最新提交 `lingxi-V7.1`
- 目标目录:`/root/lingxi`
- 入口:`cmd/lingxi/main.go` → `internal/app/run.go`
- 主引擎:`internal/app/funding_sniper_engine.go`
- 配置内核与默认值:`internal/core/defaults.go`(所有默认闸值在这里)
- 用户覆盖配置:`config.yaml`(只列与默认不同的项)
- 密钥:`secret.yaml`(已在私有仓库里,clone 下来就自带,无需另建;字段:`api` / `secret` / `Telegram_Token` / `Telegram_Chat_ID`)
- 启动脚本:`./start.sh`(自动 stop 旧进程 → 用 `-ldflags` 注入版本号 → `go build` → `nohup` 后台启动 →
  删除旧令牌文件 → 启动后从令牌文件/日志里提取并打印「控制令牌」)
- 停止脚本:`./stop.sh`
- 日志:`records/lingxi_server.log`
- 控制令牌文件:`records/.dashboard_control_token`(0600,仅本机可读)
- 生产守护:`deploy/` 下有 systemd 单元(`lingxi.service` / `lingxi-watchdog.{service,timer}` / `watchdog.sh` / `RUNBOOK.md`),
  长期跑可用它做开机自启与崩溃重启;**先手动 `start.sh` 跑通再上 systemd**。

## 三、部署步骤(按顺序做,每步把结果告诉我)

1. 装 git 和 Go(≥1.25):
   - `yum install -y git`(或 `dnf`)
   - Go 用官方二进制:从 https://go.dev/dl/ 下 `go1.25.x.linux-amd64.tar.gz`,
     解压到 `/usr/local`,`PATH` 加 `/usr/local/go/bin`。验证 `go version`。

2. 配 Deploy Key 拉私有仓库:
   - `ssh-keygen -t ed25519 -N "" -f ~/.ssh/id_ed25519`
   - 把 `~/.ssh/id_ed25519.pub` 内容打印出来发给我(我去 GitHub 仓库 Settings → Deploy keys 添加)
   - 我加好后,你执行:`git clone git@github.com:13356344994/lingxi.git /root/lingxi`
   - (clone 完成后 `secret.yaml` 已就位)

3. 测到币安的网络延迟(头等验收项),连测 5 次报给我:
   ```sh
   for i in 1 2 3 4 5; do curl -o /dev/null -s -w "%{time_total}\n" https://fapi.binance.com/fapi/v1/time; done
   ```
   期望 total time 在几十毫秒级。若仍很高/超时,告诉我,可能要换区或排查。

4. 编译并启动:
   ```sh
   cd /root/lingxi && chmod +x start.sh stop.sh && ./start.sh
   ```
   - 它会打印「构建版本: version=... commit=... build_time=...」和最后一行「控制令牌: xxxx」
   - 把这串控制令牌原样发给我。确认进程存活、面板在 8080 监听(`ss -ltnp | grep 8080`)。

## 四、面板访问(重要:默认只绑本机)
面板默认监听 `127.0.0.1:8080`(`internal/core/defaults.go` 的 `DashboardAddr`,`dashboard_server.go`),
只绑回环、公网访问不到。**推荐用 SSH 隧道**从本地访问(安全,不暴露公网):
```sh
# 在本地电脑执行
ssh -L 8080:127.0.0.1:8080 root@108.61.250.51
```
然后本地浏览器开 http://127.0.0.1:8080/ ,用控制令牌操作。
**不要**把 8080 改成 `0.0.0.0` 暴露公网(面板能启停实盘引擎、动账户)。

## 五、当前配置姿态(`config.yaml`,已是数据采集小仓)
| 项 | 值 | 说明 |
|---|---|---|
| `strategy.fee_reserve_rate` | 0.0024 | 三档 BNB 抵扣后往返费 |
| `strategy.min_estimated_funding_rate` | 0.0034 | |
| `strategy.max_scan_basis_rate` | 0.04 | 放大基差门,主动放高资费币进场(数据采集用) |
| `risk.min_net_profit_rate` | 0.0005 | 最低净利门 0.05%(优先开出单) |
| `risk.immediate_invalidate_rate` | 0.0003 | |
| `portfolio.enable_rolling_hold` | true | 跨周期滚动持有 |

默认侧关键值:`MaxPortfolioSymbols=10`,`MaxPortfolioNotionalUSDT=0`(无名义顶),
`RevalidateLead=5m`,`MonitorStopLead=60s`,`StreamWarmupTimeout=3s`,`MaxConsecutiveRequestErrors=6`。
注意:页面「调仓」设的小仓只影响**新开**每腿名义,已常驻仓不会自动缩。

## 六、近期已修的关键问题(了解现状,别误判)
- **R12c**:深度数据缺失曾被误判为「流动性枯竭」导致 `coverage=0.0000` 暂停整轮 → 已改为数据缺失走单币冷却,不再停整轮。
- **R12d**:只读行情 GET 超时(`context deadline exceeded`)曾被当「订单不确定」计入致命熔断、误杀健康单 →
  已把只读路径与写路径分开计数(`risk_supervisor.go` 的 `NotePlatformReadRequestResult` / `binance_gateway.go` `GetJSON`),
  只读网络超时不再触发全局 `pause_cycle`。
- 同时把 `StreamWarmupTimeout` 1.5s→3s、`MonitorStopLead` 30s→60s,给 WS 预热和开火重试更多缓冲。

这些都已在 `lingxi-V7.1`。搬到东京后,理想情况是延迟低到根本不触发这些熔断。

## 七、红线 / 注意
- **绝不要**把 `secret.yaml`、API key 提交进 git(`.gitignore` 已含,但该文件历史上已被跟踪,别再 `git add` 它)。
- 这是**真钱实盘**,只用极小仓试错;任何放大仓位/改风控闸的动作**先问我**。
- 服务器**时间必须准**(币安签名对时钟漂移敏感,`-1021` 错误):确认 `chrony`/`ntpd` 已开并同步。
- 长期运行用 `deploy/` 里的 systemd + watchdog(见 `deploy/RUNBOOK.md`),先手动跑通再上。

## 八、做完后请回报
1. `go version`、Go 安装位置
2. 币安延迟 5 次测速结果
3. 控制令牌
4. 面板 8080 是否监听
5. 启动横幅的 `version`/`commit`/`build_time`
6. 任何报错日志(`records/lingxi_server.log` 末尾)
