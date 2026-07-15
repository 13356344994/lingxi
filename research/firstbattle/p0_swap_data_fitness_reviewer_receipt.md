# P0 SWAP Data Fitness — Independent Reviewer Receipt

> Reviews artifact: `firstbattle.p0-swap-data-fitness` (generation `firstbattle-p0-swap-data-fitness-v1`)
> Reviewer: independent agent instance, Claude Sonnet 5, workspace `/root/lingxi`
> Reviewed at: `2026-07-14T03:17:36Z`, repo HEAD `ab2780405f0fd5c80d268fe0feff39eb14a29eda`
> Receipt hash: `0cdd322c130a7c6e58bd9803e5611f6a144d35bf6e5a3f0cdac49292ad9dcad4`
> Verdict: `PASS_INDEPENDENT_VERIFICATION_DATA_FITNESS_CLASSIFICATION`
> Document hash: `567cbaea088027a4a521e6ecfa11fdd28fb1b3ac149896e77f13042d031e7bbc`

## 1. Why this file exists

`p0_swap_data_fitness.json`/`.md` already contain an owner-authored
`independent_redteam` block claiming a prior Sonnet review passed the
artifact. That block was written inside the owner's own file, by the same
lineage of agents that produced the measurement — it is a self-attestation,
not independent evidence. `FIRST_BATTLE_MASTER_PLAN.md` §5.11 still labels
the artifact "owner产物（待独立红队）" (owner artifact, pending independent
red-team), which is inconsistent with the artifact's own
`REDTEAM_PASS_DATA_FITNESS_CLASSIFICATION_STOPPED` status. This receipt is
the first artifact in the lineage produced by a genuinely separate reviewer
identity, with write access restricted to new reviewer-receipt files only,
that independently re-derives the numbers instead of trusting the owner's
narrative.

## 2. Scope of this review

Read-only over:

- `research/firstbattle/p0_swap_data_fitness_measurement.json`
- `research/firstbattle/p0_swap_data_fitness.json` / `.md`
- `research/firstbattle/route_data_contracts.json` (and its `.md`)
- `research/firstbattle/route_data_source_registry.json` / `.md`
- `internal/historydownload/datafitness/*.go`, `internal/historydownload/OWNER_JOURNAL.md`
- External orders/fills manifests and tokenized projection pages under
  `/root/.lingxi-private/historydownload/api/v5/trade/{orders-history-archive,fills-history}/SWAP`
- External public instruments snapshot under `/root/.lingxi-data/historydownload/instruments`

Never opened: `private-raw/`, `raw/`, `records/`, any exam or real-money
control surface, or any `pnl`/`fillPnl`/outcome field (the orders/fills
projection schemas contain none, and this was independently confirmed by a
full-row forbidden-key scan, see §4).

No file outside this receipt (and its `.json` twin) was created or modified.
No commit was made.

## 3. Independent methods used

1. **Fresh CLI recompute (M1).** Built the repo (`go build ./...`), ran
   `go test ./internal/historydownload/...` (all packages `ok`, including
   `datafitness` in 9.63s), then ran `go run ./cmd/p0-swap-data-fitness` with
   stdout output only (no file write) to force a fresh read of every
   manifest, projection page, and instrument snapshot. The freshly produced
   measurement JSON was deep-equality compared against the committed
   `p0_swap_data_fitness_measurement.json`: **identical**, including
   `measurement_receipt_hash` and both projection aggregate hashes.
2. **Clean-room Python reimplementation (M2).** A standalone script outside
   the repository (not committed) that imports no `lingxi` code independently
   recomputed the projection aggregate hash algorithm, all order/fill counts,
   join statistics, the UTC-day/top-two-day share, cross-endpoint instId
   conflicts, current-instrument field presence, and traded-vs-snapshot
   missing sets, directly from the raw manifest and projection page bytes.
   Every value matched the frozen artifact exactly (see JSON `independent_methods.M2_...result`
   for the full value list).
3. **Self-excluding hash recompute (M3).** For every frozen JSON/MD pair
   (`p0_swap_data_fitness`, `route_data_contracts`, `route_data_source_registry`),
   nulled the declared self-excluded hash field and recomputed it via the
   repository's own `internal/datatruth.HashJSON` (for JSON) or the
   documented `SELF_EXCLUDED` line-normalization + SHA-256 (for Markdown).
   All declared hashes matched recomputed hashes.
4. **Manifest clock/denominator field audit (M4).** Directly read
   `event_time_range`/`receive_time_range`/`clock_fitness`/`denominator_fitness`/
   `pit_status` from the external orders/fills `manifest.json` files and
   confirmed they match `p0_swap_data_fitness.json`'s `time_ranges` and
   corroborate `earliest_blockers`/`fitness_matrix` (original ReceiveTime
   absent, incomplete API-slice denominator, no `decision_id`/ack-time field
   in either schema, order states limited to `{filled, canceled}` with zero
   no-fill rows).
