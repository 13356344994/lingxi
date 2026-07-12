# LINGXI 首战总计划：分层路由到认证赚钱叶子

> 状态日期：2026-07-11  
> 本文档是当前首战推进路线的权威计划。  
> `FIRST_BATTLE_HANDOFF_20260711.md` 是历史交接证据，不再作为实时 Todo 真相。

## 1. 唯一目标

系统只服务一个目标：

> 在不同市场环境或特殊情况下，从根节点逐层匹配到对应的、已经用历史证据证明具有条件净 edge 的机制方向；不同情况走不同赚钱路径，没有认证路径就空仓，使资金流调整后的真实账户净值长期增长。

系统不寻找一条适用于所有行情的万能策略。系统的完整形态是：

- 不同市场状态进入不同机制树；
- 每棵树只在自己的历史支持域内工作；
- 叶子负责给出该情形下的赚钱方向；
- 未知、冲突、不可执行或未认证情形一律 `NO_TRADE`。

以下均不是成功指标：

- 架构复杂度；
- 策略数量；
- 胜率；
- 服务在线时间；
- 回测毛收益；
- 尚未通过密封考试的研究结果。

## 2. 不可违背的原则

1. **调研定方向**
   - 方向必须回答谁被迫付款、为何必须付款、竞争为何不能消灭 edge。
   - 方向叶子不能只是 `long` 或 `short`，必须绑定机制、付款者、phase 和证伪条件。

2. **极端定仓位**
   - 仓位由维持保证金、真实强平距离、相关簇、BTC beta 和最坏可信场景共同决定。
   - 最高杠杆不得超过 5 倍。

3. **天道定信心**
   - 全局行情和结构约束只能缩仓或拒绝，不能突破极端仓位硬上限。

4. **只认止错，不认机械价格止损**
   - 叶子退出由论点证伪、机制完成、期限结束、数据失真或 OOD 决定。
   - 独立偿付能力防线不得被专家关闭。

5. **开放世界**
   - 路由树不假设覆盖全部市场。
   - 任一级无法可靠匹配时必须进入 `UNKNOWN/OOD -> NO_TRADE`。

6. **考试不可背答案**
   - 每张考卷只能使用一次。
   - 研究 Agent 不得看到题目、时间范围、逐笔答案、阈值附近数值或详细失败提示。

7. **永远不等待未来数据回答策略问题**
   - 历史可得数据能证明则推进，证明无 edge 则关闭。
   - 历史字段不足且在固定工期内找不到合法代理，则该 generation 判 `UNTESTABLE/NO_GO`，不得用“再收几天看看”续命。
   - 未来数据只用于运行时 OOD、工程一致性和安全监控，不得用于发现、挽救或调优赚钱方向。

8. **真钱恢复必须重新获得用户明确批准**
   - 研究、测试、考试或 shadow 通过均不自动获得真钱权限。

## 3. 总体架构

### 3.1 路由漏斗

```text
ROOT
└── DataTruth（三时钟 / PIT / 完整性 / 去重）
    ├── invalid / missing / late / correction -> NO_TRADE
    └── valid
        └── GlobalContext（全局风险与 OOD）
            ├── unknown / unsupported -> NO_TRADE
            └── supported
                └── MechanismFamily（机制家族）
                    ├── no observable payer -> NO_TRADE
                    └── ForcedLiquidation
                        └── Phase
                            ├── Relay
                            │   └── ForcedSide
                            │       ├── long squeezed -> short candidate
                            │       └── short squeezed -> long candidate
                            └── Exhaustion
                                └── ForcedSide
                                    ├── long squeezed -> long candidate
                                    └── short squeezed -> short candidate
                                        └── Executability
                                            ├── cost/depth/latency invalid -> NO_TRADE
                                            └── valid
                                                └── CertifiedLeaf
                                                    ├── not certified -> SHADOW/NO_TRADE
                                                    └── certified -> PortfolioRisk
```

### 3.2 叶子不是简单方向

每个叶子必须包含：

- `leaf_id`；
- mechanism family；
- payer；
- phase；
- forced side 与交易方向规则；
- 适用数据支持域；
- OOD 边界；
- 可执行性条件；
- observable invalidators；
- generation；
- protocol hash；
- research / exam / shadow / live approval 状态。

初始叶子：

```text
panic_liquidation.relay.long_squeezed.short
panic_liquidation.relay.short_squeezed.long
panic_liquidation.exhaustion.long_squeezed.long
panic_liquidation.exhaustion.short_squeezed.short
```

