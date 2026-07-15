# Data-Driven Router V3 g8 Discovery Target Generation

> Document hash: `106d5c39bb44a252ef4f2bba6664291f02bbcc60803dee52bac8998cee538f78`
> Overlay hash: `c492cd191e869073b330221835fcec4b4437b27ec67e6e9d18252119fc7da682`
> g7 dataset manifest: `f9dc0944919709a49f765fe3291cf60a46952919f796add47d892af5f2808a6d`
> Generation: `firstbattle-data-driven-router-v3-g8`
> Status: `PREREGISTERED_DISCOVERY_LABEL_GENERATION_PENDING_REVIEW`

All four model implementations and search grids were submitted on synthetic fixtures before this protocol. g8 freezes how the neutral runner may create discovery targets.

## Discovery sample

- Partition: discovery only.
- Maximum 100,000 examples.
- Select the lowest SHA-256 of `event_id + canonical_context_id + seed42` before reward evaluation.
- Validation, leaf-WF and router-holdout labels remain sealed.

## Canonical account context

Each selected event receives exactly one deterministic context:

- FLAT; or
- LONG/SHORT with age 1/4/24 and fraction .1/.25/.5/1.

Entry uses the completed-bar open at `decision-age`; unrealized return uses the DecisionTime close; funding sums actual settlements through DecisionTime. These hypothetical contexts are discovery labels only. Certification uses real chronological AccountContext.

If the required entry bar is unavailable, the event-context pair is deterministically ineligible before reward evaluation and excluded from the hash-priority sample.

## Target grid

```text
signed fraction: -1,-.5,-.25,-.1,0,.1,.25,.5,1
horizon: 1,4,24 hours
delay: 0,1,2,3 bars
108 targets per example
```

The neutral evaluator scores local cashflow-adjusted wealth with frozen costs and signed funding. Unknown-cost targets are ineligible; ruin is negative infinity. Ties prefer lower turnover, lower exposure, shorter horizon, lower delay, zero target, and finally the lower numeric signed fraction. This is a total order.

Models receive only the selected `TargetIntent`, never the full reward vector.

For `kind=DISCOVERY`, the existing legacy-named `modelapi.TrainingExample.synthetic_target` field transports only that selected discovery `TargetIntent`. For `kind=SYNTHETIC`, it continues to transport fixture targets. No reward value or full target-reward vector enters model code.

## Isolation

- Owners cannot read the discovery label bundle.
- The neutral orchestrator fits all four models on the same examples.
- Peer outputs publish atomically only after all runs finish or fail.
- Validation/WF/holdout labels stay sealed.

## Safety

```text
validation_labels_unblinded=false
leaf_wf_labels_unblinded=false
router_holdout_labels_unblinded=false
routes_started=0
NO_GO
```

## Hash

JSON: set `hashes.overlay_hash` to `null`, then run repository `datatruth.HashJSON`.

Markdown: replace the single `Document hash` value with `SELF_EXCLUDED`, then SHA-256 the UTF-8 LF bytes.
