# G10 四路线重开条件存在性盘点

> Inventory ID: `firstbattle.g10.reopen-condition-inventory`
> Source manifest hash: `66b47378f674a2e9f8cdbe6e7649f11afee633610aa5ca330c09f2d38b8b79cd`
> Inventory JSON hash: `b9b62bc4d959d256e9d553385c2b3a474c1017632a35951a84e70d2a8e46e69a`
> Document hash: `b36dd23d57c5f89cc406957ecc79d4e655f4d42bd4d8d33673e1a45c3784f728`
> Status: `EXISTENCE_INVENTORY_COMPLETE_STOPPED`

## 1. 范围与绑定

本轮仅盘点当前已经存在的历史文件是否提供 RouteCard 所需字段、identity、PIT semantics 与完整分母。没有读取、汇报或排名任何收益、PnL、winner outcome；`labels_unblinded=false`。

绑定：

- Stage summary: `05550c0603fa340939c35efa9bd212509ab48cec50b07e541f530e875a7817ff`
- Route portfolio: `166ea394419544f55e54919216b6e67b31e2d1dfaf11ef27df06bb3686072977`
- Audit protocol: `dccd7974a7d172e3894174a953c4cce02ea840a48bb41fbdec589ed4d1153dd3`
- Formal audit source manifest: `c8c5f38e6542855aa6fbc8e3373a751a24646922d0838a9774fce4a7e41a9379`
- Audit summary: `cf9da460a7807c4d131bbc1e2b22ec9c0e860902fbb6a9726707d7c697b9d9ab`
- Audit receipt: `29494eb6dfc62c8317315f93391aa60bd9d4f7ae515a166f94ae0a28c99b4f9a`
- History DataHash: `f41ed2586d91054c4b5b2e15e60f32a4d64ccde0032e76dfdc5fc6dee316379c`

## 2. Source manifest 冻结方式

14 个新增候选 JSONL 仅冻结 capture 时最后完整 LF 的 byte prefix。每项记录 relative path、device/inode、capture time、capture/after file size、byte prefix、完整行数、prefix SHA-256、首末完整行 hash。

- chunked streaming，不复制 raw rows；
- 同 inode append 可继续，但 capture 后字节忽略；
- truncate、inode 变化或 prefix mutation 必须 fail closed；
- partial trailing line 排除；
- 不输出敏感 ID 或 raw value；
- 含 outcome/private 字段的文件只做 raw-byte hash 或 schema-only 投影，禁止读取相应值。

哈希期间 `fast_dynamic_feature_events.jsonl` 与 `fast_market_mids.jsonl` 继续同 inode 追加；manifest 正确保留原 byte prefix 并标记 `FROZEN_PREFIX_APPEND_IGNORED`。

已被正式 evidence audit 绑定的 maker/fills/open decisions/shadow/instrument/equity/settlements 不重复扫描，只引用正式 source manifest entry hash。

## 3. 新增合法历史源

主要 frozen prefixes：

- OKX liquidation：62,728 行，prefix `6cb46516…f48a`
- Bybit liquidation：85,960 行，prefix `ae21e08f…605f`
- dynamic features：1,881,078 行，prefix `f2e563aa…ac66`
- timestamped hot mids：421,144 行，prefix `774e3dae…3c53`
- OKX hot micro：416,862 行，prefix `3d4e9b75…e8ee`
- full burst probe：529,575 行，prefix `c62ec5f4…0954`
- market mids：163,883 行，prefix `e37d8d76…b305`
- multi-venue funding/OI snapshots：1,733 行，prefix `4877cace…ca59`
- firstbattle mechanism shadow：500 行，prefix `c10c0b36…a24c`
- rule probe：4 行，prefix `d55e090e…08c4`
- fast/slow position cleanup：10 / 2 行
- MEXC/OKX tape probe：22,192 行
- selected cross-venue settlements：214 行

