# Data-Driven Router V3 g9 — Independent Reviewer Receipt

> Document hash: `55b794f068bd720ceda7c57591c60977d4d2a608a42d38ed7bf6fcbe205744d6`
> Receipt hash: `e77d1511d022fb511165f3412535ff81a6149cb5a0da7729044c5afb9069c953`
> Reviews: `protocol_g9.{json,md}` (production-scale freeze), with SHA-256 cross-checks of the four `model_submission_*.json` files and inheritance-hash cross-references of `protocol_g6/g7/g8.json`
> Reviewed repo HEAD: `ab2780405f0fd5c80d268fe0feff39eb14a29eda`
> Verdict: **`APPROVED`** (`PASS_INDEPENDENT_VERIFICATION_G9_PRODUCTION_SCALE_FREEZE`)

## Reviewer separation

This receipt is authored by an independent **writable** reviewer subagent (Claude Sonnet 5), a session distinct both from the governance-owner agent that wrote `protocol_g9.{json,md}` and from a prior **read-only** reviewer session that separately judged this same g9 generation `APPROVED`. This receipt does not import or trust that predecessor's stated verdict; every hash and constant below was independently recomputed from on-disk bytes by this session's own commands before this receipt reached its own conclusion.

Allowed write set for this review: only this file and `protocol_g9_reviewer_receipt.json`, both new, both under `research/firstbattle/data_driven_router_v3/`. Nothing else was modified: no other protocol/overlay generation, no `model_submission_*.json`, no plan file, no code, no `records/`, no private-raw, no exam/real-money surface. No git commit was made.

**Raw/outcome access: none.** This review read only `protocol_g9.{json,md}`, the four `model_submission_*.json` files (whole-file SHA-256 only), `protocol_g6/g7/g8.json` (inheritance-hash cross-reference only), `protocol_g5_reviewer_receipt.{json,md}` (methodology precedent only), `internal/datatruth/envelope.go` (hashing algorithm confirmation), and git metadata. No dataset shard, StateRow, reward-cube, `records/` file, private-raw file, or exam/sealed surface was opened; none exists to be built under this generation (`discovery_label_bundle_created=false`).

## Methods

1. **M1 — g9 JSON self-excluding hash recompute.** A temporary Go program (built under a transient `cmd/` directory, deleted immediately after use, never committed) nulled `protocol_g9.json`'s own `hashes.overlay_hash` and called the repository's `internal/datatruth.HashJSON`. Recomputed value **exactly matches** the declared `fe3ce29e...da682`. The same tool cross-referenced `protocol_g6/g7/g8.json`'s own self-hashes, each of which exactly matches both its own file and the corresponding `g9.inheritance`/`g9.supersedes` citation.
2. **M2 — g9 Markdown document hash recompute.** Normalized the `> Document hash:` line to `SELF_EXCLUDED` and recomputed SHA-256 over UTF-8 bytes with a standalone script. Recomputed value **exactly matches** the declared `faa8cf94...4fe76e`.
3. **M3 — Four submission SHA-256 recompute.** Ran `sha256sum` directly against the raw bytes of `model_submission_{terra,opus,episodic_sonnet,grok}.json`. **4/4 exact matches** against `g9.final_owner_submissions.{terra_tree,opus_moe,sonnet_episodic,grok_changepoint}_sha256`.
4. **M4 — Cap-constant cross-check.** Compared every numeric cap in `g9.production_scale_bounds` (8,192 fit/candidate rows, 31 quantile thresholds, 1,024 LOO queries) against the same constant already frozen inside each owner's own `model_submission` file. Every cap is an exact restatement, not a new or looser value introduced only at g9.
5. **M5 — Label-blind identity-key check.** Confirmed each frozen-subset owner's (terra, opus, sonnet) cap-membership selection key uses only a fixed seed plus `ExampleID`/`EventID`/`DecisionMS`-or-neighbor-id, explicitly excluding `features`, `account_context`, `selected_target_intent`, and `reward` — matching each owner's own anti-leak/order-independence claims.
6. **M6 — Safety and git-state check.** Confirmed `git rev-parse HEAD` matches the reviewed commit, that this review added only its own two new files, and that `g9.safety` (`discovery_label_bundle_created=false`, `routes_started=0`, `NO_GO`) holds in fact.

## Findings

- **Both g9 self-hashes and all four submission SHAs reproduced with zero discrepancies.** The overlay hash, the document hash, and all four `final_owner_submissions` SHA-256 values match exactly; the g6/g7/g8 hashes g9 cites also independently reproduce, so g9's upstream chain is corroborated, not merely copied.
- **Cap constants and label-blind identity keys match owner submissions exactly, with no silent loosening.** Every cap in `production_scale_bounds` restates an already-frozen owner constant; all three frozen-subset owners select their capped rows using only seed-bound identity fields, structurally excluding features, account state, target, and reward, so target mutation and input permutation provably cannot change cap membership.

No numeric, hash, or logical discrepancy was found anywhere in the reviewed material.

## Approved / not approved

**Approved:** `protocol_g9.{json,md}` as the current effective production-scale-freeze overlay on the independently-corroborated g1-g8 chain; the four `final_owner_submissions` SHA-256 bindings as byte-exact references to the on-disk model files; the frozen `production_scale_bounds` as unweakened restatements of each owner's own constants; the label-blind cap-membership identity keys for terra/opus/sonnet.

**Not approved:** any dataset construction, StateRow/reward-cube materialization, discovery label bundle creation, or label unseal (none has occurred); the internal correctness of the four `model_submission_*.json` files beyond their byte identity (out of this receipt's assigned scope); any tournament run, validation/WF/router-holdout replay, exam action, shadow trading, or real-money action; any claim that this receipt predicts market behavior or authorizes live/exam/shadow/real-money operation; a full re-execution of `gofmt`/`go_vet`/`go_test`/`lint` against owner code (this receipt's scope is limited to the two g9 hashes, the four submission SHAs, cap constants, and label-blind identity keys per its assigned task).

## Safety

```text
system_status=NO_GO
exam_status=EXAM_UNAVAILABLE
trade_allowed=false
labels_unblinded=false
discovery_label_bundle_created=false
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
