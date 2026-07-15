# Data-Driven Router V3 g4 Withdrawal Mechanics Overlay

> Document hash: `6dd37c27f4d6b373e560af375f6ba47788dfcca567176d11d0a77c7929898c4b`
> Overlay hash: `2fd8c5852bba5023c9024d46b5b5e9527e71d670c370a1276e4728f4ef4cd2b9`
> g1 hash: `5d80166eb28307d23e33cd19efae2b60811852ff0da49a3a6e16663aba2e7ddd`
> g2 hash: `7096395f1f3bae212b6dc866c5d46ccb372cd284e4a959d424dfbfbb92d6c226`
> g3 hash: `63f4ee088efb09e9e58386ab273b997af4a9206d4c5c18e73c59fda5d42a7a57`
> Generation: `firstbattle-data-driven-router-v3-g4`
> Status: `PREREGISTERED_LABELS_SEALED_PENDING_INDEPENDENT_REVIEW`

g4 supersedes g3 before label unseal and closes withdrawal/position-margin mechanics while preserving the user's absolute-wealth risk preference.

## Withdrawal execution

Withdrawal is evaluated only at bar-close DecisionTime, after MTM and before new risk proposals. Intrabar equity spikes cannot trigger withdrawal.

```text
trigger: equity >= 1.5 * retained_base
requested: 0.5 * retained_base
free_equity: equity - maintenance_margin_required - frozen_liquidation_buffer
actual: min(requested, max(free_equity, 0))
```

After every withdrawal, recompute equity, effective leverage, maintenance margin, liquidation distance and solvency. If the withdrawal would violate 5× or solvency, roll it back to the maximum safe amount or zero. Repeat only while the trigger and safe free equity remain.

## Normal history versus stress

The untouched chronological historical account path with frozen observed cost and delay is primary. It must produce positive cashflow-adjusted wealth and beat `ALWAYS_FLAT`.

Delay+1, delays 2/3, 1.5× cost stress, no-exit 1/2/3 bars, worst-gap reapplication and all-correlation=1 remain mandatory disclosures. Failure adds explicit `DELAY_SENSITIVE`, `HIGH_DELAY_SENSITIVE`, `COST_SENSITIVE` or tail-risk labels; it does not automatically erase positive baseline wealth.

Actual adverse events occurring at their true chronological location remain fully charged. Historical liquidation sets equity to zero and stops the replay; prior withdrawals remain.

## Baselines

`ALWAYS_FLAT` is the only hard baseline because the user's objective is absolute wealth growth. Fixed long/short, buy-hold, random same-frequency and BTC-beta-matched baselines use the same withdrawal/no-top-up rules and must be reported, but relative underperformance is not an automatic rejection.

## Safety

```text
account_gross_leverage<=5
unknown_cost=NO_TRADE
labels_unblinded=false
dataset_built=false
routes_started=0
NO_GO
```

## Hash

JSON: set `hashes.overlay_hash` to `null`, then run repository `datatruth.HashJSON`.

Markdown: replace the single `Document hash` value with `SELF_EXCLUDED`, then SHA-256 the UTF-8 LF bytes.
