# LINGXI 数据优先主线交接（2026-07-14）

> 状态：`USER_REQUESTED_IMMEDIATE_STOP`
> Git commit：未提交
> HEAD / origin/main：`ab2780405f0fd5c80d268fe0feff39eb14a29eda`
> 系统：`NO_GO / EXAM_UNAVAILABLE / trade_allowed=false`
> 路线：`routes_reopened=0`

## 1. 最高宗旨

> LINGXI 永久、唯一、最高优先级目标：在不同市场状态、特殊事件和个人执行条件下，动态路由到各自经过历史证据认证的专属盈利路径；没有可靠匹配路径时必须空仓。最终以资金流调整后的真实账户净值长期增长为唯一成功标准。

不是寻找万能策略。数据、代码、模型与风控只服务于“不同情况进入各自经历史认证的机制、方向、仓位和退出路径”。

## 2. 用户停止命令

用户于 2026-07-14 要求：

> 立即停止所有操作，并生成可由下一 Agent 直接接替的交接文档。

已执行：

- 中断/停止 Report Center reviewer 与 Binance async owner；
- 终止遗留 Data Fitness 测量进程；
- 取消未完成 Todo；
- 未停止用户正在运行的 LINGXI 生产服务；
- 未修改、删除或回退实时 `records/`；
- 未 commit。

下一 Agent 在用户再次明确要求继续前，不应自动恢复后台任务或联网。

## 3. 必读顺序

1. `FIRST_BATTLE_MASTER_PLAN.md`
2. `.cursor/rules/lingxi-research-discipline.mdc`
3. `/root/.cursor/plans/lingxi_数据优先主线_f249821c.plan.md`
4. 本交接文档
5. `research/firstbattle/route_data_contracts.json`
6. `research/firstbattle/route_data_source_registry.json`
7. `research/firstbattle/p0_swap_data_fitness.json`
8. `research/firstbattle/p0_swap_data_fitness_measurement.json`
9. `research/firstbattle/okx_report_center_manual_export_checklist.md`

## 4. 已冻结/已验证核心 hash

```text
route data contract:
  protocol_hash = 4a4c201d2c8cedde5bd1ff55ea40ad31d099e189feb709a517701b504c453588

source registry:
  JSON = 1c6672563d4bc00ea102df2f9575b365a2a9dc30e763db75067f9a49052e5318
  MD   = 483ee318d954cf939646f0a9305e155ab908a3918a75afdfd6fb5f7bb149a0cf

P0 SWAP Data Fitness:
  JSON = 393a12952b480fc2671774d76790e5f850bcbc26b43c8eb8d870bba9c9e2bd66
  MD   = 697cf3686f72748459d89943c1f45f0071e670f5d22a36f23f61d0154707e899
  measurement receipt =
    f23a815fba52dab22368428a6a95250a1ad58e429c09613fea0ac977b1030c92
```

Data Fitness v3 独立 Sonnet verdict：

`PASS_DATA_FITNESS_CLASSIFICATION`

批准范围仅为：

`当前数据维持 DATA_BLIND / routes_reopened=0`

不批准收益研究、validation、WF、考试、shadow 或真钱。

## 5. 已获取的仓库外数据

### 5.1 OKX private SWAP orders

位置：

`/root/.lingxi-private/historydownload/api/v5/trade/orders-history-archive/SWAP`

```text
pages=137
records=13,622
raw_bytes=13,251,276
duplicate=0
missing_identity=0
manifest_hash=3bd6db35f9270cf22183249d542fbb0acd62d5aa70d6ae7199d23acb639a55fa
```

### 5.2 OKX private SWAP fills

位置：

`/root/.lingxi-private/historydownload/api/v5/trade/fills-history/SWAP`

```text
pages=157
records=15,676
raw_bytes=7,801,031
duplicate=0
missing_identity=0
manifest_hash=390c153265d29d7147ca7efcfc1bea38941beffa05df540bbd2895b04a3bc295
```

### 5.3 Current OKX SWAP instruments

位置：

`/root/.lingxi-data/historydownload/instruments`

