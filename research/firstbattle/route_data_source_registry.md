# 多路径路由数据来源注册表（调查/登记）

> Registry ID: `firstbattle.route-data-source-registry`
> Generation: `firstbattle-route-data-source-registry-v1`
> Registry hash (self-excluded): `1c6672563d4bc00ea102df2f9575b365a2a9dc30e763db75067f9a49052e5318`
> Document hash: `483ee318d954cf939646f0a9305e155ab908a3918a75afdfd6fb5f7bb149a0cf`
> JSON: `research/firstbattle/route_data_source_registry.json`
> Status: `INVESTIGATION_REGISTRY_NO_ACQUIRE`
> Todo: `investigate-forced-and-rules`（仅调查/注册表部分）
> Evidence date: `2026-07-13`

## 0. 最高宗旨与边界

> LINGXI 永久、唯一、最高优先级目标：在不同市场状态、特殊事件和个人执行条件下，动态路由到各自经过历史证据认证的专属盈利路径；没有可靠匹配路径时必须空仓。最终以资金流调整后的真实账户净值长期增长为唯一成功标准。

- 本文件只登记来源可用性与 route fit；**不下载大数据、不购买、不读取收益、不启动路线**。
- **不修改**冻结合同 `route_data_contracts`、G1–G10、records、考卷、真钱、plan。
- `secret.yaml` 被跟踪等凭据处置问题**不是**数据来源阻断（用户已覆盖）。
- 用户已授权有价值的非交易历史归档申请；官方核验后当前唯一白名单候选为 `POST bills-history-archive`，只能进入独立 archive-application 客户端并先红队。`fills-archive` 已下线，禁止实现；GET downloader 与交易 adapter 继续禁止 POST，下单/撤单/划转/账户设置永不在白名单。
- **P0 只有** OKX private GET + 官方公共 pilot；L2 **不得**挤占私人执行。

## 1. 绑定（REFERENCE_ONLY_NO_MODIFY）

```text
route_data_contracts_protocol_hash:                 4a4c201d2c8cedde5bd1ff55ea40ad31d099e189feb709a517701b504c453588
g10_reopen_condition_inventory_json_hash:           b9b62bc4d959d256e9d553385c2b3a474c1017632a35951a84e70d2a8e46e69a
g10_reopen_condition_inventory_source_manifest_hash: 66b47378f674a2e9f8cdbe6e7649f11afee633610aa5ca330c09f2d38b8b79cd
```

## 2. Priority 裁决

| Priority | Combo | 说明 |
|---|---|---|
| **P0（唯一）** | `combo_p0_okx_private_plus_public_pilot` | 私人 orders/fills GET → instruments 快照 → 公共 1日×1标的 trades/funding → 预算剩余才 L2 小样 → Report Center 人工清单 |
| P1 | `combo_p1_forced_reality` | 本地强平 PRESENT_PARTIAL + 官方强平 ABSENT 登记；不 reopen |
| P2 | `combo_p2_cross_venue_l2_prep` | OKX/Bybit 官方免费 L2；Binance bookDepth≠L2；tick L2 停止 |
| PAID | `combo_paid_gap_list` | Tardis 等只列公开价/字段；不买 |

P0 顺序冲突时：**丢掉 L2，不丢私人执行**。

## 3. OKX 官方公共 Historical Data

| 项 | 内容 |
|---|---|
| Portal | https://www.okx.com/historical-data |
| Discovery | `GET /api/v5/public/market-data-history` → https://www.okx.com/docs-v5/en/#public-data-rest-api-get-historical-market-data |
| CDN | `https://static.okx.com/cdn/okex/traderecords/` |
| Provenance | 官方门户 + REST 返回的 `static.okx.com` URL |
| Coverage | trades **2021-09+**；L2 **2023-03+**；funding **2022-03+**；borrowing **2021-12+**；candles 2023-07+ |
| Clocks/PIT | EventTime=文件内交易所时间；无官方 ReceiveTime；**不是**规则 PIT |
| Route fit | micro 上下文；跨场所 OKX 腿；强制流仅弱上下文 |
| Disk | discovery 的 `sizeMB`；L2≫trades；本 todo **禁止**多日全量 |
| Risk | 部分网络对 discovery **403**（地区/WAF）；SWAP discovery 可能返回 `allswap-*` 全品种包而非单标的小文件；L2 module 映射待 pilot |
| Pilot | `BTC-USDT-SWAP` × 1 日：trades + books + funding；先验 discovery 包范围，再估算磁盘 |
| Stop | 持续 403 且无替代出口 → 暂停 CDN，保住 P0 私人路径 |

