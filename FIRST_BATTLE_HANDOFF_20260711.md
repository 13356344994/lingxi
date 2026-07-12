# LINGXI 首战完全体交接文档

> 交接时间：2026-07-11 17:52（UTC+8）  
> 交接原因：用户更换电脑，需要另一台 Agent 无障碍接手。  
> 本文件是本轮停止指令后唯一允许新增的文件。所有写入型子任务均已中止。

## 0. 一页结论

当前系统不是可开真钱的盈利系统，必须维持 **`NO_GO` + 自动方向真钱封火**。

已经完成并推送的基础重构解决了：

- 自动新开仓物理封锁；
- 资金流与权益错序导致的提现误判；
- 订单状态、幂等、WAL、同币 claim、启动真仓恢复；
- OKX 私有 WS 代码路径；
- 三时钟数据真相、PIT universe、一次性密封考试；
- 真实执行回放、机制专家、极端仓位、OOD、账户分拣；
- 首战机制 shadow 与 NO-GO scorecard。

但盈利尚未被证明。当前第一研究主线是：

> 同时、对称地检验“强平接力顺势”与“强平竭尽反转”，不预设哪边正确。

当前研究阶段代码处于**未提交且可能无法编译**的中断状态，原因是双锚实现子任务被用户明确叫停。下一位 Agent 首先要恢复编译和口径一致性，不能直接跑真实数据或消费考卷。

---

## 1. 用户的不可违背目标

### 1.1 最终目标

- 本金范围：几十 U 到最多几千 U；
- 交易所杠杆上限：5 倍；
- 动态微仓、高并发；
- 最终目标：不同市场情况由不同方向专家工作，账户长期净值增长；
- 不建设面向任意资金规模的通用平台；
- 不以架构漂亮、程序稳定或策略数量作为成功指标；
- 只认资金流调整后的真实账户净值。

### 1.2 最高交易宪法

1. **调研定方向**
   - 方向必须由机制研究决定；
   - 必须回答谁被迫买卖、谁支付利润、为何不能等待。

2. **极端定仓位**
   - 仓位由最坏可信场景决定硬上限；
   - 不能由固定 5U/7.5U、胜率或平均波动决定；
   - 高并发必须按相关簇/BTC beta/同方向压力计算，不能把 30 个相关币当 30 个独立下注。

3. **天道定信心**
   - 信心来自强平、保证金、结算、被迫调仓、套利收敛等不可轻易绕开的结构约束；
   - 信心只能缩小仓位，绝不能突破极端硬上限。

4. **只认止错，不认机械价格止损**
   - 策略退出由论点证伪、期限结束、机制完成、数据失真决定；
   - 但必须有不可关闭的偿付能力防线，防止强平和账户毁灭；
   - 偿付减仓不是行情止损。

5. **开放世界**
   - 市场不是有限天气表；
   - 未知/OOD/数据红灯必须强制空仓；
   - 完全体不是认识所有未来，而是遇到未知未来也不会死亡。

6. **大模型时代约束**
   - 任何可由通用大模型仅凭公共数据轻易发现的策略，默认净 edge 为零；
   - 必须证明被迫付款机制、低容量优势、独特数据或特殊执行优势；
   - 大模型只能提出/审判假设，不能自己出题、调参、阅卷并批准真钱。

### 1.3 用户明确反对

- 不要被高可用和复杂架构迷惑；
- 不要继续沿着当前 8 格/key/casc/sqz 的思路机械优化；
- 不要等待未来几年才找答案；
- 不要固定单仓名义；
- 不要无限重复同一测试集；
- 不要让研究 Agent 记住考卷答案；
- 不要把充值/提现算作亏损；
- 不要恢复近期“只做多、越跌越抄底”的退化状态。

---

## 2. 已确认的核心事实

### 2.1 资金转出误触发

2026-07-11 的 `-22.35U` 是手动转出，不是交易亏损。

旧自主风控出现竞态：