```text
rows=420
ctType/minSz/ctVal present=420/420/420
current_hash=34cca1c1f629c6f0271722e905ea7bff04d5f1352b7562bc21d5a04c27f1099d
manifest_hash=073f3087042402e004e99cf211a069b36c6312b9bd429f61646bf444d8510715
PIT=SNAPSHOT_ONLY/PIT_UNPROVEN
historical_catalog_allowed=false
```

私人 raw、salt、checkpoint 和 tokenized projection 均在仓库外，不得复制进仓库或 `records/`。

## 6. 正式 Data Fitness 结论

生成器：

- `internal/historydownload/datafitness/`
- `cmd/p0-swap-data-fitness/`

输入 projection evidence：

```text
orders:
  137 pages / 13,622 rows / 7,334,063 bytes
  aggregate_hash=e2cad38f667a0895fcc7ad0d4961b5bf5b3d8d67d3dd3c5e931618cee877b6b3

fills:
  157 pages / 15,676 rows / 9,829,988 bytes
  aggregate_hash=36d3fd5e3e0fe267af94122e590da077fb49fa3b290d90268b488d9dc8fa2b2a
```

关键事实：

```text
order↔fill token join:
  matched=13,622
  unmatched=0

orders states:
  filled=13,566
  canceled=56
  canceled with accFillSz>0=56
  no-fill orders=0

coverage:
  distinct UTC days=26
  top-two-day share=81.742769%

cross-endpoint instId drift:
  conflicting orders=168
  conflicting fill rows=199
  orders instId=DATA-USDT-SWAP-OFF20260702-2
  fills instId=IP-USDT-SWAP

symbols:
  orders=144
  fills=144
  union=145
  current snapshot missing=2
```

正式 earliest blocker：

`INCOMPLETE_ATTEMPTS_DENOMINATOR`

secondary：

- `CROSS_ENDPOINT_INSTRUMENT_IDENTITY_DRIFT`
- `ORIGINAL_RECEIVE_TIME_ABSENT`
- `DECISION_ID_ABSENT`
- `INSTRUMENT_PIT_UNPROVEN`

100% join 只证明 survivor-biased API slice 的引用完整性，不证明完整 attempts 分母。

## 7. 五节点/四路线当前终态建议

尚未生成最终 `route-measurability-closure` 工件。

当前 Data Fitness 支持：

```text
root_context_ood:
  GATE_ACTIVE_FAIL_CLOSED

micro_execution_capacity_cliff:
  DATA_BLIND / CURRENT_GENERATION_CLOSED

exchange_rule_discontinuity:
  DATA_BLIND / PIT_UNPROVEN / CURRENT_GENERATION_CLOSED

sparse_forced_flow:
  DATA_BLIND / PRESENT_PARTIAL / CURRENT_GENERATION_CLOSED

cross_venue_operational_asymmetry:
  DATA_BLIND / WEAK_PROXY_ONLY / CURRENT_GENERATION_CLOSED

routes_reopened=0
```

不能写成 `FALSIFIED`：关闭的是当前可检验表述/generation，不是声称机制不存在。

## 8. 公共历史 smoke

实现：

- `internal/historydownload/okxarchive/`
- `cmd/okx-history-download --mode archive-smoke`

独立 verdict：

`PASS_FOR_PUBLIC_DISCOVERY_AND_EXACT_1D1S_DOWNLOAD`

真实 official discovery：

- `module=1 / SWAP / BTC-USDT-SWAP / 2026-07-10`
- 返回 `allswap` 全市场包 263.72MB，并额外返回次日 177.2MB；
- module=4 L2 返回空 details；
- CLI 在 `discovery detail range outside exact request` 停止；
- ZIP transport 未触发；
- `/root/.lingxi-data/historydownload/archive-smoke` 未创建。

这代表来源不满足冻结的 1d1s 合同，不是 downloader 未完成。不得下载 `allswap`。

## 9. Report Center 状态

人工操作清单：

`research/firstbattle/okx_report_center_manual_export_checklist.md`

状态：

`AWAITING_USER_MANUAL_EXPORT`

Inspector：

- `internal/historydownload/reportcenter/`
- `cmd/okx-report-center-inspect/`

已完成：

