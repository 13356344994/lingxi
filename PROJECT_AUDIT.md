# lingxi 全面审计报告(2026-06-10)

> 视角:架构师 / CTO / 交易员 / 量化工程师 / 战略投资者。
> 前提:你当前的目标是「稳定交易 + 收集真实成本数据」,不是立刻盈利。
> 范围排除(你已明确不关注):密钥安全、经济性 edge(费率盖不住手续费的"学费")、上帝对象拆分与 HA 热备(已知架构债)。
> 所有问题均有代码或日志佐证,标注 `文件:行号`。FIX_PLAN.md 中已修复的 9 项不再重复。

> **修复状态(2026-06-10 第二批,与本报告同日执行):**
> C-1 ~ C-11、O-2 **已全部修复**(带回归测试,build/vet/test/-race 全绿)。
> O-1 已由重启解决;新二进制已重新构建,**重启后生效**。
> O-3(watchdog 安装)与 O-4(ENA/TAO 残腿人工核实)仍待人工执行;O-5 为配置对齐提醒。
> 各条目下方标注【已修复】及修复方式。

---

## 第一部分:运营现状(最紧急,先于一切代码问题)

### O-1【立即处理】线上跑的是两天前的旧二进制,两批已修 bug 全部没生效

- 现役进程 PID 552462,二进制构建于 **06-08 17:31,版本 `aff14b5-dirty`**(`records/lingxi_server.log` 启动行)。
- 此后有两批关键修复未上线:
  - `d1a4cbb`(06-09):救援自死锁、WS 断流 REST 兜底、单腿斩残腿、**滚动持有误清全组合(原 #1 致命)**、Bybit 负 carry、台账 fsync;
  - `9ae2337`(06-10):借币利息重复计费、部分成交对齐、Bybit reconcile 方向感知、裸永续误买现货、全局 panic 并发、生命周期桥接、滚动 4h 周期、撕裂读、救援缓存。
- **后果:你现在实盘暴露在所有这些已知 bug 之下**(例如滚动持有清仓时误删全组合台账)。
- `-dirty` 后缀说明当时构建还混入了未提交改动,线上代码与 git 历史不完全对应。
- **建议:重新 `go build` 并按面板流程重启,这是收益最大的单一动作。**

### O-2【高|已修复 06-10】崩溃后不会自动恢复实盘交易

- `/etc/systemd/system/lingxi.service` 里 `LINGXI_AUTOSTART_LIVE=1` 处于**注释状态**。
- 进程崩溃 → systemd 5 秒拉起 → 但只起 Dashboard,**实盘引擎不启动**,持仓(币安若有 + Bybit MBOX)无人管理,直到你手工点"启动实盘"。
- 凌晨崩一次 = 裸暴露数小时。reconcile 启动对账已经做了,就差这一行。
- 建议:取消注释(单写租约已防双开),或至少配好 Telegram 崩溃告警。

### O-3【高】watchdog 没有安装

- `deploy/lingxi-watchdog.service/.timer` 只存在于仓库,`/etc/systemd/system/` 下没有,`systemctl is-enabled lingxi-watchdog.timer` → 不存在。
- 进程"活着但卡死"(心跳停滞)的场景目前无人监督。
- 注意:安装前先修 C-4(Bybit 不写心跳,装了反而会误杀,见下)。

### O-4【中】币安残腿救援任务卡死 5 次未清

- `records/live_exposure_rescue_tasks.json`:ENAUSDT(247 个)与 TAOUSDT 两个负 carry 平仓任务,`Attempts=5` 耗尽,`KnownExecutedQty=0`,06-09 04:00 起搁置。
- 引擎已按设计锁定 symbol 防带残腿重开,但**需要人工去交易所核实这两个币是否真有残腿**(还掉借币/平掉现货),否则这两个 symbol 永久不可交易且可能有未还借币持续计息。

### O-5【低】资金配置与账户余额不匹配反复拒启

- 日志 3 次:「配置要求交易资金 300 USDT,但总可调仅 ~101」(06-07~06-08)。preflight 拦住了,行为正确,但说明 `config.yaml` 资金参数与真实账户脱节,改配置时记得对齐。

---

## 第二部分:本次审计新发现的代码缺陷(FIX_PLAN 之外)

### C-1【致命|已修复 06-10】币安 WS 订单回报字段被大小写覆盖:Symbol 变成 "BUY"、现货回报全被丢弃

Go `encoding/json` 对未声明的键做**大小写不敏感回退匹配**,JSON 中后出现的键覆盖先出现的。币安推送里 `"s"`(symbol)与 `"S"`(side)、`"c"`(clientOrderId)与 `"C"`(origClientOrderId)、`"i"`(orderId)与 `"I"`(ignore)并存,而结构体只声明了小写字段:

- 合约流结构体:`internal/app/user_data_stream.go:513-524`(只有 `json:"s"`,没有吸收 `"S"` 的字段)
- 现货流结构体:`internal/app/user_data_stream.go:420-437`(同病,且缺 `"C"`、`"I"` 吸收字段)

最小复现(已在本机验证):

```go
// 合约 ORDER_TRADE_UPDATE: {"s":"ZECUSDT",...,"S":"BUY",...}
// 解析结果 → Symbol="BUY"
// 现货 executionReport: {"s":"...","c":"lx-spot1","S":"BUY",...,"C":"",...,"i":4293153,...,"I":8641984,...}
// 解析结果 → Symbol="BUY", ClientOrderID="", OrderID=8641984(I 的垃圾值)
```

**实际后果(有日志实锤):**

1. **现货 WS 确认通道整体失效**:`ClientOrderID` 被 `"C":""` 清空 → `isManagedClientOrderID("")` 为 false → 所有现货回报被当作非托管单丢弃(`user_data_stream.go:454-456`)→ 现货成交确认 100% 退化为 REST 轮询兜底(0.4~2s 延迟),在结算秒级窗口内拖慢两腿对齐。
2. **合约真实手续费查询失败**:snapshot.Symbol="BUY" → `/fapi/v1/userTrades?symbol=BUY` → `Invalid symbol`。日志 6 次:`查询合约成交手续费失败,symbol=BUY`(`engine_fees_market.go:72-77`)→ 这些笔的真实手续费回退到估算值,**直接污染你要收集的成本数据**。
3. **强平/ADL 检测(R3)实际失效**:`handleFuturesForcedClosure(envelope.Order.Symbol, ...)` 拿到的 symbol 是 "BUY"(`user_data_stream.go:554-556`),真发生强平时找不到对应持仓,单腿裸露无感知——README 标记 R3"已修复"并不成立。
4. `futuresExecutionToResult` 的 `nonEmpty(snapshot.Symbol, order.Symbol)` 回退失效("BUY" 非空,优先级反而高,`engine_orders.go:204`)。

**修法(小改动)**:两个匿名结构体补声明 `Side string \`json:"S"\``、现货补 `OrigClientOrderID string \`json:"C"\`` 和 `Ignore int64 \`json:"I"\``,让精确匹配吸收掉这些键;加一条用真实币安报文样例的反序列化单测。

### C-2【致命→数据|已修复 06-10】domain_events.jsonl 压缩永不生效,文件无限增长 + 每次追加白白重写全文件

- 写入的事件只有 `OccurredAt`(无 json tag → 序列化为 `"OccurredAt"`,`event_contract.go:37-48`);
- 压缩逻辑只认 `row["recorded_at"]`(`internal/store/fileio.go` 的 `CompactJSONLByRecordedAt`);
- 结果:**没有一行能匹配保留期裁剪 → 永不压缩**;且文件已超 8MB 阈值,**每次 append 都会全量扫描+重写 32MB 到 tmp 然后丢弃**(O(N²) I/O)。现状:`domain_events.jsonl` 32MB 且持续膨胀,目录里反复出现 30MB 级 `.tmp` 残留。
- 修法:落盘时补 `recorded_at` 字段,或压缩函数兼容 `OccurredAt`;清理现存 tmp。

### C-3【高|已修复 06-10】Bybit 结算/平仓数据不进 ReportStore,两所成本口径不可比

- 币安:`settlements.json` + journal + TRADE_DETAIL + pending funding worker 完整闭环;
- Bybit:平仓盈亏只打日志 + Telegram + `records/bybit/execution_costs.jsonl`(best-effort,写失败仅告警、无 fsync,`bybit_exec_live.go:152-158`),**不写 settlements**(`bybit_runtime.go` 平仓路径无 `AppendSettlement`)。
- 对"收集数据再开始赚钱"的目标,这意味着 Bybit 的学费数据没有结构化台账,事后只能从日志手工拼。
- 修法:Bybit 平仓后写同 schema 的 SettlementRecord(带 venue 标签)。

### C-4【高|已修复 06-10】Bybit 实盘不写 heartbeat.json;watchdog 一旦安装会误杀

- 心跳只由币安引擎写(`funding_sniper_engine.go:164` / `observability_http.go`);Bybit 执行器循环不写。
- `deploy/watchdog.sh:104-112` 要求心跳新鲜度,缺失按 999999s 处理 → **Bybit 单所模式 + watchdog = 健康进程被连续重启**。
- 与 O-3 联动:先修这个,再装 watchdog。

### C-5【高|已修复 06-10】Bybit 启动无 preflight:不校时、不看真实余额

- 币安启动校验服务器时间偏移(>5s 拒启)与资金充足性;Bybit 路径显式跳过(`runtime_controller.go:314-323`)。
- Bybit 签名直接用本地 `time.Now()`,recvWindow 5000ms(`internal/venue/bybit/transport.go`);VPS 时钟漂移 >5s → 全部请求签名拒绝,且无自愈。
- planner 只信配置 `AllocatedUSDT`,从不 `FetchBalances` 对照(`bybit_exec_runner.go:125`)→ 配置与真实余额脱节时按名义下单,制造无谓失败样本。

### C-6【高|已修复 06-10】Bybit HTTP 无重试、manage 循环一错弃整轮

- transport 8s 硬超时无退避重试;`bybitManage` 循环里 `PlanCycle` 失败直接 return,本轮平仓/续持全部跳过,等下个 30s tick(`bybit_runtime.go:390-393`)。
- 临近结算的网络抖动会让该结算周期的关键动作(平仓)整体顺延。

### C-7【中|已修复 06-10】:58/:59 取价风暴超时,决策用不到最新盘口

- 日志 3 天 286 次 `context deadline exceeded`,集中在每小时 :58~:59(`补齐深度失败`/`补齐 bookTicker 失败`)。
- 根因:补齐逻辑对每个监控标的无上限地起 goroutine 并发拉 REST(`funding_sniper_engine.go:1005` 起,`wg.Add(1)` 每标的一个,无信号量),共享同一个 ctx 截止时间,高峰期互相挤兑超时。
- 后果:恰好在开仓决策最需要新鲜盘口的那一分钟,部分标的拿不到数据被跳过或用旧缓存。
- 修法:加并发上限(如 8)+ 给补齐单独的宽限 ctx + 提前到 :55 预热。

### C-8【中|已修复 06-10】executionIncidents 计数:atomic 写 + 裸读混用

- 写:`atomic.AddInt32`(`engine_open_exec.go:589`);读:`int(e.executionIncidents)` 裸读至少 8 处(`exposure_manager.go:356,419,426`、`live_safety.go:430,449,572,600`、`realtime_drawdown.go:716`)。
- `-race` 会报;熔断阈值比较在高压下读到旧值。统一 `atomic.LoadInt32` 即可。

### C-9【中|已修复 06-10】settlement worker 与风控状态推进非原子

- 先 `UpdateSettlement` 再 `noteCycleFinalSettlement`/`noteDailySettlement` 两步(`funding_settlement_worker.go:195-209`),测试注释自认竞态窗口(`funding_settlement_worker_test.go:155-160`)。
- 窗口内实时 drawdown 读到滞后的日盈亏/连亏计数,可能误触发/漏触发熔断。

### C-10【中|已修复 06-10】Bybit 开仓成功但台账落盘失败 → 只告警不自救

- `OpenDeltaNeutral` 成功后 `store.upsert` 失败只打日志(`bybit_runtime.go:437-438`);此时崩溃 = 交易所有仓、本地无账;reconcile 对孤儿仓只告警不接管(`bybit_runtime.go:239-247`)。
- 修法:落盘失败转入"只平不开"并高频重试;孤儿仓匹配本机 idem 前缀(`lx-` 开头)则自动收编。

### C-11【低|已修复 06-10(除 runtime_events I/O 与 BindConfig 注意项)】杂项卫生

- `heartbeat.json` 写入无 fsync、rename 失败 tmp 残留(`observability_http.go:82-91`);现存 8 个 `live_daily_risk_state.json.*.tmp` 残留佐证非优雅退出频繁发生。
- `settlements_journal.jsonl`/`pending_funding_journal.jsonl` append-only 无轮转(目前为空,长跑后注意)。
- `runtime_events.jsonl` 单日 42MB:按天轮转+保留期清理是好的,但每条日志一次 open/写/close,量大;建议降低重复打印(:58 风暴一次打 13 行同类错误)。
- watchdog 的 `FLATTEN_ON_DEAD` 只看 `live_open_hedges.json`,不看 `bybit_positions.json`(`deploy/watchdog.sh:41-42`)。
- `BindConfig` 跳过空值 → YAML 无法显式"清零覆盖"默认值(`config_bind.go:97-98`),改配置时注意"删键=回到默认"。
- funding worker 用 `time.Now()` 而非 `serverNow()`(`funding_settlement_worker.go:74,138`),影响极小。

---

## 第三部分:旧日志核对(records/lingxi_server.log,06-06 ~ 06-09)

| 日志症状 | 次数/最后出现 | 新版状态 |
|---|---|---|
| Bybit 现货平仓 `170137 too many decimals` | 06-08 11:28 后消失 | ✅ 已修(精度对齐),重启后未复发 |
| `查询合约成交手续费失败 symbol=BUY` | 6 次,最近 06-09 03:59 | ✅ 已修(C-1 字段显式声明),重启后生效 |
| `:58/:59 补齐深度/bookTicker 超时` | 286 次,持续发生 | ✅ 已修(C-7 并发上限 8),重启后观察 |
| 启动资金 preflight 拒启(300 vs 101) | 3 次,06-08 14:56 | ⚠️ 行为正确,配置需对齐(O-5) |
| ENA/TAO 救援耗尽搁置 | 06-09 04:00 | ⚠️ 待人工核实(O-4) |
| 进程重启 31 次(无 panic 记录) | — | 多数为人工重启;但 tmp 残留说明常有非优雅退出 |

---

## 第四部分:战略评估(投资者/CTO 视角,只说现实)

**做对了的**(在同类个人项目中少见):幂等下单 + ClientOrderID 命名空间、启动 reconcile、双层熔断、execution incidents 台账、决策痕迹(scan_observations)、敢写回归测试(127 个测试文件)、FIX_PLAN 这种工程纪律。

**当前真正的瓶颈不是策略,是数据闭环**:你的目标是"先收数据"。但今天:币安侧真实手续费有 C-1 污染、Bybit 侧没有结构化结算台账(C-3)、domain_events 不可维护(C-2)。**先把"每一笔交易的真实四件套——开仓名义、实扣手续费、实收资金费、平仓盈亏——在两所统一 schema 落盘"做到 100% 可信,数据收集期才有意义。**

**运营纪律 > 新功能**:O-1(旧二进制)说明发布流程缺一个"修完即部署"的闭环。建议固定节奏:修复 → 测试 → 构建 → 重启 → 在面板确认版本号,五步一气呵成,否则修复只存在于 git 里。

**建议优先级**(稳定+数据导向)——*06-10 进度标注*:

1. ~~重建二进制并重启(O-1)~~ ✅ 已重启;~~开 AUTOSTART(O-2)~~ ✅ systemd 已开启(下次用 `systemctl start lingxi` 启动才受保护)
2. ~~C-1 WS 字段覆盖~~ ✅
3. ~~C-2 domain_events 压缩键~~ ✅
4. ~~C-3 Bybit 结算落盘~~ ✅
5. ~~C-4/C-5/C-6 Bybit 健壮化~~ ✅;装 watchdog(O-3)**待执行**(心跳前置条件已满足)
6. ~~C-7~C-10~~ ✅
7. 人工核 ENA/TAO 残腿(O-4)**待执行**

## 附 2:第二批修复摘要(2026-06-10)

| 项 | 修复方式 | 回归测试 |
|---|---|---|
| C-1 | 合约/现货流结构体显式声明 `S`/`C`/`I`;撤单回报按原订单 `C` 归属;合约解析抽成 `processFuturesUserDataEvent` 可测 | `TestProcessFuturesUserDataEventParsesFullBinancePayload` 等 3 个(真实键序报文) |
| C-2 | 压缩键兼容 `OccurredAt/occurred_at/at`;`domain_events`+`settlements_journal` 纳入启动压缩;清理 9 个孤儿 tmp | `TestCompactJSONLByOccurredAtCompactsDomainEvents` |
| C-3 | Bybit 平仓后写同 schema `AppendSettlement`(真实资金费/两腿价差拆分/实现净利;查不到流水退估算并标 pending) | `TestAppendBybitSettlement*` 2 个 |
| C-4 | Bybit 循环每 tick 写 `records/bybit/heartbeat.json`;watchdog 取各 venue 心跳最新者 | — |
| C-5 | `SyncServerTime` 同步签名时钟(启动+每小时,偏移>2s 告警);开仓前 `FetchBalances` 真实余额裁剪 | `TestSyncServerTimeAdjustsSignTimestamp` |
| C-6 | GET 幂等重试 ≤3 次(网络错/5xx/限频 10006,退避 300ms×n,时间戳每次重算);业务错误不重试 | `TestDoRetriesOn5xx`/`TestDoDoesNotRetryBusinessError` |
| C-7 | 补齐取价并发上限 8(信号量) | — |
| C-8 | 8 处裸读统一 `atomic.LoadInt32` | `-race` 全绿 |
| C-9 | 日内盈亏增量与最终净利信号合并到同一 `platformRiskMu` 临界区(`noteCycleFinalSettlementWithDailyDelta`) | 既有 worker 测试通过 |
| C-10 | 落盘失败转内存兜底队列(每 tick 重试+并入管理视图,绝不失管);reconcile 孤儿仓按确定性 orderLinkId 回查 48 个整点,命中即收编待平,不命中仅告警 | `TestBybitProbeOrphanOwnership*`/`TestBybitPersistPositionFallsBackToMemory` |
| C-11 | 心跳走 tmp+fsync+rename;journal 纳入压缩;watchdog 兜底强平核对 `bybit_positions.json`;funding worker 改 `serverNow()` | — |
| O-2 | `deploy/lingxi.service` 与 `/etc/systemd/system/lingxi.service` 开启 `LINGXI_AUTOSTART_LIVE=1`(root/`/root/lingxi` 路径,`daemon-reload` 已执行) | `systemd-analyze verify` 通过 |

---

## 附:文档清理记录(2026-06-10,与本报告同批执行)

- `DEPLOY_HANDOFF.md`:**已删除**。部署快照全面失实(config 数值、commit 标签、面板绑定 127.0.0.1 vs 现网 0.0.0.0、无多所内容),有效信息已被 `deploy/RUNBOOK.md` 与 `MULTI_VENUE_REFACTOR.md` 覆盖。
- `README.md`:删除/修正与代码矛盾的断言(单所叙事、"配置硬编码待引入 config.yaml"、R2/R3/R4/R12/R17/R20/R24 在 §十二 标"未实现"但 §十八 已标修复 等自相矛盾)。注意:R3 强平检测因 C-1 实际仍失效,已如实标注。
- `PROJECT_OVERVIEW.md`:修正"单体币安系统/仅 2 个外部依赖/140+94 文件"等过时事实为多所架构与实测规模(304 源文件 + 127 测试,~68k LOC,依赖 gorilla/websocket + yaml.v3)。
- `MULTI_VENUE_REFACTOR.md`:勾掉已完成但 checklist 未同步的 P4 Bybit 调仓项;`reconcile/shadow --venue` 维持未完成标记(属实)。
- `IMPROVEMENT_PLAN.md`:文首加现状注记(已多所、config.yaml 已存在),阈值表标注为 defaults 而非现网配置。
- `FIX_PLAN.md`:保留,内容与 06-09/06-10 两批修复一致。
