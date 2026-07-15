# Data-Driven Router V3 g7 Purged Dataset Binding

> Document hash: `92c09f9c8cec70f1c4a9665abc570483c880a7f1e944b60ee0e3fbd7a9d289d4`
> Overlay hash: `4aacc43e928d25b6b83cdf8fe10d5b576e2b1009ea10b7bfad3e750388e47747`
> g6 hash: `08b609e3271029b1e9f772921e325ef7c7fd30326589ae9c03cc86d73d9c844b`
> Generation: `firstbattle-data-driven-router-v3-g7`
> Status: `PREREGISTERED_REWARD_LABELS_STILL_SEALED_PENDING_INDEPENDENT_REVIEW`

g7 preserves the g6 account-state protocol but replaces its blocked V2 market-dataset binding.

## Effective market dataset

```text
path: research/firstbattle/data_driven_router_v3/dataset_state_okx_1h_v3
manifest hash: f9dc0944919709a49f765fe3291cf60a46952919f796add47d892af5f2808a6d
rows: 3,996,040
shards: 34
```

The three partition transitions have 50-hour gaps, satisfying the frozen ±24h global purge. Manifest self-hash and shard-row sum are verified.

V1 remains `STALLED_UNTRUSTED`; V2 remains `BLOCKED_PURGE_MISSING`. Neither is a valid model input.

## Account state

g6 AccountContext/target-position transition semantics remain inherited. Synthetic fixtures pass without reading real StateRows or reward labels.

## Safety

```text
market_state_layer=APPROVED_PURGED_V3
reward_labels=SEALED_NOT_READ_OR_CREATED
routes_started=0
NO_GO
```

## Hash

JSON: set `hashes.overlay_hash` to `null`, then run repository `datatruth.HashJSON`.

Markdown: replace the single `Document hash` value with `SELF_EXCLUDED`, then SHA-256 the UTF-8 LF bytes.