1. 资金流事件先到；
2. 权益快照仍是旧值；
3. 高水位先下调又被旧权益抬回；
4. 权益后来下降时资金流增量已为零；
5. 被错误计算成 38.9% 回撤并触发全平。

已在提交 `75d3c99` 中修复为事件账本 + pending 双向匹配 + clean frontier。

### 2.2 历史可见账本

可复算的现存账本大致为：

- OKX 方向：约 `-15.373U`；
- Binance 方向：约 `-0.997U`；
- Bybit 方向：约 `-10.671U`；
- Gate 方向：约 `-9.813U`；
- 四所方向约 `-36.855U`；
- crossvenue 资金费约 `+9.471U`；
- 可见账本合计约 `-27.61U`。

注意：

- 历史账本可能不完整；
- 净转出不是交易亏损；
- 不得把“累计净出金”称为学费。

### 2.3 近期只做多的根因

- 历史并非从未做空；
- 唯一做空枪 `key_sqz_fade` 20 笔亏损后 `stalled`；
- `key_casc_fade` 只会做多并继续运行；
- 做空能力死亡后，方向暴露由“谁没死”决定，而不是由市场判断决定；
- casc 在多头强平增加时开多，本质上可能在下跌最猛烈时接飞刀；
- 天气格只打标签，旧 `fast_key_grid_gate_enabled=false`，未形成真钱分拣。

### 2.4 maker/撤单事实

key maker：

- 尝试 269 次；
- 成交 79 次；
- 成交率约 29%；
- 约 71% 撤单。

撤单本身不是主要亏损：

- 未成交反事实整体未证明稳定机会成本；
- 快速（<=1s）成交桶显著更差，存在毒性成交；
- 目标不能是提高成交率，而应是提高“成交后的条件净收益”。

---

## 3. Git 状态与安全基线

### 3.1 已推送提交

```text
75d3c99 (HEAD -> main, origin/main, origin/HEAD)
84 首战完全体原地重构:封死自动新仓并重建资金/订单/数据真相;接入机制专家、极端仓位、密封考场、真实回放与NO-GO scorecard;OKX私有WS/真仓恢复/幂等WAL全链测试绿,真钱仍保持封火
```

远端已确认与本地一致。

### 3.2 该提交包含

- `internal/risk/equity_truth.go`
- `internal/execution/`
- `internal/datatruth/`
- `internal/exam/`
- `internal/replay/`
- `internal/mechanism/`
- `internal/decision/`
- `internal/portfolio/`
- `internal/scorecard/`
- `cmd/firstbattle/`
- `cmd/sealedexam/`
- OKX 私有 WS/用户事件/启动恢复；
- 首战机制 shadow；
- `records/firstbattle_exams.json`
- `records/firstbattle_scorecard.json`
- 生产配置自动真钱封火。

### 3.3 提交前已通过

```bash
go test ./... -count=1
go test -race ./internal/... -count=1
go vet ./...
git diff --check -- config.yaml internal cmd/firstbattle cmd/sealedexam
```

### 3.4 当前未提交研究阶段

以下为新阶段未跟踪目录：

```text
research/
internal/gauntlet/
internal/mechanismpanel/
internal/mechanismresearch/
cmd/firstbattle-gauntlet/
cmd/mechanism-panel/
cmd/mechanism-research/
cmd/okx-private-smoke/
```

另外工作区长期存在大量实时 records、PID、控制令牌、历史数据、旧文档和 capcarry 未提交改动。不要把它们一起提交。

特别不要提交：

- `secret.yaml`
- `.dashboard_control_token`
- `.lingxi.pid`
- `.lingxi-live.lock`
- 实时日志；
- `records/history/`
- 大型 JSONL；
- `records/okx_directional/firstbattle_mechanism_shadow.jsonl`
- 编辑器历史中的密钥副本。

### 3.5 与本轮研究无关、早已存在的未提交代码

```text
HANDOFF.md
cmd/capcarry/main.go
cmd/capcarry/main_test.go
```

