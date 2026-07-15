# 多路径路由数据合同（根层 + 四类专属路径）

> Contract ID: `firstbattle.route-data-contracts`
> Generation: `firstbattle-route-data-contracts-v1`
> Protocol hash (self-excluded): `4a4c201d2c8cedde5bd1ff55ea40ad31d099e189feb709a517701b504c453588`
> JSON: `research/firstbattle/route_data_contracts.json`
> Document hash: `68dc6666fc0138850fff2911965e5ef05505797ac9fc6137ddde928744e74639`
> Status: `FROZEN_CONTRACT_DEFINITION_STOPPED`
> Todo: `freeze-route-data-contracts`

## 0. 最高宗旨与边界

> LINGXI 永久、唯一、最高优先级目标：在不同市场状态、特殊事件和个人执行条件下，动态路由到各自经过历史证据认证的专属盈利路径；没有可靠匹配路径时必须空仓。最终以资金流调整后的真实账户净值长期增长为唯一成功标准。

- 本文件**只冻结数据合同**：先定义、后下载。不下载、不读取收益、不选路线、不启动实验。
- **不是万能策略**：完整系统是 `根层状态/OOD -> 专属机制/方向/仓位/退出 -> 账户组合`；无可靠匹配即 `NO_TRADE`。
- **路径独立认证**：四条专属路径各自拥有独立的 required fields、identity、三时钟、PIT、完整分母、匹配输家、最小支持、来源优先级、forbidden fields 与停止门；单路失血不拖垮全树。
- 普通 K 线或价格 proxy 不得冒充专属机制字段。
- `labels_unblinded=false`，`routes_started=0`。

## 1. 上游冻结绑定（REFERENCE_ONLY_NO_MODIFY，加载时逐一比对，不匹配 fail closed）

```text
history_data_hash:                              f41ed2586d91054c4b5b2e15e60f32a4d64ccde0032e76dfdc5fc6dee316379c
forced_liquidation_g1_protocol_hash:            6b16eb531230a14242f29d398fd357188867062f2fb2f0f0af6b7f554dd83bcb
hierarchical_router_g1_manifest_hash:           0eab9bda13055209c81d2b47f0286a0317b79dbb642592ec1b00cd4771397d31
g10_protocol_hash:                              9f86ce54c24b00de41d1a6735a4f8190b00d49dce2a66371242646ffecd10750
g10_execution_evidence_audit_protocol_hash:     dccd7974a7d172e3894174a953c4cce02ea840a48bb41fbdec589ed4d1153dd3
g10_execution_evidence_audit_source_manifest:   c8c5f38e6542855aa6fbc8e3373a751a24646922d0838a9774fce4a7e41a9379
g10_execution_evidence_audit_summary:           cf9da460a7807c4d131bbc1e2b22ec9c0e860902fbb6a9726707d7c697b9d9ab
g10_execution_evidence_audit_stage_summary:     05550c0603fa340939c35efa9bd212509ab48cec50b07e541f530e875a7817ff
g10_wild_route_portfolio_json:                  166ea394419544f55e54919216b6e67b31e2d1dfaf11ef27df06bb3686072977
g10_reopen_condition_inventory_json:            b9b62bc4d959d256e9d553385c2b3a474c1017632a35951a84e70d2a8e46e69a
g10_reopen_condition_inventory_source_manifest: 66b47378f674a2e9f8cdbe6e7649f11afee633610aa5ca330c09f2d38b8b79cd
```

## 2. 官方已确认来源覆盖（数据来源优先级与停止门的硬约束）

