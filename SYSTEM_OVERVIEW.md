# 灵犀(lingxi)系统全景与实现清单

> 更新于 2026-06-12。本文是系统的**事实快照**:架构、已实现能力、运行状态、安全机制、待办。
> 战略与收益规划见 `ROADMAP_100APY.md`;操作手册见 `README.md`。

---

## 1. 系统是什么

四交易所(币安 / Bybit / OKX / Gate)资金费率套利系统,Go 单进程 + systemd 常驻,核心策略两层:

- **单所 delta-neutral**:正费率 → 空永续+买现货;负费率 → 多永续+借币卖现货。赚资金费,价格风险对冲掉。
- **跨所 perp-perp 对冲**(P1,影子运行中):同一币在两所费率差极端时,空高费所永续+多低费所永续,赚**跨所费率差**。专吃单所做不了的妖币(无现货腿/借不到币)。

部署:东京 VPS,`/root/lingxi`,Go 1.26,systemd 服务 `lingxi` + 看门狗 `lingxi-watchdog`。

---

## 2. 架构分层

```text
┌─ 面板/HTTP (dashboard_server, :8080) ── /healthz /metrics /api/*
├─ 编排层 (orchestrator) ── 多 venue 注册、per-venue RuntimeController、记录目录隔离
├─ 引擎层
│   ├─ 币安主引擎(遗留路径,资金费狙击)         records/
│   ├─ Bybit 精简执行器(B5,实盘运行中)          records/bybit/
│   ├─ 跨所价差扫描器(只读,10min/轮)            内存快照 + /api/cross-venue/funding
│   └─ 跨所对冲执行引擎(影子模式)               records/crossvenue/
├─ venue 适配层 (internal/venue/*) ── 统一 venue.Venue 接口,四所各自实现
└─ 安全/运维 ── watchdog、halt-live 急停、dead-man、启动清扫、Telegram 告警
```

**设计铁律**:币安/Bybit 既有实盘路径「逐字节不变」,新能力全部纯增量;跨所资金划转(提币)永不自动化。

---

## 3. venue 适配层(四所全部完工,2026-06-12 交付)

统一接口 `internal/venue/venue.go`(`venue.Venue`):行情、账户、交易、资金管理、安全(dead-man/错误分类),并有契约测试 `RunConformance` 钉死语义。

| 能力 | binance | bybit | okx | gate |
|---|---|---|---|---|
| 签名 | HMAC-SHA256 query | HMAC-SHA256 头 | HMAC-SHA256 Base64 + passphrase | HMAC-SHA512 hex 五段串 |
| 行情快照(资金费全市场) | ✅ 2 请求 | ✅ | ✅ 逐币+130ms 限速 | ✅ 单端点 |
| 永续下单单位 | 币数量 | 币数量 | **张**(ctVal 换算) | **带符号整数张**(quanto_multiplier) |
| 现货市价买 | ✅ | ✅ | ✅ tgtCcy=base_ccy | IOC 限价替代(Gate 市价买按 USDT 计) |
| 借币(负 carry) | ❌(声明已修正) | ❌ venue 层(旧引擎自有路径✅) | ❌ 未接 | ❌ 未接 |
| 原生 dead-man | ✅ countdownCancelAll | ❌(机构专属)→ 启动清扫替代 | ✅ cancel-all-after | ✅ countdown_cancel_all |
| 所内划转(调仓) | 旧路径✅ | ✅ 资金↔统一 | ✅ 资金↔交易(6↔18) | ✅ spot↔futures |
| key 自检 | ✅ | ✅ | ✅ perm/ip 解析 | ❌ 无此接口 |
| 资金费周期 | 8h/4h | 8h/4h/1h | 8h/4h/1h | 8h/4h/1h |

各所怪癖全部收敛在适配器内部(clientOrderID 净化、张数换算、限频 pacing、instruments 10 分钟缓存),上层引擎只看统一接口。

**未实现(均显式 ErrUnsupported)**:各所 Streaming(WebSocket)、PlaceBatch(okx/gate)、OKX/Gate 借币、Gate key 自检等。

---

## 4. 引擎层

### 4.1 币安主引擎(遗留)
- 资金费率狙击,现货+永续 50/50,全套风控/结算/回测/残差记录。
- 现状:**闲置**(账户仅 ~1.4U),autostart 关。跨所 v1 不接币安交易(避免双引擎打架),适配器已就绪,接入只差白名单+冲突守卫。

### 4.2 Bybit 精简执行器(实盘运行中)
- 正费率:结算前 240s 开仓(空永续+买现货),跨结算收资金费,EV 门(期望结算次数×费率×名义−成本)定开平。
- 负费率:多永续+借币卖现货;借币前 MaxBorrowable 预检,170207(借币池空)识别→拉黑该币,**绝不裸腿重试**。
- 腿序:正费率永续先行;负 carry 反转(先借卖现货再开永续)。
- 配置现状:`live_trading_enabled: true`,分配资金 60U,数据采集姿态(接受小额手续费亏损换执行数据)。