这些叶子目前都是**候选叶子**，不是已证明赚钱的叶子。

### 3.3 多机制不是强制单选树

- 每个机制家族拥有自己的路由树；
- 多个树可同时产生候选；
- 账户层统一处理相关性、同向压力和资金竞争；
- 相反方向冲突且无预注册仲裁规则时，必须空仓；
- 禁止通过“最近谁赚钱”临时改变路由顺序。

## 4. 协议边界

### 4.1 已冻结的强平机制协议

```text
protocol_id: firstbattle.forced_liquidation.preregistration
generation: firstbattle-forced-liquidation-g1
protocol_hash: 6b16eb531230a14242f29d398fd357188867062f2fb2f0f0af6b7f554dd83bcb
```

该协议定义：

- relay / exhaustion 双锚；
- AsOfTime / DecisionTime / FeatureLag；
- 标签时点与 staleness；
- walk-forward 时间隔离；
- 红队 12 项；
- bounded-memory/checkpoint 参数。

该 hash 不得静默修改。

### 4.2 待新增的路由协议

分层路由使用独立协议，不篡改强平 g1：

```text
protocol_id: firstbattle.hierarchical_router
generation: firstbattle-router-g1
binds:
  forced_liquidation_protocol_hash:
    6b16eb531230a14242f29d398fd357188867062f2fb2f0f0af6b7f554dd83bcb
```

路由协议冻结：

- 节点顺序；
- 每个节点的可观测输入；
- fail-closed 条件；
- 叶子身份；
- 冲突规则；
- 认证状态机；
- 路由审计输出。

## 5. 当前完成状态

只有同时满足“实现完成、审计复核、对应测试通过”的项目才打勾。

### 5.1 基线与安全

- [x] 独立通读源码、配置、测试、Git、研究脚本、关键文档和账本结构。
- [x] 确认本地 `HEAD` 与 `origin/main` 基线均为 `75d3c99`。
- [x] 确认系统继续维持 `NO_GO`，未恢复自动新仓。
- [x] 确认不读取真实收益、不消费黄金卷、不根据结果调阈值。
- [x] 确认现有用户改动与运行态 records 不得回退或夹带提交。

### 5.2 双锚与红队

- [x] 修复未定义 `maybeDetectRelay` 导致的编译阻断。
- [x] relay / exhaustion 使用独立 phase ID、AsOfTime 和 DecisionTime。
- [x] 落实 `DecisionTime = AsOfTime + FeatureLag`。
- [x] 落实 EventTime 与 ReceiveTime 双重因果截断。
- [x] 标签只使用 DecisionTime 之后数据。
- [x] 标签 staleness 超限后保持 unobserved。
- [x] 反向跨所强平不得增加确认。
- [x] 去除源内乱序下的 `break` 单调假设。
- [x] 坏质量 episode 在研究侧 fail closed。
- [x] feature / label 重复 ID 和 join 不一致必须报错。
- [x] 禁止把 `SecondHalfNotional` 改名后冒充独立 remaining 证据。
- [x] walk-forward 使用时间 purge/embargo，并覆盖 label horizon。
- [x] 少于两个真实 shock cluster 时不得报告有效 cluster CI。
- [x] checkpoint 绑定输入与配置，写失败不得推进 offset。
- [x] 中断恢复不得重复 append 输出。
- [x] 去重状态有固定窗口，checkpoint 不随历史文件无限增长。

### 5.3 预注册与测试

- [x] 强平双锚 manifest、代码常量与运行时配置三方漂移检测。
- [x] 重新生成并验证强平 g1 protocol hash。
- [x] 双锚及红队小 fixture 全部通过。
- [x] `go test ./... -count=1` 通过。
- [x] `go test -race ./internal/... -count=1` 通过。
- [x] `go vet ./...` 通过。

### 5.4 有上限真实数据 smoke

- [x] 使用单日、限行、进度、checkpoint 和独立临时输出运行 smoke。
- [x] smoke 未读取收益、未运行研究评分、未消费考卷。
- [x] 处理约 49,760 行强平输入并正常到达 EOF。
- [x] checkpoint 从约 11MB 的无界状态修正为约 940KB 的固定去重窗口状态。
- [x] 数据不完整时正确暴露质量红灯，而不是产生可用研究结论。
- [ ] smoke 数据质量通过。

当前 smoke 阻断事实：

```text
candidate features: 7
missing PIT ctVal:   7 / 7
missing OI:          7 / 7
missing depth:       7 / 7
complete labels:     0 / 7
```

