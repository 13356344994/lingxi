# LINGXI 首战总计划：多状态动态路由到认证专属盈利路径

> 状态日期：2026-07-13（数据优先主线）
> 本文档是当前首战推进路线的唯一实时权威计划；历史 handoff、stage summary 和 preregistration 是冻结证据，不是并行 Todo。
> `FIRST_BATTLE_HANDOFF_20260711.md` 是历史交接证据，不再作为实时 Todo 真相。
> 2026-07-14 用户要求立即停止本 Agent 的全部操作；当前执行状态 `PAUSED_BY_USER`。接续入口：`LINGXI_DATA_MAINLINE_HANDOFF_20260714.md`。
> 2026-07-14 当前主线切换为数据驱动路由 V3（`firstbattle-data-driven-router-v3`）：不再以付款者/机构排斥/稀疏/容量/速度/护城河作为先验准入。旧机制先验已隔离为历史假设证据，边界见 `research/firstbattle/data_driven_router_v3/supersession.{json,md}`（见 5.13）。最高宗旨（第 1 节）原文不变。

## 1. 永久、唯一、最高优先级目标

> LINGXI 永久、唯一、最高优先级目标：在不同市场状态、特殊事件和个人执行条件下，动态路由到各自经过历史证据认证的专属盈利路径；没有可靠匹配路径时必须空仓。最终以资金流调整后的真实账户净值长期增长为唯一成功标准。

这不是任何场景都赚钱的万能策略。当前任务尺度是几十 U 到几千 U、最高 5 倍杠杆与严格偿付约束的个人账户；系统完整形态是：

- `根层市场状态 / 特殊事件 / 个人执行条件 / OOD`；
- 路由到不同的机制、方向、仓位与退出路径；
- 每条路径只在自己的历史支持域内工作；
- 账户层组合多条已认证路径并处理相关性、偿付与资金竞争；
- 冲突、OOD、无可靠匹配、不可执行或无认证一律 `NO_TRADE`。

数据、模型、代码、架构、测试和风控全部是实现宗旨的手段；不能缩短“专属盈利路径被历史证据认证并安全路由”的工作必须降级或停止。安全、因果、考试隔离与真钱审批是硬约束，但 `NO_GO`、稳定运行和复杂架构都不是最终目标。

**当前最高杠杆主线：获取并验收真正适配多路径动态路由的历史数据。**

以下均不是成功指标：

- `NO_GO` 本身；
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
   - 历史字段不足且在固定工期内找不到合法代理，按阻断层裁决为 `DATA_BLIND` 或 `UNIDENTIFIABLE`；旧 generation 已冻结的 `UNTESTABLE/NO_GO` 标签保留为历史事实，不回写改名。
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

### 4.2 已冻结的路由基础协议

分层路由基础协议已独立冻结，不篡改强平 g1；后续 G2–G10 均以新 generation/hash 延伸，而不是回写此协议：

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
- [x] 2026-07-13 复核本地 `HEAD` 与 `origin/main` 基线均为 `ab2780405f0fd5c80d268fe0feff39eb14a29eda`；运行态 `records/` 改动与本计划更新隔离。
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

### 5.5 G10 v1 冻结结论（当前研究基线）

权威证据：`research/firstbattle/g10_wild_brain_stage_summary.json` 与
`research/firstbattle/hierarchical_router_g10_preregistration.json`。以下身份与结果只读冻结，不修改原 generation：

```text
generation:                 firstbattle-router-g10
protocol_hash:              9f86ce54c24b00de41d1a6735a4f8190b00d49dce2a66371242646ffecd10750
history_data_hash:          f41ed2586d91054c4b5b2e15e60f32a4d64ccde0032e76dfdc5fc6dee316379c
fingerprint_hash:           3c0924a6bf3f47a57d71a7c61e92c00043fee53ce2f9f2315692e5d1198c6f1e
fingerprint_coverage_hash:  11858d147de03da2663fbd253bc7a6b50e6412ff33870ddeb854b9db5a508c33
okx_1h_map_manifest_hash:   f757f2b103785c835603ad165e5b40f4e704b1295e4e9a32bde228acf966a321
okx_5m_map_manifest_hash:   60e050cfcc9889cd44d391e6354088a2e3119e0976c9d769a37c569a8abc2f4e
residual_map_hash:          dae64ed12164f7d7deaff7e1cb5c562091c1b424293bcb872c8bedb918351581
residual_ledger_hash:       3fe3912e3f6f44c784e1f5587744562f50cc68107f5eaf1e8c66d90c6e40f4fd
residual_manifest_hash:     f5e6093e77ffc1914d8bec2482e46f4a436a697aead3c08f20708651b322bb64
playbook_hash:              8ceae12715dc9cc885fcaf77c230b7c44dc3becfe3644372ad1f276bd2d8a8f0
experiment_hash:            a471569db03b5667815a944506a935cac8c5f2b74371a434807cd2c306995d19
playbook_manifest_hash:     ef82da8421a9c6eb02b9854eebaf47673e94365918a058b610077d35064ce0c5
validation_report_hash:     13703d334a9092eca81db52c17371c8a2377c2646b5b42e50e91e0c7959a8006
validation_survivors_hash:  a1b07559b108743f79fa20dc7c106f46574ff8f4ef53ce1325cb78a8de9b5dc8
validation_receipt_hash:    eeb2d22b92506ba925ee74713147096750f72adcf7ad10837b22b9a47ccf3e06
wf_report_hash:             7fe38567643f18af7222d7b8da5a72765162d0e9345c7547e7deb6d11d686628
wf_stage_receipt_hash:      271765ec4c040e29ab5e134ffc695315baa90718a71dd17acb81767d60686b30
brain_fixture_trace_hash:   13614361ef4303670366f04f0973f51ad74c4b313e638c1d685c262328a4c63d
```

冻结关键数字与状态：

- 完整扫描 `40,685` 个 fingerprint-eligible triggers（attempts `40,685`），接受 `101` 个个人候选，拒绝 `40,584`。
- 从 `101` 个 winner seeds 冻结 `31` 个 Playbook；validation `0/31 pass`、`31 fail`、`0 inconclusive`、`0 survivor`。
- validation 实际观测事件为 `0`；表层原因 `BRAIN_DEGENERATE`，**底层门是 `COVERAGE`**。因此当前结论是支持域没有 validation 匹配事件，不是机制已被 `FALSIFIED`。
- WF 因无 validation survivor 正确 `SKIPPED_VALIDATION_FAIL`（`PLAYBOOK_UNAVAILABLE`），不是 WF 已检验后失败。
- Brain path fixture 的 8 个工程场景通过、重放一致、`degeneracy_status=OK`，只证明动作路径与退化检测工程链可运行，**不等于盈利认证通过**。
- 私人执行指纹共 822 个 symbol/direction，仅 9 个 direction（8 个 symbol）达到 critical-known。缺 fee/slip 不再伪装为零，但覆盖仍不足。
- `exam_actions=0`、`EXAM_UNAVAILABLE`、`trade_allowed=false`、系统 `NO_GO`；阶段 F–H 均未通过。

