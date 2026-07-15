# 旧数据主线最小关闭（firstbattle-data-mainline-closure-v1）

> Artifact: `firstbattle.data-mainline-closure`（generation `firstbattle-data-mainline-closure-v1`）
> Frozen at: `2026-07-14T03:30:00Z`，repo HEAD `ab2780405f0fd5c80d268fe0feff39eb14a29eda`
> Status: `OLD_DATA_MAINLINE_CLOSED_MINIMAL_HANDOFF_TO_MECHANISM_SEARCH_V2`
> Artifact hash: `d4f78f1efabead5b86e0656ab73903734da6c36d1d737337c535a14e6568525e`
> Document hash: `48da099f841ca1814f1eaf656e9b64e38c865c4e5e536c966f38601d04085a06`

## 1. 本工件目的

机制搜索V2第 1 项收尾。停止把旧数据工程本身当主线，只做一次**最小可信关闭**：把旧 OKX 私有 orders/fills/current-instruments 的 Data Fitness 数据主线当前 generation 关闭，绑定权威独立证据，然后把主线交给 `firstbattle-mechanism-search-v2`。本工件不重开任何路线、不解盲标签、不读取或汇报收益字段。

关闭的是**当前数据主线 generation 的可检验表述**，不是四条机制路线家族本身。Data 工程关闭 **不等于** 路线/机制失败，不得写成 `FALSIFIED`。

## 2. 权威新证据（独立 reviewer receipt）

- 工件：`research/firstbattle/p0_swap_data_fitness_reviewer_receipt.json`（及 `.md`）
- receipt hash：`0cdd322c130a7c6e58bd9803e5611f6a144d35bf6e5a3f0cdac49292ad9dcad4`
- receipt md document hash：`567cbaea088027a4a521e6ecfa11fdd28fb1b3ac149896e77f13042d031e7bbc`
- reviewer：独立 reviewer 会话（Claude Sonnet 5），与撰写被审工件的 owner agent 谱系不同
- reviewed HEAD：`ab2780405f0fd5c80d268fe0feff39eb14a29eda`
- verdict：`PASS_INDEPENDENT_VERIFICATION_DATA_FITNESS_CLASSIFICATION`
- 批准范围：**仅**当前数据维持 `DATA_BLIND` / `routes_reopened=0`，无更广主张
- 独立方法：M1 CLI 重算、M2 clean-room Python 重实现、M3 自排除 hash 重算、M4 manifest 时钟/分母审计、M5 plan drift 检查；带自排除 receipt hash，不依赖 owner 叙述。

**不批准**：收益/盈利研究、validation/walk-forward、考试签发或动作、shadow、真钱或任何路线重开；不批准把 100% order/fill join 当作完整 attempts 分母；不批准把 current instruments 快照当作历史 PIT。

## 3. 统一矛盾状态：owner 自述降级为非证据

- 位置：`p0_swap_data_fitness.json` 的 `independent_redteam` 块与 `p0_swap_data_fitness.md` 第 8 节。
- 旧角色：被当作独立红队 PASS 的证据。
- 新角色：`NON_EVIDENCE_OWNER_NARRATIVE_RETAINED`。
- 原因：该块是由撰写其余工件的同一 owner 侧 agent 谱系、在只有 owner 可控的文件内写下的自证，本身不是独立可验证证据（对应 reviewer receipt finding **F4**，MEDIUM，process）。
- 处置：**不修改** `p0_swap_data_fitness.json/.md`，owner 自述原文保留；仅在 `FIRST_BATTLE_MASTER_PLAN.md` 与本 closure 中把它降级为非证据，并改引独立 reviewer receipt 作为证据权重来源。

这样解决了 Master `§5.11 (待独立红队)` / `GENERATOR_REBUILT_PENDING_INDEPENDENT_REDTEAM` 与工件自身 `REDTEAM_PASS…STOPPED` / 嵌入 `PASS` 的矛盾。

## 4. 冻结旧数据主线状态

保留并冻结（不继续工程化扩展）：

```text
GET-only downloader:                RETAINED_FROZEN
OKX P0 private orders/fills:         RETAINED_FROZEN
current instruments:                RETAINED_FROZEN_SNAPSHOT_ONLY (PIT_UNPROVEN)
route data source registry:         RETAINED_FROZEN
Data Fitness measurement (v3):      RETAINED_FROZEN
```

标为 `NOT_REQUIRED_UNLESS_SHORTLISTED_ROUTE_DEMANDS_IT`：

```text
OKX Report Center 人工导出:          NOT_REQUIRED_UNLESS_SHORTLISTED_ROUTE_DEMANDS_IT
Binance async order-history pilot:   NOT_REQUIRED_UNLESS_SHORTLISTED_ROUTE_DEMANDS_IT
  connected path:                    STOP_UNVERIFIED_DOWNLOAD_HOST（禁止 --connect / 禁止消耗 quota）
public L2 / depth:                   NOT_REQUIRED_UNLESS_SHORTLISTED_ROUTE_DEMANDS_IT
```