除非用户明确要求，不要在下一次研究提交中夹带这些文件。

---

## 4. 已完成的首战基础设施

### 4.1 自动真钱封火

- `config.yaml` 中方向/carry 自动 live 开关关闭；
- 探针/持证名单清空；
- `internal/app/directional_live_executor.go` 是单一方向新开仓执行门；
- 自动考试、auto_miner、旧自治生命周期无权自动晋级真钱；
- verified close/reduce-only/exchange truth flatten 保留；
- 单写租约覆盖方向栈；
- 旧 key/fast 可以继续记录 shadow，但不能新开真钱。

### 4.2 资金/权益真相

- `FundFlowEvent`
- `EquityObservation`
- `PendingFlow`
- 15 分钟匹配窗；
- 5U 大额候选门；
- flow-first/equity-first 双向匹配；
- Raw/Trading/Solvency 三套权益；
- clean frontier；
- 重启幂等事件 ID；
- 只允许 real 资金流修正高水位，heuristic 不再永久抹掉真实亏损。

### 4.3 订单真相

- Intent/Order/Fill/Position 状态机；
- WAL；
- 同币原子 claim；
- trade ID 幂等；
- UNKNOWN/quarantine；
- 启动交易所真仓屏障；
- 私有 orders/positions/fills 事件；
- OKX 登录、订阅、心跳、重连代码；
- 私有流失败只维持 shadow/manage-only，不会恢复新开仓。

### 4.4 数据、考试和 scorecard

- 三时钟 event envelope；
- 重复/乱序/gap 标记；
- PIT instrument/universe；
- 数据/特征/实验/部署哈希；
- 一次性考卷；
- HMAC receipt；
- 多卷 gauntlet 新阶段代码；
- 首战 scorecard。

当前已提交 scorecard：

```text
status: NO_GO
legacy settlements: 1112
legacy net: -15.373158896242876 U
maker attempts: 29849
firstbattle shadow rows: 500
accepted theses: 0
force-flat/OOD rows: 491
signed sealed exam pass: 0
```

这不是失败的基础设施，而是正确 fail-closed：没有盈利证据就不允许真钱。

---

## 5. 当前下一阶段任务状态

### 已完成但未提交

1. 强平机制预注册草案：
   - `research/firstbattle/forced_liquidation_preregistration.md`
   - `research/firstbattle/forced_liquidation_preregistration.json`

2. 多卷考试编排：
   - `internal/gauntlet/`
   - `cmd/firstbattle-gauntlet/`

3. 机制研究引擎基础：
   - `internal/mechanismresearch/`
   - `cmd/mechanism-research/`

4. OKX 私有 WS 只读冒烟工具：
   - `cmd/okx-private-smoke/`

5. 流式机制面板基础：
   - `internal/mechanismpanel/`
   - `cmd/mechanism-panel/`

### 当前中断且可能无法编译

用户叫停前，双锚子任务改了：

```text
internal/mechanismpanel/types.go
internal/mechanismpanel/episode.go
internal/mechanismpanel/preregistration.go   # 新增
```

子任务明确报告：

- 双 phase 核心逻辑未完整；
- research phase 隔离未完成；
- CLI/预注册同步未完成；
- 双锚断点恢复与无泄漏测试未完成；
- 未格式化、未运行测试；
- 当前代码可能无法编译。

不要直接运行真实数据。

### 我在子任务停止前已完成但未提交的修正

- checkpoint 改为使用 merger 已实际 emitted 行数，而不是预读 LinesRead；
- checkpoint offset 支持跨多次 resume 累计；
- 补齐 `cmd/mechanism-panel`；
- 补齐 CLI fixture 与进度测试；
- research 支持物理分离的 features/labels 输入；
- 缺 ctVal/OI/depth/capacity 的 episode 强制剔除；
- research 阈值改成 absorption `0.60`、remaining ratio `0.20`；
- panel/research 小 fixture 曾经通过，但**之后双锚子任务又修改了类型和 episode，必须重新测试**。