G10 v1 关闭的是这组冻结 protocol/history/fingerprint/Playbook 的**当前可检验表述**。在观测和识别未充分前，不默认关闭整个机制家族；同样也不得以“机制仍可能存在”为由修改原 generation、降低门槛、等待未来数据或进入考试/真钱。

### 5.6 G10 执行证据审计正式裁决（当前停止点）

正式阶段总结：

- `research/firstbattle/g10_execution_evidence_audit_stage_summary.json`
  - hash: `05550c0603fa340939c35efa9bd212509ab48cec50b07e541f530e875a7817ff`
- `research/firstbattle/g10_execution_evidence_audit_stage_summary.md`
  - hash: `0fca772df97bb474536aac64ea674a026a02d0a00762228fe224378b0b9ff8c0`

绑定证据链：

```text
audit protocol:       dccd7974a7d172e3894174a953c4cce02ea840a48bb41fbdec589ed4d1153dd3
source manifest:      c8c5f38e6542855aa6fbc8e3373a751a24646922d0838a9774fce4a7e41a9379
audit summary:        cf9da460a7807c4d131bbc1e2b22ec9c0e860902fbb6a9726707d7c697b9d9ab
audit receipt:        29494eb6dfc62c8317315f93391aa60bd9d4f7ae515a166f94ae0a28c99b4f9a
route portfolio JSON: 166ea394419544f55e54919216b6e67b31e2d1dfaf11ef27df06bb3686072977
route portfolio MD:   733b7ae620b083eb18802bcdcb5c4520b345df7c05ef5da97140105d2efacec8
```

唯一正式分类是 **`DATA_BLIND at observability`**：

- instrument `ctType`：`0/394` known；
- dynamic min-notional：`0/12826` known（12,644 缺 ctType，182 缺 instrument metadata）；
- exchange-fill fee_bps：`0/12826` 可合法归一化，原因同上；
- validation 只有 84 个 outcome-blind 聚合 cells，没有 event/day/shock identity；winner 仅绑定 opaque hash，matched controls、baseline support 与 events/days/shock counts 均不可冻结。

因此不得写成 `FALSIFIED`、`POSTHOC_ONLY`、`UNEXECUTABLE` 或 `INSUFFICIENT_SUPPORT`：这些层全部位于已失败的 observability 之后。

已看见的 after-decision execution evidence 为 settlement fee `973/1091`、entry slip `296/1091`、exit slip `196/1091`、maker fill `29849/29849`。它们不能作为事前预测特征，也不能补齐 instrument/event identity 门。

冲突与去重保持显式：suppressed lower-priority facts `808`、cross-source conflicts `42`、unresolved cross-source identities `45,332`，severity=`CRITICAL_UNKNOWN`。

正式 FULL_PREFIX run 投影 `2,802,749` 行，峰值 `220` rows / `170425` bytes，耗时约 `117.5s`。`go test ./... -count=1`、target evidence-audit race 和 `go vet ./...` 全部通过。

机制假设库的 4 条路线当前也均为 `DATA_BLIND`：执行容量断层与规则断点保留但不启动，稀疏强制流休眠，跨场所非对称近期归档；`routes_started=0`。

当前 audit/measurement generation 的进一步收益、validation/WF、考试与路线自动启动全部关闭；未被充分观测的机制家族不自动关闭。禁止等待未来数据续命。

### 5.7 现有历史重开条件存在性盘点（已完成，未重开）

用户批准的 existence-only 盘点已完成：

```text
source manifest: 66b47378f674a2e9f8cdbe6e7649f11afee633610aa5ca330c09f2d38b8b79cd
inventory JSON:  b9b62bc4d959d256e9d553385c2b3a474c1017632a35951a84e70d2a8e46e69a
inventory MD:    b36dd23d57c5f89cc406957ecc79d4e655f4d42bd4d8d33673e1a45c3784f728
```

新增 14 个合法候选 JSONL 仅按最后完整 LF 冻结 byte prefix；正式 audit 已绑定的 maker/fills/open decisions/shadow/instrument/equity 等只引用 `c8c5f38e…`，没有重扫。hash 期间仍在同 inode 追加的 dynamic features 与 market mids 新行被正确忽略；所有 absent expected paths 以 `hash=null` 登记。

存在性结论（不是收益七类）：

- `micro_execution_capacity_cliff = PRESENT_PARTIAL`：maker attempts 与 fills identity 分别存在，但没有共享 lifecycle、attempted notional、partial + ack/cancel/fill 全链或完整 event denominator。
- `exchange_rule_discontinuity = PIT_UNPROVEN`：当前 snapshot 有 ctVal/lot/tick，但无历史 ctType/effective_at/KnownFrom/rule versions；rule archive absent，当前 snapshot 不得回填。
- `sparse_forced_flow = PRESENT_PARTIAL`：正式 audit 漏纳的 OKX/Bybit liquidation streams 是 direct forced-action observations，但无稳定 exchange event ID、真三时钟、uptime/gap 完整分母、payer disappearance。
- `cross_venue_operational_asymmetry = WEAK_PROXY_ONLY`：timestamped anchor mids 与 selected two-leg fragments 存在，但 mid 不是同步双边 executable book，且缺完整费用/spec/collateral/authorization/event denominator。

writer code 具备 execution WAL / PIT catalog 等模型不等于生产历史已落盘；production WAL、instrument/rule archive、同步 executable books 与 collateral authorization history 均未找到。

没有 `PRESENT_COMPLETE` 路线，`routes_reopened=0`。本轮未解盲 labels、未读取或汇报收益字段、未修改 `records/`。

### 5.8 多路径路由数据合同已冻结（当前停止点）

`freeze-route-data-contracts` 已执行，先定义、后下载；未下载、未读取收益、未选路线、未启动实验。产物：

- `research/firstbattle/route_data_contracts.json`
- `research/firstbattle/route_data_contracts.md`

```text
contract_id:            firstbattle.route-data-contracts
generation:             firstbattle-route-data-contracts-v1
protocol_hash:          4a4c201d2c8cedde5bd1ff55ea40ad31d099e189feb709a517701b504c453588
document_hash:          68dc6666fc0138850fff2911965e5ef05505797ac9fc6137ddde928744e74639
chosen_design:          B_LAYERED_ROOT_GATE_PLUS_INDEPENDENT_ROUTES
status:                 FROZEN_CONTRACT_DEFINITION_STOPPED
```

合同覆盖根层 context/OOD 门与四类专属路径，每条独立冻结 required fields、identity、Event/Receive/Process 三时钟、PIT/KnownFrom、完整分母、匹配输家、最小 `50 事件 / 20 独立日 / 10 shock blocks`、数据来源优先级、outcome forbidden fields 与停止门；明确**不是万能策略**且**路径独立认证**。