- per-canonical source alias allowlist；
- outcome source header 双层 firewall；
- raw 同 FD 双 hash；
- root/inbox/mapping/generation/current 全链 dirfd/openat；
- current/receipt/projection/checkpoint/mapping/raw hash 验证；
- timezone-qualified/epoch UTC 时间；
- formula/Unicode/control 拒绝；
- crash/root-swap/tamper tests。

最后 reviewer 在用户停止前确认唯一 MEDIUM（epoch `.UTC()`）已闭合，且关键锚点未回退，但没有留下新的显式
`PASS_REPORT_CENTER_INSPECTOR` 字符串。

交接时保守状态：

`IMPLEMENTATION_FIXED_FINAL_VERDICT_NOT_RECORDED_DUE_STOP`

下一 Agent 若继续，只需：

1. 对最新代码重跑 target/race/vet；
2. 用独立 Sonnet 做一次最终只读复验；
3. 得到显式 PASS 后，等待用户按清单提供 recent-month CSV。

## 10. Binance async pilot 状态

目录：

- `internal/historydownload/binancereadonly/`
- `cmd/binance-order-history-pilot/`

目标：

一次固定 7 日 async order-history pilot，检查
`CANCELED/EXPIRED × executedQty=0` 是否存在。

红队曾正式：

`BLOCK_BINANCE_ASYNC_PILOT_IMPLEMENTATION`

owner 在被停止前正在修复：

- decimal zero；
- quota GET 前不可逆 intent；
- job receipt 先持久化再 status；
- crash recovery / one-shot 去重；
- status polling 上限；
- legacy flat Binance credentials；
- fixed completed window；
- dirfd persistence；
- downloadId token；
- 官方 download host / max window 证据。

用户停止时的 owner 明确未完成点：

- 最新 artifact/ZIP fd-relative 修改尚未编译测试；
- target/race/vet/gofmt/static/diff 尚未最终复跑；
- 尚未重新提交 Sonnet；
- connected path 仍因 download host 未被官方钉死而预检 STOP；
- 未进行任何 Binance 私有 API 请求，未消耗 quota。

下一 Agent 禁止直接 `--connect`。先检查未完成 diff、测试和官方 host 证据；无法钉死就终态
`STOP_UNVERIFIED_DOWNLOAD_HOST`，不要消耗 quota。

## 11. 跨所来源结论

Registry 已扩展：

- Bybit：长历史丢 no-fill，停止作为 denominator；
- Gate：zero-fill canceled 约 10 分钟后不可查，停止；
- Binance async：一次性候选，当前实现未获准；
- MEXC canceled/invalid：无已确认私有账户活动/安全客户端，默认 `ACCOUNT_HISTORY_UNAVAILABLE`；
- `fills-archive`：2024-09-19 已下线；
- `bills-history-archive`：只补账本/费用，不能补 attempts。

没有 VERIFIED 的现成长历史能解除 micro incomplete-attempt denominator。

## 12. 本地历史/WAL 结论

只读审计确认：

- `fast_maker_attempts.jsonl` 29,849 行仅为 maker 后验摘要；
- 缺 `decision_id/clOrdId/ordId/tradeId`；
- `fast_exchange_fills_cache` 只有成交分子；
- slow `open_decision_events` 与 fast 管线平行；
- production `execution.wal` / `order_lifecycle.jsonl` 均 ABSENT；
- 代码有 `internal/execution/journal.go` schema，但 production 未接线。

最早 blocker：

`SHARED_ORDER_LIFECYCLE_ID_ABSENT`

Forward WAL/PIT 只属于 `RUNTIME_READINESS_ONLY`：

`historical_certification_credit=false`

不得等待未来 WAL 来认证当前路线。

## 13. 数据主线完成条件

还未全部完成。

已完成：

- 永久宗旨；
- route data contracts；
- GET-only downloader；
- OKX P0 private 数据；
- current instruments；
- source registry；
- public discovery smoke；
- Data Fitness v3 + independent PASS；
- 全仓 `go test ./... -count=1`（在后续 Report/Binance 最终改动前通过）。

未完成：

1. Report inspector 显式最终 PASS；
2. Binance pilot 修复/复验或正式 STOP；
3. `firstbattle-route-measurability-closure-v1`；
4. `firstbattle-data-mainline-closure-v1`；
5. 最终代码冻结后的全量 tests/race/vet/hash；
6. plan/todo 状态关闭；
7. `firstbattle-mechanism-search-v2` 启动。