| 来源 | 覆盖 | 提供 | 约束 |
|---|---|---|---|
| OKX private `orders-history-archive`（Read API） | 最近约 3 个月 | ordId/clOrdId、ordType、state、cTime/uTime、fee/feeCcy、撤单来源 | API 只回溯约 3 个月；`pnl` 等结果字段禁止进研究层 |
| OKX private `fills-history`（Read API） | 最近约 3 个月 | tradeId、ordId、fillPx/fillSz/fillTime、execType(T/M)、fee | API 只回溯约 3 个月；`fillPnl` 禁止进研究层 |
| OKX Report Center 人工导出 | 约 2021 起更长 | Order/Trading/Position history | 人工下载（自动化不可得）；未导入前私人完整分母只覆盖约 3 个月，通常不足门槛 |
| OKX 官方公共 tick trades | 2021-09 起 | 逐笔 ts/px/sz/side | 公共 tape，无私人执行 identity |
| OKX 官方 L2 order book | 2023-03 起 | 历史盘口快照 | 单场所盘口 |
| OKX instruments 快照 | 当前 | ctType/minSz/lotSz/tickSz/ctVal/state/listTime | 仅向未来生效，禁回填；当前 ctType `0/394` known |
| 付费 / whitelist 来源 | — | — | 只列价格/字段/覆盖/预期价值清单，单独审批，禁止一开始全量下载 |

## 3. 合同设计选择（>=3 候选，择一）

| 设计 | 概述 | 裁决 |
|---|---|---|
| A 单一巨型共享 schema | 全部路径共享同一六源 context | REJECTED：任一源缺字段致盲全树，破坏独立认证 |
| **B 分层根门 + 独立路径合同** | 冻结根门作为共同前置，四路径各自独立、按 hash 绑定根门、独立认证 | **CHOSEN** |
| C outcome-anchored / winner-first | 以观测赢家反推 required fields | REJECTED：锚定 forbidden outcome fields，泄漏与选择偏差，无法事前冻结匹配输家 |
| D availability-first / downloader-driven | 先看 API 返回再定义合同 | REJECTED：可得性定义机制，违反先定义后下载，会合理化 3 个月窗口/字段缺口 |

`chosen_design = B_LAYERED_ROOT_GATE_PLUS_INDEPENDENT_ROUTES`。选择 **B** 的理由：对齐路由漏斗 `ROOT -> DataTruth -> GlobalContext -> MechanismFamily -> Leaf`；每条路线独立分母与停止门，符合最高宗旨与研究纪律；根门 fail-closed 保证无匹配即 `NO_TRADE`。

## 4. 公共约定

- **三时钟**：`EventTime`（venue authoritative）/ `ReceiveTime`（本地 recorded_at）/ `ProcessTime`（落盘）。`DecisionTime = max(EventTime, ReceiveTime) + FeatureLag`；特征只用 `ReceiveTime <= DecisionTime`，标签只用 `DecisionTime` 之后。三时钟缺一按缺失登记，不得互相顶替。
- **最小支持门（每条认证叶子独立）**：`>= 50 事件 / >= 20 独立 UTC 日 / >= 10 非重叠 shock blocks`；不足判 `INSUFFICIENT_SUPPORT`（仅在 observability/identifiability 通过后可达）。禁止用聚合 cell 冒充 event，禁止跨路线共享分母。
- **数据来源优先级**：`tier0 官方权威+PIT可证 > tier1 本地带三时钟+uptime manifest > tier2 可重建 composite > tier3 弱代理`；禁止用快照回填 PIT、禁止 K 线顶替机制字段、禁止未审批付费全量下载。
- **outcome forbidden fields**：`future_return, return, pnl, fillPnl, realized_pnl, realized_usdt, profit, winner, future_outcome, mfe, mae, upnl_after_decision`。OKX history 的 pnl/fillPnl 可留本地私有 raw，禁止进研究/特征层。
- **可证伪阶梯**：`observability -> identifiability -> ex_ante_predictability -> personal_executability -> independent_support -> net_edge`，前层不过不得越级。
- **停止门语义**：任一 required field/identity/三时钟/PIT/完整分母缺失即停，按 earliest-blocker 分类，不因沉没成本续命。

## 5. 根层 Context / OOD 合同（GATE，不认证叶子）