在 4 个候选设计（A 单一共享 schema / B 分层根门+独立路径 / C outcome-anchored / D availability-first）中选 B；A/C/D 均记录 REJECTED 理由。

官方已确认覆盖写入来源优先级与停止门：OKX private `orders-history-archive` / `fills-history` 约 3 个月，官方公共 trades 自 2021-09、L2 自 2023-03，Report Center 人工导出可得约 2021 起更长历史（未导入前 micro/forced 完整分母天然不足门槛）。

四路径当前分类保持不变：`micro_execution_capacity_cliff = DATA_BLIND`、`exchange_rule_discontinuity = DATA_BLIND/PIT_UNPROVEN`、`sparse_forced_flow = DATA_BLIND(sources PRESENT_PARTIAL)`、`cross_venue_operational_asymmetry = DATA_BLIND/WEAK_PROXY_ONLY`；根门 `GATE_ACTIVE_FAIL_CLOSED`。protocol/document hash 自排除、三方 drift（manifest/代码/运行时配置）与绑定 drift fail-closed、owner decision journal 均已写入合同。`routes_started=0`，停在用户审批点；下载、读取收益、路线启动、考试、真钱须再次批准。

### 5.9 数据来源注册表独立核验（调查完成，尚未获取）

官方/免费与付费候选来源已按 route fit、provenance、coverage、identity/clocks/PIT、磁盘风险和停止门登记，并经不同模型独立外核后修订：

```text
registry JSON:  research/firstbattle/route_data_source_registry.json
registry hash:  1c6672563d4bc00ea102df2f9575b365a2a9dc30e763db75067f9a49052e5318
registry MD:    research/firstbattle/route_data_source_registry.md
document hash:  483ee318d954cf939646f0a9305e155ab908a3918a75afdfd6fb5f7bb149a0cf
status:         INVESTIGATION_REGISTRY_NO_ACQUIRE
```

独立核验确认 P0 仍是 OKX 私人 `orders-history-archive` + `fills-history`、current instruments 快照和官方公共 1 日×1 标的 pilot；同时修正了以下关键边界：

- orders 分页 `after/before=ordId`、时间筛选 `cTime`；fills 分页必须使用 `billId`、时间筛选 `ts`，不能用 `tradeId/fillTime` 冒充；
- `orders-history-archive` 不证明 rejected/expired/live-open attempts，约 3 个月 API 切片不能声称完整 attempts 分母；
- OKX Historical Data 的 SWAP discovery 可能返回 `allswap-*` 全品种包，必须先验包范围和磁盘；L2 module 映射仍待 smoke；
- OKX 有近期滚动 public liquidation REST/WS，但没有官方长历史 liquidation 批量归档；本地流仍仅 `PRESENT_PARTIAL`；
- current instruments 继续仅 `SNAPSHOT_ONLY/PIT_UNPROVEN`；官方历史 rule version archive 仍未找到；
- Bybit L2 日文件体积须在 pilot 前重验；Binance `bookDepth` 不是 tick L2，旧 tick L2 路径已停止且旧下载路径 404。

本节只完成来源真实性调查和 registry 修订，不是 Data Fitness，不代表任何来源已下载、任何分母完整或任何路线脱离 `DATA_BLIND`。付费来源仍只列清单，未购买。

### 5.10 P0 真实 GET 获取进度（进行中，未做收益研究）

Downloader 经独立 Sonnet 三轮红队与真实试点增量复核后，仅在批准的 GET-only 范围联网。原始私人数据位于仓库外 `/root/.lingxi-private/historydownload`，公共数据位于 `/root/.lingxi-data/historydownload`；未写入 `records/`，未输出密钥或 raw identity。

当前已发布：

```text
OKX SWAP orders-history-archive:
  pages=137 records=13,622 raw_bytes=13,251,276
  duplicate=0 missing_identity=0 publish_state=published

OKX SWAP fills-history:
  pages=157 records=15,676 raw_bytes=7,801,031
  duplicate=0 missing_identity=0 publish_state=published

outcome-blind identity join:
  unique order tokens=13,622
  fill-side unique order tokens=13,622
  matched=13,622 unmatched=0

current public SWAP instruments:
  rows=420 ctType=420 minSz=420 ctVal=420
  PIT=SNAPSHOT_ONLY/PIT_UNPROVEN
  historical_catalog_allowed=false
```

这些数字只证明当前约 3 个月 API slice 内的 order↔fill identity 可唯一连接。orders 中 13,566 `filled`、56 `canceled`，但端点不提供 rejected/expired/live-open attempts，且原始 collector ReceiveTime 不存在；manifest 已明确 `DenominatorFitness=INCOMPLETE_API_SLICE`、`ClockFitness=ARCHIVE_ORIGINAL_RECEIVE_TIME_ABSENT`。因此不能宣称完整 attempts 分母，也不能据此让 micro route 脱离 `DATA_BLIND`。

2026-07-14 对官方 public discovery 做了真实 GET 元数据探测（无归档下载）：

- `module=1 / SWAP / BTC-USDT-SWAP / daily / 2026-07-10` 实际返回 `allswap-trades-2026-07-10.zip`（263.72MB），并额外返回 2026-07-11 的 `allswap` 包（177.2MB）；`instIdList` 未形成单标的文件；
- `module=4` L2 对同请求返回空 `details`；
- 因不满足冻结的“1日×1标的”pilot、返回范围歧义且全市场包会挤占私人执行预算，未下载 ZIP，公共 smoke 在 discovery data-fit 门 fail closed。

公共 archive smoke 已通过独立复验：`PASS_FOR_PUBLIC_DISCOVERY_AND_EXACT_1D1S_DOWNLOAD`。实现禁止所有 redirect，要求整个响应恰好 `1 data / 1 detail / 1 group` exact scope，逐实际字节检查 root cap，使用 CSV 实际 EventTime min/max，且只开放 module=1 trades（books 4/5 关闭）。真实 CLI 复跑在 `discovery detail range outside exact request` 停止，ZIP transport 未触发、archive-smoke 目录未创建；因此当前来源本身不满足 1d1s，不是实现未完成。

当前 `acquire-p0-history=IN_PROGRESS`；下一步先完成公共 discovery 实现/红队与正式 hash-bound Data Fitness，再决定是否存在合法更小的官方公共 slice。没有 route 重开、收益研究、考试或真钱动作。

### 5.11 P0 SWAP Data Fitness（独立 reviewer receipt PASS，旧数据主线已最小关闭）

已对 published SWAP orders/fills projection 与 current instruments 做 outcome-blind 正式 Data Fitness，并由独立 reviewer 会话产出 hash-bound receipt 完成独立复验：