5. **Master-plan drift check (M5).** Confirmed the `FIRST_BATTLE_MASTER_PLAN.md`
   §5.11 heading/artifact status inconsistency described in §1, without
   editing the plan.

## 4. Key independently reproduced facts

```text
orders: 13,622 records / 13,622 distinct tokens / 0 duplicates
  states: filled=13,566, canceled=56
  canceled with accFillSz>0: 56; canceled with accFillSz=0: 0
  no-fill orders: 0
  ord types: ioc=7,228 / post_only=6,393 / market=1
  clOrdId missing: 1; source non-empty: 0

fills: 15,676 records / 15,676 distinct trade+bill tokens / 0 duplicates
  exec types: T=9,081 / M=6,595
  fills per order: single=12,212 / multi=1,410 / max=40
  clOrdId missing: 1

join: matched=13,622 / unmatched=0

coverage: distinct UTC days=26; top-two-day share=81.74276905006607%

instruments (current snapshot): 420 rows; ctType/minSz/ctVal present=420/420/420
traded instIds: orders=144 / fills=144 / union=145 / present in snapshot=143
missing from snapshot: DATA-USDT-SWAP-OFF20260702-2, IP-USDT-SWAP

cross-endpoint identity drift: 168 orders / 199 fill rows
  orders instId=DATA-USDT-SWAP-OFF20260702-2 -> fills instId=IP-USDT-SWAP

projection aggregate hashes:
  orders  e2cad38f667a0895fcc7ad0d4961b5bf5b3d8d67d3dd3c5e931618cee877b6b3
  fills   36d3fd5e3e0fe267af94122e590da077fb49fa3b290d90268b488d9dc8fa2b2a

forbidden outcome-key scan across all 29,298 projection rows: 0 hits
```

Every one of these lines was independently recomputed by this reviewer and
matches the owner's artifact exactly. No numeric, hash, or field-presence
discrepancy was found anywhere in this review.

## 5. Findings

- **F1 (INFO):** All quantitative claims independently reproduced with zero
  discrepancies (§4).
- **F2 (INFO):** Outcome-blindness independently confirmed by a clean-room
  scan, not merely the calculator's internal firewall.
- **F3 (INFO):** Denominator/identity gaps (`INCOMPLETE_ATTEMPTS_DENOMINATOR`,
  `ORIGINAL_RECEIVE_TIME_ABSENT`, `DECISION_ID_ABSENT`,
  `INSTRUMENT_PIT_UNPROVEN`) are directly corroborated by the external
  manifests' own `clock_fitness`/`denominator_fitness`/`pit_status` fields and
  by the schema field lists, independent of the owner's prose.
- **F4 (MEDIUM, process):** The owner-authored `independent_redteam` block
  inside `p0_swap_data_fitness.json` is not, by itself, independent evidence,
  and `FIRST_BATTLE_MASTER_PLAN.md` §5.11 still says "pending independent
  red-team" while the artifact claims the opposite. This receipt resolves the
  evidentiary gap going forward; governance should update the plan reference
  accordingly (not done here, per scope).

## 6. Verdict and approved scope

`PASS_INDEPENDENT_VERIFICATION_DATA_FITNESS_CLASSIFICATION`.

The **only** approved scope is that current OKX SWAP orders/fills/instruments
data remain classified:

```text
root_context_ood:                    GATE_ACTIVE_FAIL_CLOSED
micro_execution_capacity_cliff:      DATA_BLIND
exchange_rule_discontinuity:         DATA_BLIND/PIT_UNPROVEN
sparse_forced_flow:                  DATA_BLIND
cross_venue_operational_asymmetry:   DATA_BLIND/WEAK_PROXY_ONLY
routes_reopened:                     0
```

**Not approved:** outcome/profitability research, validation/walk-forward,
exam issuance, shadow trading, real-money trading or any route reopening,
treating the 100% join as a complete attempts denominator, treating the
current instrument snapshot as historical PIT, or treating this receipt as
authorizing anything beyond the classification above.

## 7. Reproducibility

- `cd /root/lingxi && go build ./... && go test ./internal/historydownload/...`
- `cd /root/lingxi && go run ./cmd/p0-swap-data-fitness` (stdout only; diff
  against `research/firstbattle/p0_swap_data_fitness_measurement.json`)
- Recompute any frozen JSON self-hash via `internal/datatruth.HashJSON` per
  that file's own `hashes.hash_method`.
- Recompute any frozen Markdown document hash by normalizing the
  `> Document hash:` line to `SELF_EXCLUDED` and taking SHA-256 over UTF-8 LF
  bytes.

## 8. Hash recomputation

- JSON: set `hashes.receipt_hash` to `null` in
  `p0_swap_data_fitness_reviewer_receipt.json`, then compute
  `datatruth.HashJSON`.
- Markdown: normalize the single `> Document hash: \`<value>\`` line to
  `> Document hash: \`SELF_EXCLUDED\`` and SHA-256 the UTF-8 LF bytes of this
  file.