2026-07-14 真实 GET 元数据探测（未下载 ZIP）：

- trades `module=1 / SWAP / BTC-USDT-SWAP / daily / 2026-07-10` 返回 `allswap-trades-2026-07-10.zip`（263.72MB），并额外返回 2026-07-11 `allswap` 包（177.2MB），没有 exact single-instrument group；
- L2 `module=4` 返回空 `details`；
- 结论：`FAIL_CLOSED_NOT_1D1S_NO_ARCHIVE_DOWNLOADED`，不能把全市场包标成 1日×1标的 pilot。

## 4. OKX 私人执行

| Source | Method | Coverage | Status |
|---|---|---|---|
| `orders-history-archive` | **GET** | 近 **3 个月** | P0 |
| `fills-history` | **GET** | 近 **3 个月** | P0 |
| Report Center Order/Trading/Position | **人工导出** | 下载自 **2021-01-28**；Position snapshot 自 2022-12-01（单日） | `MANUAL_EXPORT_UNVERIFIED` |
| `bills-history-archive` | **POST 申请 + GET** | 2021-02 起（不含当前季度） | `P1_AUTHORIZED_NOT_IMPLEMENTED`：仅独立非交易 archive applicant；补账本/费用，不补 attempts 分母 |
| `fills-archive` | POST（历史端点） | 曾为约 2 年 | **DELISTED_2024-09-19 / STOP**，禁止实现 |

- Docs（orders）：https://www.okx.com/docs-v5/en/#order-book-trading-trade-get-orders-history-last-3-months
- Docs（fills）：https://www.okx.com/docs-v5/en/#order-book-trading-trade-get-transaction-details-last-3-months
- Report Center Help：https://www.okx.com/en-us/help/how-to-check-download-order-history-position-history-and-trading-history
- Identity：`ordId/clOrdId/tradeId`；研究层禁止 `pnl/fillPnl`
- 分页：orders 的 `after/before=ordId`、`begin/end=cTime`；fills 的 `after/before=billId`、`begin/end=ts`；`limit≤100`。`instType` 必须显式且按类型隔离 checkpoint/dataset。
- 状态边界：orders-history-archive 官方 state 为 `canceled/filled/mmp_canceled`；不能据此补造 rejected/expired/live-open attempts。
- 长 orders：v5 无更长 orders archive API；P0 唯一路径是 Report Center Order History 人工 CSV（2021-01-28 起），精确列/ID/时钟须导入后验收。
- Stop：无法形成 attempts 完整分母或 order↔fill 唯一 join → 停 micro 测量。

### 4.1 跨所私人 order-history 对 denominator 的调查

- **Bybit**：取消/拒绝/Deactivated 约只保留 24h，超过 7 天主要只剩 filled 或部分成交后撤；长历史为 survivor-only，停止长拉。
- **Gate futures**：zero-fill canceled 约在取消后 10 分钟不可查；长 finished-order 历史不能形成 no-fill 分母。
- **Binance futures async order export**：端点存在，但是否保留 `CANCELED/EXPIRED × executedQty=0` 未验证；只允许一个最小 7–30 日 P1 pilot，配额语义不清或无 no-fill 即停。
- **MEXC futures history_orders/export**：state 含 canceled/invalid，但深历史是否保留 `dealVol=0` 未验证；仅在账户有相关活动时做 bounded P1 pilot。
- 裁决：没有 VERIFIED 的现成跨所长历史能解除 `INCOMPLETE_ATTEMPTS_DENOMINATOR`。公共 trades/L2 与付费 Tardis 只有 market context 价值，不能顶替 personal attempts。

## 5. Instruments / 规则 PIT

| Source | Availability | PIT |
|---|---|---|
| Current `public/account instruments`（含 ctType 等） | `PRESENT_SNAPSHOT_ONLY` | 仅 KnownFrom=抓取时刻向前 |
| 官方历史 rule/instrument version archive | **ABSENT** | **PIT_UNPROVEN** |
| 公告碎片 / 2026-01-15 后自动调参默认不公告 | 碎片 | **不能**组成版本链 |

Stop：禁止用当前快照或公告回填历史 PIT。`exchange_rule_discontinuity` 保持 `DATA_BLIND/PIT_UNPROVEN`。

## 6. 官方强平历史 vs 本地自采