```text
JSON: research/firstbattle/p0_swap_data_fitness.json
hash: 393a12952b480fc2671774d76790e5f850bcbc26b43c8eb8d870bba9c9e2bd66

MD:   research/firstbattle/p0_swap_data_fitness.md
hash: 697cf3686f72748459d89943c1f45f0071e670f5d22a36f23f61d0154707e899

measurement receipt: f23a815fba52dab22368428a6a95250a1ad58e429c09613fea0ac977b1030c92

独立 reviewer receipt JSON: research/firstbattle/p0_swap_data_fitness_reviewer_receipt.json
hash: 0cdd322c130a7c6e58bd9803e5611f6a144d35bf6e5a3f0cdac49292ad9dcad4
独立 reviewer receipt MD:   research/firstbattle/p0_swap_data_fitness_reviewer_receipt.md
document hash: 567cbaea088027a4a521e6ecfa11fdd28fb1b3ac149896e77f13042d031e7bbc

status: INDEPENDENT_REVIEWER_RECEIPT_PASS_DATA_FITNESS_CLASSIFICATION_OLD_MAINLINE_CLOSED
```

权威证据是上面这份独立 reviewer receipt（verdict `PASS_INDEPENDENT_VERIFICATION_DATA_FITNESS_CLASSIFICATION`，reviewed HEAD `ab2780405f0fd5c80d268fe0feff39eb14a29eda`）：由与撰写被审工件不同的独立会话产出，通过 M1 CLI 重算、M2 clean-room Python 重实现、M3 自排除 hash 重算、M4 manifest 时钟/分母审计、M5 plan drift 检查独立复算，带自排除 receipt hash。批准范围仅为当前数据维持 `DATA_BLIND / routes_reopened=0`，不批准收益研究、validation、WF、考试、shadow、真钱，也不批准把 100% join 当完整分母或把 current snapshot 当历史 PIT。

`p0_swap_data_fitness.json` 内嵌的 owner 自撰 `independent_redteam` 块与 `.md` 第 8 节的 Sonnet 叙述，是由撰写其余工件的同一 owner 侧 agent 谱系、在 owner 可控文件内写下的自证，**降级为 `NON_EVIDENCE_OWNER_NARRATIVE_RETAINED`**：原文保留为 owner 自述，但其证据权重由上面这份独立 reviewer receipt 承担（对应 receipt finding F4，MEDIUM，process）。此前本节标题 `（待独立红队）` / 状态 `GENERATOR_REBUILT_PENDING_INDEPENDENT_REDTEAM` 与工件自身 `REDTEAM_PASS…STOPPED` 的矛盾据此统一。`p0_swap_data_fitness.json/.md` 未被本收尾修改。

独立复算的关键事实：

- orders 13,622、fills 15,676；13,622 个 fill-side order token 全部匹配，unmatched=0；
- 13,566 filled + 56 canceled，但 56 个 canceled 均有正 `accFillSz`；no-fill canceled=0；
- 因而 100% join 只证明 survivor-biased API slice 的引用完整性，不能形成 fill/no-fill 完整 attempts 分母；
- 原始 ReceiveTime 缺失，current instruments KnownFrom 晚于全部 attempt；
- orders 与 fills 各自包含 144 个 traded instId，但集合并集为 145；current snapshot 缺 `DATA-USDT-SWAP-OFF20260702-2` 与 `IP-USDT-SWAP` 两个；
- 同一 order token 在两端出现 168 个订单 / 199 条 fill rows 的 instId 冲突：orders=`DATA-USDT-SWAP-OFF20260702-2`、fills=`IP-USDT-SWAP`，证明 exchange endpoint 自身存在历史 symbol rewrite/PIT 污染；
- 26 个 UTC 日中 top-2 days 占 81.7428%，支持度高度集中。

独立红队曾 `BLOCK` 手工 owner 工件：traded symbol 与 exchange identity 的事实描述错误，`exchange_identity=DIRECT` 过宽。现已用 v3 带单测离线生成器重算 137/157 个 projection pages，并绑定 measurement receipt hash `f23a815f…0c92`、orders aggregate `e2cad38f…b6b3`、fills aggregate `36d3fd5e…2b2a`。v3 额外封死嵌套/额外 outcome 字段、非法 `accFillSz` 伪造 no-fill 及 CLI 覆盖其他 research 工件。最终独立复验为 `PASS_DATA_FITNESS_CLASSIFICATION`，无 MEDIUM 以上 finding；批准范围仅为当前数据维持 `DATA_BLIND / routes_reopened=0`，不批准收益研究、考试、shadow 或真钱。earliest-blocker 是 `INCOMPLETE_ATTEMPTS_DENOMINATOR`，secondary 包含 `CROSS_ENDPOINT_INSTRUMENT_IDENTITY_DRIFT`。

owner 建议的最高价值动作从“继续拉更多同类成交历史”切换为：append-only execution attempt WAL（含 rejected/no-fill/decision_id/ack-cancel-fill 时钟与 uptime denominator）和 forward-only instrument PIT ledger；Report Center 只延长 completed-order 覆盖，bills archive 只延长账本/费用，二者均不能补造完整 attempts 分母。

Report Center 的用户操作清单已写入
`research/firstbattle/okx_report_center_manual_export_checklist.md`，状态
`AWAITING_USER_MANUAL_EXPORT`。先导出 recent-month SWAP Order CSV（不隐藏 canceled）做 header/identity/state/clock pilot；schema 通过后才按不重叠区间导出 2021-01-28 起长历史。原始 CSV 必须进入仓库外 private root，任何 outcome 字段只留 raw、不进入 projection。

本地 attempt 历史链只读审计进一步确认：

- `fast_maker_attempts.jsonl` 29,849 行是通过 fast guard 后的 maker 后验摘要，缺 `decision_id/clOrdId/ordId/tradeId`；
- `fast_exchange_fills_cache` 只有成交分子，无法确定性回连 maker attempt；
- slow `open_decision_events` 与 fast 路径平行，策略 precheck rejection 不是 exchange rejection；
- 代码存在 `internal/execution/journal.go` schema，但 production `records/execution.wal`、`records/okx_directional/execution.wal` 与 `order_lifecycle.jsonl` 均 ABSENT；
- 因此现有 records 的最早 blocker 是 `SHARED_ORDER_LIFECYCLE_ID_ABSENT`，其次才是 WAL/partial/clocks/time-window；不能把无记录推断成 no-fill。

Report Center CSV inspector 的首轮独立红队为 `BLOCK_REPORT_CENTER_INSPECTOR`：outcome 列名变体可绕过 mapping、current/generation 未完整 hash-seal、mapping/inbox symlink/权限和 naive timestamp 语义未闭合。修复并复验前不接收真实 CSV，不让工具绿灯冒充数据已验收。

### 5.12 旧数据主线最小关闭（机制搜索V2第 1 项收尾）

停止把旧数据工程本身当主线，只做一次最小可信关闭并绑定权威独立证据，然后把主线交给 `firstbattle-mechanism-search-v2`。产物：

