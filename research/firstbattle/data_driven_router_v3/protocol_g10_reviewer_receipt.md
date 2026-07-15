# Data-Driven Router V3 g10 — Independent Reviewer Receipt

> Document hash: `a81adde5e99a7cd266b7fb9671ce9063d91e215cb6f284bf94cb0104de3d17d9`
> Receipt hash: `9de865f7620f0252b22ed8bf53f1a6e008e9df5a021ac8e4564baff2899fc6b8`
> Reviews: `protocol_g10.{json,md}` (counterfactual account semantics preregistration), with a byte-level binding cross-check of `protocol_g9.json` / `protocol_g9_reviewer_receipt.json` and an inheritance-hash cross-reference of `protocol_g6/g7/g8.json`
> Reviewed repo HEAD: `ab2780405f0fd5c80d268fe0feff39eb14a29eda`
> Verdict: **`APPROVED`** (`PASS_INDEPENDENT_VERIFICATION_G10_COUNTERFACTUAL_ACCOUNT_SEMANTICS`)

## Reviewer separation

This receipt is authored by an independent **writable** reviewer subagent (Claude Sonnet 5), a session distinct both from the governance-owner agent that wrote `protocol_g10.{json,md}` and from a prior **read-only** reviewer session that separately judged this same g10 generation `APPROVED`. This receipt does not import or trust that predecessor's stated verdict; every hash and binding below was independently recomputed from on-disk bytes by this session's own commands before this receipt reached its own conclusion.

Allowed write set for this review: only this file and `protocol_g10_reviewer_receipt.json`, both new, both under `research/firstbattle/data_driven_router_v3/`. This receipt is a purely external, additive attestation: it does not modify `protocol_g10.{json,md}` in any way — that artifact's bytes, its `PREREGISTERED_COUNTERFACTUAL_ACCOUNT_SEMANTICS_PENDING_REVIEW` status, and its self-declared hashes remain exactly as before this review. Nothing else was modified: no other protocol/overlay generation, no `model_submission_*.json`, no plan file, no code, no `records/`, no private-raw, no exam/real-money surface. No git commit was made. Reward/discovery labels remain uncreated; `system_status` remains `NO_GO`.

**Raw/outcome access: none.** This review read only `protocol_g10.{json,md}`, `protocol_g9.json` and `protocol_g9_reviewer_receipt.{json,md}` (byte-level binding cross-check and methodology precedent), `protocol_g6/g7/g8.json` (inheritance-hash cross-reference only), `internal/datatruth/envelope.go` (hashing algorithm confirmation), and git metadata. No dataset shard, StateRow, reward-cube, `records/` file, private-raw file, or exam/sealed surface was opened; none exists to be built under this generation (`discovery_label_bundle_created=false`).

## Methods

1. **M1 — g10 JSON self-excluding hash recompute.** A temporary Go program (built under a transient `cmd/` directory, deleted immediately after use, never committed) nulled `protocol_g10.json`'s own `hashes.overlay_hash` and called the repository's `internal/datatruth.HashJSON`. Recomputed value **exactly matches** the declared `0d09d595...263e7cb`. The same tool independently reproduced `protocol_g9.json`'s `hashes.overlay_hash` and `protocol_g9_reviewer_receipt.json`'s `hashes.receipt_hash` against their own declared values, for use in M3.
2. **M2 — g10 Markdown document hash recompute.** Normalized the `> Document hash:` line to `SELF_EXCLUDED` and recomputed SHA-256 over UTF-8 bytes with a standalone script. Recomputed value **exactly matches** the declared `c08db9de...02e4653`.
3. **M3 — g9 receipt binding cross-check.** Compared `protocol_g10.json`'s `supersedes.overlay_hash`, `supersedes.reviewer_receipt_hash`, and `inheritance.g9_hash`, plus `protocol_g10.md`'s `> g9 reviewer receipt:` header line, against the independently recomputed self-hashes of `protocol_g9.json` and `protocol_g9_reviewer_receipt.json` (M1) rather than trusting g10's restatement at face value. **All bindings match exactly**: g10's cited g9 overlay hash and inheritance `g9_hash` both equal `protocol_g9.json`'s own reverified `fe3ce29e...2802c`; g10's cited g9 reviewer-receipt hash equals `protocol_g9_reviewer_receipt.json`'s own reverified `e77d1511...9c953`.
4. **M4 — g1-g8 inheritance chain unchanged check.** Compared all eight of `protocol_g10.json.inheritance.{g1_hash..g8_hash}` field-by-field against `protocol_g9.json.inheritance`'s own block. **Byte-identical across all eight entries**; g10 adds only a new, independently-verified `g9_hash` and does not silently alter any earlier-generation citation.
5. **M5 — Safety and git-state check.** Confirmed `git rev-parse HEAD` matches the reviewed commit, that this review added only its own two new files, and that `g10.safety` (`discovery_label_bundle_created=false`, `routes_started=0`, `NO_GO`) holds in fact.

## Findings

- **g10 self-hashes and g9 receipt binding both reproduced with zero discrepancies.** The overlay hash and the document hash both match exactly; g10's binding to the g9 overlay and to the g9 independent-reviewer `APPROVED` receipt is verified against on-disk bytes, not merely restated.
- **g1-g8 inheritance chain restated without alteration.** All eight inherited hashes are byte-identical to `protocol_g9.json`'s own citation of the same generations.

No numeric, hash, or logical discrepancy was found anywhere in the reviewed material.

## Approved / not approved

**Approved:** `protocol_g10.{json,md}` as the current effective counterfactual-account-semantics preregistration overlay on the independently-corroborated g1-g9 chain; the g9 reviewer-receipt binding (`supersedes.overlay_hash`, `supersedes.reviewer_receipt_hash`, `inheritance.g9_hash`) as an exact, byte-verified reference to `protocol_g9.json` and its independently `APPROVED` `protocol_g9_reviewer_receipt.json`; the unaltered restatement of the g1-g8 inheritance chain.

**Not approved:** any dataset construction, StateRow/reward-cube materialization, discovery label bundle creation, or label unseal (none has occurred); a substantive re-review of the counterfactual account-semantics formulas themselves (capital tier assignment, canonical context initialization, dynamic execution cost, counterfactual timeline/objective) beyond hash/binding integrity (out of this receipt's assigned scope); `protocol_g10.json`'s own `PREREGISTERED_COUNTERFACTUAL_ACCOUNT_SEMANTICS_PENDING_REVIEW` status field is unchanged at the byte level by this receipt, which is an external, additive approval attestation only; any tournament run, validation/WF/router-holdout replay, exam action, shadow trading, or real-money action; any claim that this receipt predicts market behavior or authorizes live/exam/shadow/real-money operation; a full re-execution of `gofmt`/`go_vet`/`go_test`/`lint` against owner code (this receipt's scope is limited to the g10 hashes, the g9 receipt binding, and the g1-g8 inheritance chain per its assigned task).

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
