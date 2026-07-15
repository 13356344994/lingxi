# LINGXI Data-Driven Router V3 Verdict

> Document hash: `PENDING`
> Verdict hash: `d2822528914fdc71559683724a3a852a414334a2ab63698a3c4dba8524d8c86b`
> Status: `NO_CERTIFIED_ROUTE_CURRENT_GENERATION`
> Router holdout unblinded: `false`
> Trade allowed: `false`

## What this verdict means

No route produced by this V3 generation is authorized to enter router holdout or trading.

It does **not** mean that LINGXI's supreme objective or the existence of a proprietary profitable route has been disproved.

The supreme objective remains:

> Dynamically route each market state, special event and personal execution condition to a historically certified proprietary profitable path; remain flat when no reliable path matches; judge success only by long-run growth of cashflow-adjusted real account equity.

## Evidence outcome

- 100,000 discovery examples were generated and four owner policies were frozen atomically.
- Independent validation evaluated 52 candidate paths.
- Terra was fully replayable; Grok was partly replayable; Opus and Sonnet lacked replay-complete fitted artifacts.
- No path completed leaf-WF account certification.

The strongest result was Terra `leaf-05`:

- 38,307 validation examples;
- all 9 frozen robustness gates passed;
- base reward sum `+146,444.83`;
- 1.5× cost-stress reward sum `+81,890.28`;
- fraction `0.25` was the largest account-safe and profitable validation size.

In leaf-WF, the same route repeatedly accumulated eight correlated long positions, totaling approximately `2×` retained capital. Broad drawdown reduced equity to roughly `0.39×` retained capital, breaching the hard `5×` leverage ceiling in every delay/cost scenario.

This was a genuine account-level failure, not inactivity and not a drawdown-only rejection.

## What the data revealed

The current generation learned single-instrument state-direction edge, but discovery reward did not learn cross-instrument concurrency and correlation. Those account effects were applied only after path discovery.

The next generation therefore must:

1. include concurrent positions, correlation, gross-notional occupation, pending actions and withdrawal-adjusted equity inside discovery-time reward;
2. learn account-aware admission and sizing rather than adding a subjective tighter pre-filter;
3. require replay-complete fitted artifacts from every model;
4. keep ruin as a hard failure, drawdown as soft evidence, and missed certified opportunities as an explicit failure.

## Invalidated intermediate runs

- `run-20260715-gpt56terra-final`: future-MTM and execution-price defects.
- `run-20260715-gpt56sol-corrected-v2`: overstrict profitability gate.
- `run-20260715-gpt56sol-g1-selection-v3`: missing eligibility gate.

Only `run-20260715-gpt56sol-eligibility-v4` is trusted.

## Safety

```text
router_holdout_unblinded=false
routes_started=0
trade_allowed=false
NO_GO
awaiting_user_approval=true
```

No next-generation work starts without user approval.

## Hash

JSON: set `hashes.verdict_hash` to `null`, then run repository `datatruth.HashJSON`.

Markdown: replace the single `Document hash` value with `SELF_EXCLUDED`, then SHA-256 the UTF-8 LF bytes.