这些候选不得进入收益研究。

## 6. 当前关键路径

### 阶段 A：可信 PIT/context 数据链

- [x] 建立 append-only `InstrumentCatalog` materializer，保留 ListedAt/KnownFrom；无法证明的历史 DelistedAt 不推测。
- [x] 建立 append-only `UniverseCatalog` materializer，禁止当前 shortlist 回填历史。
- [x] 当前 snapshot 只能从抓取时刻向未来生效。
- [ ] 恢复并持续记录 OI。
- [ ] 增加 depth 数据源。
- [x] 实现 mids / OI / depth / xvenue 按 UTC 日期流式分区。
- [x] 每个 canonical 分区保留 event / receive / process 三时钟及来源说明。
- [x] 每日输出数据 manifest：
  - 文件 hash；
  - 行数；
  - event-time / receive-time 范围；
  - 缺失率；
  - 迟到率；
  - 乱序率；
  - 重复率；
  - correction 数量；
  - schema/version。
- [x] 大文件处理支持日期、最大行数、可见进度、checkpoint、输入身份校验和崩溃恢复。
- [x] 实现独立 validator；缺源、PIT 覆盖不足、OI 停更、时钟/质量异常均 fail closed。
- [x] context materializer/validator fixture、全量测试、race、vet 通过。
- [x] 对 2026-07-11 运行无收益、无考卷的 bounded context smoke，并得到正确 `FAIL`。

PIT/context 工具的定位是审计历史可用性和运行时 OOD，不是等待未来数据证明策略。

2026-07-11 context smoke 的拒绝原因：

```text
DEPTH_MISSING
MIDS_MISSING（限行 checkpoint 尚未推进到目标日完整区间）
OI_STALE / PARTITION_EMPTY（xmeta 于 2026-07-10 停更）
UNIVERSE_MISSING（当前 shortlist 在日中才 KnownFrom，不能覆盖当天开端）
QUALITY_INVALID（xvenue 存在源内 event-time 乱序）
INCOMPLETE_DAY
```

验收：

- 不使用当前快照回填历史；
- 不存在静默 schema 错配；
- 候选不会因缺 ctVal/OI/depth 被研究侧全量剔除；
- 数据异常自动进入 `NO_TRADE`。

### 阶段 A2：历史资产与考试边界

- [x] 盘点 `records/history/` 已有多年/多月数据的字段、时间覆盖和标的覆盖。
- [x] 建立目录：7 个数据族、2,218 个完成文件、8,891,185 行，`DataHash=f41ed2586d91054c4b5b2e15e60f32a4d64ccde0032e76dfdc5fc6dee316379c`。
- [x] 把旧研究引用过及无法证明预先封存的数据族保守登记为 `RESEARCH_ONLY`，不得重新包装为考卷。
- [x] 实现考官侧 DataHash/ChallengeHash/ProtocolHash、0600 hidden manifest、固定顺序、崩溃消费和跨 generation 禁止复卷。
- [ ] 由独立考官导入并核验用户此前预留卷的 commitment 与消费状态；研究 Agent 不读取隐藏时间范围。
- [x] 按叶子声明最小数据需求，不再要求所有叶子共享六类 context：
  - OHLCV/funding 叶子只要求自身历史字段；
  - 强平双锚叶子才要求 liquidation/OI/depth/xvenue/PIT；
  - 缺字段只关闭对应叶子，不拖住整棵树。
- [x] 对历史不可检验叶子设置固定寻找死线；强平四叶当前均判 `UNTESTABLE/NO_GO`，不等待未来样本。
- [x] 实现只用决策时刻信息的行情分层契约及 chronological research/WF/exam 分配器。
- [x] 实现 file-backed 流式 grader、exact-ticket manifest hash 和最小签名回执；CLI 无法按次传入阈值或 pass。
- [x] 历史边界限定测试、全量测试、race、vet 与独立终审通过。

### 阶段 B：分层路由协议与实现

- [x] 新建并冻结 `firstbattle.hierarchical_router` manifest，当前 hash=`0eab9bda13055209c81d2b47f0286a0317b79dbb642592ec1b00cd4771397d31`。
- [x] 定义 root/node/leaf/trace 类型，并支持 phase 叶与通用 mechanism variant 叶。
- [x] 每个节点只读取决策时刻可见字段。
- [x] 每个拒绝路径返回稳定 coarse reason code。
- [x] 所有无匹配、歧义、冲突和 OOD 默认 `NO_TRADE`。
- [x] 绑定强平 g1 protocol hash。
- [x] 实现候选叶子状态机：
  - `UNRESEARCHED`；
  - `RESEARCH_PASS`；
  - `EXAM_PASS`；
  - `SHADOW_PASS`；
  - `LIVE_APPROVED`。