---

## 6. 为什么需要双锚

不能用同一张“episode 结束时特征”同时验证：

- relay continuation（强平仍在加速时顺势）；
- exhaustion reversal（强平结束/衰减后反向）。

否则 relay 会偷看“后来强平结束”的信息。

正确设计应有两个独立 candidate：

1. **Relay anchor**
   - 累计被迫名义达到冻结门；
   - payer/方向明确；
   - 被迫流仍加速；
   - 决策锚只使用当时可见数据；
   - 独立 phase ID 和未来标签。

2. **Exhaustion anchor**
   - 最后强平后 gap 被确认；
   - 被迫流衰减；
   - 剩余燃料/吸收条件满足；
   - 决策时刻必须包含“等待 gap 确认”的现实延迟；
   - 独立 phase ID 和未来标签。

两个锚中途不得共享未来标签或后验信息。

---

## 7. 红队发现的 12 个放行前硬门

这些是下一位 Agent 必须逐项转成测试的验收断言。

1. **ReceiveTime 截断**
   - `ReceiveTime > DecisionTime` 的记录不得进入特征；
   - 不能只按 EventTime 截断。

2. **坏质量 episode 剔除**
   - out_of_order、late、gap、clock_invalid、correction 必须剔除或独立报告；
   - 不能只留标记后继续研究。

3. **标签时点误差**
   - horizon 标签必须取固定 horizon 附近报价；
   - 必须有 `max_label_staleness`；
   - 不得把远晚于 horizon 的第一笔报价伪装成该 horizon。

4. **feature/label 一一对应**
   - EpisodeID、Instrument、DecisionTime 必须全部一致；
   - label 文件重复 ID 必须报错，不能后写覆盖。

5. **remaining 语义**
   - 必须是决策时可获得的未释放量估计；
   - 不能仅把 `SecondHalfNotional` 改名为 remaining 后当独立证据；
   - 若用代理，必须在预注册中冻结公式和局限。

6. **研究与线上专家同构**
   - research eligibility 与 `PanicLiquidationExpert` 必须同构；
   - 若不同，必须明确是不可迁移的新机制 generation。

7. **跨所确认方向一致**
   - 只计与 OKX 主导强平方向一致、且事件/接收延迟在预注册窗口内的跨所事件；
   - 反向强平不得增加确认或分母。

8. **源内乱序**
   - 特征切片不能假设源内天然单调；
   - 乱序必须重排、隔离或拒绝；
   - 不能用 `break` 隐式假设后续更晚。

9. **walk-forward 时间 purge/embargo**
   - 必须按时间和最大标签 horizon；
   - 不能在 hypothesis 筛选后的样本里按条数跳过。

10. **真实 shock cluster**
    - cluster 不能全部写死为 `panic_liquidation`；
    - 少于两个独立 cluster 时不得报告有效 cluster CI。

11. **运行参数与哈希进入预注册**
    - feature version/hash；
    - label rule/staleness/horizon；
    - cost；
    - episode threshold；
    - exclusion；
    - seed；
    - input manifest hash；
    - 不能只靠 CLI 临时参数。

12. **checkpoint 与输出原子性**
    - checkpoint 绑定输入文件身份和完整 config；
    - 恢复时拒绝 hash/config 不一致；
    - feature/label flush 成功后才能推进 offset；
    - 重跑不得 append 重复行。

---

## 8. 预注册草案当前不一致

当前 `research/firstbattle/forced_liquidation_preregistration.json` 与代码仍有潜在冲突：

- manifest：
  - `merge_gap_ms = 60000`
  - `max_episode_duration_ms = 3600000`
  - `min_trigger_notional_usdt = 50000`
  - `feature_lag_ms = 250`
  - `max_label_horizon_ms = 1800000`
- 旧 panel 默认曾是：
  - gap 20s；
  - duration 5m；
  - labels 30/60/120/300s。