### 4.3 跨所价差扫描器(实盘,只读)
- 每 10 分钟并发拉四所全市场资金费 → 按各所真实结算间隔归一年化 → 同币跨所配对取最优价差 → 过滤 ≥30% 年化 → top 30。
- 输出:`GET /api/cross-venue/funding`、日志、Telegram 告警(≥100% 年化,同币-所对 4h 冷却)。
- 实测:整轮 ~46s(瓶颈 OKX 逐币限速),首轮即发现 30 个机会(头部 4000%+ 年化)。
- 快照内置 per-venue per-symbol 费率索引,供执行引擎复用(管理循环**不**现场拉全量)。

### 4.4 跨所对冲执行引擎(影子模式运行中)
- 策略:空高费所永续 + 多低费所永续,可交易腿 = **okx / gate / bybit**(币安搁置)。
- **双闸门**:`enabled`(已开)+ `live_trading_enabled`(**关,影子模式**)——影子跑完整决策链但绝不下单,日志输出「[影子] 本应开仓」与每轮闸门拒绝原因。
- 开仓闸门(全过才动手):价差≥150% 年化;两所合约活跃;**幽灵腿核验**(NextFundingTime 非零+两所中间价偏离<3%,防下架残留假费率);盘口价差≤30bps;名义≤头部 3 档深度 20%;两所可用 USDT≥名义/杠杆×1.5;有效杠杆≤2x;不与 Bybit 单所台账撞币;无重复持仓;`.halt-live` 未激活;同币 1h 冷却。
- 执行协议:极端所(高费)先腿,IOC 限价 ±20bps 封顶;第二腿按第一腿实际成交量对齐;**第二腿失败→立即 reduce-only 回滚第一腿**(裸腿>30s 升级 critical)。
- 持仓风控(60s/轮):孤腿检测(一腿消失→平另一腿+critical);两腿数量偏差>5%→补齐;强平距离<15%→两腿同步各减 50%(绝不单腿动);价差<30% 连续 3 轮或持仓>72h→平仓;**平仓必须核验两腿真平了才删台账**(平不掉保持 closing 下轮重试,≥3 轮告警)。
- 台账:`records/crossvenue/positions.json` 原子落盘,状态机 opening_first_leg→opening_second_leg→open→closing→closed(+orphan);崩溃重启对账恢复。
- 资金费记账:按真实间隔与真实流逝时间累加估计(展示用)。
- 持仓期间对 okx/gate 腿 60s 续期原生 dead-man。
- 默认参数:每腿 50U 上限、最多 2 并发。

---

## 5. 资金管理(面板「输入一个数→落位」)

| 所 | 状态 | 语义 |
|---|---|---|
| binance | ✅ 旧路径 | 25/25/50 钱包预切 |
| bybit | ✅ | 先平仓+清杂币 → 目标额进统一账户,余量回资金账户;目标持久化跨重启 |
| okx | ✅(今日交付) | 目标额进交易账户(6↔18 划转),余量留资金账户 |
| gate | ✅(今日交付) | 目标额进 USDT 永续账户(spot↔futures 划转),余量留现货账户 |

红线:**只做所内划转,永不跨所提币**。四所之间的本金分布由人工充值决定。
金额处理:向下截断 8 位小数、非科学计数法、亚分级差额静默跳过、目标超可调总额拒绝。

---

## 6. 安全与运维

| 机制 | 实现 |
|---|---|
| 进程守护 | systemd `Restart=always` + `lingxi-watchdog` timer(healthz 探活,带重启冷却) |
| 健康检查 | `/healthz` 多 venue 感知:**任一 venue 就绪即 200**(修复了 Bybit 单跑时恒 503 被误杀的问题) |
| 急停 | `.halt-live` 文件(全局/分所):停止开新仓,在场持仓继续管理;watchdog 识别为「有意暂停」不重启;已实弹演练 |
| dead-man | 币安/OKX/Gate 原生倒计时撤单;Bybit(set-dcp 仅机构)→ **启动清扫**:进程拉起后立即撤全部残留挂单 |
| 崩溃恢复 | 各引擎启动 reconcile:台账 vs 所内真实持仓对账;Bybit autostart 自动恢复执行任务 |
| 告警 | Telegram 双通道:引擎内 AlertDispatcher + watchdog 直推(读 secret.yaml);开/平/回滚/孤腿/风控全覆盖 |
| SELinux | 二进制与 watchdog.sh 需 `bin_t` 标签(semanage fcontext 已固化,重编译后 `restorecon` 即可) |
| API key 卫生 | OKX key 禁提币✅;诊断确认可交易;建议补绑 IP 白名单 |
| 限频纪律 | OKX 逐币 130ms pacing;面板快照 15s/60s 缓存;错误分类 TOO_MANY_REQUESTS→退避;扫描周期≥5min;同 IP/key 单进程 |