- **role**：`GATE_NOT_MECHANISM`。为所有路线提供决策时刻全局状态、OOD 边界、市场/事件 identity 与 universe PIT。
- **required fields**：全局风险/regime 特征（BTC beta、相关簇、波动）、OOD 边界特征、市场/事件 identity、PIT universe membership(KnownFrom)、每源 missing/late/out-of-order/duplicate/correction 率。
- **identity**：`root_decision_id = (decision_time_bucket, universe_snapshot_known_from)`；当天开端前未 KnownFrom 的 shortlist 不得覆盖该日。
- **三时钟**：各 context 源保留三时钟；缺失/乱序 -> `QUALITY_INVALID` -> `NO_TRADE`。
- **PIT/KnownFrom**：只用 DecisionTime 之前可见值，禁未来快照回填。
- **完整分母**：scope 内通过 DataTruth 的**全部** decision timestamps；每条路线分母都是本门通过集合的子集。
- **匹配输家**：根门不匹配（非机制层），但定义所有路线匹配输家所处的 outcome-blind 总体。
- **停止门**：任一 required context 源缺失/停更/乱序/质量异常 -> fail closed -> `NO_TRADE`；universe 无法覆盖当天开端 -> 当日该源路线分母作废。
- **当前状态**：`GATE_ACTIVE_FAIL_CLOSED`；2026-07-11 context smoke 因 `DEPTH_MISSING/MIDS_MISSING/OI_STALE/UNIVERSE_MISSING/QUALITY_INVALID/INCOMPLETE_DAY` 正确 FAIL。

## 6. Route 1：`micro_execution_capacity_cliff`（TOP_PRIORITY_NOT_STARTED）

- **机制**：微小订单在 fill 概率/冲击/排队毒性/授权摩擦上可能优于放大订单；付款者是需立即成交或撤出流动性的对手方。机构排斥须来自每事件硬容量、固定审批/监控成本与大单自冲击，不能来自“不知道”。公开后若可统一批量化并聚合，机制关闭。
- **required fields**：稳定 lifecycle identity（`decision_id<->clOrdId<->ordId<->fill tradeId`）、attempted notional/size、ordType/post-only/报价偏移、partial fill 序列（fillSz/fillPx/fillTime/fee/liquidity flag/execType）、ack/cancel(+source/reason)/fill/cTime/uTime、决策时刻 depth/spread/mid、队列位置代理(WEAK)、PIT ctType/minSz/lotSz/tickSz/ctVal、outcome-blind event identity(symbol/session/spread/depth/notional bucket + maker action)。
- **identity**：order lifecycle ID；当前 formal audit 有 `45,332` 未解析 cross-source identity，join 未证明唯一。
- **三时钟**：Event=交易所 ack/fill(cTime/uTime/fillTime)；Receive=recorded_at；Process=落盘。
- **PIT/KnownFrom**：attempt 时点 instrument 语义须 KnownFrom；当前 ctType `0/394`、dynamic min-notional `0/12826`，未证明。
- **完整分母**：scope 内全部尝试（filled + partially filled + cancelled + rejected + expired），非只已成交。
- **匹配输家**：同 symbol/session/spread/depth/notional bucket 且同 maker action 的全部 attempts（fill/no-fill 属执行物理，非 forbidden PnL；净 edge 仍 forbidden）。
- **最小支持**：`50 / 20 / 10`。
- **来源优先级**：tier0 OKX orders-history-archive + fills-history（约 3 个月）+ Report Center 补长；tier1 本地 maker_attempts + exchange_fills + open_decisions + execution WAL（WAL 生产文件当前 ABSENT）；tier3 shadow depth/spread（仅上下文）。
- **停止门**：lifecycle 无法唯一 join，或 attempted notional/完整分母/PIT ctType 缺失 -> 停。预算 `1 CPU / 8h / 1 次 / 仅冻结历史`。
- **当前分类**：`DATA_BLIND`（下游风险 `UNIDENTIFIABLE/POSTHOC_ONLY/UNEXECUTABLE`）。

## 7. Route 2：`exchange_rule_discontinuity`（SECONDARY_NOT_STARTED）