- [x] 未达到对应状态的叶子不得越级。
- [x] 实现多机制冲突时的 fail-closed 行为。
- [x] 路由输出完整但不泄漏考卷的审计 trace。

### 阶段 C：纯路由 fixture

- [x] 覆盖四个强平方向候选叶子。
- [x] 覆盖 PIT 缺失、ReceiveTime 晚到、乱序、correction。
- [x] 覆盖未知机制与全局 OOD。
- [x] 覆盖反向跨所确认。
- [x] 覆盖执行成本/depth 不可用。
- [x] 覆盖未认证叶子拒绝。
- [x] 覆盖多树同向、反向冲突和无仲裁规则。
- [x] 证明不存在绕过 `DataTruth/OOD/Executability/Certification` 的路径。

此阶段只验证路由，不读取收益。

### 阶段 D：历史分层路由 smoke

- [ ] 使用现有历史研究区数据，不等待未来样本。
- [ ] 每个叶子只检查其预注册的数据需求，不做全局六源一刀切。
- [ ] 限日期、限行、显示进度、checkpoint。
- [ ] 输出各节点通过/拒绝计数。
- [ ] 输出候选叶子覆盖率、歧义率、冲突率和 OOD 比例。
- [ ] 不读取收益、不调整阈值、不消费考卷。
- [ ] 验证重跑结果确定且无重复。

### 阶段 E：研究区与 walk-forward

- [x] 完成 bounded 逆向奖金研究 smoke：20 个固定字母序标的、7,048 个事件、2 个冻结组合。
- [x] 首轮红队发现并修正“只把赢家放处理组”的后视条件偏差；旧报告作废并重跑。
- [x] 最终 2 个组合均未通过 permutation/cluster/Bonferroni 选择税，候选提名为 0，因此没有候选可进入 forward falsification/WF。
- [x] 生成事件严格绑定历史目录 DataHash、选中文件哈希、协议与 search-space definition hash。
- [x] 冻结 G2 条件路由协议：`395d15ece40050e7227e0cff5a74a91c68cdab83285c02a10657d13f21a118ec`。
- [x] G2 真正按 `司天监 SkyState → 分拣器 SorterDomain → 机制 → 条件方向` 生成研究事件，不再混合全部行情。
- [x] G2 使用 20 个固定标的，得到 discovery 3,486 / forward 924 / WF 1,974 个合格事件，完整记录各 SkyState 覆盖与拒绝原因。
- [x] 红队发现并修复“2,000 次 permutation 导致任何组合数学上都不可能过门”的死门；backward/forward alpha 各 0.005，permutation 提升至 50,000，生产参数真 edge 可达测试通过。
- [x] G2 162 个冻结 `BTC return × realized volatility × BTC funding state × horizon × direction` 的
  `funding payer side flip` 条件组合：83 个未过选择税、47 个缺匹配对照、32 个无赢家、
  0 nomination、0 `RESEARCH_PASS`。
- [x] 因没有 nomination，forward falsification/WF 未启动；G2 按预注册关闭，不等待未来数据。
- [x] 冻结 G3 结果优先倒序归纳协议：`1628e1a575585a7a1c2b7da0e01eb54de9707997272a6301e3824505dece17fb`；
  先从 induction 净赢家倒推决策前条件，再回到完整 winner/loss/neutral 样本做 outcome-blind 对照。
- [x] G3 使用 20 个固定标的，得到 induction 14,022 / recheck 5,910 / forward 5,310 / WF 7,350 个合格事件；
  全程下一根 open 成交、Rubik 2h 陈旧门、canonical history 重生成、HMAC receipt、checkpoint，考卷动作 0。
- [x] G3 红队修复 outcome-biased controls、2-cluster 伪独立、permutation 分辨率、Rubik 陈旧、发现产物签名及
  5.2GB 全量候选物化；最终派生为流式 `O(192 + current group)`。
- [x] 初始 group-first smoke 因 192 条预算只覆盖字典序前 2/686 组而作废；最终改为机制特征优先、coarse Sky
  `ret × rv × fund × horizon × direction` 跨组 round-robin，162/162 个观察组全部覆盖。
