# G10 执行证据审计正式阶段裁决

> Stage: `firstbattle.g10.execution-evidence-audit.final-verdict`
> JSON stage-summary hash: `05550c0603fa340939c35efa9bd212509ab48cec50b07e541f530e875a7817ff`
> Document hash: `0fca772df97bb474536aac64ea674a026a02d0a00762228fe224378b0b9ff8c0`
> Status: `STAGE_CLOSED_AT_USER_APPROVAL_POINT`

## 1. 正式裁决

G10 v1 / current measurement generation 的唯一正式分类：

`DATA_BLIND at observability`

这是 earliest-blocker 裁决，不是为 G10 辩护，也不声称机制存在。当前 measurement 无法看见关键 instrument 语义和 outcome-blind event identity，因此后续识别、事前预测、个人可执行、支持度和净 edge 层都没有合法到达。

不能写成：

- `FALSIFIED`：观测、识别、支持和净 edge 均未通过前置门；
- `POSTHOC_ONLY`：没有 event identity，无法先冻结 matched controls 和 baseline；
- `UNEXECUTABLE`：部分执行字段可见，但 critical-known instrument 与事件 linkage 不完整；
- `INSUFFICIENT_SUPPORT`：events/days/shock blocks 是不可得，不是观察到低于门槛；
- `UNIDENTIFIABLE`：它可能是下游风险，但 observability 是更早且已证明的 blocker；
- `SUPPORTED_FOR_NEW_GENERATION`：任何支持门都未满足。

七类互斥全集为：

`SUPPORTED_FOR_NEW_GENERATION / DATA_BLIND / UNIDENTIFIABLE / POSTHOC_ONLY / UNEXECUTABLE / INSUFFICIENT_SUPPORT / FALSIFIED`

## 2. Hash chain

- Audit protocol: `dccd7974a7d172e3894174a953c4cce02ea840a48bb41fbdec589ed4d1153dd3`
- FULL_PREFIX source manifest: `c8c5f38e6542855aa6fbc8e3373a751a24646922d0838a9774fce4a7e41a9379`
- Audit summary: `cf9da460a7807c4d131bbc1e2b22ec9c0e860902fbb6a9726707d7c697b9d9ab`
- Audit receipt: `29494eb6dfc62c8317315f93391aa60bd9d4f7ae515a166f94ae0a28c99b4f9a`
- Audit decision journal: `9ba234f0a6ff4f8d9cdcb830a4b8a40cf3a1e2ff5d6708d32d16706eba90afa3`
- Conflict matrix: `e37796a989c7d325e7d7809c8309f2881c6a0a1538834a84f5287c666f110105`
- Classification evidence: `8e5846c730a292645eedb63298c3535587d95cc9a13b3cd3cf88b2aa30d7fb18`
- Route portfolio JSON: `166ea394419544f55e54919216b6e67b31e2d1dfaf11ef27df06bb3686072977`
- Route portfolio Markdown: `733b7ae620b083eb18802bcdcb5c4520b345df7c05ef5da97140105d2efacec8`

## 3. 精确 observability 阻断

### Instrument 与 fee

- `ctType`: `0/394` known，`394` 个均为 `MISSING_CT_TYPE_DATA`。
- dynamic min-notional: `0/12826` known：
  - `12,644` 为 `MISSING_CT_TYPE_DATA`；
  - `182` 为 `MISSING_INSTRUMENT_METADATA`。
- exchange-fill fee_bps: `0/12826` 可合法归一化：
  - `12,644` 行缺 ctType；
  - `182` 行缺 instrument metadata。

exchange fill 源虽有 fee 字段，但缺合约/notional 语义时不能把它归一化为 fee_bps，也不能补零。

### Event identity 与支持域

- validation map 暴露 `84` 个 outcome-blind 聚合 cells；
- events、independent days、non-overlap shock blocks 均为 `null`；
- 没有 event/day/shock identity；
- winner binding 仅为 `PLAYBOOK_HASH_PLUS_OPAQUE_SOURCE_WINNER_IDS_HASH`；
- matched-control set hash 与 baseline-support set hash 均为 `null`；
- labels 未解盲，observed-failure denominator 为 `0`。

聚合 cell 不能冒充 event，也不能用 opaque winner hash 发明 matched losers。

## 4. 已看见但不足以越门的证据

冻结、去重后的 after-decision execution evidence：

- settlement fee_bps: `973/1091`
- entry slip: `296/1091`
- exit slip: `196/1091`
- maker fill: `29849/29849`

