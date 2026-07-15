# G10 执行证据回收审计预注册

> Protocol ID：`firstbattle.g10.execution-evidence-audit`
> Generation：`firstbattle-g10-execution-evidence-audit-v1`
> Protocol hash：`dccd7974a7d172e3894174a953c4cce02ea840a48bb41fbdec589ed4d1153dd3`
> 状态：`PREREGISTERED_NOT_RUN`
> 机器可读真相：`g10_execution_evidence_audit_preregistration.json`

## 1. 问题重述与边界

本审计不负责证明 G10 必须成功，也不把 validation 0/31 自动解释为机制失败。它只回答阻断最早发生在哪一层：

`observability -> identifiability -> ex_ante_predictability -> personal_executability -> independent_support -> net_edge`

当前冻结事实是：G10 v1 扫描 40,685 次，得到 101 个个人候选和 31 个 Playbook；validation 0/31、observed validation events=0、底层原因 `COVERAGE`，0 survivor，WF skipped。Brain fixture 通过只证明工程路径可运行，不是盈利认证。

审计只读已有历史，不等待未来数据，不修改 G10 v1 generation、协议或产物，不读取考卷，不改变真钱开关，不写 `records/`，不连接交易所。输出继续保持 `exam_actions=0`、`trade_allowed=false`、`NO_GO`。

## 2. 审计方案选择

预先比较四种设计：

1. **完整不可变副本**：隔离最强，但复制多 GB 运行日志会增加 I/O、磁盘和私人原始行处理风险，拒绝。
2. **字节前缀清单 + 流式字段投影**：冻结每个输入的完整换行前缀、hash、行数和 cutoff；追加内容不进入；只解析允许字段。**采用。**
3. **事件时间 cutoff 重扫**：迟到/回填行和时钟歧义会改变成员集合，拒绝。
4. **只冻结行数**：不能发现前缀原地改写，malformed/partial 行会使解码 offset 漂移，拒绝。

采用方案避免复制约 2 GB shadow 文件，同时比现有“decoded line offset” checkpoint 更严格：恢复时必须重验字节前缀和全部绑定。

## 3. 允许输入

运行态证据仅限：

- `records/okx_directional/settlements.jsonl`
- `records/okx_directional/fast_v2_settlements.jsonl`
- `records/fast_exchange_fills_cache.jsonl`
- `records/okx_directional/fast_maker_attempts.jsonl`
- `records/okx_directional/fast_executable_shadow_events.jsonl`
- `records/okx_directional/open_decision_events.jsonl`
- `records/okx_directional/instrument_meta.json`
- `records/crossvenue/equity_snapshots.jsonl`

冻结研究绑定仅限：

- G10 1h/5m validation event maps 的 outcome-blind trigger 投影；
- 冻结 Playbook 与 manifest 的身份、public trigger 和 proof bindings；
- JSON 中列出的 G10 protocol/history/fingerprint/map/playbook/validation/WF hash 链。

禁止新增其他来源，禁止把当前 universe 回填历史，禁止在审计期间吸收 append 新行。

## 4. 稳定输入边界

每次正式运行先生成 `source_manifest.json`，然后才能解析：

1. 对 JSONL 使用 stat-before/read-prefix/stat-after 稳定性检查；只冻结最后一个 LF 结束位置。
2. 记录 device/inode、相对路径、capture time、byte prefix、完整行数、prefix SHA-256、首末完整行 hash。
3. 尾部 partial line 排除并单独报告，不算 malformed；capture 后新增字节忽略。
4. 完整但 malformed 的行只输出隔离 hash 和计数，不输出原文；该行影响的关键字段保持 `UNKNOWN`，source 标为 degraded。
5. 可覆盖写的 `instrument_meta.json` 必须在稳定 stat 区间内原子复制到新的 audit `input_snapshots/`，记录 whole-file hash。
6. 任何 prefix mutation、truncate、inode 变化、绑定漂移均 fail closed。

`captured_at` 只是冻结时刻，不替代 event time。成员资格只由冻结 byte prefix 决定。

source manifest 固定 envelope 为 schema/generation/protocol/captured_at/sources/manifest hash；sources 按 source ID 排序，将 `manifest_hash` 置 null 后用 `datatruth.HashJSON` 计算。全部 source entries 校验完成后才原子写 manifest，写后禁止追加来源。

