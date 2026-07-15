# OKX Report Center 私人长历史人工导出清单

> Status: `AWAITING_USER_MANUAL_EXPORT`
> Purpose: 延长 completed-order 状态覆盖并验收 CSV schema；不得声称完整 attempts 分母
> Privacy root: `/root/.lingxi-private/historydownload/report-center/inbox`

## 1. 先做最小 pilot

在 OKX Web/App 的 Order Center / History 中：

1. 选择 **Order History**；
2. 产品优先选择 SWAP / perpetual；
3. 选择一个最近完整自然月；
4. **不要勾选 Hide canceled**；
5. 选择 CSV（不选 PDF）；
6. 生成后下载，链接约 1 小时过期；
7. 保留原始文件名，不打开后另存、不用 Excel 改写；
8. 将文件放入：
   `/root/.lingxi-private/historydownload/report-center/inbox/order-history/recent-month/`

本 pilot 只验 header、行数、identity、状态和时钟，不读取或报告收益字段。

## 2. Pilot schema 通过后再导长历史

官方帮助页声明可下载范围自 `2021-01-28` 起。按 UI 允许的最大不重叠区间分批：

- Order History：P0，必须；
- Trading History：P0 supporting，用于 order↔trade join；
- Position History：P1 supporting，不用于 attempts 分母；
- Position Snapshot：暂不需要。

每个区间：

- 使用相同产品过滤；
- canceled 不隐藏；
- 区间不重叠、不留缝；
- 文件名外另建同名 `.source.json`，只记录导出类型、选择日期范围、下载 UTC 时间和官方页面 URL，不记录 ID 或收益。

## 3. 必须验收的 Order CSV 字段

以下字段在导入前均为 `UNVERIFIED`，不能假设存在：

- stable order identity：`ordId`；
- client/decision linkage：`clOrdId` 或其他 client order ID；
- instrument：`instId / instType`；
- order semantics：`ordType / side / posSide / px / sz`；
- state：至少 `filled / canceled / expired` 的实际编码；
- exchange clocks：creation/update/cancel time；
- accumulated fill size / partial fill indicator；
- cancel source/reason（若有）；
- API/manual source（官方说明报表可能不区分，缺失须登记）。

任一关键字段缺失保持 `MISSING`，不得补零或用行号造 identity。

## 4. Trading CSV 验收

目标仅为连接 orders，不读取收益：

- `tradeId / billId / ordId / clOrdId`；
- `instId / instType`；
- fill price/size/time；
- maker/taker 或 execType；
- fee/feeCcy。

`pnl / fillPnl / realized* / profit / MFE / MAE / future*` 可保留在私有 raw，但导入研究 projection 时必须 endpoint/template-specific 删除。

## 5. 完整分母停止门

即使长历史 CSV schema 通过，仍不得声称完整 attempts 分母，除非文件实际包含并可证明：

- rejected attempts；
- zero-fill canceled/expired；
- historical live-open denominator；
- decision-side non-submission；
- uptime/gap 与本地 ReceiveTime；
- attempt-time instrument PIT。

Report Center 的价值是延长 completed/canceled/expired 订单覆盖和独立日，不会自动解除 `INCOMPLETE_ATTEMPTS_DENOMINATOR`。

## 6. 隐私与导入边界

- 原始 CSV 只放仓库外 private root；
- 不放 `records/`，不提交 Git；
- 文件与目录权限 0600/0700；
- 导入器先 hash 原始字节，再做 tokenized projection；
- manifest 只记录文件 hash、行数、字段存在率、时间范围和 omitted counts；
- 不打印 raw order/trade ID、密钥或收益值；
- schema 不符立即 STOP，不手工改列后重试。

## 7. 官方来源

- https://www.okx.com/en-us/help/how-to-check-download-order-history-position-history-and-trading-history
- 下载范围：Order/Trading/Position History 自 2021-01-28 起；
- 在线页面通常只展示约 1 年，但下载范围更长；
- 报表不保证区分 API 与人工订单；
- 精确 CSV 列必须以实际 pilot 为准。