```text
JSON: research/firstbattle/data_mainline_closure_v1.json
hash: d4f78f1efabead5b86e0656ab73903734da6c36d1d737337c535a14e6568525e

MD:   research/firstbattle/data_mainline_closure_v1.md
document hash: 48da099f841ca1814f1eaf656e9b64e38c865c4e5e536c966f38601d04085a06

generation: firstbattle-data-mainline-closure-v1
status:     OLD_DATA_MAINLINE_CLOSED_MINIMAL_HANDOFF_TO_MECHANISM_SEARCH_V2
```

closure 绑定 route data contract、source registry、P0 Data Fitness JSON/MD/measurement、orders/fills projection aggregate、独立 reviewer receipt JSON/MD 与 repo HEAD `ab2780405f0fd5c80d268fe0feff39eb14a29eda`，并写入 owner Decision Journal 与 scope，JSON/MD 各自排除自身 hash。

冻结旧数据主线状态：

```text
GET-only downloader / OKX P0 private orders·fills / current instruments(SNAPSHOT_ONLY,PIT_UNPROVEN)
  / route data source registry / Data Fitness measurement(v3):  RETAINED_FROZEN

OKX Report Center 人工导出 / Binance async pilot / public L2·depth:
  NOT_REQUIRED_UNLESS_SHORTLISTED_ROUTE_DEMANDS_IT
Binance connected path:  STOP_UNVERIFIED_DOWNLOAD_HOST（禁止 --connect / 禁止消耗 quota）
```

工程指令 `NO_FURTHER_OLD_MAINLINE_ENGINEERING_TO_COMPLETE_LEGACY_TODOS`：禁止为“完成旧 todo”继续工程化旧数据主线；上述 `NOT_REQUIRED` 项只在机制搜索V2某条入选路线的冻结数据需求明确触发时才重新单独立项并先过红队。

最早 blocker 是 `INCOMPLETE_ATTEMPTS_DENOMINATOR`：全部下载订单 `accFillSz>0`、no-fill=0，100% order/fill join 只证明 survivor-biased API slice 的引用完整性，**不等于**完整 attempts 分母。Data 工程关闭 **不等于** 路线/机制失败——关闭的是当前数据主线 generation 的可检验表述，四路径保持 `DATA_BLIND`，不写 `FALSIFIED`，不等待未来数据续命。

验证：本轮主代理已运行 target 测试 / race / vet / `git diff --check`，命令已 exit 0；未声称已重跑最终全仓 race（全仓 `go test ./...` 曾在 Report/Binance 最后改动前通过，最终全量 test/race/vet 待代码冻结后重跑）。

下一主线 `firstbattle-mechanism-search-v2`：`labels_unblinded=false`、`routes_started=0`、`routes_reopened=0`、`system_status=NO_GO`；先过 payer / existing-history observability / complete denominator / PIT-identity / `50 events · 20 days · 10 blocks`，之后才允许读任何历史标签。未经用户再次批准，不运行收益研究、validation/WF、考试、shadow 或真钱。

### 5.13 数据驱动路由 V3 启动：隔离旧机制先验（当前停止点）

主线由 `firstbattle-mechanism-search-v2` 切换为 `firstbattle-data-driven-router-v3`。V3 停止把付款者、机构排斥、事件稀疏、容量、速度或护城河作为先验准入，让真实历史决策时数据直接决定各状态下走 `LONG/SHORT/HOLD/CLOSE/NO_TRADE` 等；只保留使证据可信所必需的 DataTruth、PIT、时间隔离、完整成本和账户安全。本节只执行 V3 todo `supersede-mechanism-priors`（隔离边界），未冻结 V3 协议、未构建数据集、未跑竞赛/验证/回放/裁决。产物：

```text
JSON: research/firstbattle/data_driven_router_v3/supersession.json
hash: 44f098e19cffa0657a963673b5c97425dcac58dcf15ae7d5939a8fec9ae7bad2

MD:   research/firstbattle/data_driven_router_v3/supersession.md
document hash: b24e26398b8082b4fbd9895bb7ef834fedc195b79127905c09888241ead812dc

generation: firstbattle-data-driven-router-v3
status:     MECHANISM_PRIORS_SUPERSEDED_ISOLATED_AS_HISTORICAL_HYPOTHESIS_EVIDENCE
```

冻结边界（JSON/MD 权威，本节为摘要，不删除任何历史段）：

- 第 1 节最高宗旨原文保留不变；V3 只改搜索方法，不改宗旨。
- `research/firstbattle/mechanism_search_v2/` 全部工件（candidates_opus/fable/grok、hypothesis library、shortlist 及 reviewer receipt、equity session-gap source_fitness、us_market_calendar_2026、equity session-gap data_contract）状态 `HYPOTHESIS_ONLY_NOT_MODEL_INPUT`：只读留存、永不删除，不进入 V3 state/features/labels/reward/candidate/selection；无论 hash 如何均不得据此进入 V3 输入。
- V3 **不继承** payer / 机构排斥 / 稀疏 / 容量 / 速度 / 护城河作为先验准入；它们只能作为 PIT 可测的原始可见变量，或在某状态—动作区已通过独立 validation/WF 后作为事后经济解释，且解释不得回改发现/选择/阈值。
- V3 **继承** DataTruth、PIT、时间隔离、完整成本、账户安全，以及 G1–G10 与机制搜索V2 的历史失败结论——仅为保证证据可信，不作为路线，不重开或改名任何冻结裁决。
- 旧股票 3-symbol 合同 `equity_session_gap_data_contract.{json,md}`：scope 因用户否定预设范围标 `SUPERSEDED_SCOPE_REJECTED`；hash 标 `HASH_INTEGRITY_DISPUTED_NOT_TRUSTED`——Terra Go 重算报告仓库 `datatruth.HashJSON` 为 `1618af25…f3ac982`，与声明 `47e4113e…c6213a` 冲突，另有 owner 相反声明称 `47e4113e…` 可复现。本轮不自行裁决、不修改旧文件；无论 hash 如何均不作为 V3 输入或任何有效性门。（非裁决旁证：独立 Python 复算 `datatruth.HashJSON` 精确匹配 3 个控制 hash，仅因 hypothesis library 存整数值浮点 `6.0`（Go 记 `6`、Python 记 `6.0`）而与其声明 `d208f96…` 不同——证明 Python-vs-Go 数字序列化差异足以单独解释此类分歧。）
- 旧数据主线 closure 与 P0 Data Fitness（`data_mainline_closure_v1.*`、`p0_swap_data_fitness*.{json,md}`）仅作数据质量/成本证据，`DATA_BLIND / routes_reopened=0` 是数据质量陈述，**不作 route label**、不作市场裁决。
- 当前动作切换 `firstbattle-data-driven-router-v3`：`labels_unblinded=false`、`routes_started=0`、`routes_reopened=0`、`exam_actions=0`、`system_status=NO_GO`、`exam_status=EXAM_UNAVAILABLE`、`trade_allowed=false`、`global_halt=true`。未提交。后续 V3 protocol 冻结、数据集构建、多模型竞赛、独立 validation/WF、账户回放与最终裁决各自单独设门，须再次批准。