---

## 7. 可观测性

- 面板 `:8080`:venue 选择器(四所)、启停、调仓、日志、结算/监控/回测记录。
- 关键 API:`/api/cross-venue/funding`(价差机会)、`/api/cross-venue/positions`(跨所台账)、`/api/runtime/status|logs|capital`、`/api/venues`、`/healthz`、`/metrics`。
- 记录目录:`records/`(币安)、`records/bybit/`、`records/crossvenue/`,含结算流水、执行成本、事件、心跳。

---

## 8. 当前运行状态(2026-06-12)

| 组件 | 状态 |
|---|---|
| 币安主引擎 | 闲置(资金 1.4U,不参与 v1) |
| Bybit 执行器 | **实盘**,126U,数据采集姿态 |
| 跨所扫描器 | **实盘**,10min/轮,四所全绿 |
| 跨所执行引擎 | **实盘**(2026-06-12 起,单腿 30U、并发 1) |
| OKX 单所 funding 执行器 | **实盘**(30U 上限、并发 1) |
| Gate 单所 funding 执行器 | **实盘**(30U 上限、并发 1) |
| OKX 账户 | 交易账户 100U 已落位 |
| Gate 账户 | 合约账户 100U 已落位 |

### 8.1 实弹自检(2026-06-12,cmd/livecheck)

影子模式只能验证决策链,验证不了「交易所收不收单」。用微额真实往返单
(永续 IOC 开平 + 现货买卖,DOGE,~5U/87U 名义)对 OKX/Gate 做了全链路自检,
总成本几毛钱,炸出 4 个**只有实弹才能暴露**的适配器 bug(均已修复):

1. **OKX `FetchInstruments` 永续全军覆没**:SWAP 行情的 `quoteCcy` 为空(现货专属字段),按它过滤 USDT 本位 → 0 个永续。改用 `settleCcy`。
2. **Gate 现货盘口解析必炸**:现货 order_book 档位是 `["价","量"]` 数组,合约才是 `{p,s}` 对象,共用结构体解析现货直接报错。已分拆两种格式。
3. **Gate 现货下单回执 id 是字符串**(合约是数字),用 int64 解析会在**下单已成功后**报错 → 引擎误判失败 → 裸腿。改用 `json.Number`。
4. **Gate 现货查单 status 用错**:列表接口只认 `open/finished`(代码写了 `closed`),已成交 IOC 永远查不到 → 误判未成交 → 可能重复下单。已改为按自定义 ID 精确查单 + 正确状态兜底。

另外两个实弹教训(工具侧已适配,引擎侧需注意):
- **OKX 现货有价格保护带**(51137):限价偏移 >~0.5% 直接拒单,IOC 滑点预算不能拍太大。
- **OKX 现货买入手续费从收到的币里扣**:买 57.44 实收 57.38,按成交量原数卖回会报余额不足(51008),卖出量必须按实际余额算。

---

## 9. 待办(按优先级)

1. ~~OKX/Gate 充值+落位~~ ✅ 已完成(各 100U 在交易/合约账户)
2. ~~实弹验证~~ ✅ 已完成(livecheck 全绿,三引擎已切小额实盘:跨所 30U/腿×1 并发,OKX/Gate 单所 30U×1)
3. 借币可得性探测加进扫描器(只读):量化「本所借不到、别所能借且利率划算」的真实频率,数据决定是否建跨所借币执行链
4. 币安接入跨所可交易集合:停用/守卫币安老引擎 + 白名单(约一天,等影子期结论)
5. WebSocket 实时行情(降低轮询延迟,P1 之后)
6. `FLATTEN_ON_DEAD` 二阶段:观察 1–2 周零误报后启用
7. P2(新币上线事件流)、P4(干净度量+活动底仓)见 ROADMAP

---

## 10. 关键文件索引

| 路径 | 内容 |
|---|---|
| `internal/venue/{binance,bybit,okx,gate}/` | 四所适配器(各 ~15 文件,含测试) |
| `internal/venue/venue.go` / `conformance.go` | 统一接口与契约测试 |
| `internal/app/cross_venue_scanner.go` | 跨所价差扫描器 |
| `internal/app/cross_venue_{executor,gates,open,manage,qty,positions_store}.go` | 跨所执行引擎 |
| `internal/app/bybit_exec_*.go` / `bybit_runtime.go` | Bybit 精简执行器 |
| `internal/app/runtime_capital_*.go` | 面板调仓(四所) |
| `internal/app/venue_adapter_runtime.go` | okx/gate 编排接线 |
| `internal/app/observability_http.go` | healthz/metrics 多 venue 聚合 |
| `deploy/watchdog.sh` + `lingxi-watchdog.*` | 看门狗(halt 豁免+Telegram 直推) |
| `config.yaml` | venues、cross_venue_executor、bybit_executor 等全部开关 |
| `secret.yaml` | 四所 key + Telegram 凭证(权限 600,永不入库) |