- **机制**：ctVal/lot/tick/minSz/结算/迁移/风控规则离散变化，使微小账户位于可执行侧、大资金跨过断点。付款者是被规则迫使调整订单/仓位/抵押品的一侧。机构若能零成本自动分拆则护城河失败。
- **required fields**：PIT 版本链（ctType/minSz/lotSz/tickSz/ctVal/ctValCcy/state/listTime/effective_at/KnownFrom）、规则公告(delist/listing/参数变更)+发布时间、离散边界两侧 event identity、两侧 session/volatility/direction。
- **identity**：`(instId, rule_version, effective_at)` + 被迫调整的订单/仓位 event identity。
- **三时钟**：Event=effective_at/公告 publish；Receive=捕获时刻；Process=落盘。
- **PIT/KnownFrom**：须带 KnownFrom 历史版本链；当前只有单快照，rule archive `ABSENT`，故 `PIT_UNPROVEN`。
- **完整分母**：跨越同一离散边界两侧的全部 outcome-blind instrument-events。
- **匹配输家**：同 symbol/version/session/volatility/direction、边界两侧的全部 outcome-blind events。
- **最小支持**：`50 / 20 / 10`。
- **来源优先级**：tier0 OKX instruments 快照（仅向未来生效）+ 官方规则公告/变更记录 + Report Center；tier2 fill instId/instType 语义交叉检查（不得补造 ctType）；forbidden 快照回填 PIT。
- **停止门**：无 KnownFrom 历史 PIT 版本链或无边界两侧 event identity -> 停。预算 `1 CPU / 8h / 1 次`。
- **当前分类**：`DATA_BLIND` / `PIT_UNPROVEN`（下游风险 `UNIDENTIFIABLE/POSTHOC_ONLY`）。

## 8. Route 3：`sparse_forced_flow`（DORMANT_NOT_STARTED）

- **机制**：清算/到期/保证金/规则整改/抵押品短缺迫使一侧短窗成交；个人只在小容量且付款者仍存在时提供或撤回流动性。价格 shock/funding/taker flow 不能证明 forced payer。
- **required fields**：forced action identity（稳定 exchange event ID）、venue/symbol/forced side/size/price/bankruptcy price、collector subscription/uptime/reconnect-gap 完整性 manifest、payer disappearance/episode 完成标记、冻结 shock-cluster identity、事件时点 instrument spec。
- **identity**：稳定 exchange event ID；过渡 composite `(venue+symbol+exch_ts+side+qty+price)` 为 `RECONSTRUCTABLE`，须预注册，不等于稳定 ID。
- **三时钟**：Event=交易所 forced ts；Receive=recorded_at；Process=落盘。当前仅可靠两时钟，无 uptime/gap manifest。
- **PIT/KnownFrom**：事件时点 instrument spec 须 KnownFrom。
- **完整分母**：uptime 校验窗口内全部 forced episodes（非只交易过的）；缺 uptime manifest 不得声称完整。
- **匹配输家**：同 action type/liquidity/direction/intensity/session/prior volatility 的 episodes。
- **最小支持**：`50 / 20 / 10`。
- **来源优先级**：tier0 官方 provenance 可证 liquidation/forced 历史（若付费只列清单）；tier1 本地 OKX(62,728)/Bybit(85,960) liquidation streams（schema seed，需补 uptime manifest）；tier3-forbidden price shock/funding/taker-flow 顶替 forced identity。
- **停止门**：无稳定 forced event ID + 真 event/receive time + uptime 完整分母 + payer disappearance -> 停。预算 `1 CPU / 12h / 1 次`，不等待未来 liquidation。
- **当前分类**：`DATA_BLIND`（sources `PRESENT_PARTIAL`；下游风险 `UNIDENTIFIABLE/POSTHOC_ONLY/INSUFFICIENT_SUPPORT`）。

## 9. Route 4：`cross_venue_operational_asymmetry`（NEAR_TERM_ARCHIVE_ONLY）