### 5.14 数据驱动路由 V3 协议冻结

V3 generation `firstbattle-data-driven-router-v3-g1` 已在读取新一轮标签前冻结：

```text
JSON: research/firstbattle/data_driven_router_v3/protocol.json
hash: 5d80166eb28307d23e33cd19efae2b60811852ff0da49a3a6e16663aba2e7ddd

MD:   research/firstbattle/data_driven_router_v3/protocol.md
document hash: b76f341e0694f27513b602203c8bde2d184bcd10260800b95a988f75ad04132a

status: PREREGISTERED_LABELS_SEALED
```

关键冻结事实：

- 正式 profile 仅 `okx_1h`；`okx_5m` 与 `rubik_taker_5m` 因历史过短只允许 smoke，不能进入正式选择。
- 所有模型共享全局 discovery/validation/leaf-WF/router-holdout=`50/20/15/15` 时间切分及 24h 全局 purge/embargo；router holdout 只允许一次冻结组合回放。
- 状态只含 DecisionTime 可见连续变量和 missing mask，禁止 symbol ID、绝对日期、旧候选/route/threshold、未来值与当前未闭合 bar。
- 四个模型 owner 各最多 16 次尝试、32 个 leaves、1 CPU/12h；候选并集统一做 validation BH-FDR q=0.10，leaf-WF 在全部 attempt ledger 并集上做 Holm alpha=0.01。
- base funding 使用真实带符号现金流，stress 使用不利绝对 funding；delay、MTM leverage、min lot、pending、EOF 与完整成本统一由冻结 reward evaluator 处理。
- leaf 认证只认 chronological full-account replay；isolated reward 仅供 discovery。独立 leaf 组合后，只在从未用于选择的 router holdout 上评估一次账户净值，禁止二次调参。
- `NO_VALIDATED_PATH` 是合格结局。当前仍 `labels_unblinded=false / routes_started=0 / NO_GO`；下一步仅构建 hash-bound state-action dataset 与 reward evaluator。

### 5.15 数据驱动路由 V3 g2 风险与证据补充协议

g1 经独立 Sonnet 复核为 `BLOCKED`（成本覆盖门和 owner 隔离机制未完全数值化）；同时外部批评正确指出流动性真空和危机相关性压力不足。由于 g1 尚未解盲标签、尚未构建数据集，现以 g2 overlay 在任何数据任务前取代 g1：

```text
JSON: research/firstbattle/data_driven_router_v3/protocol_g2.json
hash: 7096395f1f3bae212b6dc866c5d46ccb372cd284e4a959d424dfbfbb92d6c226

MD:   research/firstbattle/data_driven_router_v3/protocol_g2.md
document hash: ad51f16f891fe4abc5293ce62403330ab6d7fa6e60bf9375cb0e86001f706c62

generation: firstbattle-data-driven-router-v3-g2
status: PREREGISTERED_LABELS_SEALED_PENDING_INDEPENDENT_REVIEW
```

g2 冻结补充：

- 正式 profile 成本已知覆盖率须 `>=95%`；每个实际交易决策成本状态必须为 `DIRECT` 或完整冻结 proxy，否则 `NO_TRADE`。`VALIDATED_PATH` 需要 `>=80%` 直接执行成本覆盖，否则最高只能 `RESEARCH_PATH_EXECUTION_UNPROVEN`。
- 四模型 owner 只提交 schema/search grid/code 与 synthetic tests；所有代码先 hash 冻结，再由中立 runner 统一读取真实 reward labels。owner 直接运行真实标签或提前读取 peer 输出使 tournament 作废。
- validation/WF/router holdout 强制执行 1/2/3-bar 流动性真空、历史最坏 gap/bar path、持续 MTM/funding/保证金/强平距离；无法退出时不得假装成交。
- 5 倍仅为账户绝对上限，不是目标。仓位按模型、流动性、min-lot 与全部压力场景下偿付能力的最小值向下裁剪；需要向上凑 lot 时 `NO_TRADE`。
- 所有同刻 proposal 先批量汇总再执行；危机场景将全部持仓相关性设为 1、合并为一个簇，并汇总同方向与 BTC beta。压力下不偿付则同比缩仓重算，否则全部拒绝；`CLOSE/TRIM` 优先。
- Sonnet 仅是机械证据 reviewer，不预测结构性突变、不批准未来市场安全、考试、shadow 或真钱。历史通过不会自动升级 live。

g2 独立复核通过前，仍禁止构建正式数据集或解盲标签。

### 5.16 数据驱动路由 V3 g3 提现与风险效用协议

用户明确真实资金管理方式后，g2 的“任一合成压力失败即否决路线”不符合最高宗旨。g2 尚未解盲标签、未构建数据集，现以 g3 overlay 冻结真实提现与风险效用：

```text
JSON: research/firstbattle/data_driven_router_v3/protocol_g3.json
hash: 63f4ee088efb09e9e58386ab273b997af4a9206d4c5c18e73c59fda5d42a7a57

MD:   research/firstbattle/data_driven_router_v3/protocol_g3.md
document hash: 4ffab243d5f894c0efdaa3f7f904222652c934e8e29096be7dabfca35d61124b

generation: firstbattle-data-driven-router-v3-g3
status: PREREGISTERED_LABELS_SEALED_PENDING_INDEPENDENT_REVIEW
```

真实资金流规则：

- 每个资金档的初始权益为固定保留资金基准 `B`；当权益 `>=1.5B` 时每次提取 `0.5B`，若一次跨过多档则连续提取，直到保留权益 `<1.5B`。
- 亏损不下调基准、不自动补本金；历史路径中强平或权益归零后停止交易，既往已提取利润保留。
- 唯一主指标改为 `累计提取金额 + 期末权益 - 初始外部投入`，并与 `ALWAYS_FLAT` 比较。
- 流动性真空、全相关、cost stress 等冻结场景继续强制运行并完整披露，但 synthetic stress 失败不再自动抹掉正常历史路径的正财富；真实 validation/WF/holdout 历史中的强平按权益归零计入。
- 历史归零路线只有在既往累计提取 + 期末权益仍超过初始投入、且全部统计门通过时才可保留，并强制标记 `HISTORICAL_RUIN_WITH_WITHDRAWAL_RECOVERY`。
- 账户总杠杆硬上限 5、unknown cost=`NO_TRADE`、完整成本和时间隔离不变；低于 80% 直接执行成本覆盖改为认证等级限制 `VALIDATED_MARKET_PATH_EXECUTION_EVIDENCE_LIMITED`，不再自动作为盈利否决。
- reviewer 只核验数学与证据，必须同时报告正常历史与全部压力情景的财富/归零/提取结果，不得用自创综合安全分替代用户风险偏好。

