# Data-Driven Router V3 g9 Production Scale Freeze

> Document hash: `faa8cf94367b80110e380c9dee0c08ca6dd691a5a4c1fed943ef1774004fe76e`
> Overlay hash: `fe3ce29ef3b8b07da7ef81e0ea3ccc732722f8a184e7ba91ebb8edacd7b2802c`
> Generation: `firstbattle-data-driven-router-v3-g9`
> Status: `PREREGISTERED_MODEL_SCALE_FREEZE_PENDING_REVIEW`

g9 freezes production-scale bounds and final owner submissions before any discovery target is created. It inherits g1-g8 without modification except where this overlay adds explicit scale constraints.

## Final submissions

- Terra tree: `301bf46567b8049ea27b0dae60bca99c7e2e563382348dbe8c921e9d0107fd09`
- Opus MoE: `2215eb5e50bd911a75225ab0c82e39d7b024ab1e087de43c2825c6b838fc070f`
- Sonnet episodic: `ecb5ede85a0895dca0784d4450db445b2312ccb4f8c265f1360a27bf2e1d2c67`
- Grok change-point: `b2776efd27fe79407e5ab4021131b2f3f05413f35eefbfe42f4eaf21702c4c52`

Every submission declares `labels_read=false`.

## Frozen scale bounds

- Tree: at most 8,192 fit rows and 31 quantile thresholds per feature/node.
- MoE: at most 8,192 fit rows and 31 quantile thresholds per feature/gate.
- Episodic: at most 8,192 candidate rows and 1,024 LOO queries.
- Change-point: all 100,000 rows through bounded linear scans.

Tree, MoE and episodic cap membership uses only seed-bound example/event identity. Features, account state, selected target and reward cannot affect membership. Input permutation and target mutation therefore cannot change the capped subset.

The neutral runner passes the same 100,000 discovery examples to all owners. Internal caps are frozen model hyperparameters, not owner-specific input datasets.

## Freeze rule

After the discovery label bundle is created:

- no owner code or grid may change;
- no submission hash may change;
- any change requires a new protocol generation and invalidates downstream tournament output.

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
