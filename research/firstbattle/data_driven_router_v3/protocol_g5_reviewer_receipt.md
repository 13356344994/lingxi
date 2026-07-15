# Data-Driven Router V3 g5 — Independent Reviewer Receipt

> Document hash: `422d6c3de4704ed0ced24226343b50a8f64bb5d76876dbb734d8fe1f32584e8c`
> Receipt hash: `63e7f6c68aa2197dcbeebd7dacfeb96c9c5cd0c1425cfa0b02243bbe4b7d85b8`
> Reviews: `supersession.{json,md}`, `protocol.{json,md}` (g1), `protocol_g2.{json,md}`, `protocol_g3.{json,md}`, `protocol_g4.{json,md}`, `protocol_g5.{json,md}`, and Master `FIRST_BATTLE_MASTER_PLAN.md` §5.14-5.18 (citation cross-check only)
> Reviewed repo HEAD: `ab2780405f0fd5c80d268fe0feff39eb14a29eda`
> Verdict: **`APPROVED`** (`PASS_INDEPENDENT_VERIFICATION_G5_DETERMINISTIC_MECHANICS_OVERLAY`)

## Reviewer separation

This receipt is authored by an independent reviewer subagent (Claude Sonnet 5), a session distinct from the governance-owner agent that wrote `supersession.{json,md}` and `protocol{,_g2,_g3,_g4,_g5}.{json,md}`. Allowed write set for this review: only this file and `protocol_g5_reviewer_receipt.json`, both new, both under `research/firstbattle/data_driven_router_v3/`. Nothing else was modified: no plan file, no g1-g5 artifact, no supersession file, no code, no `records/`, no private-raw, no exam/real-money surface. No git commit was made.

**Raw/outcome access: none.** This review read only the 12 reviewed JSON/MD files, Master §1 and §5.14-5.18 (read-only), `internal/datatruth/envelope.go` (to confirm the hashing algorithm before calling it), git metadata, and — for corroboration only — the self-excluded hash fields of five upstream `mechanism_search_v2`/data-mainline/P0 artifacts plus the plain `.data_hash` field of `history_catalog.json`. No `pnl`/`fillPnl`/`realized_pnl`/`return`/`winner`/outcome field, no `records/` file, no private-raw file, and no exam/sealed surface was opened. No dataset, StateRow, or reward-cube artifact exists anywhere in the repository at review time; none was read because none exists.

## Methods