双锚子任务新增了 `Unified*` 常量，意图对齐 manifest，但实现尚未完成。

另外预注册的旧文字仍可能写：

- `t0=首个触发事件`
- 特征只使用 `<=t0-feature_lag`
- 标签 anchor=`t0`

这与双锚 phase 的 `AsOfTime/DecisionTime` 设计不完全一致，下一位 Agent 必须在**查看任何真实收益前**修订并生成新 protocol hash。

当前没有消费新的强平机制考卷，因此仍可修订；修订后必须冻结 generation，之后不能再改。

---

## 9. 多卷考试公正规则

用户明确要求：可以多次考试，但必须公平、不能记答案。

规则：

- 可以有 A/B/C/执行卷/压力卷/最终黄金卷；
- 每张卷只能使用一次；
- 固定顺序，不能挑好行情；
- 同一 generation 连续考试，中途不能改代码/参数；
- 任一修改生成新 generation；
- 已使用考卷永久作废；
- 考官只返回签名 pass/fail 和粗粒度标准原因；
- 不返回逐笔、分桶、阈值附近答案；
- 研究 Agent 无权读取考卷文件、时间范围、样本分布和详细日志；
- 最终黄金卷只使用一次；
- HMAC 只是当前本地签名边界，真正生产密钥需独立 secret manager；
- 当前 `LINGXI_EXAM_SIGNING_KEY` 未配置。

---

## 10. 下一位 Agent 的严格接手顺序

### 步骤 1：保持停火

先检查：

```bash
git log -1 --oneline
git status --short --branch
```

确认：

- HEAD/remote 为 `75d3c99`；
- 不修改 `records/control_plane.json` 去解除 halt；
- 不恢复 `fast_probe_source`；
- 不启动任何真钱探针。

### 步骤 2：恢复研究阶段编译

仅针对未提交新阶段：

```bash
gofmt -w internal/mechanismpanel internal/mechanismresearch cmd/mechanism-panel cmd/mechanism-research internal/gauntlet cmd/firstbattle-gauntlet cmd/okx-private-smoke
go test ./internal/mechanismpanel ./cmd/mechanism-panel ./internal/mechanismresearch ./cmd/mechanism-research ./internal/gauntlet ./cmd/firstbattle-gauntlet ./cmd/okx-private-smoke -count=1
```

预期当前可能失败，先修双锚未完成代码。

### 步骤 3：逐条实现红队 12 断言

- 每条至少一个失败 fixture + 一个通过 fixture；
- 不允许只加注释；
- 不允许先跑真实收益后再改阈值。

### 步骤 4：冻结新 protocol

- 修正预注册与代码；
- 生成 protocol hash；
- 固定输入 manifest schema；
- 固定 relay/exhaustion phase；
- 固定成本、horizon、staleness、cluster；
- 固定失败标准；
- 冻结后不得再修改。

### 步骤 5：只跑小 fixture 和全量测试

```bash
go test ./... -count=1
go test -race ./internal/... -count=1
go vet ./...
```

### 步骤 6：准备真实数据，不直接跑

需要的输入：

- `records/okx_directional/fast_liquidation_events.jsonl`
- `records/okx_directional/fast_xvenue_liq_events.jsonl`
- `records/okx_directional/fast_market_mids.jsonl`
- `records/okx_directional/fast_xmeta_snapshots.jsonl`
- 深度输入（现有 raw schema 是否兼容需先验证）
- PIT instrument catalog（必须包含 ctVal）

若 ctVal/OI/depth 缺失，episode 必须剔除，不能用 1 伪造 ctVal 后出结论。

### 步骤 7：小规模真实 smoke

首次真实面板只能：

- 单一日期；
- `--max-lines` 有上限；
- `--progress-every` 开启；
- checkpoint 独立路径；
- features/labels 新文件；
- 不允许消费考试卷。

示意命令（具体参数以修复后的 CLI 为准）：