| Venue | Official batch history | Verdict |
|---|---|---|
| OKX | Historical Data **无**长历史 liquidation 批量产品；public REST/WS 仅近期滚动 | **ABSENT（长窗批量）** |
| Bybit | History Data **无** liquidation 包 | **ABSENT** |
| Binance UM | Vision `liquidationSnapshot/` **空前缀** | **ABSENT** |
| Binance CM | Vision CM `liquidationSnapshot` 有限存在 | **LIMITED**（Coin-M only） |
| Local OKX/Bybit WS collectors | 62,728 / 85,960 rows（inventory-bound） | **PRESENT_PARTIAL** |

**不能**把本地自采称为官方完整分母（缺稳定 exchange event ID、uptime/gap 分母、payer disappearance）。

## 7. L2 三家对照

| Venue | Verdict | Notes |
|---|---|---|
| OKX | 官方免费可 pilot | L2 自 2023-03；P0 次级，不得挤占私人执行 |
| Bybit | 官方免费可 pilot | `quote-saver.bycsi.com`；BTCUSDT ob500 listing 自 2023-01-18；日文件体积须 pilot 前重新 HEAD 核验；P2 |
| Binance Vision `bookDepth` | **非 L2** | percentage/notional 汇总带 |
| Binance tick L2 (`T_DEPTH`) | **停止 / 不可依赖** | 官方支持于 2024-11 停止（public-data #437）；旧 `Futures_Order_Book_Download` 路径截至 2026-07-13 为 404 |

## 8. 付费候选（只列不买）

| Vendor | Classification | Public price | Value |
|---|---|---|---|
| **Tardis.dev** | `NON_OFFICIAL_THIRD_PARTY` | Perpetuals Academic→Business **$350–$3000/月**；All Exchanges **$650–$6000/月**（https://tardis.dev/，2026-07-13） | 系统性补强平历史 ABSENT 的候选；不替代私人 lifecycle / 规则 PIT；**未购买** |
| Kaiko / CoinAPI 等 | `NON_OFFICIAL_THIRD_PARTY` | **无公开可信价 → 询价**；不猜 | 未展开 |

## 9. ≥3 Source 组合

1. **P0（推荐唯一立即路径）**：OKX private orders+fills + instruments snapshot + Historical Data 1日×1标的 pilot + Report Center 人工清单。
2. **P1**：P0 + 本地强平 PRESENT_PARTIAL + Binance CM 对照；官方 OKX/Bybit/UM 强平 ABSENT。
3. **P2**：OKX+Bybit 官方 L2；拒绝 Binance bookDepth/停产 tick L2。
4. **PAID list**：Tardis（有公开价）+ 其他询价；全部待单独审批。

## 10. Owner Decision Journal

1. **问题重述**：登记官方强制流与规则版本调查结果，并锁死 P0=私人执行优先。
2. **竞争假设**：官方强平长历史存在 / 规则版本库存在 / 应先拉 L2 / Report Center 已可当验证分母 / Tardis 可称官方 — 均 REJECTED 或降级。
3. **最可能被忽略的反例**：把本地强平或 CM snapshot 写成官方完整分母；用公告伪造成 PIT；L2 挤掉私人 GET。
4. **方法**：官方页与已完成探测；GET downloader 继续弃绝 POST；有价值的 archive apply 由另一个物理隔离边界实现并红队；弃选 bookDepth-as-L2、未审批购买。
5. **已花预算**：调查综合 + registry；无 bulk download / purchase / outcome read。
6. **停止**：本 todo 调查/注册表部分完成即停；获取属后续 todo。
7. **reviewer 分离**：不自批 Data Fitness 或路线重开。
8. **独立官方源复核**：确认核心来源边界，同时修正 fills=`billId` 分页、OKX 近期强平 REST、`allswap-*` 包风险、Bybit L2 起点及 Binance 旧路径 404；仍未下载归档或调用私有 API。
9. **真实 discovery 反馈**：SWAP 单标的请求实际返回 `allswap` 全市场 trades 包且 L2 为空；按停止门不下载，优先保留已完成的私人 orders/fills。

## 11. Safety

```text
global_halt: true
system_status: NO_GO
exam_status: EXAM_UNAVAILABLE
exam_actions: 0
trade_allowed: false
labels_unblinded: false
routes_started: 0
no_purchase: true
no_bulk_download: true
no_commit: true
```

## 12. Hash 复算

- **JSON**：将 `hashes.registry_hash` 置为 `null` 后用仓库 `datatruth.HashJSON`。
- **Markdown**：将唯一一行 `> Document hash: \`<value>\`` 规范化为 `> Document hash: \`SELF_EXCLUDED\``，保持其余 UTF-8 LF 字节不变，再做 SHA-256。