## 14. 下一 Agent 推荐第一步

严格按顺序：

1. `git status --short --branch`
2. 不碰 `records/`，不回退任何现有修改。
3. 检查 `internal/historydownload/binancereadonly/` 未完成 diff。
4. 运行：

```bash
go test ./internal/historydownload/reportcenter ./cmd/okx-report-center-inspect -count=1
go test -race ./internal/historydownload/reportcenter ./cmd/okx-report-center-inspect -count=1
go vet ./internal/historydownload/reportcenter ./cmd/okx-report-center-inspect

go test ./internal/historydownload/binancereadonly ./cmd/binance-order-history-pilot -count=1
go test -race ./internal/historydownload/binancereadonly ./cmd/binance-order-history-pilot -count=1
go vet ./internal/historydownload/binancereadonly ./cmd/binance-order-history-pilot
```

5. Report inspector 取得显式 PASS。
6. Binance 无法钉死官方下载 host 则 STOP；不得联网试错。
7. 冻结 route measurability closure 与 data-mainline closure。
8. 代码冻结后跑：

```bash
GOMAXPROCS=2 go test ./... -count=1
GOMAXPROCS=2 go test -race ./internal/historydownload/... ./cmd/okx-history-download ./cmd/okx-report-center-inspect ./cmd/p0-swap-data-fitness ./cmd/binance-order-history-pilot -count=1
GOMAXPROCS=2 go vet ./...
git diff --check
```

9. 数据主线关闭后，启动新 generation：

`firstbattle-mechanism-search-v2`

该 generation 必须先过：

- mechanism payer；
- existing-history observability；
- complete denominator；
- PIT/identity；
- 50 events / 20 days / 10 blocks；

之后才允许读任何历史标签。

## 15. Git / 工作树边界

交接时：

```text
HEAD == origin/main
ab2780405f0fd5c80d268fe0feff39eb14a29eda
```

工作树大量 untracked/modified 尚未提交。

必须隔离：

- 所有 `records/` 修改均来自运行服务或既有研究资产；
- `records/okx_directional/direction_factor_shadow.jsonl` 为新 runtime 文件；
- 不得 `git add records/`；
- 不得删除、reset、checkout 或回退用户/其他 Agent 修改；
- 不得 commit，除非用户明确要求。

主要代码/产物变更：

- `.cursor/rules/lingxi-research-discipline.mdc`
- `FIRST_BATTLE_MASTER_PLAN.md`
- `internal/historydownload/**`
- `cmd/okx-history-download/**`
- `cmd/okx-report-center-inspect/**`
- `cmd/p0-swap-data-fitness/**`
- `cmd/binance-order-history-pilot/**`
- `research/firstbattle/route_data_contracts.*`
- `research/firstbattle/route_data_source_registry.*`
- `research/firstbattle/p0_swap_data_fitness*`
- `research/firstbattle/okx_report_center_manual_export_checklist.md`
- G10 evidence audit / reopen inventory / route portfolio 未提交产物。

## 16. 验证状态

已通过：

- downloader target/race/vet；
- public archive target/race/vet；
- Data Fitness v3 target/race/vet；
- Report inspector 最新 target/race/vet；
- `git diff --check`；
- 全仓 `go test ./...` 在 Binance/Report 最后改动前通过。

未验证：

- Binance owner 被停止时的最后未完成改动；
- 最终全量 race/vet；
- 最终全仓 test（需在所有代码冻结后重跑）。

## 17. 安全边界

继续保持：

```text
global_halt=true
system_status=NO_GO
exam_status=EXAM_UNAVAILABLE
exam_actions=0
trade_allowed=false
routes_reopened=0
labels_unblinded=false
```

禁止：

- 下单、撤单、划转、账户设置；
- 收益研究、validation/WF、考试、shadow、真钱；
- 用当前 snapshot 回填历史；
- 把 future WAL 算作历史认证；
- 把 100% join 算作完整 denominator；
- 把 public tape 算作 private attempts；
- 下载 `allswap` 冒充 1d1s；
- 在 Binance connected path 未 PASS 前消耗 quota。