```bash
go run ./cmd/mechanism-panel \
  --liquidations records/okx_directional/fast_liquidation_events.jsonl \
  --xvenue-liquidations records/okx_directional/fast_xvenue_liq_events.jsonl \
  --market-mids records/okx_directional/fast_market_mids.jsonl \
  --xmeta-oi records/okx_directional/fast_xmeta_snapshots.jsonl \
  --instruments <PIT_INSTRUMENT_CATALOG> \
  --date 2026-07-10 \
  --max-lines 200000 \
  --progress-every 10000 \
  --checkpoint records/research/firstbattle-panel-20260710.checkpoint.json \
  --features-out records/research/firstbattle-features-20260710.jsonl \
  --labels-out records/research/firstbattle-labels-20260710.jsonl \
  --finalize
```

### 步骤 8：研究区/WF，仍不考试

```bash
go run ./cmd/mechanism-research \
  --features <FEATURES> \
  --labels <LABELS> \
  --horizon-ms <FROZEN_HORIZON> \
  --mode research \
  --cost-bps <FROZEN_COST> \
  --progress-every 1000
```

必须先看数据质量和有效独立 episode 数，不能先看收益。

### 步骤 9：冻结 generation 后多卷考试

- 只有 research/WF 合格才进入 gauntlet；
- 考试进程使用隐藏考卷；
- 研究 Agent 只接收签名最小 scorecard；
- 失败不能复用同卷。

### 步骤 10：实时 shadow

- 历史多卷过门后才接实时 shadow；
- 仍不下单；
- scorecard 通过后向用户申请最小真钱；
- 用户未单独批准前不得解除封火。

---

## 11. OKX 私有 WS 只读冒烟

工具：

```text
cmd/okx-private-smoke/
```

默认 dry：

```bash
go run ./cmd/okx-private-smoke -secret secret.yaml
```

显式真实只读：

```bash
go run ./cmd/okx-private-smoke --connect -secret secret.yaml -duration 30s
```

纪律：

- 不打印 key/secret/passphrase；
- 不下单；
- 只检查登录、orders/positions、事件延迟、REST 持仓对账；
- 真实连接尚未在本轮执行；
- 用户未要求前不要擅自连接真钱账户。

---

## 12. 当前 Todo 状态

```text
COMPLETED  冻结强平接力与竭尽机制研究问题/字段/切分/失败标准（但双锚修订后需新 protocol hash）
COMPLETED  流式机制面板基础（双锚中断后需重新验收）
IN_PROGRESS 研究区与 walk-forward 判断方向
COMPLETED  多卷一次性考试与签名回执基础
PENDING    完整成本与极端仓位回放冻结代际
PENDING    全量实时 shadow 与 scorecard
PENDING    OKX 私有 WS 真实只读认证冒烟
```

由于用户要求立即停止，`mechanism-dev` 不能继续执行。下一台 Agent 接手时应把它视为“等待修复双锚和红队断言”，不是可直接跑数据。

---

## 13. 交接后的第一条回复建议

下一台 Agent 读取本文件后应先回复用户：

1. 已确认真钱仍封火；
2. 已确认远端基线 `75d3c99`；
3. 已确认未提交双锚代码可能无法编译；
4. 将先恢复小 fixture 测试并实现红队 12 条断言；
5. 在 protocol 重新冻结前不会读取真实收益或消费考卷。

---

## 14. 最终提醒

- 当前最有价值的是数据、订单、资金和失败档案，不是现有策略；
- 不要为“首战完全体”这个名字强行给出盈利结论；
- `NO_GO` 是正确状态，不是工程失败；
- 不要恢复 casc/key；
- 不要用 architecture completion 覆盖 no edge；
- 不要为了赶进度读取黄金卷；
- 不要把“强平发生”误认为“强平已经耗尽”；
- 不要把多次考试变成重复背答案；
- 不要在测试失败后移动阈值；
- 不要提交运行态 records 和密钥；
- 所有真钱恢复必须再次获得用户明确批准。

