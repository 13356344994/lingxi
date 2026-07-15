# Data-Driven Router V3 g3 Capital Extraction Overlay

> Document hash: `4ffab243d5f894c0efdaa3f7f904222652c934e8e29096be7dabfca35d61124b`
> Overlay hash: `63f4ee088efb09e9e58386ab273b997af4a9206d4c5c18e73c59fda5d42a7a57`
> Base protocol hash: `5d80166eb28307d23e33cd19efae2b60811852ff0da49a3a6e16663aba2e7ddd`
> g2 overlay hash: `7096395f1f3bae212b6dc866c5d46ccb372cd284e4a959d424dfbfbb92d6c226`
> Generation: `firstbattle-data-driven-router-v3-g3`
> Status: `PREREGISTERED_LABELS_SEALED_PENDING_INDEPENDENT_REVIEW`

g3 supersedes g2 before labels or dataset construction. It preserves g1/g2 evidence and execution definitions but replaces the risk-utility objective with the user's actual capital extraction policy.

## Capital extraction

For each 30/100/1000U capital tier, initial equity is the fixed retained-capital base `B`.

```text
while equity >= 1.5 * B:
    withdraw 0.5 * B
```

- Losses never reset B downward.
- There are no automatic top-ups.
- Liquidation or equity <= 0 sets terminal equity to zero and permanently stops that replay.
- Withdrawals completed before ruin remain external wealth.

## Primary objective

```text
cashflow_adjusted_total_wealth
  = cumulative_withdrawals + terminal_equity - initial_external_deposit
```

The candidate must exceed `ALWAYS_FLAT` on validation, leaf-WF and the one-shot router holdout. Report cumulative withdrawals, terminal equity, time to first withdrawal, withdrawal count, max drawdown, historical ruin, no-trade fraction and capital occupation.

## Stress is disclosure, not an automatic veto

The frozen liquidity-void, adverse-gap, cost-stress and all-correlation=1 scenarios remain mandatory. They publish withdrawal-adjusted wealth, ruin, prior withdrawals retained, terminal equity, drawdown and liquidation time side by side.

A synthetic stress failure does not automatically reject a path that grows wealth on untouched chronological history. A real liquidation inside validation/WF/holdout is charged in full: equity becomes zero and trading stops, while prior withdrawals remain.

Historical ruin can pass only when cumulative withdrawals plus terminal equity still exceed the initial deposit and every statistical gate passes. It must be labeled `HISTORICAL_RUIN_WITH_WITHDRAWAL_RECOVERY`.

## Leverage and execution

- Account gross leverage remains hard-capped at 5.
- Models may choose any lower leverage.
- The baseline historical allocator is frozen before replay and is not retroactively resized after seeing stress.
- Unknown execution cost still means `NO_TRADE`.
- Direct execution-cost coverage below 80% changes certification to `VALIDATED_MARKET_PATH_EXECUTION_EVIDENCE_LIMITED`; it is not an automatic profitability veto.

## Reviewer boundary

The independent reviewer checks calculations, hashes, leakage, statistics and disclosure. It cannot replace the user's risk preference with a composite safety score and cannot approve live or real-money trading.

## Safety

```text
labels_unblinded=false
dataset_built=false
routes_started=0
system_status=NO_GO
trade_allowed=false
```

## Hash

JSON: set `hashes.overlay_hash` to `null`, then run repository `datatruth.HashJSON`.

Markdown: replace the single `Document hash` value with `SELF_EXCLUDED`, then SHA-256 the UTF-8 LF bytes.