- **机制**：非纯速度型的规格/抵押品/授权/结算/最小下单差异，使一个场所被迫调整、另一场所可小单承接。纯 quote 差或亚秒速度不是个人 edge。
- **required fields**：双场所同步双边 executable bid/ask/depth、各 venue 时钟、双腿 fee/slip/latency、各 venue PIT ctType/spec 版本、collateral/borrow/transfer/authorization state、跨场所 event identity。
- **identity**：venue pair 上的同步 executable event ID。
- **三时钟**：Event=各 venue 盘口/成交 ts；Receive=recorded_at；Process=落盘。
- **PIT/KnownFrom**：各 venue instrument spec 版本须 KnownFrom。
- **完整分母**：全部同步 executable、资金已就位、eligible 的跨场所窗口（misses + takes）。
- **匹配输家**：同 venue-pair/spec/session/depth/collateral state 的窗口。
- **最小支持**：`50 / 20 / 10`。
- **来源优先级**：tier0 官方 per-venue L2（OKX L2 自 2023-03；peer venue 需申请/whitelist -> 只列清单）+ funding/borrow 历史；tier3-forbidden timestamped anchor mids 顶替同步 executable book。
- **停止门**：仅当合同先证明机制非纯速度且个人可执行才重开，否则归档。预算 `1 CPU / 6h / 1 次`。
- **当前分类**：`DATA_BLIND` / route-level `WEAK_PROXY_ONLY`（下游风险 `UNIDENTIFIABLE/UNEXECUTABLE`）。

## 10. Drift 要求

- **protocol hash 自排除**：`hashes.protocol_hash` 计算时置 null，用 `datatruth.HashJSON`（Go 确定性编码 + SHA-256 小写 hex）。
- **禁止静默修改**：任一字段变更须开新 generation 与新 hash。
- **三方漂移**：本 JSON（manifest）、下载器/消费者代码常量、运行时配置三方必须一致，不匹配 fail closed（对齐强平 g1）。
- **绑定漂移**：`bindings` 内上游冻结 hash 加载时逐一比对，不匹配 fail closed。
- **JSON↔MD 一致性**：route_id、current_classification、minimum_support、bindings、chosen_design 必须一致。
- **MD hash 自排除**：将唯一一行 `> Document hash: \`<value>\`` 规范化为 `> Document hash: \`SELF_EXCLUDED\`` 后对整文件 UTF-8 LF 字节做 SHA-256。

## 11. Owner Decision Journal

1. **问题重述**：在不下载、不读取收益、不改冻结产物前提下，先定义根门与四路径决策时刻真正需要的数据合同，使后续获取/验收有可复算、可 fail-closed 的目标。
2. **竞争假设**：`H0` 选择/成本 null、`H1` 规则离散、`H2` 稀疏强制流、`H3` 微执行断层、`H4` 跨场所非对称。
3. **最可能被忽略的盲区/反例**：机构可用统一基础设施在多标的/多场所复制小单，把单事件小容量聚合成机构级容量；且 OKX 私有 API 仅约 3 个月窗口会让 micro/forced 路线完整分母天然不足 `20 日/50 事件`，Report Center 未导入前极易把窗口不足误当机制边界。
4. **拟采方法与弃选理由**：选 B（分层根门 + 独立路径）；拒 A（致盲全树/破坏独立认证）、拒 C（outcome 泄漏/无法事前冻结匹配输家）、拒 D（可得性定义机制/合理化窗口缺口）。
5. **已花预算**：仅读取现有 inventory/portfolio/stage summary/审计源码与冻结 hash，定义合同并计算自身 hash；未下载、未读收益、未跑实验。
6. **停止决策**：合同冻结完成即停在用户审批点；不自动进入 downloader 或任何获取动作。
7. **reviewer 分离**：任何路线重开、实验、考试或真钱仍须不同 reviewer 独立红队，提出/实现 owner 不得自批。

## 12. Safety

```text
global_halt:      true
system_status:    NO_GO
exam_status:      EXAM_UNAVAILABLE
exam_actions:     0
trade_allowed:    false
labels_unblinded: false
routes_started:   0
not_modified:     records/ | G1-G10 冻结协议/hash/generation/产物 | 考卷 | 真钱开关/控制面 | plan.md
```

## 13. Hash 复算

- **JSON**：将 `hashes.protocol_hash` 置为 `null` 后用仓库 `datatruth.HashJSON`。
- **Markdown**：将唯一一行 `> Document hash: \`<value>\`` 规范化为 `> Document hash: \`SELF_EXCLUDED\``，保持其余 UTF-8 LF 字节不变，再做 SHA-256。