## 5. Data Fitness Matrix

收益或裁决前，必须按 `route_id + required_field` 生成矩阵。每行至少包含：

- 字段语义、所需 falsification layer、来源；
- `DIRECT / RECONSTRUCTABLE / WEAK_PROXY / MISSING`；
- `KNOWN_AT_DECISION / KNOWN_AFTER_DECISION / PIT_UNPROVEN / UNKNOWN`；
- 合法重建公式或弱代理限制；
- coverage numerator/denominator、缺失原因；
- false-negative risk、leakage risk、evidence hash。

JSON 零值不能证明字段已知；实现必须维护 presence bit。`KNOWN_AFTER_DECISION` 可作执行结果证据，但不能作事前预测特征。`WEAK_PROXY` 不得升级为交易所真值，`UNKNOWN` 不得补零。

## 6. 冻结执行语义

### 6.1 Fee、notional 与 slip

- 实际 fill price/size/time/fee 优先级：exchange fills > settlements > fast_v2 settlements。
- fee 必须先证明币种并按成交时点合法换算为 USDT，再算 `abs(fee_usdt) / notional_usdt * 10000`；缺换算、notional 或符号语义即 `UNKNOWN`。
- linear contract notional 可按 `abs(fillSz) * ctVal * fillPx` 重建；inverse/币本位或缺字段保持 `UNKNOWN`。
- settlement 显式 entry/exit slip 是直接证据。重建必须绑定决策时可见 reference mid/quote：long 为 `(fill-reference)/reference*10000`，short 反向。
- 只有成交价而没有 reference 时不得推断零滑点。

### 6.2 Latency 与 maker

- signal-to-ack、ack-to-fill 必须各自具备两端时钟；缺任一端即 `UNKNOWN`。
- maker attempt 的显式 `maker_wait_ms` 可作直接 wait 证据。
- shadow `eval_rest_ms` 仅是 `WEAK_PROXY`，不得冒充 signal-to-ack-fill latency。
- maker fill rate 分母是所有语义有效、冻结的 maker attempts；只有显式 `maker_filled=true` 计 filled。缺记录或未匹配不能自动计失败。

### 6.3 Instrument 与 open/close pairing

- instrument 版本必须证明 `KnownFrom <= decision_time`；当前 snapshot 没有历史 PIT 证明时只能 `PIT_UNPROVEN/WEAK_PROXY`。
- tick/lot/min size/ctVal/ctType 全已知且 linear，才计算动态最小名义。
- 优先用稳定 position/order lifecycle identity 配对；无稳定 ID 时只允许 venue + symbol + side 下唯一、守恒的事件时间 FIFO/size pairing。
- 并发歧义、方向/数量/时序冲突即不可识别；unmatched 行仍贡献自身可证明字段，但不产生零收益、零 fee 或零 slip。
- partial fill 分段配对并保留 remainder，禁止当 full fill。

## 7. 去重、冲突与来源等级

- exchange fill 首选 `venue+tradeId`，其次 `venue+billId`，最后才用冻结复合键。
- settlement、maker attempt、decision、shadow、equity 的键与 fallback 在 JSON 中冻结。
- 原始 order/trade/position ID 只在内存中以 audit-salt token 做 join；不得写入输出。
- exact duplicate 只保留一份；跨源重复建立 provenance link，不重复累计。
- 高优先来源可决定单一事实，但所有冲突必须进入 conflict matrix。身份、单位或时序不能证明一致时，该事实保持 `UNKNOWN`，不得静默覆盖。

## 8. Outcome 防火墙与倒序对照

Phase A 只读取 trigger identity/support 字段和 Playbook public bindings。禁止字段包括 return、PnL、profit、winner、future outcome、conservative net、MFE/MAE 和 subsequently profitable。

每个冻结 winner seed 必须同时绑定：

- 全部 outcome-blind support domain；
- profile/event family/horizon/direction 完全匹配的 control pool；
- 决策时 volatility、liquidity、UTC session、min-notional bucket 的预冻结粗匹配；
- 每 seed 最多 5 个、不放回 controls；
- support domain 的基础发生率。

排序只用冻结 bucket 距离、decision time 和 event ID。无合格 control 判识别不足，不能删除 seed。

