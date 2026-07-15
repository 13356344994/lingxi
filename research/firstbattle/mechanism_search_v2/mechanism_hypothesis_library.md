# Mechanism Hypothesis Library (机制假设库) — Independent Sonnet 5 Reviewer

> Document hash: `8f6511879c12f6306170af71c387f7ecc8c6a873219d480023a021b501544937`

Binds `mechanism_hypothesis_library.json` (`hashes.library_hash`). Produced by an independent reviewer subagent, distinct from the Opus/Fable/Grok owners who authored `candidates_opus.json`, `candidates_fable.json`, `candidates_grok.json`. Read-only: added only the three new artifact pairs under `research/firstbattle/mechanism_search_v2/`; did not modify any candidate/plan/master/code/records/private-raw/G1-G10 artifact; made no git commits; read no pnl/return/winner/mfe/mae/future_outcome/exam field.

## 1. Scope and inputs

19 outcome-blind candidates (6 Opus + 6 Fable + 7 Grok) bound to `route_data_contracts.json`, `route_data_source_registry.json`, `history_catalog.json`, `p0_swap_data_fitness_reviewer_receipt.json`, and `data_mainline_closure_v1.json`. The original review found that `candidates_fable.json`'s then-declared self-hash did not reproduce; the Fable owner corrected only that hash to `a244b53c...f97cea`, exactly this reviewer's prior independent recompute. A fresh standalone Go recomputation now matches, as does the corrected Markdown hash `879d93eb...703a9`. Status: **`RESOLVED_OWNER_HASH_CORRECTED`**. Because the corrected JSON canonical hash equals the prior independent recompute, mechanism content is proven unchanged apart from the self-hash field; all candidate scores, clusters, and shortlist decisions remain unchanged.

The frozen 30/25/20/15/10 scoring rubric and the 8-item hard-reject list are declared identically inside all three owner files before this reviewer compared them, satisfying "freeze scoring/hard-rejects before judging content."

## 2. Independent feature-side re-verification highlights

- Confirmed via direct file reads (not owner narration): 401 `okx_1h` files, 401 `okx_funding` files, 200 `okx_5m` files; AAPL/SPY/TSLA/GME date windows match cited day counts; BTC funding schema/settlement-stamp identity clean; H/LAB/RAVE/ENJ funding pin runs present but too few for the 50-event gate.
- **New finding not in any owner file**: `records/history/rubik_lsratio_1h` (hourly long/short-ratio tape) exists locally and was missed by Opus's OI-imbalance existence scan.
- **Cross-owner catch**: Fable's own decision journal independently hard-rejects "Rubik long/short-ratio extreme" as a pure public factor — exactly the data family OPUS-01 would need to fill its decision-time crowding gap. Neither owner could see this alone; it is decisive against shortlisting OPUS-01.
- **New finding**: zero local trading-calendar/holiday/DST artifacts exist anywhere in the repo; the `okx_1h` universe spans multiple distinct underlying-market calendars (US equities/ETFs, foreign single stocks/country ETFs, commodities, private-company perps with no market at all) rather than one shared "121 symbols, one shock" cluster as FAB-01 implied.
- Independently ran FAB-01's own falsification cut #1 (does the perp move during the underlying's closed window) on a sampled Saturday for AAPL: **passed** — continuous, non-frozen hourly price/volume.
- G2's frozen protocol (grepped from `FIRST_BATTLE_MASTER_PLAN.md` §4.2, read-only) confirmed as a 162-combination BTC-only funding-state grid with 0 nominations; used to grade isomorphism risk objectively rather than trusting each owner's self-declared non-isomorphism proof.

## 3. Clustering (12 clusters over 19 candidates)

| Cluster | Members | Relationship |
|---|---|---|
| Funding settlement clock & clamp family | OPUS-01, OPUS-06, FAB-04, GROK-01 | Sibling family |
| Cross-venue funding phase offset | FAB-06 | Adjacent to above + high G1 isomorphism risk |
| Borrowing interest settlement | OPUS-03, GROK-02 | Sibling/duplicate |
| Contract lifecycle terminal convergence | OPUS-02, FAB-03 | Sibling family |
| Listing/delisting/migration lifecycle | FAB-02, GROK-03 | Sibling, opposite lifecycle ends |
| Collateral/authorization restriction | GROK-05 | Independent |
| Forced liquidation/ADL/mark-index | OPUS-05, GROK-04 | Isomorphic to existing frozen route `sparse_forced_flow` |
| Rule discreteness abstention | GROK-06 | Isomorphic to existing frozen route `exchange_rule_discontinuity` |
| Cross-venue ops asymmetry | GROK-07 | Duplicate of existing frozen route `cross_venue_operational_asymmetry` |
| **Equity perp session/calendar gap** | **FAB-01** | **Independent — sole shortlisted** |
| Copy-trade authorized mirror flow | OPUS-04 | Independent |
| Vesting cliff statutory supply | FAB-05 | Independent |

## 4. Verdicts

- **Shortlisted (conditional, rescoped): 1** — FAB-MSV2-01.
- **Retain hypothesis, not shortlisted: 2** — MSV2-OPUS-01 (decision-feature denominator incomplete / public-factor risk via the only dense proxy), FAB-MSV2-06 (owner's own highest G1-isomorphism risk, weakest moat).
- **Rejected, insufficient support: 1** — FAB-MSV2-04 (measured ~a dozen qualifying events vs. the 50-event gate).
- **Rejected, payer not directly observable: 1** — FAB-MSV2-02 (symmetric untested payer hypothesis; owner's own strongest counterexample is generic listing drift).
- **Hard-rejected: 14** — MSV2-OPUS-02/03/04/05/06, FAB-MSV2-03/05, GROK-MSV2-01/02/03/04/05/06/07, predominantly on `COMPLETE_DENOMINATOR_NOT_CONSTRUCTIBLE` and/or `PIT_BACKFILL` and/or existing-route/G-series homology. Full per-candidate reasoning, hard-reject screen, and reviewer-assigned scores (not owner self-scores) are in the JSON.

See `mechanism_shortlist.json`/`.md` for the single conditional shortlist entry and `mechanism_shortlist_reviewer_receipt.json`/`.md` for methods, conflict matrix, and approved scope.

## 5. Hash and verification methods

- **JSON**: `hashes.library_hash` set to null, then `datatruth.HashJSON` (deterministic Go `encoding/json` + SHA-256 lowercase hex).
- **Markdown**: normalize the single `> Document hash:` line above to `` `SELF_EXCLUDED` ``, then SHA-256 over UTF-8 LF bytes.

## 6. Stop decision

Library frozen; proceeds to the shortlist and receipt in the same delivery, then stops for user approval. Does not proceed to any data-contract freeze, acquisition beyond what is already local, generation preregistration, validation/WF, exam, shadow, or real-money action.
