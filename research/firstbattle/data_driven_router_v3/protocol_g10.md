# Data-Driven Router V3 g10 Counterfactual Account Semantics

> Document hash: `c08db9de8768918cc04c72a7ebc5c87f257a82d952520abefab06a6ec02e4653`
> Overlay hash: `0d09d595947f09aeb6258b9af023e5c87e2d334bc52649761f91cb2ae263e7cb`
> g9 reviewer receipt: `e77d1511d022fb511165f3412535ff81a6149cb5a0da7729044c5afb9069c953`
> Generation: `firstbattle-data-driven-router-v3-g10`
> Status: `PREREGISTERED_COUNTERFACTUAL_ACCOUNT_SEMANTICS_PENDING_REVIEW`

g10 makes inherited capital tiers, execution costs and target expiry unique before the first discovery label is created.

## Capital tier

Each event receives one deterministic tier from 30, 100 or 1,000 USDT using `sha256(seed42 + event_id + capital_tier) mod 3`.

The selected tier is both retained capital base and notional-per-full-fraction. Fixed notional is `abs(fraction) × tier`.

## Canonical context

A flat context starts with cash and equity equal to its tier.

A non-flat context starts at the historical completed-bar open. Cash at DecisionTime is:

```text
tier - entry execution cost + actual signed funding
```

Marked equity adds unrealized PnL at the DecisionTime completed-bar close. Missing bars/cost/funding, negative cash, non-positive equity or leverage above 5 make the event-context pair ineligible before target scoring.

## Dynamic cost

The inherited fee, spread floor, capacity impact and bar-toxicity formula is evaluated on each actual execution bar. Base discovery target generation uses stress 1.0.

The computed round-trip cost is split equally:

- entry/add pays one half at its execution bar;
- trim/close pays one half at its execution bar;
- reverse pays exit half on the old leg and entry half on the new leg.

Validation later repeats at stress 1.5.

## Counterfactual timeline

- Delay 0 executes at the DecisionTime completed-bar close.
- Delay 1–3 keeps the existing position exposed and funded until execution.
- Horizon starts when the target executes.
- At `execution + horizon`, the target is forced to zero at that bar's close.
- Funding settlements are consumed once in `(previous_mark, current_mark]`.
- Withdrawals follow inherited g3-g5 ordering.

The local reward is ending cashflow-adjusted total wealth minus starting cashflow-adjusted total wealth. Ruin is negative infinity. The selected target uses the g8 total-order tie-break.

## Scope

This generation defines single-instrument canonical discovery labels. It does not certify a multi-instrument account. Validation and route composition require a later shared portfolio runner.

## Safety

```text
discovery_label_bundle_created=false
validation_labels_unblinded=false
leaf_wf_labels_unblinded=false
router_holdout_labels_unblinded=false
routes_started=0
NO_GO
```

## Hash

JSON: set `hashes.overlay_hash` to `null`, then run repository `datatruth.HashJSON`.

Markdown: replace the single `Document hash` value with `SELF_EXCLUDED`, then SHA-256 the UTF-8 LF bytes.