只有 `winner_seed_set_hash`、`matched_control_set_hash`、`baseline_support_set_hash`、matching config hash、Data Fitness Matrix hash 全部冻结后，独立 Phase B 才可读取既有冻结标签用于评分。解盲后任何口径变化都必须新 generation。

只能解释 winner、无法事前区分匹配 controls 时裁决 `POSTHOC_ONLY`，不得调 bucket 或阈值复活。

## 9. Critical-known 与成功门

每条路径必须知道 fee、entry/exit slip、maker fill、latency、lot、contract value/type、dynamic min-notional；每项至少 3 个有效样本。

既有成功门原样保留：

- `>=50` events；
- `>=20` independent days；
- `>=10` non-overlap shock blocks；
- critical-known 全部通过。

任一 UNKNOWN 阻塞对应层，禁止降低门槛。

## 10. 唯一结果分类

每条路线只能进入一类，按最早已证明 blocker 裁决：

- `DATA_BLIND`：关键变量 missing/weak proxy，可能造成假阴性。
- `UNIDENTIFIABLE`：变量可见，但无法区分替代解释或匹配 controls。
- `POSTHOC_ONLY`：只能解释 winner，无法事前区分 matched controls。
- `UNEXECUTABLE`：毛机会存在，但本账户真实成本、延迟、排队或最小下单后不可捕获。
- `INSUFFICIENT_SUPPORT`：可观测且可识别，但支持门或 critical-known 样本不足。
- `FALSIFIED`：前五层与样本均充分，冻结机制表述的净 edge 仍失败。
- `SUPPORTED_FOR_NEW_GENERATION`：六层均获支持；只允许建议新 generation 预注册。

observability、identifiability、critical-known 或样本门未通过时，禁止判 `FALSIFIED`。所有裁决默认只关闭当前可检验表述，不默认关闭机制家族；保留机制不产生交易权或无限预算。

## 11. 实现与输出契约

Terra owner 必须输出：

- source manifest；
- Data Fitness Matrix；
- field completeness 与 critical reasons；
- support counts（按 Playbook/symbol/day/direction）；
- winner/control/baseline bindings；
- falsification chain；
- classification evidence；
- conflict matrix；
- decision journal；
- audit summary、receipt、checkpoint 及全部 output hashes。

必须分别输出“数据未看见”和“看见后失败”的分母。checkpoint 绑定 protocol/source manifest/projection/matching/output-dir hash，使用 frozen byte offset + complete-line ordinal；恢复结果必须 byte-identical。输出同目录临时写、fsync、rename，最终 manifest 最后写。

每个 JSON artifact 将自身 hash 字段置 null 后用 `datatruth.HashJSON`；JSONL 对完整 LF 行的冻结原始字节前缀做 SHA-256；receipt 绑定按相对路径排序的 artifact hashes。

## 12. 最强反例与未知项

1. 记录可能只覆盖系统已选择和已成交路径；真实个人 edge 若发生在未记录路径，本审计仍会选择性失明。
2. fill 与策略 intent 可能没有足够稳定的 join key；并发订单下时间/价格/数量配对可能不可识别。
3. instrument metadata 可能缺历史 PIT 版本，动态最小名义只能是弱代理。
4. `eval_rest_ms` 不是完整延迟；没有 ack/receive timestamp 时 latency 必须 UNKNOWN。
5. validation trigger 物理文件含 outcome 字段；字段投影或两阶段 hash 防火墙实现错误会造成后视泄漏。
6. byte prefix 防追加但不天然防原地改写；运行和恢复都必须复验 prefix hash。

这些不确定性不得被解释成零收益或机制失败。

## 13. Hash 与 drift 检测

协议 hash 复算规则与仓库 `datatruth.HashJSON` 一致：

1. JSON 解析为通用对象；
2. 将 `audit.protocol_hash` 置为 `null`；
3. 递归规范化、由 Go `encoding/json` 确定性编码；
4. SHA-256，输出小写 hex。

实现必须嵌入 protocol hash、来源、枚举、critical fields、支持门和 forbidden labels，并测试 manifest/code drift。当前冻结 hash：

`dccd7974a7d172e3894174a953c4cce02ea840a48bb41fbdec589ed4d1153dd3`
