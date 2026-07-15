# Data-Driven Router V3 Protocol

> Document hash: `b76f341e0694f27513b602203c8bde2d184bcd10260800b95a988f75ad04132a`
> Protocol hash: `5d80166eb28307d23e33cd19efae2b60811852ff0da49a3a6e16663aba2e7ddd`
> Generation: `firstbattle-data-driven-router-v3-g1`
> Status: `PREREGISTERED_LABELS_SEALED`

## Objective

> 在不同市场状态、特殊事件和个人执行条件下，动态路由到各自经过历史证据认证的专属盈利路径；没有可靠匹配路径时必须空仓。最终以资金流调整后的真实账户净值长期增长为唯一成功标准。

V3 不以付款者、机构排斥、事件稀疏、容量、速度或护城河作为准入先验。旧 G1–G10 与 mechanism-search-v2 的候选、赢家、阈值和 route 不进入 V3 模型输入。

## Formal profile and partitions

- 正式 profile 仅 `okx_1h`（401 symbols，约 2023-12-31 至 2026-07-09）。
- `okx_5m` 与 `rubik_taker_5m` 因历史过短，只允许 smoke，不参与正式选择。
- 所有模型共享一条全局日历切分：discovery 50%、validation 20%、leaf-WF 15%、router-holdout 15%。
- 全局 purge/embargo 24h；禁止 per-symbol 独立切分。
- leaf-WF 每 owner 一次性提交；router-holdout 只允许一次冻结组合回放。

## State, action, and reward

- State 只含 DecisionTime 可见的价格、波动、量、BTC context、横截面 rank、funding/Rubik 可选字段和 missing mask。
- 禁止 symbol ID、绝对日期、旧 route/candidate/threshold、未来值、PnL 和当前未闭合 bar。
- Actions: `NO_TRADE/LONG/SHORT/HOLD/ADD/TRIM/CLOSE/REVERSE`；delay 0–3；horizon 1h/4h/24h；30/100/1000U；最大杠杆 5。
- base funding 使用带符号现金流（正 funding 时 long 付、short 收）；stress 使用不利方向绝对 funding。
- 统一计入 10bps roundtrip fee、2bps spread floor、容量冲击、bar toxicity、delay、MTM leverage、min lot、pending 和 EOF。
- isolated reward 只用于 discovery；任何认证必须通过 chronological full-account replay。

## Model tournament

- Terra: bounded-depth trees。
- Opus: sparse mixture-of-experts。
- Fable: episodic nearest-neighbor。
- Grok: change-point / rare-state segmentation。
- 每 owner 最多 16 attempts、32 leaves、1 CPU、12h；总计最多 64 attempts、128 leaves。
- 四个 owner 使用同一 dataset/partition/cost hash，提交前互不读取结果；ledger 外 run 无效。

## Statistical gates

- validation 在四模型候选并集上做 BH-FDR `q=0.10`。
- sealed leaf-WF 对四个 attempt ledger 与 selected policies 的并集做 Holm family alpha `0.01`。
- 最少 50 events、20 active days、10 non-overlap BTC-regime blocks。
- 10 个 symbol-hash folds 至少 8 个净正；leave-one-month 至少 80% 净正；至少 8 个 regime blocks 净正。
- top-2-day 和 single-symbol 正贡献均不得超过 35%。
- delay 0–3 全部净正，delay+1 失效即 FAIL；1.5× cost stress 后仍须净正。
- 基线包括 ALWAYS_FLAT、同频固定 LONG/SHORT、buy-hold、同频随机和 BTC-beta matched。

## Router holdout

所有独立通过的 leaves 在进入 router-holdout 前冻结。反向冲突且无 validation-only priority 时 `NO_TRADE`；不得使用 holdout 调 priority、Top-K 或组合。唯一主指标为资金流调整后的账户最终净值。

## Valid outcomes

`VALIDATED_PATH / POSTHOC_ONLY / UNSTABLE / UNEXECUTABLE / DATA_BLIND / NO_EDGE / DEGENERATE / NO_VALIDATED_PATH`

`NO_VALIDATED_PATH` 是合格结局，不得为产出策略而移动门槛。

## Safety

```text
global_halt=true
system_status=NO_GO
exam_status=EXAM_UNAVAILABLE
exam_actions=0
trade_allowed=false
labels_unblinded=false
```

## Hash

JSON: set `hashes.protocol_hash` to `null`, then run repository `datatruth.HashJSON`.

Markdown: replace the single `Document hash` value with `SELF_EXCLUDED`, then SHA-256 the UTF-8 LF bytes.