1. **M1 — Self-excluding JSON hash recompute.** A temporary Go program (built under a transient `cmd/` directory, deleted immediately after use, never committed) nulled each artifact's own `hashes.self_excluded_field` and called the repository's `internal/datatruth.HashJSON`. All **6/6** JSON self-hashes (supersession + g1-g5) reproduced exactly.
2. **M2 — Markdown document hash recompute.** Normalized each `> Document hash:` line to `SELF_EXCLUDED` and recomputed SHA-256 over UTF-8 bytes with a standalone script. All **6/6** document hashes reproduced exactly.
3. **M3 — Inheritance chain cross-reference.** Checked that every `inheritance`/`supersedes` citation in g2→g5 points at the *independently recomputed* hash of the file it claims (not merely the file's own self-declared value), and that the cited-hash set grows by exactly one per generation with no dropped or altered prior citation. All four supersession edges (g1→g2→g3→g4→g5) check out.
4. **M4 — Master-plan citation cross-check.** Compared Master §5.14-5.18's quoted hashes against this reviewer's own M1/M2 recomputes (not the artifacts' self-declared values) and confirmed the supreme objective is byte-identical across Master §1, `protocol.json/.md`, and `supersession.json/.md`.
5. **M5 — Upstream binding spot-check.** Re-derived 5 of the hashes `supersession.json`/`protocol.json` cite from `mechanism_search_v2`, `data_mainline_closure_v1`, and the P0 reviewer receipt, corroborating rather than trusting the owner's own reverification claims. 5/5 matched.
6. **M6 — Mechanics, safety-gate, and git-state review.** Read every g5 mechanism against the six properties this review was commissioned to check, cross-checked git HEAD/status, and confirmed no dataset artifact exists yet.

## Findings

- **All hash and inheritance claims independently reproduced, zero discrepancies** (F7): 6/6 JSON self-hashes, 6/6 Markdown document hashes, 4/4 inheritance edges, 5/5 Master citations.
- **Deterministic withdrawal reserve (F1):** `leverage_reserve = total_abs_notional/5` is the algebraic inverse of the 5× ceiling itself, not an arbitrary buffer. `required_reserve = max(leverage_reserve, maintenance_reserve)` is correct — both are independent floors on required remaining equity, so the binding one is the larger, not their sum. `floor_to_micro_usdt` rounds only in the safe (never-over-withdraw) direction.
- **Unknown MMR (F2):** blocks the withdrawal to zero rather than assuming a zero margin rate — fails closed, correctly.
- **Synthetic-override scope (F3):** g5 names exactly four g2 fields it overrides; it does not touch g2's cost-coverage gate or g1's core state/action/reward definitions.
- **Real hard gates (F4):** all six required gates (historical equity/liquidation, 5× leverage, known cost or frozen proxy, actual-time maintenance margin, no negative cash, PIT/time isolation) are present verbatim and corroborated by unmodified upstream fields.
- **Baseline-neutral evaluator (F5):** the 14-field shared-identity condition with `action_policy` as the only allowed difference is sufficient for an apples-to-apples comparison, and is hash-manifest-checkable, not a bare promise.
- **Capital withdrawal rules / 5×/ no negative cash (F6):** g3→g4→g5 is a strict, non-reversing refinement; the 1.5×/0.5× trigger/tranche, no-topup, and ruin-zeroes-but-withdrawals-survive rules are byte-unchanged since g3; 5× and no-negative-cash are never weakened.
- **Out-of-scope preflight tool observed running mid-review (F8, LOW, not blocking):** `cmd/data-driven-v3-dataset/main.go` and `internal/datadrivenv3/` appeared as untracked files during this review and were then actually executed by a separate process, producing `dataset_data_fitness.{json,md}` and a decision journal. Its result: `status=DATA_BLIND_COST`, `highest_blocker="known cost coverage 0.8750 < 0.95"`, `labels_unblinded=false`. Because status ≠ `PASS`, the tool's own code never reaches dataset/state construction — it failed closed exactly as designed, and independently corroborates this reviewer's own reading of g2's 0.95 cost-coverage threshold. This receipt does not review or approve that preflight code's correctness; it only records that no dataset was built and no label was unblinded during this review.

No numeric, hash, logical, or scope-leak defect was found anywhere in the reviewed material.

## Approved / not approved

**Approved:** the g5 overlay as the current effective mechanics layer on the verified g1-g4 chain; its deterministic withdrawal reserve, unknown-MMR rule, synthetic-override scope, six hard gates, and baseline-identity condition; the `supersession.{json,md}` isolation boundary and its explicit non-adjudication of the disputed `equity_session_gap_data_contract` hash; Master §5.14-5.18's citations of all five generations.

**Not approved:** any dataset construction, StateRow/reward-cube build, or label unseal (the one preflight run observed mid-review returned `DATA_BLIND_COST`, not `PASS`); the preflight code or its cost-coverage measurement (observed running mid-review, not reviewed for correctness); any model-owner submission, tournament run, validation/WF/router-holdout replay, exam action, shadow trading, or real-money action; any claim that this receipt predicts market behavior or authorizes live/exam/shadow/real-money operation; resolution of the disputed contract hash in either direction.

## Safety

```text
system_status=NO_GO
exam_status=EXAM_UNAVAILABLE
trade_allowed=false
labels_unblinded=false
dataset_built=false
routes_started=0
routes_reopened=0
reviewer_made_commits=false
reviewer_modified_owner_artifacts=false
reviewer_read_raw_or_outcome_values=false
```

## Hash

JSON: set `hashes.receipt_hash` to `null`, then run repository `datatruth.HashJSON`.

Markdown: replace the single `Document hash` value with `SELF_EXCLUDED`, then SHA-256 the UTF-8 LF bytes.
