# Data-Driven Router V3 g2 Safety and Evidence Overlay

> Document hash: `ad51f16f891fe4abc5293ce62403330ab6d7fa6e60bf9375cb0e86001f706c62`
> Overlay hash: `7096395f1f3bae212b6dc866c5d46ccb372cd284e4a959d424dfbfbb92d6c226`
> Base protocol hash: `5d80166eb28307d23e33cd19efae2b60811852ff0da49a3a6e16663aba2e7ddd`
> Generation: `firstbattle-data-driven-router-v3-g2`
> Status: `PREREGISTERED_LABELS_SEALED_PENDING_INDEPENDENT_REVIEW`

g2 supersedes g1 before any label was unsealed or dataset built. The effective protocol is the hash-bound combination of g1 and this overlay.

## Cost coverage

- Every traded decision must have `DIRECT` or `FROZEN_CONSERVATIVE_PROXY` cost.
- Formal profile known-cost coverage must be at least 95%; unknown decisions become `NO_TRADE`.
- The frozen proxy is exactly the g1 10bps fee + 2bps spread floor + capacity formula + bar-toxicity formula + signed funding.
- `VALIDATED_PATH` requires at least 80% direct execution-cost coverage. Below that, the highest possible status is `RESEARCH_PATH_EXECUTION_UNPROVEN`.

## Owner isolation

Model owners receive only schema, synthetic fixtures, API, budgets and hashes. They submit and freeze model code/search grids before real reward labels are generated. A neutral deterministic orchestrator runs all four frozen implementations. Peer outputs publish atomically only after every run finishes or fails. Any owner-run real-label attempt or peer-output read invalidates the tournament.

## Liquidity void

Validation, leaf-WF and router holdout must include:

- no executable close for 1, 2 and 3 bars;
- continued MTM, signed funding, maintenance margin and liquidation-distance accounting;
- historical worst observed adverse gap block and adverse bar path;
- conservative exit at the first eligible bar (`min(open,low)` for long, `max(open,high)` for short);
- zero/missing-volume bars remain unexecutable.

No hand-entered 10% or 20% slippage assumption is used. Any solvency or liquidation breach fails the path. Missing depth/fill evidence caps status at `RESEARCH_PATH_EXECUTION_UNPROVEN`.

## Extreme sizing and account resonance

- Five times leverage is an absolute account ceiling, never a target.
- Size is the minimum of model size, liquidity capacity, min-lot feasible size and solvency size under every frozen stress.
- Upward rounding to satisfy min lot is forbidden.
- All same-timestamp proposals are collected before execution.
- Total account gross leverage remains at most 5 after the batch.
- Same-direction and BTC-beta exposure are aggregated.
- Crisis stress sets all pairwise correlations to 1 and merges all positions into one cluster.
- If the stressed batch is insolvent, shrink all proposals proportionally and recheck; otherwise `NO_TRADE`.
- `CLOSE/TRIM` reduce-only actions execute before any new risk action, followed by a fresh account check.

## Reviewer authority

Sonnet is a mechanical evidence reviewer for protocol drift, code/tests, hashes, PIT/leakage, statistics and reproducibility. It cannot predict structural breaks, declare future safety, or approve exam/shadow/real money. Historical success never auto-promotes live operation.

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
