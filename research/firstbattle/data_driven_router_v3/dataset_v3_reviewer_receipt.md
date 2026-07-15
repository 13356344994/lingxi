# Dataset V3 Independent Reviewer Receipt

> Document hash: `f7cb62b7bc53841768b808beecf03ec6759620837b3aefe4cd5c9d10873aef76`
> JSON receipt hash: `5fbb51bf0bab3be2649b57ba76ed68b2f714cda8a5fc7c0722a1a53f4c52edfb`
> Verdict: `APPROVED`

## Approved scope

Approval is hash-bound to g7 overlay `4aacc43e...e47747`, dataset V3 manifest `f9dc0944...2808a6d`, 3,996,040 declared rows, 34 shards, source binding `40895510...9960405`, and the PASS account fixture receipt. This does not authorize label unsealing, route execution, exam, shadow, or real-money trading.

## Methods

- Recomputed g7 JSON and Markdown self-hashes and checked the g6 inheritance/dataset-binding override.
- Recomputed the manifest hash using the builder's pre-insertion method; checked receipt, COMPLETE checkpoint, sealed-shard equality, and source binding.
- Stream-hashed all 34 compressed shard files, decompressed each fully for gzip integrity, matched every shard receipt to its manifest entry, and sampled each shard's first/last `decision_ms`. No new 4M-row semantic rescan was performed.
- Summed declared shard rows and derived all three partition gaps from manifest extrema.
- Checked V1/V2 blocked status, sealed reward/safety flags, and account fixture PASS declarations.

## Findings

- g7 JSON hash: PASS (`4aacc43e928d25b6b83cdf8fe10d5b576e2b1009ea10b7bfad3e750388e47747`).
- g7 Markdown hash: PASS (`92c09f9c8cec70f1c4a9665abc570483c880a7f1e944b60ee0e3fbd7a9d289d4`).
- Manifest self-hash: PASS (`f9dc0944919709a49f765fe3291cf60a46952919f796add47d892af5f2808a6d`).
- Receipt/checkpoint/source binding: PASS.
- 34/34 shard SHA-256, gzip, receipt, and first/last samples: PASS; declared row sum = 3,996,040.
- Partition gaps: 50h / 50h / 50h; each satisfies the frozen ±24h purge.
- V1 `STALLED_UNTRUSTED`; V2 `BLOCKED_PURGE_MISSING`; neither is a valid model input.
- Reward remains `SEALED_NOT_READ_OR_CREATED`; labels remain blinded; system remains `NO_GO`.
- Account fixture receipt: PASS, declaring no real StateRows or sealed labels read.

## Verdict

`APPROVED` within the exact hash-bound scope above.

## Self-hash

Normalize the single `Document hash` value to `SELF_EXCLUDED`, then SHA-256 the UTF-8 LF bytes.
