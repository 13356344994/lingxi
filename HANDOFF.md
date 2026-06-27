# 灵犀(lingxi)项目交接文档

> 面向接手的新 AI agent / 工程师。先读完本文件再动手。
> 最后更新:2026-06-28(北京时间)。

## 1. 项目本质
- 路径:`/root/lingxi`,Go 编写的实盘加密交易系统。
- **历史**:原为「资金费 carry / 五策略统一轮动」多策略系统(binance/bybit/gate/okx)。
- **现状(已转型)**:carry 已停用,系统现在是 **四所独立「方向量化」引擎**(OKX / Bybit / Gate / Binance),每所一套相同逻辑、独立资金/台账/控制。
- **铁律**:全实盘,用户明确**禁止影子/dry-run**。杠杆固定 5X。沟通用中文。

## 2. 运行 / 部署(必须掌握)
- **部署**:`cd /root/lingxi && ./start.sh`(`go build` 注入版本 → restorecon → `systemctl restart lingxi`)。**改完代码必须 ./start.sh 才生效**(从源码重建)。
- **日志**:`records/lingxi_server.log`(北京时间戳)。方向引擎日志前缀:OKX = `[OKX方向]`,其余所 = `[bybit][OKX方向]` / `[gate]...` / `[binance]...`。
- **面板**:`http://127.0.0.1:8080/`,登录令牌在 `records/.dashboard_control_token`(每次重启变)。
- **控制接口**:`GET/POST /api/okx-directional?venue=<okx|bybit|gate|binance>`;POST 需 header `X-Dashboard-Token`;启动(active=true)需 body `confirm:"LIVE"`,停止不需要。
- **并发编辑警告**:多个 agent/composer 不要同时改同一 .go 文件;重启前确认无后台任务正在改源码(否则会编进半成品)。
- **沙箱网络**:开发环境的浏览器/抓取工具**无法访问服务器 localhost:8080**;改 UI 需让用户截图反馈。

## 3. 关键代码地图(均在 internal/app/ 除非注明)
- `okx_directional_engine.go` — 主 plumbing(类型名仍叫 `OKXDirectionalEngine`,已泛化为任意所)。构造:`NewDirectionalEngine(venueName, v DirectionalVenue, peers, cfg, recordDir, logger)`、`NewDirectionalEngineForVenue(venueName, cfg, orch, ...)`、兼容包装 `NewOKXDirectionalEngine`。含 `openPosition` / `manageOne` / `closePosition` / `partialClose` / `scaleIn` / `provisionCapital`(资金划转,margin-aware,只搬自由资金)/ `placeDirectionalLeg`(maker-first + IOC 兜底)/ `gatherPeerDirections`。
- `okx_directional_signal.go` — **纯函数(可单测)**:`computeDirectionalSignal`、`decideDirectionalPositionAction`、`directionalEntryGuard`、`crossVenueCorroboration` / `recentDirection`、`sanitizeFillPrice`。
- `okx_directional_scan.go` / `okx_directional_universe.go` — 每 30min 全场选币(资费+ATR+量+BTC情境综合分)。
- `okx_directional_stats.go` — 复盘/归因聚合 `computeDirectionalStats`。
- `okx_announcement.go` — OKX 公告闸(下架/维护;best-effort,fail-open)。
- `okx_directional_api.go` — 面板 API(多所,`?venue=`)。
- `directional_venue.go` — 接口 `DirectionalVenue` = `venue.Venue` + `FetchCandles`。
- `run.go` — 四所引擎构造接线 + 账户级服务常驻(`carryTradingEnabled` 门控)。
- `venue_adapter_runtime.go` — okx/gate(必需)+ bybit(best-effort)适配器注入 orchestrator;binance 在 run.go 单独构造。
- 配置:`internal/core/config.go` 的 `OKXDirectionalConfig` + `internal/core/defaults.go`。
- 面板:`internal/app/webpanel/index.html`(go:embed,改完要 build)。
- K 线:四所 `FetchCandles` 返回统一 `venue.Candle`(`internal/venue/<所>/marketdata.go`)。

## 4. 策略逻辑
### 出场优先级(decideDirectionalPositionAction)
1. 硬止损(uPnL ≤ stopFloor,默认 -20% 账户)/ 信号反转 → 平
2. **移动保盈**:峰值过 +5% 本金后,回吐到 `max(0, 峰值−15%)` 即平
3. **快进快出止盈**:浮盈 ≥ +12% 本金即全平(强趋势延续才骑乘加仓)
4. **停滞离场(治超时)**:持仓 ≥40min 仍未盈利 → 提前砍
5. 超时(60min):盈利且信号同向→续持(封顶 180min),否则平