g3 独立复核通过前，禁止构建正式数据集或解盲标签。

### 5.17 数据驱动路由 V3 g4 提现执行与基线边界

g3 独立复核确认 hash/资金规则正确，但发现提现与保证金交互、DecisionTime、压力 override 范围和基线语义仍不够机械。用户再次明确绝对财富目标后，g3 尚未解盲标签、未构建数据集，现以 g4 overlay 取代：

```text
JSON: research/firstbattle/data_driven_router_v3/protocol_g4.json
hash: 2fd8c5852bba5023c9024d46b5b5e9527e71d670c370a1276e4728f4ef4cd2b9

MD:   research/firstbattle/data_driven_router_v3/protocol_g4.md
document hash: 6dd37c27f4d6b373e560af375f6ba47788dfcca567176d11d0a77c7929898c4b

generation: firstbattle-data-driven-router-v3-g4
status: PREREGISTERED_LABELS_SEALED_PENDING_INDEPENDENT_REVIEW
```

g4 补充：

- 提现只在 bar-close DecisionTime、完成 MTM 后且新开风险前判断；盘中权益尖峰不能触发。
- 实际提款为 `min(0.5B, equity - maintenance margin - frozen liquidation buffer)`；提款后立即重算权益、有效杠杆、保证金、强平距离和偿付，违反 5 倍或偿付时回滚到最大安全金额或 0。
- 未触碰的 chronological 历史账户路径是主路径；delay/cost/liquidity void/all-corr=1 必须完整披露并标记敏感性，但 synthetic stress 失败不自动否决正常历史正财富。
- 真实时间位置发生的历史亏损/强平仍全额进入主路径；把历史最坏事件复制到其他时点属于 synthetic stress。
- 唯一硬收益基线是 `ALWAYS_FLAT`，因为用户目标是绝对资金流财富增长；固定 long/short、buy-hold、随机同频和 BTC-beta 基线使用相同提款规则完整报告，但相对落后不自动否决。

g4 独立复核通过前，禁止构建正式数据集或解盲标签。

### 5.18 数据驱动路由 V3 g5 确定性提现与同口径基线

g4 独立复核为 `BLOCKED`：风险偏好方向正确，但提现 reserve、synthetic override 和基线 evaluator 尚不够确定。g4 尚未解盲标签、未构建数据集，现以 g5 overlay 取代：

```text
JSON: research/firstbattle/data_driven_router_v3/protocol_g5.json
hash: 518d0003c829da641f1bcc955c4382023e158ba9938c0ac6f0ff19b5bf729385

MD:   research/firstbattle/data_driven_router_v3/protocol_g5.md
document hash: e211ec431d8aa9d6ceaeb8e9907604041d5fb99121b447176067227621717b5e

generation: firstbattle-data-driven-router-v3-g5
status: PREREGISTERED_LABELS_SEALED_PENDING_INDEPENDENT_REVIEW
```

g5 补充：

- 提现 reserve 使用 micro-USDT 确定性闭式公式：`max(total_abs_notional/5, maintenance_margin + frozen_close_cost)`；无历史 MMR 且有持仓时提款为 0。
- g5 明确覆盖 g2 的 synthetic-stress 缩仓/否决：synthetic no-exit、最坏路径重放和 all-corr=1 仅披露，不改变 baseline policy；真实历史、5倍、known cost、实际时点保证金、非负现金、PIT/时间隔离仍为硬门。
- delay 1/2/3 与 1.5× cost stress 产生敏感性标签，不自动否决 baseline 正财富。
- 所有 candidate 与基线必须由同一中立 runner 使用完全相同的 state、时间、分区、成本、delay、funding、min-lot、保证金、allocator、提现、归零和执行顺序；唯一差异只能是 action policy。

g5 独立复核通过前，禁止构建正式数据集或解盲标签。

### 5.19 数据驱动路由 V3 市场状态数据集与 g6 账户状态协议

g5 已获独立 `APPROVED`。无标签 Data Fitness 在补齐 922 天官方 funding 后通过；正式市场状态数据集已经生成并独立复核：

```text
dataset: research/firstbattle/data_driven_router_v3/dataset_state_okx_1h_v2
manifest hash: 0c084dc440761569eeb0bbf9b078d653daf2688d65f5a1ac7c2c977c26691fa1
rows: 4,025,979
shards: 34
reward labels: SEALED_NOT_READ_OR_CREATED
market state layer: APPROVED
```

独立复核同时发现：市场 StateRow 不含当前仓位/账户状态，不能直接训练 `HOLD/ADD/TRIM/CLOSE/REVERSE`。因此在解盲 reward 前冻结 g6：

```text
JSON: research/firstbattle/data_driven_router_v3/protocol_g6.json
hash: 08b609e3271029b1e9f772921e325ef7c7fd30326589ae9c03cc86d73d9c844b

MD:   research/firstbattle/data_driven_router_v3/protocol_g6.md
document hash: e14ca0b8f36c1b5aca68798a22b48d937ef9086bb2d1bdee709f9c7771248328

generation: firstbattle-data-driven-router-v3-g6
status: PREREGISTERED_REWARD_LABELS_STILL_SEALED_PENDING_INDEPENDENT_REVIEW
```

g6 不改 4M 行市场数据；它增加 neutral-runner-owned `AccountContext`。模型只输出 target signed position / horizon / delay，由统一状态机按当前持仓派生 `NO_TRADE/LONG/SHORT/HOLD/ADD/TRIM/CLOSE/REVERSE`。ADD/TRIM/CLOSE/REVERSE/funding/pending/EOF/提现全部由同一个中立 runner 记账，四个模型不得各自实现账户状态。

g6 独立复核与账户状态 fixtures 通过前，reward label bundle 保持密封，不启动模型竞赛。

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

第 4–6 节保留 G1–G10 的历史事实、架构和认证门；旧 checklist 的未勾选项不再自动构成当前 Todo。

历史审计与存在性盘点已完成并冻结，结论仍是 `routes_reopened=0`。当前最高杠杆工作不再是调 G10 或继续解释旧输出，而是**获取与验收适配多路径的历史数据**。

当前数据优先顺序：

1. **冻结多路径数据合同**（已完成，见 5.8）
   - 根层状态/OOD 与四类专属路径分别冻结 required fields、稳定 identity、Event/Receive/Process 三时钟、PIT/KnownFrom、完整分母、匹配输家和停止门。
   - 普通 K 线或价格 proxy 不得冒充专属机制字段。
   - 产物 `research/firstbattle/route_data_contracts.{json,md}`，protocol hash `4a4c201d…c453588`；下一步 `build-readonly-downloaders` 须再次批准。