- [x] G3 最终 192 条冻结规则（162 单特征 + 30 双特征）：168 条未过选择税、23 条无非触发匹配对照、
  1 条无赢家、0 nomination、0 `RESEARCH_PASS`。
- [x] 因没有 nomination，G3 recheck/forward/WF 未启动；G3 按预注册关闭，不等待未来数据，不触碰考试。
- [x] 冻结 G4 全市场赚钱地图协议：`55d5da85272d9d326c81570a677b47b66ede7da69b14fc58fe4c154c0a1d6425`；
  发现阶段宽进，付款机制和严格认证后置，`UNEXPLAINED` 永远不得交易或考试。
- [x] G4 完成真正全量地图：1h profile 401/401 标的、5m profile 200/200 标的，无字母序截断；
  discovery 共计算 32,705,532 + 43,753,080 条本金档位机会。
- [x] 地图确认市场持续存在事后扣保守成本为正的路径：1h conservative-positive rate 随 1h/4h/24h
  为 18.66%/31.77%/42.66%，5m profile 随 5m/15m/60m/240m 为 9.61%/20.13%/32.49%/40.53%；
  该统计不等于决策前可预测。
- [x] G4 从 10,098 个 discovery cells 公平冻结 1,024 个候选（994 个去重现象），覆盖两个 profile、
  long/short、全部 horizon、三档本金和 15 个实际可用决策前特征。
- [x] 首轮 validation 的 15 条初筛候选因单 bar volatility 退化、资金档重复、日级集中及无多重校正而作废；
  修复为 RMS volatility、PhenomenonID、10 天/8 非重叠块、日级贡献门和 BH-FDR 后重跑。
- [x] G4 最终 validation：0 pass、204 fail、820 `INCONCLUSIVE/COVERAGE`；失败中 179 为延迟一根 bar 后失效、
  19 为收益集中、5 为成本压力、1 为多重检验。当前简单规则无法事前稳定选中地图中的赚钱路径。
- [x] G4 自动机制解释数 0、严格 WF 入口 0、认证赚钱叶 0、系统 `NO_GO`、考卷动作 0。
- [x] 冻结 G5 动态动作协议：`bfe9f784039a89b773f2f9a0fb86d252c39ffa52b7f7202071e02bcc32333a7d`；
  动作覆盖 flat/open/hold/increase/trim/close/reverse、delay 0–3、连续仓位、Top-K 和 5 倍账户上限。
- [x] G5 实现连续 Sky/Sorter、PIT universe、横截面 rank、动态动作 reward cube、线性 action-value 学习、
  pending delay 执行、账户状态转移、相关簇 Top-K、validation/WF 与考试 evaluator fail-closed 接口。
- [x] G5 红队修复 fixed-notional 假净值、funding 漏扣、direction/verb/start-state 交互、trim/increase 标签、
  pending 执行额度、MTM 杠杆、cluster LOO 重复累计、WF 尾窗和 exam replay 信任边界；无残余 HIGH。
- [x] 为避免服务器 CPU 再次满载，G5 真实 smoke 使用 `GOMAXPROCS=1 + nice=19`、3/200 标的、
  每标的 3,000 根 5m bars 的 `TRUNCATED_SMOKE`，不冒充全市场结论。
- [x] G5 smoke 生成 discovery 2,791,536 / validation 874,368 / WF 870,474 条动作 reward，
  8,743 条 PIT 特征上下文和 100,000 条无偏 discovery 标签；97,196 条标签/特征成功 join。
- [x] G5 五个 lambda 策略均退化为固定方向/动作并被安全门拒绝，未生成假 policy；
  validation=`SKIPPED_POLICY_UNAVAILABLE`、WF=`SKIPPED_VALIDATION_FAIL`、研究通过策略 0。
- [x] G5 公开预留卷 commitment 不存在，状态 `EXAM_UNAVAILABLE`；未读取隐藏范围，考卷动作 0，系统 `NO_GO`。
- [ ] generation 冻结后才读取研究区标签。
- [ ] 只评估资金流调整后的条件净 edge。
- [ ] 使用完整手续费、滑点、资金费、毒性成交和退出盘口。
- [ ] 每个叶子独立统计，不用统一 PF 覆盖差异。
- [ ] 使用时间 walk-forward、purge、embargo。
- [ ] 使用 day / shock-cluster block bootstrap。
- [ ] 使用 Holm-Bonferroni 控制多重检验。
- [ ] 样本不足判 `INCONCLUSIVE`，不得判通过。
- [ ] 删除无机制或无净 edge 的叶子。
- [ ] 不因研究结果修改密封考试答案或时间范围。

