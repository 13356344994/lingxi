# Data-Driven Router V3 g5 Deterministic Mechanics Overlay

> Document hash: `e211ec431d8aa9d6ceaeb8e9907604041d5fb99121b447176067227621717b5e`
> Overlay hash: `518d0003c829da641f1bcc955c4382023e158ba9938c0ac6f0ff19b5bf729385`
> g1 hash: `5d80166eb28307d23e33cd19efae2b60811852ff0da49a3a6e16663aba2e7ddd`
> g2 hash: `7096395f1f3bae212b6dc866c5d46ccb372cd284e4a959d424dfbfbb92d6c226`
> g3 hash: `63f4ee088efb09e9e58386ab273b997af4a9206d4c5c18e73c59fda5d42a7a57`
> g4 hash: `2fd8c5852bba5023c9024d46b5b5e9527e71d670c370a1276e4728f4ef4cd2b9`
> Generation: `firstbattle-data-driven-router-v3-g5`
> Status: `PREREGISTERED_LABELS_SEALED_PENDING_INDEPENDENT_REVIEW`

## Deterministic withdrawal reserve

All money uses micro-USDT integer precision.

```text
N = sum(abs(fixed position notional))
leverage_reserve = N / 5
maintenance_reserve =
    sum(position_notional * maintenance_margin_rate)
  + frozen_close_cost
required_reserve = max(leverage_reserve, maintenance_reserve)
free_equity = max(0, marked_equity - required_reserve)
withdrawal = floor_micro(min(0.5 * retained_base, free_equity))
```

If maintenance-margin data are unknown while a position is open, withdrawal is zero. After withdrawal, leverage, maintenance margin, liquidation distance and cash are recomputed. Any failure blocks the withdrawal.

## Synthetic stress does not resize the baseline

g5 explicitly overrides g2's synthetic-stress sizing/veto fields. No-exit scenarios, worst-path reapplication and all-correlation=1 remain mandatory disclosures, but they do not resize or veto the untouched chronological baseline policy.

Hard gates remain:

- actual chronological historical equity and liquidation;
- account gross leverage ≤5;
- known execution cost or frozen proxy;
- actual-time maintenance margin;
- no negative cash;
- PIT and time isolation.

Delay 1/2/3 and 1.5× cost stress create sensitivity labels rather than automatic rejection.

## Baseline identity

Every baseline and candidate is executed by the same neutral runner with identical:

- state rows and decision timestamps;
- partitions;
- reward evaluator and cost evidence;
- delay/funding/min-lot/maintenance-margin logic;
- account allocator;
- withdrawal, no-top-up and ruin rules;
- execution order.

The only allowed difference is the action policy. `ALWAYS_FLAT` is the hard baseline; all other baselines are comparative disclosures.

## Safety

```text
labels_unblinded=false
dataset_built=false
routes_started=0
NO_GO
```

## Hash

JSON: set `hashes.overlay_hash` to `null`, then run repository `datatruth.HashJSON`.

Markdown: replace the single `Document hash` value with `SELF_EXCLUDED`, then SHA-256 the UTF-8 LF bytes.