这些字段可审计其覆盖范围内的执行，但都是 `KNOWN_AFTER_DECISION` 证据，不能作为事前预测特征；也不能补齐 ctType、dynamic min-notional 或 event identity。

## 5. 冲突与去重不可隐藏

- suppressed lower-priority facts: `808`
- cross-source conflicts: `42`
- unresolved cross-source identities: `45332`
- severity: `CRITICAL_UNKNOWN`

来源优先级抑制不等于冲突消失。未解析身份不能重复累计、不能补零，也不能被写成通过。

## 6. 正式运行与验证

正式 FULL_PREFIX run：

- projected rows before dedup: `2,802,749`
- peak buffered rows: `220`
- peak buffered bytes: `170425`
- elapsed: `117530 ms`（约 `117.5s`）
- low CPU，exit code `0`

验证链：

- `GOMAXPROCS=2 go test ./... -count=1`
- `GOMAXPROCS=2 go test -race ./internal/routerg10/evidenceaudit ./cmd/router-g10-evidence-audit -count=1`
- `GOMAXPROCS=2 go vet ./...`

链式命令 exit code `0`，总 elapsed `188414 ms`；target race package `114.164s`。因此全量 tests、target race 和 go vet 均通过。

## 7. Route portfolio 裁决

四条机制路线当前均为 `DATA_BLIND`，routes started = `0`：

1. `micro_execution_capacity_cliff`：保留第一优先，不启动。
2. `exchange_rule_discontinuity`：保留第二优先，不启动。
3. `sparse_forced_flow`：休眠。
4. `cross_venue_operational_asymmetry`：近期归档，不启动。

排序仅使用 outcome-blind data fitness、可识别性、个人可执行性、机构排斥强度和最短证伪成本；不使用候选未来收益。

## 8. Closure scope

关闭当前 G10 v1 audit/measurement generation 的：

- 后续收益或 net-edge scoring；
- 从聚合 cells 推断 matched controls/support；
- validation/WF 继续推进；
- 考卷创建、解封或消费；
- route 自动选择或启动；
- 等待未来数据续命。

不关闭尚未充分观测的机制家族。机制保留不产生交易权、考试资格、自动立项权或无限预算。

## 9. 唯一可能的重开问题

下一步若由用户另行批准，只能先回答：

> 当前已经存在的历史资产，是否满足任一 RouteCard 的冻结重开条件？

这是 existence-only inventory，不是收益研究。至少需要：

- PIT-valid ctType/contract version 与 dynamic min-notional 语义；
- 稳定 outcome-blind event/day/shock identity，可冻结 winner/control/baseline；
- RouteCard 指定的 direct 或合法 reconstructable 字段；
- 不可变 source identity 和可复算 hash。

若找到，只向用户返回存在性清单；任何实验必须新 generation 预注册并再次批准。若未找到，维持 `DATA_BLIND`/归档并停止。禁止等待未来采集。

## 10. Decision journal 与责任

1. evidence-audit implementation owner：负责 source freeze、bounded-memory run、artifact integrity、测试与恢复。
2. LINGXI research governance owner：负责 earliest-blocker 分类、closure scope、portfolio 综合和最终责任结论。
3. governance reviewer（与实现 owner 分离）：复核 hash chain、分子分母、分层裁决和安全边界；不得自行批准新 route、考试或真钱。
4. 任一重开 route 仍需不同 reviewer 独立红队，提出/实现 owner 不得自批。

当前 journal 决策：冻结 FULL_PREFIX → 在 observability 停止 → 四路线不启动 → 停在用户审批点。

## 11. 最强不确定性

审计证明的是当前 measurement blind，不是 edge 存在。现有 records 可能偏向系统已选择/已成交路径；缺失 event 与 instrument identity 既可能遮住真实个人 edge，也可能只是阻止我们排除伪 edge。这个 generation 无法识别两者。

## 12. Safety

- `global_halt` 必须保持；
- `NO_GO`
- `EXAM_UNAVAILABLE`
- `exam_actions=0`
- `trade_allowed=false`
- 阶段 F–H 未通过；
- 不修改 records、G1–G10 frozen outputs/protocol、考卷或真钱开关。

## 13. Hash 复算

JSON：将 `hashes.stage_summary_hash` 置为 `null`，使用仓库 `datatruth.HashJSON`。

Markdown：将唯一一行 `> Document hash: \`<value>\`` 规范化为
`> Document hash: \`SELF_EXCLUDED\``，其余 UTF-8 LF 字节不变，再做 SHA-256。