这些文件此前不在正式 audit 白名单中；“存在”不等于满足 route reopen condition。

## 4. 路线结论

```text
micro_execution_capacity_cliff       PRESENT_PARTIAL
exchange_rule_discontinuity          PIT_UNPROVEN
sparse_forced_flow                   PRESENT_PARTIAL
cross_venue_operational_asymmetry    WEAK_PROXY_ONLY

PRESENT_COMPLETE                     0
routes_reopened                      0
```

### 4.1 Micro execution capacity cliff

存在：

- 正式 maker attempts：29,849/29,849 writer-scope attempts，含 recorded_at、maker price、wait、filled、fill price；
- exchange fills：12,826 行，含 trade/bill/order IDs、fill size/price/time/fee；
- open decisions：14,773 行，含 position ID 与 decision result；
- rule probe 4 行含少量 req_qty/notional；
- firstbattle shadow 有 decision_id/card event-receive fragment；
- position cleanup 有少量 position/event fragment。

阻断：

- maker rows 没有 shared order ID 或 attempted notional；
- fill IDs 无法连接 maker attempt；
- formal audit 有 45,332 个 unresolved cross-source identities；
- 没有 partial-size + signal/ack/cancel/fill 完整时钟；
- execution WAL writer/schema 存在于代码，但 `records/execution.wal` 与 `records/okx_directional/execution.wal` 均不存在；
- 500 条 mechanism shadow 只覆盖约 2 秒 selected subset，不是 outcome-blind denominator。

结论：真实 fragments 存在，但共享 lifecycle 与完整分母缺失，`PRESENT_PARTIAL`。

### 4.2 Exchange rule discontinuity

存在：

- 当前 instrument snapshot 有 394 个 live symbols 的 `list_time_ms/ct_val/lot/tick/state`；
- exchange fills 有 `instId/instType`；
- xmeta 有 1,733 个 multi-venue funding/OI snapshots；
- rule probe 有 4 行单一实验片段。

阻断：

- snapshot 无 `ctType/effective_at/KnownFrom/rule version`；
- snapshot 只可从 `fetched_at=2026-07-04T15:38:18.189311Z` 向后生效，不得回填；
- xmeta vector 是 funding/OI，不是 instrument/rule metadata；
- history catalog 的 7 个 data families 没有 instrument/rule archive；
- production InstrumentCatalog writer model 虽存在，实际 catalog 文件不存在，且当前 model 本身也没有 ctType；
- `instrument_rule_archive.jsonl` 不存在；
- 没有规则版本两侧的 outcome-blind event denominator。

结论：当前值可见但历史 PIT 未证明，rule archive=`ABSENT`，路线=`PIT_UNPROVEN`。

### 4.3 Sparse forced flow

此前 formal audit 漏纳了两个直接来源：

- OKX public liquidation stream：exchange timestamp、local recorded_at、forced side、qty、bankruptcy price；
- Bybit public all-liquidation stream：venue、exchange timestamp、local recorded_at、raw/normalized side、qty、price。

它们是 direct forced-action observation，不是价格 proxy。

仍缺：

- 稳定 exchange event ID；`venue+symbol+exch_ts+side+qty+price` 只能作为待预注册 composite；
- event/receive/process 真三时钟，目前只有 exchange timestamp + recorded_at；
- collector subscription/uptime/reconnect gap 完整性 manifest；
- payer disappearance；
- 冻结 shock-cluster identity 与完整 event denominator。

dynamic/burst/mechanism-shadow 可提供 decision-time context 或 selected identity fragments，但不能补足上述缺口。

结论：从“源未纳入”提升为 `PRESENT_PARTIAL`，仍不能重开。

### 4.4 Cross-venue operational asymmetry

存在的 component：

