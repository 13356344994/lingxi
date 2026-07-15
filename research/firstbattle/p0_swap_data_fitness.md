# P0 SWAP Data Fitness（outcome-blind）

> Artifact: `firstbattle.p0-swap-data-fitness`
> Generation: `firstbattle-p0-swap-data-fitness-v1`
> JSON artifact hash: `393a12952b480fc2671774d76790e5f850bcbc26b43c8eb8d870bba9c9e2bd66`
> Measurement receipt hash: `f23a815fba52dab22368428a6a95250a1ad58e429c09613fea0ac977b1030c92`
> Document hash: `697cf3686f72748459d89943c1f45f0071e670f5d22a36f23f61d0154707e899`
> Status: `REDTEAM_PASS_DATA_FITNESS_CLASSIFICATION_STOPPED`

## 1. 范围

本产物只审计已发布的 OKX SWAP private orders/fills 与 current instruments 的 provenance、字段存在性、token identity join、时钟、PIT、分母和路线适配性。

- 未读取 `pnl/fillPnl/outcome` 值；
- 未运行收益、validation、WF、考试、shadow 或真钱；
- `NO_GO / EXAM_UNAVAILABLE / trade_allowed=false` 不变；
- 冻结合同 hash：`4a4c201d…c453588`；
- 来源 registry hash：`1c667256…2e5318`。

## 2. 冻结证据

- orders manifest：`3bd6db35…a55fa`
- fills manifest：`390c1532…c295`
- instruments current：`34cca1c1…099d`
- instruments manifest：`073f3087…0715`
- instruments raw：`e2445309…9267`

全部私人分析只读取 tokenized projection；raw outcome 值未进入本审计。

## 3. 观测事实

```text
orders: 13,622 unique order tokens
  filled:  13,566
  canceled:    56
  canceled with accFillSz > 0: 56
  canceled with accFillSz = 0:  0

fills: 15,676
  unique order tokens: 13,622
  matched to orders:   13,622
  unmatched:                0
  multi-fill orders:    1,410
  max fills/order:          40

coverage:
  distinct UTC days: 26
  top-two-day share: 81.7428%

current instruments:
  rows / ctType / minSz / ctVal: 420 / 420 / 420 / 420
  orders traded instIds: 144
  fills traded instIds:  144
  union traded instIds:  145
  present now:           143
  missing now:           DATA-USDT-SWAP-OFF20260702-2, IP-USDT-SWAP

cross-endpoint identity drift:
  conflicting orders:    168
  conflicting fill rows: 199
  orders instId: DATA-USDT-SWAP-OFF20260702-2
  fills instId:  IP-USDT-SWAP
```

## 4. 关键解释

`13,622 / 13,622` token join 只证明当前已下载切片的行级引用完整性，**不证明完整 attempts 分母**。

所有 13,622 个 order 均有正 `accFillSz`；56 个 canceled order 也都是部分成交后撤。当前切片没有 no-fill 臂，也没有 rejected、expired、历史 live-open、decision-side non-submission。

因此 micro 机制最关键的“完整 attempts 上 fill/no-fill 与规模断层”仍不可观测。

## 5. 时钟与 PIT

- EventTime：orders `cTime/uTime`、fills `fillTime/ts` 可见；
- 原始 ReceiveTime：缺失；manifest 中的 receive range 是本地下载时刻；
- ProcessTime：可见；
- instrument KnownFrom：`2026-07-13T17:29:07.322248434Z`，晚于全部 attempt；
- current snapshot 只能向未来生效，禁止回填历史；
- 已交易退市合约从 current snapshot 消失，证明当前快照不能作为历史权威。

## 6. Data Fitness 裁决建议

- root context/OOD：`GATE_ACTIVE_FAIL_CLOSED`
  - private execution provenance 直接；
  - 原始 ReceiveTime、PIT universe、depth/mids/OI context 缺失。
- micro execution capacity cliff：`DATA_BLIND`
  - token join、partial fill、fee 为 DIRECT/RECONSTRUCTABLE；
  - exchange identity 为 `PARTIAL_UNSTABLE_ACROSS_ENDPOINTS`；
  - earliest blocker：`INCOMPLETE_ATTEMPTS_DENOMINATOR`；
  - secondary：`CROSS_ENDPOINT_INSTRUMENT_IDENTITY_DRIFT / ORIGINAL_RECEIVE_TIME_ABSENT / DECISION_ID_ABSENT / INSTRUMENT_PIT_UNPROVEN`。
- exchange rule discontinuity：`DATA_BLIND/PIT_UNPROVEN`
  - current snapshot 仅 forward-only；
  - 无历史 rule version / effective_at / KnownFrom 链。
- sparse forced flow：`DATA_BLIND`
  - 本来源不是 forced-flow event denominator。
- cross-venue operational asymmetry：`DATA_BLIND/WEAK_PROXY_ONLY`
  - 无同步双场所 executable books、资金授权和共享 event identity。

`routes_reopened=0`。

## 7. 下一最高价值动作

1. 建立 append-only 本地 execution attempt WAL：`decision_id`、submit/reject/no-fill/partial/cancel、ack/cancel/fill 三时钟、uptime/gap manifest。
2. 建立 forward-only instrument PIT ledger：KnownFrom、参数版本、listing/delist/change。
3. 准备 Report Center Order History 人工 CSV 导入与 schema/identity 门；只延长 completed-order 覆盖，不宣称完整 attempts 分母。
4. exact bounded public tape 仅作 decision-time context，不能顶替私人 attempts。

`bills-history-archive` 只能延长账本/费用；`fills-archive` 已下线。两者都不能解除 earliest blocker。

## 8. Reviewer separation / STOP

独立 Sonnet reviewer 曾 `BLOCK` 手工 owner 工件：漏掉跨端点 instId 冲突，并低报 traded symbol union/snapshot missing。当前数字已由 v3 带单测的离线生成器从 137/157 个 tokenized projection pages 重建；projection aggregate hashes 与 measurement receipt 已绑定。最终复验为 `PASS_DATA_FITNESS_CLASSIFICATION`，无 MEDIUM 以上 finding。批准范围仅为当前数据继续维持 `DATA_BLIND / routes_reopened=0`；不批准收益研究、考试、shadow 或真钱。

Generator evidence：

- orders projection：137 pages / 13,622 rows / 7,334,063 bytes，aggregate hash `e2cad38f667a0895fcc7ad0d4961b5bf5b3d8d67d3dd3c5e931618cee877b6b3`；
- fills projection：157 pages / 15,676 rows / 9,829,988 bytes，aggregate hash `36d3fd5e3e0fe267af94122e590da077fb49fa3b290d90268b488d9dc8fa2b2a`；
- measurement receipt：`research/firstbattle/p0_swap_data_fitness_measurement.json`；
- calculator version：`p0-swap-data-fitness.v3`；
- receipt hash：`f23a815fba52dab22368428a6a95250a1ad58e429c09613fea0ac977b1030c92`。

复核前及复核后若无新完整 denominator：

- 不启动 route；
- 不读取收益；
- 不运行 WF/考试；
- 不进入 shadow/真钱；
- 不用未来数据等待补样。

## 9. Hash 复算

- JSON：将 `hashes.artifact_hash` 置为 `null`，使用仓库 `datatruth.HashJSON`。
- Markdown：将唯一一行 `> Document hash: \`<value>\`` 规范化为 `> Document hash: \`SELF_EXCLUDED\``，对 UTF-8 LF 字节做 SHA-256。