2. **建立物理 GET-only 获取边界**
   - 公共与私有 downloader 禁止 POST、下单、撤单、划转或账户设置；原始私人 ID 不进入研究产物。
   - 所有下载进入隔离 staging，具备 manifest、checkpoint、hash、rate/disk budget 与崩溃恢复；不得写实时 `records/`。
   - 用户已另行授权“有用的历史都要”。官方核验后白名单仅保留仍有效的 `POST /api/v5/account/bills-history-archive`：如 Data Fitness 证明有增量价值，可新建与 downloader/交易 adapter 物理隔离的 archive-application 客户端并先独立红队。`POST /api/v5/trade/fills-archive` 已于 2024-09-19 下线，禁止实现；下单、撤单、划转或账户设置永不在白名单。

3. **P0：私人执行 + 官方公共历史**
   - 优先补 attempt→order→fill 生命周期、attempted notional、partial/cancel/ack/fill clocks 与真实费用。
   - 获取并验收官方 tick trades、必要 L2、完整 instruments 字段；当前 snapshot 只向未来生效，不回填历史。

4. **P1：历史规则版本 + 强制流**
   - 官方来源调查已登记并独立核验；OKX/Bybit 长历史 liquidation 批量归档与历史 rule version archive 仍缺失。
   - 本地强平流仅作 schema seed；不等待未来样本。任何付费来源只提交清单并单独审批。

5. **P2：跨场所支持层**
   - 仅在数据合同证明非纯速度且个人可执行后，才获取同步双边盘口、双腿成本和资金授权历史；禁止一开始全市场下载。

6. **验收后仅裁决可测量性**
   - 先验证 provenance、PIT、三时钟、稳定 identity、完整分母、缺口/重复/乱序与 Data Fitness。
   - 只有完整观测与识别条件满足，才提交独立新 generation 建议；本阶段不运行收益、validation/WF、考试、shadow 或真钱。

已批准免费公开来源、现有凭据的私有只读查询，以及经物理隔离实现与独立红队后的 `POST /api/v5/account/bills-history-archive` 非交易归档申请。P0 长 orders 历史转为 Report Center Order CSV 人工导出（2021-01-28 起）+ 本地验收；不存在更长 orders archive API。付费来源可广泛调查但购买前逐项确认；下单/撤单/划转/账户设置、收益研究、考试或真钱仍未批准。

## 8. 当前状态结论

```text
永久成功标准：资金流调整后的真实账户净值长期增长
系统形态：根层状态/OOD -> 专属机制/方向/仓位/退出 -> 账户组合；无可靠路径即空仓
当前最高杠杆主线：获取并验收适配多路径路由的历史数据
工程状态：G10 v1 Brain 工程链与 fixture 完成；不等于盈利认证完成
研究状态：40,685 scans -> 101 candidates -> 31 Playbooks -> validation 0/31
正式执行证据审计：FULL_PREFIX 完成；2,802,749 projected rows；tests/race/vet PASS
唯一正式裁决：DATA_BLIND at observability
关键盲区：ctType 0/394；dynamic min-notional 0/12826；event/day/shock identity unavailable
可见执行证据：fee 973/1091；entry slip 296/1091；exit slip 196/1091；maker fill 29849/29849
证据冲突：suppressed 808；cross-source conflicts 42；unresolved identities 45,332
Route portfolio：4 条均 DATA_BLIND；2 保留不启动、1 休眠、1 近期归档；routes_started=0
重开条件盘点：micro PRESENT_PARTIAL；rule PIT_UNPROVEN；forced PRESENT_PARTIAL；cross-venue WEAK_PROXY_ONLY
PRESENT_COMPLETE：0；routes_reopened=0
WF 状态：SKIPPED_VALIDATION_FAIL；survivors=0
已证明赚钱叶子：0
密封考试通过叶子：0
shadow 通过叶子：0
真钱批准叶子：0
当前动作：firstbattle-data-driven-router-v3（V3 数据驱动路由）；已执行 todo supersede-mechanism-priors，旧机制先验隔离为 HYPOTHESIS_ONLY_NOT_MODEL_INPUT，边界见 research/firstbattle/data_driven_router_v3/supersession.{json,md}(hash 44f098e1…bad2 / doc b24e2639…12dc)；旧股票3-symbol合同 SUPERSEDED_SCOPE_REJECTED + HASH_INTEGRITY_DISPUTED_NOT_TRUSTED(不裁决/不改旧文件)；旧数据主线closure与P0 DataFitness仅作数据质量/成本证据，不作route label；未冻结V3协议、未构建数据集、未选route、未读收益
前一动作(冻结事实)：ACQUIRE_P0_HISTORY + VALIDATE_DATA_FITNESS；路由数据合同已冻结(protocol 4a4c201d…c453588)，来源 registry 已加入跨所 private-order retention 调查(hash 1c667256…5318)；SWAP 私人 orders/fills 与 current instruments 已发布，公共 1日×1标的在 allswap/空 L2 discovery 门 fail closed；无 VERIFIED 跨所长历史能补 no-fill 分母，P0 曾为Report Center CSV+本地WAL审计
labels_unblinded：false
routes_started：0
exam_actions：0
trade_allowed：false
系统状态：NO_GO
```

### 8.1 分层裁决词典

- **`SUPPORTED_FOR_NEW_GENERATION`**：数据可见、事前可区分、个人可执行且独立支持门满足；只允许提交新 generation 预注册。
- **`DATA_BLIND`**：关键机制变量未被现有数据观测；保留机制，关闭当前测量方案，仅允许有限期历史重建或代理提案。
- **`UNIDENTIFIABLE`**：现有历史无法区分机制与替代解释；保留假设但不得交易，仅新识别设计可重开。
- **`POSTHOC_ONLY`**：只能解释赢家，无法事前区分匹配输家；关闭当前表述，不得调阈值复活。
- **`UNEXECUTABLE`**：毛机会存在，但经本账户成本、排队、最小下单或延迟后不可捕获；关闭当前执行形式。
- **`INSUFFICIENT_SUPPORT`**：可观测但达不到 `≥50 事件 / ≥20 独立日 / ≥10 非重叠 shock blocks`；冻结结论，不等待未来数据，仅可寻找已有但尚未纳入的合法历史来源。
- **`FALSIFIED`**：观测、识别、预测、执行和样本均充分，净 edge 仍失败；关闭当前机制表述并转向非同源路线。

裁决默认关闭的是**当前可检验表述/generation**，不是未经充分观测的机制家族。机制保留不产生交易权，也不产生无限研究预算；每条路线必须预先限定工期、CPU、数据和尝试次数，触发停止门立即关闭。

### 8.2 不可越过的控制边界

- 维持 `global_halt`、`NO_GO`、`EXAM_UNAVAILABLE`、`trade_allowed=false`；不连接交易所，不创建、解封或消费考卷。
- 不修改 G1–G10 冻结协议、阈值、generation 或 hash，不用新解释篡改历史结论。
- 不修改真钱开关、运行控制面或 `records/`；阶段 F–H 保持未通过。
- 研究、fixture、validation、WF 或 shadow 的任何通过都不自动授予真钱权限；最终仍须用户明确批准。