### 阶段 F：固定顺序密封考试

- [ ] 冻结 router generation 和全部叶子。
- [ ] 固定考卷顺序与 alpha budget。
- [ ] 每张考卷只使用一次。
- [ ] 崩溃、解封或返回结果均永久消费该卷。
- [ ] 研究 Agent 只获得签名 pass/fail 和 coarse reason。
- [ ] 不返回逐笔、CI 边界、阈值附近数值或详细失败提示。
- [ ] 标准卷全部通过后才允许黄金卷。
- [ ] 黄金卷失败后该 generation 永久失败。

### 阶段 G：实时 shadow

- [ ] 历史多卷通过后才启动。
- [ ] 实时运行完整根到叶路由，但不下单。
- [ ] 检查数据延迟、漂移、OOD 和路由稳定性。
- [ ] 检查执行成本和可成交性。
- [ ] 检查历史认证逻辑与实时实现是否同构，不用未来盈亏重新裁决 edge。
- [ ] 检查多叶子并发、相关簇和冲突处理。
- [ ] shadow 使用固定、短期工程验收门；禁止“等到未来赚钱为止”。
- [ ] shadow 不得自动升级真钱。

### 阶段 H：真钱前偿付与执行闭环

- [ ] 账户级最高 5 倍约束在所有路径硬生效。
- [ ] 极端仓位、维持保证金、强平求根接入真实 sizing。
- [ ] 相关簇/BTC beta/同向压力进入账户分配。
- [ ] 独立偿付状态机不可关闭。
- [ ] 订单 WAL 接入运行态。
- [ ] 挂单、近期成交、私有 WS、REST、持仓与余额恢复闭环。
- [ ] unknown 订单状态强制 quarantine。
- [ ] 运行二进制与审核源码基线一致。
- [ ] 仍保持自动真钱封火。
- [ ] 向用户提交最终证据并申请明确真钱批准。

## 7. 下一步执行顺序

当前只能按以下顺序推进：

1. **阶段 A2：历史资产与考试边界**；
2. **阶段 B：独立分层路由协议**；
3. **阶段 C：纯路由 fixture**；
4. **阶段 D：历史分层路由 smoke**；
5. **阶段 E：研究区/WF**；
6. **阶段 F：密封考试**；
7. **阶段 G：实时 shadow 工程验收**；
8. **阶段 H：偿付/执行闭环与用户真钱批准**。

阶段 A 的工具链已完成；A2 与 B 可以并行，但：

- 每个叶子必须先通过自身历史数据需求门，不能被无关 context 缺失拖住；
- 阶段 B/C 未通过前不得把任何候选称为“赚钱叶子”；
- 阶段 E 未通过前不得考试；
- 阶段 F 未通过前不得 shadow；
- 阶段 G/H 未通过且用户未明确批准前不得真钱。

## 8. 当前状态结论

```text
工程与研究护栏：G1/G2/G3/G4/G5 历史闭环完成
历史数据与考试边界：已盘点并冻结；无合法 G5 公开预留卷，考卷未消费
已证明赚钱叶子：0
密封考试通过叶子：0
shadow 通过叶子：0
真钱批准叶子：0
系统状态：NO_GO
```

当前最小正确行动是冻结 G5 的“动态数据链成立、首轮线性策略退化”结论；若继续下一代，应更换模型族
（例如受限深度树或 mixture-of-experts），扩大但保持低 CPU 的分层标的样本，并继续使用相同账户回放、
delay/funding/偿付安全门。禁止放宽退化策略检测来强行得到 policy，也不得等待未来数据补考试。

G6 已转向小资金事件策略：冻结协议 `d34b2798bec83cfcbb39eceec05cb644a74e362b741bb3326bf67bcbabfeb759`，
实现事件地图、EventCard 倒序、全触发 validation、1/2/3 微仓账户、StopError、严格 WF 与考试边界。
低 CPU 三标的 smoke 生成 discovery/validation/WF 事件结果 7,102/2,301/2,713，倒推出 256 张 EventCard；
全部因仅覆盖 3 个独立日期而 `INCONCLUSIVE/COVERAGE`，0 validation pass、0 WF pass、考试 `EXAM_UNAVAILABLE`，
未创建假通过。下一步只能扩大历史事件日期/分层样本后新 generation 研究，不能降低 20 日/10 shock 门槛。