工程指令：`NO_FURTHER_OLD_MAINLINE_ENGINEERING_TO_COMPLETE_LEGACY_TODOS`。禁止为“完成旧 todo”继续工程化旧数据主线；上述 `NOT_REQUIRED` 项只在机制搜索V2某条**入选路线**的冻结数据需求明确触发时，才作为该路线的最小数据获取重新单独立项并先过红队。

## 5. 最早 blocker 与关闭语义

正式 earliest blocker：`INCOMPLETE_ATTEMPTS_DENOMINATOR`（层：`micro_execution_capacity_cliff`）。

- 全部 13,622 个下载订单 `accFillSz>0`；56 个 canceled 均为部分成交撤单；no-fill 订单 = 0；端点不提供 rejected/expired/live-open/decision 侧非提交 arm。
- secondary：`CROSS_ENDPOINT_INSTRUMENT_IDENTITY_DRIFT`、`ORIGINAL_RECEIVE_TIME_ABSENT`、`DECISION_ID_ABSENT`、`INSTRUMENT_PIT_UNPROVEN`。

解释边界：

- 100% order/fill token join 只证明 survivor-biased API slice 的行级引用完整性，**不等于**完整 attempts 分母。
- 同一 order token 在两端出现 168 订单 / 199 fill rows 的 instId 冲突（orders `DATA-USDT-SWAP-OFF20260702-2` → fills `IP-USDT-SWAP`），exchange identity 跨端点不稳定。
- current instrument 元数据可能只从 KnownFrom 向未来生效，不能回填历史 attempts。
- 26 个 UTC 日中 top-2 日占 `81.74276905006607%`，支持度高度集中。

**Data 工程关闭 ≠ 路线/机制失败。** 四路径保持 `DATA_BLIND`，不写 `FALSIFIED`，不等待未来数据续命。

## 6. 验证状态（本轮，不谎称最终全仓 race）

- 本轮主代理已运行 target 测试 / race / vet / `git diff --check`，命令已 **exit 0**。
- 未声称本轮已重跑最终全仓 race；全仓 `go test ./...` 曾在 Report/Binance 最后改动前通过，最终全量 test/race/vet 仍待所有代码冻结后重跑。
- 本 closure 工件仅新增 JSON/MD 并更新 Master，用仓库 `datatruth.HashJSON` 计算自排除 hash；未修改代码、plan 文件、候选机制文件、`records/`、private raw 或 G1–G10 冻结工件；未提交。无必要不跑额外命令。

## 7. 下一主线

```text
generation:        firstbattle-mechanism-search-v2
labels_unblinded:  false
routes_started:    0
routes_reopened:   0
system_status:     NO_GO
```

由 Opus、Fable、Grok 并行提出至少十五个非同源付款机制候选，先过 payer / existing-history observability / complete denominator / PIT-identity / `50 events · 20 days · 10 blocks`，之后才允许读任何历史标签。未经用户再次批准，不运行收益研究、validation/WF、考试、shadow 或真钱。

## 8. 绑定 hash

```text
repo HEAD:                          ab2780405f0fd5c80d268fe0feff39eb14a29eda
route data contract protocol:       4a4c201d2c8cedde5bd1ff55ea40ad31d099e189feb709a517701b504c453588
route data contract document:       68dc6666fc0138850fff2911965e5ef05505797ac9fc6137ddde928744e74639
source registry JSON:               1c6672563d4bc00ea102df2f9575b365a2a9dc30e763db75067f9a49052e5318
source registry document:           483ee318d954cf939646f0a9305e155ab908a3918a75afdfd6fb5f7bb149a0cf
p0 data fitness JSON:               393a12952b480fc2671774d76790e5f850bcbc26b43c8eb8d870bba9c9e2bd66
p0 data fitness MD:                 697cf3686f72748459d89943c1f45f0071e670f5d22a36f23f61d0154707e899
p0 measurement receipt:             f23a815fba52dab22368428a6a95250a1ad58e429c09613fea0ac977b1030c92
orders projection aggregate:        e2cad38f667a0895fcc7ad0d4961b5bf5b3d8d67d3dd3c5e931618cee877b6b3
fills projection aggregate:         36d3fd5e3e0fe267af94122e590da077fb49fa3b290d90268b488d9dc8fa2b2a
independent reviewer receipt JSON:  0cdd322c130a7c6e58bd9803e5611f6a144d35bf6e5a3f0cdac49292ad9dcad4
independent reviewer receipt MD:    567cbaea088027a4a521e6ecfa11fdd28fb1b3ac149896e77f13042d031e7bbc
closure JSON artifact hash:         d4f78f1efabead5b86e0656ab73903734da6c36d1d737337c535a14e6568525e
```

## 9. 文档 hash 复算

把本文件唯一一行 `> Document hash: \`<value>\`` 规范化为 `> Document hash: \`SELF_EXCLUDED\``，再对 UTF-8 LF 字节取 SHA-256。JSON 自排除 hash 复算见 `data_mainline_closure_v1.json.hashes.hash_method`。