### 开仓链路
30min 选币 shortlist → `computeDirectionalSignal`(regime 顺势/震荡反转 + 资费对齐闸 + 防陷阱闸)→ 成本地板(60bps)→ **跨所交叉印证**(≥2 所反向则否决)→ **流动性闸**(盘口>50bps 跳过)→ **下架/公告闸** → `openPosition`。

### 关键默认参数(defaults.go,OKXDirectional)
`SignalWindowBars=24`、`QuickTPPct=0.12`、`StopLossPct=0.20`、`BreakevenArmPct=0.05`、`TrailGapPct=0.15`、`StaleCutMinutes=40`、`MaxHoldMinutes=60`、`AbsMaxHoldMinutes=180`、`MaxConcurrent=3`、`Leverage=5`、`CostFloorBps=60`、`MaxOpenSpreadBps=50`、`FillPriceMaxDevPct=0.10`、`CrossVenueVetoMinPeers=2`、`CrossVenueConvictionStep=0.1`、`UniverseScanIntervalMinutes=30`、`SymbolCooldownMinutes=30`、`LossCooldownMultiplier=3`、`DailyLossLimitUSDT=0`(不熔断)。

## 5. 本轮(今日)完成的工作
**A. 诊断 + 策略修复(基于真实日志/复盘):**
- 修「regime 永远判 range」bug:效率比/z 分改用短窗 `SignalWindowBars`,趋势分支被激活(实盘已出 trend 单)。
- 加移动保盈 + 快进快出止盈 + 停滞离场(治超时);收紧硬止损 0.5→0.2。
- 复盘实证:OKX 累计胜率 ~63%、净 +4.37U;**快进快出止盈 = 利润源(8 战 8 胜 +12.5U)**,**超时离场 = 主失血口(-5.6U)**。

**B. 四所方向化(carry→directional):**
- 停 carry(`cross_venue_scanner.enabled=false`,run.go 短路 scanner+executor+rotation)。
- 账户级服务(权益采样 / 充提核算 / 账户硬止损)从 carry 解耦常驻。
- 四所 K 线统一 `venue.Candle` + `DirectionalVenue` 接口;引擎泛化;四所接线(bybit 适配器注入修复);面板多所切换。

**C. 跨所与防护:**
- 跨所交叉印证(同币四所联动,单所假动作否决)。
- 下架/停牌守卫(复用 `DelistGuard`)、OKX 公告闸。
- **成交价合理性校验 `sanitizeFillPrice`**(修 Gate 假成交价记账 bug——曾把一笔记成 -9.65U,经权益快照证实真金没亏)。
- **开仓流动性闸**(挡薄盘,已实战挡掉 AIN/AGT)。

**D. 资金/运维:**
- 资金供给修复(持仓时也从资金账户单向补到 `本金−已占保证金`,margin-aware,只搬自由资金不动保证金)。
- binance 补仓噪声静默(不支持划转就跳过)。
- 数据瘦身:records 132M→4.6M(清 carry 残留 + 服务日志)。

**E. 面板:**
- 复盘/归因(胜率 / 出场原因占比)、per-venue 余额收益并入各所方向框、隐藏 carry 残留、设计系统级 UI 重做、隐藏「控制面状态条」与冗余 venue tab。

## 6. 已知问题 / 下一步建议
1. **首要:观察 + 复盘对比**。今天改动巨多,盈利未充分验证。用面板「复盘/归因」或读 `records/<venue>_directional/settlements.jsonl`,统计改动后「超时离场净亏是否下降、快进快出占比、胜率」。**先用数据验证再继续加逻辑。**
2. **入场质量**是最大杠杆(让更多单够到 100% 胜的快进快出出口)。
3. 重启会清零内存「当日已实现」(daily_realized),面板该数字短期不准;**以 settlements.jsonl / 复盘为准**。
4. binance 不支持所内钱包划转(provisionCapital 静默跳过),用账户现有可用交易;binance 也未作为其他所跨所 peer(orchestrator 未注入,可后续补)。
5. OKX 公告闸只做了 delistings 类型(maintenance annType 名不稳,已禁用);仅 OKX,其他所未做。
6. 用户偏好:实盘禁影子;大改交 composer + 人工审核 + 测试 + 灰度重启;动真金逻辑先诊断不盲改;UI 改动靠用户截图反馈。

## 7. 验证习惯(每次改完)
```
cd /root/lingxi
go build ./... && go vet ./internal/app/ && go test ./internal/app/ -run 'Directional|OKX|Universe|CrossVenue|SanitizeFill|Stats' -count=1
./start.sh
# 然后 grep records/lingxi_server.log 确认无 panic + 功能生效
```