- `fast_hot_mids` 有 Binance/Bybit/Gate/OKX-spot timestamped anchor mids；
- OKX executable shadow/hot micro 有单场所 bid/ask/depth；
- MEXC/OKX tape probe 有 22,192 条 trade prints；
- selected cross-venue settlements 有少量两腿费用/滑点 fragment；
- equity snapshots 有 venue cash；
- xmeta 有 multi-venue funding/OI。

阻断：

- timestamped anchor 是 mid proxy，不是同步双边 executable bid/ask/depth；
- 只有 OKX book，没有 peer venue executable book；
- selected settlements 不构成所有 eligible attempts/misses denominator；
- 缺 venue PIT ctType/spec versions；
- cash 不证明 borrow/transfer/collateral authorization；
- `synchronized_executable_books.jsonl` 与 `collateral_authorization_history.jsonl` 不存在；
- 无共享 executable event identity。

component 可标 `PRESENT_PARTIAL`，但 route-level 仍为 `WEAK_PROXY_ONLY`。

## 5. Writer code 与真实资产的区别

发现三类“代码有、生产文件无”：

1. execution WAL schema 支持 decision/intent/order/trade IDs 与 fill payload，但无生产 WAL；
2. PIT InstrumentCatalog model 支持 KnownFrom，但无生产 catalog，且 schema 缺 ctType；
3. fast peer lead/lag、versioned rule archive、同步 executable books 与 collateral authorization history 的 expected paths 均不存在。

所有 absent path 在 manifest 中以 `ABSENT_EXPECTED_PATH` + `hash:null` 登记，没有伪造 hash。

## 6. 两 owner 对账

一致点：

- 四条路线均非 `PRESENT_COMPLETE`；
- micro 缺 shared lifecycle；
- rule 缺 PIT ctType/version archive；
- forced stream 是 direct observation，但没有完整 denominator；
- timestamped mids 不是 executable books；
- 不得自动重开。

口径差异及裁决：

- Terra 可从 forced rows 构造 composite identity；治理裁决记录为 `RECONSTRUCTABLE`，但不等于稳定 exchange event ID。
- Terra 将 anchor mids、selected settlements 视为 `PRESENT_PARTIAL` components；治理保留 component 状态，但 route-level 为 `WEAK_PROXY_ONLY`。
- mechanism shadow 有 decision ID；治理认定其 500 行/约 2 秒 selected subset 不能构成完整分母。

无未解决的 owner 冲突。

## 7. Decision Journal

1. 新 live sources 按 complete-LF prefix 冻结；正式 audit sources 只引用、不重扫。
2. forced-flow 从假定 missing 修正为 `PRESENT_PARTIAL`。
3. cross-venue 不因 timestamped mids 升级为 executable。
4. micro 保持 `PRESENT_PARTIAL`；rule 保持 `PIT_UNPROVEN`。
5. `routes_reopened=0`，停止。

## 8. 最强不确定性

新增 forced-action 与 timestamped-anchor sources 确实被 formal audit 漏纳，但其 collector uptime 与 schema 边界不完整。它们可能支持新的测量设计，也可能继续产生 selection/gap bias；source existence 既不证明完整 falsification denominator，也不证明净 edge。

## 9. 下一步与 STOP

本次存在性盘点已完成，没有路线可直接进入新 generation。

若后续仍只使用当前冻结的现有历史，可另行提出：

> 针对 `PRESENT_PARTIAL` 路线的新测量设计。

这需要新的用户审批；不得自动预注册、启动实验、读取收益、运行 WF、考试或交易。当前动作：`STOP`。

## 10. Hash 复算

Source manifest：将 `hashes.manifest_hash` 置为 `null` 后使用 `datatruth.HashJSON`。

Inventory JSON：将 `hashes.inventory_hash` 置为 `null` 后使用 `datatruth.HashJSON`。

Markdown：将唯一 `> Document hash: \`<value>\`` 行规范化为
`> Document hash: \`SELF_EXCLUDED\``，保持其余 UTF-8 LF 字节不变，再做 SHA-256。
